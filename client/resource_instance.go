package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v5"
	"github.com/gorilla/websocket"
	incusClient "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/pkg/sftp"
)

// Reader wraps bytes.Reader to add a no-op Close.
type Reader struct {
	*bytes.Reader
}

// NewReaderFromBytes returns the given ClosingBufferReader from the given bytes.
func NewReaderFromBytes(b []byte) *Reader {
	return &Reader{bytes.NewReader(b)}
}

// Close is a noop.
func (cb *Reader) Close() error {
	return nil
}

// InstanceFile represents a file to push to an instance after creation.
type InstanceFile struct {
	Target string

	// Give either "File", "Content" or "Reader"
	File    string
	Content io.ReadSeekCloser

	UID       int64 // Uses oci.uid if -1 has been given.
	GID       int64 // Uses oci.gid if -1 has been given.
	Mode      int
	NoMKDir   bool
	DirMode   int
	Overwrite bool
}

// InstanceConfig configures instance creation.
type InstanceConfig struct {
	// ServiceName represents the compose service name.
	ServiceName string

	// Type is the instance type (container or VM).
	Type incusApi.InstanceType

	// Full fetches the full instance.
	Full bool

	// Image is the OCI image to create the instance from.
	Image string

	// Ensured Resources that this instance depends on.
	Resources []Resource

	// Devices are devices attached before instance creation (networks, proxies).
	Devices []InstanceDevice

	// PostStartDevices are devices attached after the instance is started.
	// Use for devices that require a running instance, e.g. NAT proxy (needs container IP).
	PostStartDevices []InstanceDevice

	// Files are files pushed into the instance after creation.
	// Map key is the target path in the instance.
	Files []InstanceFile

	// Extensions contains Incus instance configuration options.
	Extensions map[string]string

	// ExtraDevices contains additional raw device configurations.
	ExtraDevices map[string]map[string]string

	// Dependencies maps dependency Incus instance names to the required health
	// status (HealthStatusHealthy, HealthStatusStarting, HealthStatusUnhealthy).
	// Instance.Start() blocks until all dependencies reach the required status.
	Dependencies map[string]string

	// Priority if set sets the instance priority to this instead PriorityInstance.
	Priority int

	// AppendEntrypoint will be with a space appended to "oci.entrypoint".
	// This is here cause at configuration time we don't know what `oci.entrypoint` is.
	AppendEntrypoint string

	// UID if not 0 use that value else use the user id from the image.
	UID uint64
	// GID if not 0 use that value else use the user id from the image.
	GID uint64
}

// GetConfig returns the configuration.
func (c *InstanceConfig) GetConfig() any {
	return c
}

// Instance represents an Incus container or virtual machine.
type Instance struct {
	*BaseResource

	client    *Client
	incusName string
	created   bool
	Config    InstanceConfig

	// deleteMarked indicates that this instance will be deleted after Ensure(),
	// this is for down scaling instances.
	deleteMarked bool

	// conn is this resource's own event-isolated Incus connection, set in
	// Ensure() (which always runs before any other action) so concurrent
	// workers never share a *ProtocolIncus. See Client.Connection.
	conn *incusClient.ProtocolIncus

	// image is for internal use in create operations.
	image *Image

	// State - nil means not ensured.
	IncusInstance *incusApi.Instance
	ETag          string

	// // UID/GID from the config or extracted from container (for volume shifting).
	UID uint64
	GID uint64

	IncusInstanceFull *incusApi.InstanceFull
}

func newInstance(c *Client, name string, configGetter Config) (*Instance, error) {
	if configGetter == nil {
		return nil, ErrUnknownConfig.WithKindName(KindInstance, name)
	}

	var config *InstanceConfig
	cConfig, ok := configGetter.GetConfig().(*InstanceConfig)
	if !ok {
		return nil, ErrUnknownConfig.WithKindName(KindInstance, name)
	}
	config = cConfig

	if config.Priority == 0 {
		config.Priority = PriorityInstance
	}

	// Set defaults
	if config.Type == "" {
		config.Type = incusApi.InstanceTypeContainer
	}
	if config.Extensions == nil {
		config.Extensions = make(map[string]string)
	}

	inst := &Instance{
		BaseResource: NewBaseResource(KindInstance, name, config.Priority),
		client:       c,
		incusName:    SanitizeIncusName(name, -1),
		Config:       *config,
	}

	return inst, nil
}

// String is for debugging.
func (r *Instance) String() string {
	return fmt.Sprintf("%v(%v)", r.kind, r.incusName)
}

// IncusName returns the sanitized instance name used in Incus.
func (r *Instance) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the instance has been fetched/created.
func (r *Instance) IsEnsured() bool {
	return r.IncusInstance != nil
}

// Created returns true if the instance was created during the last Ensure call.
func (r *Instance) Created() bool {
	return r.created
}

