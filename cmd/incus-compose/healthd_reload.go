package main

import (
	"context"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/project"
)

func newHealthdReloadCommand() *cli.Command {
	return &cli.Command{
		Name:  "reload",
		Usage: "Send SIGHUP to the ic-healthd process",
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

			if !cmd.Root().Bool("debug") {
				progress := newProgressRenderer(c, cmd.Root().Writer, noColor, isatty.IsTerminal(os.Stdout.Fd()))
				progress.Start()
				defer progress.Stop()
			}

			h, err := healthdResolve(c)
			if err != nil {
				c.LogError(err.Error())
				return errLogged.Wrap(err)
			}

			if err := h.Ensure(ctx); err != nil {
				c.LogError("Ensuring healthd", "error", err)
				return errLogged.Wrap(err)
			}

			if err := healthdReload(c, h); err != nil {
				c.LogError("Reloading healthd", "error", err)
				return errLogged.Wrap(err)
			}

			return nil
		},
	}
}
