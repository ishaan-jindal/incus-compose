package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/lxc/incus-compose/client"
)

type xICInstanceVolume struct {
	Seed bool `mapstructure:"seed"`
}

func buildPlatform(service types.ServiceConfig) (string, error) {
	if service.Build == nil {
		return "", nil
	}
	if len(service.Build.Platforms) > 1 {
		return "", fmt.Errorf("build.platforms with multiple platforms is not supported")
	}
	if len(service.Build.Platforms) == 1 {
		return service.Build.Platforms[0], nil
	}
	return service.Platform, nil
}

// serviceToInstance translates a compose service to an Incus instance.
// Environment vars become instance config, labels become user metadata.
// Volumes default to bind mounts for paths starting with / or ., otherwise named volumes.
func serviceToInstance(c *client.Client, p *types.Project, serviceName string, options *ResourcesOptions, index, scale int) (*client.Instance, []client.Resource, error) {
	service, ok := p.Services[serviceName]
	if !ok {
		return nil, nil, fmt.Errorf("service %q not found", serviceName)
	}

	var errs error
	resources := []client.Resource{}

	config, err := instanceConfig(service)
	if err != nil {
		errs = errors.Join(errs, err)
	}

	image, err := instanceImage(c, service)
	if err != nil {
		errs = errors.Join(errs, err)
	}
	if image == nil {
		return nil, nil, errs
	}
	resources = append(resources, image)

	devices, networks, err := instanceNetworkDevices(c, p, service)
	if err != nil {
		errs = errors.Join(errs, err)
	}
	resources = append(resources, networks...)

	proxies, postStartDevices, err := instanceProxyDevices(c, service, devices)
	if err != nil {
		errs = errors.Join(errs, err)
	}
	devices = append(devices, proxies...)

	var (
		uid uint64
		gid uint64
	)
	// User override - https://github.com/compose-spec/compose-spec/blob/main/05-services.md#user
	if service.User != "" {
		split := strings.Split(service.User, ":")

		uid, err = strconv.ParseUint(split[0], 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot convert service '%v' user '%v' to int: %w", service.Name, split[0], err)
		}
		config["oci.uid"] = split[0]
		if len(split) > 1 {
			gid, err = strconv.ParseUint(split[1], 10, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("cannot convert service '%v' user '%v' to int: %w", service.Name, split[1], err)
			}
		}
	}

	volumes, files, volumeResources, err := instanceVolumeDevices(c, p, service, image, uid, gid, options)
	if err != nil {
		errs = errors.Join(errs, err)
	}
	devices = append(devices, volumes...)
	resources = append(resources, volumeResources...)

	extraDevices, err := serviceExtraDevices(service)
	if err != nil {
		errs = errors.Join(errs, err)
	}
	devices = append(devices, extraDevices...)

	secrets, err := instanceSecrets(p, service)
	if err != nil {
		errs = errors.Join(errs, err)
	}
	files = append(files, secrets...)

	if errs != nil {
		return nil, nil, errs
	}

	instCfg := &client.InstanceConfig{
		ServiceName:      service.Name,
		Image:            image.Name(),
		Full:             options.Full,
		Resources:        slices.Clone(resources),
		Extensions:       config,
		Devices:          devices,
		PostStartDevices: postStartDevices,
		Files:            files,
		Dependencies:     instanceDependencyWaits(p, service, options),
		AppendEntrypoint: formatCommand(service.Command),
		UID:              uid,
		GID:              gid,
	}

	ir, err := c.Resource(client.KindInstance, instanceName(service, index, scale), instCfg)
	if err != nil {
		return nil, nil, err
	}

	instance, ok := ir.(*client.Instance)
	if !ok {
		return nil, nil, client.ErrUnknown.WithKindName(client.KindInstance, instanceName(service, index, scale))
	}

	return instance, resources, nil
}

