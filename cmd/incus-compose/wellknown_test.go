package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWellKnownRegistryQuayIO(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	ctx := context.Background()
	pn := t.Name()

	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  hello:
    image: quay.io/podman/hello
`,
	})
	compose := filepath.Join(dir, "compose.yaml")

	t.Cleanup(func() {
		_, _ = runCommand(ctx, t, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(ctx, t, pn, "-f", compose, "pull", "--no-healthd", "hello")
	require.NoError(t, err)
}

func TestWellKnownRegistryMCR(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	ctx := context.Background()
	pn := t.Name()

	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  hello:
    image: mcr.microsoft.com/azurelinux/busybox:1.36
`,
	})
	compose := filepath.Join(dir, "compose.yaml")

	t.Cleanup(func() {
		_, _ = runCommand(ctx, t, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(ctx, t, pn, "-f", compose, "pull", "--no-healthd", "hello")
	require.NoError(t, err)
}
