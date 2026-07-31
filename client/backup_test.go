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

func TestManifestReadWrite(t *testing.T) {
	t.Parallel()

	bm := &BackupManager{projectDir: t.TempDir()}

	entry := BackupEntry{
		Timestamp: "2024-01-01T00:00:00Z",
		Name:      "daily",
		Pool:      "default",
		Volumes:   []string{"vol-db-data", "vol-cache-data"},
	}

	m := &manifest{Backups: []BackupEntry{entry}}

	err := bm.writeManifest(m)
	require.NoError(t, err)

	got, err := bm.readManifest()
	require.NoError(t, err)
	require.Len(t, got.Backups, 1)
	assert.Equal(t, entry.Timestamp, got.Backups[0].Timestamp)
	assert.Equal(t, entry.Name, got.Backups[0].Name)
	assert.Equal(t, entry.Pool, got.Backups[0].Pool)
	assert.Equal(t, entry.Volumes, got.Backups[0].Volumes)
}

func TestManifestEmptyOnMissing(t *testing.T) {
	t.Parallel()

	bm := &BackupManager{projectDir: t.TempDir()}

	got, err := bm.readManifest()
	require.NoError(t, err)
	assert.NotNil(t, got.Backups)
	assert.Len(t, got.Backups, 0)
}

func TestManifestAppendMultiple(t *testing.T) {
	t.Parallel()

	bm := &BackupManager{projectDir: t.TempDir()}

	e1 := BackupEntry{Timestamp: "2024-01-01T00:00:00Z", Name: "first", Pool: "default", Volumes: []string{"vol-a"}}
	e2 := BackupEntry{Timestamp: "2024-01-02T00:00:00Z", Name: "second", Pool: "default", Volumes: []string{"vol-b"}}

	m, err := bm.readManifest()
	require.NoError(t, err)
	m.Backups = append(m.Backups, e1)
	err = bm.writeManifest(m)
	require.NoError(t, err)

	m, err = bm.readManifest()
	require.NoError(t, err)
	m.Backups = append(m.Backups, e2)
	err = bm.writeManifest(m)
	require.NoError(t, err)

	got, err := bm.readManifest()
	require.NoError(t, err)
	require.Len(t, got.Backups, 2)
	assert.Equal(t, "first", got.Backups[0].Name)
	assert.Equal(t, "second", got.Backups[1].Name)
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

	bm := &BackupManager{projectDir: t.TempDir()}

	m := &manifest{Backups: []BackupEntry{
		{Timestamp: "2024-01-01T00:00:00Z", Name: "daily", Pool: "default", Volumes: []string{"vol-db-data"}},
		{Timestamp: "2024-01-02T00:00:00Z", Name: "hourly", Pool: "default", Volumes: []string{"vol-db-data", "vol-cache-data"}},
	}}

	err := bm.writeManifest(m)
	require.NoError(t, err)

	got, err := bm.readManifest()
	require.NoError(t, err)
	assert.Equal(t, m.Backups, got.Backups)
}
