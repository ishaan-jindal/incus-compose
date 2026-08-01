package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	backupManifestFile   = "backups.json"
	backupManifestVolume = backupVolumePrefix + "manifest"
	backupVolumePrefix   = "ic-backup-"
)

// BackupManager manages volume backups for a compose project.
type BackupManager struct {
	globalClient  *GlobalClient
	composeClient *Client
	backupClient  *Client
	composePool   string
	backupPool    string
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
			return nil, fmt.Errorf("create manifest volume: %w", err)
		}
	}

	return &BackupManager{
		globalClient:  globalClient,
		composeClient: composeClient,
		backupClient:  backupClient,
		composePool:   composePool,
		backupPool:    pool,
	}, nil
}

// Done returns any accumulated error from the backup client.
func (bm *BackupManager) Done() error {
	return bm.backupClient.Done()
}

func incusVolumeName(name string) string {
	return "vol-" + SanitizeIncusName(name, MaxIncusNameLen-4)
}

func manifestFromJSON(data []byte) (*manifest, error) {
	m := &manifest{Backups: []BackupEntry{}}

	if len(data) == 0 {
		return m, nil
	}

	err := json.Unmarshal(data, m)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	return m, nil
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

	return manifestFromJSON(content)
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
