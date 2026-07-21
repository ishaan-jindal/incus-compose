package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

var snapshotter = cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "..", "test", "snapshots", "ic-healthd")))

func skipLocal(t *testing.T) {
	_, ok := os.LookupEnv("INCUS_COMPOSE_TEST_LOCAL")
	if ok {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_LOCAL is set, run `just test` for this test")
	}
}

func skipE2E(t *testing.T) {
	_, ok := os.LookupEnv("INCUS_COMPOSE_TEST_E2E")
	if !ok {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_E2E is not set, run `just test-e2e` for this test")
	}
}

func runIncusCommand(ctx context.Context, t *testing.T, projectName string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
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

// stripListOutput removes dynamic content (IP addresses, network hashes) for snapshot comparison.
func stripListOutput(t *testing.T, output *bytes.Buffer) string {
	t.Helper()

	ipRegex := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	outStr := ipRegex.ReplaceAllString(output.String(), "-stripped-")

	// // Strip health status for now, its flaky.
	// healthRegex, err := regexp.Compile(`"health": "[a-zA-Z]+",`)
	// require.NoError(t, err)
	// outStr = healthRegex.ReplaceAllString(outStr, `"health": "-stripped-",`)

	// Cupaloy adds a newline, 2 lines are bad for my editors format on save.
	return strings.Trim(outStr, "\n")
}

func projectClient(ctx context.Context, t *testing.T, projectName string, opts ...client.EnsureProjectOption) *client.Client {
	t.Helper()

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	err = gc.Connect()
	require.NoError(t, err)

	c, err := gc.EnsureProject(projectName, opts...)
	require.NoError(t, err)

	return c
}

func loadProject(ctx context.Context, t *testing.T, compose string, projectName string) (*client.Client, *project.Project) {
	files := []string{compose}
	incusCFile := filepath.Join(
		filepath.Dir(compose),
		strings.TrimSuffix(
			filepath.Base(compose),
			filepath.Ext(compose))+".incus"+filepath.Ext(compose),
	)
	if _, err := os.Stat(incusCFile); err == nil {
		files = append(files, incusCFile)
	}

	p, err := project.New().Load(
		ctx,
		project.LoadName(projectName),
		project.LoadFiles(files),
	)
	require.NoError(t, err)

	c := projectClient(ctx, t, projectName,
		client.EnsureProjectWithCreate(),
		client.EnsureProjectWithConfig(p.ClientConfig.XIncus),
	)

	return c, p
}

// newToken creates a restricted token for the healthd to use.
func newToken(c *client.Client) (string, error) {
	req := incusApi.CertificatesPost{
		CertificatePut: incusApi.CertificatePut{
			Name:       "ic-healthd-" + c.IncusProject(),
			Type:       "client",
			Restricted: true,
			Projects:   []string{c.IncusProject()},
		},
		Token: true,
	}

	conn, err := c.Connection()
	if err != nil {
		return "", err
	}

	op, err := conn.CreateCertificateToken(req)
	if err != nil {
		return "", err
	}

	opAPI := op.Get()
	addToken, err := opAPI.ToCertificateAddToken()
	if err != nil {
		return "", fmt.Errorf("converting operation to certificate add token: %w", err)
	}

	return addToken.String(), nil
}

func incusURL(c *client.Client) (string, error) {
	u, ok := os.LookupEnv("INCUS_COMPOSE_HEALTHD_INCUS")
	if ok {
		return u, nil
	}

	if !c.IsRemote() {
		return "", errors.New("healthd works only with a https connection, provide one with INCUS_COMPOSE_HEALTHD_INCUS")
	}

	return c.Config().URL, nil
}

// revokeCert removes the healthd's trust-store certificate, if any.
func revokeCert(c *client.Client) error {
	gConn, err := c.GlobalConnection()
	if err != nil {
		return fmt.Errorf("while getting a global connection: %w", err)
	}

	certs, err := gConn.GetCertificates()
	if err != nil {
		return fmt.Errorf("listing certificates: %w", err)
	}

	want := "ic-healthd-" + c.IncusProject()
	for _, cert := range certs {
		if cert.Name != want {
			continue
		}
		if err := gConn.DeleteCertificate(cert.Fingerprint); err != nil {
			return fmt.Errorf("deleting certificate %s: %w", cert.Fingerprint, err)
		}
	}
	return nil
}

func prepareHealthd(t *testing.T, c *client.Client) (func(), *Runner) {
	t.Helper()

	cmd := newRootCommand()
	// stdout := &bytes.Buffer{}
	// stderr := &bytes.Buffer{}
	// cmd.Writer = stdout
	// cmd.ErrWriter = stderr
	cmd.Writer = os.Stdout
	cmd.ErrWriter = os.Stderr

	incusURL, err := incusURL(c)
	require.NoError(t, err)

	token, err := newToken(c)
	require.NoError(t, err)

	secretsDir, err := os.MkdirTemp("", "ic-secrets-*")
	require.NoError(t, err)
	dataDir, err := os.MkdirTemp("", "ic-data-*")
	require.NoError(t, err)

	cfg := &Config{}
	cfg.DataDir = dataDir
	cfg.SecretsDir = secretsDir
	cfg.IncusURL = incusURL
	cfg.Project = c.IncusProject()
	cfg.Token = token

	runner, err := NewRunner(cfg)
	require.NoError(t, err)

	cleanup := func() {
		_ = revokeCert(c)

		_ = os.RemoveAll(secretsDir)
		_ = os.RemoveAll(dataDir)
	}

	return cleanup, runner
}

func TestMain(m *testing.M) {
	logger := slog.New(slog.NewTextHandler(
		os.Stderr,
		&slog.HandlerOptions{Level: slog.LevelDebug - 4}),
	)

	slog.SetDefault(logger)

	code := m.Run()
	os.Exit(code)
}

func TestE2EHealthdNginx(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx, cancel := context.WithCancel(t.Context())
	projectName := strings.ToLower(t.Name())
	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	c, p := loadProject(ctx, t, compose, projectName)
	err := c.Open()
	require.NoError(t, err)

	hCleanup, hRunner := prepareHealthd(t, c)

	hReload := make(chan struct{}, 10)

	go func() {
		err := hRunner.Run(ctx, hReload)
		if err != nil {
			slog.Error("Runner exited", "error", err)
		}
	}()

	t.Cleanup(func() {
		_ = c.Done()

		_, _, _ = runIncusCommand(context.Background(), t, projectName, "-f", compose, "down", "--project")
		hCleanup()
		cancel()
	})

	c.IgnoreError(client.ActionEnsure, client.ErrNotFound)

	// err = c.RegisterDNSWatcher()
	// require.NoError(t, err)

	c.AddHookAfter(func(_ context.Context, _ client.Action, r client.Resource, _ client.Options, err error) error {
		if err != nil || !r.IsEnsured() || r.Kind() != client.KindInstance {
			return err
		}

		hReload <- struct{}{}

		return err
	})

	stack := client.NewStack(c, client.StackFailFast())
	order, err := p.ServiceOrder(false)
	require.NoError(t, err)

	resources, err := p.Resources(c)
	require.NoError(t, err)
	stack.AddOrdered(order, resources)

	err = stack.ForAction(client.ActionEnsure).Run(
		ctx,
		client.ActionEnsure,
		os.Stdout,
		os.Stderr,
		client.OptionCreate(),
	)
	require.NoError(t, err)

	err = stack.ForAction(client.ActionStart).Run(
		ctx,
		client.ActionStart,
		os.Stdout,
		os.Stderr,
		client.OptionExternalHealthd(),
	)
	require.NoError(t, err)

	args := []string{"-f", compose, "list", "--format", "json"}
	stdout, _, err := runIncusCommand(ctx, t, projectName, args...)
	require.NoError(t, err)
	snapshotter.SnapshotT(t, stripListOutput(t, stdout))
}
