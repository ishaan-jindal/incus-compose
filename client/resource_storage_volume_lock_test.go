package client

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
)

func ensuredLockTestVolume(t *testing.T, c *Client, name string) *StorageVolume {
	t.Helper()

	r, err := c.Resource(KindStorageVolume, name, &StorageVolumeConfig{})
	require.NoError(t, err)
	require.NoError(t, RunAction(t.Context(), r, ActionEnsure, OptionCreate()))

	vol, ok := r.(*StorageVolume)
	require.True(t, ok)
	return vol
}

// lockTestSFTP opens an SFTP connection to vol and registers t.Cleanup to close it.
func lockTestSFTP(t *testing.T, vol *StorageVolume) *sftp.Client {
	t.Helper()

	sc, err := vol.SFTP()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sc.Close() })
	return sc
}

func TestStorageVolumeLock_NotEnsured(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	c := newRandomTestClient(t.Context(), t, "volume-lock-notensured-")

	r, err := c.Resource(KindStorageVolume, "unensured", &StorageVolumeConfig{})
	require.NoError(t, err)
	vol, ok := r.(*StorageVolume)
	require.True(t, ok)

	_, err = vol.SFTP()
	require.ErrorIs(t, err, ErrNotEnsured)

	_, err = vol.Lock(t.Context(), nil, "test.lock", 0)
	require.ErrorIs(t, err, ErrNotEnsured)
}

func TestStorageVolumeLock_AcquireExcludesAndUnlockRemoves(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	c := newRandomTestClient(t.Context(), t, "volume-lock-")
	vol := ensuredLockTestVolume(t, c, "locked-vol")

	sc := lockTestSFTP(t, vol)
	lock, err := vol.Lock(t.Context(), sc, "test.lock", 0)
	require.NoError(t, err)

	// Held: a second, independent connection cannot create the same file exclusively.
	probe := lockTestSFTP(t, vol)
	_, err = probe.OpenFile("/test.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	require.Error(t, err)

	require.NoError(t, lock.Unlock())

	// Released: the lock file is gone, so creating it exclusively now succeeds.
	f, err := probe.OpenFile("/test.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func TestStorageVolumeLock_BlocksUntilContextDone(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	c := newRandomTestClient(t.Context(), t, "volume-lock-block-")
	vol := ensuredLockTestVolume(t, c, "blocked-vol")

	sc := lockTestSFTP(t, vol)
	lock, err := vol.Lock(t.Context(), sc, "test.lock", 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Unlock() })

	// Open the competing connection before starting the clock - dialing it is a
	// real network round trip, and timing that against ctx's deadline instead of
	// against Lock's actual start under-counts elapsed time on a slow runner.
	competingSC := lockTestSFTP(t, vol)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = vol.Lock(ctx, competingSC, "test.lock", 0)
	require.Error(t, err)
	// It blocked for roughly the ctx timeout rather than returning instantly;
	// a few ms of scheduling slop around the deadline is expected, not a bug.
	require.GreaterOrEqual(t, time.Since(start), 400*time.Millisecond)
}

func TestStorageVolumeLock_StaleTakeoverAfterCrash(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	c := newRandomTestClient(t.Context(), t, "volume-lock-stale-")
	vol := ensuredLockTestVolume(t, c, "stale-vol")

	stale := 300 * time.Millisecond

	scA := lockTestSFTP(t, vol)
	lockA, err := vol.Lock(t.Context(), scA, "test.lock", stale)
	require.NoError(t, err)

	// Simulate a crash: kill the heartbeat without releasing the lock file.
	lockA.cancel()
	lockA.wg.Wait()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	scB := lockTestSFTP(t, vol)
	lockB, err := vol.Lock(ctx, scB, "test.lock", stale)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lockB.Unlock() })

	probe := lockTestSFTP(t, vol)
	_, err = probe.Stat("/test.lock")
	require.NoError(t, err, "lockB should have reaped and taken over the stale lock")
}

func TestStorageVolumeLock_HeartbeatPreventsStaleTakeover(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	c := newRandomTestClient(t.Context(), t, "volume-lock-heartbeat-")
	vol := ensuredLockTestVolume(t, c, "heartbeat-vol")

	// A generous stale window so a real heartbeat round trip has headroom against
	// scheduler/network jitter under parallel test load - the point being tested
	// is "never reaped while live", not how tight the window can be cut.
	stale := time.Second

	sc := lockTestSFTP(t, vol)
	lock, err := vol.Lock(t.Context(), sc, "test.lock", stale)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Unlock() })

	// Outlive several heartbeats; a live holder's mtime should never go stale.
	time.Sleep(stale * 3)

	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()

	competingSC := lockTestSFTP(t, vol)
	_, err = vol.Lock(ctx, competingSC, "test.lock", stale)
	require.Error(t, err, "a live, heartbeating lock must not be reaped")
}

func TestStorageVolumeLock_UnlockDeletesOnlyOwnLock(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	c := newRandomTestClient(t.Context(), t, "volume-lock-owner-")
	vol := ensuredLockTestVolume(t, c, "owner-vol")

	scA := lockTestSFTP(t, vol)
	lockA, err := vol.Lock(t.Context(), scA, "test.lock", 0)
	require.NoError(t, err)

	// Directly overwrite the lock file to simulate another holder taking it
	// over between lockA's acquire and its (delayed) unlock.
	probe := lockTestSFTP(t, vol)
	require.NoError(t, probe.Remove(lockA.path))
	f, err := probe.OpenFile(lockA.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	require.NoError(t, err)
	_, err = f.Write([]byte("someone-else"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, lockA.Unlock())

	_, err = probe.Stat("/test.lock")
	require.NoError(t, err, "unlock must not delete a lock file it no longer owns")
}