// ServiceName returns the compose service name which has been set by the config.
func (r *Instance) ServiceName() string {
	return r.Config.ServiceName
}

// WaitIPs polls the instance state until it reports at least one global address
// or the timeout elapses. A freshly started container may not have its DHCP
// lease yet, so this gives it time. On timeout it returns whatever was found
// (possibly empty).
func (r *Instance) WaitIPs(ctx context.Context, timeout time.Duration) (ips []InterfaceIPs, err error) {
	if err := r.fetch(); err != nil {
		return nil, err
	}

	deadline, cancel := context.WithTimeout(ctx, timeout)

	for {
		r.client.LogDebug("Waiting for IPs", "instance", r)

		if r.Running() {
			ips, err = r.client.InstanceIPs(r.IncusName())
			if err == nil {
				cancel()
				return ips, nil
			}
		}

		select {
		case <-deadline.Done():
			cancel()
			return nil, NewError("WaitIPs").WithText(fmt.Sprintf("timeout after: %v", timeout))
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// HasFull returns true if the instance has a full instance.
func (r *Instance) HasFull() bool {
	return r.IncusInstanceFull != nil
}

func (r *Instance) fetch() error {
	// Fresh instance.
	instance, eTag, err := r.conn.GetInstance(r.incusName)
	if err != nil {
		return err
	}
	r.IncusInstance = instance
	r.ETag = eTag

	r.UID = r.Config.UID
	r.GID = r.Config.GID

	if r.UID == 0 || r.GID == 0 {
		var err error
		r.UID, r.GID, err = extractUIDGID(r.IncusInstance)
		if err != nil {
			return ErrInvalidFormat.WithText("extracting uid/gid").Wrap(err)
		}
	}

	if r.Config.Full {
		full, _, err := r.conn.GetInstanceFull(r.IncusInstance.Name)
		if err != nil {
			return err
		}

		r.IncusInstanceFull = full
	}

	return nil
}

// Ensure retrieves an existing instance or creates a new one if args.Create is true.
func (r *Instance) Ensure(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionEnsure, r, options, nil); err != nil {
		return err
	}

	conn, err := r.client.Connection()
	if err != nil {
		return err
	}
	r.conn = conn

	// Try to get existing
	// Check if exists
	err = r.fetch()
	if err == nil {
		err = r.ensured()
		err = r.client.hookAfter(ctx, ActionEnsure, r, options, err)

		if err == nil && r.deleteMarked {
			if err := r.Stop(ctx, OptionTimeout(options.Timeout), OptionForce()); err != nil {
				return err
			}

			if err := r.Delete(ctx); err != nil {
				return err
			}
		}

		return err
	}

	if !options.Create {
		err = ErrNotFound.Wrap(err)
		err = r.client.hookAfter(ctx, ActionEnsure, r, options, err)

		if r.deleteMarked {
			// Just remove the resource
			r.client.resources.Remove(r)
		}

		return err
	}

	err = r.create(ctx, opts...)
	err = r.client.hookAfter(ctx, ActionEnsure, r, options, err)

	return err
}

func (r *Instance) ensured() error {
	if r.Config.Image == "" {
		if alias, ok := r.IncusInstance.Config["user.image_alias"]; ok {
			r.Config.Image = alias
		} else {
			r.Config.Image = r.client.ResolveImageFingerprint(r.IncusInstance.Config["volatile.base_image"])
		}
	}

	return nil
}

func (r *Instance) create(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	// Can't create an instance without an image
	if r.Config.Image == "" {
		return ErrImageRequired
	}

	if r.Config.Resources != nil {
		for _, rDep := range r.Config.Resources {
			if !rDep.IsEnsured() {
				return ErrDependencyNotEnsured.WithResource(rDep)
			}
		}
	}

	imageResource, err := r.client.Resource(KindImage, r.Config.Image, &ImageConfig{})
	if err != nil {
		return err
	}

	image, ok := imageResource.(*Image)
	if !ok {
		return ErrUnknown.WithResource(imageResource)
	}

	// The image must have been ensured first. If its Ensure failed (e.g. the
	// pull errored), IncusAlias is nil; fail cleanly instead of dereferencing it.
	if !image.IsEnsured() {
		r.client.LogDebug("Dependency", "image", image)
		return ErrDependencyNotEnsured.WithResource(image)
	}

	r.image = image

	config := map[string]string{}

	r.UID = r.Config.UID
	r.GID = r.Config.GID

	if r.UID == 0 && r.GID == 0 {
		// Use UID/GID from image properties when available so volumes are created
		// with the correct shifted config before the instance is created.
		if image.UID > 0 || image.GID > 0 {
			r.UID = image.UID
			r.GID = image.GID
		}
	}

	if r.Config.AppendEntrypoint != "" {
		config["oci.entrypoint"] = r.image.Entrypoint + " " + r.Config.AppendEntrypoint
	}

	// Store UID/GID.
	config["oci.uid"] = strconv.FormatUint(r.UID, 10)
	config["oci.gid"] = strconv.FormatUint(r.GID, 10)

	// Store the image name
	config["user.image_alias"] = image.IncusName()

	// Build devices map after volumes are resolved.
	devices, err := r.buildDevices()
	if err != nil {
		return err
	}

	// Get image info from project
	incusImage, _, err := r.conn.GetImage(image.IncusAlias.Target)
	if err != nil {
		return ErrNotFound.WithText("getting image").Wrap(err)
	}

	// Copy users project / x-incus config.
	// This is after all our configs so we allow users to override it.
	maps.Copy(config, r.Config.Extensions)

	if options.Healthd {
		// Healthd should wait until we allow it to work with it.
		config[HealthStoppedKey] = "true"
	}

	// Create instance request
	req := incusApi.InstancesPost{
		Name: r.incusName,
		Type: r.Config.Type,
		Source: incusApi.InstanceSource{
			Type:        "image",
			Fingerprint: incusImage.Fingerprint,
		},
		InstancePut: incusApi.InstancePut{
			Description: fmt.Sprintf(r.client.Config().DescriptionFormat, r.Name()),
			Config:      config,
			Devices:     devices,
		},
	}

	// Create instance from project image.
	op, err := r.conn.CreateInstanceFromImage(r.conn, *incusImage, req)
	if err = r.client.hookRemoteOperation(ctx, ActionEnsure, r, options, op, err); err != nil {
		return err
	}

	// Get instance to extract UID/GID
	if err := r.fetch(); err != nil {
		return ErrCreate.WithText("fetching created instance").Wrap(err)
	}

	if err = r.ensured(); err != nil {
		return err
	}

	r.created = true

	return nil
}

func (r *Instance) buildDevices() (map[string]map[string]string, error) {
	var devices map[string]map[string]string

	if r.Config.ExtraDevices != nil {
		devices = maps.Clone(r.Config.ExtraDevices)
	} else {
		devices = make(map[string]map[string]string)
	}

	profiles, err := ByKind[*Profile](r.Config.Resources, KindProfile)
	if err != nil {
		return nil, err
	}

	// Add Devices
	for _, dev := range r.Config.Devices {
		name, config, err := dev.ToIncusDevice()
		if err != nil {
			return nil, err
		}

		// The code below would have allowed us to overwrite `eth0`,
		// but it breaks normal incus behaviour (instances overwrite profile).
		// foundInProfile := false
		// for _, profile := range profiles {
		// 	foundInProfile = profile.HasDevice(name)
		// 	if foundInProfile {
		// 		break
		// 	}
		// }

		// if foundInProfile {
		// 	return nil, ErrDeviceConflict.WithText("device exists in profile " + name)
		// }

		devices[name] = config
	}

	if _, ok := devices["root"]; !ok {
		foundInProfile := false
		for _, profile := range profiles {
			foundInProfile = profile.HasDevice("root")
			if foundInProfile {
				break
			}
		}

		if !foundInProfile {
			devices["root"] = map[string]string{
				"type": "disk",
				"path": "/",
				"pool": r.client.Config().DefaultStoragePool,
			}
		}
	}

	return devices, nil
}

func (r *Instance) attachPostStartDevices(ctx context.Context) error {
	// Resolve container IPs once - instance is running so this should be fast.
	ips, err := r.WaitIPs(ctx, dnsIPWaitTimeout)
	if err != nil {
		r.client.LogWarn("could not resolve IPs for post-start devices", "instance", r.incusName, "err", err)
	}

	network := ips[0].Network
	iPv4s := ips[0].IPv4s
	iPv6s := ips[0].IPv6s

	var bridgeV4Addrs, bridgeV6Addrs []string
	bridgeV4Addrs, bridgeV6Addrs, err = r.client.Global().NetworkBridgeIPs(network)
	if err != nil {
		return fmt.Errorf("nat-proxy: could not get bridge IPs for network %s: %w", network, err)
	}

	if len(bridgeV4Addrs) == 0 && len(bridgeV6Addrs) == 0 {
		return fmt.Errorf("nat-proxy: didn't get an IP for network %s", network)
	}

	devices := map[string]map[string]string{}
	for _, dev := range r.Config.PostStartDevices {
		if dev.Config.DeviceType == InstanceDeviceTypeProxy && dev.Config.Proxy.Nat {
			if dev.Config.Proxy.ListenAddr == "" {
				dev.Config.Proxy.ListenAddr = bridgeV4Addrs[0]
			}

			if ip := net.ParseIP(dev.Config.Proxy.ListenAddr).To4(); ip == nil {
				if len(iPv6s) < 1 {
					return fmt.Errorf("no IPv6 address for NAT proxy, instance %s", r.Name())
				}
				dev.Config.Proxy.ConnectAddr = iPv6s[0]
			} else {
				if len(iPv4s) < 1 {
					return fmt.Errorf("no IPv4 address for NAT proxy, instance %s", r.Name())
				}
				dev.Config.Proxy.ConnectAddr = iPv4s[0]
			}
		}

		devName, devConfig, err := dev.ToIncusDevice()
		if err != nil {
			return err
		}

		devices[devName] = devConfig
	}

	w := r.IncusInstance.Writable()
	w.Devices = devices

	op, err := r.conn.UpdateInstance(r.IncusName(), w, r.ETag)
	if err != nil {
		return err
	}

	err = op.WaitContext(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Start starts the instance.
func (r *Instance) Start(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionStart, r, options, nil); err != nil {
		return err
	}

	if !r.IsEnsured() {
		return r.client.hookAfter(ctx, ActionStart, r, options, ErrNotEnsured)
	}

	if r.Running() {
		if options.Healthd {
			err := r.SetHealthCheckingStopped(ctx, false)
			if err != nil {
				return r.client.hookAfter(ctx, ActionStart, r, options, err)
			}
		}

		return r.client.hookAfter(ctx, ActionStart, r, options, ErrRunning)
	}

	startCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	err := r.start(startCtx, options)
	if err != nil {
		return r.client.hookAfter(ctx, ActionStart, r, options, err)
	}

	if options.Healthd {
		err := r.SetHealthCheckingStopped(ctx, false)
		if err != nil {
			return r.client.hookAfter(ctx, ActionStart, r, options, err)
		}

		// Wait for the healthcheck to success if a test is defined.
		_, hasTest := r.IncusInstance.Config[HealthKeyPrefix+"test"]
		_, isHealthd := r.IncusInstance.Config[HealthKeyPrefix+"daemon"]

		if hasTest && !isHealthd {
			err = r.waitForHealthCheck(ctx, ActionStart, options)
			if err != nil {
				return r.client.hookAfter(ctx, ActionStart, r, options, err)
			}
		}
	}

	return r.client.hookAfter(ctx, ActionStart, r, options, nil)
}

// Running returns true if the instance is running.
func (r *Instance) Running() bool {
	if !r.IsEnsured() {
		return false
	}

	return r.IncusInstance.StatusCode == incusApi.Running
}

func (r *Instance) waitForHealthCheck(ctx context.Context, action Action, options Options) error {
	var cancel context.CancelFunc
	if options.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, options.Timeout)
		defer cancel()
	} else {
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
	}

	if !options.ExternalHealthd {
		// Wait for healthd to be available for 3 seconds.
		err := retry.New(
			retry.Context(ctx),
			retry.Attempts(6),
			retry.Delay(500*time.Millisecond),
		).Do(func() error {
			healthd, err := r.client.FindHealthd()
			if err != nil {
				return err
			}

			hInstState, _, err := r.conn.GetInstanceState(healthd)
			if err != nil {
				return fmt.Errorf("failed to get the healthd '%v' instance state: %w", healthd, err)
			}

			if hInstState.StatusCode != incusApi.Running {
				return fmt.Errorf("healthd '%v' not running cannot wait for it to check dependencies", healthd)
			}

			return nil
		})
		if err != nil {
			return err
		}
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		err := r.fetch()
		if err == nil && r.IncusInstance.Config[HealthStatusKey] == HealthStatusHealthy {
			r.client.LogDebug("Ready", "resource", r)

			return nil
		}

		r.client.globalClient.emitProgress(action, r, options, Progress{
			Percent: -1,
			Text:    "Waiting for the healthcheck",
		})

		select {
		case <-ticker.C:
			// r.client.LogDebug("Waiting for the healthcheck", "resource", r)
		case <-ctx.Done():
			return fmt.Errorf("did not reach status %q within %s", HealthStatusHealthy, options.Timeout)
		}
	}
}

// waitForDependencies blocks until all Config.Dependencies reach their required
// health status, or until the dependency timeout elapses.
func (r *Instance) waitForDependencies(ctx context.Context, action Action, options Options) error {
	if len(r.Config.Dependencies) == 0 {
		return nil
	}

	if !options.ExternalHealthd {
		// Wait for healthd to be available for 3 seconds.
		err := retry.New(
			retry.Context(ctx),
			retry.Attempts(6),
			retry.Delay(500*time.Millisecond),
		).Do(func() error {
			healthd, err := r.client.FindHealthd()
			if err != nil {
				return err
			}

			hInstState, _, err := r.conn.GetInstanceState(healthd)
			if err != nil {
				return fmt.Errorf("failed to get the healthd '%v' instance state: %w", healthd, err)
			}

			if hInstState.StatusCode != incusApi.Running {
				return fmt.Errorf("healthd '%v' not running cannot wait for it to check dependencies", healthd)
			}

			return nil
		})
		if err != nil {
			return err
		}
	}

	timeout := options.DependencyTimeout
	if timeout == 0 {
		timeout = options.Timeout
	}

	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	logTicker := time.NewTicker(2 * time.Second)
	defer logTicker.Stop()

	startTimeout := time.After(timeout / 3)

	for depName, requiredStatus := range r.Config.Dependencies {
		r.client.LogDebug("Waiting for dependency", "instance", r.incusName, "dep", depName, "status", requiredStatus)
		// Report the wait on the instance's start line so it shows a spinner
		// instead of stalling silently. This wait is not an Incus operation,
		// so it has no percentage.
		r.client.globalClient.emitProgress(action, r, options, Progress{
			Percent: -1,
			Text:    fmt.Sprintf("Waiting for dependency %s", depName),
		})
		for {
			inst, _, err := r.conn.GetInstance(depName)
			if err == nil && inst.Config[HealthStatusKey] == requiredStatus {
				r.client.LogDebug("Dependency ready", "dep", depName)
				break
			}

			select {
			case <-startTimeout:
				if inst.StatusCode != incusApi.Running {
					cancel()
					return fmt.Errorf("dependency '%v' not running after %s", depName, timeout/3)
				}
			case <-ticker.C:
				select {
				case <-logTicker.C:
					if err == nil {
						r.client.LogDebug("Dependency not ready", "dep", depName, "requiredStatus", requiredStatus, "status", inst.Config[HealthStatusKey])
					} else {
						r.client.LogDebug("Dependency not ready", "dep", depName, "requiredStatus", requiredStatus, "error", err)
					}
				default:
				}
			case <-ctx.Done():
				cancel()
				return fmt.Errorf("dependency '%v' did not reach status %q within %s", depName, requiredStatus, timeout)
			}
		}
	}

	cancel()
	return nil
}

func (r *Instance) start(ctx context.Context, options Options) error {
	if r.Running() {
		return nil
	}

	if options.Healthd {
		_, isHealthd := r.IncusInstance.Config[HealthKeyPrefix+"daemon"]
		if !isHealthd {
			if err := r.waitForDependencies(ctx, ActionStart, options); err != nil {
				return err
			}
		}
	}

	err := r.fetch()
	if err != nil {
		return err
	}

	sftpConn, err := r.conn.GetInstanceFileSFTP(r.incusName)
	if err != nil {
		return ErrCreate.WithText("connecting to instance SFTP").Wrap(err)
	}

	// Push files while the instance is stopped: SFTP mounts the stopped rootfs,
	// most apps need theier secrets before the actual start happened.
	if err := r.PushFiles(sftpConn); err != nil {
		r.client.WarnError(sftpConn.Close, "Failed to close a sFTP connection")
		return err
	}

	r.client.WarnError(sftpConn.Close, "Failed to close a sFTP connection")

	if r.Running() {
		return ErrRunning
	}

	op, err := r.conn.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
		Action:  "start",
		Timeout: options.incusTimeout(),
	}, r.ETag)
	if err != nil {
		return ErrOperation.WithText("creating an instance start operation").Wrap(err)
	}

	// The operation completes once the instance is running or failed to start.
	err = r.client.hookOperation(ctx, ActionStart, r, options, op, err)
	if err != nil {
		return ErrOperation.WithText("starting an instance").Wrap(err)
	}

	err = r.fetch()
	if err != nil {
		return ErrOperation.WithText("fetch after create").Wrap(err)
	}

	if !r.Running() {
		return ErrNotRunning.WithText("after a start")
	}

	if r.created && len(r.Config.PostStartDevices) > 0 {
		err := r.attachPostStartDevices(ctx)
		if err != nil {
			return ErrCreate.WithText("post start").Wrap(err)
		}
	}

	return nil
}

