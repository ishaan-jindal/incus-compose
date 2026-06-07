package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	incusApi "github.com/lxc/incus/v6/shared/api"
	"github.com/mattn/go-colorable"
	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

// healthdParams holds the image/binary options for healthd setup.
type healthdParams struct {
	projectName string
	binary      string
	image       string // already resolved via resolveHealthdImage
	reCreate    bool
	network     string // Incus bridge name; empty = auto-detect
}

// projectUsesHealthd reports whether any of the named services declares a healthcheck.
// If services is empty, all project services are checked.
func projectUsesHealthd(p *project.Project) bool {
	for _, svc := range p.Services {
		// https://github.com/compose-spec/compose-spec/blob/main/05-services.md#restart
		if svc.Restart != "no" {
			return true
		}

		if svc.HealthCheck != nil {
			return true
		}
	}
	return false
}

// mkHealthdStack resolves the sidecar image, builds HealthdConfig, and returns a stack containing all required resources.
func mkHealthdStack(globalClient *client.GlobalClient, c *client.Client, params healthdParams) (*client.Stack, error) {
	imageName := params.image
	if params.binary != "" {
		// Use system container when pushing local binary
		imageName = "images:alpine/edge"
	}

	c.LogDebug("Using healthd", "params", params)

	imageConfig := &client.ImageConfig{}
	imgRes, err := c.Resource(client.KindImage, imageName, imageConfig)
	if err != nil {
		return nil, fmt.Errorf("getting the healthd image '%v': %w", imageName, err)
	}

	volRes, err := c.Resource(
		client.KindStorageVolume,
		"ic-healthd",
		&client.StorageVolumeConfig{Shifted: true, ImageResource: imgRes},
	)
	if err != nil {
		return nil, client.ErrUnknown.WithKindName(client.KindStorageVolume, "ic-healthd").Wrap(err)
	}

	volume, ok := volRes.(*client.StorageVolume)
	if !ok {
		return nil, client.ErrUnknown.WithResource(volRes)
	}

	img, ok := imgRes.(*client.Image)
	if !ok {
		return nil, client.ErrUnknown.WithResource(imgRes)
	}

	config := &client.HealthdConfig{
		Binary:        params.binary,
		StorageVolume: volume,
		ImageResource: img,
		Network:       params.network,
	}

	healthd, err := c.Resource(client.KindHealthd, fmt.Sprintf("%s-ic-healthd", params.projectName), config)
	if err != nil {
		return nil, fmt.Errorf("getting the healthd resource: %w", err)
	}

	stack := client.NewStack(c)
	stack.Add(img, volume, healthd)

	return stack, nil
}

// resolveHealthd returns the existing Healthd resource or errors if the sidecar
// is not running. Used by management sub-commands that require ic-healthd to exist.
func resolveHealthd(c *client.Client) (*client.Healthd, error) {
	name, err := c.FindHealthdName()
	if err != nil {
		return nil, fmt.Errorf("finding healthd: %w", err)
	}
	if name == "" {
		return nil, errors.New("healthd is not running")
	}

	res, err := c.Resource(client.KindHealthd, name, &client.HealthdConfig{})
	if err != nil {
		return nil, err
	}
	h, ok := res.(*client.Healthd)
	if !ok {
		return nil, errors.New("unexpected resource type for healthd")
	}
	return h, nil
}

func startHealthd(c *client.Client, h *client.Healthd, params healthdParams) error {
	if err := h.Start(); err != nil {
		return err
	}

	if params.binary != "" {
		flags := []string{fmt.Sprintf(" --incus=%s --project=%s", c.Config().URL, c.IncusProject())}

		// Passthrough debugging.
		if c.IsDebugging() {
			flags = append(flags, " --debug")
		}

		// Wait for network to be ready, then run healthd in background.
		// The network device might not be fully configured when the container starts.
		cmd := []string{
			"sh", "-c",
			`nohup /usr/local/bin/ic-healthd run` + strings.Join(flags, " ") + `> /var/log/ic-healthd.log 2>&1 &`,
		}

		execReq := incusApi.InstanceExecPost{
			Command:     cmd,
			WaitForWS:   false,
			Interactive: false,
		}

		op, err := c.Connection().ExecInstance(h.IncusName(), execReq, nil)
		if err != nil {
			return err
		}

		if err := op.Wait(); err != nil {
			return err
		}

		return registerHealthdReloader(c, h)
	}

	return registerHealthdReloader(c, h)
}

type healthdReloaderState struct {
	existed bool
	changed bool
}

func snapshotHealthdReloader(c *client.Client, h *client.Healthd, st *healthdReloaderState) {
	_, _, err := c.Connection().GetInstance(h.IncusName())
	st.existed = err == nil
	c.LogDebug("HealthdReloader snapshot", "healthd", h.IncusName(), "existed", st.existed)
}

func healthdInstanceChanged(h *client.Healthd, action client.Action, r client.Resource, err error) bool {
	if err != nil || r.Kind() != client.KindInstance {
		return false
	}

	inst, ok := r.(*client.Instance)
	if !ok || inst.IncusName() == h.IncusName() {
		return false
	}

	switch action {
	case client.ActionEnsure:
		return inst.Created()
	case client.ActionStart, client.ActionStop, client.ActionDelete:
		return true
	default:
		return false
	}
}

