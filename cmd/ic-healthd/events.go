package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/shared"
)

// handleEvent dispatches one lifecycle event to the matching handler. The
// event payload carries only name/project/action; handlers do a targeted
// GetInstance(name) to read config, cheaper than a full list.
func (r *Runner) handleEvent(ctx context.Context, event incusApi.Event) {
	if event.Type != incusApi.EventTypeLifecycle {
		return
	}

	var lc incusApi.EventLifecycle
	if err := json.Unmarshal(event.Metadata, &lc); err != nil {
		slog.Debug("Decoding lifecycle event", "error", err)
		return
	}

	if lc.Name == "" {
		return
	}

	slog.Debug("New lifecycle event", "instance", lc.Name, "action", lc.Action)

	switch lc.Action {
	case incusApi.EventLifecycleInstanceCreated, incusApi.EventLifecycleInstanceStarted:
		r.handleStarted(ctx, lc.Name)
	case incusApi.EventLifecycleInstanceUpdated:
		r.handleUpdated(ctx, lc.Name)
	case incusApi.EventLifecycleInstanceDeleted:
		r.handleDeleted(ctx, lc.Name)
	case incusApi.EventLifecycleInstanceStopped, incusApi.EventLifecycleInstanceShutdown:
		r.handleStopped(ctx, lc.Name)
	}
}

// handleStarted handles instance-created (parse config, start a checker) and
// instance-started (no-op if already tracked, otherwise same as
// instance-created).
func (r *Runner) handleStarted(ctx context.Context, name string) {
	r.mu.Lock()
	_, tracked := r.tracked[name]
	r.mu.Unlock()
	if tracked {
		return
	}

	inst, _, err := r.conn.GetInstance(name)
	if err != nil {
		slog.Debug("Fetching instance for lifecycle event", "instance", name, "error", err)
		return
	}

	if isIgnored(inst.Config) || !hasHealthCheck(inst.Config) {
		return
	}

	cfg, err := parseInstance(inst.Config, inst.StatusCode == incusApi.Running)
	if err != nil {
		slog.Warn("Parsing instance config", "instance", name, "error", err)
		return
	}

	r.spawn(ctx, name, cfg, true, false)
}

