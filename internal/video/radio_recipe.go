package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type RadioFrameRate struct {
	Numerator   int `json:"numerator"`
	Denominator int `json:"denominator"`
}

type RadioEncodingParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type RadioFilter struct {
	Name       string                   `json:"name"`
	Parameters []RadioEncodingParameter `json:"parameters,omitempty"`
}

type RadioRateControl struct {
	Mode       string `json:"mode"`
	Bitrate    string `json:"bitrate"`
	MaxRate    string `json:"maxRate"`
	BufferSize string `json:"bufferSize"`
}

type RadioVideoContract struct {
	Codec                 string           `json:"codec"`
	Width                 int              `json:"width"`
	Height                int              `json:"height"`
	FrameRate             RadioFrameRate   `json:"frameRate"`
	PixelFormat           string           `json:"pixelFormat"`
	GOPFrames             int              `json:"gopFrames"`
	MinKeyframeFrames     int              `json:"minKeyframeFrames"`
	ForcedKeyframeSeconds float64          `json:"forcedKeyframeSeconds"`
	BFrames               int              `json:"bFrames"`
	RateControl           RadioRateControl `json:"rateControl"`
}

type RadioAudioContract struct {
	Codec      string `json:"codec"`
	Bitrate    string `json:"bitrate"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
}

type RadioEncodingContract struct {
	Video             RadioVideoContract       `json:"video"`
	Audio             RadioAudioContract       `json:"audio"`
	PrivateParameters []RadioEncodingParameter `json:"privateParameters"`
}

type StreamEncodingContract = RadioEncodingContract

func (c RadioEncodingContract) Clone() RadioEncodingContract {
	clone := c
	clone.PrivateParameters = append([]RadioEncodingParameter(nil), c.PrivateParameters...)
	return clone
}

func (c RadioEncodingContract) CanonicalJSON() []byte {
	clone := c.Clone()
	sort.Slice(clone.PrivateParameters, func(i, j int) bool {
		return clone.PrivateParameters[i].Name < clone.PrivateParameters[j].Name
	})
	data, err := json.Marshal(clone)
	if err != nil {
		panic(fmt.Sprintf("marshal radio encoding contract: %v", err))
	}
	return data
}

type RadioRenderCanvas struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FrameRate   int    `json:"frameRate"`
	PixelFormat string `json:"pixelFormat"`
}
type RadioWaveformRule struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Rate        int    `json:"rate"`
	Mode        string `json:"mode"`
	ColorPolicy string `json:"colorPolicy"`
	Opacity     string `json:"opacity"`
}
type RadioOverlayRule struct {
	X int `json:"x"`
	Y int `json:"y"`
}
type RadioASSRule struct {
	Renderer      string `json:"renderer"`
	ScriptBinding string `json:"scriptBinding"`
	FontsBinding  string `json:"fontsBinding"`
}
type RadioFadeRule struct {
	EdgeSeconds            float64 `json:"edgeSeconds"`
	MinimumDurationSeconds float64 `json:"minimumDurationSeconds"`
}
type AssetRenderRecipeContract struct {
	Canvas         RadioRenderCanvas `json:"canvas"`
	AudioFilter    string            `json:"audioFilter,omitempty"`
	Waveform       RadioWaveformRule `json:"waveform"`
	Overlay        RadioOverlayRule  `json:"overlay"`
	ASS            RadioASSRule      `json:"ass"`
	Fades          RadioFadeRule     `json:"fades"`
	OrderedFilters []string          `json:"orderedFilters"`
}
type AssetRenderContentValues struct {
	DurationSeconds     float64 `json:"durationSeconds"`
	FadeEnabled         bool    `json:"fadeEnabled"`
	FadeOutStartSeconds float64 `json:"fadeOutStartSeconds,omitempty"`
	WaveColor           string  `json:"waveColor"`
}

func (c AssetRenderRecipeContract) CanonicalJSON() []byte {
	data, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return data
}
func (c AssetRenderRecipeContract) Fingerprint() string {
	sum := sha256.Sum256(c.CanonicalJSON())
	return hex.EncodeToString(sum[:])
}

type radioSemanticOptions struct {
	Stream  RadioEncodingContract
	Render  AssetRenderRecipeContract
	Content AssetRenderContentValues
}

func (c RadioEncodingContract) EncodingFingerprint() string {
	sum := sha256.Sum256(c.CanonicalJSON())
	return hex.EncodeToString(sum[:])
}

// RadioRenderRecipe contains both the semantic encoding recipe and execution
// details. Only receiver-visible fields are projected into the normalized
// contract and fingerprint.
type RadioRenderRecipe struct {
	Preset          QualityPreset
	Encoder         VideoEncoderProfile
	DeliveryProfile string
	AudioFilter     string
	DurationSeconds float64
	EdgeFadeSeconds float64

	SourcePath            string
	ASSPath               string
	FontDir               string
	OutputPath            string
	LogLevel              string
	ProgressEnabled       bool
	ThreadCount           int
	OutputTimestampOffset float64
	MuxURL                string
	AppVersion            string
}

func NewRadioRenderRecipe(preset QualityPreset, encoder VideoEncoderProfile, audioFilter string, durationSeconds float64) RadioRenderRecipe {
	if encoder.Name == "" {
		encoder = CPUVideoEncoder(EncoderLowLatency)
	}
	profile := strings.ToLower(strings.TrimSpace(preset.RadioLatency))
	if profile == "" {
		profile = "rtsp-ultra"
	}
	return RadioRenderRecipe{
		Preset: preset, Encoder: encoder, DeliveryProfile: profile,
		AudioFilter: audioFilter, DurationSeconds: durationSeconds,
		EdgeFadeSeconds: radioEdgeFadeSeconds, LogLevel: "error",
	}
}

func MusicRadioRenderRecipe(preset QualityPreset, durationSeconds float64) RadioRenderRecipe {
	return NewRadioRenderRecipe(preset, CPUVideoEncoder(EncoderLowLatency), audioLoudnormFilter(SourceMusic), durationSeconds)
}

func (r RadioRenderRecipe) NormalizedEncodingContract() RadioEncodingContract {
	return r.semanticOptions(nil).Stream
}

func (r RadioRenderRecipe) semanticOptions(mode *ForegroundMode) radioSemanticOptions {
	height := r.Preset.Height
	if height <= 0 {
		height = 720
	}
	width := height * 16 / 9
	if width%2 != 0 {
		width++
	}
	gop := legacyRadioGOPFrames(r.DeliveryProfile)
	waveW := int(math.Round(752 * float64(width) / 1280))
	waveH := int(math.Round(168 * float64(height) / 720))
	waveX := int(math.Round(432 * float64(width) / 1280))
	waveY := int(math.Round(320 * float64(height) / 720))
	waveColor := "#FFFFFF@0.55"
	if mode != nil {
		waveColor = fmt.Sprintf("#%02X%02X%02X@0.55", mode.AccentColor.R, mode.AccentColor.G, mode.AccentColor.B)
	}
	fadeEnabled := r.EdgeFadeSeconds > 0 && r.DurationSeconds > r.EdgeFadeSeconds*2
	ordered := []string{"showwaves", "overlay", "ass"}
	if r.AudioFilter != "" {
		ordered = append([]string{"audio-filter", "asplit"}, ordered...)
	}
	ordered = append(ordered, "conditional-video-fade-in", "conditional-video-fade-out", "conditional-audio-fade-in", "conditional-audio-fade-out")
	stream := RadioEncodingContract{
		Video: RadioVideoContract{
			Codec: "h264", Width: width, Height: height,
			FrameRate:   RadioFrameRate{Numerator: 30, Denominator: 1},
			PixelFormat: "yuv420p", GOPFrames: gop, MinKeyframeFrames: gop,
			ForcedKeyframeSeconds: float64(gop) / 30, BFrames: 0,
			RateControl: RadioRateControl{Mode: "constrained-vbr", Bitrate: r.Preset.VideoBitrate, MaxRate: r.Preset.MaxRate, BufferSize: r.Preset.BufferSize},
		},
		Audio:             RadioAudioContract{Codec: "aac", Bitrate: r.Preset.AudioBitrate, SampleRate: 48000, Channels: 2},
		PrivateParameters: []RadioEncodingParameter{{Name: "h264-aud", Value: "insert"}, {Name: "repeat-headers", Value: "keyframe"}},
	}
	render := AssetRenderRecipeContract{Canvas: RadioRenderCanvas{width, height, 30, "yuv420p"}, AudioFilter: r.AudioFilter, Waveform: RadioWaveformRule{waveW, waveH, waveX, waveY, 30, "line", "foreground-accent-or-white", "0.55"}, Overlay: RadioOverlayRule{waveX, waveY}, ASS: RadioASSRule{"libass", "track-ass-path", "font-directory"}, Fades: RadioFadeRule{r.EdgeFadeSeconds, r.EdgeFadeSeconds * 2}, OrderedFilters: ordered}
	content := AssetRenderContentValues{DurationSeconds: r.DurationSeconds, FadeEnabled: fadeEnabled, WaveColor: waveColor}
	if fadeEnabled {
		content.FadeOutStartSeconds = r.DurationSeconds - r.EdgeFadeSeconds
	}
	return radioSemanticOptions{Stream: stream, Render: render, Content: content}
}

func (r RadioRenderRecipe) StreamEncodingContract() RadioEncodingContract {
	return r.semanticOptions(nil).Stream
}
func (r RadioRenderRecipe) StreamFingerprint() string {
	return r.StreamEncodingContract().EncodingFingerprint()
}
func (r RadioRenderRecipe) AssetRenderRecipeContract() AssetRenderRecipeContract {
	return r.semanticOptions(nil).Render
}
func (r RadioRenderRecipe) AssetRenderFingerprint() string {
	return r.AssetRenderRecipeContract().Fingerprint()
}
func (r RadioRenderRecipe) AssetRenderContentValues() AssetRenderContentValues {
	return r.semanticOptions(nil).Content
}

func (r RadioRenderRecipe) CanonicalEncodingJSON() []byte {
	return r.NormalizedEncodingContract().CanonicalJSON()
}

func (r RadioRenderRecipe) EncodingFingerprint() string {
	return r.NormalizedEncodingContract().EncodingFingerprint()
}

// FFmpegArgs is the only radio-MP4 encode argument builder. Paths and encoder
// implementation details affect execution but are excluded from identity.
func (r RadioRenderRecipe) FFmpegArgs(mode *ForegroundMode) []string {
	o := r.semanticOptions(mode)
	c, content := o.Stream, o.Content
	wavesInput, audioMap, prefix := "1:a", "1:a", ""
	if o.Render.AudioFilter != "" {
		prefix = fmt.Sprintf("[1:a]%s,asplit=2[aud][wsrc];", o.Render.AudioFilter)
		wavesInput, audioMap = "wsrc", "[aud]"
	}
	filter := prefix + fmt.Sprintf("[%s]showwaves=s=%dx%d:rate=%d:mode=%s:colors=%s[wave];[0:v][wave]overlay=%d:%d[vid];[vid]ass=filename='%s':fontsdir='%s'[out]", wavesInput, o.Render.Waveform.Width, o.Render.Waveform.Height, o.Render.Waveform.Rate, o.Render.Waveform.Mode, content.WaveColor, o.Render.Overlay.X, o.Render.Overlay.Y, escapeFilterPath(r.ASSPath), escapeFilterPath(r.FontDir))
	videoMap := "[out]"
	if content.FadeEnabled {
		filter += fmt.Sprintf(";[out]fade=t=in:st=0:d=%.2f,fade=t=out:st=%.2f:d=%.2f[vfade]", o.Render.Fades.EdgeSeconds, content.FadeOutStartSeconds, o.Render.Fades.EdgeSeconds)
		videoMap = "[vfade]"
		src := audioMap
		if src == "1:a" {
			src = "[1:a]"
		}
		filter += fmt.Sprintf(";%safade=t=in:st=0:d=%.2f,afade=t=out:st=%.2f:d=%.2f[afade]", src, o.Render.Fades.EdgeSeconds, content.FadeOutStartSeconds, o.Render.Fades.EdgeSeconds)
		audioMap = "[afade]"
	}
	args := []string{"-v", r.LogLevel, "-f", "rawvideo", "-pix_fmt", o.Render.Canvas.PixelFormat, "-s", fmt.Sprintf("%dx%d", o.Render.Canvas.Width, o.Render.Canvas.Height), "-r", strconv.Itoa(o.Render.Canvas.FrameRate), "-i", "pipe:0", "-i", r.SourcePath, "-filter_complex", filter, "-map", videoMap, "-map", audioMap}
	encodePreset := QualityPreset{Height: c.Video.Height, VideoBitrate: c.Video.RateControl.Bitrate, MaxRate: c.Video.RateControl.MaxRate, BufferSize: c.Video.RateControl.BufferSize, AudioBitrate: c.Audio.Bitrate, RadioLatency: r.DeliveryProfile}
	args = append(args, radioRTSPVideoEncoderArgs(r.Encoder, encodePreset)...)
	args = append(args, r.encodeOptions(r.Encoder)...)
	args = append(args, "-c:a", c.Audio.Codec, "-b:a", c.Audio.Bitrate, "-ar", strconv.Itoa(c.Audio.SampleRate), "-ac", strconv.Itoa(c.Audio.Channels), "-pix_fmt", c.Video.PixelFormat)
	return append(args, "-movflags", "+faststart", "-f", "mp4", "-y", r.OutputPath)
}

func (r RadioRenderRecipe) encodeOptions(encoder VideoEncoderProfile) []string {
	c := r.NormalizedEncodingContract()
	gop := strconv.Itoa(c.Video.GOPFrames)
	args := []string{
		"-g", gop, "-keyint_min", strconv.Itoa(c.Video.MinKeyframeFrames), "-bf", strconv.Itoa(c.Video.BFrames),
		"-force_key_frames", "expr:gte(t,n_forced*" + strconv.FormatFloat(c.Video.ForcedKeyframeSeconds, 'f', -1, 64) + ")",
	}
	if !encoder.Hardware {
		args = append(args, "-sc_threshold", "0", "-x264-params", "aud=1:repeat-headers=1")
	}
	args = append(args, "-bsf:v", "h264_metadata=aud="+parameterValue(c.PrivateParameters, "h264-aud")+",dump_extra=freq="+parameterValue(c.PrivateParameters, "repeat-headers"))
	return args
}

func parameterValue(parameters []RadioEncodingParameter, name string) string {
	for _, parameter := range parameters {
		if parameter.Name == name {
			return parameter.Value
		}
	}
	return ""
}

func legacyRadioGOPFrames(profile string) int {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "hls-high":
		return 120
	case "rtsp-low":
		return 60
	case "rtsp-realtime":
		return 15
	default:
		return 30
	}
}

type RadioObservedVideo struct {
	Codec       string         `json:"codec"`
	Width       int            `json:"width"`
	Height      int            `json:"height"`
	FrameRate   RadioFrameRate `json:"frameRate"`
	PixelFormat string         `json:"pixelFormat"`
	BFrames     int            `json:"bFrames"`
	GOPFrames   int            `json:"gopFrames,omitempty"`
	Bitrate     string         `json:"bitrate,omitempty"`
}

type RadioObservedAudio struct {
	Codec      string `json:"codec"`
	Bitrate    string `json:"bitrate,omitempty"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
}

