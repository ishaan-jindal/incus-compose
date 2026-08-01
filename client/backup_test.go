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

func TestManifestFromJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{
			name: "nil data",
		},
		{
			name: "empty data",
			data: []byte{},
		},
		{
			name: "valid manifest",
			data: []byte(`{"backups":[{"timestamp":"2024-01-01T00:00:00Z","name":"daily","pool":"default","volumes":["vol-db-data"]}]}`),
		},
		{
			name:    "invalid json",
			data:    []byte(`{"backups":`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := manifestFromJSON(tt.data)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, got.Backups)
		})
	}
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

	got, err := manifestFromJSON(data)
	require.NoError(t, err)
	assert.Equal(t, m.Backups, got.Backups)
}
