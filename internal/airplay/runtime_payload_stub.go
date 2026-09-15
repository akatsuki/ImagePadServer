//go:build !airplay_runtime_embedded

package airplay

import (
	"errors"
	"io"
)

var errEmbeddedRuntimeUnavailable = errors.New("embedded AirPlay runtime payload is unavailable")

func embeddedRuntimeAvailable() bool { return false }

func loadEmbeddedRuntimeArtifact() (RuntimeArtifactDescriptor, error) {
	return RuntimeArtifactDescriptor{}, errEmbeddedRuntimeUnavailable
}

func openEmbeddedRuntimeArchive() (io.ReadCloser, RuntimeArtifactDescriptor, error) {
	return nil, RuntimeArtifactDescriptor{}, errEmbeddedRuntimeUnavailable
}
