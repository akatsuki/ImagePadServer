package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
)

const (
	GPUPlaylistTimelineSchema   uint16 = 1
	GPUPlaylistMaxProfileFrames        = 1_000_000
)

// TrackAssets is the immutable, per-track asset set for the explicit playlist
// GPU evaluation route. It is deliberately separate from frame controls so
// artwork, glyphs, text rasters, palette, layout, and loudness data are not
// re-sent with every frame.
type TrackAssets struct {
	Schema           uint16               `json:"schema"`
	TrackID          string               `json:"track_id"`
	AssetsHash       string               `json:"assets_hash"`
	Artwork          *ArtworkMetadata     `json:"artwork"`
	GlyphAtlas       *GlyphAtlasMetadata  `json:"glyph_atlas"`
	TextOverlay      *TextOverlayMetadata `json:"text_overlay,omitempty"`
	BaseTexture      *BaseTextureMetadata `json:"base_texture,omitempty"`
	WaveformTexture  *BaseTextureMetadata `json:"waveform_texture,omitempty"`
	LoudnessTexture  *BaseTextureMetadata `json:"loudness_texture,omitempty"`
	SpectrumTexture  *BaseTextureMetadata `json:"spectrum_texture,omitempty"`
	Layout           MusicSceneLayout     `json:"layout"`
	Palette          MusicScenePalette    `json:"palette"`
	LoudnessEnvelope []uint16             `json:"loudness_envelope"`
	LoudnessTrend    []uint16             `json:"loudness_trend"`
	LoudnessGuides   [4]uint16            `json:"loudness_guides"`
	DurationFrames   uint64               `json:"duration_frames"`
}

// ScrollProfile is a precomputed frame-indexed motion profile. GPU evaluation
// reads it by frame index; it does not ask the CPU for a position each frame.
type ScrollProfile struct {
	ID          string    `json:"id"`
	FPS         uint32    `json:"fps"`
	LoopFrames  uint64    `json:"loop_frames"`
	XQ16        []int32   `json:"x_q16"`
	YQ16        []int32   `json:"y_q16"`
	AlphaQ16    []uint16  `json:"alpha_q16"`
	Clip        SceneRect `json:"clip"`
	ProfileHash string    `json:"profile_hash"`
}

// FrameDirective contains only frame-indexed controls. It intentionally has no
// PCM or final-frame image field.
type FrameDirective struct {
	Sequence      uint64   `json:"sequence"`
	FrameIndex    uint64   `json:"frame_index"`
	PTSNs         int64    `json:"pts_ns"`
	FeatureIndex  uint32   `json:"feature_index"`
	ScrollProfile string   `json:"scroll_profile,omitempty"`
	SpectrumQ16   []uint16 `json:"spectrum_q16"`
	WaveformQ16   []uint16 `json:"waveform_q16,omitempty"`
	ProgressQ16   uint16   `json:"progress_q16"`
	TextAlphaQ16  uint16   `json:"text_alpha_q16"`
	BlackAlphaQ16 uint16   `json:"black_alpha_q16"`
}

// TrackTimeline is the CPU-compiled control timeline for one track. The GPU
// transport may send it in bounded chunks, but the timeline itself is not a
// per-frame RPC and never carries the final rendered image.
type TrackTimeline struct {
	Schema          uint16           `json:"schema"`
	TrackID         string           `json:"track_id"`
	TimelineVersion uint64           `json:"timeline_version"`
	FPS             uint32           `json:"fps"`
	FrameCount      uint64           `json:"frame_count"`
	ScrollProfiles  []ScrollProfile  `json:"scroll_profiles"`
	Frames          []FrameDirective `json:"frames"`
	TimelineHash    string           `json:"timeline_hash"`
}

// TimelineChunk is the bounded transport slice sent after immutable assets
// have been prepared. It contains controls only, never PCM or final pixels.
type TimelineChunk struct {
	ScrollProfiles []ScrollProfile  `json:"scroll_profiles"`
	Frames         []FrameDirective `json:"frames"`
}

func (c TimelineChunk) Validate() error {
	if len(c.Frames) == 0 || len(c.Frames) > GPUH264MaxBatchFrames {
		return fmt.Errorf("playlist GPU timeline chunk must contain 1..%d frames", GPUH264MaxBatchFrames)
	}
	return c.validateFrames()
}

// ValidateTimeline validates a full per-track timeline (no bounded-window frame
// cap) for the one-time prepare_timeline upload.
func (c TimelineChunk) ValidateTimeline() error {
	if len(c.Frames) == 0 {
		return errors.New("playlist GPU timeline must contain at least one frame")
	}
	return c.validateFrames()
}

func (c TimelineChunk) validateFrames() error {
	profiles := make(map[string]struct{}, len(c.ScrollProfiles))
	for _, profile := range c.ScrollProfiles {
		if err := profile.Validate(); err != nil {
			return err
		}
		if _, exists := profiles[profile.ID]; exists {
			return errors.New("duplicate playlist GPU timeline chunk scroll profile")
		}
		profiles[profile.ID] = struct{}{}
	}
	for i, frame := range c.Frames {
		if err := frame.Validate(); err != nil {
			return fmt.Errorf("playlist GPU timeline chunk frame %d: %w", i, err)
		}
		if _, ok := profiles[frame.ScrollProfile]; !ok {
			return fmt.Errorf("playlist GPU timeline chunk frame %d references unknown scroll profile", i)
		}
		if i > 0 && frame.PTSNs <= c.Frames[i-1].PTSNs {
			return fmt.Errorf("playlist GPU timeline chunk frame %d PTS is not strictly increasing", i)
		}
	}
	return nil
}