// handleUpdated re-reads user.healthcheck.* and, if the checker's params
// changed, kills and replaces it (debounced). Self-caused updates (this
// instance's own status write) are suppressed without a GetInstance call.
func (r *Runner) handleUpdated(ctx context.Context, name string) {
	r.mu.Lock()
	if ti, ok := r.tracked[name]; ok && ti.selfWrites > 0 {
		ti.selfWrites--
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	inst, _, err := r.conn.GetInstance(name)
	if err != nil {
		slog.Debug("Fetching instance for update event", "instance", name, "error", err)
		return
	}

	r.mu.Lock()
	_, tracked := r.tracked[name]
	r.mu.Unlock()

	if isIgnored(inst.Config) || !hasHealthCheck(inst.Config) {
		if tracked {
			// No longer relevant: same debounced drop a real delete uses.
			r.handleDeleted(ctx, name)
		}
		return
	}

	cfg, err := parseInstance(inst.Config, inst.StatusCode == incusApi.Running)
	if err != nil {
		slog.Warn("Parsing instance config", "instance", name, "error", err)
		return
	}

	// When not running just update the tracked instance.
	if !cfg.Running {
		r.mu.Lock()
		ti, ok := r.tracked[name]
		if ok {
			ti.serverParams = cfg
		}
		r.mu.Unlock()

		return
	}

	r.mu.Lock()
	ti, ok := r.tracked[name]
	if !ok {
		r.mu.Unlock()
		// Wasn't tracked before (e.g. a healthcheck was just added via
		// `incus config set`); start fresh rather than debouncing nothing.
		r.spawn(ctx, name, cfg, true, false)
		return
	}

	ti.serverParams = cfg
	r.mu.Unlock()

	r.debounce(ctx, name, ti)
}

// handleDeleted marks the instance for removal through the same debounced
// pipeline as instance-updated - a pending delete supersedes any pending
// config update, then kills and drops the tracked instance.
func (r *Runner) handleDeleted(ctx context.Context, name string) {
	r.mu.Lock()

	ti, ok := r.tracked[name]
	if !ok {
		r.mu.Unlock()
		return
	}

	ti.pendingDelete = true
	r.mu.Unlock()

	r.debounce(ctx, name, ti)
}

// handleStopped evaluates restart policy now, via the same path a
// retries-exhausted checker uses.
func (r *Runner) handleStopped(ctx context.Context, name string) {
	r.mu.Lock()
	_, ok := r.tracked[name]
	r.mu.Unlock()
	if !ok {
		return
	}

	r.evaluateBackoff(ctx, name)
}

// debounce applies the debounce policy shared by instance-updated and
// instance-deleted: act immediately if we're outside the window and nothing
// is already pending, otherwise let the existing timer (or a freshly
// scheduled one) settle after the window - it always re-reads the current
// pending state when it fires, so a second event inside the window doesn't
// need to reschedule it. Must be called with r.mu held.
func (r *Runner) debounce(ctx context.Context, name string, ti *trackedInstance) {
	r.mu.Lock()

	now := time.Now()
	if ti.debounce == nil && now.Sub(ti.lastNotify) > debounceWindow {
		ti.lastNotify = now

		if ti.pendingDelete {
			ti.cancel()
			delete(r.tracked, name)

			r.mu.Unlock()
			return
		}

		if ti.serverParams.equal(ti.knownParams) {
			r.mu.Unlock()
			return
		}

		ti.cancel()
		cfg := ti.serverParams
		r.mu.Unlock()

		r.spawn(ctx, name, cfg, false, false)
		return
	}

	ti.debounce = time.AfterFunc(debounceWindow, func() {
		r.mu.Lock()

		cur, ok := r.tracked[name]
		if !ok {
			r.mu.Unlock()
			return
		}
		cur.debounce = nil
		cur.lastNotify = time.Now()

		if cur.pendingDelete {
			cur.cancel()
			delete(r.tracked, name)

			r.mu.Unlock()
			return
		}

		if cur.serverParams.equal(cur.knownParams) {
			r.mu.Unlock()
			return
		}

		cur.cancel()
		cfg := cur.serverParams
		r.mu.Unlock()

		r.spawn(ctx, name, cfg, false, false)
	})

	r.mu.Unlock()
}

// watch owns the receiving end of one checker generation's statusCh/exitCh,
// on its own goroutine so a slow-to-exit checker never stalls the event
// dispatch loop or another instance's watcher.
func (r *Runner) watch(name string, statusCh <-chan string, exitCh <-chan error) {
	for {
		select {
		case status := <-statusCh:
			r.handleCheckerStatus(name, status)
		case err := <-exitCh:
			r.handleCheckerExit(name, err)
			return
		}
	}
}

// handleCheckerStatus records a self-caused write (suppressing the resulting
// instance-updated event once) and, on a healthy status, resets restart
// backoff back to base - no separate signal needed, it rides the same
// channel that already exists for the self-write suppression.
func (r *Runner) handleCheckerStatus(name, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ti, ok := r.tracked[name]
	if !ok {
		return
	}

	ti.selfWrites++
	if status == shared.HealthStatusHealthy {
		ti.restartDelay = baseRestartDelay(ti.knownParams)
	}
}

// handleCheckerExit reacts to a checker exiting on its own after exhausting
// retries (see Backoff). A plain nil exit (the runner canceled it - a param
// change, delete, or instance-stopped/-shutdown) needs no action here: that
// decision and its consequence (drop or respawn) already happened
// synchronously at cancellation time, in settleLocked/evaluateBackoff.
func (r *Runner) handleCheckerExit(name string, err error) {
	if !errors.Is(err, ErrRetriesExhausted) {
		return
	}

	r.mu.Lock()
	_, ok := r.tracked[name]
	r.mu.Unlock()
	if !ok {
		return
	}

	r.evaluateBackoff(context.Background(), name)
}

// evaluateBackoff runs the Backoff decision for name after its checker
// exhausted retries or the instance stopped/shut down: read
// serverParams.Restart (the freshest observed value, not knownParams which
// only updates when the checker itself gets respawned) and either respawn
// after a delay (doubling for next time) or drop tracking entirely.
func (r *Runner) evaluateBackoff(ctx context.Context, name string) {
	r.mu.Lock()
	ti, ok := r.tracked[name]
	if !ok {
		r.mu.Unlock()
		return
	}
	restart := ti.serverParams.Restart
	cfg := ti.serverParams
	delay := ti.restartDelay
	r.mu.Unlock()

	respawn := restart != "" && restart != "no"
	if respawn && restart == "unless-stopped" && r.isMarkedStopped(name) {
		respawn = false
	}

	r.mu.Lock()
	ti, ok = r.tracked[name]
	if !ok {
		r.mu.Unlock()
		return
	}
	if !respawn {
		ti.cancel()
		delete(r.tracked, name)
		r.mu.Unlock()
		return
	}
	ti.cancel()
	ti.restartDelay = min(delay*2, maxRestartDelay)
	r.mu.Unlock()

	time.AfterFunc(delay, func() {
		r.spawn(ctx, name, cfg, true, true)
	})
}

// isMarkedStopped reports whether the instance carries
// user.healthcheck.stopped=true, meaning it was intentionally stopped.
// Returns true on API error (instance gone counts as stopped).
func (r *Runner) isMarkedStopped(name string) bool {
	inst, _, err := r.conn.GetInstance(name)
	if err != nil {
		return true
	}
	return inst.Config[shared.HealthStoppedKey] == "true"
}

// restartInstance brings the instance back to Running. If it's already
// stopped we only start; otherwise we stop (force) and start. We avoid the
// "restart" action because it errors on a stopped instance.
func (r *Runner) restartInstance(name string) error {
	conn := r.conn

	state, _, err := conn.GetInstanceState(name)
	if err != nil {
		return err
	}

	if state.StatusCode != incusApi.Stopped {
		stopReq := incusApi.InstanceStatePut{
			Action:  "stop",
			Timeout: -1,
			Force:   true,
		}

		op, err := conn.UpdateInstanceState(name, stopReq, "")
		if err != nil {
			return err
		}

		if err := op.Wait(); err != nil {
			return err
		}
	}

	startReq := incusApi.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}

	op, err := conn.UpdateInstanceState(name, startReq, "")
	if err != nil {
		return err
	}

	return op.Wait()
}

