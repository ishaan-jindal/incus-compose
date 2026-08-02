package client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIncusVolumeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "db-data", "vol-db-data"},
		{"single-char", "a", "vol-a"},
		{"with-dashes", "my-long-volume-name", "vol-my-long-volume-name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := incusVolumeName(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestBackupIncusVolumeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "vol-db-data", "ic-backup-vol-db-data"},
		{"single-char", "vol-x", "ic-backup-vol-x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := backupIncusVolumeName(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestTimestamp(t *testing.T) {
	t.Parallel()

	ts := timestamp()

	parsed, err := time.Parse(time.RFC3339, ts)
	require.NoError(t, err)
	assert.True(t, parsed.UTC().Equal(parsed))
}

func TestBackupEntryJSON(t *testing.T) {
	t.Parallel()

	entry := BackupEntry{
		Timestamp: "2024-01-01T00:00:00Z",
		Name:      "daily",
		Pool:      "default",
		Volumes:   []string{"vol-db-data"},
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	var got BackupEntry
	err = json.Unmarshal(data, &got)
	require.NoError(t, err)
	assert.Equal(t, entry, got)
}

func TestManifestJSONRoundTrip(t *testing.T) {
	t.Parallel()

	m := &manifest{Backups: []BackupEntry{
		{Timestamp: "2024-01-01T00:00:00Z", Name: "daily", Pool: "default", Volumes: []string{"vol-db-data"}},
		{Timestamp: "2024-01-02T00:00:00Z", Name: "hourly", Pool: "default", Volumes: []string{"vol-db-data", "vol-cache-data"}},
	}}

	data, err := json.Marshal(m)
	require.NoError(t, err)

	got := &manifest{}
	err = json.Unmarshal(data, got)
	require.NoError(t, err)
	assert.Equal(t, m.Backups, got.Backups)
}

// newLockTestBackupManager creates a BackupManager on a fresh project and
// cleans up the backup project on teardown.
func newLockTestBackupManager(t *testing.T) (*GlobalClient, *Client, *BackupManager) {
	t.Helper()
	skipLocal(t)

	gc, err := NewTestClient(t.Context())
	require.NoError(t, err)

	c := newRandomTestClient(t.Context(), t, "lock-")
	pool := c.Config().DefaultStoragePool

	bm, err := NewBackupManager(gc, c, pool, BackupConfig{})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = bm.Done()
		_ = gc.DeleteProject(c.Project()+"-backup", true)
	})

	return gc, c, bm
}

func TestBackupLockExclusive(t *testing.T) {
	t.Parallel()

	gc, c, bmA := newLockTestBackupManager(t)

	bmB, err := NewBackupManager(gc, c, c.Config().DefaultStoragePool, BackupConfig{})
	require.NoError(t, err)
	bmB.lockTimeout = 2 * time.Second
	t.Cleanup(func() { _ = bmB.Done() })

	ctx := t.Context()

	err = bmA.acquireLock(ctx)
	require.NoError(t, err)

	start := time.Now()
	err = bmB.acquireLock(ctx)
	require.Error(t, err)
	assert.Greater(t, time.Since(start), time.Second)

	bmA.releaseLock()

	err = bmB.acquireLock(ctx)
	require.NoError(t, err)
	bmB.releaseLock()
}

func TestBackupLockStaleTakeover(t *testing.T) {
	t.Parallel()

	_, _, bm := newLockTestBackupManager(t)

	err := bm.writeLock(time.Now().UTC().Add(-10 * time.Minute))
	require.NoError(t, err)

	err = bm.acquireLock(t.Context())
	require.NoError(t, err)
	bm.releaseLock()
}
