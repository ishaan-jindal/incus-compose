package client

import (
	"testing"

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