func registerHealthdReloader(c *client.Client, h *client.Healthd) error {
	st := &healthdReloaderState{}
	snapshotHealthdReloader(c, h, st)

	c.AddHookConnected(func(err error) error {
		snapshotHealthdReloader(c, h, st)
		return err
	})

	c.AddHookAfter(func(action client.Action, r client.Resource, _ client.Options, err error) error {
		if healthdInstanceChanged(h, action, r, err) {
			st.changed = true
			c.LogDebug("HealthdReloader instance changed", "action", action, "instance", r.IncusName())
		}
		return err
	})

	c.AddHookDone(func(err error) error {
		c.LogDebug("HealthdReloader disconnecting", "healthd", h.IncusName(), "existed", st.existed, "changed", st.changed)
		if !st.existed || !st.changed {
			return err
		}

		state, _, e := c.Connection().GetInstanceState(h.IncusName())
		if e != nil {
			c.LogDebug("HealthdReloader healthd missing, skipping reload", "healthd", h.IncusName(), "error", e)
			return err
		}
		if state.StatusCode != incusApi.Running {
			c.LogDebug("HealthdReloader healthd not running, skipping reload", "healthd", h.IncusName(), "status", state.Status)
			return err
		}

		if e := reloadHealthd(c, h); e == nil {
			c.LogDebug("HealthdReloader reloaded healthd", "healthd", h.IncusName())
			return err
		}

		c.LogWarn("Reloading healthd failed, restarting", "healthd", h.IncusName(), "error", e)
		return errors.Join(err, e, h.Stop(client.OptionForce()), h.Start())
	})

	return nil
}

func reloadHealthd(c *client.Client, h *client.Healthd) error {
	req := incusApi.InstanceExecPost{
		Command:     []string{"sh", "-c", "pids=\"$(pidof ic-healthd)\" && for pid in $pids; do kill -HUP \"$pid\"; done"},
		WaitForWS:   false,
		Interactive: false,
	}

	op, err := c.Connection().ExecInstance(h.IncusName(), req, nil)
	if err != nil {
		return err
	}

	return op.Wait()
}

var healthdCommand = &cli.Command{
	Name:     "healthd",
	Usage:    "Manage the ic-healthd sidecar",
	Category: "extensions",
	Commands: []*cli.Command{
		healthdLogsCommand,
		healthdReloadCommand,
		healthdRestartCommand,
		healthdUpCommand,
		healthdDownCommand,
	},
}

var healthdLogsCommand = &cli.Command{
	Name:  "logs",
	Usage: "View output from the healthd sidecar",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "follow",
			Aliases: []string{"f"},
			Usage:   "Follow log output",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		c, err := globalClient.EnsureProject(p.Name)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}
		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Done() }()

		h, err := resolveHealthd(c)
		if err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		var out io.Writer
		if f, ok := cmd.Root().Writer.(*os.File); ok {
			out = colorable.NewColorable(f)
		} else {
			out = cmd.Root().Writer
		}
		formatter := newLogFormatter(out, noColor)
		formatter.registerService(h.IncusName())
		globalClient.SetOutputHandler(formatter.write)

		var opts []client.Option
		if cmd.Bool("follow") {
			opts = append(opts, client.OptionFollow())
		}

		if err := h.Log(opts...); err != nil {
			c.LogError("Getting healthd logs", "error", err)
			return errLogged.Wrap(err)
		}

		formatter.flush()
		return nil
	},
}

var healthdReloadCommand = &cli.Command{
	Name:  "reload",
	Usage: "Send SIGHUP to the ic-healthd process",
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		c, err := globalClient.EnsureProject(p.Name)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}
		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Done() }()

		h, err := resolveHealthd(c)
		if err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		if err := reloadHealthd(c, h); err != nil {
			c.LogError("Reloading healthd", "error", err)
			return errLogged.Wrap(err)
		}

		return nil
	},
}

var healthdRestartCommand = &cli.Command{
	Name:  "restart",
	Usage: "Restart the ic-healthd sidecar",
	Flags: []cli.Flag{
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for stopping",
			Value: 10,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		c, err := globalClient.EnsureProject(p.Name)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}
		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Done() }()

		h, err := resolveHealthd(c)
		if err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		timeout := int(cmd.Int("timeout"))
		if err := h.Stop(client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
			c.LogWarn("Stopping healthd", "error", err)
		}

		params := healthdParams{
			projectName: p.Name,
			binary:      "",
			image:       resolveHealthdImage(cmd.String("image")),
			reCreate:    false,
			network:     "auto",
		}
		if err := startHealthd(c, h, params); err != nil {
			c.LogError("Starting healthd", "error", err)
			return errLogged.Wrap(err)
		}

		return nil
	},
}