// instanceConfig builds the Incus instance config map from a compose service.
// Environment vars become environment.* keys, labels become user.* keys, and
// restart/resource/healthcheck settings and raw x-incus options are merged in.
func instanceConfig(service types.ServiceConfig) (map[string]string, error) {
	config := make(map[string]string, len(service.Environment)+len(service.Labels))

	// Environment variables
	for key, val := range service.Environment {
		if val != nil {
			config["environment."+key] = *val
		}
	}

	// Labels as user config
	for key, val := range service.Labels {
		config["user."+key] = val
	}

	// Privileged.
	if service.Privileged {
		config["security.privileged"] = "true"
	}

	// Restart policy
	applyRestartPolicy(config, service.Restart)
	if service.Restart != "" {
		config["user.restart"] = service.Restart
	}

	// Resource limits
	if service.Deploy != nil && service.Deploy.Resources.Limits != nil {
		applyResourceLimits(config, service.Deploy.Resources.Limits)
	}

	// Healtcheck
	config[client.HealthStatusKey] = client.HealthStatusUnknown

	if service.HealthCheck != nil {
		testB, err := json.Marshal(service.HealthCheck.Test)
		if err != nil {
			return nil, fmt.Errorf("converting service %q healthcheck test: %w", service.Name, err)
		}
		config[client.HealthKeyPrefix+"test"] = string(testB)

		if service.HealthCheck.StartPeriod != nil {
			config[client.HealthKeyPrefix+"start_period"] = service.HealthCheck.StartPeriod.String()
		}

		if service.HealthCheck.StartInterval != nil {
			config[client.HealthKeyPrefix+"start_interval"] = service.HealthCheck.StartInterval.String()
		}

		if service.HealthCheck.Interval != nil {
			config[client.HealthKeyPrefix+"interval"] = service.HealthCheck.Interval.String()
		}

		if service.HealthCheck.Retries != nil {
			config[client.HealthKeyPrefix+"retries"] = strconv.FormatUint(*service.HealthCheck.Retries, 10)
		}

		if service.HealthCheck.Timeout != nil {
			config[client.HealthKeyPrefix+"timeout"] = service.HealthCheck.Timeout.String()
		}
	}

	// Apply x-incus extensions (raw Incus options)
	if xIncusOpts := serviceXIncusExtensions(service); len(xIncusOpts) > 0 {
		for k, v := range xIncusOpts {
			config[k] = v
		}
	}

	// Ensure the network interface is up before the container's init starts.
	// Append lxc.start.delay only if the user hasn't already set it via x-incus.
	_, ok := config["raw.lxc"]
	if !ok {
		config["raw.lxc"] = "lxc.start.delay = 1\n"
	} else {
		if !strings.Contains(config["raw.lxc"], "lxc.start.delay") {
			config["raw.lxc"] += "lxc.start.delay = 1\n"
		}
	}

	return config, nil
}

// instanceImage resolves the image resource for a service, building from a
// Dockerfile when service.Build is set, otherwise pulling service.Image.
func instanceImage(c *client.Client, service types.ServiceConfig) (client.Resource, error) {
	var errs error

	imageName := service.Image
	cfg := &client.ImageConfig{}
	if service.Build != nil {
		if imageName == "" {
			imageName = "localhost/" + service.Name
		}
		platform, err := buildPlatform(service)
		if err != nil {
			errs = errors.Join(errs, err)
		}
		buildCfg := &client.BuildConfig{
			Context:          service.Build.Context,
			Dockerfile:       service.Build.Dockerfile,
			DockerfileInline: service.Build.DockerfileInline,
			Target:           service.Build.Target,
			Platform:         platform,
			NoCache:          service.Build.NoCache,
			Pull:             service.Build.Pull,
			Args:             service.Build.Args.ToMapping(),
		}
		if len(service.Build.Args) > 0 {
			buildCfg.Args = make(map[string]string, len(service.Build.Args))
			for k, v := range service.Build.Args {
				if v != nil {
					buildCfg.Args[k] = *v
				}
			}
		}
		cfg.Build = buildCfg
	}

	image, err := c.Resource(client.KindImage, imageName, cfg)
	if err != nil {
		return nil, errors.Join(errs, err)
	}

	img, ok := image.(*client.Image)
	if !ok {
		return nil, errors.Join(errs, errors.New("not an image"))
	}
	img.Config.Services = append(img.Config.Services, service.Name)

	return image, errs
}

