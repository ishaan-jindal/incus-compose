package version

import "testing"

func TestCurrent(t *testing.T) {
	if got := Current(); got != Version {
		t.Errorf("Current() = %q, want %q", got, Version)
	}

	old := Version
	Version = "v1.2.3"
	t.Cleanup(func() { Version = old })

	if got := Current(); got != "v1.2.3" {
		t.Errorf("Current() = %q, want %q", got, "v1.2.3")
	}
}