type PlaylistTransitionReason string

const (
	PlaylistTransitionStop        PlaylistTransitionReason = "stop"
	PlaylistTransitionTrackChange PlaylistTransitionReason = "track_change"
)

type TransitionRequest struct {
	Schema               uint16                   `json:"schema"`
	Epoch                uint64                   `json:"epoch"`
	Reason               PlaylistTransitionReason `json:"reason"`
	SourceTrackID        string                   `json:"source_track_id"`
	PlaybackHeadSequence uint64                   `json:"playback_head_sequence"`
	PlaybackHeadPTSNs    int64                    `json:"playback_head_pts_ns"`
	ObservedBufferDepth  uint32                   `json:"observed_buffer_depth"`
}

type AudioFadePlan struct {
	StartPTSNs int64  `json:"start_pts_ns"`
	DurationNS int64  `json:"duration_ns"`
	Curve      string `json:"curve"`
}

type TransitionPlan struct {
	Schema               uint16                   `json:"schema"`
	PlanVersion          uint64                   `json:"plan_version"`
	Epoch                uint64                   `json:"epoch"`
	Reason               PlaylistTransitionReason `json:"reason"`
	SourceTrackID        string                   `json:"source_track_id"`
	TargetTrackID        string                   `json:"target_track_id,omitempty"`
	PlaybackHeadSequence uint64                   `json:"playback_head_sequence"`
	PlaybackHeadPTSNs    int64                    `json:"playback_head_pts_ns"`
	QueuedTailSequence   uint64                   `json:"queued_tail_sequence"`
	QueuedTailPTSNs      int64                    `json:"queued_tail_pts_ns"`
	FadeStartSequence    uint64                   `json:"fade_start_sequence"`
	FadeStartPTSNs       int64                    `json:"fade_start_pts_ns"`
	FadeEndSequence      uint64                   `json:"fade_end_sequence"`
	FadeEndPTSNs         int64                    `json:"fade_end_pts_ns"`
	DurationFrames       uint64                   `json:"duration_frames"`
	Curve                string                   `json:"curve"`
	AudioFadePlan        AudioFadePlan            `json:"audio_fade_plan"`
}