// instanceNetworkDevices builds the NIC devices (eth0, eth1, ...) for a service's
// networks along with the network resources they reference.
func instanceNetworkDevices(c *client.Client, p *types.Project, service types.ServiceConfig) ([]client.InstanceDevice, []client.Resource, error) {
	var errs error
	devices := []client.InstanceDevice{}
	resources := []client.Resource{}

	// service.Networks is a map, so order it deterministically and place any
	// gateway-marked attachment (x-incus-compose.gateway: true) last, giving its
	// NIC the highest eth index. Incus uses the last NIC's gateway as the
	// instance's default route.
	names := make([]string, 0, len(service.Networks))
	for name := range service.Networks {
		names = append(names, name)
	}
	slices.SortStableFunc(names, func(a, b string) int {
		ga := serviceNetworkGateway(service.Networks[a])
		gb := serviceNetworkGateway(service.Networks[b])
		if ga != gb {
			if ga {
				return 1
			}
			return -1
		}
		return strings.Compare(a, b)
	})

	ethIdx := 0
	for _, name := range names {
		sNet := service.Networks[name]

		netConfig := &client.NetworkConfig{}
		if networkDef, ok := p.Networks[name]; ok {
			netConfig.External = bool(networkDef.External)
			netConfig.Extensions = networkExtensions(networkDef)
			netConfig.OverrideName = xICInstanceNetwork(networkDef)
		}

		extensions := map[string]string{}
		if sNet != nil && sNet.Extensions != nil {
			extensions = xIncusExtensions(sNet.Extensions)
		}

		network, err := c.Resource(client.KindNetwork, name, netConfig)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		nicConfig := client.InstanceDeviceConfig{
			DeviceType: client.InstanceDeviceTypeNic,
			Network:    network,
			Extensions: extensions,
		}

		if sNet != nil {
			nicConfig.Ipv4Address = sNet.Ipv4Address
			nicConfig.Ipv6Address = sNet.Ipv6Address
		}

		devices = append(devices, client.InstanceDevice{
			Name:   fmt.Sprintf("eth%d", ethIdx),
			Config: nicConfig,
		})
		ethIdx++

		resources = append(resources, network)
	}

	return devices, resources, errs
}

// serviceNetworkGateway reports whether a service network attachment is marked as
// the default-gateway interface via x-incus-compose.gateway: true. Such a NIC is
// placed last so Incus uses its gateway as the instance's default route.
func serviceNetworkGateway(sNet *types.ServiceNetworkConfig) bool {
	if sNet == nil {
		return false
	}
	var ext struct {
		Gateway bool `mapstructure:"gateway"`
	}
	ok, err := sNet.Extensions.Get("x-incus-compose", &ext)
	if !ok || err != nil {
		return false
	}
	return ext.Gateway
}

