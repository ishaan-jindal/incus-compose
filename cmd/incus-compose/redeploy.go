package main

import (
	"context"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/project"
)

var redeployCommand = &cli.Command{
	Name:      "redeploy",
	Usage:     "Recreate containers with refreshed images (down + pull + up)",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "no-start",
			Usage: "Don't start containers after creating",
		},
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for stopping/starting",
			Value: 10,
		},
		&cli.StringSliceFlag{
			Name:  "scale",
			Usage: "Scale SERVICE to NUM instances (service=num)",
		},
		&cli.BoolFlag{
			Name:  "no-healthd",
			Usage: "Don't recreate healthd sidecar for healthchecks",
		},
		&cli.StringFlag{
			Name:  "healthd-binary",
			Usage: "Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)",
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

		c, err := globalClient.EnsureProject(p.Name, true)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}

		services := cmd.Args().Slice()
		noHealthd := cmd.Bool("no-healthd")
		timeout := int(cmd.Int("timeout"))

		// Remove instances and their per-project image copies (volumes and the
		// image cache are kept), then create fresh with refreshed cache images.
		if err := runDown(globalClient, c, p, downParams{
			services:  services,
			timeout:   timeout,
			noHealthd: noHealthd,
		}); err != nil {
			return err
		}

		return runUp(globalClient, c, p, upParams{
			services:      services,
			start:         !cmd.Bool("no-start"),
			noHealthd:     noHealthd,
			healthdBinary: cmd.String("healthd-binary"),
			pull:          true,
			timeout:       timeout,
			scale:         parseScale(cmd.StringSlice("scale")),
		})
	},
}