// CompilePlaylistGPUTrack compiles immutable track assets and frame-indexed
// controls for the explicit playlist GPU evaluation path. It does not alter the
// normal CPU renderer and does not retain per-frame PCM windows.
func CompilePlaylistGPUTrack(input AudioRenderInput, trackID string) (TrackAssets, TrackTimeline, error) {
	if trackID == "" {
		return TrackAssets{}, TrackTimeline{}, errors.New("playlist GPU track id is required")
	}
	fps := input.Analysis.FPS
	if fps <= 0 {
		fps = 30
	}
	if len(input.Analysis.Frames) == 0 {
		return TrackAssets{}, TrackTimeline{}, errors.New("playlist GPU track requires analyzed frames")
	}
	if input.Analysis.Duration < 0 || math.IsNaN(input.Analysis.Duration) || math.IsInf(input.Analysis.Duration, 0) {
		return TrackAssets{}, TrackTimeline{}, errors.New("playlist GPU track duration is invalid")
	}

	builder := NewCanonicalMusicSceneBuilder(input)
	seed := builder.Scene(0, 0)
	loudnessTexture, err := playlistLoudnessGraphTexture(trackID, seed.Dynamics.LoudnessEnvelope)
	if err != nil {
		return TrackAssets{}, TrackTimeline{}, err
	}
	frameCount := int(math.Ceil(input.Analysis.Duration * float64(fps)))
	if frameCount <= 0 {
		frameCount = len(input.Analysis.Frames)
	}
	if frameCount <= 0 {
		frameCount = 1
	}

	assets := TrackAssets{
		Schema:           GPUPlaylistTimelineSchema,
		TrackID:          trackID,
		Artwork:          cloneArtworkMetadata(seed.Artwork),
		GlyphAtlas:       cloneGlyphAtlasMetadata(seed.GlyphAtlas),
		TextOverlay:      cloneTextOverlayMetadata(seed.TextOverlay),
		BaseTexture:      cloneBaseTextureMetadata(input.BaseTexture),
		WaveformTexture:  cloneBaseTextureMetadata(input.WaveformTexture),
		LoudnessTexture:  loudnessTexture,
		SpectrumTexture:  cloneBaseTextureMetadata(seed.SpectrumTexture),
		Layout:           seed.Layout,
		Palette:          seed.Palette,
		LoudnessEnvelope: append([]uint16(nil), seed.Dynamics.LoudnessEnvelope...),
		LoudnessTrend:    append([]uint16(nil), seed.Dynamics.LoudnessTrend...),
		LoudnessGuides:   seed.Dynamics.LoudnessGuides,
		DurationFrames:   uint64(frameCount),
	}
	assets.AssetsHash, _ = playlistTrackAssetsHash(assets)

	profile := ScrollProfile{
		ID:         "default-scroll",
		FPS:        uint32(fps),
		LoopFrames: uint64(frameCount),
		XQ16:       make([]int32, frameCount),
		YQ16:       make([]int32, frameCount),
		AlphaQ16:   make([]uint16, frameCount),
		Clip:       seed.Layout.Title,
	}
	for i := range profile.AlphaQ16 {
		profile.AlphaQ16[i] = ^uint16(0)
	}
	profile.ProfileHash, _ = playlistValueHash(struct {
		ID         string
		FPS        uint32
		LoopFrames uint64
		XQ16       []int32
		YQ16       []int32
		AlphaQ16   []uint16
		Clip       SceneRect
	}{profile.ID, profile.FPS, profile.LoopFrames, profile.XQ16, profile.YQ16, profile.AlphaQ16, profile.Clip})

	frames := make([]FrameDirective, frameCount)
	for i := 0; i < frameCount; i++ {
		featureIndex := i
		if featureIndex >= len(input.Analysis.Frames) {
			featureIndex = len(input.Analysis.Frames) - 1
		}
		feature := input.Analysis.Frames[featureIndex]
		spectrum := make([]uint16, len(feature.Spectrum24))
		for band, value := range feature.Spectrum24 {
			spectrum[band] = playlistQuantizeUnit(value)
		}
		var waveform []uint16
		if featureIndex < len(input.Analysis.WaveformFrames) {
			waveform = normalizeSceneWaveformQ16(input.Analysis.WaveformFrames[featureIndex])
		}
		current := float64(i) / float64(fps)
		progress := 0.0
		if input.Analysis.Duration > 0 {
			progress = sceneClamp01(current / input.Analysis.Duration)
		}
		// The normal CPU production renderer does not burn an edge fade into
		// the track timeline. Transition fade tails carry the explicit text and
		// black alpha controls; normal playlist frames must match the CPU frame
		// semantics and remain fully visible.
		textAlpha := 1.0
		blackAlpha := 0.0
		frames[i] = FrameDirective{
			Sequence:      uint64(i),
			FrameIndex:    uint64(i),
			PTSNs:         int64(i) * int64(1_000_000_000/fps),
			FeatureIndex:  uint32(featureIndex),
			ScrollProfile: profile.ID,
			SpectrumQ16:   spectrum,
			WaveformQ16:   waveform,
			ProgressQ16:   playlistQuantizeUnit(progress),
			TextAlphaQ16:  playlistQuantizeUnit(textAlpha),
			BlackAlphaQ16: playlistQuantizeUnit(blackAlpha),
		}
	}
	timeline := TrackTimeline{
		Schema:          GPUPlaylistTimelineSchema,
		TrackID:         trackID,
		TimelineVersion: 1,
		FPS:             uint32(fps),
		FrameCount:      uint64(frameCount),
		ScrollProfiles:  []ScrollProfile{profile},
		Frames:          frames,
	}
	timeline.TimelineHash, _ = playlistTimelineHash(timeline)
	if err := assets.Validate(); err != nil {
		return TrackAssets{}, TrackTimeline{}, err
	}
	if err := timeline.Validate(); err != nil {
		return TrackAssets{}, TrackTimeline{}, err
	}
	return assets, timeline, nil
}

// RebasePlaylistTimeline applies the server-authoritative sequence/PTS origin
// without changing frame-local controls. The returned timeline is immutable and
// receives a new hash because sequence/PTS are part of the GPU contract.
func RebasePlaylistTimeline(timeline TrackTimeline, baseSequence uint64, basePTSNs int64) (TrackTimeline, error) {
	if err := timeline.Validate(); err != nil {
		return TrackTimeline{}, err
	}
	if basePTSNs < 0 || timeline.FPS == 0 {
		return TrackTimeline{}, errors.New("invalid playlist GPU timeline rebase origin")
	}
	frameDurationNS := int64(1_000_000_000 / timeline.FPS)
	if frameDurationNS <= 0 || uint64(len(timeline.Frames)) != timeline.FrameCount {
		return TrackTimeline{}, errors.New("invalid playlist GPU timeline rebase frame contract")
	}
	rebased := timeline
	rebased.Frames = append([]FrameDirective(nil), timeline.Frames...)
	for i := range rebased.Frames {
		if baseSequence > ^uint64(0)-uint64(i) {
			return TrackTimeline{}, errors.New("playlist GPU timeline rebase sequence overflow")
		}
		if basePTSNs > int64(^uint64(0)>>1)-int64(i)*frameDurationNS {
			return TrackTimeline{}, errors.New("playlist GPU timeline rebase PTS overflow")
		}
		rebased.Frames[i].Sequence = baseSequence + uint64(i)
		rebased.Frames[i].PTSNs = basePTSNs + int64(i)*frameDurationNS
	}
	var err error
	rebased.TimelineHash, err = playlistTimelineHash(rebased)
	if err != nil {
		return TrackTimeline{}, err
	}
	if err := rebased.Validate(); err != nil {
		return TrackTimeline{}, err
	}
	return rebased, nil
}

