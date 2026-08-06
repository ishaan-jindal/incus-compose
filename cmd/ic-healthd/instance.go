package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v5"
	incus "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/panjf2000/ants/v2"

	"github.com/lxc/incus-compose/shared"
)

func patchInstanceConfig(ctx context.Context, conn incus.InstanceServer, instance string, config map[string]string) error {
	log := logger(ctx)

	// Bounded because this runs on the Run loop.
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	info, err := conn.GetConnectionInfo()
	if err != nil {
		return err
	}

	log.Log(ctx, levelTrace, "Updating an instances config", "project", info.Project, "instance", instance, "patch", config)

	path := incusApi.NewURL().
		Path("1.0", "instances", instance).
		Project(info.Project).
		Target(info.Target).
		String()

	return retryBusy(ctx, func() error {
		_, _, err := withContext(ctx, func() (struct{}, string, error) {
			_, _, patchErr := conn.RawQuery("PATCH", path, struct {
				Config map[string]string `json:"config"`
			}{Config: config}, "")

			return struct{}{}, "", patchErr
		})

		return err
	})
}

// pools cap the Incus actions in flight, over every watched project.
type pools struct {
	check   *ants.Pool
	restart *ants.Pool
}

// newPools builds the two action pools. They refuse rather than queue: waiting
// for a worker burns the deadline the watchdog reaps the action by.
func newPools(workers, restartWorkers int) (*pools, error) {
	check, err := ants.NewPool(max(workers, 1), ants.WithNonblocking(true))
	if err != nil {
		return nil, fmt.Errorf("creating the check pool: %w", err)
	}

	restart, err := ants.NewPool(max(restartWorkers, 1), ants.WithNonblocking(true))
	if err != nil {
		check.Release()

		return nil, fmt.Errorf("creating the restart pool: %w", err)
	}

	return &pools{check: check, restart: restart}, nil
}

func releasePools(p *pools) {
	p.check.Release()
	p.restart.Release()
}

// retryBusy runs write again while Incus rejects it for the instance's
// operation lock. The lock is taken by the driver, so a caller that creates an
// operation must do its wait inside write.
func retryBusy(ctx context.Context, write func() error) error {
	return retry.New(
		retry.Context(ctx),
		retry.Attempts(6),
		retry.Delay(250*time.Millisecond),
		retry.LastErrorOnly(true),
		retry.RetryIf(func(err error) bool {
			return strings.Contains(err.Error(), "Instance is busy")
		}),
	).Do(write)
}

// writeInstanceStatus persists status into the daemon's own instance, when it knows its identity.
func writeInstanceStatus(ctx context.Context, conn incus.InstanceServer, instance string, status string) error {
	return patchInstanceConfig(ctx, conn, instance, map[string]string{shared.HealthStatusKey: status})
}

type instanceConfig struct {
	test          []string
	startPeriod   time.Duration
	startInterval time.Duration
	interval      time.Duration
	timeout       time.Duration
	retries       int

	restart string

	running bool
}

func (ic *instanceConfig) equals(other *instanceConfig) bool {
	return slices.Equal(ic.test, other.test) &&
		ic.startPeriod == other.startPeriod &&
		ic.startInterval == other.startInterval &&
		ic.interval == other.interval &&
		ic.timeout == other.timeout &&
		ic.retries == other.retries &&
		ic.restart == other.restart &&
		ic.running == other.running
}

type instanceResult struct {
	kind instanceResultKind
	name string

	// ctx identifies the action that ran: an abandoned one can still deliver,
	// and the loop compares this against the context it holds. Unset by
	// instanceResultDiscovered, which no instance state produced.
	ctx context.Context

	config *instanceConfig

	// status is the value that was written (instanceResultStatus) or the value
	// found on the instance (instanceResultDiscovered).
	status string

	// names is every instance the pass saw, set by instanceResultRoster only.
	names []string

	err error
}

type instance struct {
	name   string
	config *instanceConfig

	state instanceState

	due    time.Time
	action instanceAction

	// actionContext is created fresh per action and cleared the moment one
	// ends, so its identity says which action a result came from.
	actionDeadline time.Time
	actionContext  context.Context
	actionCancel   context.CancelFunc

	inRestart   bool
	restartDone time.Time

	// failures counts consecutive failed checks since the last success.
	// Failures inside the start period do not count, as in docker.
	failures int

	// status is the last value known to be on the instance, so a write only
	// happens on a transition. Discovery refreshes it.
	status string

	// restartDelay doubles per restart; a healthy check rebases it.
	restartDelay time.Duration
}

