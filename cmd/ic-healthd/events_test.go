package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/shared"
)

func writeTempFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	return dir
}

// trackedCopy copies r.tracked so a test goroutine can inspect it without racing.
func trackedCopy(r *Runner) map[string]*trackedInstance {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[string]*trackedInstance, len(r.tracked))
	for k, v := range r.tracked {
		out[k] = v
	}
	return out
}

// TestE2EEventDrivenDiscovery covers the whole event pipeline against a real Incus:
// pick up instances via events, respawn on a live config edit, drop on delete.
func TestE2EEventDrivenDiscovery(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(t.Context())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/proxy/compose.yaml"

	c, p := loadProject(ctx, t, compose, projectName)
	err := c.Open()
	require.NoError(t, err)

	hCleanup, hRunner := prepareHealthd(t, c)
	hReload := make(chan struct{}, 10)

	go func() {
		_ = hRunner.Run(ctx, hReload)
	}()

	t.Cleanup(func() {
		_ = c.Done()

		_, _, _ = runIncusCommand(context.Background(), t, projectName, "-f", compose, "down", "--project")
		hCleanup()
		cancel()
	})

	c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

	// Listening, but nothing to track yet: the project's instances don't exist.
	require.Eventually(t, func() bool {
		return len(trackedCopy(hRunner)) == 0
	}, 10*time.Second, 100*time.Millisecond, "daemon should have started with nothing to track")

	stack := client.NewStack(c, client.StackFailFast())
	order, err := p.ServiceOrder(false)
	require.NoError(t, err)

	resources, err := p.Resources(c)
	require.NoError(t, err)
	stack.AddOrdered(order, resources)

	err = stack.ForAction(client.ActionEnsure).Run(
		ctx, client.ActionEnsure, client.OptionCreate(),
	)
	require.NoError(t, err)

	err = stack.ForAction(client.ActionStart).Run(
		ctx, client.ActionStart, client.OptionExternalHealthd(),
	)
	require.NoError(t, err)

	// Events, not the initial resync, should pick up all three instances.
	require.Eventually(t, func() bool {
		return len(trackedCopy(hRunner)) == 3
	}, 30*time.Second, 200*time.Millisecond, "instances should be tracked via lifecycle events")

	conn, err := c.Connection()
	require.NoError(t, err)

	// All reported healthy proves the checkers spawned from those events run.
	names := []string{}
	for name := range trackedCopy(hRunner) {
		names = append(names, name)
	}
	require.Len(t, names, 3)

	for _, name := range names {
		name := name
		require.Eventually(t, func() bool {
			inst, _, err := conn.GetInstance(name)
			return err == nil && inst.Config[shared.HealthStatusKey] == shared.HealthStatusHealthy
		}, 30*time.Second, 500*time.Millisecond, "instance %s should become healthy", name)
	}

	// A live interval edit should debounce, then replace the checker with new params.
	target := names[0]
	inst, etag, err := conn.GetInstance(target)
	require.NoError(t, err)

	wInst := inst.Writable()
	wInst.Config[shared.HealthKeyPrefix+"interval"] = "7s"
	op, err := conn.UpdateInstance(target, wInst, etag)
	require.NoError(t, err)
	require.NoError(t, op.Wait())

	require.Eventually(t, func() bool {
		tracked := trackedCopy(hRunner)
		ti, ok := tracked[target]
		return ok && ti.knownParams.Interval == 7*time.Second
	}, 15*time.Second, 200*time.Millisecond, "config change should debounce-respawn the checker with new params")

	// instance-deleted should drop tracking once the debounce window settles.
	stopOp, err := conn.UpdateInstanceState(target, incusApi.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "")
	require.NoError(t, err)
	require.NoError(t, stopOp.Wait())

	delOp, err := conn.DeleteInstance(target)
	require.NoError(t, err)
	require.NoError(t, delOp.Wait())

	require.Eventually(t, func() bool {
		_, ok := trackedCopy(hRunner)[target]
		return !ok
	}, 15*time.Second, 200*time.Millisecond, "deleted instance should be dropped from tracking")
}

