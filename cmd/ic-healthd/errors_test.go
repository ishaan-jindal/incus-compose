package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewErrorCarriesItsText(t *testing.T) {
	t.Parallel()

	require.Equal(t, "test error", newError("test error").Error())
}

// TestNewErrorIdentityIsThePointer pins why this type exists: matching on the
// message would make two unrelated sentinels compare equal.
func TestNewErrorIdentityIsThePointer(t *testing.T) {
	t.Parallel()

	first := newError("same text")
	second := newError("same text")

	require.True(t, errors.Is(first, first))
	require.False(t, errors.Is(first, second),
		"two sentinels with the same text are still different errors")
}

// TestErrorIsRejectsForeignErrors pins that the daemon's sentinels never match
// an error from elsewhere, however it is spelled.
func TestErrorIsRejectsForeignErrors(t *testing.T) {
	t.Parallel()

	require.False(t, errors.Is(ErrNotRunning, errors.New("not running")))
	require.False(t, errors.Is(ErrNotRunning, fmt.Errorf("not running")))
	require.False(t, errors.Is(ErrNotRunning, ErrInstanceIgnored))
}

// TestErrorIsThroughAWrappedChain pins the shape every caller uses: the actions
// return wrapped errors and the loop matches with errors.Is.
func TestErrorIsThroughAWrappedChain(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("restarting web-1: %w", ErrIntentionallyStopped)

	require.ErrorIs(t, wrapped, ErrIntentionallyStopped)
	require.NotErrorIs(t, wrapped, ErrNotRunning)
}

// TestSentinelsAreDistinct pins that no two of the daemon's sentinels can be
// confused for each other.
func TestSentinelsAreDistinct(t *testing.T) {
	t.Parallel()

	sentinels := map[string]*Error{
		"ignored":       ErrInstanceIgnored,
		"nohealthcheck": ErrInstanceNoHealthcheck,
		"stopped":       ErrIntentionallyStopped,
		"notrunning":    ErrNotRunning,
	}

	for name, err := range sentinels {
		for otherName, other := range sentinels {
			if name == otherName {
				require.ErrorIs(t, err, other)
				continue
			}

			require.NotErrorIs(t, err, other, "%s must not match %s", name, otherName)
		}
	}
}