// instanceStarted puts an instance in the shape a fresh start leaves it in: due
// for a check at once, its failure run cleared and its start period re-armed.
func instanceStarted(inst *instance, now time.Time) {
	inst.state = instanceIdle
	inst.action = instanceActionCheck
	inst.failures = 0
	inst.due = now

	// As in docker, a restarted instance gets its start period back.
	inst.inRestart = inst.config.startPeriod > 0
	if inst.inRestart {
		inst.restartDone = now.Add(inst.config.startPeriod)
	}
}

// baseRestartDelay is interval*retries, clamped to [defaultRestartDelay, maxRestartDelay].
func baseRestartDelay(cfg *instanceConfig) time.Duration {
	if cfg.interval <= 0 || cfg.retries <= 0 {
		return defaultRestartDelay
	}

	return max(min(cfg.interval*time.Duration(cfg.retries), maxRestartDelay), defaultRestartDelay)
}

func getInstance(ctx context.Context, conn incus.InstanceServer, name string) (*incusApi.Instance, string, error) {
	return withContext(ctx, func() (*incusApi.Instance, string, error) {
		i, etag, err := conn.GetInstance(name)
		return i, etag, err
	})
}

func parseInstanceConfig(inst *incusApi.Instance) (*instanceConfig, error) {
	if inst.Config[shared.HealthIgnoreKey] == "true" {
		return nil, ErrInstanceIgnored
	}

	wantsChecking := inst.Config[shared.HealthKeyPrefix+"test"] != "" ||
		slices.Contains(shared.RestartPolicies, inst.Config[shared.HealthKeyPrefix+"restart"])

	// Watching is opt-in. One that looks like it wants checking but never
	// opted in is reported rather than assumed.
	if inst.Config[shared.HealthEnabledKey] != "true" {
		if wantsChecking {
			return nil, ErrInstanceNotEnabled
		}

		return nil, ErrInstanceNoHealthcheck
	}

	if !wantsChecking {
		return nil, ErrInstanceNoHealthcheck
	}

	cfg := instanceConfig{
		startPeriod:   defaultRestartPeriod,
		startInterval: defaultRestartInterval,
		interval:      defaultInterval,
		timeout:       defaultTimeout,
		retries:       defaultRetries,
		restart:       inst.Config[shared.HealthKeyPrefix+"restart"],
		running:       inst.StatusCode == incusApi.Running,
	}

	testRaw := inst.Config[shared.HealthKeyPrefix+"test"]
	if testRaw == "" && slices.Contains(shared.RestartPolicies, cfg.restart) {
		// Restart policy without a test: probe with a no-op so run state is watched.
		testRaw = `["NONE"]`
	}

	if testRaw != "" {
		if err := json.Unmarshal([]byte(testRaw), &cfg.test); err != nil {
			return nil, fmt.Errorf("parsing test: %w", err)
		}
	}

	if len(cfg.test) > 0 && cfg.test[0] == "CMD-SHELL" && len(cfg.test) < 2 {
		return nil, errors.New("CMD-SHELL requires a command")
	}

	if v := inst.Config[shared.HealthKeyPrefix+"start_period"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("parsing start_period: %w", err)
		}
		cfg.startPeriod = d
	}

	if v := inst.Config[shared.HealthKeyPrefix+"start_interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("parsing start_interval: %w", err)
		}
		if d <= 0 {
			return nil, errors.New("start_interval must be greater than 0")
		}
		cfg.startInterval = d
	}

	if v := inst.Config[shared.HealthKeyPrefix+"interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("parsing interval: %w", err)
		}
		if d <= 0 {
			return nil, errors.New("interval must be greater than 0")
		}
		cfg.interval = d
	}

	if v := inst.Config[shared.HealthKeyPrefix+"timeout"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("parsing timeout: %w", err)
		}
		if d <= 0 {
			return nil, errors.New("timeout must be greater than 0")
		}
		cfg.timeout = d
	}

	if v := inst.Config[shared.HealthKeyPrefix+"retries"]; v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("parsing retries: %w", err)
		}
		if n == 0 {
			return nil, errors.New("retries must be greater than 0")
		}
		cfg.retries = int(n)
	}

	return &cfg, nil
}