type RadioAssetSpec struct {
	Video RadioObservedVideo `json:"video"`
	Audio RadioObservedAudio `json:"audio"`
}

func (s RadioAssetSpec) CanonicalJSON() []byte {
	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("marshal radio asset spec: %v", err))
	}
	return data
}

func (s RadioAssetSpec) EncodingFingerprint() string {
	sum := sha256.Sum256(s.CanonicalJSON())
	return hex.EncodeToString(sum[:])
}

type radioFFprobeOutput struct {
	Streams []struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		PixelFormat string `json:"pix_fmt"`
		FrameRate   string `json:"r_frame_rate"`
		BFrames     int    `json:"has_b_frames"`
		Bitrate     string `json:"bit_rate"`
		SampleRate  string `json:"sample_rate"`
		Channels    int    `json:"channels"`
	} `json:"streams"`
	Frames []struct {
		MediaType string `json:"media_type"`
		KeyFrame  int    `json:"key_frame"`
		Timestamp string `json:"best_effort_timestamp_time"`
	} `json:"frames"`
}

func ParseRadioAssetSpecJSON(data []byte) (RadioAssetSpec, error) {
	var out radioFFprobeOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return RadioAssetSpec{}, fmt.Errorf("parse radio ffprobe output: %w", err)
	}
	var spec RadioAssetSpec
	for _, stream := range out.Streams {
		switch stream.CodecType {
		case "video":
			n, d := parseFrameRate(stream.FrameRate)
			spec.Video = RadioObservedVideo{Codec: stream.CodecName, Width: stream.Width, Height: stream.Height, FrameRate: RadioFrameRate{Numerator: n, Denominator: d}, PixelFormat: stream.PixelFormat, BFrames: stream.BFrames, Bitrate: normalizeObservedBitrate(stream.Bitrate)}
		case "audio":
			rate, _ := strconv.Atoi(stream.SampleRate)
			spec.Audio = RadioObservedAudio{Codec: stream.CodecName, Bitrate: normalizeObservedBitrate(stream.Bitrate), SampleRate: rate, Channels: stream.Channels}
		}
	}
	if spec.Video.Codec == "" || spec.Audio.Codec == "" {
		return RadioAssetSpec{}, fmt.Errorf("radio asset requires video and audio streams")
	}
	gop, err := observedGOPFrames(out, spec.Video.FrameRate)
	if err != nil {
		return RadioAssetSpec{}, err
	}
	spec.Video.GOPFrames = gop
	return spec, nil
}

