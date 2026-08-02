package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/avast/retry-go/v5"
	incusClient "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
)

// BackupConfig holds backup configuration from x-incus-compose.backup.
type BackupConfig struct {
	Pool string
}

// BackupEntry represents a single backup in the manifest.
type BackupEntry struct {
	Timestamp string   `json:"timestamp"`
	Name      string   `json:"name"`
	Pool      string   `json:"pool"`
	Volumes   []string `json:"volumes"`
}

// manifest is the top-level manifest document stored as JSON.
type manifest struct {
	Backups []BackupEntry `json:"backups"`
}

const (
	backupManifestFile   = "backups.json"
	backupManifestVolume = backupVolumePrefix + "manifest"
	backupVolumePrefix   = "ic-backup-"
	backupLockFile       = "backup.lock"
)

// BackupManager manages volume backups for a compose project.
type BackupManager struct {
	composeClient *Client
	backupClient  *Client
	composePool   string
	backupPool    string

	// Lock parameters, overridable in tests.
	lockTimeout time.Duration
	lockStale   time.Duration
	lockRefresh time.Duration

	// lockCtx/lockCancel keep the lock refresh goroutine alive until the lock is released.
	lockCtx    context.Context
	lockCancel context.CancelFunc
}

// NewBackupManager creates a new BackupManager and ensures the backup project and manifest volume exist.
func NewBackupManager(globalClient *GlobalClient, composeClient *Client, composePool string, backupConfig BackupConfig) (*BackupManager, error) {
	backupProject := composeClient.Project() + "-backup"

	backupClient, err := globalClient.EnsureProject(backupProject, EnsureProjectWithCreate(), EnsureProjectWithConfig(map[string]string{"restricted": "false"}))
	if err != nil {
		return nil, fmt.Errorf("ensure backup project: %w", err)
	}

	err = backupClient.Open()
	if err != nil {
		return nil, fmt.Errorf("open backup client: %w", err)
	}

	pool := backupConfig.Pool
	if pool == "" {
		pool = composePool
	}

	conn, err := backupClient.Connection()
	if err != nil {
		return nil, err
	}

	_, _, err = conn.GetStoragePoolVolume(pool, "custom", backupManifestVolume)
	if err != nil {
		if !incusApi.StatusErrorCheck(err, http.StatusNotFound) {
			return nil, fmt.Errorf("get manifest volume: %w", err)
		}

		volReq := incusApi.StorageVolumesPost{
			Name:        backupManifestVolume,
			Type:        "custom",
			ContentType: "filesystem",
		}

		err = conn.CreateStoragePoolVolume(pool, volReq)
		if err != nil {
			// Another backup may have created the volume in the meantime.
			_, _, getErr := conn.GetStoragePoolVolume(pool, "custom", backupManifestVolume)
			if getErr != nil {
				return nil, fmt.Errorf("create manifest volume: %w", err)
			}
		}
	}

	return &BackupManager{
		composeClient: composeClient,
		backupClient:  backupClient,
		composePool:   composePool,
		backupPool:    pool,
		lockTimeout:   10 * time.Minute,
		lockStale:     time.Minute,
		lockRefresh:   30 * time.Second,
	}, nil
}

// Done fires the done hooks on the backup client.
func (bm *BackupManager) Done() error {
	return bm.backupClient.Done()
}

func incusVolumeName(name string) string {
	return "vol-" + SanitizeIncusName(name, MaxIncusNameLen-4)
}

func (bm *BackupManager) readManifest() (*manifest, error) {
	conn, err := bm.backupClient.Connection()
	if err != nil {
		return nil, err
	}

	data, _, err := conn.GetStorageVolumeFile(bm.backupPool, "custom", backupManifestVolume, backupManifestFile)
	if err != nil {
		if incusApi.StatusErrorCheck(err, http.StatusNotFound) {
			return &manifest{Backups: []BackupEntry{}}, nil
		}

		return nil, fmt.Errorf("read manifest: %w", err)
	}
	defer data.Close()

	content, err := io.ReadAll(data)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	m := &manifest{Backups: []BackupEntry{}}
	if len(content) == 0 {
		return m, nil
	}

	err = json.Unmarshal(content, m)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	return m, nil
}

func (bm *BackupManager) writeManifest(m *manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	conn, err := bm.backupClient.Connection()
	if err != nil {
		return err
	}

	args := incusClient.InstanceFileArgs{
		Content:   bytes.NewReader(data),
		WriteMode: "overwrite",
		Mode:      0o600,
	}

	err = conn.CreateStorageVolumeFile(bm.backupPool, "custom", backupManifestVolume, backupManifestFile, args)
	if err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	return nil
}

func (bm *BackupManager) acquireLock(ctx context.Context) error {
	lockCtx, cancel := context.WithTimeout(ctx, bm.lockTimeout)
	defer cancel()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		existing, err := bm.readLock()
		if err != nil {
			return fmt.Errorf("acquire backup lock: %w", err)
		}

		if existing == nil || time.Since(*existing) > bm.lockStale {
			now := time.Now().UTC()
			err = bm.writeLock(now)
			if err != nil {
				return fmt.Errorf("acquire backup lock: %w", err)
			}

			// Give concurrent writers time to land before confirming ownership.
			time.Sleep(100 * time.Millisecond)

			current, err := bm.readLock()
			if err != nil {
				return fmt.Errorf("acquire backup lock: %w", err)
			}

			if current != nil && current.Equal(now) {
				bm.lockCtx, bm.lockCancel = context.WithCancel(ctx)
				go bm.refreshLock()

				return nil
			}
		}

		select {
		case <-lockCtx.Done():
			return fmt.Errorf("timed out waiting for backup lock: %w", lockCtx.Err())
		case <-ticker.C:
		}
	}
}

