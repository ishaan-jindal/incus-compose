package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newHealthdDownCommand() *cli.Command {
	return &cli.Command{
		Name:  "down",
		Usage: "Stop and remove the ic-healthd sidecar",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "image",
				Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
				Value:   defaultHealthdImage,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
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

			params := healthdParams{
				projectName: p.Name,
				binary:      "",
				image:       resolveHealthdImage(cmd.String("image")),
				reCreate:    false,
				timeout:     cmd.Duration("timeout"),
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

			stdout := cmd.Root().Writer
			stderr := cmd.Root().ErrWriter

			if !cmd.Root().Bool("debug") {
				progress := newProgressRenderer(stdout, noColor, isatty.IsTerminal(os.Stdout.Fd()))
				progress.Start(c)
				defer progress.Stop(c)

				stdout = progress.bypass()
				stderr = stdout
			}

			stack := client.NewStack(c, client.StackSortDescending())

			volRes, err := c.Resource(
				client.KindStorageVolume,
				"ic-healthd",
				&client.StorageVolumeConfig{},
			)
			if err != nil {
				c.LogError("Getting the volume resource", "error", err)
				return errLogged.Wrap(err)
			}
			stack.Add(volRes)

			instRes, err := c.Resource(client.KindInstance, fmt.Sprintf("%s-ic-healthd", params.projectName), &client.InstanceConfig{})
			if err != nil {
				c.LogError("Getting the healthd instance resource", "error", err)
				return errLogged.Wrap(err)
			}
			stack.Add(instRes)

			c.LogDebug("Ensure", "resources", stack.All())

			if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, stdout, stderr); err != nil {
				c.LogError("Ensuring healthd", "error", err)
				return errLogged.Wrap(err)
			}

			if err := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, stdout, stderr, client.OptionForce(), client.OptionTimeout(cmd.Duration("timeout"))); err != nil {
				c.LogError("Stopping healthd resources", "error", err)
				return errLogged.Wrap(err)
			}

			if err := stack.ForAction(client.ActionDelete).Run(ctx, client.ActionDelete, stdout, stderr, client.OptionForce(), client.OptionTimeout(cmd.Duration("timeout"))); err != nil {
				c.LogError("Deleting healthd resources", "error", err)
				return errLogged.Wrap(err)
			}

			if err := healthdRevokeCert(c); err != nil {
				c.LogError("Cannot revoke the healthd cert", "error", err)
				return errLogged.Wrap(err)
			}

			return err
		},
	}
}
