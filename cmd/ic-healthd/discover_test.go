package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/shared"
)

// ----------------------------------------------------------------------------
// isIgnored / hasHealthCheck
// ----------------------------------------------------------------------------

func TestIsIgnored(t *testing.T) {
	t.Parallel()

	assert.True(t, isIgnored(map[string]string{healthIgnoreKey: "true"}))
	assert.False(t, isIgnored(map[string]string{healthIgnoreKey: "false"}))
	assert.False(t, isIgnored(map[string]string{}))
}

func TestHasHealthCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  map[string]string
		want bool
	}{
		{"nothing set", map[string]string{}, false},
		{"has test", map[string]string{shared.HealthKeyPrefix + "test": `["NONE"]`}, true},
		{"has restart always", map[string]string{shared.HealthKeyPrefix + "restart": "always"}, true},
		{"has restart on-failure", map[string]string{shared.HealthKeyPrefix + "restart": "on-failure"}, true},
		{"has restart unless-stopped", map[string]string{shared.HealthKeyPrefix + "restart": "unless-stopped"}, true},
		{"restart no", map[string]string{shared.HealthKeyPrefix + "restart": "no"}, false},
		{"restart garbage", map[string]string{shared.HealthKeyPrefix + "restart": "bogus"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, hasHealthCheck(tt.cfg))
		})
	}
}

// ----------------------------------------------------------------------------
// parseInstance
// ----------------------------------------------------------------------------

func TestParseInstance_Defaults(t *testing.T) {
	t.Parallel()

	cfg, err := parseInstance(map[string]string{
		shared.HealthKeyPrefix + "test": `["CMD","true"]`,
	}, true)
	require.NoError(t, err)

	assert.Equal(t, []string{"CMD", "true"}, cfg.Test)
	assert.Equal(t, defaultStartPeriod, cfg.StartPeriod)
	assert.Equal(t, defaultStartInterval, cfg.StartInterval)
	assert.Equal(t, defaultInterval, cfg.Interval)
	assert.Equal(t, defaultTimeout, cfg.Timeout)
	assert.Equal(t, defaultRetries, cfg.Retries)
	assert.Equal(t, "", cfg.Restart)
	assert.True(t, cfg.Running)
}

func TestParseInstance_AllKeysOverride(t *testing.T) {
	t.Parallel()

	cfg, err := parseInstance(map[string]string{
		shared.HealthKeyPrefix + "test":           `["CMD","true"]`,
		shared.HealthKeyPrefix + "start_period":   "10s",
		shared.HealthKeyPrefix + "start_interval": "2s",
		shared.HealthKeyPrefix + "interval":       "15s",
		shared.HealthKeyPrefix + "timeout":        "3s",
		shared.HealthKeyPrefix + "retries":        "7",
		shared.HealthKeyPrefix + "restart":        "always",
	}, false)
	require.NoError(t, err)

	assert.Equal(t, 10*time.Second, cfg.StartPeriod)
	assert.Equal(t, 2*time.Second, cfg.StartInterval)
	assert.Equal(t, 15*time.Second, cfg.Interval)
	assert.Equal(t, 3*time.Second, cfg.Timeout)
	assert.Equal(t, 7, cfg.Retries)
	assert.Equal(t, "always", cfg.Restart)
	assert.False(t, cfg.Running)
}

func TestParseInstance_RestartPolicyWithoutTestDefaultsToNone(t *testing.T) {
	t.Parallel()

	cfg, err := parseInstance(map[string]string{
		shared.HealthKeyPrefix + "restart": "unless-stopped",
	}, true)
	require.NoError(t, err)

	assert.Equal(t, []string{"NONE"}, cfg.Test)
	assert.Equal(t, "unless-stopped", cfg.Restart)
}

func TestParseInstance_NoTestNoRestart(t *testing.T) {
	t.Parallel()

	cfg, err := parseInstance(map[string]string{}, true)
	require.NoError(t, err)
	assert.Empty(t, cfg.Test)
}

func TestParseInstance_InvalidTestJSON(t *testing.T) {
	t.Parallel()

	_, err := parseInstance(map[string]string{
		shared.HealthKeyPrefix + "test": `not json`,
	}, true)
	require.Error(t, err)
}

func TestParseInstance_CmdShellRequiresCommand(t *testing.T) {
	t.Parallel()

	_, err := parseInstance(map[string]string{
		shared.HealthKeyPrefix + "test": `["CMD-SHELL"]`,
	}, true)
	require.Error(t, err)
}

