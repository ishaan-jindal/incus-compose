package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/client"
)

func writeTempCompose(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	err := os.WriteFile(path, []byte(content), 0o644)
	require.NoError(t, err)

	return path
}

func assertBackupVolumeExists(t *testing.T, c *client.Client, pool, name string) {
	t.Helper()

	conn, err := c.Connection()
	require.NoError(t, err)

	_, _, err = conn.GetStoragePoolVolume(pool, "custom", name)
	require.NoError(t, err, "volume %s should exist in pool %s", name, pool)
}

func assertBackupSnapshotExists(t *testing.T, c *client.Client, pool, volumeName, snapshotName string) {
	t.Helper()

	conn, err := c.Connection()
	require.NoError(t, err)

	names, err := conn.GetStoragePoolVolumeSnapshotNames(pool, "custom", volumeName)
	require.NoError(t, err)

	for _, n := range names {
		if n == snapshotName {
			return
		}
	}

	t.Errorf("snapshot %s not found on volume %s in pool %s", snapshotName, volumeName, pool)
}

func readBackupManifest(t *testing.T, bp *client.Client) []client.BackupEntry {
	t.Helper()

	conn, err := bp.Connection()
	require.NoError(t, err)

	data, _, err := conn.GetStorageVolumeFile(bp.Config().DefaultStoragePool, "custom", "ic-backup-manifest", "backups.json")
	if incusApi.StatusErrorCheck(err, http.StatusNotFound) {
		return nil
	}
	require.NoError(t, err)
	defer data.Close()

	content, err := io.ReadAll(data)
	require.NoError(t, err)

	var m struct {
		Backups []client.BackupEntry `json:"backups"`
	}
	err = json.Unmarshal(content, &m)
	require.NoError(t, err)

	return m.Backups
}

func openBackupProject(ctx context.Context, t *testing.T, composeProject string) *client.Client {
	t.Helper()

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	err = gc.Connect()
	require.NoError(t, err)

	backupProject := composeProject + "-backup"
	c, err := gc.EnsureProject(backupProject)
	require.NoError(t, err)

	err = c.Open()
	require.NoError(t, err)

	return c
}

func deleteBackupProject(ctx context.Context, t *testing.T, composeProject string) {
	gc, err := client.NewTestClient(ctx)
	if err != nil {
		return
	}

	_ = gc.Connect()
	_ = gc.DeleteProject(composeProject+"-backup", true)
}

func assertNoLockFile(t *testing.T, c *client.Client) {
	t.Helper()

	conn, err := c.Connection()
	require.NoError(t, err)

	_, _, err = conn.GetStorageVolumeFile(c.Config().DefaultStoragePool, "custom", "ic-backup-manifest", "backup.lock")
	if incusApi.StatusErrorCheck(err, http.StatusNotFound) {
		return
	}

	t.Errorf("lock file should not exist after backup completes, but it was found")
}

func TestE2EBackupCreate(t *testing.T) {
	t.Parallel()

	compose := "../../test/fixtures/with-backup/compose.yaml"
	projectDir := t.TempDir()

	ctx := t.Context()
	pn := t.Name()

	t.Cleanup(func() {
		runCommand(context.Background(), t, pn, "-f", compose, "down", "--project")
		deleteBackupProject(context.Background(), t, pn)
	})

	_, err := runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	stdout, err := runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "backup", "create")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "created with timestamp")

	bp := openBackupProject(ctx, t, pn)
	defer func() { _ = bp.Done() }()

	entries := readBackupManifest(t, bp)
	require.Len(t, entries, 1)
	assert.Equal(t, "backup", entries[0].Name)
	assert.NotEmpty(t, entries[0].Pool)
	assert.Equal(t, []string{"data"}, entries[0].Volumes)

	pool := entries[0].Pool
	assertBackupVolumeExists(t, bp, pool, "ic-backup-vol-data")
	assertBackupSnapshotExists(t, bp, pool, "ic-backup-vol-data", entries[0].Timestamp)
	assertNoLockFile(t, bp)

	c := projectClient(ctx, t, pn)
	exists, err := c.InstanceExists("app-1")
	require.NoError(t, err)
	assert.True(t, exists, "service should be restarted after consistent backup")
}

func TestE2EBackupCreateLive(t *testing.T) {
	t.Parallel()

	compose := "../../test/fixtures/with-backup/compose.yaml"
	projectDir := t.TempDir()

	ctx := t.Context()
	pn := t.Name()

	t.Cleanup(func() {
		runCommand(context.Background(), t, pn, "-f", compose, "down", "--project")
		deleteBackupProject(context.Background(), t, pn)
	})

	_, err := runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "backup", "create", "--live")
	require.NoError(t, err)

	bp := openBackupProject(ctx, t, pn)
	defer func() { _ = bp.Done() }()

	entries := readBackupManifest(t, bp)
	require.Len(t, entries, 1)

	pool := entries[0].Pool
	assertBackupVolumeExists(t, bp, pool, "ic-backup-vol-data")
	assertBackupSnapshotExists(t, bp, pool, "ic-backup-vol-data", entries[0].Timestamp)
	assertNoLockFile(t, bp)
}

