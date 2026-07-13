package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	programVideoFrameRate       = 30
	programAudioSampleRate      = 48000
	programAudioChannels        = 2
	programAudioBytes           = 2
	programSamplesPerFrame      = programAudioSampleRate / programVideoFrameRate
	programTransitionFadeFrames = 6
)

type RadioOutputMode string

const (
	RadioOutputModeCompatibilityCopy RadioOutputMode = "compatibility-copy"
	RadioOutputModeProgram           RadioOutputMode = "program"
)

func normalizeRadioOutputMode(mode RadioOutputMode) RadioOutputMode {
	if mode == RadioOutputModeProgram {
		return RadioOutputModeProgram
	}
	return RadioOutputModeCompatibilityCopy
}

type ProgramTick struct {
	VideoPTS     time.Duration
	AudioPTS     time.Duration
	AudioSamples int
}

type ProgramClock struct {
	mu                sync.Mutex
	videoFrames       uint64
	audioSamples      uint64
	sourceTransitions uint64
}

func NewProgramClock() *ProgramClock {
	return &ProgramClock{}
}

func (c *ProgramClock) Next() ProgramTick {
	c.mu.Lock()
	defer c.mu.Unlock()
	tick := ProgramTick{
		VideoPTS:     time.Duration(c.videoFrames) * time.Second / programVideoFrameRate,
		AudioPTS:     time.Duration(c.audioSamples) * time.Second / programAudioSampleRate,
		AudioSamples: programSamplesPerFrame,
	}
	c.videoFrames++
	c.audioSamples += programSamplesPerFrame
	return tick
}

// MarkSourceTransition records source ownership changes without resetting either
// output timeline. ProgramClock remains the only output timestamp authority.
func (c *ProgramClock) MarkSourceTransition() {
	c.mu.Lock()
	c.sourceTransitions++
	c.mu.Unlock()
}

type ProgramSourceFrame struct {
	VideoRGBA      []byte
	AudioPCM       []byte
	SourceVideoPTS time.Duration
	SourceAudioPTS time.Duration
	written        chan error
}

type ProgramFrameWriter interface {
	WriteVideoRGBA(frame []byte, pts time.Duration) error
	WriteAudioPCM(samples []byte, pts time.Duration) error
}

type ProgramEncoder interface {
	ProgramFrameWriter
	Output() io.Reader
	Healthy() error
	Close()
}

type ProgramOverlayRender func(width, height int, elapsed time.Duration, snapshot OverlaySnapshot) ([]byte, error)

type ProgramCompositor struct {
	mu               sync.Mutex
	width            int
	height           int
	encoder          ProgramFrameWriter
	renderOverlay    ProgramOverlayRender
	overlay          OverlaySnapshot
	overlayFailures  int
	overlayDisabled  bool
	lastOverlayError error
	lastVideo        []byte
	fadeFrame        int
}

func NewProgramCompositor(width, height int, encoder ProgramFrameWriter, renderOverlay ProgramOverlayRender) *ProgramCompositor {
	return &ProgramCompositor{width: width, height: height, encoder: encoder, renderOverlay: renderOverlay}
}

func (c *ProgramCompositor) SetOverlay(snapshot OverlaySnapshot) {
	c.mu.Lock()
	c.overlay = snapshot
	c.overlayFailures = 0
	c.overlayDisabled = false
	c.lastOverlayError = nil
	c.mu.Unlock()
}

func (c *ProgramCompositor) DisableOverlay() {
	c.mu.Lock()
	c.overlayDisabled = true
	c.mu.Unlock()
}

func (c *ProgramCompositor) OverlayDisabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.overlayDisabled
}

func (c *ProgramCompositor) LastOverlayError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastOverlayError
}

