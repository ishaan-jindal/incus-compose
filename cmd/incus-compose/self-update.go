package main

import (
	"context"
	"errors"
	"runtime"

	"github.com/creativeprojects/go-selfupdate"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/cmd/incus-compose/version"
)

func newSelfUpdateCommand() *cli.Command {
	return &cli.Command{
		Name:     "self-update",
		Usage:    `Self update incus-compose`,
		Category: "extensions",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "draft",
				Usage: `Also consider draft releases when checking for updates`,
			},
			&cli.BoolFlag{
				Name:  "pre-release",
				Usage: `Also consider pre-releases when checking for updates`,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			gc, err := clientFromContext(ctx)
			if err != nil {
				return err
			}

			updater, err := selfupdate.NewUpdater(selfupdate.Config{
				Draft:      cmd.Bool("draft"),
				Prerelease: cmd.Bool("pre-release"),
				Filters:    []string{"^incus-compose_"},
			})
			if err != nil {
				gc.LogError("Creating updater", "error", err)
				return errLogged.Wrap(err)
			}

			latest, found, err := updater.DetectLatest(context.Background(), selfupdate.ParseSlug("lxc/incus-compose"))
			if err != nil {
				gc.LogError("While detecting a version", "error", err)
				return errLogged.Wrap(err)
			}

			if !found {
				gc.LogError("Latest version could not be found from the github repository", "GOOS", runtime.GOOS, "GOARCH", runtime.GOARCH)
				return errLogged.Wrap(errors.New("version not found"))
			}

			if latest.LessOrEqual(version.Current()) {
				gc.LogInfo("You have already the newest version", "version", version.Current())
				return nil
			}

			exe, err := selfupdate.ExecutablePath()
			if err != nil {
				gc.LogError("Could not locate executable path", "error", err)
				return errLogged.Wrap(errors.New("could not locate executable path"))
			}
			if err := selfupdate.UpdateTo(context.Background(), latest.AssetURL, latest.AssetName, exe); err != nil {
				gc.LogError("While updating the binary", "error", err)
				return errLogged.Wrap(err)
			}

			gc.LogInfo("Successfully updated to version", "version", latest.Version())
			return nil
		},
	}
}
