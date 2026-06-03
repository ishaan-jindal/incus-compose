package main

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type UpCommandSuite struct {
	suite.Suite
}

func (s *UpCommandSuite) TestParseScale() {
	tests := []struct {
		name   string
		values []string
		want   map[string]int
	}{
		{name: "empty", values: nil, want: map[string]int{}},
		{name: "single", values: []string{"web=3"}, want: map[string]int{"web": 3}},
		{name: "multiple", values: []string{"web=3", "api=2"}, want: map[string]int{"web": 3, "api": 2}},
		{name: "invalid ignored", values: []string{"web", "api=bad", "db=1"}, want: map[string]int{"db": 1}},
		{name: "last wins", values: []string{"web=2", "web=4"}, want: map[string]int{"web": 4}},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, parseScale(tt.values))
		})
	}
}

func TestUpCommandSuite(t *testing.T) {
	suite.Run(t, new(UpCommandSuite))
}
