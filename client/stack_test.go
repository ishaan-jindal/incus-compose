package client

import (
	"context"
	"fmt"
	"testing"

	"github.com/lxc/incus/v7/shared/cliconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// TestGroupByKind tests the batch grouping logic without Incus.
func TestGroupByKind(t *testing.T) {
	tests := []struct {
		name        string
		tasks       []Resource
		wantBatches int
		wantSizes   []int
	}{
		{
			name:        "empty tasks",
			tasks:       []Resource{},
			wantBatches: 0,
			wantSizes:   nil,
		},
		{
			name: "single task",
			tasks: []Resource{
				newMockResource("a", "", 0, false),
			},
			wantBatches: 1,
			wantSizes:   []int{1},
		},
		{
			name: "same kind groups together",
			tasks: []Resource{
				newMockResource("a", "", 0, false),
				newMockResource("b", "", 0, false),
				newMockResource("c", "", 0, false),
			},
			wantBatches: 1,
			wantSizes:   []int{3},
		},
		{
			name: "different kinds create separate batches",
			tasks: []Resource{
				newMockResource("profile", KindProfile, 0, false),
				newMockResource("volume", KindStorageVolume, 0, false),
				newMockResource("instance", KindInstance, 0, false),
			},
			wantBatches: 3,
			wantSizes:   []int{1, 1, 1},
		},
		{
			name: "mixed kinds with multiple per batch",
			tasks: []Resource{
				newMockResource("profile", KindProfile, 0, false),
				newMockResource("image", KindImage, 0, false),
				newMockResource("image2", KindImage, 0, false),
				newMockResource("volume", KindStorageVolume, 0, false),
				newMockResource("volume2", KindStorageVolume, 0, false),
				newMockResource("instance", KindInstance, 0, false),
			},
			wantBatches: 4,
			wantSizes:   []int{1, 2, 2, 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stack := NewStack(nil)
			stack.Add(tc.tasks...)

			batches := stack.groupByKind()

			require.Len(t, batches, tc.wantBatches)

			if tc.wantSizes != nil {
				for i, size := range tc.wantSizes {
					assert.Len(t, batches[i], size, "batch %d should have %d tasks", i, size)
				}
			}
		})
	}
}

// TestAddDeduplicatesSamePointer is a regression test for the "Alias already exists"
// race: two services sharing the same image resolve to the same Resource pointer via
// Client.Resource(), but Stack.Add used to append it twice, causing parallel Ensure
// calls on the same object.
func TestAddDeduplicatesSamePointer(t *testing.T) {
	r := newMockResource("nginx", KindImage, PriorityImage, false)

	stack := NewStack(nil)
	stack.Add(r, r) // same pointer twice, as mkUpStack does for shared images

	assert.Len(t, stack.resources, 1, "same resource added twice must appear only once")
}

// TestParallelImageDownload verifies multiple images download in parallel.
// Uses tiny busybox variants to minimize bandwidth.
func TestParallelImageDownload(t *testing.T) {
	ctx := context.Background()

	client, err := NewTestClient(ctx)
	if err != nil {
		t.Skipf("Skipping: %v", err)
		return
	}

	// Load CLI config for image server resolution
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		t.Skipf("Skipping: failed to load config: %v", err)
		return
	}
	if _, err := conf.GetImageServer("docker.io"); err != nil {
		t.Skipf("Skipping: docker.io not configured: %v", err)
		return
	}

	// Create test project
	c, err := createProjectClient(client, "parallel-image-test")
	if err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}
	defer func() { _ = client.DeleteProject("parallel-image-test", true) }()

	// Use busybox with different tags - tiny images (~2MB each)
	imageNames := []string{
		"docker.io/library/busybox:1.36",
		"docker.io/library/busybox:1.35",
		"docker.io/library/busybox:1.34",
	}

	stack := NewStack(c, StackWorkers(3))
	for _, name := range imageNames {
		img, err := c.Resource(KindImage, name, &ImageConfig{})
		require.NoError(t, err)

		stack.Add(img)
	}

	// Verify all images are in same batch (same priority)
	batches := stack.groupByKind()
	assert.Len(t, batches, 1, "all images should be in one batch")
	assert.Len(t, batches[0], 3, "batch should have 3 images")

	// Run with parallelism
	err = stack.Run(ctx, ActionEnsure, OptionCreate())
	assert.NoError(t, err)

	// Verify all images ensured
	for _, name := range imageNames {
		img, err := c.Resource(KindImage, name, &ImageConfig{})
		require.NoError(t, err)

		assert.True(t, img.IsEnsured(), "image %s should be ensured", name)
	}

	t.Logf("Successfully downloaded %d images in parallel", len(imageNames))
}

