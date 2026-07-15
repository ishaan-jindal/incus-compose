package client

import (
	"context"
	"testing"

	"github.com/lxc/incus/v7/shared/cliconfig"
	"github.com/stretchr/testify/require"
)

func TestAddWellKnownRegistriesHook(t *testing.T) {
	gc := New(context.Background())

	delete(gc.CliConfig().Remotes, "ghcr.io")

	img := &Image{
		BaseResource: NewBaseResource(KindImage, "ghcr.io/lxc/incus-compose/ic-healthd:latest", PriorityImage),
		incusName:    "ghcr.io/lxc/incus-compose/ic-healthd:latest",
		remote:       "ghcr.io",
		image:        "lxc/incus-compose/ic-healthd:latest",
	}

	err := gc.hookBefore(context.Background(), ActionEnsure, img, Options{}, nil)
	require.NoError(t, err)

	remote, ok := gc.CliConfig().Remotes["ghcr.io"]
	require.True(t, ok, "ghcr.io should be added by hook")

	require.Equal(t, "oci", remote.Protocol)
	require.True(t, remote.Public)
	require.Equal(t, []string{"https://ghcr.io"}, remote.Addrs)
}

func TestWellKnownHookSkipsUnknownRegistries(t *testing.T) {
	gc := New(context.Background())

	img := &Image{
		BaseResource: NewBaseResource(KindImage, "unknown.registry.example.com/image:tag", PriorityImage),
		incusName:    "unknown.registry.example.com/image:tag",
		remote:       "unknown.registry.example.com",
		image:        "image:tag",
	}

	err := gc.hookBefore(context.Background(), ActionEnsure, img, Options{}, nil)
	require.NoError(t, err)

	_, ok := gc.CliConfig().Remotes["unknown.registry.example.com"]
	require.False(t, ok, "unknown registry should not be added")
}

func TestWellKnownHookSkipsExistingRemotes(t *testing.T) {
	gc := New(context.Background())

	gc.CliConfig().Remotes["ghcr.io"] = cliconfig.Remote{
		Addrs:    []string{"https://custom.example.com"},
		Protocol: "oci",
		Public:   true,
	}

	img := &Image{
		BaseResource: NewBaseResource(KindImage, "ghcr.io/something:latest", PriorityImage),
		incusName:    "ghcr.io/something:latest",
		remote:       "ghcr.io",
		image:        "something:latest",
	}

	err := gc.hookBefore(context.Background(), ActionEnsure, img, Options{}, nil)
	require.NoError(t, err)

	require.Equal(t, "https://custom.example.com", gc.CliConfig().Remotes["ghcr.io"].Addrs[0],
		"existing remote should not be overwritten")
}
