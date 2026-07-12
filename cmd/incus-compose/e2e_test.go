package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
)

func cleanLines(t *testing.T, in string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(in), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// TestE2EUpNoDeps verifies `up <service> --no-deps` starts only the named service
// and does not wait on its (unstarted) service_healthy dependencies.
func TestE2EUpNoDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach", "--no-deps", "nginx")
	require.NoError(t, err)

	_, err = runCommand(t, ctx, pn, "-f", compose, "ps", "--quiet")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	exists, err := c.InstanceExists("nginx-1")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = c.InstanceExists("backend1-1")
	require.NoError(t, err)
	assert.False(t, exists)

	exists, err = c.InstanceExists("backend2-1")
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestE2EUpDeps verifies `up <service>` (default) follows depends_on and starts the
// linked services too.
func TestE2EUpDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach", "nginx")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	exists, err := c.InstanceExists("nginx-1")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = c.InstanceExists("backend1-1")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = c.InstanceExists("backend2-1")
	require.NoError(t, err)
	assert.True(t, exists)
}

// TestE2EDownNoDeps verifies `down <service> --no-deps` removes only the named
// service and leaves its dependants running.
func TestE2EDownNoDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = runCommand(t, ctx, pn, "-f", compose, "down", "--no-deps", "backend1")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	exists, err := c.InstanceExists("nginx-1")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = c.InstanceExists("backend2-1")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = c.InstanceExists("backend1-1")
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestE2EDownDeps verifies `down <service>` (default) follows depends_on in reverse
// and also removes the services that depend on the named one.
func TestE2EDownDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	exists, err := c.InstanceExists("nginx-1")
	require.NoError(t, err)
	assert.True(t, exists)

	_, err = runCommand(t, ctx, pn, "-f", compose, "down", "backend1")
	require.NoError(t, err)

	exists, err = c.InstanceExists("backend2-1")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = c.InstanceExists("backend1-1")
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestE2EPsDeps verifies that `ps <service> --with-deps` includes the linked
// services as real services, whereas the default scopes to the named service
// (other running instances show up only as <orphan>).
func TestE2EPsDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	stdoutNoDeps, err := runCommand(t, ctx, pn, "-f", compose, "ps", "--services", "nginx")
	require.NoError(t, err)

	noDeps := cleanLines(t, stdoutNoDeps.String())
	require.Contains(t, noDeps, "nginx")
	require.NotContains(t, noDeps, "backend1")
	require.NotContains(t, noDeps, "backend2")

	stdoutDeps, err := runCommand(t, ctx, pn, "-f", compose, "ps", "--services", "--with-deps", "nginx")
	require.NoError(t, err)

	withDeps := cleanLines(t, stdoutDeps.String())
	require.Contains(t, withDeps, "nginx")
	require.Contains(t, withDeps, "backend1")
	require.Contains(t, withDeps, "backend2")
}

