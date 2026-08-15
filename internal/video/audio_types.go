package video

const MaxMediaSourceBytes int64 = 1<<32 - 1

type SourceKind string

const (
	SourceSoundCloud  SourceKind = "soundcloud"
	SourceMusic       SourceKind = "music"
	SourceLocalAudio  SourceKind = "local_audio"
	SourceRemoteAudio SourceKind = "remote_audio"
)

type MediaClass string

const (
	MediaUnsupported MediaClass = "unsupported"
	MediaAudio       MediaClass = "audio"
	MediaVideo       MediaClass = "video"
)

type AudioMetadata struct {
	Title    string
	Artist   string
	Album    string
	Uploader string
}

type MediaStream struct {
	Index       int
	CodecType   string
	CodecName   string
	AttachedPic bool
	Width       int
	Height      int
	FieldOrder  string
	Tags        map[string]string
}

type MediaProbe struct {
	Streams    []MediaStream
	Duration   float64
	FormatTags map[string]string
}

type AudioFeatures struct {
	BPM               float64
	IntegratedLUFS    float64
	LowFrequencyRatio float64
	SpectralCentroid  float64
	Fingerprint64     [64]float64
	LoudnessEnvelope  [1000]float64
}

type AudioFrame struct {
	Spectrum24 [24]float64
}

type AudioAnalysis struct {
	FPS      int
	Duration float64
	Frames   []AudioFrame
	// PCMInterleavedS16 is the same resampled stereo PCM stream used to derive
	// spectrum and waveform frames. It is retained for the canonical GPU scene
	// handoff so the shader receives real audio windows rather than a fixture.
	PCMInterleavedS16 []int16
	// WaveformFrames contains the bounded Q16 peak envelope generated from
	// the same decoded PCM stream as Frames. Each entry corresponds to one
	// video tick and is kept separate from AudioFrame for wire compatibility.
	WaveformFrames [][]uint16
	Features       AudioFeatures
}

type FontSet struct {
	Regular400  string
	Medium500   string
	SemiBold600 string
}

type ArtworkCandidate struct {
	Path       string
	FrontCover bool
	Width      int
	Height     int
	Bytes      int64
}

type AcquiredAudio struct {
	SourcePath                string
	SourceName                string
	Kind                      SourceKind
	Probe                     MediaProbe
	EmbeddedMetadata          AudioMetadata
	SoundCloudMetadata        AudioMetadata
	EmbeddedArtwork           []ArtworkCandidate
	SoundCloudArtworkPath     string
	SoundCloudInformationPath string
}

type AudioRenderInput struct {
	SourcePath      string
	Kind            SourceKind
	Metadata        AudioMetadata
	ArtworkPath     string
	BaseTexture     *BaseTextureMetadata
	WaveformTexture *BaseTextureMetadata
	TextOverlay     *TextOverlayMetadata
	Analysis        AudioAnalysis
}
