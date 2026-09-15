//go:build airplay_runtime_embedded

package airplay

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
)

var errEmbeddedRuntimeUnavailable = errors.New("embedded AirPlay runtime payload is unavailable")

// The release build copies the pinned archive and its descriptor into this
// directory immediately before invoking go build. They stay out of source
// control because the archive contains third-party runtime binaries.
//
//go:embed payload/runtime.zip payload/runtime-bootstrap.json
var embeddedRuntimeFiles embed.FS

func embeddedRuntimeAvailable() bool { return true }

func loadEmbeddedRuntimeArtifact() (RuntimeArtifactDescriptor, error) {
	data, err := embeddedRuntimeFiles.ReadFile("payload/runtime-bootstrap.json")
	if err != nil {
		return RuntimeArtifactDescriptor{}, err
	}
	var bootstrap runtimeBootstrapDescriptor
	if err := json.Unmarshal(data, &bootstrap); err != nil {
		return RuntimeArtifactDescriptor{}, err
	}
	if bootstrap.Schema != 1 {
		return RuntimeArtifactDescriptor{}, errors.New("embedded AirPlay runtime bootstrap schema is unsupported")
	}
	if err := bootstrap.RuntimeArtifactDescriptor.validate(false); err != nil {
		return RuntimeArtifactDescriptor{}, err
	}
	return bootstrap.RuntimeArtifactDescriptor, nil
}

func openEmbeddedRuntimeArchive() (io.ReadCloser, RuntimeArtifactDescriptor, error) {
	artifact, err := loadEmbeddedRuntimeArtifact()
	if err != nil {
		return nil, RuntimeArtifactDescriptor{}, err
	}
	file, err := embeddedRuntimeFiles.Open("payload/runtime.zip")
	if err != nil {
		return nil, RuntimeArtifactDescriptor{}, err
	}
	return file, artifact, nil
}
