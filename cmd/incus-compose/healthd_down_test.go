package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHealthdDownConfirmRefusesWithoutATerminal pins the gate that keeps a
// script from stopping a daemon other projects rely on. `go test` has no
// terminal, so this is the CI path too.
func TestHealthdDownConfirmRefusesWithoutATerminal(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}

	ok, err := healthdDownConfirm(out, []string{"blog", "shop"})

	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "--force")

	// The projects at stake are named before it gives up, so the operator can
	// see what --force would cost.
	assert.Contains(t, out.String(), "blog, shop")
	assert.Contains(t, out.String(), "2 other project(s)")
}