// discoverOne re-reads one instance and reports it on its own goroutine, so the
// loop never blocks on the API call.
func discoverOne(ctx context.Context, conn incus.InstanceServer, results chan instanceResult, name string) {
	go func() {
		res := instanceResult{kind: instanceResultDiscovered, name: name}

		inst, _, err := getInstance(ctx, conn, name)
		if err != nil {
			res.err = err
		} else {
			res.status = inst.Config[shared.HealthStatusKey]
			res.config, res.err = parseInstanceConfig(inst)
		}

		select {
		case results <- res:
		case <-ctx.Done():
		}
	}()
}

// handleInstanceEvent does what the name says, all operations MUST be non-blocking.
// goroutines are not allowed to modify instances or its values.
func handleInstanceEvent(ctx context.Context, conn incus.InstanceServer, instances map[string]*instance, results chan instanceResult, ev instanceEvent) {
	log := logger(ctx)

	switch ev.Action {
	case instanceRestarted:
		inst, ok := instances[ev.Instance]
		if !ok {
			discoverOne(ctx, conn, results, ev.Instance)

			return
		}

		// An action in flight owns the instance; its result says what comes next.
		if inst.state == instanceChecking || inst.state == instanceRestarting {
			return
		}

		// It is running again, whoever did it, so a queued restart is moot.
		instanceStarted(inst, time.Now())
	case instanceStopped:
		inst, ok := instances[ev.Instance]
		if !ok {
			return
		}

		// Before the branches below, one of which stops watching it entirely.
		reportStatus(ctx, conn, results, inst, shared.HealthStatusStopped)

		// Before anything else, so a policy dropped since discovery still drops
		// the instance rather than scheduling a restart for it.
		if !slices.Contains(shared.RestartPolicies, inst.config.restart) {
			if inst.actionCancel != nil {
				inst.actionCancel()
			}

			delete(instances, ev.Instance)

			return
		}

		// Already waiting on a restart: leave its backoff alone.
		if inst.action == instanceActionRestart {
			return
		}

		// A stop nobody asked for, so widen the window: a crash loop backs off.
		inst.action = instanceActionRestart
		inst.due = time.Now().Add(inst.restartDelay)
		inst.restartDelay = min(inst.restartDelay*2, maxRestartDelay)

	case instanceUpdated:
		discoverOne(ctx, conn, results, ev.Instance)
	case instanceDeleted:
		inst, ok := instances[ev.Instance]
		if ok && inst.actionCancel != nil {
			inst.actionCancel()
		}

		delete(instances, ev.Instance)
	default:
		log.Error("Unknown instance event action", "action", ev.Action)
	}
}

// instanceRestartAction starts an instance unless it was stopped on purpose, and reports the outcome.
func instanceRestartAction(ctx context.Context, conn incus.InstanceServer, name string) instanceResult {
	res := instanceResult{kind: instanceResultRestarted, name: name}

	inst, _, err := getInstance(ctx, conn, name)
	switch {
	case err != nil:
		res.err = err
		return res
	case inst.Config[shared.HealthStoppedKey] == "true":
		res.err = ErrIntentionallyStopped
		return res
	default:
		state, _, err := conn.GetInstanceState(name)
		if err != nil {
			res.err = err
			return res
		}

		if state.StatusCode != incusApi.Stopped {
			stopReq := incusApi.InstanceStatePut{
				Action:  "stop",
				Timeout: -1,
				Force:   true,
			}

			err := retryBusy(ctx, func() error {
				op, err := conn.UpdateInstanceState(name, stopReq, "")
				if err != nil {
					return err
				}

				return op.WaitContext(ctx)
			})
			if err != nil {
				res.err = err
				return res
			}
		}
	}

	startReq := incusApi.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}

	res.err = retryBusy(ctx, func() error {
		op, err := conn.UpdateInstanceState(name, startReq, "")
		if err != nil {
			return err
		}

		return op.WaitContext(ctx)
	})

	return res
}

