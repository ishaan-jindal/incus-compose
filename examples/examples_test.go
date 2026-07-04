package examples

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/require"
)

var snapshotter = cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "test", "snapshots", "examples")))

func skipExamples(t *testing.T) {
	_, ok := os.LookupEnv("INCUS_COMPOSE_TEST_EXAMPLES")
	if !ok {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_EXAMPLES is not set, run `just test-slow` for this test")
	}
}

func runCommand(t *testing.T, ctx context.Context, projectName string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()

	projectName = strings.ToLower(strings.ReplaceAll(projectName, "/", "-"))

	mArgs := []string{"run", "--", "github.com/lxc/incus-compose/cmd/incus-compose/...", "--debug", "--project-name", projectName}
	mArgs = append(mArgs, args...)
	slog.DebugContext(ctx, "Running", "args", mArgs)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	execCmd := exec.CommandContext(ctx, "go", mArgs...) //nolint:gosec
	execCmd.Stdout = stdout
	// execCmd.Stderr = os.Stderr
	execCmd.Stderr = stderr

	err := execCmd.Run()
	return stdout, stderr, err
}

// normalizeListOutput removes dynamic content (IP addresses, network hashes) for snapshot comparison.
func normalizeListOutput(t *testing.T, output *bytes.Buffer) string {
	t.Helper()

	ipRegex := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	outStr := ipRegex.ReplaceAllString(output.String(), "")

	return outStr
}

func TestMain(m *testing.M) {
	logger := slog.New(slog.NewTextHandler(
		os.Stdout,
		&slog.HandlerOptions{Level: slog.LevelDebug - 4}),
	)

	slog.SetDefault(logger)

	code := m.Run()
	os.Exit(code)
}

func TestExample(t *testing.T) {
	t.Parallel()
	skipExamples(t)

	tname := t.Name()

	examples := []struct {
		name string
		dir  string
	}{
		{
			name: "hugo",
			dir:  "./hugo/",
		},
		{
			name: "immich",
			dir:  "./immich/",
		},
		{
			name: "immich",
			dir:  "./immich/",
		},
		{
			name: "many-dependencies",
			dir:  "./many-dependencies/",
		},
		{
			name: "wikijs",
			dir:  "./wikijs/",
		},
	}

	for _, example := range examples {
		t.Run(example.name, func(t *testing.T) {
			t.Parallel()

			projectName := tname + "-" + example.name

			ctx := context.Background()
			t.Cleanup(func() {
				_, _, _ = runCommand(t, ctx, projectName, "--project-directory", example.dir, "down", "--project")
			})

			args := []string{"--project-directory", example.dir, "up", "--detach", "--timeout", "15m", "--dependency-timeout", "15m"}
			_, stderr, err := runCommand(t, ctx, projectName, args...)
			if err != nil {
				_, err = fmt.Fprint(os.Stderr, stderr)
				if err != nil {
					slog.Error("Failed to write stderr after an error", "error", err)
				}
				t.FailNow()
			}

			args = []string{"--project-directory", example.dir, "list", "--format", "json"}
			stdout, _, err := runCommand(t, ctx, projectName, args...)
			require.NoError(t, err)

			snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
		})
	}
}
