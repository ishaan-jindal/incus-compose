package main

import (
	"cmp"
	"context"
	"fmt"
	"slices"

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

			bm, err := client.NewBackupManager(globalClient, c, composePool, backupConfig)
			if err != nil {
				c.LogError("Creating backup manager", "error", err)
				return errLogged.Wrap(err)
			}
			defer func() { _ = bm.Done() }()

			if !cmd.Bool("live") {
				err = stopServices(ctx, cmd, p, c)
				if err != nil {
					c.LogError("Stopping services for backup", "error", err)
					return errLogged.Wrap(err)
				}
			}

			resources, err := p.Resources(c)
			if err != nil {
				c.LogError("Getting project resources", "error", err)
				return errLogged.Wrap(err)
			}

			volumes := uniqueVolumeNames(resources, cmd.Args().Slice())

			if len(volumes) == 0 {
				c.LogWarn("No volumes to backup")
				return nil
			}

			name := cmd.String("name")
			if name == "" {
				name = "backup"
			}

			ts, err := bm.CreateBackup(ctx, name, volumes)
			if err != nil {
				c.LogError("Creating backup", "error", err)
				return errLogged.Wrap(err)
			}

			_, err = fmt.Fprintf(cmd.Root().Writer, "Backup %q created with timestamp %s\n", name, ts)
			if err != nil {
				c.LogWarn("Writing backup output", "error", err)
			}

			if !cmd.Bool("live") {
				c.IgnoreError(client.ActionStart, client.ErrRunning)
				c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

				restartOrder, err := p.ServiceOrder(false)
				if err != nil {
					c.LogWarn("Getting service order for restart", "error", err)
				} else {
					args := filterResourcesArgs{
						OnlyServices:     cmd.Args().Slice(),
						WithDependencies: false,
						ExcludeKinds:     []client.Kind{client.KindImage, client.KindNetwork, client.KindStorageVolume},
					}
					myResources := filterResources(p, resources, args)

					startStack := client.NewStack(c)
					startStack.AddOrdered(restartOrder, myResources)

					err = startStack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure)
					if err != nil {
						c.LogWarn("Ensuring resources after backup", "error", err)
					}

					restartFilter := func(r client.Resource) bool { return r.IsEnsured() }
					err = startStack.ForActionF(client.ActionStart, restartFilter).Run(ctx, client.ActionStart)
					if err != nil {
						c.LogWarn("Starting services after backup", "error", err)
					}
				}
			}

			return nil
		},
	}
}

func stopServices(ctx context.Context, cmd *cli.Command, p *project.Project, c *client.Client) error {
	resources, err := p.Resources(c)
	if err != nil {
		return err
	}

	args := filterResourcesArgs{
		OnlyServices:     cmd.Args().Slice(),
		WithDependencies: false,
		Reverse:          true,
		ExcludeKinds:     []client.Kind{client.KindImage, client.KindNetwork, client.KindStorageVolume},
	}
	myResources := filterResources(p, resources, args)

	order, err := p.ServiceOrder(true)
	if err != nil {
		return err
	}

	stack := client.NewStack(c, client.StackSortDescending())
	stack.AddOrdered(order, myResources)

	err = stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure)
	if err != nil {
		c.LogWarn("Ensuring resources before stop", "error", err)
	}

	c.IgnoreError(client.ActionStop, client.ErrNotRunning)
	c.IgnoreError(client.ActionStop, client.ErrNotEnsured)
	c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

	filter := func(r client.Resource) bool { return r.IsEnsured() }

	err = stack.ForActionF(client.ActionStop, filter).Run(ctx, client.ActionStop, client.OptionForce())
	if err != nil {
		c.LogWarn("Stopping services", "error", err)
	}

	return nil
}

func uniqueVolumeNames(resources map[string][]client.Resource, onlyServices []string) []string {
	var filtered []client.Resource

	for svcName, svcResources := range resources {
		if len(onlyServices) > 0 {
			included := false
			for _, s := range onlyServices {
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
			if r.Kind() == client.KindStorageVolume {
				vol, ok := r.(*client.StorageVolume)
				if !ok {
					continue
				}

				if vol.Config.HostPath != "" {
					continue
				}

				filtered = append(filtered, r)
			}
		}
	}

	seen := map[string]bool{}
	var names []string
	for _, r := range filtered {
		if !seen[r.Name()] {
			seen[r.Name()] = true
			names = append(names, r.Name())
		}
	}

	slices.SortFunc(names, cmp.Compare)

	return names
}
