package client

import (
	"testing"

	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// SanitizeInstanceName Tests
// ----------------------------------------------------------------------------

func TestSanitizeInstanceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		input             string
		expected          string
		checkHashFallback bool
	}{
		{
			name:     "simple name",
			input:    "web",
			expected: "web",
		},
		{
			name:     "underscore replacement",
			input:    "my_service",
			expected: "my-service",
		},
		{
			name:     "uppercase to lowercase",
			input:    "MyService",
			expected: "myservice",
		},
		{
			name:     "special characters",
			input:    "my service!",
			expected: "my-service",
		},
		{
			name:              "very long name uses hash",
			input:             "this-is-a-very-long-service-name-that-exceeds-the-63-character-limit-for-incus-instances",
			checkHashFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := SanitizeIncusName(tt.input, -1)

			if tt.checkHashFallback {
				require.Len(t, result, 32)
				require.Regexp(t, "^[0-9a-f]{32}$", result)
			} else {
				require.Equal(t, tt.expected, result)
			}
			require.LessOrEqual(t, len(result), MaxIncusNameLen)
		})
	}
}

// TestInstanceConfigPatchOnlyTouchesNamedKeys pins the semantics
// SetHealthCheckingStopped relies on.
func TestInstanceConfigPatchOnlyTouchesNamedKeys(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := t.Context()
	c := newRandomTestClient(ctx, t, "patch-config-")

	imageResource, err := c.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
	require.NoError(t, err)
	image, ok := imageResource.(*Image)
	require.True(t, ok)

	instRes, err := c.Resource(KindInstance, "web", &InstanceConfig{
		Image: image.Name(),
		Extensions: map[string]string{
			HealthStatusKey:             HealthStatusUnknown,
			HealthStoppedKey:            "true",
			HealthKeyPrefix + "restart": "unless-stopped",
			"user.keep.me":              "untouched",
		},
	})
	require.NoError(t, err)
	inst, ok := instRes.(*Instance)
	require.True(t, ok)

	stack := NewStack(c)
	stack.Add(image, inst)
	require.NoError(t, stack.ForAction(ActionEnsure).Run(ctx, ActionEnsure, OptionCreate()))
	require.True(t, inst.IsEnsured())

	conn, err := c.Connection()
	require.NoError(t, err)

	before, _, err := conn.GetInstance(inst.IncusName())
	require.NoError(t, err)
	require.NotEmpty(t, before.Description, "fixture needs a description to detect wiping")
	require.NotEmpty(t, before.Devices, "fixture needs devices to detect wiping")

	// Stand in for ic-healthd writing its own key.
	info, err := conn.GetConnectionInfo()
	require.NoError(t, err)
	_, _, err = conn.RawQuery("PATCH", incusApi.NewURL().
		Path("1.0", "instances", inst.IncusName()).
		Project(info.Project).
		Target(info.Target).
		String(), instanceConfigPatch{
		Config: map[string]string{HealthStatusKey: HealthStatusHealthy},
	}, "")
	require.NoError(t, err)

	require.NoError(t, inst.SetHealthCheckingStopped(ctx, false))

	got, _, err := conn.GetInstance(inst.IncusName())
	require.NoError(t, err)

	require.Equal(t, "false", got.Config[HealthStoppedKey], "the key we asked for must be written")
	require.Equal(t, HealthStatusHealthy, got.Config[HealthStatusKey], "another writer's key must survive")
	require.Equal(t, "unless-stopped", got.Config[HealthKeyPrefix+"restart"])
	require.Equal(t, "untouched", got.Config["user.keep.me"])
	require.Equal(t, before.Description, got.Description, "PATCH must not wipe the description")
	require.Equal(t, before.Devices, got.Devices, "PATCH must not wipe devices")
	require.Equal(t, before.Profiles, got.Profiles, "PATCH must not wipe profiles")
	require.Equal(t, before.Architecture, got.Architecture, "PATCH must not wipe the architecture")
}
