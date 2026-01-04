package client

// mockResource is a minimal resource for testing groupByPriority.
type mockResource struct {
	name     string
	kind     Kind
	priority int
	ensured  bool
	created  bool
}

func newMockResource(name string, kind Kind, priority int, ensured bool) *mockResource {
	if name == "" {
		name = "mock-resource"
	}

	if kind == "" {
		kind = KindProfile
	}

	if priority == 0 {
		priority = PriorityProfile
	}

	return &mockResource{name: name, kind: kind, priority: priority, ensured: ensured}
}

func (m *mockResource) Name() string      { return m.name }
func (m *mockResource) IncusName() string { return m.name }
func (m *mockResource) Kind() Kind        { return m.kind }
func (m *mockResource) Priority() int     { return m.priority }
func (m *mockResource) IsEnsured() bool   { return m.ensured }
func (m *mockResource) Created() bool     { return m.created }

var _ Resource = (*mockResource)(nil)
