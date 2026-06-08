package client

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type ParsePercentSuite struct {
	suite.Suite
}

func TestParsePercentSuite(t *testing.T) {
	suite.Run(t, new(ParsePercentSuite))
}

func (s *ParsePercentSuite) TestParsePercent() {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"native rootfs", "rootfs: 42% (3.10MB/s)", 42},
		{"native complete", "metadata: 100% (876B/s)", 100},
		{"leading zero progress", "rootfs: 0% (0B/s)", 0},
		{"oci status text", "Retrieving OCI image from registry", -1},
		{"oci tarball bytes", "Generating rootfs tarball: 12.3MB (4.1MB/s)", -1},
		{"empty", "", -1},
		{"bare percent sign", "%", -1},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, parsePercent(tt.in))
		})
	}
}