// StackTestSuite tests Stack operations against a real Incus instance.
type StackTestSuite struct {
	suite.Suite
	ctx          context.Context
	globalClient *GlobalClient
	client       *Client

	incusConfig *cliconfig.Config
}

// SetupSuite runs once before all tests.
func (s *StackTestSuite) SetupSuite() {
	s.ctx = context.Background()

	client, err := NewTestClient(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping tests: %v", err)
		return
	}
	s.globalClient = client

	// Load Incus CLI config to get image server
	conf, err := cliconfig.LoadConfig("")
	s.Require().NoError(err, "Failed to load incus config")

	s.incusConfig = conf
}

// SetupTest runs before each test.
func (s *StackTestSuite) SetupTest() {
	client, err := createProjectClient(s.globalClient, "stack-test")
	s.Require().NoError(err, "Failed to create the stack-test project")

	s.client = client
}

func (s *StackTestSuite) TearDownTest() {
	_ = s.globalClient.DeleteProject("stack-test", true)
}

// TestHooksWithStack tests that hooks are called during Stack.Run.
func (s *StackTestSuite) TestHooksWithStack() {
	// Track hook calls
	var beforeCalled, afterCalled bool
	var afterErr error

	s.client.AddHookBefore(func(_ context.Context, action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindProfile {
			if _, ok := r.(*Profile); ok {
				beforeCalled = true
			}
		}

		return err
	})

	s.client.AddHookAfter(func(_ context.Context, action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindProfile {
			if _, ok := r.(*Profile); ok {
				afterCalled = true
				afterErr = err
			}
		}

		return err
	})

	// Create stack and run
	stack := NewStack(s.client)
	profile, err := s.client.Resource(KindProfile, "test-hooks-stack", &ProfileConfig{})
	s.Require().NoError(err)

	stack.Add(profile)
	err = stack.Run(s.ctx, ActionEnsure, OptionCreate())

	s.NoError(err)
	s.True(beforeCalled, "before hook should be called")
	s.True(afterCalled, "after hook should be called")
	s.NoError(afterErr, "after hook should receive nil error")
}

// TestErrorAggregation verifies errors from multiple tasks are aggregated.
func (s *StackTestSuite) TestErrorAggregation() {
	stack := NewStack(s.client)

	p1, err := s.client.Resource(KindProfile, "error-test-1", &ProfileConfig{})
	s.Require().NoError(err)

	p2, err := s.client.Resource(KindProfile, "error-test-2", &ProfileConfig{})
	s.Require().NoError(err)

	stack.Add(p1, p2)

	err = stack.Run(s.ctx, ActionEnsure)

	s.Require().Error(err)
	s.Contains(err.Error(), "error-test-1")
	s.Contains(err.Error(), "error-test-2")
}

