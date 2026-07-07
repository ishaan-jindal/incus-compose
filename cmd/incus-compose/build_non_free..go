//go:build !unix || darwin

package main

import (
	"context"
	"errors"

	"github.com/urfave/cli/v3"
)

func newBuildCommand() *cli.Command {
	return &cli.Command{
		Name:      "build",
		Usage:     "Build or rebuild service images",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "no-cache",
				Usage: "Do not use a cache when building the image",
			},
			&cli.StringFlag{
				Name:  "pull",
				Usage: `Pull image before running ("always"|"missing"|"never"|"policy")`,
				Value: "policy",
			},
			&cli.StringFlag{
				Name:    "builder",
				Usage:   "Preferred builder, binary name or absolute path. Empty for auto-detect.",
				Sources: cli.EnvVars("INCUS_COMPOSE_BUILDER"),
			},
		},
		Action: func(_ context.Context, _ *cli.Command) error {
			return errors.New("the build command is only available on linux")
		},
	}
}
