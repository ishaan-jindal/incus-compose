package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/r3j0/incus-compose/client"
)

func TestUpDownGrafana(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/grafana/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up grafana",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list grafana",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDownProjectDeletesNetworks(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	networks := plannedNetworkNames(t, ctx, pn, compose)
	require.NotEmpty(t, networks)

	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
		}
	})

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c, err := gc.EnsureProject(pn)
	require.NoError(t, err)

	for _, name := range networks {
		_, _, err := c.Connection().GetNetwork(name)
		require.NoError(t, err)
	}

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	require.NoError(t, err)
	cleaned = true

	for _, name := range networks {
		_, _, err := c.Connection().GetNetwork(name)
		require.Error(t, err)
	}
}

func TestUpSimpleNginx(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		snapshot bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:     "list simple-nginx",
			args:     []string{"-f", compose, "list"},
			wantErr:  false,
			snapshot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tt.snapshot {
				snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
			}
		})
	}
}

func TestUpDownUpSimpleNginx(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		snapshot bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:     "list up simple-nginx",
			args:     []string{"-f", compose, "list"},
			wantErr:  false,
			snapshot: true,
		},
		{
			name:    "down simple-nginx",
			args:    []string{"-f", compose, "down"},
			wantErr: false,
		},
		{
			name:     "list down simple-nginx",
			args:     []string{"-f", compose, "list"},
			wantErr:  false,
			snapshot: true,
		},
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:     "list down-up simple-nginx",
			args:     []string{"-f", compose, "list"},
			wantErr:  false,
			snapshot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tt.snapshot {
				snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
			}
		})
	}
}

func TestUpRecreate(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach", "--recreate"},
			wantErr: false,
		},
		{
			name:    "list simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUpUpRecreate(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list1 simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach", "--recreate"},
			wantErr: false,
		},
		{
			name:    "list2 simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUpRecreateDown(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
		{
			name:    "recreate simple-nginx",
			args:    []string{"-f", compose, "up", "--detach", "--recreate"},
			wantErr: false,
		},
		{
			name:    "list recreated simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestLifecycleSimpleNginx(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "up",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "ps table",
			args: []string{"-f", compose, "ps", "--all"},
		},
		{
			name: "ps json",
			args: []string{"-f", compose, "ps", "--all", "--format", "json"},
		},
		{
			name: "ps quiet",
			args: []string{"-f", compose, "ps", "--all", "--quiet"},
		},
		{
			name: "ps services",
			args: []string{"-f", compose, "ps", "--all", "--services"},
		},
		{
			name: "stop service",
			args: []string{"-f", compose, "stop", "web"},
		},
		{
			name: "ps stopped",
			args: []string{"-f", compose, "ps", "--all"},
		},
		{
			name: "start service",
			args: []string{"-f", compose, "start", "web"},
		},
		{
			name: "exec dry run",
			args: []string{"-f", compose, "exec", "--dry-run", "web", "echo", "hello"},
		},
		// {
		// 	name: "restart service",
		// 	args: []string{"-f", compose, "restart", "web"},
		// },
		{
			name: "logs service",
			args: []string{"-f", compose, "logs", "web"},
		},
		{
			name: "down resources",
			args: []string{"-f", compose, "down"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			require.NoError(t, err)
		})
	}
}

func TestUpDownScale(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-scale/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "scale nginx-scale",
			args:    []string{"-f", compose, "up", "--detach", "--scale=web=3"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUpDownDownscale(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-scale/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "downscale nginx-scale",
			args:    []string{"-f", compose, "up", "--detach", "--scale=web=6"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUpDownWithScale(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-scale/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list nginx-scale",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestListSnapshots(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "list_table",
			args: []string{"-f", compose, "list"},
		},
		{
			name: "list_yaml",
			args: []string{"-f", compose, "list", "--format", "yaml"},
		},
		{
			name: "list_json",
			args: []string{"-f", compose, "list", "--format", "json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, tt.args...)
			require.NoError(t, err)
			snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
		})
	}
}

func TestExternalNetwork(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/test-external-network/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up test-external-network",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list test-external-network",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUpDownWithIncusOptions(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-incus-options/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-incus-options",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-incus-options",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUpDownWithProjectOptions(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-project-options/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-project-options",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-project-options",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUpDownWithSecrets(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-secrets/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-secrets",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-secrets",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUpDownWithVolume(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-volume/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-volume",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-volume",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