// RebasePlaylistTimelineFrameIndex assigns a session-global GPU frame index
// without changing the server-authoritative sequence or PTS. Track timelines
// are compiled with local frame indices, but one continuous sidecar/NVENC
// session requires the index to increase across the fade tail and target track.
func RebasePlaylistTimelineFrameIndex(timeline TrackTimeline, baseFrameIndex uint64) (TrackTimeline, error) {
	if err := timeline.Validate(); err != nil {
		return TrackTimeline{}, err
	}
	if uint64(len(timeline.Frames)) != timeline.FrameCount {
		return TrackTimeline{}, errors.New("invalid playlist GPU frame-index rebase frame contract")
	}
	rebased := timeline
	rebased.Frames = append([]FrameDirective(nil), timeline.Frames...)
	for i := range rebased.Frames {
		if baseFrameIndex > ^uint64(0)-uint64(i) {
			return TrackTimeline{}, errors.New("playlist GPU frame-index rebase overflow")
		}
		rebased.Frames[i].FrameIndex = baseFrameIndex + uint64(i)
	}
	var err error
	rebased.TimelineHash, err = playlistTimelineHash(rebased)
	if err != nil {
		return TrackTimeline{}, err
	}
	if err := rebased.Validate(); err != nil {
		return TrackTimeline{}, err
	}
	return rebased, nil
}

// CompilePlaylistGPUFadeTail creates the deterministic GPU-only tail required
// by a transition plan. It carries controls only; artwork/glyph/loudness remain
// owned by TrackAssets and are not copied into per-frame payloads.
func CompilePlaylistGPUFadeTail(plan TransitionPlan, source TrackTimeline) (TrackTimeline, error) {
	if err := plan.Validate(); err != nil {
		return TrackTimeline{}, err
	}
	if err := source.Validate(); err != nil {
		return TrackTimeline{}, err
	}
	if source.FPS == 0 || plan.DurationFrames == 0 || plan.DurationFrames > uint64(^uint(0)>>1) {
		return TrackTimeline{}, errors.New("invalid playlist GPU fade tail frame contract")
	}
	frameDurationNS := int64(1_000_000_000 / source.FPS)
	if frameDurationNS <= 0 || plan.FadeEndPTSNs != plan.FadeStartPTSNs+int64(plan.DurationFrames-1)*frameDurationNS {
		return TrackTimeline{}, errors.New("playlist GPU fade tail PTS does not match source FPS")
	}
	last := source.Frames[len(source.Frames)-1]
	frames := make([]FrameDirective, int(plan.DurationFrames))
	for i := range frames {
		black := uint16(0)
		if plan.DurationFrames == 1 {
			black = ^uint16(0)
		} else {
			black = uint16((uint64(i) * uint64(^uint16(0))) / (plan.DurationFrames - 1))
		}
		frames[i] = FrameDirective{
			Sequence:      plan.FadeStartSequence + uint64(i),
			FrameIndex:    uint64(i),
			PTSNs:         plan.FadeStartPTSNs + int64(i)*frameDurationNS,
			FeatureIndex:  last.FeatureIndex,
			ScrollProfile: last.ScrollProfile,
			SpectrumQ16:   append([]uint16(nil), last.SpectrumQ16...),
			WaveformQ16:   append([]uint16(nil), last.WaveformQ16...),
			ProgressQ16:   ^uint16(0),
			TextAlphaQ16:  ^uint16(0) - black,
			BlackAlphaQ16: black,
		}
	}
	tail := TrackTimeline{
		Schema:          source.Schema,
		TrackID:         source.TrackID,
		TimelineVersion: source.TimelineVersion + 1,
		FPS:             source.FPS,
		FrameCount:      plan.DurationFrames,
		ScrollProfiles:  append([]ScrollProfile(nil), source.ScrollProfiles...),
		Frames:          frames,
	}
	var err error
	tail.TimelineHash, err = playlistTimelineHash(tail)
	if err != nil {
		return TrackTimeline{}, err
	}
	if err := tail.Validate(); err != nil {
		return TrackTimeline{}, err
	}
	return tail, nil
}

func (r TransitionRequest) Validate() error {
	if r.Schema != GPUPlaylistTimelineSchema || r.Epoch == 0 || r.SourceTrackID == "" || r.PlaybackHeadPTSNs < 0 {
		return errors.New("invalid playlist GPU transition request")
	}
	if r.Reason != PlaylistTransitionStop && r.Reason != PlaylistTransitionTrackChange {
		return errors.New("invalid playlist GPU transition reason")
	}
	return nil
}

