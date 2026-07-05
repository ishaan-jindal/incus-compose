package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pulledImageAliases runs the wrapped `incus image list --format=json` inside the
// compose project's Incus project and returns the alias names of the images found.
func pulledImageAliases(t *testing.T, ctx context.Context, projectName, compose string) []string {
	t.Helper()

	stdout, err := runCommand(t, ctx, projectName, "-f", compose, "incus", "image", "list", "--format=json")
	require.NoError(t, err)

	var images []struct {
		Aliases []struct {
			Name string `json:"name"`
		} `json:"aliases"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &images))

	var aliases []string
	for _, img := range images {
		for _, a := range img.Aliases {
			aliases = append(aliases, a.Name)
		}
	}
	return aliases
}

// hasImage reports whether any alias contains sub.
func hasImage(aliases []string, sub string) bool {
	return slices.ContainsFunc(aliases, func(a string) bool {
		return strings.Contains(a, sub)
	})
}

// TestSlowPull verifies `pull` copies the service image into the project and that
// the compatibility flags (--policy, --no-healthd) are accepted. The image is
// verified through the wrapped `incus image list --format=json`.
func TestSlowPull(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "pull",
			args: []string{"-f", compose, "pull"},
		},
		{
			name: "pull policy missing",
			args: []string{"-f", compose, "pull", "--policy", "missing"},
		},
		{
			name: "pull no-healthd",
			args: []string{"-f", compose, "pull", "--no-healthd"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runCommand(t, ctx, pn, tt.args...)
			require.NoError(t, err)

			aliases := pulledImageAliases(t, ctx, pn, compose)
			require.True(t, hasImage(aliases, "nginx"),
				"expected the nginx image in the project, got %v", aliases)
		})
	}
}

// TestSlowPullWithDeps verifies that `pull <service>` copies only the named
// service's image while `pull --with-deps <service>` also follows depends_on.
func TestSlowPullWithDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/postgres-redis/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	// Pulling just "api" copies its own image, not its dependencies.
	_, err := runCommand(t, ctx, pn, "-f", compose, "pull", "--no-healthd", "api")
	require.NoError(t, err)

	aliases := pulledImageAliases(t, ctx, pn, compose)
	require.True(t, hasImage(aliases, "node"), "expected the api image, got %v", aliases)
	require.False(t, hasImage(aliases, "postgres"), "did not expect dep images, got %v", aliases)
	require.False(t, hasImage(aliases, "redis"), "did not expect dep images, got %v", aliases)

	// --with-deps follows depends_on and also pulls postgres and redis.
	_, err = runCommand(t, ctx, pn, "-f", compose, "pull", "--no-healthd", "--with-deps", "api")
	require.NoError(t, err)

	aliases = pulledImageAliases(t, ctx, pn, compose)
	require.True(t, hasImage(aliases, "node"), "expected the api image, got %v", aliases)
	require.True(t, hasImage(aliases, "postgres"), "expected the postgres dep image, got %v", aliases)
	require.True(t, hasImage(aliases, "redis"), "expected the redis dep image, got %v", aliases)
}

// TestSlowPullInvalidImage verifies `pull` fails when a service references an
// image that cannot be resolved from any registry.
func TestSlowPullInvalidImage(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  bogus:
    image: docker.io/library/incus-compose-does-not-exist:latest
`,
	})
	compose := filepath.Join(dir, "compose.yaml")

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "pull")
	require.Error(t, err)
}

// TestSlowPullIgnoreBuildable verifies --ignore-buildable skips images with a
// build config; plain pull tries (and fails) to pull them from a registry.
func TestSlowPullIgnoreBuildable(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-build/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	// Plain pull tries to pull the buildable images, which don't exist in a registry.
	_, err := runCommand(t, ctx, pn, "-f", compose, "pull")
	require.Error(t, err)

	// --ignore-buildable skips images with a build config, leaving nothing to pull.
	_, err = runCommand(t, ctx, pn, "-f", compose, "pull", "--ignore-buildable")
	require.NoError(t, err)
}
