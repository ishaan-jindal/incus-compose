package main

import (
	"context"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newDownCommand() *cli.Command {
	return &cli.Command{
		Name:      "down",
		Usage:     "Stop and remove containers",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name: "project",
				// The alias volumes is for docker-compose compatibility
				Aliases: []string{"volumes"},
				Usage:   "Remove the project",
			},
			&cli.StringFlag{
				Name:  "rmi",
				Usage: `Remove images used by services. "local" for known images - all is currently the same as "local".`,
			},
			&cli.BoolFlag{
				Name:  "images",
				Usage: `Remove known images from the project.`,
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping",
				Value: 10 * time.Second,
			},
			&cli.BoolFlag{
				Name:  "no-deps",
				Usage: "Don't stop linked services",
			},
			&cli.BoolFlag{
				Name:  "no-networks",
				Usage: "Don't touch networks",
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

			// Get the per Project client early, gives early errors if the project does not exists
			c, err := globalClient.EnsureProject(p.Name)
			if err != nil {
				globalClient.LogWarn("Getting the incus project", "project", p.Name, "error", err)
				return nil
			}
			defer func() { _ = c.Done() }()

			if err := c.Open(); err != nil {
				globalClient.LogError("Opening the project client", "project", p.Name, "error", err)
				return errLogged.Wrap(err)
			}

			// We start all resources, just ignore that warning but let progress know them (so add before - LIFO - progress runs before).
			c.IgnoreError(client.ActionStop, client.ErrNotEnsured)
			c.IgnoreError(client.ActionStop, client.ErrNotRunning)
			c.IgnoreError(client.ActionEnsure, client.ErrNotFound)
			c.IgnoreError(client.ActionDelete, client.ErrNotEnsured)
			c.IgnoreError(client.ActionDelete, client.ErrNotFound)

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
				WithDependencies: !cmd.Bool("no-deps"),
				Reverse:          true,
			}

			if !cmd.Bool("project") {
				args.ExcludeKinds = append(args.ExcludeKinds, client.KindStorageVolume)
			}

			// Do not delete networks when we are not deleting all other resources.
			if cmd.Args().Len() > 0 || cmd.Bool("no-networks") {
				args.ExcludeKinds = append(args.ExcludeKinds, client.KindNetwork)
			}

			if !cmd.Bool("images") && cmd.String("rmi") != "local" && cmd.String("rmi") != "all" {
				args.ExcludeKinds = append(args.ExcludeKinds, client.KindImage)
			}

			order, err := p.ServiceOrder(true)
			if err != nil {
				c.LogError("Getting the service dependency order", "error", err)
				return errLogged.Wrap(err)
			}

			myResources := filterResources(p, resources, args)

			stack := client.NewStack(c, client.StackSortDescending(), client.StackWorkers(cmd.Root().Int("workers")))
			stack.AddOrdered(order, myResources)

			if cmd.Args().Len() == 0 {
				if healthdInUseByProject(globalClient, p) {
					h, err := healthdResolve(c)
					if err == nil {
						stack.Add(h)
					}
				}
			}

			if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, stdout, stderr); err != nil {
				c.LogWarn("Getting resources", "error", err)
			}

			runOpts := []client.Option{
				client.OptionForce(),
				client.OptionTimeout(cmd.Duration("timeout")),
			}

			errStop := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, stdout, stderr, runOpts...)
			if errStop != nil {
				c.LogWarn("Stopping resources", "error", errStop)
			}

			errDel := stack.ForAction(client.ActionDelete).Run(ctx, client.ActionDelete, cmd.Root().Writer, stderr, runOpts...)
			if errDel != nil {
				c.LogWarn("Deleting resources", "error", errDel)
			}

			if cmd.Bool("project") {
				c.LogDebug("Deleting the project")
				err := globalClient.DeleteProject(c.Project(), true)
				if err != nil {
					c.LogError("Deleting the project", "error", err)
					return errLogged.Wrap(err)
				}
			}

			return nil
		},
	}
}
