package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newPullCommand() *cli.Command {
	return &cli.Command{
		Name:      "pull",
		Usage:     "Pull service images",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "ignore-buildable",
				Usage: `Ignore images that can be built`,
			},
			&cli.BoolFlag{
				Name:  "ignore-pull-failures",
				Usage: `Pull what it can and ignores images with pull failures`,
			},
			&cli.BoolFlag{
				Name:    "include-deps",
				Aliases: []string{"with-deps"},
				Usage:   "Also pull linked services",
			},
			&cli.StringFlag{
				Name:  "policy",
				Usage: `Apply pull policy ("missing"|"always") - ignored just for compatibility now`,
				Value: "always",
			},
			&cli.BoolFlag{
				Name:  "no-healthd",
				Usage: "Don't pull the healthd sidecar",
			},
			&cli.StringFlag{
				Name:    "healthd-image",
				Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
				Value:   defaultHealthdImage,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			noColor := noColor(ctx)

			withDeps := cmd.Bool("include-deps")

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
				return err
			}

			c, err := globalClient.EnsureProject(
				p.Name,
				client.EnsureProjectWithCreate(),
				client.EnsureProjectWithConfig(p.ProjectConfig()),
			)
			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged
			}
			defer func() {
				_ = c.Done()
			}()

			err = c.Open()
			if err != nil {
				globalClient.LogError("Opening the project client", "error", err)
				return errLogged.Wrap(err)
			}

			c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

			stdout := cmd.Root().Writer
			stderr := cmd.Root().ErrWriter

			if !cmd.Root().Bool("debug") {
				progress := newProgressRenderer(stdout, noColor, isatty.IsTerminal(os.Stdout.Fd()))
				progress.Start(c)
				defer progress.Stop(c)

				stdout = progress.bypass()
				stderr = stdout
			}

			// Register the DNS Watcher after the progress renderer so progress waits for the dns changes.
			if err := c.RegisterDNSWatcher(); err != nil {
				globalClient.LogError("Registering the DNS watcher", "project", p.Name, "error", err)
				return errLogged.Wrap(err)
			}

			resources, err := p.Resources(c)
			if err != nil {
				c.LogError("Getting project resources in reCreate", "error", err)
				return errLogged.Wrap(err)
			}

			args := filterResourcesArgs{
				OnlyServices:     cmd.Args().Slice(),
				WithDependencies: withDeps,
			}
			myResources := filterResources(p, resources, args)

			var stack *client.Stack
			if cmd.Bool("ignore-pull-failures") {
				stack = client.NewStack(c, client.StackWorkers(cmd.Root().Int("workers")))
			} else {
				stack = client.NewStack(c, client.StackWorkers(cmd.Root().Int("workers")), client.StackFailFast())
			}

			for _, res := range myResources {
				for _, r := range res {
					if r.Kind() != client.KindImage {
						continue
					}

					if !cmd.Bool("ignore-buildable") {
						stack.Add(r)
					} else {
						i, ok := r.(*client.Image)
						if !ok {
							continue
						}

						// Ignore images with a build config.
						if i.Config.Build == nil {
							stack.Add(i)
						}
					}
				}
			}

			if !cmd.Bool("no-healthd") && healthdInUseByProject(globalClient, p) {
				hparams := healthdParams{
					projectName: p.Name,
					binary:      "",
					image:       resolveHealthdImage(cmd.String("healthd-image")),
					pull:        cmd.String("policy"),
					reCreate:    false,
					incus:       nil,
					network:     "",
					timeout:     time.Second,
					workers:     cmd.Root().Int("workers"),
				}

				_, hResources, err := healthdGetResources(c, hparams)
				if err != nil {
					globalClient.LogError("Creating healthd resources", "error", err)
					return errLogged.Wrap(err)
				}

				for _, r := range hResources {
					if r.Kind() == client.KindImage {
						stack.Add(r)
					}
				}
			}

			var errs error
			if err := stack.ForAction(client.ActionEnsure).Run(
				ctx,
				client.ActionEnsure,
				stdout,
				stderr,
				client.OptionPull(),
				client.OptionCreate(),
			); err != nil {
				c.LogError("Getting resources", "error", err)
				errs = errors.Join(errs, err)
			}

			if errs != nil {
				return errLogged.Wrap(errs)
			}

			return nil
		},
	}
}
