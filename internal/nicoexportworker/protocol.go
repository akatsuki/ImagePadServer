package nicoexportworker

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
	"imagepadserver/internal/video"
)

const (
	ProtocolVersion = 1
	maxEventBytes   = 64 * 1024
)

type Request struct {
	Version               int     `json:"version"`
	RunID                 string  `json:"run_id"`
	MediaID               string  `json:"media_id"`
	SourcePath            string  `json:"source_path"`
	SnapshotPath          string  `json:"snapshot_path"`
	OutputPath            string  `json:"output_path"`
	HLSStagingDir         string  `json:"hls_staging_dir"`
	OutputMode            string  `json:"output_mode,omitempty"`
	FFmpeg                string  `json:"ffmpeg"`
	BrowserPath           string  `json:"browser_path,omitempty"`
	Compositor            string  `json:"compositor,omitempty"`
	TimelineEnabled       bool    `json:"timeline_enabled,omitempty"`
	TimelineCompositor    string  `json:"timeline_compositor,omitempty"`
	TimelineReadbackSlots int     `json:"timeline_readback_slots,omitempty"`
	TimelineGPUBackend    string  `json:"timeline_gpu_backend,omitempty"`
	TimelineRandomSeed    *uint32 `json:"timeline_random_seed,omitempty"`
	Backend               string  `json:"backend,omitempty"`
	Encoder               string  `json:"encoder,omitempty"`
	Width                 int     `json:"width"`
	Height                int     `json:"height"`
	DurationMs            int64   `json:"duration_ms"`
	FPSNum                int64   `json:"fps_num"`
	FPSDen                int64   `json:"fps_den"`
	CRF                   int     `json:"crf"`
	AudioBitrate          string  `json:"audio_bitrate"`
	FilterThreads         int     `json:"filter_threads,omitempty"`
	DecoderThreads        int     `json:"decoder_threads,omitempty"`
	EncoderThreads        int     `json:"encoder_threads,omitempty"`
}

type Event struct {
	Version                 int                                `json:"version"`
	Type                    string                             `json:"type"`
	RunID                   string                             `json:"run_id,omitempty"`
	MediaID                 string                             `json:"media_id,omitempty"`
	Stage                   string                             `json:"stage,omitempty"`
	Completed               int64                              `json:"completed,omitempty"`
	Total                   int64                              `json:"total,omitempty"`
	Output                  string                             `json:"output,omitempty"`
	Playlist                string                             `json:"playlist,omitempty"`
	OK                      bool                               `json:"ok,omitempty"`
	Error                   string                             `json:"error,omitempty"`
	Renderer                string                             `json:"renderer,omitempty"`
	Backend                 string                             `json:"backend,omitempty"`
	GPUBackend              string                             `json:"gpu_backend,omitempty"`
	GPUAdapter              string                             `json:"gpu_adapter,omitempty"`
	HelperSHA256            string                             `json:"helper_sha256,omitempty"`
	BundleSHA256            string                             `json:"bundle_sha256,omitempty"`
	ReadbackSlots           int                                `json:"readback_slots,omitempty"`
	TimelineFallback        bool                               `json:"timeline_fallback,omitempty"`
	FallbackNotice          string                             `json:"fallback_notice,omitempty"`
	WorkerWallSeconds       float64                            `json:"worker_wall_seconds,omitempty"`
	ConvertWallSeconds      float64                            `json:"convert_wall_seconds,omitempty"`
	StageTimings            []video.NicoStageTiming            `json:"stage_timings,omitempty"`
	SpriteCaptureMetrics    *nicorender.TimelineCaptureMetrics `json:"sprite_capture_metrics,omitempty"`
	TimelineProtocol        string                             `json:"timeline_protocol,omitempty"`
	TimelineCaptureDone     *time.Duration                     `json:"timeline_capture_done_ns,omitempty"`
	TimelineFirstAssetReady *time.Duration                     `json:"timeline_first_asset_ready_ns,omitempty"`
	TimelineFirstFrame      *time.Duration                     `json:"timeline_first_frame_ns,omitempty"`
	TimelineStreamEnd       *time.Duration                     `json:"timeline_stream_end_ns,omitempty"`
	TimelineHelperDone      *time.Duration                     `json:"timeline_helper_done_ns,omitempty"`
	TimelineFFmpegDone      *time.Duration                     `json:"timeline_ffmpeg_done_ns,omitempty"`
	TimelineAttempt         *TimelineAttemptMetadata           `json:"timeline_attempt,omitempty"`
}