// PushFiles pushes files into the instance over the instance's SFTP endpoint.
func (r *Instance) PushFiles(sftpConn *sftp.Client) error {
	if !r.IsEnsured() {
		return ErrNotEnsured
	}

	if len(r.Config.Files) == 0 {
		return nil
	}

	if sftpConn == nil {
		var err error
		sftpConn, err = r.conn.GetInstanceFileSFTP(r.incusName)
		if err != nil {
			return ErrCreate.WithText("connecting to instance SFTP").Wrap(err)
		}

		defer r.client.WarnError(sftpConn.Close, "Failed to close a sFTP connection")
	}

	for _, file := range r.Config.Files {
		err := r.pushFile(sftpConn, file)
		if err != nil {
			return ErrCreate.WithText("pushing file " + file.Target).Wrap(err)
		}
	}

	return nil
}

// pushFile writes a single InstanceFile over an established SFTP connection,
// creating parent directories and honoring the Overwrite flag.
func (r *Instance) pushFile(sftpConn *sftp.Client, file InstanceFile) error {
	if file.File != "" && file.Content != nil {
		return ErrCreate.WithText(fmt.Sprintf("cannot have both 'File' and 'Content' for instance file %q", file.Target))
	}

	if file.File != "" && file.Content == nil {
		fp, err := os.Open(file.File)
		if err != nil {
			return ErrCreate.Wrap(err)
		}
		file.Content = fp
	}

	// Resolve ownership: -1 falls back to the instance's oci.uid/oci.gid.
	uid, gid := file.UID, file.GID
	if uid == -1 {
		uid = int64(r.UID)
	}
	if gid == -1 {
		gid = int64(r.GID)
	}

	// Create parent directories, owned by the instance user.
	if !file.NoMKDir {
		dirMode := os.FileMode(file.DirMode)
		if dirMode == 0 {
			dirMode = 0o755
		}

		err := sftpRecursiveMkdir(r.client, sftpConn, filepath.Dir(file.Target), &dirMode, uid, gid)
		if err != nil {
			return ErrCreate.Wrap(err)
		}
	}

	// Leave an existing file untouched unless the caller opted into overwriting.
	if !file.Overwrite {
		_, err := sftpConn.Lstat(file.Target)
		if err == nil {
			// PushFiles owns the reader, so close it even when skipping.
			if file.Content != nil {
				r.client.WarnError(file.Content.Close, "Closing a push file")
			}

			r.client.LogDebug("Skipping existing instance file", "resource", r, "target", file.Target)
			return nil
		}
	}

	args := incusClient.InstanceFileArgs{
		Content:   file.Content,
		UID:       uid,
		GID:       gid,
		Mode:      file.Mode,
		Type:      "file",
		WriteMode: "overwrite",
	}

	err := sftpCreateFile(r.client, sftpConn, file.Target, args, true)
	if err != nil {
		return ErrCreate.Wrap(err)
	}

	r.client.WarnError(file.Content.Close, "Failed to close a file")

	return nil
}

