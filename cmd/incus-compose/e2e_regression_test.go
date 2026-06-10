package main

import (
	"strings"
)

// TestExecSelectsCorrectInstance is a regression test for the exec command
// dispatching to the wrong instance when multiple services share a stack.
// It runs `hostname` in each service of a multi-service project and asserts
// the output matches the expected Incus instance name.
func (s *E2ESuite) TestExecSelectsCorrectInstance() {
	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	defer func() {
		_ = s.run("-f", compose, "down", "--project")
	}()

	s.Require().NoError(s.run("-f", compose, "up", "--detach"))

	tests := []struct {
		service  string
		wantHost string
	}{
		{"nginx", "nginx-1"},
		{"backend1", "backend1-1"},
		{"backend2", "backend2-1"},
	}

	for _, tt := range tests {
		s.Run(tt.service, func() {
			s.Require().NoError(s.run("-f", compose, "exec", "--no-tty", tt.service, "hostname"))
			s.Equal(tt.wantHost, strings.TrimSpace(s.stdout.String()))
		})
	}
}
