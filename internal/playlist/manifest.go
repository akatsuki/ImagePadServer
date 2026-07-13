package playlist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"imagepadserver/internal/about"
	"imagepadserver/internal/video"
)

const (
	PlaylistManifestSchemaVersion = 1
	PlaylistManifestFileName      = "playlist.manifest.json"
)

type BuildDiagnostics struct {
	AppName      string `json:"appName"`
	AppVersion   string `json:"appVersion"`
	BuildVersion string `json:"buildVersion"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
}

type GeneratorDiagnostics struct {
	Component string `json:"component"`
	Encoder   string `json:"encoder,omitempty"`
}

type ManifestTrack struct {
	Track                   Track                           `json:"track"`
	MediaSHA256             string                          `json:"mediaSha256,omitempty"`
	SourceSHA256            string                          `json:"sourceSha256,omitempty"`
	ObservedEncoding        video.RadioAssetSpec            `json:"observedEncoding"`
	EncodingContract        video.RadioEncodingContract     `json:"encodingContract"`
	EncodingFingerprint     string                          `json:"encodingFingerprint"`
	RenderRecipeContract    video.AssetRenderRecipeContract `json:"renderRecipeContract"`
	RenderRecipeFingerprint string                          `json:"renderRecipeFingerprint"`
	RenderContentValues     video.AssetRenderContentValues  `json:"renderContentValues"`
	RegenerationState       string                          `json:"regenerationState"`
	RegenerationReason      string                          `json:"regenerationReason,omitempty"`
}

type RegenerationDiagnostics struct {
	NeedsRegenerationCount int      `json:"needsRegenerationCount"`
	IncompatibleCount      int      `json:"incompatibleCount"`
	Reasons                []string `json:"reasons,omitempty"`
}

type PlaylistManifest struct {
	Digest                  string                          `json:"digest"`
	SchemaVersion           int                             `json:"schemaVersion"`
	PlaylistID              string                          `json:"playlistId"`
	CreatedAt               time.Time                       `json:"createdAt"`
	UpdatedAt               time.Time                       `json:"updatedAt"`
	CreatedWith             BuildDiagnostics                `json:"createdWith"`
	UpdatedWith             BuildDiagnostics                `json:"updatedWith"`
	GeneratedBy             GeneratorDiagnostics            `json:"generatedBy"`
	EncodingContract        video.RadioEncodingContract     `json:"encodingContract"`
	EncodingFingerprint     string                          `json:"encodingFingerprint"`
	RenderRecipeContract    video.AssetRenderRecipeContract `json:"renderRecipeContract"`
	RenderRecipeFingerprint string                          `json:"renderRecipeFingerprint"`
	RegenerationState       string                          `json:"regenerationState"`
	Diagnostics             RegenerationDiagnostics         `json:"diagnostics"`
	Tracks                  []ManifestTrack                 `json:"tracks"`
}

func (m PlaylistManifest) canonicalBytesForDigest() ([]byte, error) {
	m.Digest = ""
	return json.Marshal(m)
}

func (m PlaylistManifest) computedDigest() (string, error) {
	data, err := m.canonicalBytesForDigest()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type TrackAudit struct {
	TrackID            string `json:"trackId"`
	NeedsRegeneration  bool   `json:"needsRegeneration"`
	Incompatible       bool   `json:"incompatible"`
	RegenerationReason string `json:"regenerationReason,omitempty"`
}

type PlaylistAudit struct {
	PlaylistID             string       `json:"playlistId"`
	Name                   string       `json:"name"`
	NeedsRegeneration      bool         `json:"needsRegeneration"`
	Incompatible           bool         `json:"incompatible"`
	RegenerationReason     string       `json:"regenerationReason,omitempty"`
	Tracks                 []TrackAudit `json:"tracks"`
	NeedsRegenerationCount int          `json:"needsRegenerationCount"`
	IncompatibleCount      int          `json:"incompatibleCount"`
}

func currentBuildDiagnostics() BuildDiagnostics {
	return BuildDiagnostics{AppName: about.AppName, AppVersion: about.Version, BuildVersion: about.FileVersion, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func observedMatchesContract(spec video.RadioAssetSpec, contract video.RadioEncodingContract) (bool, string) {
	v, wantV := spec.Video, contract.Video
	a, wantA := spec.Audio, contract.Audio
	switch {
	case v.GOPFrames <= 0:
		return false, "GOP evidence missing"
	case v.Codec != wantV.Codec:
		return false, fmt.Sprintf("video codec %s != %s", v.Codec, wantV.Codec)
	case v.Width != wantV.Width || v.Height != wantV.Height:
		return false, fmt.Sprintf("resolution %dx%d != %dx%d", v.Width, v.Height, wantV.Width, wantV.Height)
	case v.FrameRate != wantV.FrameRate:
		return false, "frame rate mismatch"
	case v.PixelFormat != wantV.PixelFormat:
		return false, "pixel format mismatch"
	case v.BFrames != wantV.BFrames:
		return false, "B-frame mismatch"
	case v.GOPFrames != wantV.GOPFrames:
		return false, "GOP mismatch"
	case a.Codec != wantA.Codec:
		return false, fmt.Sprintf("audio codec %s != %s", a.Codec, wantA.Codec)
	case a.SampleRate != wantA.SampleRate:
		return false, "audio sample rate mismatch"
	case a.Channels != wantA.Channels:
		return false, "audio channel mismatch"
	}
	return true, ""
}
