package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHasDefaultRoute(t *testing.T) {
	t.Parallel()

	// This sandbox always has a default route, so read the real /proc/net/route.
	require.True(t, hasDefaultRoute())
}

func TestNewVersionCommand(t *testing.T) {
	t.Parallel()

	cmd := newRootCommand()

	err := cmd.Run(t.Context(), []string{"ic-healthd", "version"})
	require.NoError(t, err)
}

// TestE2ERunActionViaCLI drives the real `ic-healthd run` entrypoint the way
// incus-compose invokes it; every other e2e test skips main.go entirely.
func TestE2ERunActionViaCLI(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(t.Context())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/with-restart/compose.yaml"

	c, _ := loadProject(ctx, t, compose, projectName)
	err := c.Open()
	require.NoError(t, err)

	iURL, err := incusURL(c)
	require.NoError(t, err)

	token, err := newToken(c)
	require.NoError(t, err)

	secretsDir, err := os.MkdirTemp("", "ic-secrets-*")
	require.NoError(t, err)
	dataDir, err := os.MkdirTemp("", "ic-data-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = c.Done()

		_, _, _ = runIncusCommand(context.Background(), t, projectName, "-f", compose, "down", "--project")
		_ = revokeCert(c)
		_ = os.RemoveAll(secretsDir)
		_ = os.RemoveAll(dataDir)
		cancel()
	})

	cmd := newRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Writer = stdout
	cmd.ErrWriter = stderr

	runCtx, runCancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Run(runCtx, []string{
			"ic-healthd", "run",
			"--incus", iURL,
			"--token", token,
			"--project", c.IncusProject(),
			"--secrets-dir", secretsDir,
			"--data-dir", dataDir,
			"--debug",
		})
	}()

	// Let it register, connect, discover, and listen before shutting it down.
	time.Sleep(3 * time.Second)
	runCancel()

	select {
	case err := <-errCh:
		require.NoError(t, err, "stderr: %s", stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("ic-healthd run did not exit after its context was canceled")
	}
}