func CompilePlaylistTransition(request TransitionRequest, targetTrackID string, queuedTailSequence uint64, queuedTailPTSNs int64, safetyFrames, durationFrames uint64, frameDurationNS int64, audio AudioFadePlan) (TransitionPlan, error) {
	if err := request.Validate(); err != nil {
		return TransitionPlan{}, err
	}
	if queuedTailSequence < request.PlaybackHeadSequence || queuedTailPTSNs < request.PlaybackHeadPTSNs || durationFrames == 0 || frameDurationNS <= 0 {
		return TransitionPlan{}, errors.New("invalid playlist GPU transition queue or duration")
	}
	if request.Reason == PlaylistTransitionTrackChange && targetTrackID == "" {
		return TransitionPlan{}, errors.New("playlist track change requires a target track")
	}
	if request.Reason == PlaylistTransitionStop && targetTrackID != "" {
		return TransitionPlan{}, errors.New("playlist stop cannot select a target track")
	}
	if ^uint64(0)-request.PlaybackHeadSequence < safetyFrames || queuedTailSequence == ^uint64(0) {
		return TransitionPlan{}, errors.New("playlist GPU transition sequence overflow")
	}
	headSafeSequence := request.PlaybackHeadSequence + safetyFrames
	tailSafeSequence := queuedTailSequence + 1
	fadeStartSequence := headSafeSequence
	if tailSafeSequence > fadeStartSequence {
		fadeStartSequence = tailSafeSequence
	}
	if ^uint64(0)-fadeStartSequence < durationFrames-1 {
		return TransitionPlan{}, errors.New("playlist GPU fade sequence overflow")
	}
	if audio.DurationNS == 0 {
		audio.DurationNS = int64(durationFrames) * frameDurationNS
	}
	if audio.DurationNS != int64(durationFrames)*frameDurationNS || audio.Curve == "" {
		return TransitionPlan{}, errors.New("playlist GPU audio fade does not match video fade")
	}
	fadeStartPTSNs := request.PlaybackHeadPTSNs + int64(safetyFrames)*frameDurationNS
	tailStartPTSNs := queuedTailPTSNs + frameDurationNS
	if tailStartPTSNs > fadeStartPTSNs {
		fadeStartPTSNs = tailStartPTSNs
	}
	fadeEndPTSNs := fadeStartPTSNs + int64(durationFrames-1)*frameDurationNS
	audio.StartPTSNs = fadeStartPTSNs
	plan := TransitionPlan{
		Schema:               GPUPlaylistTimelineSchema,
		PlanVersion:          1,
		Epoch:                request.Epoch,
		Reason:               request.Reason,
		SourceTrackID:        request.SourceTrackID,
		TargetTrackID:        targetTrackID,
		PlaybackHeadSequence: request.PlaybackHeadSequence,
		PlaybackHeadPTSNs:    request.PlaybackHeadPTSNs,
		QueuedTailSequence:   queuedTailSequence,
		QueuedTailPTSNs:      queuedTailPTSNs,
		FadeStartSequence:    fadeStartSequence,
		FadeStartPTSNs:       fadeStartPTSNs,
		FadeEndSequence:      fadeStartSequence + durationFrames - 1,
		FadeEndPTSNs:         fadeEndPTSNs,
		DurationFrames:       durationFrames,
		Curve:                audio.Curve,
		AudioFadePlan:        audio,
	}
	if err := plan.Validate(); err != nil {
		return TransitionPlan{}, err
	}
	return plan, nil
}

func (p TransitionPlan) Validate() error {
	if p.Schema != GPUPlaylistTimelineSchema || p.PlanVersion == 0 || p.Epoch == 0 || p.SourceTrackID == "" || p.DurationFrames == 0 || p.Curve == "" {
		return errors.New("invalid playlist GPU transition plan header")
	}
	if p.Reason != PlaylistTransitionStop && p.Reason != PlaylistTransitionTrackChange {
		return errors.New("invalid playlist GPU transition plan reason")
	}
	if p.Reason == PlaylistTransitionTrackChange && p.TargetTrackID == "" {
		return errors.New("playlist GPU transition plan target is missing")
	}
	if p.Reason == PlaylistTransitionStop && p.TargetTrackID != "" {
		return errors.New("playlist GPU stop plan cannot have a target")
	}
	if p.QueuedTailSequence < p.PlaybackHeadSequence || p.QueuedTailPTSNs < p.PlaybackHeadPTSNs || p.FadeStartSequence <= p.QueuedTailSequence || p.FadeEndSequence < p.FadeStartSequence || p.FadeEndSequence-p.FadeStartSequence+1 != p.DurationFrames {
		return errors.New("invalid playlist GPU transition sequence range")
	}
	if p.PlaybackHeadPTSNs < 0 || p.QueuedTailPTSNs < 0 || p.FadeStartPTSNs < p.QueuedTailPTSNs || p.FadeEndPTSNs < p.FadeStartPTSNs {
		return errors.New("invalid playlist GPU transition PTS range")
	}
	if p.AudioFadePlan.StartPTSNs != p.FadeStartPTSNs || p.AudioFadePlan.DurationNS <= 0 || p.AudioFadePlan.DurationNS == 0 || p.AudioFadePlan.Curve != p.Curve {
		return errors.New("playlist GPU audio fade plan does not match video fade")
	}
	return nil
}

func (p TransitionPlan) ValidateFor(currentEpoch, committedSequence uint64) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Epoch != currentEpoch {
		return errors.New("playlist GPU transition epoch is stale")
	}
	if p.FadeStartSequence <= committedSequence {
		return errors.New("playlist GPU transition starts at an already committed sequence")
	}
	return nil
}

type PlaylistGPUTransitionState string