// sftpSetOwnerMode
// From: https://github.com/lxc/incus/blob/975d9869315b6db088c7c40ca5b37ee45e5ff8cf/cmd/incus/utils_sftp.go#L24
func sftpSetOwnerMode(sftpConn *sftp.Client, targetPath string, args incusClient.InstanceFileArgs) error {
	// Skip if not on UNIX.
	_, err := sftpConn.StatVFS("/")
	if err != nil {
		return nil
	}

	// Get the current stat information.
	st, err := sftpConn.Stat(targetPath)
	if err != nil {
		return err
	}

	fileStat, ok := st.Sys().(*sftp.FileStat)
	if !ok {
		return fmt.Errorf("Invalid filestat data for %q", targetPath)
	}

	// Set owner.
	if args.UID >= 0 || args.GID >= 0 {
		if args.UID == -1 {
			args.UID = int64(fileStat.UID)
		}

		if args.GID == -1 {
			args.GID = int64(fileStat.GID)
		}

		err = sftpConn.Chown(targetPath, int(args.UID), int(args.GID))
		if err != nil {
			return err
		}
	}

	// Set mode.
	if args.Mode >= 0 {
		err = sftpConn.Chmod(targetPath, fs.FileMode(args.Mode))
		if err != nil {
			return err
		}
	}

	return nil
}

