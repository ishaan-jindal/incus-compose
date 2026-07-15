package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/shared"
)

// trackedCopy returns a shallow copy of r.tracked, safe to inspect from a
// test goroutine without racing the runner's own goroutines.
func trackedCopy(r *Runner) map[string]*trackedInstance {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[string]*trackedInstance, len(r.tracked))
	for k, v := range r.tracked {
		out[k] = v
	}
	return out
}

// TestE2EEventDrivenDiscovery exercises the event-driven pipeline end to end
// against a real Incus: instances created and started after the daemon is
// already listening must be picked up via instance-created/instance-started
// events (not the initial resync, since the daemon starts listening before
// any of the project's instances exist), a live user.healthcheck.* edit must
// debounce-and-respawn the checker with the new params, and instance-deleted
// must drop tracking.
func TestE2EEventDrivenDiscovery(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(context.Background())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

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

		_, _, _ = runIncusCommand(ctx, t, projectName, "-f", compose, "down", "--project")
		hCleanup()
		cancel()
	})

	c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

	// The daemon should be connected and listening, with nothing to track
	// yet - the project's instances don't exist.
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
		ctx, client.ActionEnsure, os.Stdout, os.Stderr, client.OptionCreate(),
	)
	require.NoError(t, err)

	err = stack.ForAction(client.ActionStart).Run(
		ctx, client.ActionStart, os.Stdout, os.Stderr, client.OptionExternalHealthd(),
	)
	require.NoError(t, err)

	// instance-created/instance-started events, not the initial resync
	// (which already ran before these instances existed), should pick up
	// all three instances.
	require.Eventually(t, func() bool {
		return len(trackedCopy(hRunner)) == 3
	}, 30*time.Second, 200*time.Millisecond, "instances should be tracked via lifecycle events")

	conn, err := c.Connection()
	require.NoError(t, err)

	// Every tracked instance should eventually be reported healthy - proof
	// the checkers spawned from those events are actually running.
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

	// Live-edit user.healthcheck.interval on one instance; the debounced
	// instance-updated pipeline should kill and replace its checker with
	// the new params.
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

// TestE2EIgnoredInstanceIsNeverTracked confirms user.healthcheck.ignore
// excludes an instance from both the initial resync and every subsequent
// lifecycle event.
func TestE2EIgnoredInstanceIsNeverTracked(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(context.Background())
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

		_, _, _ = runIncusCommand(ctx, t, projectName, "-f", compose, "down", "--project")
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
		ctx, client.ActionEnsure, os.Stdout, os.Stderr, client.OptionCreate(),
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

	// Give lifecycle events (instance-created/-started/-updated) a fair
	// chance to arrive, then confirm the ignored instance never gets tracked.
	time.Sleep(3 * time.Second)

	_, tracked := trackedCopy(hRunner)[inst.IncusName()]
	require.False(t, tracked, "ignored instance must never be tracked")
}

// TestE2ECrashedInstanceRestarts simulates a crash by stopping an instance
// directly through the Incus API, bypassing incus-compose's own stop (which
// sets user.healthcheck.stopped=true and is treated as intentional). The
// runner should notice the resulting instance-stopped event, evaluate
// restart policy via evaluateBackoff, and bring the instance back up and
// healthy on its own.
func TestE2ECrashedInstanceRestarts(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(context.Background())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

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

		_, _, _ = runIncusCommand(ctx, t, projectName, "-f", compose, "down", "--project")
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
		ctx, client.ActionEnsure, os.Stdout, os.Stderr, client.OptionCreate(),
	)
	require.NoError(t, err)

	err = stack.ForAction(client.ActionStart).Run(
		ctx, client.ActionStart, os.Stdout, os.Stderr, client.OptionExternalHealthd(),
	)
	require.NoError(t, err)

	// backend1 has no dependencies of its own (only nginx depends on it),
	// so it's healthy as soon as its own healthcheck passes.
	target := "backend1-1"

	conn, err := c.Connection()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		inst, _, err := conn.GetInstance(target)
		return err == nil && inst.Config[shared.HealthStatusKey] == shared.HealthStatusHealthy
	}, 30*time.Second, 500*time.Millisecond, "instance should become healthy before the crash")

	// Simulate a crash: stop the instance directly via the Incus API, not
	// through incus-compose, so it's never marked user.healthcheck.stopped.
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

// TestE2ERepeatedCrashesBackoff crashes the same instance three times in a
// row (same technique as TestE2ECrashedInstanceRestarts: a raw Incus stop,
// bypassing incus-compose) and checks the restart backoff actually doubles
// each time - not a fixed delay - by measuring wall-clock time between each
// crash and the resulting restart. Each next crash is issued immediately
// once the previous restart is observed, well inside the healthcheck
// interval, so the checker never gets a chance to report healthy and reset
// the backoff in between (see evaluateBackoff/handleCheckerStatus).
//
// Uses backend1's default interval(5s)*retries(1)=5s baseline as-is rather
// than editing it live: spawn() only computes restartDelay fresh for a
// brand-new trackedInstance, not on a respawn of an already-tracked one, so
// a live edit wouldn't actually change the backoff baseline used below.
func TestE2ERepeatedCrashesBackoff(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(context.Background())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

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

		_, _, _ = runIncusCommand(ctx, t, projectName, "-f", compose, "down", "--project")
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
		ctx, client.ActionEnsure, os.Stdout, os.Stderr, client.OptionCreate(),
	)
	require.NoError(t, err)

	err = stack.ForAction(client.ActionStart).Run(
		ctx, client.ActionStart, os.Stdout, os.Stderr, client.OptionExternalHealthd(),
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