func instanceExec(ctx context.Context, conn incus.InstanceServer, name string, cmd []string) (int, string, string, error) {
	log := logger(ctx)

	req := incusApi.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: false,
	}

	var stdout, stderr bytes.Buffer
	args := incus.InstanceExecArgs{
		Stdin:    nil,
		Stdout:   &stdout,
		Stderr:   &stderr,
		DataDone: make(chan bool),
	}

	// A timed-out call leaves any operation it creates for the server to reap.
	op, _, err := withContext(ctx, func() (incus.Operation, string, error) {
		op, err := conn.ExecInstance(name, req, &args)
		return op, "", err
	})
	if err != nil {
		return -1, "", "", err
	}

	select {
	case <-args.DataDone:
	case <-ctx.Done():
		// ctx is already done, so give Cancel its own budget to get out.
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), apiTimeout)
		defer cancel()

		_, _, cancelErr := withContext(cancelCtx, func() (struct{}, string, error) {
			return struct{}{}, "", op.Cancel()
		})
		if cancelErr != nil {
			log.Debug("Canceling exec operation", "instance", name, "error", cancelErr)
		}

		return -1, "", "", ctx.Err()
	}

	// WaitContext, not Wait: an operation whose completion never arrives would
	// block this checker for good, and it is the only caller on its goroutine.
	err = op.WaitContext(ctx)
	if err != nil {
		return -1, stdout.String(), stderr.String(), err
	}

	opAPI := op.Get()
	if exitCode, ok := opAPI.Metadata["return"].(float64); ok {
		return int(exitCode), stdout.String(), stderr.String(), nil
	}

	return -1, "", "", nil
}

func instanceCheckAction(ctx context.Context, conn incus.InstanceServer, name string, cfg *instanceConfig) instanceResult {
	log := logger(ctx)

	res := instanceResult{kind: instanceResultChecked, name: name}

	inst, _, err := withContext(ctx, func() (*incusApi.InstanceState, string, error) {
		return conn.GetInstanceState(name)
	})
	if err != nil {
		log.Debug("Fetching instance status error", "instance", name, "error", err)

		res.err = err
		return res
	}

	if inst.StatusCode != incusApi.Running {
		log.Log(ctx, levelTrace, "Instance is not running", "instance", name, "status", inst.Status)

		res.err = ErrNotRunning
		return res
	}

	if len(cfg.test) == 0 {
		res.err = errors.New("trying to run a check on a instance without a test")
		return res
	}

	var cmd []string
	switch cfg.test[0] {
	case "CMD":
		cmd = cfg.test[1:]
	case "CMD-SHELL":
		cmd = []string{"/bin/sh", "-c", cfg.test[1]}
	case "NONE":
		return res
	default:
		// Assume it's a direct command
		cmd = cfg.test
	}

	exitCode, stdout, stderr, err := instanceExec(ctx, conn, name, cmd)
	if err != nil {
		log.Debug("exec error", "error", err, "stdout", stdout, "stderr", stderr)
		res.err = err
		return res
	}

	if exitCode != 0 {
		res.err = fmt.Errorf("cmd failed, exit code: %d", exitCode)
	}

	return res
}

// instanceStatusAction writes user.healthcheck.status, the only key the daemon
// owns on a watched instance.
func instanceStatusAction(ctx context.Context, conn incus.InstanceServer, name, status string) instanceResult {
	res := instanceResult{kind: instanceResultStatus, name: name, status: status}
	res.err = patchInstanceConfig(ctx, conn, name, map[string]string{shared.HealthStatusKey: status})

	return res
}

// reportStatus writes status when it differs from what the instance last had,
// so a write only happens on a transition.
func reportStatus(ctx context.Context, conn incus.InstanceServer, results chan<- instanceResult, inst *instance, status string) {
	if status == inst.status {
		return
	}

	// Recorded before the write lands, so two writes in flight cannot invert.
	inst.status = status

	go func(name string) {
		res := instanceStatusAction(ctx, conn, name, status)

		select {
		case <-ctx.Done():
		case results <- res:
		}
	}(inst.name)
}

// checkFailed applies one failed check and returns the status to report. The
// watchdog goes through here too: as in docker, a timed-out probe is a failure.
func checkFailed(inst *instance, now time.Time) string {
	if inst.inRestart {
		// As in docker, failures inside the start period are not counted.
		return shared.HealthStatusStarting
	}

	inst.failures++

	if inst.failures < inst.config.retries {
		return inst.status
	}

	// Retries exhausted: hand it to the restart policy and count afresh.
	if slices.Contains(shared.RestartPolicies, inst.config.restart) {
		inst.failures = 0
		inst.action = instanceActionRestart
		inst.due = now.Add(inst.restartDelay)
		inst.restartDelay = min(inst.restartDelay*2, maxRestartDelay)
	}

	return shared.HealthStatusUnhealthy
}