const (
	PlaylistGPUStateRunning             PlaylistGPUTransitionState = "running"
	PlaylistGPUStateTransitionRequested PlaylistGPUTransitionState = "transition_requested"
	PlaylistGPUStateStopNormalSubmit    PlaylistGPUTransitionState = "stop_normal_submit"
	PlaylistGPUStateDrainInFlight       PlaylistGPUTransitionState = "drain_in_flight"
	PlaylistGPUStateFadeTailRendering   PlaylistGPUTransitionState = "fade_tail_rendering"
	PlaylistGPUStateFlushOutput         PlaylistGPUTransitionState = "flush_output"
	PlaylistGPUStateTransitionComplete  PlaylistGPUTransitionState = "transition_complete"
	PlaylistGPUStateNextTrack           PlaylistGPUTransitionState = "next_track"
)

// PlaylistGPUTransitionController is the explicit evaluation-only lifecycle
// boundary for stop/track-change plans. It does not alter the normal CPU
// renderer and deliberately exposes no implicit fallback path.
type PlaylistGPUTransitionController struct {
	state             PlaylistGPUTransitionState
	currentEpoch      uint64
	committedSequence uint64
	plan              *TransitionPlan
}

func NewPlaylistGPUTransitionController(epoch, committedSequence uint64) (*PlaylistGPUTransitionController, error) {
	if epoch == 0 {
		return nil, errors.New("playlist GPU transition controller epoch must be non-zero")
	}
	return &PlaylistGPUTransitionController{
		state:             PlaylistGPUStateRunning,
		currentEpoch:      epoch,
		committedSequence: committedSequence,
	}, nil
}

func (c *PlaylistGPUTransitionController) State() PlaylistGPUTransitionState {
	return c.state
}

func (c *PlaylistGPUTransitionController) Plan() (TransitionPlan, bool) {
	if c.plan == nil {
		return TransitionPlan{}, false
	}
	return *c.plan, true
}

func (c *PlaylistGPUTransitionController) Request(plan TransitionPlan) error {
	if c.state != PlaylistGPUStateRunning {
		return errors.New("playlist GPU transition request is not allowed in the current state")
	}
	if err := plan.ValidateFor(c.currentEpoch, c.committedSequence); err != nil {
		return err
	}
	c.plan = &plan
	c.state = PlaylistGPUStateTransitionRequested
	return nil
}

func (c *PlaylistGPUTransitionController) StopNormalSubmit() error {
	if c.state != PlaylistGPUStateTransitionRequested {
		return errors.New("playlist GPU normal submit can only stop after a transition request")
	}
	c.state = PlaylistGPUStateStopNormalSubmit
	return nil
}

func (c *PlaylistGPUTransitionController) BeginDrainInFlight() error {
	if c.state != PlaylistGPUStateStopNormalSubmit {
		return errors.New("playlist GPU in-flight drain requires normal submit to be stopped")
	}
	c.state = PlaylistGPUStateDrainInFlight
	return nil
}

func (c *PlaylistGPUTransitionController) BeginFadeTailRendering() error {
	if c.state != PlaylistGPUStateDrainInFlight {
		return errors.New("playlist GPU fade tail requires in-flight drain")
	}
	c.state = PlaylistGPUStateFadeTailRendering
	return nil
}

func (c *PlaylistGPUTransitionController) BeginNextTrackRendering(epoch uint64) error {
	if c.state != PlaylistGPUStateFadeTailRendering || epoch <= c.currentEpoch {
		return errors.New("playlist GPU next-track rendering requires fade tail and newer epoch")
	}
	c.currentEpoch = epoch
	c.state = PlaylistGPUStateNextTrack
	return nil
}

func (c *PlaylistGPUTransitionController) BeginFlushOutput() error {
	if c.state != PlaylistGPUStateFadeTailRendering {
		return errors.New("playlist GPU Flush/EOS requires fade tail completion")
	}
	if c.plan != nil && c.plan.Reason == PlaylistTransitionTrackChange {
		return errors.New("playlist GPU track-change Flush/EOS requires completed next-track rendering")
	}
	c.state = PlaylistGPUStateFlushOutput
	return nil
}

func (c *PlaylistGPUTransitionController) BeginFlushOutputAfterNextTrack() error {
	if c.state != PlaylistGPUStateNextTrack {
		return errors.New("playlist GPU Flush/EOS requires completed next-track rendering")
	}
	c.state = PlaylistGPUStateFlushOutput
	return nil
}

func (c *PlaylistGPUTransitionController) CompleteTransition(committedSequence uint64) error {
	if c.state != PlaylistGPUStateFlushOutput || c.plan == nil {
		return errors.New("playlist GPU transition cannot complete before Flush/EOS")
	}
	if committedSequence < c.plan.FadeEndSequence {
		return errors.New("playlist GPU transition completed before fade tail was committed")
	}
	c.committedSequence = committedSequence
	c.state = PlaylistGPUStateTransitionComplete
	return nil
}

func (c *PlaylistGPUTransitionController) BeginNextTrack(epoch uint64) error {
	if c.state != PlaylistGPUStateTransitionComplete || epoch <= c.currentEpoch {
		return errors.New("playlist GPU next track requires a newer epoch after transition completion")
	}
	c.currentEpoch = epoch
	c.plan = nil
	c.state = PlaylistGPUStateNextTrack
	return nil
}

