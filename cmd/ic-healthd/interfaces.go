package main

import (
	"context"
	"slices"
	"time"

	"github.com/lxc/incus-compose/shared"
)

const (
	certFile  = "client.crt"
	keyFile   = "client.key"
	tokenFile = "token"
)

const (
	// checkInstanceRunningDelay is the checker's own running-state poll.
	checkInstanceRunningDelay = 30 * time.Second
	maxRestartDelay           = 5 * time.Minute

	// debounceWindow coalesces bursts of instance-updated/instance-deleted
	// events for the same instance (e.g. several sequential `incus config
	// set` calls) into one kill-and-replace.
	debounceWindow = 100 * time.Millisecond
)

// Default healthcheck settings when keys are missing on the instance.
const (
	defaultRestartDelay  = 5 * time.Second
	defaultInterval      = 30 * time.Second
	defaultTimeout       = 30 * time.Second
	defaultRetries       = 3
	defaultStartPeriod   = 0 * time.Second
	defaultStartInterval = 5 * time.Second
)

// healthIgnoreKey opts an instance out of health checking entirely: excluded
// from discovery and from every lifecycle event handler. Set by incus-compose
// on the healthd sidecar itself, and available to any service via
// `x-incus: user.healthcheck.ignore: "true"`.
const healthIgnoreKey = shared.HealthKeyPrefix + "ignore"

// Config holds the healthd configuration.
type Config struct {
	DataDir    string
	SecretsDir string
	IncusURL   string
	Token      string
	OwnProject string
	OwnName    string
	Project    string
}

// instanceConfig is what a checker is built with: the six health-check
// parameters, plus Restart - a real parameter the runner reads for its own
// restart-policy logic, not something the checker itself acts on - and
// Running, the instance's run state at the moment of the read.
type instanceConfig struct {
	Test          []string
	StartPeriod   time.Duration
	StartInterval time.Duration
	Interval      time.Duration
	Timeout       time.Duration
	Retries       int

	Restart string
	Running bool // inst.StatusCode == api.Running, read alongside the other fields
}

// equal compares two instanceConfig values field by field. Test is a slice
// (not comparable with ==), so this can't be a plain == on the struct; use
// slices.Equal for Test and plain field comparison for everything else -
// no reflect.DeepEqual needed.
func (a instanceConfig) equal(b instanceConfig) bool {
	return slices.Equal(a.Test, b.Test) &&
		a.StartPeriod == b.StartPeriod &&
		a.StartInterval == b.StartInterval &&
		a.Interval == b.Interval &&
		a.Timeout == b.Timeout &&
		a.Retries == b.Retries &&
		a.Restart == b.Restart &&
		a.Running == b.Running
}

// trackedInstance is the runner's per-instance state.
type trackedInstance struct {
	cancel       context.CancelFunc
	restartDelay time.Duration // backoff; the runner owns this, not the checker

	knownParams   instanceConfig // what the live checker was built with
	serverParams  instanceConfig // most recent observed params (write-always)
	pendingDelete bool           // instance-deleted arrived; supersedes serverParams at notify time

	selfWrites int         // in-flight status writes not yet matched to their own event
	debounce   *time.Timer // nil when idle
	lastNotify time.Time
}
