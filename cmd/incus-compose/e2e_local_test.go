package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/suite"
)

type E2ELocalSuite struct {
	suite.Suite
	ctx    context.Context
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func TestLocalSuite(t *testing.T) {
	suite.Run(t, new(E2ELocalSuite))
}

func (s *E2ELocalSuite) SetupSuite() {
	s.ctx = context.Background()
	s.stdout = &bytes.Buffer{}
	s.stderr = &bytes.Buffer{}
}

func (s *E2ELocalSuite) run(args ...string) error {
	s.stdout.Reset()
	s.stderr.Reset()
	cmd := newRootCommand()
	cmd.Writer = s.stdout
	cmd.ErrWriter = s.stderr
	return cmd.Run(s.ctx, append([]string{"incus-compose", "--debug"}, args...))
}

func (s *E2ELocalSuite) TestConfigCommand() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "simple-nginx yaml",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "simple-nginx json",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "config", "--format", "json"},
			wantErr: false,
		},
		{
			name:    "wordpress",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-restart",
			args:    []string{"-f", "../../test/fixtures/with-restart/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-incus-options",
			args:    []string{"-f", "../../test/fixtures/with-incus-options/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-incus-options",
			args:    []string{"-f", "../../test/fixtures/with-project-options/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-build",
			args:    []string{"-f", "../../test/fixtures/with-build/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "nonexistent file",
			args:    []string{"-f", "nonexistent.yaml", "config"},
			wantErr: true,
		},
		{
			name:    "invalid yaml",
			args:    []string{"-f", "../../test/fixtures/invalid/compose.yaml", "config"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			err := s.run(tt.args...)
			if tt.wantErr {
				s.Error(err)
			} else {
				s.NoError(err)
			}
		})
	}
}

func (s *E2ELocalSuite) TestConfigFilterByService() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "wordpress filter db service",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "db"},
			wantErr: false,
		},
		{
			name:    "wordpress filter wordpress service includes db dependency",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "wordpress"},
			wantErr: false,
		},
		{
			name:    "config --services list",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--services"},
			wantErr: false,
		},
		{
			name:    "config --volumes list",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--volumes"},
			wantErr: false,
		},
		{
			name:    "config --quiet validation",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--quiet"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			err := s.run(tt.args...)
			if tt.wantErr {
				s.Error(err)
			} else {
				s.NoError(err)
			}
		})
	}
}