func (c *PlaylistGPUTransitionController) ResumeRunning() error {
	if c.state != PlaylistGPUStateNextTrack {
		return errors.New("playlist GPU controller can resume only for the next track")
	}
	c.state = PlaylistGPUStateRunning
	return nil
}

// ResumeRunningAfterContinuousTrack returns the same long-lived controller to
// normal submission after a track-change transition has rendered and flushed
// the target track. The target epoch must already have been installed by
// BeginNextTrackRendering; accepting an older epoch here would allow a stale
// transition to reopen the sidecar session.
func (c *PlaylistGPUTransitionController) ResumeRunningAfterContinuousTrack(epoch, committedSequence uint64) error {
	if c.state != PlaylistGPUStateTransitionComplete || c.plan == nil {
		return errors.New("playlist GPU controller can resume continuous playback only after transition completion")
	}
	if epoch == 0 || epoch != c.currentEpoch {
		return errors.New("playlist GPU continuous playback resume epoch is stale")
	}
	if committedSequence < c.committedSequence || committedSequence <= c.plan.FadeEndSequence {
		return errors.New("playlist GPU continuous playback resume sequence is not fully committed")
	}
	c.committedSequence = committedSequence
	c.plan = nil
	c.state = PlaylistGPUStateRunning
	return nil
}

// ResumeRunningAfterStopTransition reopens normal submission after a stop/
// pause fade tail has been flushed. Stop transitions keep the current session
// epoch and committed sequence; the final radio-session teardown still owns
// worker closure and context cancellation.
func (c *PlaylistGPUTransitionController) ResumeRunningAfterStopTransition() error {
	if c.state != PlaylistGPUStateTransitionComplete || c.plan == nil {
		return errors.New("playlist GPU controller can resume after stop only after transition completion")
	}
	if c.plan.Reason != PlaylistTransitionStop {
		return errors.New("playlist GPU stop resume cannot consume a track-change plan")
	}
	c.plan = nil
	c.state = PlaylistGPUStateRunning
	return nil
}

func (a TrackAssets) Validate() error {
	if a.Schema != GPUPlaylistTimelineSchema || a.TrackID == "" || a.DurationFrames == 0 || a.Artwork == nil || a.GlyphAtlas == nil {
		return errors.New("invalid playlist GPU track assets header")
	}
	if err := a.Artwork.Validate(); err != nil {
		return fmt.Errorf("playlist GPU artwork: %w", err)
	}
	if err := a.GlyphAtlas.Validate(); err != nil {
		return fmt.Errorf("playlist GPU glyph atlas: %w", err)
	}
	if a.TextOverlay != nil {
		if err := a.TextOverlay.Validate(); err != nil {
			return fmt.Errorf("playlist GPU text overlay: %w", err)
		}
	}
	for name, texture := range map[string]*BaseTextureMetadata{"base": a.BaseTexture, "waveform": a.WaveformTexture, "loudness": a.LoudnessTexture, "spectrum": a.SpectrumTexture} {
		if texture != nil {
			if err := texture.Validate(); err != nil {
				return fmt.Errorf("playlist GPU %s texture: %w", name, err)
			}
		}
	}
	if len(a.LoudnessEnvelope) > MusicMaxLoudnessSamples || len(a.LoudnessTrend) > MusicMaxLoudnessSamples {
		return errors.New("playlist GPU loudness timeline is too large")
	}
	if !isPlaylistHash(a.AssetsHash) {
		return errors.New("invalid playlist GPU assets hash")
	}
	expectedHash, err := playlistTrackAssetsHash(a)
	if err != nil || expectedHash != a.AssetsHash {
		return errors.New("playlist GPU assets hash does not match immutable payload")
	}
	return nil
}

func (t TrackTimeline) Validate() error {
	if t.Schema != GPUPlaylistTimelineSchema || t.TrackID == "" || t.TimelineVersion == 0 || t.FPS == 0 || t.FrameCount == 0 || len(t.Frames) != int(t.FrameCount) {
		return errors.New("invalid playlist GPU timeline header")
	}
	profiles := make(map[string]ScrollProfile, len(t.ScrollProfiles))
	for _, profile := range t.ScrollProfiles {
		if err := profile.Validate(); err != nil {
			return err
		}
		profiles[profile.ID] = profile
	}
	if len(profiles) != len(t.ScrollProfiles) {
		return errors.New("duplicate playlist GPU scroll profile")
	}
	baseFrameIndex := t.Frames[0].FrameIndex
	for i, frame := range t.Frames {
		if err := frame.Validate(); err != nil {
			return fmt.Errorf("playlist GPU frame %d: %w", i, err)
		}
		if baseFrameIndex > ^uint64(0)-uint64(i) || frame.FrameIndex != baseFrameIndex+uint64(i) {
			return fmt.Errorf("playlist GPU frame %d identity is not contiguous", i)
		}
		if i > 0 {
			if t.Frames[i-1].Sequence == ^uint64(0) || frame.Sequence != t.Frames[i-1].Sequence+1 {
				return fmt.Errorf("playlist GPU frame %d sequence is not contiguous", i)
			}
			if frame.PTSNs <= t.Frames[i-1].PTSNs {
				return fmt.Errorf("playlist GPU frame %d PTS is not strictly increasing", i)
			}
		}
		if _, ok := profiles[frame.ScrollProfile]; !ok {
			return fmt.Errorf("playlist GPU frame %d references unknown scroll profile", i)
		}
	}
	if !isPlaylistHash(t.TimelineHash) {
		return errors.New("invalid playlist GPU timeline hash")
	}
	expectedHash, err := playlistTimelineHash(t)
	if err != nil || expectedHash != t.TimelineHash {
		return errors.New("playlist GPU timeline hash does not match frame controls")
	}
	return nil
}

