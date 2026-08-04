package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/stretchr/testify/require"
)

// TestCheckerCheckHonoursTimeoutOnStalledServer pins that check returns when the API stalls.
func TestCheckerCheckHonoursTimeoutOnStalledServer(t *testing.T) {
	t.Parallel()

	// Closed on cleanup to release the handler goroutines.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	// Completes the TLS handshake, accepts the request, then holds it open.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	conn, err := incus.ConnectIncusWithContext(t.Context(), srv.URL, &incus.ConnectionArgs{
		InsecureSkipVerify: true,
		SkipGetServer:      true,
		SkipGetEvents:      true,
	})
	require.NoError(t, err)

	c := newChecker(conn, "wedged-1", instanceConfig{
		Test:    []string{"CMD", "true"},
		Timeout: time.Second,
	}, nil, nil)

	done := make(chan error, 1)
	go func() {
		// Background keeps cleanup's cancel from unblocking check on its own.
		done <- c.check(context.Background())
	}()

	select {
	case err := <-done:
		// The contract is that check returns at all; any error satisfies it.
		require.Error(t, err, "a stalled server must surface as a failed check")
	case <-time.After(15 * time.Second):
		t.Fatal("check did not return within 15s for a 1s healthcheck timeout: " +
			"a stalled Incus API wedges the instance's checker forever")
	}
}