// refreshLock keeps the lock fresh while a backup is running so long backups don't go stale.
func (bm *BackupManager) refreshLock() {
	ticker := time.NewTicker(bm.lockRefresh)
	defer ticker.Stop()

	for {
		select {
		case <-bm.lockCtx.Done():
			return
		case <-ticker.C:
			// Keep refreshing on transient errors; the lock only goes stale when the process dies.
			_ = bm.writeLock(time.Now().UTC())
		}
	}
}

func (bm *BackupManager) releaseLock() {
	if bm.lockCancel != nil {
		bm.lockCancel()
		bm.lockCancel = nil
	}

	conn, err := bm.backupClient.Connection()
	if err != nil {
		return
	}

	// A refresh write may be in flight; retry so it does not outlive the release.
	_ = retry.New(
		retry.Attempts(3),
		retry.Delay(200*time.Millisecond),
	).Do(func() error {
		return conn.DeleteStorageVolumeFile(bm.backupPool, "custom", backupManifestVolume, backupLockFile)
	})
}

func (bm *BackupManager) readLock() (*time.Time, error) {
	conn, err := bm.backupClient.Connection()
	if err != nil {
		return nil, err
	}

	data, _, err := conn.GetStorageVolumeFile(bm.backupPool, "custom", backupManifestVolume, backupLockFile)
	if err != nil {
		if incusApi.StatusErrorCheck(err, http.StatusNotFound) {
			return nil, nil
		}

		return nil, fmt.Errorf("get lock file: %w", err)
	}
	defer data.Close()

	content, err := io.ReadAll(data)
	if err != nil {
		// A concurrent refresh may replace the file mid-read; treat it as an absent lock.
		return nil, nil
	}

	ts, err := time.Parse(time.RFC3339Nano, string(content))
	if err != nil {
		// A concurrent refresh may write mid-read; treat unparseable content as an absent lock.
		return nil, nil
	}

	return &ts, nil
}

func (bm *BackupManager) writeLock(ts time.Time) error {
	conn, err := bm.backupClient.Connection()
	if err != nil {
		return err
	}

	args := incusClient.InstanceFileArgs{
		Content:   bytes.NewReader([]byte(ts.UTC().Format(time.RFC3339Nano))),
		WriteMode: "overwrite",
		Mode:      0o600,
	}

	err = conn.CreateStorageVolumeFile(bm.backupPool, "custom", backupManifestVolume, backupLockFile, args)
	if err != nil {
		return fmt.Errorf("create lock file: %w", err)
	}

	return nil
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func backupIncusVolumeName(incusName string) string {
	return backupVolumePrefix + incusName
}

// Create copies volumes to the backup project and snapshots the mirrors.
func (bm *BackupManager) Create(ctx context.Context, name string, volumes []string) (string, error) {
	err := bm.acquireLock(ctx)
	if err != nil {
		return "", err
	}
	defer bm.releaseLock()

	ts := timestamp()

	conn, err := bm.backupClient.Connection()
	if err != nil {
		return "", err
	}

	sourceConn, err := bm.composeClient.Connection()
	if err != nil {
		return "", err
	}

	entry := BackupEntry{
		Timestamp: ts,
		Name:      name,
		Pool:      bm.backupPool,
	}

	for _, volName := range volumes {
		incusName := incusVolumeName(volName)
		bkpName := backupIncusVolumeName(incusName)

		mirrorExists := true
		_, _, err := conn.GetStoragePoolVolume(bm.backupPool, "custom", bkpName)
		if err != nil {
			mirrorExists = false
		}

		srcVol := incusApi.StorageVolume{
			Name:        incusName,
			Type:        "custom",
			ContentType: "filesystem",
		}

		copyArgs := incusClient.StoragePoolVolumeCopyArgs{
			Name:       bkpName,
			Refresh:    mirrorExists,
			VolumeOnly: true,
		}

		copyOp, err := conn.CopyStoragePoolVolume(bm.backupPool, sourceConn, bm.composePool, srcVol, &copyArgs)
		err = bm.backupClient.hookRemoteOperation(ctx, ActionEnsure, nil, NewOptions(), copyOp, err)
		if err != nil {
			return "", fmt.Errorf("copy volume %s: %w", volName, err)
		}

		snap := incusApi.StorageVolumeSnapshotsPost{
			Name: ts,
		}

		snapOp, err := conn.CreateStoragePoolVolumeSnapshot(bm.backupPool, "custom", bkpName, snap)
		err = bm.backupClient.hookOperation(ctx, ActionEnsure, nil, NewOptions(), snapOp, err)
		if err != nil {
			return "", fmt.Errorf("snapshot volume %s: %w", volName, err)
		}

		entry.Volumes = append(entry.Volumes, volName)
	}

	m, err := bm.readManifest()
	if err != nil {
		return "", err
	}

	m.Backups = append(m.Backups, entry)

	err = bm.writeManifest(m)
	if err != nil {
		return "", err
	}

	return ts, nil
}