// TestE2EIgnoredInstanceIsNeverTracked covers user.healthcheck.ignore in resync and events.
func TestE2EIgnoredInstanceIsNeverTracked(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(t.Context())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/with-restart/compose.yaml"

	c, p := loadProject(ctx, t, compose, projectName)
	err := c.Open()
	require.NoError(t, err)

	hCleanup, hRunner := prepareHealthd(t, c)
	hReload := make(chan struct{}, 10)

	go func() {
		_ = hRunner.Run(ctx, hReload)
	}()

	t.Cleanup(func() {
		_ = c.Done()

		_, _, _ = runIncusCommand(context.Background(), t, projectName, "-f", compose, "down", "--project")
		hCleanup()
		cancel()
	})

	c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

	resources, err := p.Resources(c)
	require.NoError(t, err)

	order, err := p.ServiceOrder(false)
	require.NoError(t, err)

	stack := client.NewStack(c, client.StackFailFast())
	stack.AddOrdered(order, resources)

	err = stack.ForAction(client.ActionEnsure).Run(
		ctx, client.ActionEnsure, client.OptionCreate(),
	)
	require.NoError(t, err)

	svcResources, ok := resources["always-restart"]
	require.True(t, ok)

	var inst *client.Instance
	for _, r := range svcResources {
		if i, ok := r.(*client.Instance); ok {
			inst = i
		}
	}
	require.NotNil(t, inst)

	conn, err := c.Connection()
	require.NoError(t, err)

	i, etag, err := conn.GetInstance(inst.IncusName())
	require.NoError(t, err)

	wInst := i.Writable()
	wInst.Config[healthIgnoreKey] = "true"
	op, err := conn.UpdateInstance(inst.IncusName(), wInst, etag)
	require.NoError(t, err)
	require.NoError(t, op.Wait())

	require.NoError(t, inst.Start(ctx))

	// Give the lifecycle events a fair chance to arrive before asserting.
	time.Sleep(3 * time.Second)

	_, tracked := trackedCopy(hRunner)[inst.IncusName()]
	require.False(t, tracked, "ignored instance must never be tracked")
}

// TestE2ECrashedInstanceRestarts stops an instance through the raw Incus API, which
// unlike incus-compose's stop is not marked intentional, so the runner restarts it.
func TestE2ECrashedInstanceRestarts(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(t.Context())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/proxy/compose.yaml"

	c, p := loadProject(ctx, t, compose, projectName)
	err := c.Open()
	require.NoError(t, err)

	hCleanup, hRunner := prepareHealthd(t, c)
	hReload := make(chan struct{}, 10)

	go func() {
		_ = hRunner.Run(ctx, hReload)
	}()

	t.Cleanup(func() {
		_ = c.Done()

		_, _, _ = runIncusCommand(context.Background(), t, projectName, "-f", compose, "down", "--project")
		hCleanup()
		cancel()
	})

	c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

	stack := client.NewStack(c, client.StackFailFast())
	order, err := p.ServiceOrder(false)
	require.NoError(t, err)

	resources, err := p.Resources(c)
	require.NoError(t, err)
	stack.AddOrdered(order, resources)

	err = stack.ForAction(client.ActionEnsure).Run(
		ctx, client.ActionEnsure, client.OptionCreate(),
	)
	require.NoError(t, err)

	err = stack.ForAction(client.ActionStart).Run(
		ctx, client.ActionStart, client.OptionExternalHealthd(),
	)
	require.NoError(t, err)

	// backend1 depends on nothing, so it is healthy as soon as its own check passes.
	target := "backend1-1"

	conn, err := c.Connection()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		inst, _, err := conn.GetInstance(target)
		return err == nil && inst.Config[shared.HealthStatusKey] == shared.HealthStatusHealthy
	}, 30*time.Second, 500*time.Millisecond, "instance should become healthy before the crash")

	// Crash it: a raw API stop is never marked user.healthcheck.stopped.
	stopOp, err := conn.UpdateInstanceState(target, incusApi.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "")
	require.NoError(t, err)
	require.NoError(t, stopOp.Wait())

	inst, _, err := conn.GetInstance(target)
	require.NoError(t, err)
	require.NotEqual(t, "true", inst.Config[shared.HealthStoppedKey], "a raw API stop must not look intentional")

	require.Eventually(t, func() bool {
		state, _, err := conn.GetInstanceState(target)
		return err == nil && state.StatusCode == incusApi.Running
	}, 30*time.Second, 500*time.Millisecond, "crashed instance should be restarted by the runner")

	require.Eventually(t, func() bool {
		inst, _, err := conn.GetInstance(target)
		return err == nil && inst.Config[shared.HealthStatusKey] == shared.HealthStatusHealthy
	}, 30*time.Second, 500*time.Millisecond, "restarted instance should become healthy again")
}

