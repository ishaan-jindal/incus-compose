//go:build !unix || darwin

package client

import (
	"context"
	"errors"
	"io"
)

var errBuild = errors.New("not implemented: building on non-linux clients is not implemented")

// buildDetectBuilder is a stub o non-linux.
func buildDetectBuilder(preferredBuilder string) (string, error) {
	return "", errBuild
}

// buildRootfs is a stub on non-linux.
func buildRootfs(ctx context.Context, builder string, cfg *BuildConfig, stdout io.Writer, stderr io.Writer) (io.ReadCloser, []byte, error) {
	return nil, nil, errBuild
}

// buildMetadataTar is a stub on non-linux.
func buildMetadataTar(name, arch string, configJSON []byte) (io.Reader, error) {
	return nil, errBuild
}
