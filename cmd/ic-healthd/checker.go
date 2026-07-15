package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	incus "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/shared"
)

// checker runs the health probe for a single instance, independent of the
// runner except for statusCh/exitCh. It never restarts the instance it
// watches or decides whether to - it only probes, reports its own status,
// and exits once its retries are exhausted or its context is canceled. The
// runner decides what happens next.
type checker struct {
	conn   incus.InstanceServer
	name   string
	params instanceConfig

	failures int    // local to this run; never carried across a respawn
	status   string // last status written, to skip redundant writes

	statusCh chan<- string // every user.healthcheck.status write, mirrored here
	exitCh   chan<- error  // exactly one send, then run has returned for good
}

// newChecker starts a checker for name under ctx. inStart selects the
// start-period checker; the checker is never told to restart anything - when
// the runner wants the instance restarted first, it does so itself before
// calling newChecker for the replacement.
func newChecker(
	conn incus.InstanceServer,
	name string,
	cfg instanceConfig,
	statusCh chan<- string, exitCh chan<- error,
) *checker {
	return &checker{
		conn:     conn,
		name:     name,
		params:   cfg,
		statusCh: statusCh,
		exitCh:   exitCh,
	}
}

// phaseResult tells run what to do after a checking phase ends.
type phaseResult int

const (
	phaseStop   phaseResult = iota // retries exhausted: stop and report
	phaseNormal                    // continue with the normal-interval checker
)

// run drives the health check loop until ctx is canceled or retries are
// exhausted. It alternates between the start-period checker (start interval,
// bounded by the start period) and the normal checker. It keeps the outer
// checkInstanceRunningDelay poll as a safety net for the gap between an
// instance-stopped/-shutdown event firing and the runner's kill landing.
func (c *checker) run(ctx context.Context, inStart bool) {
	for {
		if c.params.StartPeriod < 1 {
			// Disable inStart if the period is smaller 1
			inStart = false
		}

		result := c.runPhase(ctx, inStart)
		if ctx.Err() != nil {
			c.exitCh <- nil
			return
		}

		switch result {
		case phaseStop:
			c.exitCh <- ErrRetriesExhausted.WithFailures(uint64(c.failures))
			return
		case phaseNormal:
			inStart = false
		}
	}
}

// runPhase runs a single checking phase until a transition is required. When
// inStart is true it uses the start interval and is bounded by the start
// period; otherwise it uses the normal interval and runs until ctx is
// canceled or retries are exhausted. The returned phaseResult tells run how
// to proceed; the caller checks ctx.Err() first since phaseCtx.Done() also
// unblocks this when the parent ctx is canceled.
func (c *checker) runPhase(ctx context.Context, inStart bool) phaseResult {
	interval := c.params.Interval
	retries := c.params.Retries
	phaseCtx, cancel := context.WithCancel(ctx)
	if inStart {
		interval = c.params.StartInterval
		phaseCtx, cancel = context.WithTimeout(ctx, c.params.StartPeriod)
	}
	defer cancel()

	if inStart && c.check(phaseCtx) == nil {
		// First success during the start period: switch to the normal checker.
		c.failures = 0

		if err := c.writeStatus(shared.HealthStatusHealthy); err != nil {
			slog.Debug("updating healthcheck status", "instance", c.name, "error", err)
		}

		return phaseNormal
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var status string
			var result phaseResult
			done := false

			err := c.check(phaseCtx)
			if err == nil {
				c.failures = 0
				status = shared.HealthStatusHealthy

				if inStart {
					// First success during the start period: switch to the normal checker.
					result, done = phaseNormal, true
				}
			} else {
				c.failures++
				slog.Debug("check failed",
					"instance", c.name,
					"failures", c.failures,
					"retries", retries,
					"inStart", inStart,
					"error", err,
				)
				status = shared.HealthStatusUnhealthy

				if c.failures >= retries {
					result, done = phaseStop, true
				}
			}

			if err := c.writeStatus(status); err != nil {
				slog.Debug("updating healthcheck status", "instance", c.name, "error", err)
			}

			if done {
				return result
			}
		case <-phaseCtx.Done():
			if inStart {
				slog.Debug("checker phase (start -> normal)", "instance", c.name)
				// Start period elapsed: switch to the normal checker.
				return phaseNormal
			}

			return phaseStop
		}
	}
}