var healthdUpCommand = &cli.Command{
	Name:  "up",
	Usage: "Create or recreate the ic-healthd sidecar",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "image",
			Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
			Value:   client.DefaultHealthdImage,
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
		},
		&cli.StringFlag{
			Name:    "binary",
			Usage:   "Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)",
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_BINARY"),
		},
		&cli.StringFlag{
			Name:    "network",
			Usage:   "Incus bridge for healthd to use (default: auto-detect)",
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_NETWORK"),
		},
		&cli.BoolFlag{
			Name:  "recreate",
			Usage: "Recreate the sidecar even if it already exists",
		},
		&cli.StringFlag{
			Name:  "pull",
			Usage: `Pull image before running ("always"|"missing"|"never"|"policy")`,
			Value: "policy",
		},
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for stopping",
			Value: 10,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		if !projectUsesHealthd(p) {
			return fmt.Errorf("no service in this project declares a healthcheck")
		}

		params := healthdParams{
			projectName: p.Name,
			binary:      cmd.String("binary"),
			image:       resolveHealthdImage(cmd.String("image")),
			reCreate:    cmd.Bool("recreate"),
			network:     cmd.String("network"),
		}

		c, err := globalClient.EnsureProject(
			p.Name,
			client.EnsureProjectWithConfig(p.ProjectConfig()),
		)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}
		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Done() }()

		if params.network == "" {
			if _, _, err := c.Connection().GetNetwork("incusbr0"); err == nil {
				params.network = "incusbr0"
			} else {
				ip, err := c.ConnectionIP()
				if err != nil {
					globalClient.LogError("Getting the connection IP", "error", err)
					return errLogged.Wrap(err)
				}

				network, err := c.NetworkForIP(ip)
				if err != nil {
					globalClient.LogError("Getting the connection network", "error", err)
					return errLogged.Wrap(err)
				}

				params.network = network
			}
		}

		if params.reCreate {
			stack, err := mkHealthdStack(globalClient, c, params)
			if err != nil {
				globalClient.LogError("Creating the stack", "error", err)
				return errLogged.Wrap(err)
			}

			timeout := int(cmd.Int("timeout"))

			c.LogDebug("Ensure", "resources", stack.All())

			if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure); err != nil {
				c.LogDebug("Ensuring healthd in recreate", "error", err)
			} else {
				if err := stack.ForAction(client.ActionStop).Run(client.ActionStop, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
					c.LogDebug("Stopping healthd in recreate", "error", err)
				} else {
					if err := stack.ForAction(client.ActionDelete).Run(client.ActionDelete, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
						c.LogDebug("Deleting healthd in recreate", "error", err)
					}
				}
			}
		}

		// Create a new stack after Delete as stack entries are now invalid.
		stack, err := mkHealthdStack(globalClient, c, params)
		if err != nil {
			globalClient.LogError("Creating the stack", "error", err)
			return errLogged.Wrap(err)
		}

		c.LogDebug("Ensure", "resources", stack.All())

		// Ensure with create. --pull=always refreshes cached images from registry.
		ensureOpts := []client.Option{client.OptionCreate()}
		if cmd.String("pull") == "always" {
			ensureOpts = append(ensureOpts, client.OptionPull())
		}

		if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure, ensureOpts...); err != nil {
			c.LogError("Creating healthd", "error", err)
			return errLogged.Wrap(err)
		}

		h, err := resolveHealthd(c)
		if err != nil {
			c.LogError("Getting healthd after ensure", "error", err)
			return errLogged.Wrap(err)
		}

		if err := startHealthd(c, h, params); err != nil {
			c.LogError("Starting healthd", "error", err)
			return errLogged.Wrap(err)
		}

		if err := stack.ForAction(client.ActionStart).Run(client.ActionStart); err != nil {
			c.LogError("Starting healthd", "error", err)
			return errLogged.Wrap(err)
		}

		return nil
	},
}

var healthdDownCommand = &cli.Command{
	Name:  "down",
	Usage: "Stop and remove the ic-healthd sidecar",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "image",
			Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
			Value:   client.DefaultHealthdImage,
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
		},
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for stopping",
			Value: 10,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		params := healthdParams{
			projectName: p.Name,
			binary:      "",
			image:       resolveHealthdImage(cmd.String("image")),
			reCreate:    false,
			network:     "auto",
		}

		c, err := globalClient.EnsureProject(p.Name)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}
		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Done() }()

		stack, err := mkHealthdStack(globalClient, c, params)
		if err != nil {
			globalClient.LogError("Creating the stack", "error", err)
			return errLogged.Wrap(err)
		}

		c.LogDebug("Ensure", "resources", stack.All())

		if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure); err != nil {
			c.LogError("Ensuring healthd", "error", err)
			return errLogged.Wrap(err)
		}

		timeout := int(cmd.Int("timeout"))

		if err := stack.ForAction(client.ActionStop).Run(client.ActionStop, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
			c.LogError("Stopping healthd", "error", err)
			return errLogged.Wrap(err)
		}

		if err := stack.ForAction(client.ActionDelete).Run(client.ActionDelete, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
			c.LogError("Deleting healthd", "error", err)
			return errLogged.Wrap(err)
		}

		return nil
	},
}