// instanceProxyDevices builds proxy devices for published ports and nat-proxy
// entries. Userspace proxy devices are returned for immediate attachment;
// NAT proxy devices are returned separately as post-start devices because their
// connect address is resolved once the instance is running. nicDevices is used
// to resolve bridge listen addresses and to verify a managed NIC exists.
func instanceProxyDevices(c *client.Client, service types.ServiceConfig, nicDevices []client.InstanceDevice) ([]client.InstanceDevice, []client.InstanceDevice, error) {
	var errs error
	devices := []client.InstanceDevice{}
	postStartDevices := []client.InstanceDevice{}

	// natProxyEntries maps listen-port -> {listen IPs, connect port}.
	type natProxyEntry struct {
		listen  []string
		connect uint32
	}
	natProxyEntries := map[uint32]natProxyEntry{}

	// Extract nat-proxy configuration from x-incus-compose extension
	if xIncusCompose := serviceXIncusComposeExtensions(service); xIncusCompose != nil {
		if rawList, ok := xIncusCompose["nat-proxy"].([]any); ok {
			for _, item := range rawList {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				var lPort uint64
				switch v := entry["port"].(type) {
				case int:
					lPort = uint64(v)
				case float64:
					lPort = uint64(v)
				case string:
					var portErr error
					lPort, portErr = strconv.ParseUint(v, 10, 32)
					if portErr != nil {
						errs = errors.Join(errs, fmt.Errorf("nat-proxy port %q is not a number: %w", v, portErr))
						continue
					}
				}
				var connectPort uint32
				switch v := entry["connect"].(type) {
				case int:
					connectPort = uint32(v)
				case float64:
					connectPort = uint32(v)
				}
				var listenIPs []string
				if rawListen, ok := entry["listen"].([]any); ok {
					for _, ip := range rawListen {
						if s, ok := ip.(string); ok {
							listenIPs = append(listenIPs, s)
						}
					}
				}
				natProxyEntries[uint32(lPort)] = natProxyEntry{listen: listenIPs, connect: connectPort}
			}
		}
	}

	// Resolve empty listen lists from the project's bridge network addresses.
	// Collect NIC-referenced network names once, reuse for all unspecified entries.
	var bridgeAddrs []string
	for lPort, entry := range natProxyEntries {
		if len(entry.listen) > 0 {
			continue
		}
		if bridgeAddrs == nil {
			for _, dev := range nicDevices {
				if dev.Config.DeviceType != client.InstanceDeviceTypeNic || dev.Config.Network == nil {
					continue
				}
				v4, v6, err := c.Global().NetworkBridgeIPs(dev.Config.Network.IncusName())
				if err != nil {
					c.LogWarn("nat-proxy: could not get bridge IPs", "network", dev.Config.Network.IncusName(), "err", err)
					continue
				}
				bridgeAddrs = append(bridgeAddrs, v4...)
				bridgeAddrs = append(bridgeAddrs, v6...)
			}
			if len(bridgeAddrs) == 0 {
				bridgeAddrs = []string{"0.0.0.0"}
			}
		}
		entry.listen = bridgeAddrs
		natProxyEntries[lPort] = entry
	}

	for _, port := range service.Ports {
		lPort, err := strconv.ParseUint(port.Published, 10, 32)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("bad publishing port %q must be a number: %w", port.Published, err))
			continue
		}

		// A nat-proxy entry for this listen port takes over -- skip the userspace proxy.
		if _, covered := natProxyEntries[uint32(lPort)]; covered {
			continue
		}

		proto := port.Protocol
		if proto == "" {
			proto = "tcp"
		}

		listenIP := port.HostIP
		if listenIP == "" {
			listenIP = "0.0.0.0"
		}

		devices = append(devices, client.InstanceDevice{
			Name: fmt.Sprintf("proxy-%d", lPort),
			Config: client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeProxy,
				Proxy: client.InstanceDeviceProxyConfig{
					ListenType:  proto,
					ListenAddr:  listenIP,
					ListenPort:  uint32(lPort),
					ConnectType: proto,
					ConnectAddr: "127.0.0.1",
					ConnectPort: port.Target,
				},
			},
		})
	}

	// Create NAT proxy devices -- one per listen IP per nat-proxy entry.
	// connect.addr is left empty and resolved in attachPostStartDevices once the instance is running.
	hasNic := false
	for _, dev := range nicDevices {
		if dev.Config.DeviceType == client.InstanceDeviceTypeNic {
			hasNic = true
			break
		}
	}

	for lPort, entry := range natProxyEntries {
		if !hasNic {
			c.LogWarn("nat-proxy requested but no managed NIC found, skipping", "service", service.Name, "port", lPort)
			continue
		}
		for idx, listenIP := range entry.listen {
			postStartDevices = append(postStartDevices, client.InstanceDevice{
				Name: fmt.Sprintf("proxy-%d-%d", lPort, idx),
				Config: client.InstanceDeviceConfig{
					DeviceType: client.InstanceDeviceTypeProxy,
					Proxy: client.InstanceDeviceProxyConfig{
						ListenType:  "tcp",
						ListenAddr:  listenIP,
						ListenPort:  lPort,
						ConnectType: "tcp",
						ConnectAddr: "", // resolved in attachPostStartDevices
						ConnectPort: entry.connect,
						Nat:         true,
					},
				},
			})
		}
	}

	return devices, postStartDevices, errs
}

