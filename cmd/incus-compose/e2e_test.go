package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/require"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

var snapshotter = cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "..", "test", "snapshots", "e2e")))

func skipLocal(t *testing.T) {
	if os.Getenv("INCUS_COMPOSE_TEST_LOCAL") != "" {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_LOCAL is set, run `just test` for this test")
	}
}

func skipSlow(t *testing.T) {
	if os.Getenv("INCUS_COMPOSE_TEST_SLOW") == "" {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_SLOW is not set, run `just test-slow` for this test")
	}
}

func runCommand(t *testing.T, ctx context.Context, projectName string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()

	projectName = strings.ToLower(strings.ReplaceAll(projectName, "/", "-"))

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := newRootCommand()
	cmd.Writer = stdout
	cmd.ErrWriter = stderr
	err := cmd.Run(ctx, append([]string{
		"incus-compose",
		"--debug",
		"--project-name ", projectName,
	}, args...),
	)

	return stdout, stderr, err
}

// normalizeListOutput removes dynamic content (IP addresses, network hashes) for snapshot comparison.
func normalizeListOutput(t *testing.T, output *bytes.Buffer) string {
	t.Helper()

	ipRegex := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	outStr := ipRegex.ReplaceAllString(output.String(), "")

	return outStr
}

func plannedNetworkNames(t *testing.T, ctx context.Context, pn, compose string) []string {
	t.Helper()

	proj, err := project.New().Load(ctx, project.LoadFiles([]string{compose}))
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, pn)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack))

	names := []string{}
	for _, r := range stack.All() {
		if r.Kind() == client.KindNetwork {
			names = append(names, r.IncusName())
		}
	}
	return names
}

func TestConfigCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "simple-nginx yaml",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "simple-nginx json",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "config", "--format", "json"},
			wantErr: false,
		},
		{
			name:    "wordpress",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-restart",
			args:    []string{"-f", "../../test/fixtures/with-restart/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-incus-options",
			args:    []string{"-f", "../../test/fixtures/with-incus-options/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-project-options",
			args:    []string{"-f", "../../test/fixtures/with-project-options/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-build",
			args:    []string{"-f", "../../test/fixtures/with-build/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "nonexistent file",
			args:    []string{"-f", "nonexistent.yaml", "config"},
			wantErr: true,
		},
		{
			name:    "invalid yaml",
			args:    []string{"-f", "../../test/fixtures/invalid/compose.yaml", "config"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, err := runCommand(t, context.Background(), "test-local-config", tt.args...)

			if tt.wantErr {
				require.Error(t, err, "Stdout: %s --- Stderr: %s", stdout.String(), stderr.String())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigFilterByService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "wordpress filter db service",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "db"},
			wantErr: false,
		},
		{
			name:    "wordpress filter wordpress service includes db dependency",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "wordpress"},
			wantErr: false,
		},
		{
			name:    "config --services list",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--services"},
			wantErr: false,
		},
		{
			name:    "config --volumes list",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--volumes"},
			wantErr: false,
		},
		{
			name:    "config --quiet validation",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--quiet"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := runCommand(t, context.Background(), "test-local-config-extra", tt.args...)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
