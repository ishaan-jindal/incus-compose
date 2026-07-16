package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

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
				Name:  "pull",
				Usage: `Pull image before running ("always"|"missing"|"never"|"policy")`,
				Value: "policy",
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping",
				Value: 10 * time.Second,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			noColor := noColor(ctx)

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

			if !healthdInUseByProject(globalClient, p) {
				globalClient.LogError("No service in this project declares a healthcheck")
				return errLogged.Wrap(errors.New("no service"))
			}

			healthdIncus := p.ClientConfig.Healthd.Incus
			healthdNetwork := p.ClientConfig.Healthd.Network
			if cmd.String("incus") != "" {
				healthdIncus = cmd.String("incus")
			}
			if cmd.String("network") != "" {
				healthdNetwork = cmd.String("network")
			}

			var incus *url.URL
			if healthdIncus != "" {
				incus, err = url.Parse(healthdIncus)
				if err != nil {
					globalClient.LogError("Parsing the URL given with `--incus` failed", "error", err)
					return errLogged.Wrap(errors.New("parsing error"))
				}
			}

			params := healthdParams{
				projectName: p.Name,
				binary:      cmd.String("binary"),
				image:       resolveHealthdImage(cmd.String("image")),
				pull:        cmd.String("pull"),
				incus:       incus,
				network:     healthdNetwork,
				timeout:     cmd.Duration("timeout"),
				workers:     cmd.Root().Int("workers"),
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

			stdout := cmd.Root().Writer

			if !cmd.Root().Bool("debug") {
				progress := newProgressRenderer(stdout, noColor, isatty.IsTerminal(os.Stdout.Fd()))
				progress.Start(c)
				defer progress.Stop(c)

				stdout = progress.bypass()
			}

			stack := client.NewStack(c, client.StackWorkers(params.workers))

			// healthdGetResources needs it network configured.
			{
				pResources, err := p.Resources(c)
				if err != nil {
					c.LogError("Getting the service resources", "error", err)
					return errLogged.Wrap(err)
				}

				args := filterResourcesArgs{
					IncludeKinds: []client.Kind{client.KindNetwork},
				}
				myPResources := filterResources(p, pResources, args)

				order, err := p.ServiceOrder(true)
				if err != nil {
					c.LogError("Getting the service dependency order", "error", err)
					return errLogged.Wrap(err)
				}
				stack.AddOrdered(order, myPResources)
			}

			hInst, hResources, err := healthdGetResources(c, params)
			if err != nil {
				globalClient.LogError("Creating healthd resources", "error", err)
				return errLogged.Wrap(err)
			}

			stack.Add(hResources...)
			stack.Add(hInst)

			c.LogDebug("Ensure", "resources", stack.All())

			ensureOpts := []client.Option{client.OptionCreate()}
			if params.pull == "always" {
				ensureOpts = append(ensureOpts, client.OptionPull())
			}

			if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, stdout, cmd.Root().ErrWriter, ensureOpts...); err != nil {
				c.LogError("Creating healthd resources", "error", err)
				return errLogged.Wrap(err)
			}

			if err := stack.ForAction(client.ActionStart).Run(ctx, client.ActionStart, stdout, cmd.Root().ErrWriter); err != nil {
				c.LogError("Starting healthd resources", "error", err)
				return errLogged.Wrap(err)
			}

			return nil
		},
	}
}