// check executes the healthcheck command and returns true if healthy.
func (c *checker) check(ctx context.Context) error {
	inst, _, err := c.conn.GetInstanceState(c.name)
	if err != nil {
		slog.Debug("fetching instance status error", "instance", c.name, "error", err)
		return err
	}

	if inst.StatusCode != incusApi.Running {
		return errors.New("not running")
	}

	// Build command based on test format
	if len(c.params.Test) == 0 {
		return nil
	}

	var cmd []string
	switch c.params.Test[0] {
	case "CMD":
		cmd = c.params.Test[1:]
	case "CMD-SHELL":
		cmd = []string{"/bin/sh", "-c", c.params.Test[1]}
	case "NONE":
		return nil
	default:
		// Assume it's a direct command
		cmd = c.params.Test
	}

	// Execute with timeout
	execCtx, cancel := context.WithTimeout(ctx, c.params.Timeout)
	defer cancel()

	exitCode, stdout, stderr, err := c.exec(execCtx, cmd)
	if err != nil {
		slog.Debug("exec error", "instance", c.name, "error", err, "stdout", stdout, "stderr", stderr)
		return err
	}

	if exitCode != 0 {
		return fmt.Errorf("cmd failed, exit code: %d", exitCode)
	}

	return nil
}

// exec runs a command inside the instance and returns the exit code.
func (c *checker) exec(ctx context.Context, cmd []string) (int, string, string, error) {
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

	op, err := c.conn.ExecInstance(c.name, req, &args)
	if err != nil {
		return -1, "", "", err
	}

	// Wait for I/O to complete
	select {
	case <-args.DataDone:
	case <-ctx.Done():
		if err := op.Cancel(); err != nil {
			slog.Debug("canceling exec operation", "instance", c.name, "error", err)
		}
		return -1, "", "", ctx.Err()
	}

	// Wait for operation to complete
	err = op.Wait()
	if err != nil {
		return -1, stdout.String(), stderr.String(), err
	}

	// Get exit code from operation metadata
	opAPI := op.Get()
	if exitCode, ok := opAPI.Metadata["return"].(float64); ok {
		return int(exitCode), stdout.String(), stderr.String(), nil
	}

	return -1, "", "", nil
}

// writeStatus persists status into the instance's user.healthcheck.status
// config key. Before actually calling UpdateInstance, it sends the new
// status on statusCh - the runner uses this to recognize the resulting
// instance-updated event as self-caused, and to reset restart backoff on a
// healthy status.
func (c *checker) writeStatus(status string) error {
	if c.status == status {
		// We already wrote that.
		return nil
	}

	inst, etag, err := c.conn.GetInstance(c.name)
	if err != nil {
		return err
	}

	if inst.Config[shared.HealthStoppedKey] == "true" {
		status = shared.HealthStatusStopped

		if c.status == status {
			// We already wrote that.
			return nil
		}
	}

	if inst.Config[shared.HealthStatusKey] == status {
		return nil
	}

	select {
	case c.statusCh <- status:
	default:
		// Runner isn't reading fast enough; don't block the checker on it -
		// this is an efficiency measure only (see selfWrites), not required
		// for correctness.
	}

	slog.Info("Status update", "instance", c.name, "old", inst.Config[shared.HealthStatusKey], "current", status)

	wInst := inst.Writable()
	wInst.Config[shared.HealthStatusKey] = status
	op, err := c.conn.UpdateInstance(c.name, wInst, etag)
	if err != nil {
		return err
	}

	if err := op.Wait(); err != nil {
		return err
	}

	c.status = status
	return nil
}
