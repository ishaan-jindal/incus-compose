package examples

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/require"
)

var snapshotter = cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "test", "snapshots", "examples")))

func skipExamples(t *testing.T) {
	_, ok := os.LookupEnv("INCUS_COMPOSE_TEST_EXAMPLES")
	if !ok {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_EXAMPLES is not set, run `just test-examples` for this test")
	}
}

func runCommand(t *testing.T, ctx context.Context, projectName string, args ...string) (*bytes.Buffer, error) {
	t.Helper()

	projectName = strings.ToLower(strings.ReplaceAll(projectName, "/", "-"))

	mArgs := []string{"run", "--", "github.com/lxc/incus-compose/cmd/incus-compose/...", "--debug", "--project-name", projectName}
	mArgs = append(mArgs, args...)
	slog.DebugContext(ctx, "Running", "args", mArgs)

	stdout := &bytes.Buffer{}
	execCmd := exec.CommandContext(ctx, "go", mArgs...) //nolint:gosec
	execCmd.Stdout = stdout
	execCmd.Stderr = os.Stderr

	err := execCmd.Run()
	return stdout, err
}

// stripListOutput removes dynamic content (IP addresses, network hashes) for snapshot comparison.
func stripListOutput(t *testing.T, output *bytes.Buffer) string {
	t.Helper()

	ipRegex, err := regexp.Compile(`\d+\.\d+\.\d+\.\d+`)
	require.NoError(t, err)
	outStr := ipRegex.ReplaceAllString(output.String(), "-stripped-")

	// // Strip health status for now, its flaky.
	// healthRegex, err := regexp.Compile(`"health": "[a-zA-Z]+",`)
	// require.NoError(t, err)
	// outStr = healthRegex.ReplaceAllString(outStr, `"health": "-stripped-",`)

	// Cupaloy adds a newline, 2 lines are bad for my editors format on save.
	return strings.Trim(outStr, "\n")
}

// func TestMain(m *testing.M) {
// 	logger := slog.New(slog.NewTextHandler(
// 		os.Stderr,
// 		&slog.HandlerOptions{Level: slog.LevelDebug - 4}),
// 	)

// 	slog.SetDefault(logger)

// 	code := m.Run()
// 	os.Exit(code)
// }

func TestExample(t *testing.T) {
	t.Parallel()
	skipExamples(t)

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

			ctx := context.Background()
			t.Cleanup(func() {
				_, _ = runCommand(t, ctx, t.Name(), "--project-directory", example.dir, "down", "--project")
			})

			args := []string{"--project-directory", example.dir, "up", "--detach"}
			_, err := runCommand(t, ctx, t.Name(), args...)
			require.NoError(t, err)

			// Sometimes this is needed to get the real health status.
			time.Sleep(1 * time.Second)

			args = []string{"--project-directory", example.dir, "list", "--format", "json"}
			stdout, err := runCommand(t, ctx, t.Name(), args...)
			require.NoError(t, err)

			snapshotter.SnapshotT(t, stripListOutput(t, stdout))
		})
	}
}