func (s *StackTestSuite) TestInstanceWithSecrets() {
	network, err := s.client.Resource(KindNetwork, "default", &NetworkConfig{})
	s.Require().NoError(err)

	imageResource, err := s.client.Resource(KindImage, "docker.io/alpine:latest", &ImageConfig{})
	s.Require().NoError(err)

	image, ok := imageResource.(*Image)
	s.Require().True(ok)

	devices := []InstanceDevice{
		{
			Name: "eth0",
			Config: InstanceDeviceConfig{
				DeviceType: InstanceDeviceTypeNic,
				Network:    network,
			},
		},
	}

	secrets := []InstanceSecret{
		{
			Source:  "db_password",
			Content: []byte("super-secret-password"),
		},
		{
			Source:  "api_key",
			Target:  "/app/secrets/api.key",
			Content: []byte("my-api-key-value"),
			UID:     0,
			GID:     0,
			Mode:    0o440,
		},
	}

	instance, err := s.client.Resource(KindInstance, "app-with-secrets", &InstanceConfig{
		Image:   image.Name(),
		Devices: devices,
		Secrets: secrets,
	})
	s.Require().NoError(err)

	stack := NewStack(s.client)
	stack.Add(network, image, instance)

	ensureStack := stack.ForAction(ActionEnsure)
	s.Require().NoError(ensureStack.Run(s.ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		s.Require().True(r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
	s.Require().NoError(stack.ForAction(ActionStart).Run(s.ctx, ActionStart))
	s.Require().NoError(stack.ForAction(ActionStop).Run(s.ctx, ActionStop, OptionForce()))
}

func (s *StackTestSuite) TestEnsureWithoutCreateFailsForNonExistent() {
	profile, err := s.client.Resource(KindProfile, "p1", &ProfileConfig{})
	s.Require().NoError(err)

	stack := NewStack(s.client)
	stack.Add(profile)
	s.Require().Error(stack.ForAction(ActionEnsure).Run(s.ctx, ActionEnsure))
}

func (s *StackTestSuite) TestSingleProfileEnsure() {
	profile, err := s.client.Resource(KindProfile, "p1", &ProfileConfig{})
	s.Require().NoError(err)

	stack := NewStack(s.client)
	stack.Add(profile)

	ensureStack := stack.ForAction(ActionEnsure)
	s.Require().NoError(ensureStack.Run(s.ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		s.Require().True(r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
}

func (s *StackTestSuite) TestProfileAndNetworkMixedPriorities() {
	profile, err := s.client.Resource(KindProfile, "p1", &ProfileConfig{})
	s.Require().NoError(err)

	network, err := s.client.Resource(KindNetwork, "n1", &NetworkConfig{})
	s.Require().NoError(err)

	stack := NewStack(s.client)
	stack.Add(profile, network)

	ensureStack := stack.ForAction(ActionEnsure)
	s.Require().NoError(ensureStack.Run(s.ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		s.Require().True(r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
}

func (s *StackTestSuite) TestSimpleNginx() {
	network, err := s.client.Resource(KindNetwork, "default", &NetworkConfig{})
	s.Require().NoError(err)

	imageResource, err := s.client.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
	s.Require().NoError(err)

	image, ok := imageResource.(*Image)
	s.Require().True(ok)

	devices := []InstanceDevice{
		{
			Name: "eth0",
			Config: InstanceDeviceConfig{
				DeviceType: InstanceDeviceTypeNic,
				Network:    network,
			},
		},
	}

	instance, err := s.client.Resource(KindInstance, "web", &InstanceConfig{
		Image:   image.Name(),
		Devices: devices,
	})
	s.Require().NoError(err)

	stack := NewStack(s.client)
	stack.Add(network, image, instance)

	ensureStack := stack.ForAction(ActionEnsure)
	s.Require().NoError(ensureStack.Run(s.ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		s.Require().True(r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
	s.Require().NoError(stack.ForAction(ActionStart).Run(s.ctx, ActionStart))
	s.Require().NoError(stack.ForAction(ActionStop).Run(s.ctx, ActionStop, OptionForce()))
}

func (s *StackTestSuite) TestNginxScale() {
	network, err := s.client.Resource(KindNetwork, "default", &NetworkConfig{})
	s.Require().NoError(err)

	imageResource, err := s.client.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
	s.Require().NoError(err)

	image, ok := imageResource.(*Image)
	s.Require().True(ok)

	devices := []InstanceDevice{
		{
			Name: "eth0",
			Config: InstanceDeviceConfig{
				DeviceType: InstanceDeviceTypeNic,
				Network:    network,
			},
		},
	}

	resources := []Resource{network, image}

	// Create 3 scaled instances: web-1, web-2, web-3
	for i := 1; i <= 3; i++ {
		instance, err := s.client.Resource(KindInstance, fmt.Sprintf("web-%d", i), &InstanceConfig{
			Image:   image.Name(),
			Devices: devices,
		})
		s.Require().NoError(err)

		resources = append(resources, instance)
	}

	stack := NewStack(s.client)
	stack.Add(resources...)

	ensureStack := stack.ForAction(ActionEnsure)
	s.Require().NoError(ensureStack.Run(s.ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		s.Require().True(r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
	s.Require().NoError(stack.ForAction(ActionStart).Run(s.ctx, ActionStart))
	s.Require().NoError(stack.ForAction(ActionStop).Run(s.ctx, ActionStop, OptionForce()))
}

// TestStackSuite runs the test suite.
func TestStackSuite(t *testing.T) {
	suite.Run(t, new(StackTestSuite))
}