// spawn (re)starts a checker for name with cfg under ctx, launching a
// dedicated goroutine to receive its statusCh/exitCh (see watch). When the
// instance is already tracked this reuses its *trackedInstance so in-flight
// events for name always find an entry; restartInstance restarts the
// instance first (used for backoff/restart-policy respawns).
func (r *Runner) spawn(ctx context.Context, name string, cfg instanceConfig, inStart, restartInstance bool) {
	if restartInstance {
		if err := r.restartInstance(name); err != nil {
			slog.Warn("restarting instance before respawn", "instance", name, "error", err)
		}
	}

	checkerCtx, cancel := context.WithCancel(ctx)
	statusCh := make(chan string, 4)
	exitCh := make(chan error, 1)

	r.mu.Lock()
	ti, existed := r.tracked[name]
	if !existed {
		ti = &trackedInstance{restartDelay: baseRestartDelay(cfg)}
		r.tracked[name] = ti
	}
	ti.cancel = cancel
	ti.knownParams = cfg
	ti.serverParams = cfg
	ti.pendingDelete = false
	ti.selfWrites = 0
	r.mu.Unlock()

	slog.Info("Starting instance checker", "instance", name, "config", cfg)

	go newChecker(r.conn, name, cfg, statusCh, exitCh).run(checkerCtx, inStart)
	go r.watch(name, statusCh, exitCh)
}

// resync runs a full discover() and reconciles it against what's currently
// tracked: start checkers for newly-discovered instances, kill checkers for
// instances no longer there, leave everything else running untouched. Used
// on (re)connect and by the manual reload trigger.
func (r *Runner) resync(ctx context.Context) error {
	discovered, err := discover(r.conn)

	r.mu.Lock()
	var toKill []string
	for name := range r.tracked {
		if _, ok := discovered[name]; !ok {
			toKill = append(toKill, name)
		}
	}
	var toStart []string
	for name := range discovered {
		if _, ok := r.tracked[name]; !ok {
			toStart = append(toStart, name)
		}
	}
	for _, name := range toKill {
		ti := r.tracked[name]
		delete(r.tracked, name)
		ti.cancel()
	}
	r.mu.Unlock()

	for _, name := range toStart {
		r.spawn(ctx, name, discovered[name], true, false)
	}

	return err
}
