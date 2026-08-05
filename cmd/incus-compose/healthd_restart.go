package main

import (
	"context"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
)

func newHealthdRestartCommand() *cli.Command {
	return &cli.Command{
		Name:  "restart",
		Usage: "Restart the ic-healthd sidecar",
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "timeout",
				Usage:   "Timeout for stopping",
				Value:   10 * time.Second,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_RESTART_TIMEOUT"),
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

			target, done, err := resolveHealthdTarget(ctx, cmd, globalClient)
			if err != nil {
				globalClient.LogError("Finding healthd", "error", err)
				return errLogged.Wrap(err)
			}
			defer done()

			c, h := target.client, target.instance

			if !cmd.Root().Bool("debug") {
				progress := newProgressRenderer(cmd.Root().Writer, noColor, isatty.IsTerminal(os.Stdout.Fd()))
				progress.Start(c)
				defer progress.Stop(c)
			}

			if err := h.Ensure(ctx); err != nil {
				c.LogError("Ensuring healthd", "error", err)
				return errLogged.Wrap(err)
			}

			timeout := cmd.Duration("timeout")
			if err := h.Stop(ctx, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
				c.LogWarn("Stopping healthd", "error", err)
			}

			if err := h.Start(ctx); err != nil {
				c.LogError("Starting healthd", "error", err)
				return errLogged.Wrap(err)
			}

			return nil
		},
	}
}
