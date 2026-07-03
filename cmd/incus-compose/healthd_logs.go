package main

import (
	"context"
	"io"
	"os"

	"github.com/mattn/go-colorable"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newHealthdLogsCommand() *cli.Command {
	return &cli.Command{
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

			h, err := healthdResolve(c)
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

			if err := h.Ensure(ctx); err != nil {
				c.LogError("Ensuring healthd", "error", err)
				return errLogged.Wrap(err)
			}

			if err := h.Log(ctx, opts...); err != nil {
				c.LogError("Getting healthd logs", "error", err)
				return errLogged.Wrap(err)
			}

			formatter.flush()
			return nil
		},
	}
}