func TestE2EBackupCreateNamed(t *testing.T) {
	t.Parallel()

	compose := "../../test/fixtures/with-backup/compose.yaml"
	projectDir := t.TempDir()

	ctx := t.Context()
	pn := t.Name()

	t.Cleanup(func() {
		runCommand(context.Background(), t, pn, "-f", compose, "down", "--project")
		deleteBackupProject(context.Background(), t, pn)
	})

	_, err := runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "backup", "create", "--name", "daily")
	require.NoError(t, err)

	bp := openBackupProject(ctx, t, pn)
	defer func() { _ = bp.Done() }()

	entries := readBackupManifest(t, bp)
	require.Len(t, entries, 1)
	assert.Equal(t, "daily", entries[0].Name)
	assertNoLockFile(t, bp)
}

func TestE2EBackupCreateIncremental(t *testing.T) {
	t.Parallel()

	compose := "../../test/fixtures/with-backup/compose.yaml"
	projectDir := t.TempDir()

	ctx := t.Context()
	pn := t.Name()

	t.Cleanup(func() {
		runCommand(context.Background(), t, pn, "-f", compose, "down", "--project")
		deleteBackupProject(context.Background(), t, pn)
	})

	_, err := runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "backup", "create", "--name", "first")
	require.NoError(t, err)

	_, err = runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "backup", "create", "--name", "second")
	require.NoError(t, err)

	bp := openBackupProject(ctx, t, pn)
	defer func() { _ = bp.Done() }()

	entries := readBackupManifest(t, bp)
	require.Len(t, entries, 2)
	assert.Equal(t, "first", entries[0].Name)
	assert.Equal(t, "second", entries[1].Name)
	assert.NotEqual(t, entries[0].Timestamp, entries[1].Timestamp)
	assertNoLockFile(t, bp)
}

func TestE2EBackupCreateFiltered(t *testing.T) {
	t.Parallel()

	compose := writeTempCompose(t, `
services:
  db:
    image: docker.io/library/nginx:alpine
    volumes:
      - type: volume
        source: db-data
        target: /data
  app:
    image: docker.io/library/nginx:alpine
    volumes:
      - type: volume
        source: app-data
        target: /data

volumes:
  db-data:
  app-data:
`)
	projectDir := t.TempDir()

	ctx := t.Context()
	pn := t.Name()

	t.Cleanup(func() {
		runCommand(context.Background(), t, pn, "-f", compose, "down", "--project")
		deleteBackupProject(context.Background(), t, pn)
	})

	_, err := runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "backup", "create", "db")
	require.NoError(t, err)

	bp := openBackupProject(ctx, t, pn)
	defer func() { _ = bp.Done() }()

	entries := readBackupManifest(t, bp)
	require.Len(t, entries, 1)
	assert.Equal(t, []string{"db-data"}, entries[0].Volumes)
	assertNoLockFile(t, bp)
}

func TestE2EBackupCreateNoVolumes(t *testing.T) {
	t.Parallel()

	compose := "../../test/fixtures/with-backup/compose.yaml"
	projectDir := t.TempDir()

	ctx := t.Context()
	pn := t.Name()

	t.Cleanup(func() {
		runCommand(context.Background(), t, pn, "-f", compose, "down", "--project")
		deleteBackupProject(context.Background(), t, pn)
	})

	_, err := runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "backup", "create", "nonexistent")
	require.NoError(t, err)

	bp := openBackupProject(ctx, t, pn)
	defer func() { _ = bp.Done() }()

	assertBackupVolumeExists(t, bp, bp.Config().DefaultStoragePool, "ic-backup-manifest")
	entries := readBackupManifest(t, bp)
	require.Len(t, entries, 0)
}

func TestE2EBackupCreateDefaultPool(t *testing.T) {
	t.Parallel()

	compose := writeTempCompose(t, `
services:
  app:
    image: docker.io/library/nginx:alpine
    volumes:
      - type: volume
        source: data
        target: /data

volumes:
  data:
`)
	projectDir := t.TempDir()

	ctx := t.Context()
	pn := t.Name()

	t.Cleanup(func() {
		runCommand(context.Background(), t, pn, "-f", compose, "down", "--project")
		deleteBackupProject(context.Background(), t, pn)
	})

	_, err := runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "backup", "create")
	require.NoError(t, err)

	bp := openBackupProject(ctx, t, pn)
	defer func() { _ = bp.Done() }()

	entries := readBackupManifest(t, bp)
	require.Len(t, entries, 1)

	pool := bp.Config().DefaultStoragePool
	assertBackupVolumeExists(t, bp, pool, "ic-backup-vol-data")
	assertNoLockFile(t, bp)
}

func TestE2EBackupCreateParallel(t *testing.T) {
	t.Parallel()

	compose := "../../test/fixtures/with-backup/compose.yaml"
	projectDir := t.TempDir()

	ctx := t.Context()
	pn := t.Name()

	t.Cleanup(func() {
		runCommand(context.Background(), t, pn, "-f", compose, "down", "--project")
		deleteBackupProject(context.Background(), t, pn)
	})

	_, err := runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = runCommand(ctx, t, pn, "--project-directory", projectDir, "-f", compose, "backup", "create")
		}(i)
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	bp := openBackupProject(ctx, t, pn)
	defer func() { _ = bp.Done() }()

	entries := readBackupManifest(t, bp)
	require.Len(t, entries, 2)
	assertNoLockFile(t, bp)
}