// sftpCreateFile
// From: https://github.com/lxc/incus/blob/975d9869315b6db088c7c40ca5b37ee45e5ff8cf/cmd/incus/utils_sftp.go#L69
func sftpCreateFile(c *Client, sftpConn *sftp.Client, targetPath string, args incusClient.InstanceFileArgs, push bool) error {
	switch args.Type {
	case "file":
		file, err := sftpConn.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return fmt.Errorf("failed to open target file %q: %w", targetPath, err)
		}

		defer c.WarnError(file.Close, "")

		if push {
			_, err = io.Copy(file, args.Content)
			if err != nil {
				return err
			}
		}

		err = sftpSetOwnerMode(sftpConn, targetPath, args)
		if err != nil {
			return err
		}

	case "directory":
		err := sftpConn.MkdirAll(targetPath)
		if err != nil {
			return err
		}

		err = sftpSetOwnerMode(sftpConn, targetPath, args)
		if err != nil {
			return err
		}

	case "symlink":
		// If already a symlink, re-create it.
		fInfo, err := sftpConn.Lstat(targetPath)
		if err == nil && fInfo.Mode()&os.ModeSymlink == os.ModeSymlink {
			err = sftpConn.Remove(targetPath)
			if err != nil {
				return err
			}
		}

		dest, err := io.ReadAll(args.Content)
		if err != nil {
			return err
		}

		err = sftpConn.Symlink(string(dest), targetPath)
		if err != nil {
			return err
		}
	}

	return nil
}