// TestE2ERepeatedCrashesBackoff crashes an instance three times, timing each restart to
// prove the backoff doubles: crashes land inside the check interval so it is never reset,
// and the fixture pins retries to 1 so the 5s baseline is known upfront.
func TestE2ERepeatedCrashesBackoff(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(t.Context())
	projectName := strings.ToLower(t.Name())

	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  frontend:
    image: docker.io/library/busybox:glibc
    restart: unless-stopped
    command: ["-c", "mkdir -p /www && echo frontend-ok > /www/index.html && httpd -f -v -p 8080 -h /www"]
    x-incus:
      limits.cpu: 1
    depends_on:
      backend1:
        condition: service_healthy
      backend2:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://127.0.0.1:8080"]
      interval: 5s
      timeout: 5s
      retries: 3

  backend1:
    image: docker.io/library/busybox:glibc
    restart: unless-stopped
    command: ["-c", "mkdir -p /www && echo backend1-ok > /www/index.html && httpd -f -v -p 8080 -h /www"]
    x-incus:
      limits.cpu: 1
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://127.0.0.1:8080"]
      interval: 5s
      timeout: 5s
      retries: 1

  backend2:
    image: docker.io/library/busybox:glibc
    restart: unless-stopped
    command: ["-c", "mkdir -p /www && echo backend2-ok > /www/index.html && httpd -f -v -p 8080 -h /www"]
    x-incus:
      limits.cpu: 1
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://127.0.0.1:8080"]
      interval: 5s
      timeout: 5s
      retries: 3
`,
	})
	compose := filepath.Join(dir, "compose.yaml")

	c, p := loadProject(ctx, t, compose, projectName)
	err := c.Open()
	require.NoError(t, err)

	hCleanup, hRunner := prepareHealthd(t, c)
	hReload := make(chan struct{}, 10)

	go func() {
		_ = hRunner.Run(ctx, hReload)
	}()

	t.Cleanup(func() {
		_ = c.Done()

		_, _, _ = runIncusCommand(context.Background(), t, projectName, "-f", compose, "down", "--project")
		hCleanup()
		cancel()

		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	})

	c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

	stack := client.NewStack(c, client.StackFailFast())
	order, err := p.ServiceOrder(false)
	require.NoError(t, err)

	resources, err := p.Resources(c)
	require.NoError(t, err)
	stack.AddOrdered(order, resources)

	err = stack.ForAction(client.ActionEnsure).Run(
		ctx, client.ActionEnsure, client.OptionCreate(),
	)
	require.NoError(t, err)

	err = stack.ForAction(client.ActionStart).Run(
		ctx, client.ActionStart, client.OptionExternalHealthd(),
	)
	require.NoError(t, err)

	target := "backend1-1"

	conn, err := c.Connection()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		inst, _, err := conn.GetInstance(target)
		return err == nil && inst.Config[shared.HealthStatusKey] == shared.HealthStatusHealthy
	}, 30*time.Second, 500*time.Millisecond, "instance should become healthy before the crash loop")

	wantDelay := 5 * time.Second // baseline: interval(5s) * retries(1), backend1's default healthcheck
	for i := range 3 {
		crashed := time.Now()

		stopOp, err := conn.UpdateInstanceState(target, incusApi.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "")
		require.NoError(t, err)
		require.NoError(t, stopOp.Wait())

		var restarted time.Time
		require.Eventually(t, func() bool {
			state, _, err := conn.GetInstanceState(target)
			if err == nil && state.StatusCode == incusApi.Running {
				restarted = time.Now()
				return true
			}
			return false
		}, wantDelay+20*time.Second, 100*time.Millisecond, "crash %d should be restarted", i+1)

		elapsed := restarted.Sub(crashed)
		t.Logf("crash %d: restarted after %s (backoff floor %s)", i+1, elapsed, wantDelay)
		require.GreaterOrEqual(t, elapsed, wantDelay, "crash %d should not restart before its backoff delay", i+1)

		wantDelay = min(wantDelay*2, maxRestartDelay)
	}
}

// TestE2EIntentionalStopIsNotRestarted a stop through incus-compose marks
// user.healthcheck.stopped, so "unless-stopped" must leave the instance down.
func TestE2EIntentionalStopIsNotRestarted(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(t.Context())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/proxy/compose.yaml"

	c, p := loadProject(ctx, t, compose, projectName)
	err := c.Open()
	require.NoError(t, err)

	hCleanup, hRunner := prepareHealthd(t, c)
	hReload := make(chan struct{}, 10)

	go func() {
		_ = hRunner.Run(ctx, hReload)
	}()

	t.Cleanup(func() {
		_ = c.Done()

		_, _, _ = runIncusCommand(context.Background(), t, projectName, "-f", compose, "down", "--project")
		hCleanup()
		cancel()
	})

	c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

	stack := client.NewStack(c, client.StackFailFast())
	order, err := p.ServiceOrder(false)
	require.NoError(t, err)

	resources, err := p.Resources(c)
	require.NoError(t, err)
	stack.AddOrdered(order, resources)

	err = stack.ForAction(client.ActionEnsure).Run(
		ctx, client.ActionEnsure, client.OptionCreate(),
	)
	require.NoError(t, err)

	err = stack.ForAction(client.ActionStart).Run(
		ctx, client.ActionStart, client.OptionExternalHealthd(),
	)
	require.NoError(t, err)

	// backend1 depends on nothing, so it is healthy as soon as its own check passes.
	target := "backend1-1"

	conn, err := c.Connection()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		inst, _, err := conn.GetInstance(target)
		return err == nil && inst.Config[shared.HealthStatusKey] == shared.HealthStatusHealthy
	}, 30*time.Second, 500*time.Millisecond, "instance should become healthy before the stop")

	instRes, err := c.Resource(client.KindInstance, target, &client.InstanceConfig{})
	require.NoError(t, err)
	require.NoError(t, client.RunAction(ctx, instRes, client.ActionEnsure))
	require.NoError(t, client.RunAction(ctx, instRes, client.ActionStop,
		client.OptionExternalHealthd(), client.OptionForce()))

	inst, _, err := conn.GetInstance(target)
	require.NoError(t, err)
	require.Equal(t, "true", inst.Config[shared.HealthStoppedKey], "an incus-compose stop must look intentional")

	// Outlast the restart backoff (interval 5s * retries 3 = 15s) to prove the
	// runner never respawns it.
	require.Never(t, func() bool {
		state, _, err := conn.GetInstanceState(target)
		return err == nil && state.StatusCode == incusApi.Running
	}, 30*time.Second, time.Second, "an intentionally stopped instance must stay stopped")
}

// TestE2EStaleRespawnDoesNotResurrect checks that a re-check inside a timer prevents a restart.
func TestE2EStaleRespawnDoesNotResurrect(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(t.Context())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/proxy/compose.yaml"

	c, p := loadProject(ctx, t, compose, projectName)
	err := c.Open()
	require.NoError(t, err)

	hCleanup, hRunner := prepareHealthd(t, c)
	hReload := make(chan struct{}, 10)

	go func() {
		_ = hRunner.Run(ctx, hReload)
	}()

	t.Cleanup(func() {
		_ = c.Done()

		_, _, _ = runIncusCommand(context.Background(), t, projectName, "-f", compose, "down", "--project")
		hCleanup()
		cancel()
	})

	c.IgnoreError(client.ActionEnsure, client.ErrNotFound)
	c.IgnoreError(client.ActionStop, client.ErrNotRunning)

	stack := client.NewStack(c, client.StackFailFast())
	order, err := p.ServiceOrder(false)
	require.NoError(t, err)

	resources, err := p.Resources(c)
	require.NoError(t, err)
	stack.AddOrdered(order, resources)

	require.NoError(t, stack.ForAction(client.ActionEnsure).Run(
		ctx, client.ActionEnsure, client.OptionCreate(),
	))
	require.NoError(t, stack.ForAction(client.ActionStart).Run(
		ctx, client.ActionStart, client.OptionExternalHealthd(),
	))

	target := "backend1-1"

	conn, err := c.Connection()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		inst, _, err := conn.GetInstance(target)
		return err == nil && inst.Config[shared.HealthStatusKey] == shared.HealthStatusHealthy
	}, 30*time.Second, 500*time.Millisecond, "instance should become healthy first")

	// An unmarked stop looks like a crash, so the runner schedules a respawn
	// interval(5s)*retries(3) = 15s out.
	stopOp, err := conn.UpdateInstanceState(target,
		incusApi.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "")
	require.NoError(t, err)
	require.NoError(t, stopOp.Wait())

	// Now mark it intentionally stopped, well inside that window.
	instRes, err := c.Resource(client.KindInstance, target, &client.InstanceConfig{})
	require.NoError(t, err)
	require.NoError(t, client.RunAction(ctx, instRes, client.ActionEnsure))
	_ = client.RunAction(ctx, instRes, client.ActionStop, client.OptionExternalHealthd())

	inst, _, err := conn.GetInstance(target)
	require.NoError(t, err)
	require.Equal(t, "true", inst.Config[shared.HealthStoppedKey], "the mark must be written")

	// Outlast the scheduled respawn.
	require.Never(t, func() bool {
		state, _, err := conn.GetInstanceState(target)
		return err == nil && state.StatusCode == incusApi.Running
	}, 30*time.Second, time.Second, "a stale respawn must not resurrect a stopped instance")
}
