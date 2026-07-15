package client

import (
	"context"
	"sync"

	"github.com/lxc/incus/v7/shared/cliconfig"
)

// WellKnownRegistries maps well-known OCI registry domains to their server URLs.
// Registries in this map are added to the in-memory Incus CLI config on demand
// when an image from that registry is ensured, removing the need for manual
// `incus remote add` steps.
var WellKnownRegistries = map[string]string{
	"ghcr.io":             "https://ghcr.io",
	"docker.io":           "https://docker.io",
	"mcr.microsoft.com":   "https://mcr.microsoft.com",
	"quay.io":             "https://quay.io",
	"registry.gitlab.com": "https://registry.gitlab.com",
}

// AddWellKnownRegistriesHook registers a hook that transparently adds
// well-known OCI registries to the in-memory CLI config when an image from
// that registry is about to be ensured.
func AddWellKnownRegistriesHook(c *GlobalClient) {
	wellKnownMu := &sync.Mutex{}

	c.AddHookBefore(func(_ context.Context, action Action, r Resource, _ Options, err error) error {
		if r.Kind() != KindImage {
			return err
		}

		img, ok := r.(*Image)
		if !ok {
			return err
		}

		wellKnownMu.Lock()
		defer wellKnownMu.Unlock()

		remote := img.Remote()
		url, ok := WellKnownRegistries[remote]
		if !ok {
			return err
		}

		if _, exists := c.CliConfig().Remotes[remote]; !exists {
			c.CliConfig().Remotes[remote] = cliconfig.Remote{
				Addrs:    []string{url},
				Protocol: "oci",
				Public:   true,
			}
		}

		return err
	})
}