// sftpMkdirAll creates dir and any missing parents over SFTP, applying mode and
// ownership only to the directories it creates. Existing directories are left
// untouched, so it never re-owns pre-existing paths like /run.
// From: https://github.com/lxc/incus/blob/975d9869315b6db088c7c40ca5b37ee45e5ff8cf/cmd/incus/utils_sftp.go#L389
func sftpRecursiveMkdir(c *Client, sftpConn *sftp.Client, p string, mode *os.FileMode, uid int64, gid int64) error {
	/* special case, every instance has a /, we don't need to do anything */
	if p == "/" {
		return nil
	}

	// Remove trailing "/" e.g. /A/B/C/. Otherwise we will end up with an
	// empty array entry "" which will confuse the Mkdir() loop below.
	pclean := filepath.Clean(p)
	parts := strings.Split(pclean, "/")
	i := len(parts)

	for ; i >= 1; i-- {
		cur := filepath.Join(parts[:i]...)
		fInfo, err := sftpConn.Lstat(cur)
		if err != nil {
			continue
		}

		if !fInfo.IsDir() {
			return fmt.Errorf("%s is not a directory", cur)
		}

		i++
		break
	}

	for ; i <= len(parts); i++ {
		cur := filepath.Join(parts[:i]...)
		if cur == "" {
			continue
		}

		cur = "/" + cur
		cur = strings.TrimLeft(cur, "/")

		modeArg := -1
		if mode != nil {
			modeArg = int(mode.Perm())
		}

		args := incusClient.InstanceFileArgs{
			UID:  max(uid, 0),
			GID:  max(gid, 0),
			Mode: modeArg,
			Type: "directory",
		}

		c.LogDebug("Creating", "directory", cur)
		err := sftpCreateFile(c, sftpConn, cur, args, false)
		if err != nil {
			return err
		}
	}

	return nil
}