func (t TrackTimeline) ValidateChunk(start, count int) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if start < 0 || count <= 0 || count > GPUH264MaxBatchFrames || start+count > len(t.Frames) {
		return fmt.Errorf("playlist GPU timeline chunk must contain 1..%d frames", GPUH264MaxBatchFrames)
	}
	return nil
}

func (p ScrollProfile) Validate() error {
	if p.ID == "" || p.FPS == 0 || p.LoopFrames == 0 || p.LoopFrames > GPUPlaylistMaxProfileFrames || len(p.XQ16) != int(p.LoopFrames) || len(p.YQ16) != int(p.LoopFrames) || len(p.AlphaQ16) != int(p.LoopFrames) {
		return errors.New("invalid playlist GPU scroll profile")
	}
	if !isPlaylistHash(p.ProfileHash) {
		return errors.New("invalid playlist GPU scroll profile hash")
	}
	return nil
}

func (f FrameDirective) Validate() error {
	if len(f.SpectrumQ16) != 24 || len(f.WaveformQ16) > MusicMaxWaveformSamples || len(f.WaveformQ16)%2 != 0 || f.PTSNs < 0 || f.ScrollProfile == "" {
		return errors.New("invalid playlist GPU frame directive")
	}
	return nil
}

func playlistQuantizeUnit(value float64) uint16 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	if value >= 1 {
		return ^uint16(0)
	}
	return uint16(math.Round(value * float64(^uint16(0))))
}

func playlistValueHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func playlistTrackAssetsHash(a TrackAssets) (string, error) {
	return playlistValueHash(struct {
		Schema           uint16
		TrackID          string
		Artwork          *ArtworkMetadata
		GlyphAtlas       *GlyphAtlasMetadata
		TextOverlay      *TextOverlayMetadata
		BaseTexture      *BaseTextureMetadata
		WaveformTexture  *BaseTextureMetadata
		LoudnessTexture  *BaseTextureMetadata
		SpectrumTexture  *BaseTextureMetadata
		Layout           MusicSceneLayout
		Palette          MusicScenePalette
		LoudnessEnvelope []uint16
		LoudnessTrend    []uint16
		LoudnessGuides   [4]uint16
		DurationFrames   uint64
	}{a.Schema, a.TrackID, a.Artwork, a.GlyphAtlas, a.TextOverlay, a.BaseTexture, a.WaveformTexture, a.LoudnessTexture, a.SpectrumTexture, a.Layout, a.Palette, a.LoudnessEnvelope, a.LoudnessTrend, a.LoudnessGuides, a.DurationFrames})
}

func playlistTimelineHash(t TrackTimeline) (string, error) {
	return playlistValueHash(struct {
		Schema          uint16
		TrackID         string
		TimelineVersion uint64
		FPS             uint32
		FrameCount      uint64
		ScrollProfiles  []ScrollProfile
		Frames          []FrameDirective
	}{t.Schema, t.TrackID, t.TimelineVersion, t.FPS, t.FrameCount, t.ScrollProfiles, t.Frames})
}

func isPlaylistHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneArtworkMetadata(value *ArtworkMetadata) *ArtworkMetadata {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Payload = append([]byte(nil), value.Payload...)
	return &clone
}

func cloneGlyphAtlasMetadata(value *GlyphAtlasMetadata) *GlyphAtlasMetadata {
	if value == nil {
		return nil
	}
	clone := *value
	clone.FallbackOrder = append([]string(nil), value.FallbackOrder...)
	clone.Payload = append([]byte(nil), value.Payload...)
	clone.Glyphs = append([]GlyphEntry(nil), value.Glyphs...)
	clone.TextRuns = append([]TextRun(nil), value.TextRuns...)
	return &clone
}

func cloneTextOverlayMetadata(value *TextOverlayMetadata) *TextOverlayMetadata {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Payload = append([]byte(nil), value.Payload...)
	return &clone
}

func playlistLoudnessGraphTexture(trackID string, envelope []uint16) (*BaseTextureMetadata, error) {
	const width = 1000
	graph := image.NewRGBA(image.Rect(0, 0, width, 1))
	for x := 0; x < width; x++ {
		value := uint16(0)
		if x < len(envelope) {
			value = envelope[x]
		}
		level := uint8(value >> 8)
		graph.SetRGBA(x, 0, color.RGBA{R: level, G: level, B: level, A: 255})
	}
	texture, err := NewBaseTextureMetadata("playlist-loudness-"+trackID, graph, ColorSRGB)
	if err != nil {
		return nil, fmt.Errorf("compile playlist loudness graph texture: %w", err)
	}
	return &texture, nil
}

func cloneBaseTextureMetadata(value *BaseTextureMetadata) *BaseTextureMetadata {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Payload = append([]byte(nil), value.Payload...)
	return &clone
}