func runInstanceActions(ctx context.Context, conn incus.InstanceServer, pool *pools, instances map[string]*instance, results chan<- instanceResult) time.Time {
	log := logger(ctx)

	var earliest time.Time
	now := time.Now()

	keep := func(due time.Time) {
		if earliest.IsZero() || due.Before(earliest) {
			earliest = due
		}
	}

	// defer reschedules an action no worker was free for.
	deferAction := func(inst *instance) {
		log.Debug("Deferring an action, every worker is busy", "instance", inst.name, "action", inst.action)

		inst.due = now.Add(poolRetryDelay + rand.N(poolRetryDelay)) // nolint:gosec
		keep(inst.due)
	}

	for _, inst := range instances {
		if inst.state != instanceIdle {
			if !inst.actionDeadline.IsZero() && now.After(inst.actionDeadline) {
				log.Warn("Action deadline exceeded", "instance", inst.name, "state", inst.state)
				// Clearing the context marks what the abandoned action reports as stale.
				inst.actionCancel()

				checking := inst.state == instanceChecking

				inst.state = instanceIdle
				inst.actionContext = nil
				inst.actionCancel = nil
				inst.actionDeadline = time.Time{}

				if checking {
					// As in docker, a probe that exceeds its timeout is a
					// failed probe.
					reportStatus(ctx, conn, results, inst, checkFailed(inst, now))

					// checkFailed sets its own deadline when it escalates.
					if inst.action != instanceActionRestart {
						inst.due = now.Add(inst.config.interval)
					}
				} else {
					// A restart that timed out is a restart that failed.
					inst.due = now.Add(inst.restartDelay)
					inst.restartDelay = min(inst.restartDelay*2, maxRestartDelay)
				}
			}

			continue
		}

		if now.Before(inst.due) {
			keep(inst.due)

			continue
		}

		switch inst.action {
		case instanceActionRestart:
			actionCtx, cancel := context.WithCancel(ctx)
			name := inst.name

			err := pool.restart.Submit(func() {
				res := instanceRestartAction(actionCtx, conn, name)
				res.ctx = actionCtx

				select {
				case <-actionCtx.Done():
				case results <- res:
				}
			})
			if err != nil {
				cancel()
				deferAction(inst)

				continue
			}

			inst.state = instanceRestarting
			inst.actionDeadline = now.Add(restartTimeout)
			inst.actionContext, inst.actionCancel = actionCtx, cancel
		case instanceActionCheck:
			// No need to check an instance without a test.
			if len(inst.config.test) == 0 {
				continue
			}

			actionCtx, cancel := context.WithCancel(ctx)
			name, cfg := inst.name, inst.config

			err := pool.check.Submit(func() {
				res := instanceCheckAction(actionCtx, conn, name, cfg)
				res.ctx = actionCtx

				select {
				case <-actionCtx.Done():
				case results <- res:
				}
			})
			if err != nil {
				cancel()
				deferAction(inst)

				continue
			}

			// Committed only once the action is running: a refused one keeps
			// its idle state, and the watchdog never sees a deadline it never had.
			inst.state = instanceChecking
			inst.actionDeadline = now.Add(inst.config.timeout)
			inst.actionContext, inst.actionCancel = actionCtx, cancel
		}
	}

	return earliest
}

