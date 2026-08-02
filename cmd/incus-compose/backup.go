package main

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newBackupCommand() *cli.Command {
	return &cli.Command{
		Name:     "backup",
		Usage:    "Snapshot project data volumes into a backup project",
		Category: "resource",
		Commands: []*cli.Command{
			newBackupCreateCommand(),
		},
	}
}

func newBackupCreateCommand() *cli.Command {
	return &cli.Command{
		Name:      "create",
		Usage:     "Create a backup of project volumes",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "name",
				Usage: "Name for this backup",
			},
			&cli.BoolFlag{
				Name:  "live",
				Usage: "Snapshot volumes while services are running (crash-consistent)",
			},
			&cli.StringFlag{
				Name:    "pool",
				Usage:   "Storage pool for backup volumes (overrides x-incus-compose.backup.pool)",
				Sources: cli.EnvVars("INCUS_COMPOSE_BACKUP_POOL"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			globalClient, err := clientFromContext(ctx)
			if err != nil {
				return err
			}

			err = globalClient.Connect()
			if err != nil {
				return err
			}

			p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
			if err != nil {
				globalClient.LogError("Configuring the project", "error", err)
				return err
			}

			c, err := globalClient.EnsureProject(p.Name, client.EnsureProjectWithCreate(), client.EnsureProjectWithConfig(p.ClientConfig.XIncus))
			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged
			}

			err = c.Open()
			if err != nil {
				globalClient.LogError("Opening the project client", "error", err)
				return errLogged.Wrap(err)
			}
			defer func() { _ = c.Done() }()

			composePool := c.Config().DefaultStoragePool

			backupConfig := client.BackupConfig{
				Pool: p.ClientConfig.Backup.Pool,
			}
			if pool := cmd.String("pool"); pool != "" {
				backupConfig.Pool = pool
			}

			bm, err := client.NewBackupManager(globalClient, c, composePool, backupConfig)
			if err != nil {
				c.LogError("Creating backup manager", "error", err)
				return errLogged.Wrap(err)
			}
			defer func() { _ = bm.Done() }()

			services := cmd.Args().Slice()

			if !cmd.Bool("live") {
				err = stop(ctx, p, c, stopArgs{
					Services: services,
					Timeout:  2 * time.Minute,
					Workers:  cmd.Root().Int("workers"),
					Debug:    cmd.Root().Bool("debug"),
					Writer:   cmd.Root().Writer,
				})
				if err != nil {
					c.LogError("Stopping services for backup", "error", err)
					return err
				}
			}

			resources, err := p.Resources(c)
			if err != nil {
				c.LogError("Getting project resources", "error", err)
				return errLogged.Wrap(err)
			}

			seen := map[string]bool{}
			var volumes []string
			for svcName, svcResources := range resources {
				if len(services) > 0 {
					included := false
					for _, s := range services {
						if s == svcName {
							included = true
							break
						}
					}

					if !included {
						continue
					}
				}

				for _, r := range svcResources {
					if r.Kind() != client.KindStorageVolume {
						continue
					}

					vol, ok := r.(*client.StorageVolume)
					if !ok || vol.Config.HostPath != "" {
						continue
					}

					if !seen[r.Name()] {
						seen[r.Name()] = true
						volumes = append(volumes, r.Name())
					}
				}
			}
			slices.SortFunc(volumes, cmp.Compare)

			if len(volumes) == 0 {
				c.LogWarn("No volumes to backup")
				return nil
			}

			name := cmd.String("name")
			if name == "" {
				name = "backup"
			}

			ts, err := bm.Create(ctx, name, volumes)
			if err != nil {
				c.LogError("Creating backup", "error", err)
				return errLogged.Wrap(err)
			}

			_, err = fmt.Fprintf(cmd.Root().Writer, "Backup %q created with timestamp %s\n", name, ts)
			if err != nil {
				c.LogWarn("Writing backup output", "error", err)
			}

			if !cmd.Bool("live") {
				err = start(ctx, p, c, startArgs{
					Services: services,
					Timeout:  2 * time.Minute,
					Workers:  cmd.Root().Int("workers"),
					Debug:    cmd.Root().Bool("debug"),
					Writer:   cmd.Root().Writer,
				})
				if err != nil {
					c.LogWarn("Restarting services after backup", "error", err)
				}
			}

			return nil
		},
	}
}
