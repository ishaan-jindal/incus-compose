package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	backupManifestDir  = ".incus-compose"
	backupManifestFile = "backups.json"
	backupVolumePrefix = "ic-backup-"
)

// BackupManager manages volume backups for a compose project.
type BackupManager struct {
	globalClient  *GlobalClient
	composeClient *Client
	backupClient  *Client
	composePool   string
	backupPool    string
	projectDir    string
}

// NewBackupManager creates a new BackupManager and ensures the backup project exists.
func NewBackupManager(globalClient *GlobalClient, composeClient *Client, composePool string, backupConfig BackupConfig, projectDir string) (*BackupManager, error) {
	backupProject := composeClient.Project() + "-backup"

	backupClient, err := globalClient.EnsureProject(backupProject, EnsureProjectWithCreate())
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

	return &BackupManager{
		globalClient:  globalClient,
		composeClient: composeClient,
		backupClient:  backupClient,
		composePool:   composePool,
		backupPool:    pool,
		projectDir:    projectDir,
	}, nil
}

// Done returns any accumulated error from the backup client.
func (bm *BackupManager) Done() error {
	return bm.backupClient.Done()
}

func incusVolumeName(name string) string {
	return "vol-" + SanitizeIncusName(name, MaxIncusNameLen-4)
}

func (bm *BackupManager) manifestPath() string {
	return filepath.Join(bm.projectDir, backupManifestDir, backupManifestFile)
}

func (bm *BackupManager) readManifest() (*manifest, error) {
	m := &manifest{Backups: []BackupEntry{}}

	data, err := os.ReadFile(bm.manifestPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}

		return nil, fmt.Errorf("read manifest: %w", err)
	}

	err = json.Unmarshal(data, m)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	return m, nil
}

func (bm *BackupManager) writeManifest(m *manifest) error {
	dir := filepath.Join(bm.projectDir, backupManifestDir)

	err := os.MkdirAll(dir, 0o750)
	if err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	err = os.WriteFile(bm.manifestPath(), data, 0o600)
	if err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	return nil
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func backupIncusVolumeName(incusName string) string {
	return backupVolumePrefix + incusName
}

// CreateBackup copies volumes to the backup project and snapshots the mirrors.
func (bm *BackupManager) CreateBackup(ctx context.Context, name string, volumes []string) (string, error) {
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
		if err != nil {
			return "", fmt.Errorf("copy volume %s: %w", volName, err)
		}

		err = copyOp.Wait()
		if err != nil {
			return "", fmt.Errorf("wait for volume copy %s: %w", volName, err)
		}

		snap := incusApi.StorageVolumeSnapshotsPost{
			Name: ts,
		}

		snapOp, err := conn.CreateStoragePoolVolumeSnapshot(bm.backupPool, "custom", bkpName, snap)
		if err != nil {
			return "", fmt.Errorf("snapshot volume %s: %w", volName, err)
		}

		err = snapOp.Wait()
		if err != nil {
			return "", fmt.Errorf("wait for snapshot %s: %w", volName, err)
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