func ObserveRadioAsset(ctx context.Context, ffprobe, path string) (RadioAssetSpec, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, ffprobe, "-v", "error", "-show_streams", "-show_frames", "-of", "json", path)
	hideWindow(cmd)
	stdout, stderr, err := SeparateOutputTrackedFFmpeg(cmd)
	if err != nil {
		return RadioAssetSpec{}, fmt.Errorf("ffprobe radio asset: %w: %s", err, trimOutput(stderr))
	}
	return ParseRadioAssetSpecJSON(stdout)
}

func observedGOPFrames(out radioFFprobeOutput, rate RadioFrameRate) (int, error) {
	if rate.Numerator <= 0 || rate.Denominator <= 0 {
		return 0, errors.New("cannot prove GOP with invalid frame rate")
	}
	var timestamps []float64
	for _, frame := range out.Frames {
		if frame.MediaType != "video" || frame.KeyFrame == 0 {
			continue
		}
		seconds, err := strconv.ParseFloat(frame.Timestamp, 64)
		if err != nil {
			return 0, fmt.Errorf("malformed keyframe timestamp %q", frame.Timestamp)
		}
		timestamps = append(timestamps, seconds)
	}
	if len(timestamps) < 2 {
		return 0, errors.New("cannot prove GOP with fewer than two keyframes")
	}
	intervals := make([]int, 0, len(timestamps)-1)
	for i := 1; i < len(timestamps); i++ {
		current := int((timestamps[i]-timestamps[i-1])*float64(rate.Numerator)/float64(rate.Denominator) + .5)
		if current <= 0 {
			return 0, errors.New("invalid keyframe cadence")
		}
		intervals = append(intervals, current)
	}
	sort.Ints(intervals)
	interval := intervals[len(intervals)/2]
	for _, current := range intervals {
		if radioAbsInt(current-interval) > 1 {
			return 0, errors.New("variable keyframe cadence")
		}
	}
	return interval, nil
}
func radioAbsInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func parseFrameRate(value string) (int, int) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	n, _ := strconv.Atoi(parts[0])
	d, _ := strconv.Atoi(parts[1])
	return n, d
}

func normalizeObservedBitrate(value string) string {
	bps, err := strconv.ParseInt(value, 10, 64)
	if err != nil || bps <= 0 {
		return ""
	}
	return strconv.FormatInt((bps+500)/1000, 10) + "k"
}