// handleInstanceResult applies one action's outcome. Like handleInstanceEvent it
// must not block: the status write it may start runs on its own goroutine.
func handleInstanceResult(ctx context.Context, conn incus.InstanceServer, instances map[string]*instance, results chan<- instanceResult, res instanceResult) {
	log := logger(ctx)

	switch res.kind {
	case instanceResultDiscovered:
		if res.err != nil {
			switch {
			case errors.Is(res.err, ErrInstanceNotEnabled):
				// The one case worth saying out loud: it asked to be
				// checked and will not be.
				log.Warn("Not watching an instance that has a healthcheck but is not enabled",
					"instance", res.name, "key", shared.HealthEnabledKey)
			case errors.Is(res.err, ErrInstanceIgnored),
				errors.Is(res.err, ErrInstanceNoHealthcheck):
				// Nothing to say: these are the normal reasons to skip.
			default:
				log.Error("Discover failed", "instance", res.name, "error", res.err)
			}

			return
		}

		if res.config == nil {
			log.Error("No error but config nil", "instance", res.name)
			return
		}

		inst, ok := instances[res.name]
		if ok {
			inst.config = res.config

			// What Incus reports is what is on the instance.
			inst.status = res.status
		} else {
			inst = &instance{
				name:         res.name,
				config:       res.config,
				state:        instanceIdle,
				action:       instanceActionCheck,
				due:          time.Now(),
				status:       res.status,
				restartDelay: baseRestartDelay(res.config),
			}

			if inst.config.startPeriod > 0 {
				inst.inRestart = true
				inst.restartDone = time.Now().Add(inst.config.startPeriod)
			}

			instances[res.name] = inst
		}

		// A stopped instance has no verdict to earn until it runs again.
		if !inst.config.running {
			reportStatus(ctx, conn, results, inst, shared.HealthStatusStopped)
		}
	case instanceResultChecked:
		inst, ok := instances[res.name]
		if !ok {
			log.Error("Got check result for an unknown instance", "instance", res.name, "result", res.err)
			return
		}

		if res.ctx != inst.actionContext {
			log.Debug("Dropping a stale check result", "instance", res.name)
			return
		}

		inst.state = instanceIdle

		// Releasing the context is what unhooks it from evCtx.
		inst.actionCancel()
		inst.actionCancel = nil
		inst.actionContext = nil
		inst.actionDeadline = time.Time{}

		now := time.Now()
		if inst.inRestart && !now.Before(inst.restartDone) {
			inst.inRestart = false
		}

		if res.config != nil && !inst.config.equals(res.config) {
			inst.config = res.config
		}

		// The instance stopped, which is a lifecycle fact, not a health
		// verdict: the stop event owns bringing it back.
		if errors.Is(res.err, ErrNotRunning) {
			reportStatus(ctx, conn, results, inst, shared.HealthStatusStopped)

			inst.due = now.Add(inst.config.interval)
			return
		}

		var want string

		if res.err == nil {
			inst.failures = 0
			want = shared.HealthStatusHealthy

			// Staying up is what earns a fresh restart budget.
			inst.restartDelay = baseRestartDelay(inst.config)

			// As in docker, the first success ends the start period early.
			inst.inRestart = false
		} else {
			log.Debug("Check failed", "instance", inst.name, "inStart", inst.inRestart,
				"failures", inst.failures, "retries", inst.config.retries, "error", res.err)

			want = checkFailed(inst, now)
		}

		reportStatus(ctx, conn, results, inst, want)

		// A stop in flight or an escalation already set the deadline it wants.
		if inst.action == instanceActionRestart {
			return
		}

		interval := inst.config.interval
		if inst.inRestart {
			interval = inst.config.startInterval
		}

		inst.due = now.Add(interval + rand.N(interval/4)) // nolint:gosec
	case instanceResultRestarted:
		inst, ok := instances[res.name]
		if !ok {
			log.Error("Got start result for an unknown instance", "instance", res.name, "result", res.err)
			return
		}

		if res.ctx != inst.actionContext {
			log.Debug("Dropping a stale restart result", "instance", res.name)
			return
		}

		inst.actionCancel()
		inst.actionCancel = nil
		inst.actionContext = nil
		inst.actionDeadline = time.Time{}

		if res.err != nil {
			if errors.Is(res.err, ErrIntentionallyStopped) {
				// The user stopped it; only a start event brings it back.
				inst.state = instanceParked
				return
			}

			log.Error("Restart failed", "instance", res.name, "error", res.err)

			inst.state = instanceIdle
			inst.action = instanceActionRestart
			inst.due = time.Now().Add(inst.restartDelay)
			inst.restartDelay = min(inst.restartDelay*2, maxRestartDelay)

			return
		}

		instanceStarted(inst, time.Now())
	case instanceResultRoster:
		if res.err != nil {
			log.Error("Discovering the project failed", "error", res.err)
			return
		}

		// Anything the pass did not see is gone from the project.
		for name, inst := range instances {
			if slices.Contains(res.names, name) {
				continue
			}

			log.Debug("Dropping an instance the project no longer has", "instance", name)

			if inst.actionCancel != nil {
				inst.actionCancel()
			}

			delete(instances, name)
		}
	case instanceResultStatus:
		inst, ok := instances[res.name]
		if !ok {
			return
		}

		if res.err != nil {
			log.Warn("Writing the health status failed",
				"instance", res.name, "status", res.status, "error", res.err)

			// Nothing landed, so forget what reportStatus recorded.
			if inst.status == res.status {
				inst.status = ""
			}

			return
		}

		// Only transitions get here, so this stays quiet on a healthy fleet.
		log.Info("Health status", "instance", res.name, "status", res.status)
	}
}