// Stop stops the instance.
func (r *Instance) Stop(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionStop, r, options, nil); err != nil {
		return err
	}

	if !r.IsEnsured() {
		return r.client.hookAfter(ctx, ActionStop, r, options, ErrNotEnsured)
	}

	if !r.Running() {
		if options.Healthd {
			err := r.SetHealthCheckingStopped(ctx, true)
			if err != nil {
				return r.client.hookAfter(ctx, ActionStop, r, options, err)
			}
		}

		return r.client.hookAfter(ctx, ActionStop, r, options, ErrNotRunning)
	}

	stopCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	err := r.stop(stopCtx, options)

	if options.Healthd {
		err := r.SetHealthCheckingStopped(ctx, true)
		if err != nil {
			return r.client.hookAfter(ctx, ActionStop, r, options, err)
		}
	}

	return r.client.hookAfter(ctx, ActionStop, r, options, err)
}

func (r *Instance) stop(ctx context.Context, options Options) error {
	if !r.Running() {
		return nil
	}

	op, err := r.conn.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
		Action:  "stop",
		Force:   options.Force,
		Timeout: options.incusTimeout(),
	}, r.ETag)
	if err != nil {
		return ErrOperation.WithText("stopping instance").Wrap(err)
	}

	// The operation completes once the instance is stopped or failed to stop.
	err = r.client.hookOperation(ctx, ActionStop, r, options, op, err)
	if err != nil {
		return err
	}

	return r.fetch()
}

// SetHealthCheckingStopped writes the user.healthcheck.stopped config key on
// the instance. Patches only this key; a full UpdateInstance races with incusd
// writing volatile config keys around start/stop (ETag mismatch under load).
func (r *Instance) SetHealthCheckingStopped(ctx context.Context, stopped bool) error {
	if err := r.fetch(); err != nil {
		return err
	}

	if (r.IncusInstance.Config[HealthStoppedKey] == "true") == stopped {
		return nil
	}

	value := "false"
	if stopped {
		value = "true"
	}

	w := r.IncusInstance.Writable()
	w.Config[HealthStoppedKey] = value

	op, err := r.conn.UpdateInstance(r.IncusName(), w, r.ETag)
	if err != nil {
		return err
	}

	err = op.WaitContext(ctx)
	if err != nil {
		return err
	}

	return r.fetch()
}

// MarkDelete marks a instance to be deleted after Ensure(),
// this is for down scaling instances.
func (r *Instance) MarkDelete() {
	r.deleteMarked = true
}

// Delete removes the instance from Incus.
func (r *Instance) Delete(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionDelete, r, options, nil); err != nil {
		r.IncusInstance = nil
		r.ETag = ""

		r.client.resources.Remove(r)
		return err
	}

	if !r.IsEnsured() {
		r.IncusInstance = nil
		r.ETag = ""

		r.client.resources.Remove(r)
		return r.client.hookAfter(ctx, ActionDelete, r, options, ErrNotEnsured)
	}

	op, err := r.conn.DeleteInstance(r.incusName)

	// Do the delete
	err = r.client.hookOperation(ctx, ActionDelete, r, options, op, err)

	if err := r.client.hookAfter(ctx, ActionDelete, r, options, err); err != nil {
		r.IncusInstance = nil
		r.ETag = ""

		r.client.resources.Remove(r)
		return err
	}

	r.IncusInstance = nil
	r.ETag = ""

	r.client.resources.Remove(r)
	return nil
}

