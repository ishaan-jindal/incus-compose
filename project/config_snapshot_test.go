package project

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

// ConfigTestCase represents a single config snapshot test case.
type ConfigTestCase struct {
	Name     string
	Fixture  string
	Format   string
	Services []string
	Profiles []string
	EnvFiles []string
}

// newSnapshotter returns a cupaloy config writing to the project snapshot dir.
func newSnapshotter() *cupaloy.Config {
	return cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "test", "snapshots", "project")))
}

func runConfigTest(t *testing.T, tc ConfigTestCase) {
	t.Helper()

	t.Run(tc.Name, func(t *testing.T) {
		t.Parallel()

		fixture := fixturePath(tc.Fixture)

		loadOpts := []LoadOption{
			LoadWorkingDir(filepath.Dir(fixture)),
		}

		files := []string{fixture}
		if tc.Fixture != "" {
			fDir := filepath.Dir(fixture)
			fExt := filepath.Ext(fixture)
			fNoExt := strings.TrimSuffix(filepath.Base(fixture), fExt)
			incusCFile := filepath.Join(fDir, fNoExt+".incus"+fExt)
			if _, err := os.Stat(incusCFile); err == nil {
				files = append(files, incusCFile)
			}
		}
		loadOpts = append(loadOpts, LoadFiles(files))

		if len(tc.Profiles) > 0 {
			loadOpts = append(loadOpts, LoadProfiles(tc.Profiles))
		}

		if len(tc.EnvFiles) > 0 {
			absEnvFiles := make([]string, len(tc.EnvFiles))
			for i, f := range tc.EnvFiles {
				absEnvFiles[i] = filepath.Join(filepath.Dir(fixture), f)
			}
			loadOpts = append(loadOpts, LoadEnvFiles(absEnvFiles))
		}

		proj, err := New().Load(t.Context(), loadOpts...)
		require.NoError(t, err)

		// Filter services if specified.
		if len(tc.Services) > 0 {
			keep := make(map[string]bool)
			for _, name := range tc.Services {
				if svc, ok := proj.Services[name]; ok {
					keep[name] = true
					for depName := range svc.DependsOn {
						if _, ok := proj.Services[depName]; ok {
							keep[depName] = true
						}
					}
				}
			}
			// Rebuild services map with only filtered services.
			for name := range proj.Services {
				if !keep[name] {
					delete(proj.Services, name)
				}
			}
		}

		var output string
		format := tc.Format
		if format == "" {
			format = "yaml"
		}

		switch format {
		case "json":
			var buf bytes.Buffer
			encoder := json.NewEncoder(&buf)
			encoder.SetIndent("", "  ")
			require.NoError(t, encoder.Encode(proj.Project))
			output = strings.TrimSuffix(buf.String(), "\n")
		case "yaml":
			var buf bytes.Buffer
			encoder := yaml.NewEncoder(&buf)
			encoder.SetIndent(2)
			require.NoError(t, encoder.Encode(proj.Project))
			require.NoError(t, encoder.Close())
			output = strings.TrimSuffix(buf.String(), "\n")
		default:
			t.Fatalf("unsupported format: %s", format)
		}

		// Normalize paths for portability.
		absFixturePath, _ := filepath.Abs(filepath.Dir(fixture))
		output = strings.ReplaceAll(output, absFixturePath, "$FIXTURE_PATH")

		newSnapshotter().SnapshotT(t, output)
	})
}