func TestParseInstance_InvalidDurations(t *testing.T) {
	t.Parallel()

	keys := []string{"start_period", "start_interval", "interval", "timeout"}
	for _, k := range keys {
		t.Run(k, func(t *testing.T) {
			t.Parallel()
			_, err := parseInstance(map[string]string{
				shared.HealthKeyPrefix + "test": `["NONE"]`,
				shared.HealthKeyPrefix + k:      "not-a-duration",
			}, true)
			require.Error(t, err)
		})
	}
}

func TestParseInstance_InvalidRetries(t *testing.T) {
	t.Parallel()

	_, err := parseInstance(map[string]string{
		shared.HealthKeyPrefix + "test":    `["NONE"]`,
		shared.HealthKeyPrefix + "retries": "not-a-number",
	}, true)
	require.Error(t, err)
}

func TestParseInstance_ZeroRetriesErrors(t *testing.T) {
	t.Parallel()

	_, err := parseInstance(map[string]string{
		shared.HealthKeyPrefix + "test":    `["NONE"]`,
		shared.HealthKeyPrefix + "retries": "0",
	}, true)
	require.Error(t, err)
}

// ----------------------------------------------------------------------------
// baseRestartDelay
// ----------------------------------------------------------------------------

func TestBaseRestartDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  instanceConfig
		want time.Duration
	}{
		{
			name: "interval*retries within bounds",
			cfg:  instanceConfig{Interval: 10 * time.Second, Retries: 3},
			want: 30 * time.Second,
		},
		{
			name: "clamped to maxRestartDelay",
			cfg:  instanceConfig{Interval: time.Hour, Retries: 10},
			want: maxRestartDelay,
		},
		{
			name: "floored to defaultRestartDelay",
			cfg:  instanceConfig{Interval: time.Millisecond, Retries: 1},
			want: defaultRestartDelay,
		},
		{
			name: "zero interval falls back to default",
			cfg:  instanceConfig{Interval: 0, Retries: 3},
			want: defaultRestartDelay,
		},
		{
			name: "zero retries falls back to default",
			cfg:  instanceConfig{Interval: 10 * time.Second, Retries: 0},
			want: defaultRestartDelay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, baseRestartDelay(tt.cfg))
		})
	}
}

// ----------------------------------------------------------------------------
// instanceConfig.equal
// ----------------------------------------------------------------------------

func TestInstanceConfig_Equal(t *testing.T) {
	t.Parallel()

	base := instanceConfig{
		Test:          []string{"CMD", "true"},
		StartPeriod:   time.Second,
		StartInterval: time.Second,
		Interval:      time.Second,
		Timeout:       time.Second,
		Retries:       3,
		Restart:       "always",
		Running:       true,
	}

	same := base
	same.Test = []string{"CMD", "true"} // distinct slice, same contents
	assert.True(t, base.equal(same))

	tests := []struct {
		name   string
		mutate func(instanceConfig) instanceConfig
	}{
		{"test differs", func(c instanceConfig) instanceConfig { c.Test = []string{"CMD", "false"}; return c }},
		{"test length differs", func(c instanceConfig) instanceConfig { c.Test = []string{"CMD"}; return c }},
		{"start period differs", func(c instanceConfig) instanceConfig { c.StartPeriod = 2 * time.Second; return c }},
		{"start interval differs", func(c instanceConfig) instanceConfig { c.StartInterval = 2 * time.Second; return c }},
		{"interval differs", func(c instanceConfig) instanceConfig { c.Interval = 2 * time.Second; return c }},
		{"timeout differs", func(c instanceConfig) instanceConfig { c.Timeout = 2 * time.Second; return c }},
		{"retries differs", func(c instanceConfig) instanceConfig { c.Retries = 9; return c }},
		{"restart differs", func(c instanceConfig) instanceConfig { c.Restart = "no"; return c }},
		{"running differs", func(c instanceConfig) instanceConfig { c.Running = false; return c }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			other := tt.mutate(base)
			assert.False(t, base.equal(other))
			assert.False(t, other.equal(base))
		})
	}
}

func TestInstanceConfig_Equal_BothEmptyTest(t *testing.T) {
	t.Parallel()

	a := instanceConfig{}
	b := instanceConfig{}
	assert.True(t, a.equal(b))
}