// Log streams the instance console log to the outputHandler.
func (r *Instance) Log(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionLog, r, options, nil); err != nil {
		return err
	}

	if r.conn == nil {
		conn, err := r.client.Connection()
		if err != nil {
			return r.client.hookAfter(ctx, ActionLog, r, options, err)
		}

		r.conn = conn
	}

	err := r.fetch()
	if err != nil {
		return r.client.hookAfter(ctx, ActionLog, r, options, err)
	}

	err = r.log(ctx, options)
	err = r.client.hookAfter(ctx, ActionLog, r, options, err)

	return err
}

func (r *Instance) log(ctx context.Context, options Options) error {
	outputHandler := r.client.globalClient.outputHandler
	if outputHandler == nil {
		return nil
	}

	if options.Follow {
		if err := r.logBuffer(outputHandler); err != nil {
			return err
		}
		return r.logStream(ctx, options, outputHandler)
	}

	return r.logBuffer(outputHandler)
}

// logBuffer reads the saved console log buffer via GET /console (equivalent to
// `incus console --show-log`). Used for non-follow log retrieval.
func (r *Instance) logBuffer(outputHandler func(Action, Resource, []byte)) error {
	reader, err := r.conn.GetInstanceConsoleLog(r.incusName, nil)
	if err != nil {
		return ErrOperation.WithText("getting console log").Wrap(err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return ErrOperation.WithText("reading console log").Wrap(err)
	}

	outputHandler(ActionLog, r, data)
	return nil
}

// logStream streams the console using WebSocket until context is cancelled.
func (r *Instance) logStream(ctx context.Context, options Options, outputHandler func(Action, Resource, []byte)) error {
	// Channel to signal disconnect
	consoleDisconnect := make(chan bool)

	// Terminal that writes to outputHandler
	terminal := &logTerminal{
		resource:      r,
		outputHandler: outputHandler,
	}

	// Connect to console WebSocket
	req := incusApi.InstanceConsolePost{
		Type:  "console",
		Force: true, // Take over existing console connections
	}

	// Control handler - required by Incus API, but we don't need window resize.
	// We just wait for context cancellation; the library handles websocket cleanup.
	controlHandler := func(_ *websocket.Conn) {
		<-ctx.Done()
	}

	args := &incusClient.InstanceConsoleArgs{
		Terminal:          terminal,
		Control:           controlHandler,
		ConsoleDisconnect: consoleDisconnect,
	}

	op, err := r.conn.ConsoleInstance(r.incusName, req, args)
	if err != nil {
		return ErrOperation.WithText("connecting to console").Wrap(err)
	}

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		close(consoleDisconnect)
	}()

	// Wait for operation to complete using hookOperation
	err = r.client.hookOperation(ctx, ActionLog, r, options, op, err)

	// Context cancellation (including timeout) is not an error
	if ctx.Err() != nil {
		return nil
	}

	if err != nil {
		return ErrOperation.WithText("console streaming").Wrap(err)
	}

	return nil
}

// logTerminal implements io.ReadWriteCloser for console streaming.
type logTerminal struct {
	resource      *Instance
	outputHandler func(Action, Resource, []byte)
}

func (t *logTerminal) Write(p []byte) (int, error) {
	t.outputHandler(ActionLog, t.resource, p)
	return len(p), nil
}

func (t *logTerminal) Read(_ []byte) (int, error) {
	select {} // Block forever - we never send input
}

// Close implements io.Closer.
func (t *logTerminal) Close() error {
	return nil
}

// extractUIDGID extracts UID and GID from a container instance.
func extractUIDGID(instance *incusApi.Instance) (uint64, uint64, error) {
	if incusApi.InstanceType(instance.Type) != incusApi.InstanceTypeContainer {
		return 0, 0, nil
	}

	// oci.uid/gid only exist for OCI images, not native Incus images
	uidStr, hasUID := instance.Config["oci.uid"]
	gidStr, hasGID := instance.Config["oci.gid"]
	if !hasUID || !hasGID {
		return 0, 0, nil
	}

	uid, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	gid, err := strconv.ParseUint(gidStr, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	return uid, gid, nil
}

var (
	_ Resource   = (*Instance)(nil)
	_ EnsureAble = (*Instance)(nil)
	_ StartAble  = (*Instance)(nil)
	_ StopAble   = (*Instance)(nil)
	_ DeleteAble = (*Instance)(nil)
	_ LogAble    = (*Instance)(nil)
)