func (c *ProgramCompositor) WriteTick(tick ProgramTick, source ProgramSourceFrame) error {
	if c.encoder == nil {
		return errors.New("program encoder is nil")
	}
	videoBytes := c.width * c.height * 4
	videoFrame := make([]byte, videoBytes)
	if len(source.VideoRGBA) == videoBytes {
		copy(videoFrame, source.VideoRGBA)
		c.mu.Lock()
		c.lastVideo = append(c.lastVideo[:0], source.VideoRGBA...)
		c.fadeFrame = 0
		c.mu.Unlock()
	} else {
		c.mu.Lock()
		if len(c.lastVideo) == videoBytes && c.fadeFrame < programTransitionFadeFrames {
			c.fadeFrame++
			remaining := programTransitionFadeFrames - c.fadeFrame
			for i, value := range c.lastVideo {
				videoFrame[i] = uint8(int(value) * remaining / programTransitionFadeFrames)
			}
		}
		c.mu.Unlock()
	}
	audioBytes := tick.AudioSamples * programAudioChannels * programAudioBytes
	audioFrame := make([]byte, audioBytes)
	if len(source.AudioPCM) == audioBytes {
		copy(audioFrame, source.AudioPCM)
	}

	c.mu.Lock()
	snapshot := c.overlay
	disabled := c.overlayDisabled
	render := c.renderOverlay
	c.mu.Unlock()
	if !disabled && render != nil && snapshot.Mode == OverlayModeNotifications {
		overlay, err := render(c.width, c.height, tick.VideoPTS, snapshot)
		if err != nil {
			c.mu.Lock()
			c.overlayFailures++
			c.lastOverlayError = err
			if c.overlayFailures > 1 {
				c.overlayDisabled = true
			}
			c.mu.Unlock()
			// A failed overlay frame is bridged by generated black; audio remains
			// clock-owned silence/source PCM and output never stops.
			clear(videoFrame)
		} else if len(overlay) == videoBytes {
			alphaCompositeRGBA(videoFrame, overlay)
		}
	}
	if err := c.encoder.WriteVideoRGBA(videoFrame, tick.VideoPTS); err != nil {
		return fmt.Errorf("program video encoder: %w", err)
	}
	if err := c.encoder.WriteAudioPCM(audioFrame, tick.AudioPTS); err != nil {
		return fmt.Errorf("program audio encoder: %w", err)
	}
	return nil
}

type ProgramPipeline struct {
	mu              sync.Mutex
	clock           *ProgramClock
	encoder         ProgramEncoder
	compositor      *ProgramCompositor
	source          <-chan ProgramSourceFrame
	tickInterval    time.Duration
	onOverlayFail   func(error)
	overlayNotified bool
}

func NewProgramPipeline(width, height int, encoder ProgramEncoder, renderOverlay ProgramOverlayRender) *ProgramPipeline {
	return &ProgramPipeline{
		clock:        NewProgramClock(),
		encoder:      encoder,
		compositor:   NewProgramCompositor(width, height, encoder, renderOverlay),
		tickInterval: time.Second / programVideoFrameRate,
	}
}

func (p *ProgramPipeline) SetSource(source <-chan ProgramSourceFrame) {
	p.mu.Lock()
	p.clock.MarkSourceTransition()
	p.source = source
	p.mu.Unlock()
}

func (p *ProgramPipeline) ClearSource() {
	p.SetSource(nil)
}

func (p *ProgramPipeline) SetOverlay(snapshot OverlaySnapshot) {
	p.compositor.SetOverlay(snapshot)
	p.mu.Lock()
	p.overlayNotified = false
	p.mu.Unlock()
}

func (p *ProgramPipeline) DisableOverlay() {
	p.compositor.DisableOverlay()
}

func (p *ProgramPipeline) Run(ctx context.Context) error {
	if p.encoder == nil {
		return errors.New("program encoder is nil")
	}
	ticker := time.NewTicker(p.tickInterval)
	defer ticker.Stop()
	for {
		if err := p.writeNextTick(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (p *ProgramPipeline) writeNextTick() error {
	if err := p.encoder.Healthy(); err != nil {
		return fmt.Errorf("program encoder unhealthy: %w", err)
	}
	p.mu.Lock()
	source := p.source
	p.mu.Unlock()
	frame := ProgramSourceFrame{}
	if source != nil {
		select {
		case next, open := <-source:
			if open {
				frame = next
			} else {
				p.ClearSource()
			}
		default:
		}
	}
	err := p.compositor.WriteTick(p.clock.Next(), frame)
	if frame.written != nil {
		frame.written <- err
	}
	if err != nil {
		return err
	}
	if p.compositor.OverlayDisabled() {
		p.mu.Lock()
		if !p.overlayNotified {
			p.overlayNotified = true
			callback := p.onOverlayFail
			err := p.compositor.LastOverlayError()
			p.mu.Unlock()
			if callback != nil && err != nil {
				callback(err)
			}
		} else {
			p.mu.Unlock()
		}
	}
	return nil
}

func alphaCompositeRGBA(dst, overlay []byte) {
	for i := 0; i+3 < len(dst) && i+3 < len(overlay); i += 4 {
		a := uint32(overlay[i+3])
		inv := 255 - a
		dst[i] = uint8((uint32(overlay[i])*a + uint32(dst[i])*inv) / 255)
		dst[i+1] = uint8((uint32(overlay[i+1])*a + uint32(dst[i+1])*inv) / 255)
		dst[i+2] = uint8((uint32(overlay[i+2])*a + uint32(dst[i+2])*inv) / 255)
		dst[i+3] = 255
	}
}