// instanceVolumeDevices builds disk, bind, and tmpfs devices for a service's
// volumes plus the shm_size tmpfs. It returns any storage volume resources
// (when options.StorageVolumes is set) and the files map for single-file binds.
func instanceVolumeDevices(c *client.Client, p *types.Project, service types.ServiceConfig, image client.Resource, uid, gid uint64, options *ResourcesOptions) ([]client.InstanceDevice, []client.InstanceFile, []client.Resource, error) {
	var errs error
	devices := []client.InstanceDevice{}
	resources := []client.Resource{}
	files := []client.InstanceFile{}

	for _, cVol := range service.Volumes {
		seed := false
		if cVol.Extensions != nil {
			var ext xICInstanceVolume
			ok, err := cVol.Extensions.Get("x-incus-compose", &ext)
			if err != nil {
				return nil, nil, nil, err
			}

			if ok {
				seed = ext.Seed
			}
		}

		if cVol.Type == "" {
			// Infer type from source path (short syntax compatibility)
			// Absolute or relative paths are bind mounts, named sources are volumes
			if cVol.Source != "" && (strings.HasPrefix(cVol.Source, "/") || strings.HasPrefix(cVol.Source, ".")) {
				cVol.Type = "bind"
			} else if cVol.Source != "" {
				cVol.Type = "volume"
			}
		}

		extensions := xIncusExtensions(p.Volumes[cVol.Source].Extensions)

		// Inline x-incus on the volume entry takes precedence over the named
		// volume definition (this is the only place binds can set it).
		for k, v := range xIncusExtensions(cVol.Extensions) {
			if extensions == nil {
				extensions = map[string]string{}
			}
			extensions[k] = v
		}

		shifted := true
		es, ok := extensions["security.shifted"]
		if ok && es != "true" {
			shifted = false
		}

		switch cVol.Type {
		case "volume":
			volDef := p.Volumes[cVol.Source]

			pool := volumeXIncusComposePool(volDef)
			if pool == "" {
				pool = c.Config().DefaultStoragePool
			}

			volConfig := &client.StorageVolumeConfig{
				Shifted:       shifted,
				ImageResource: image,
				UID:           uid,
				GID:           gid,
				Pool:          pool,
				Extensions:    extensions,
			}

			v, err := c.Resource(client.KindStorageVolume, cVol.Source, volConfig)
			if err != nil {
				errs = errors.Join(errs, err)
				continue
			}

			resources = append(resources, v)

			devName := "vol-" + client.SanitizeIncusName(cVol.Source, client.MaxIncusNameLen-4)
			devConfig := client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeDisk,
				Disk: client.InstanceDeviceDiskConfig{
					StorageVolumeConfig: volConfig,
					Source:              v.IncusName(),
					Path:                cVol.Target,
					Shift:               shifted,
				},
			}

			if cVol.ReadOnly {
				devConfig.Disk.ReadOnly = true
			}

			devices = append(devices, client.InstanceDevice{Name: devName, Config: devConfig})
		case "bind":
			if seed {
				c.LogDebug("Will seed", "service", service.Name, "source", cVol.Source, "target", cVol.Target)

				info, err := os.Stat(cVol.Source)
				if err != nil {
					return nil, nil, nil, client.ErrUnknown.WithKindName(client.KindInstance, service.Name).Wrap(err)
				}

				if !info.IsDir() {
					files = append(files, client.InstanceFile{
						Target:    cVol.Target,
						File:      cVol.Source,
						UID:       -1,
						GID:       -1,
						Mode:      0o644,
						DirMode:   0o755,
						Overwrite: true,
					})
				} else {
					devName := "vol-seed-" + client.SanitizeIncusName(cVol.Source, client.MaxIncusNameLen-10)

					volConfig := &client.StorageVolumeConfig{
						Shifted:       shifted,
						ImageResource: image,
						UID:           uid,
						GID:           gid,
						HostPath:      cVol.Source,
						Pool:          c.Config().DefaultStoragePool,
					}

					v, err := c.Resource(client.KindStorageVolume, "bind-"+cVol.Source, volConfig)
					if err != nil {
						errs = errors.Join(errs, err)
						continue
					}

					resources = append(resources, v)

					devConfig := client.InstanceDeviceConfig{
						DeviceType: client.InstanceDeviceTypeDisk,
						Disk: client.InstanceDeviceDiskConfig{
							StorageVolumeConfig: volConfig,
							Source:              v.IncusName(),
							Path:                cVol.Target,
							Shift:               shifted,
						},
					}

					if cVol.ReadOnly {
						devConfig.Disk.ReadOnly = true
					}

					devices = append(devices, client.InstanceDevice{Name: devName, Config: devConfig})
				}
			} else {
				// Refuse bind without seed on remote hosts.
				err := c.Global().SameHost()
				if err != nil {
					return nil, nil, nil, fmt.Errorf("failed to add a bind-mount for service %v: %w", service.Name, err)
				}

				_, err = os.Stat(cVol.Source)
				if err != nil {
					return nil, nil, nil, client.ErrUnknown.WithKindName(client.KindInstance, service.Name).Wrap(err)
				}

				devName := "bind-" + client.SanitizeIncusName(cVol.Source, client.MaxIncusNameLen-5)

				devConfig := client.InstanceDeviceConfig{
					DeviceType: client.InstanceDeviceTypeDisk,
					Disk: client.InstanceDeviceDiskConfig{
						Source: cVol.Source,
						Path:   cVol.Target,
						Shift:  shifted,
					},
				}

				if cVol.ReadOnly {
					devConfig.Disk.ReadOnly = true
				}

				devices = append(devices, client.InstanceDevice{Name: devName, Config: devConfig})
			}
		case "tmpfs":
			devName := fmt.Sprintf("tmpfs-%s", strings.ReplaceAll(cVol.Target, "/", "-"))
			devConfig := client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeTmpfs,
				Tmpfs: client.InstanceDeviceTmpfsConfig{
					Path: cVol.Target,
					Size: formatTmpfsSize(cVol.Tmpfs),
				},
			}
			devices = append(devices, client.InstanceDevice{Name: devName, Config: devConfig})
		default:
			err := fmt.Errorf("Unknown volume type %q for service %q", cVol.Type, service.Name)
			errs = errors.Join(errs, err)
			continue
		}
	}

	// Another declaration for tmpfs devices.
	if len(service.Tmpfs) > 0 {
		for idx, tmpfsPath := range service.Tmpfs {
			devices = append(devices, client.InstanceDevice{
				Name: fmt.Sprintf("tmpfs-%d", idx),
				Config: client.InstanceDeviceConfig{
					DeviceType: client.InstanceDeviceTypeTmpfs,
					Tmpfs: client.InstanceDeviceTmpfsConfig{
						Path: tmpfsPath,
						Size: strconv.FormatInt(32*1024, 10),
					},
				},
			})
		}
	}

	// shm_size mounts a tmpfs at /dev/shm with the specified size.
	if service.ShmSize > 0 {
		devices = append(devices, client.InstanceDevice{
			Name: "shm",
			Config: client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeTmpfs,
				Tmpfs: client.InstanceDeviceTmpfsConfig{
					Path: "/dev/shm",
					Size: strconv.FormatInt(int64(service.ShmSize), 10),
				},
			},
		})
	}

	return devices, files, resources, errs
}

