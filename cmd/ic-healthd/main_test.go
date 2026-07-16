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

	// This sandbox always has a default route; hasDefaultRoute reads the
	// real /proc/net/route, no mocking needed.
	require.True(t, hasDefaultRoute())
}

func TestNewVersionCommand(t *testing.T) {
	t.Parallel()

	cmd := newRootCommand()

	err := cmd.Run(t.Context(), []string{"ic-healthd", "version"})
	require.NoError(t, err)
}

// TestE2ERunActionViaCLI drives the real `ic-healthd run` CLI entrypoint
// (flag parsing, the Before hook's hasDefaultRoute check, runAction, the
// signal-handling goroutine, and Runner.Run) against a real Incus, the same
// way incus-compose actually invokes the binary - rather than constructing
// a Runner directly and calling Run, which every other e2e test does and
// which never exercises main.go at all.
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

	// Give it time to register its cert, connect, discover, and start
	// listening before asking it to shut down.
	time.Sleep(3 * time.Second)
	runCancel()

	select {
	case err := <-errCh:
		require.NoError(t, err, "stderr: %s", stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("ic-healthd run did not exit after its context was canceled")
	}
}
