package obsrtmp

import (
	"errors"
	"sync"
	"time"
)

// GPUSceneKind identifies the non-source scenes that the compositor must be
// able to render without rebuilding the Go-side frame. The GPU sidecar maps
// these values to WGSL pipelines.
type GPUSceneKind string

const (
	GPUSceneFallback   GPUSceneKind = "fallback"
	GPUSceneTransition GPUSceneKind = "transition"
	GPUScenePlaylist   GPUSceneKind = "playlist"
)

// GPUScene is renderer-neutral input for a fallback/transition frame. Texture
// keys are stable asset identifiers; the sidecar owns their GPU residency.
type GPUScene struct {
	Kind       GPUSceneKind
	TextureKey string
	ProgramPTS time.Duration
	Progress   float32
	Publisher  string
	TextOwner  GPUTextOwner
}

type GPUTextOwner string

const (
	GPUTextOwnerASS   GPUTextOwner = "ass"
	GPUTextOwnerGlyph GPUTextOwner = "glyph"
)

// Validate enforces the single-owner text rule. A GPU scene using glyphs must
// not also request an ASS overlay from FFmpeg.
func (s GPUScene) Validate() error {
	if s.Kind == "" {
		return errors.New("gpu scene kind is required")
	}
	if s.TextOwner != GPUTextOwnerASS && s.TextOwner != GPUTextOwnerGlyph {
		return errors.New("gpu scene text owner must be ass or glyph")
	}
	if s.Progress < 0 || s.Progress > 1 {
		return errors.New("gpu scene progress must be between 0 and 1")
	}
	return nil
}

// StaticTextureCache prevents repeated uploads of fallback artwork and logo
// textures during playlist transitions. Values are immutable byte snapshots.
type StaticTextureCache struct {
	mu    sync.RWMutex
	items map[string][]byte
}

func NewStaticTextureCache() *StaticTextureCache {
	return &StaticTextureCache{items: make(map[string][]byte)}
}

func (c *StaticTextureCache) Get(key string) ([]byte, bool) {
	c.mu.RLock()
	value, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return append([]byte(nil), value...), true
}

func (c *StaticTextureCache) Put(key string, value []byte) {
	if key == "" || len(value) == 0 {
		return
	}
	c.mu.Lock()
	c.items[key] = append([]byte(nil), value...)
	c.mu.Unlock()
}

func (c *StaticTextureCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// FFmpegASSArgs returns no ASS filter when text is already rendered by the
// GPU. Callers can keep the existing ASS path for GPUTextOwnerASS.
func FFmpegASSArgs(owner GPUTextOwner, assPath, fontDir string) []string {
	if owner == GPUTextOwnerGlyph || assPath == "" {
		return nil
	}
	return []string{"ass=filename='" + assPath + "':fontsdir='" + fontDir + "'"}
}