// TestE2EStartStopRestartWithDeps exercises start/stop/restart/logs with and
// without --with-deps. The default keeps each command scoped to the named
// service (and, crucially, start does not block on out-of-scope healthd
// dependency conditions); --with-deps follows depends_on like up/down.
func TestE2EStartStopRestartWithDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name:     "list up",
			args:     []string{"-f", compose, "list", "--format=json"},
			snapshot: true,
		},
		{
			name: "restart",
			args: []string{"-f", compose, "restart", "--with-deps", "nginx"},
		},
		{
			name:     "list restart",
			args:     []string{"-f", compose, "list", "--format=json"},
			snapshot: true,
		},
		{
			name: "stop manually",
			args: []string{"-f", compose, "stop", "nginx", "backend1", "backend2"},
		},
		{
			name:            "list manual stop",
			args:            []string{"-f", compose, "list", "--format=json"},
			snapshot:        true,
			snapStripHealth: true,
		},
		{
			name: "start deps",
			args: []string{"-f", compose, "start", "--with-deps", "nginx"},
		},
		{
			name:     "list start deps",
			args:     []string{"-f", compose, "list", "--format=json"},
			snapshot: true,
		},
		{
			name: "stop deps",
			args: []string{"-f", compose, "stop", "--with-deps", "backend1"},
		},
		{
			name:            "list stop deps",
			args:            []string{"-f", compose, "list", "--format=json"},
			snapshot:        true,
			snapStripHealth: true,
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpDownGrafana(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/grafana/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up grafana",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list grafana",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpUp(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "up2",
			args: []string{"-f", compose, "up", "--detach"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EDownDown(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "down1",
			args: []string{"-f", compose, "down"},
		},
		{
			name: "down2",
			args: []string{"-f", compose, "down"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EDownProjectDeletesNetworks(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	networks := plannedNetworkNames(t, ctx, pn, compose)
	require.NotEmpty(t, networks)

	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
		}
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)

	for _, name := range networks {
		conn, err := c.Connection()
		require.NoError(t, err)
		_, _, err = conn.GetNetwork(name)
		require.NoError(t, err, "for network %q", name)
	}

	_, err = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	require.NoError(t, err)
	cleaned = true

	for _, name := range networks {
		conn, err := c.Connection()
		require.NoError(t, err)
		_, _, err = conn.GetNetwork(name)
		require.Error(t, err, "for network %q", name)
	}
}

func TestE2EUpRecreate(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up simple-nginx",
			args: []string{"-f", compose, "up", "--detach", "--recreate"},
		},
		{
			name: "list simple-nginx",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpUpRecreate(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up simple-nginx",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list1 simple-nginx",
			args: []string{"-f", compose, "list"},
		},
		{
			name: "up simple-nginx",
			args: []string{"-f", compose, "up", "--detach", "--recreate"},
		},
		{
			name: "list2 simple-nginx",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpRecreateDown(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up simple-nginx",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list simple-nginx",
			args: []string{"-f", compose, "list"},
		},
		{
			name: "recreate simple-nginx",
			args: []string{"-f", compose, "up", "--detach", "--recreate"},
		},
		{
			name: "list recreated simple-nginx",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2ELifecycleSimpleNginx(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name:     "ps json",
			args:     []string{"-f", compose, "ps", "--format", "json", "--all"},
			snapshot: true,
		},
		{
			name:     "ps quiet",
			args:     []string{"-f", compose, "ps", "--format", "json", "--all", "--quiet"},
			snapshot: true,
		},
		{
			name:     "ps services",
			args:     []string{"-f", compose, "ps", "--format", "json", "--all", "--services"},
			snapshot: true,
		},
		{
			name: "stop service",
			args: []string{"-f", compose, "stop", "web"},
		},
		{
			name:     "ps stopped",
			args:     []string{"-f", compose, "ps", "--format", "json", "--all"},
			snapshot: true,
		},
		{
			name: "start service",
			args: []string{"-f", compose, "start", "web"},
		},
		{
			name:     "exec dry run",
			args:     []string{"-f", compose, "exec", "--dry-run", "web", "echo", "hello"},
			snapshot: true,
		},
		{
			name: "restart service",
			args: []string{"-f", compose, "restart", "web"},
		},
		{
			name: "down resources",
			args: []string{"-f", compose, "down"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpDownScale(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-scale/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up nginx-scale",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "scale nginx-scale",
			args: []string{"-f", compose, "up", "--detach", "--scale=web=3"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpDownDownscale(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-scale/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up nginx-scale",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "downscale nginx-scale",
			args: []string{"-f", compose, "up", "--detach", "--scale=web=6"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpDownWithScale(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-scale/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up nginx-scale",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list nginx-scale",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EListSnapshots(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "list_yaml",
			args: []string{"-f", compose, "list", "--format", "yaml"},
		},
		{
			name: "list_json",
			args: []string{"-f", compose, "list", "--format", "json"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EExternalNetwork(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/test-external-network/compose.yaml"

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	conn, err := gc.Connection()
	require.NoError(t, err)

	_, _, err = conn.GetNetwork("incusbr0")
	if err != nil {
		t.Skipf("No incusbr0: %v", err)
	}

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up test-external-network",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list test-external-network",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpDownWithIncusOptions(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-incus-options/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up with-incus-options",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list with-incus-options",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpDownWithProjectOptions(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-project-options/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up with-project-options",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list with-project-options",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpDownWithSecrets(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-secrets/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up with-secrets",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list with-secrets",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpDownWithSecretsVerifyFiles(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-secrets/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	// Verify each secret file exists with correct perms/ownership
	checks := []struct {
		path  string
		perms string
		uid   string
		gid   string
	}{
		{"/run/secrets/demo_secret", "-r--------", "0", "0"},   // default mode 0o600
		{"/run/secrets/db_password", "-r--------", "0", "0"},   // default mode 0o600
		{"/app/secrets/api.key", "-r--r--r--", "1000", "1000"}, // explicit 0o444 + uid/gid
	}

	for _, tc := range checks {
		stdout, err := runCommand(t, ctx, pn, "-f", compose, "exec", "--no-tty", "app",
			"--", "ls", "-ln", tc.path)
		require.NoError(t, err)

		fields := strings.Fields(stdout.String())
		require.GreaterOrEqual(t, len(fields), 4, "unexpected ls output for %s: %q", tc.path, stdout.String())
		assert.Equal(t, tc.perms, fields[0], "perms for %s", tc.path)
		assert.Equal(t, tc.uid, fields[2], "uid for %s", tc.path)
		assert.Equal(t, tc.gid, fields[3], "gid for %s", tc.path)
	}
}

func TestE2EUpDownWithConfigs(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-configs/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up with-configs",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list with-configs",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}

func TestE2EUpDownWithConfigsVerifyFiles(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-configs/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	// Verify each config file exists with correct perms/ownership
	checks := []struct {
		path  string
		perms string
		uid   string
		gid   string
	}{
		{"/app_config", "-r--r--r--", "0", "0"},               // default mode 0o444
		{"/db_config", "-r--r--r--", "0", "0"},                // default mode 0o444
		{"/etc/nginx/nginx.conf", "-r--r-----", "101", "101"}, // explicit 0o640, write bit ignored -> 0o440
	}

	for _, tc := range checks {
		stdout, err := runCommand(t, ctx, pn, "-f", compose, "exec", "--no-tty", "app",
			"--", "ls", "-ln", tc.path)
		require.NoError(t, err)

		fields := strings.Fields(stdout.String())
		require.GreaterOrEqual(t, len(fields), 4, "unexpected ls output for %s: %q", tc.path, stdout.String())
		assert.Equal(t, tc.perms, fields[0], "perms for %s", tc.path)
		assert.Equal(t, tc.uid, fields[2], "uid for %s", tc.path)
		assert.Equal(t, tc.gid, fields[3], "gid for %s", tc.path)
	}
}

func TestE2EDownImages(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = runCommand(t, ctx, pn, "-f", compose, "down")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	r, err := c.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{})
	require.NoError(t, err)
	require.NoError(t, client.RunAction(ctx, r, client.ActionEnsure), "image should survive plain down")

	_, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = runCommand(t, ctx, pn, "-f", compose, "down", "--images")
	require.NoError(t, err)

	r, err = c.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{})
	require.NoError(t, err)
	require.Error(t, client.RunAction(ctx, r, client.ActionEnsure), "image should be removed by down --images")
}

func TestE2EUpDownWithVolume(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-volume/compose.yaml"

	t.Cleanup(func() {
		_, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []e2eTest{
		{
			name: "up with-volume",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "list with-volume",
			args: []string{"-f", compose, "list"},
		},
	}

	runE2ETests(t, ctx, pn, tests)
}