func TestConfigSnapshots(t *testing.T) {
	t.Parallel()

	testCases := []ConfigTestCase{
		{
			Name:    "dev-environment_yaml",
			Fixture: "dev-environment/compose.yaml",
		},
		{
			Name:    "grafana_yaml",
			Fixture: "grafana/compose.yaml",
		},
		{
			Name:    "simple-nginx_yaml",
			Fixture: "simple-nginx/compose.yaml",
		},
		{
			Name:    "simple-nginx_json",
			Fixture: "simple-nginx/compose.yaml",
			Format:  "json",
		},
		{
			Name:    "test-external-network_yaml",
			Fixture: "test-external-network/compose.yaml",
		},
		{
			Name:    "two-services_yaml",
			Fixture: "two-services/compose.yaml",
		},
		{
			Name:    "nginx-proxy_yaml",
			Fixture: "nginx-proxy/compose.yaml",
		},
		{
			Name:    "nginx-scale_yaml",
			Fixture: "nginx-scale/compose.yaml",
		},
		{
			Name:    "with-bind-mounts_yaml",
			Fixture: "with-bind-mounts/compose.yaml",
		},
		{
			Name:    "with-docker-compose_yaml",
			Fixture: "with-docker-compose/docker-compose.yaml",
		},
		{
			Name:    "with-env_yaml",
			Fixture: "with-env/compose.yaml",
		},
		{
			Name:    "with-labels_yaml",
			Fixture: "with-labels/compose.yaml",
		},
		{
			Name:    "with-tmpfs_yaml",
			Fixture: "with-tmpfs/compose.yaml",
		},
		{
			Name:    "with-resources_yaml",
			Fixture: "with-resources/compose.yaml",
		},
		{
			Name:    "with-network-ranges_yaml",
			Fixture: "with-network-ranges/compose.yaml",
		},
		{
			Name:    "with-ports_yaml",
			Fixture: "with-ports/compose.yaml",
		},
		{
			Name:    "with-profiles_yaml",
			Fixture: "with-profiles/compose.yaml",
		},
		{
			Name:    "with-project-options",
			Fixture: "with-project-options/compose.yaml",
		},
		{
			Name:    "with-build_yaml",
			Fixture: "with-build/compose.yaml",
		},
		{
			Name:    "with-container-name_yaml",
			Fixture: "with-container-name/compose.yaml",
		},
		{
			Name:    "with-seeded-bind-mounts_yaml",
			Fixture: "with-seeded-bind-mounts/compose.yaml",
		},
		{
			Name:    "with-shm-size_yaml",
			Fixture: "with-shm-size/compose.yaml",
		},
		{
			Name:    "wordpress_yaml",
			Fixture: "wordpress/compose.yaml",
		},
		{
			Name:     "wordpress_filter_by_service",
			Fixture:  "wordpress/compose.yaml",
			Services: []string{"wordpress"},
		},
	}

	for _, tc := range testCases {
		runConfigTest(t, tc)
	}
}

func TestConfigSnapshotsWithProfiles(t *testing.T) {
	t.Parallel()

	testCases := []ConfigTestCase{
		{
			Name:     "with-profiles_dev_profile",
			Fixture:  "with-profiles/compose.yaml",
			Profiles: []string{"dev"},
		},
		{
			Name:     "with-profiles_monitoring_profile",
			Fixture:  "with-profiles/compose.yaml",
			Profiles: []string{"monitoring"},
		},
		{
			Name:     "with-profiles_dev_and_monitoring",
			Fixture:  "with-profiles/compose.yaml",
			Profiles: []string{"dev", "monitoring"},
		},
		{
			Name:     "dev-environment_debug_profile",
			Fixture:  "dev-environment/compose.yaml",
			Profiles: []string{"debug"},
		},
	}

	for _, tc := range testCases {
		runConfigTest(t, tc)
	}
}

func TestConfigSnapshotsWithEnv(t *testing.T) {
	t.Parallel()

	testCases := []ConfigTestCase{
		{
			Name:    "with-env_default_yaml",
			Fixture: "with-env/compose.yaml",
		},
		{
			Name:     "with-env_production_yaml",
			Fixture:  "with-env/compose.yaml",
			EnvFiles: []string{"production.env"},
		},
		{
			Name:     "with-env_staging_yaml",
			Fixture:  "with-env/compose.yaml",
			EnvFiles: []string{"staging.env"},
		},
	}

	for _, tc := range testCases {
		runConfigTest(t, tc)
	}
}