// instanceSecrets resolves a service's secrets from their compose definitions,
// reading content from a file or an environment variable.
func instanceSecrets(p *types.Project, service types.ServiceConfig) ([]client.InstanceFile, error) {
	var errs error
	result := []client.InstanceFile{}

	for _, svcSecret := range service.Secrets {
		secretDef, ok := p.Secrets[svcSecret.Source]
		if !ok {
			errs = errors.Join(errs, fmt.Errorf("secret %q not defined", svcSecret.Source))
			continue
		}

		switch {
		case secretDef.File != "":
			fp, err := os.Open(secretDef.File)
			if err != nil {
				errs = errors.Join(errs, fmt.Errorf("secret '%v' source not found or not readable", secretDef.File))
				continue
			}
			result = append(result, client.InstanceFile{
				Target:  svcSecret.Target,
				Content: fp,
				UID:     parseSecretID(svcSecret.UID),
				GID:     parseSecretID(svcSecret.GID),
				Mode:    parseSecretMode(svcSecret.Mode),
			})
		case secretDef.Environment != "":
			value, ok := p.Environment[secretDef.Environment]
			if !ok {
				errs = errors.Join(errs, fmt.Errorf("secret '%v' not found in the environment", secretDef.Environment))
				continue
			}

			result = append(result, client.InstanceFile{
				Target:  svcSecret.Target,
				Content: client.NewReaderFromBytes([]byte(value)),
				UID:     parseSecretID(svcSecret.UID),
				GID:     parseSecretID(svcSecret.GID),
				Mode:    parseSecretMode(svcSecret.Mode),
			})
		default:
			errs = errors.Join(errs, fmt.Errorf("secret '%v' has no source (file or environment)", svcSecret.Source))
			continue
		}
	}

	return result, errs
}

