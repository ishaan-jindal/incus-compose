package main

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

// healthdUpArgs holds the healthdUp() options, mirroring the `healthd up` command's flags.
type healthdUpArgs struct {
	Binary  string
	Image   string // raw --image flag value; resolved via resolveHealthdImage inside healthdUp.
	Incus   string // raw --incus/--healthd-incus override; empty keeps the project default.
	Network string // raw --network/--healthd-network override; empty keeps the project default.
	Pull    string
	Timeout time.Duration
	Workers int
	Debug   bool
	Writer  io.Writer
}

// healthdUp creates or recreates the project's ic-healthd sidecar.
func healthdUp(ctx context.Context, p *project.Project, c *client.Client, args healthdUpArgs) error {
	if !healthdInUseByProject(c.Global(), p) {
		c.LogError("No service in this project declares a healthcheck")
		return errLogged.Wrap(errors.New("no service"))
	}

	noColor := noColor(ctx)

	healthdIncus := p.ClientConfig.Healthd.Incus
	healthdNetwork := p.ClientConfig.Healthd.Network
	if args.Incus != "" {
		healthdIncus = args.Incus
	}
	if args.Network != "" {
		healthdNetwork = args.Network
	}

	var incus *url.URL
	if healthdIncus != "" {
		var err error
		incus, err = url.Parse(healthdIncus)
		if err != nil {
			c.LogError("Parsing the healthd incus URL failed", "error", err)
			return errLogged.Wrap(errors.New("parsing error"))
		}
	}

	params := healthdParams{
		projectName: p.Name,
		binary:      args.Binary,
		image:       resolveHealthdImage(args.Image),
		pull:        args.Pull,
		incus:       incus,
		network:     healthdNetwork,
		timeout:     args.Timeout,
		workers:     args.Workers,
	}

	if !args.Debug {
		progress := newProgressRenderer(args.Writer, noColor, isatty.IsTerminal(os.Stdout.Fd()))
		progress.Start(c)
		defer progress.Stop(c)
	}

	stack := client.NewStack(c, client.StackWorkers(params.workers))

	// healthdGetResources needs its network configured.
	{
		pResources, err := p.Resources(c)
		if err != nil {
			c.LogError("Getting the service resources", "error", err)
			return errLogged.Wrap(err)
		}

		filterArgs := filterResourcesArgs{
			IncludeKinds: []client.Kind{client.KindNetwork},
		}
		myPResources := filterResources(p, pResources, filterArgs)

		order, err := p.ServiceOrder(true)
		if err != nil {
			c.LogError("Getting the service dependency order", "error", err)
			return errLogged.Wrap(err)
		}
		stack.AddOrdered(order, myPResources)
	}

	hInst, hResources, err := healthdGetResources(c, params)
	if err != nil {
		c.LogError("Creating healthd resources", "error", err)
		return errLogged.Wrap(err)
	}

	stack.Add(hResources...)
	stack.Add(hInst)

	c.LogDebug("Ensure", "resources", stack.All())

	ensureOpts := []client.Option{client.OptionCreate(), client.OptionTimeout(params.timeout)}
	if params.pull == "always" {
		ensureOpts = append(ensureOpts, client.OptionPull())
	}

	if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, ensureOpts...); err != nil {
		c.LogError("Creating healthd resources", "error", err)
		return errLogged.Wrap(err)
	}

	if err := stack.ForAction(client.ActionStart).Run(ctx, client.ActionStart, client.OptionTimeout(params.timeout)); err != nil {
		c.LogError("Starting healthd resources", "error", err)
		return errLogged.Wrap(err)
	}

	return nil
}

func newHealthdUpCommand() *cli.Command {
	return &cli.Command{
		Name:  "up",
		Usage: "Create or recreate the ic-healthd sidecar",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "image",
				Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
				Value:   DefaultHealthdImage,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
			},
			&cli.StringFlag{
				Name:    "binary",
				Usage:   "Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)",
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_BINARY"),
			},
			&cli.StringFlag{
				Name:    "incus",
				Usage:   `Connection URL of the incus to connect to from inside the sidecar. Empty = detect the ip from the bridge we are connected too`,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_INCUS"),
			},
			&cli.StringFlag{
				Name:    "network",
				Usage:   "Incus bridge for healthd to use (default: auto-detect)",
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_NETWORK"),
			},
			&cli.StringFlag{
				Name:    "pull",
				Usage:   `Pull image before running ("always"|"missing"|"never"|"policy")`,
				Value:   "policy",
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_PULL"),
			},
			&cli.DurationFlag{
				Name:    "timeout",
				Usage:   "Timeout for creating and starting",
				Value:   10 * time.Second,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_TIMEOUT"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			globalClient, err := clientFromContext(ctx)
			if err != nil {
				return err
			}
			if err := globalClient.Connect(); err != nil {
				return err
			}

			p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
			if err != nil {
				globalClient.LogError("Configuring the project", "error", err)
				return errLogged.Wrap(err)
			}

			c, err := globalClient.EnsureProject(
				p.Name,
				client.EnsureProjectWithCreate(),
				client.EnsureProjectWithConfig(p.ClientConfig.XIncus),
			)
			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged.Wrap(err)
			}
			defer c.WarnError(c.Done, "Failure during Client.Done()")

			return healthdUp(ctx, p, c, healthdUpArgs{
				Binary:  cmd.String("binary"),
				Image:   cmd.String("image"),
				Incus:   cmd.String("incus"),
				Network: cmd.String("network"),
				Pull:    cmd.String("pull"),
				Timeout: cmd.Duration("timeout"),
				Workers: cmd.Root().Int("workers"),
				Debug:   cmd.Root().Bool("debug"),
				Writer:  cmd.Root().Writer,
			})
		},
	}
}