// TimelineAttemptMetadata carries only validated NCT2 timing diagnostics for
// the failed renderer attempt that triggered CPU fallback.
type TimelineAttemptMetadata struct {
	Protocol        string         `json:"protocol"`
	CaptureDone     *time.Duration `json:"capture_done_ns,omitempty"`
	FirstAssetReady *time.Duration `json:"first_asset_ready_ns,omitempty"`
	FirstFrame      *time.Duration `json:"first_frame_ns,omitempty"`
	StreamEnd       *time.Duration `json:"stream_end_ns,omitempty"`
	HelperDone      *time.Duration `json:"helper_done_ns,omitempty"`
	FFmpegDone      *time.Duration `json:"ffmpeg_done_ns,omitempty"`
}

func ReadRequest(r io.Reader) (Request, error) {
	if r == nil {
		return Request{}, errors.New("nico export worker: request input is required")
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), maxEventBytes)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Request{}, fmt.Errorf("nico export worker: read request: %w", err)
		}
		return Request{}, errors.New("nico export worker: one request JSON line is required")
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return Request{}, errors.New("nico export worker: one request JSON line is required")
	}
	var request Request
	if err := json.Unmarshal([]byte(line), &request); err != nil {
		return Request{}, fmt.Errorf("nico export worker: decode request: %w", err)
	}
	if request.Version != ProtocolVersion {
		return Request{}, fmt.Errorf("nico export worker: unsupported request version %d", request.Version)
	}
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			return Request{}, errors.New("nico export worker: exactly one request JSON line is allowed")
		}
	}
	if err := scanner.Err(); err != nil {
		return Request{}, fmt.Errorf("nico export worker: read trailing input: %w", err)
	}
	return request, nil
}

func (r Request) Validate() error {
	if r.Version != ProtocolVersion {
		return fmt.Errorf("nico export worker: unsupported request version %d", r.Version)
	}
	if _, err := video.NormalizeNicoOutputMode(video.NicoOutputMode(r.OutputMode)); err != nil {
		return fmt.Errorf("nico export worker: %w", err)
	}
	if !validToken(r.RunID) {
		return fmt.Errorf("nico export worker: invalid run id")
	}
	if !validToken(r.MediaID) {
		return fmt.Errorf("nico export worker: invalid media id")
	}
	for name, value := range map[string]string{
		"source": r.SourcePath, "snapshot": r.SnapshotPath, "output": r.OutputPath, "HLS staging": r.HLSStagingDir, "ffmpeg": r.FFmpeg,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("nico export worker: %s path is required", name)
		}
	}
	if err := video.ValidateNicoOutputStaging(r.SourcePath, r.OutputPath, r.HLSStagingDir); err != nil {
		return fmt.Errorf("nico export worker: %w", err)
	}
	if r.Width <= 0 || r.Height <= 0 || r.Width > 3840 || r.Height > 2160 {
		return fmt.Errorf("nico export worker: invalid output size %dx%d", r.Width, r.Height)
	}
	if r.DurationMs <= 0 {
		return errors.New("nico export worker: duration must be positive")
	}
	if _, err := niconico.NewFrameClock(r.FPSNum, r.FPSDen); err != nil {
		return fmt.Errorf("nico export worker: frame clock: %w", err)
	}
	if r.CRF < 0 || r.CRF > 51 {
		return fmt.Errorf("nico export worker: invalid CRF %d", r.CRF)
	}
	for name, value := range map[string]int{
		"filter threads": r.FilterThreads, "decoder threads": r.DecoderThreads, "encoder threads": r.EncoderThreads,
	} {
		if value < 0 || value > 256 {
			return fmt.Errorf("nico export worker: invalid %s %d", name, value)
		}
	}
	if r.Backend != "" && r.Backend != "auto" && r.Backend != "native" && r.Backend != "browser" && r.Backend != "timeline" {
		return fmt.Errorf("nico export worker: invalid backend %q", r.Backend)
	}
	if _, err := nicorender.ValidateTimelineRuntimeOptions(nicorender.TimelineRuntimeOptions{
		Backend: r.TimelineGPUBackend, ReadbackSlots: r.TimelineReadbackSlots,
	}); err != nil {
		return fmt.Errorf("nico export worker: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(r.Encoder)) {
	case "", "x264", "nvenc":
	default:
		return fmt.Errorf("nico export worker: invalid encoder %q", r.Encoder)
	}
	return nil
}

func WriteEvent(w io.Writer, event Event) error {
	if w == nil {
		return errors.New("nico export worker: event output is required")
	}
	if event.Version != ProtocolVersion {
		return fmt.Errorf("nico export worker: unsupported event version %d", event.Version)
	}
	if event.Type != "progress" && event.Type != "result" {
		return fmt.Errorf("nico export worker: unsupported event type %q", event.Type)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("nico export worker: encode event: %w", err)
	}
	if len(data)+1 > maxEventBytes {
		return fmt.Errorf("nico export worker: event exceeds 64 KiB")
	}
	data = append(data, '\n')
	n, err := w.Write(data)
	if err != nil {
		return fmt.Errorf("nico export worker: write event: %w", err)
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func validToken(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