// instanceDependencyWaits builds the health-wait map for depends_on entries with
// condition: service_healthy, keyed by the dependency's sanitized instance names.
func instanceDependencyWaits(p *types.Project, service types.ServiceConfig, options *ResourcesOptions) map[string]string {
	deps := map[string]string{}
	for depName, dep := range service.DependsOn {
		if dep.Condition != types.ServiceConditionHealthy {
			continue
		}
		depSvc := p.Services[depName]
		depScale := 1
		if s, ok := options.Scale[depName]; ok {
			depScale = s
		} else if depSvc.Deploy != nil && depSvc.Deploy.Replicas != nil {
			depScale = int(*depSvc.Deploy.Replicas)
		}
		if depSvc.ContainerName != "" {
			deps[client.SanitizeIncusName(depSvc.ContainerName, client.MaxIncusNameLen)] = client.HealthStatusHealthy
		} else {
			for i := 1; i <= depScale; i++ {
				deps[client.SanitizeIncusName(fmt.Sprintf("%s-%d", depName, i), client.MaxIncusNameLen)] = client.HealthStatusHealthy
			}
		}
	}

	return deps
}

// instanceName derives the instance name: container_name takes precedence,
// otherwise {service}-{index}. A container_name with scale > 1 is suffixed with
// the index to keep names unique.
func instanceName(service types.ServiceConfig, index, scale int) string {
	name := fmt.Sprintf("%s-%d", service.Name, index)
	if service.ContainerName != "" {
		if scale > 1 {
			name = fmt.Sprintf("%s-%d", service.ContainerName, index)
		} else {
			name = service.ContainerName
		}
	}
	return name
}

// applyResourceLimits maps Docker Compose deploy.resources.limits to Incus config keys.
//
// CPU mapping:
//   - Integer cpus (e.g. 2.0): limits.cpu = "2" (pin to N CPUs)
//   - Fractional cpus (e.g. 0.5): limits.cpu.allowance = "50%"
//
// Memory mapping: limits.memory = human-readable size (GiB, MiB, KiB, or B).
func applyResourceLimits(config map[string]string, limits *types.Resource) {
	if limits == nil {
		return
	}
	if limits.NanoCPUs != 0 {
		cpus := limits.NanoCPUs.Value()
		if cpus == float32(int(cpus)) {
			config["limits.cpu"] = strconv.Itoa(int(cpus))
		} else {
			config["limits.cpu.allowance"] = fmt.Sprintf("%.0f%%", float64(cpus)*100)
		}
	}
	if limits.MemoryBytes != 0 {
		config["limits.memory"] = formatMemoryLimit(int64(limits.MemoryBytes))
	}
}

