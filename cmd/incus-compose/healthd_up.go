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
				Value:   defaultHealthdImage,
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
			&cli.BoolFlag{
				Name:  "recreate",
				Usage: "Recreate the sidecar even if it already exists",
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

			healthdIncus, healthdNetwork := p.HealthdConfig()
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
				reCreate:    cmd.Bool("recreate"),
				incus:       incus,
				network:     healthdNetwork,
				timeout:     cmd.Duration("timeout"),
				workers:     cmd.Root().Int("workers"),
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

			stdout := cmd.Root().Writer

			if !cmd.Root().Bool("debug") {
				progress := newProgressRenderer(stdout, noColor, isatty.IsTerminal(os.Stdout.Fd()))
				progress.Start(c)
				defer progress.Stop(c)

				stdout = progress.bypass()
			}

			if params.reCreate {
				existing, resources, err := healthdGetResources(c, params)
				if err == nil {
					rc := c.Clone()

					stack := client.NewStack(rc, client.StackSortDescending())

					for _, r := range resources {
						if r.Kind() != client.KindImage {
							stack.Add(r)
						}
					}
					stack.Add(existing)

					rc.LogDebug("Ensure", "resources", stack.All())

					// Do not recreate networks.
					recreateFilter := func(r client.Resource) bool {
						return r.Kind() != client.KindNetwork
					}

					if err := stack.ForActionF(client.ActionEnsure, recreateFilter).Run(ctx, client.ActionEnsure, stdout, cmd.Root().ErrWriter); err != nil {
						rc.LogWarn("Ensuring healthd", "error", err)
					}

					if err := stack.ForActionF(client.ActionStop, recreateFilter).Run(ctx, client.ActionStop, stdout, cmd.Root().ErrWriter, client.OptionForce(), client.OptionTimeout(cmd.Duration("timeout"))); err != nil {
						rc.LogWarn("Stopping healthd resources", "error", err)
					}

					if err := stack.ForActionF(client.ActionDelete, recreateFilter).Run(ctx, client.ActionDelete, stdout, cmd.Root().ErrWriter, client.OptionForce(), client.OptionTimeout(cmd.Duration("timeout"))); err != nil {
						rc.LogWarn("Deleting healthd resources", "error", err)
					}

					if err := healthdRevokeCert(c); err != nil {
						rc.LogWarn("Cannot revoke the healthd cert", "error", err)
					}
				}
			}

			stack := client.NewStack(c, client.StackWorkers(params.workers))

			resources, err := p.Resources(c)
			if err != nil {
				c.LogError("Getting project resources in reCreate", "error", err)
				return errLogged.Wrap(err)
			}

			for _, res := range resources {
				for _, r := range res {
					if r.Kind() == client.KindNetwork {
						stack.Add(r)
					}
				}
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