// formatMemoryLimit converts bytes to a human-readable Incus memory limit string.
func formatMemoryLimit(bytes int64) string {
	const (
		gib = 1 << 30
		mib = 1 << 20
		kib = 1 << 10
	)
	switch {
	case bytes%gib == 0:
		return fmt.Sprintf("%dGiB", bytes/gib)
	case bytes%mib == 0:
		return fmt.Sprintf("%dMiB", bytes/mib)
	case bytes%kib == 0:
		return fmt.Sprintf("%dKiB", bytes/kib)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// applyRestartPolicy maps Docker Compose restart policies to Incus boot config.
//
// Mapping:
//   - "no" (default): boot.autostart=false
//   - "always": boot.autostart=true
//   - "on-failure": boot.autostart=true, boot.autorestart=true
//   - "unless-stopped": boot.autostart unset (uses last-state behavior)
func applyRestartPolicy(config map[string]string, policy string) {
	switch policy {
	case "always":
		config["boot.autostart"] = "true"
	case "on-failure":
		config["boot.autostart"] = "true"
		config["boot.autorestart"] = "true"
	case "unless-stopped":
		// Leave unset - Incus defaults to "last-state" behavior
	case "no", "":
		config["boot.autostart"] = "false"
	}
}

// formatTmpfsSize converts compose tmpfs size to a string.
func formatTmpfsSize(opts *types.ServiceVolumeTmpfs) string {
	if opts == nil || opts.Size == 0 {
		return ""
	}
	return strconv.FormatInt(int64(opts.Size), 10)
}

// parseSecretID parses a UID string to int64.
func parseSecretID(id string) int64 {
	if id == "" {
		return -1
	}
	v, _ := strconv.ParseInt(id, 10, 64)
	return v
}

// parseSecretMode parses a file mode to int.
func parseSecretMode(mode *types.FileMode) int {
	if mode == nil {
		return 0o600
	}
	return int(*mode)
}

// xICInstanceNetwork extracts the x-incus-compose.network string override
// from a compose network definition. Returns "" if not set.
func xICInstanceNetwork(networkDef types.NetworkConfig) string {
	var raw map[string]any
	ok, err := networkDef.Extensions.Get("x-incus-compose", &raw)
	if !ok || err != nil {
		return ""
	}
	n, ok := raw["network"].(string)
	if !ok {
		return ""
	}
	return n
}

// networkExtensions extracts the x-incus extension map from a compose network
// definition and returns it as a flat map[string]string for use as Incus network
// config. Keys and values are taken verbatim from the x-incus YAML block.
func networkExtensions(networkDef types.NetworkConfig) map[string]string {
	var raw map[string]any
	ok, err := networkDef.Extensions.Get("x-incus", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = fmt.Sprint(v)
	}

	return result
}

// xIncusExtensions extracts the x-incus extension map from a compose
// volume definition or inline volume entry and returns it as a flat
// map[string]string for use as Incus volume config. Keys and values are taken
// verbatim from the x-incus YAML block.
func xIncusExtensions(ext types.Extensions) map[string]string {
	var raw map[string]any
	ok, err := ext.Get("x-incus", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = fmt.Sprint(v)
	}

	return result
}

// volumeXIncusComposePool extracts the pool name from x-incus-compose.pool on a
// compose volume definition.
func volumeXIncusComposePool(volDef types.VolumeConfig) string {
	var raw map[string]any
	ok, err := volDef.Extensions.Get("x-incus-compose", &raw)
	if !ok || err != nil {
		return ""
	}
	pool, ok := raw["pool"].(string)
	if !ok {
		return ""
	}
	return pool
}

// serviceXIncusExtensions extracts the x-incus extension map from a compose service
// definition and returns it as a flat map[string]string for use as Incus instance
// config. Keys and values are taken verbatim from the x-incus YAML block.
func serviceXIncusExtensions(service types.ServiceConfig) map[string]string {
	var raw map[string]any
	ok, err := service.Extensions.Get("x-incus", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = fmt.Sprint(v)
	}

	return result
}

// serviceXIncusComposeExtensions extracts the x-incus-compose extension map from
// a compose service definition. This is for compose-specific features and
// transformations handled by incus-compose (not raw Incus options).
func serviceXIncusComposeExtensions(service types.ServiceConfig) map[string]any {
	var raw map[string]any
	ok, err := service.Extensions.Get("x-incus-compose", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	return raw
}

// serviceExtraDevices extracts raw Incus devices from the x-incus-compose.devices
// block on a compose service. Each named entry becomes an instance device whose
// keys are passed verbatim to Incus; the `type` key selects the device type.
func serviceExtraDevices(service types.ServiceConfig) ([]client.InstanceDevice, error) {
	var raw map[string]any
	ok, err := service.Extensions.Get("x-incus-compose", &raw)
	if !ok || err != nil {
		return nil, nil
	}

	devicesRaw, ok := raw["devices"].(map[string]any)
	if !ok || len(devicesRaw) == 0 {
		return nil, nil
	}

	devices := make([]client.InstanceDevice, 0, len(devicesRaw))
	for name, cfg := range devicesRaw {
		cfgMap, ok := cfg.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("x-incus-compose.devices: device %q must be a map", name)
		}

		ext := make(map[string]string, len(cfgMap))
		for k, v := range cfgMap {
			ext[k] = fmt.Sprint(v)
		}

		if ext["type"] == "" {
			return nil, fmt.Errorf("x-incus-compose.devices: device %q is missing 'type'", name)
		}

		devices = append(devices, client.InstanceDevice{
			Name: name,
			Config: client.InstanceDeviceConfig{
				DeviceType: ext["type"],
				Extensions: ext,
			},
		})
	}

	return devices, nil
}

// formatCommand shell-quotes a command slice for appending to oci.entrypoint.
// Every element is an argument (the binary comes from the image entrypoint), so
// all are quoted; Incus splits the result back with shellquote.Split.
func formatCommand(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	if len(cmd) == 1 {
		return cmd[0]
	}

	quoted := make([]string, len(cmd))
	for i := range cmd {
		quoted[i] = `"` + cmd[i] + `"`
	}
	return strings.Join(quoted, " ")
}
