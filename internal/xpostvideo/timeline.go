// Package xpostvideo defines the output clock independently of codec processes.
package xpostvideo

import (
	"errors"
	"fmt"
	"imagepadserver/internal/xpostmodel"
	"math"
)

type Options struct {
	Width, Height, FPS                                    int
	PhotoSeconds, TailSeconds, CoverSeconds, SlideSeconds float64
	Theme                                                 string `json:",omitempty"`
}

func DefaultOptions() Options {
	return Options{Width: 1920, Height: 1080, FPS: 30, PhotoSeconds: 4, TailSeconds: .5, CoverSeconds: .6, SlideSeconds: .4, Theme: "dark"}
}

type Segment struct {
	Kind                                       string
	Start, Frames, Page, MediaIndex, FromMedia int
}

type AudioSpan struct {
	Path               string
	StartFrame, Frames int
	Media              bool
}

type Draw struct {
	AssetID                            string
	X, Y, Width, Height, Tilt, Opacity float32
	UV                                 *[4]float32 `json:",omitempty"`
}

type Frame struct {
	Layers                 []Draw
	MediaIndex, VideoFrame int
	StaticKey              string
	Direct                 bool
}

type Plan struct {
	Options        Options
	Pages          []xpostmodel.Page
	Media          []xpostmodel.Media
	Segments       []Segment
	Audio          []AudioSpan
	TotalFrames    int
	SinglePortrait bool
	Narration      []xpostmodel.Speech
	Scroll         []PageScroll
}

func Compile(pages []xpostmodel.Page, speech []xpostmodel.Speech, media []xpostmodel.Media, o Options) (Plan, error) {
	p := Plan{Options: o, Pages: append([]xpostmodel.Page(nil), pages...), Media: append([]xpostmodel.Media(nil), media...)}
	if o.Theme != "" && o.Theme != "light" && o.Theme != "dark" {
		return p, errors.New("X video: invalid background theme")
	}
	if len(pages) == 0 || len(pages) != len(speech) {
		return p, errors.New("X video: narration pages and audio must correspond")
	}
	if o.Width < 2 || o.Height < 2 || o.Width > 3840 || o.Height > 2160 || o.Width%2 != 0 || o.Height%2 != 0 || o.FPS < 1 || o.FPS > 60 {
		return p, errors.New("X video: invalid output dimensions or frame rate")
	}
	for _, d := range []float64{o.PhotoSeconds, o.CoverSeconds, o.SlideSeconds} {
		if math.IsNaN(d) || math.IsInf(d, 0) || d <= 0 || d > 60 {
			return p, errors.New("X video: invalid interval")
		}
	}
	if o.TailSeconds < 0 || o.TailSeconds > 10 || math.IsNaN(o.TailSeconds) || math.IsInf(o.TailSeconds, 0) {
		return p, errors.New("X video: invalid narration tail")
	}
	if len(media) > 4 {
		return p, errors.New("X video: maximum four attachments")
	}
	for _, m := range media {
		if m.Width <= 0 || m.Height <= 0 || (m.Kind != "image" && m.Kind != "video") || m.Kind == "video" && (m.Duration <= 0 || math.IsNaN(m.Duration) || math.IsInf(m.Duration, 0) || m.Duration > 24*3600) {
			return p, errors.New("X video: invalid media dimensions or duration")
		}
	}
	p.SinglePortrait = len(media) == 1 && portrait(media[0])
	add := func(kind string, n, page, index, from int) Segment {
		s := Segment{kind, p.TotalFrames, n, page, index, from}
		p.Segments = append(p.Segments, s)
		p.TotalFrames += n
		return s
	}
	for i, a := range speech {
		if a.Duration < 0 || math.IsNaN(a.Duration) || math.IsInf(a.Duration, 0) || a.Duration > 3600 {
			return p, errors.New("X video: invalid narration duration")
		}
		if a.Samples < 0 || a.SampleRate < 0 || (a.Samples > 0) != (a.SampleRate > 0) {
			return p, errors.New("X video: invalid narration sample clock")
		}
		dur := a.Duration
		if a.Samples > 0 {
			dur = float64(a.Samples) / float64(a.SampleRate)
		}
		if dur > 3600 || math.IsNaN(dur) || math.IsInf(dur, 0) {
			return p, errors.New("X video: invalid effective narration duration")
		}
		for _, source := range []struct {
			view    *xpostmodel.ScrollView
			width   int
			endPath string
		}{
			{pages[i].CardScroll, 1920, pages[i].CardEndPath},
			{pages[i].PanelScroll, 960, pages[i].PanelEndPath},
		} {
			if v := source.view; v != nil && (source.endPath == "" || v.Width > source.width || v.Height > 1080 || v.X > source.width-v.Width || v.Y > 1080-v.Height) {
				return p, errors.New("X video: incomplete scrolling end image or out-of-bounds viewport")
			}
		}
		card, err := makeScrollTrack(pages[i].CardScroll, a, pages[i].SpeechText)
		if err != nil {
			return p, err
		}
		panel, err := makeScrollTrack(pages[i].PanelScroll, a, pages[i].SpeechText)
		if err != nil {
			return p, err
		}
		p.Scroll = append(p.Scroll, PageScroll{Card: card, Panel: panel})
		a.Cues = append([]xpostmodel.SpeechCue(nil), a.Cues...)
		p.Narration = append(p.Narration, a)
		if i > 0 && pages[i].Quoted && !pages[i-1].Quoted {
			add("narration", frames(.25, o.FPS), i, -1, -1)
		}
		n := frames(dur, o.FPS)
		if n == 0 {
			n = frames(1.5, o.FPS)
		} else if i == len(speech)-1 {
			n += frames(o.TailSeconds, o.FPS)
		}
		s := add("narration", n, i, -1, -1)
		if a.Path != "" && dur > 0 {
			p.Audio = append(p.Audio, AudioSpan{a.Path, s.Start, frames(dur, o.FPS), false})
		}
	}
	for i, m := range media {
		if !p.SinglePortrait {
			add("coverflow", frames(o.CoverSeconds, o.FPS), len(pages)-1, i, i-1)
			if portrait(m) {
				add("slide", frames(o.SlideSeconds, o.FPS), 0, i, i-1)
			}
		}
		dur := o.PhotoSeconds
		kind := "image"
		if m.Kind == "video" {
			dur = m.Duration
			kind = "video"
		}
		s := add(kind, frames(dur, o.FPS), 0, i, i-1)
		if m.Kind == "video" && m.HasAudio {
			p.Audio = append(p.Audio, AudioSpan{m.Path, s.Start, s.Frames, true})
		}
	}
	return p, nil
}

func frames(seconds float64, fps int) int { return int(math.Ceil(seconds*float64(fps) - 1e-9)) }
func portrait(m xpostmodel.Media) bool    { return m.Height >= m.Width }

func (p Plan) FrameAt(k int) Frame {
	f := Frame{MediaIndex: -1, VideoFrame: -1}
	if k < 0 || k >= p.TotalFrames {
		return f
	}
	for _, s := range p.Segments {
		if k < s.Start || k >= s.Start+s.Frames {
			continue
		}
		local := k - s.Start
		switch s.Kind {
		case "narration":
			t := float64(local) / float64(p.Options.FPS)
			for _, next := range p.Segments {
				if next.Start == s.Start+s.Frames && next.Kind == "narration" && next.Page == s.Page {
					t = 0
					break
				}
			}
			f.Layers, f.StaticKey = p.narrationLayers(s.Page, t, p.SinglePortrait)
		case "image", "video":
			page := 0
			if len(p.Pages) > 1 {
				page = (local / (p.Options.FPS * 4)) % len(p.Pages)
			}
			if p.hasScrollPages() {
				page = len(p.Pages) - 1
			}
			f.Layers = p.mediaLayers(s.MediaIndex, page, false)
			if s.Kind == "video" {
				f.MediaIndex = s.MediaIndex
				f.VideoFrame = local
				f.Direct = !portrait(p.Media[s.MediaIndex])
			} else {
				f.StaticKey = fmt.Sprintf("image-%d-page-%d", s.MediaIndex, page)
			}
		case "slide":
			t := ease(progress(local, s.Frames))
			target := p.mediaRect(s.MediaIndex, true)
			center := p.mediaRect(s.MediaIndex, false)
			target.X = lerp(center.X, target.X, t)
			f.Layers = []Draw{target}
			page := 0
			if p.hasScrollPages() {
				page = len(p.Pages) - 1
			}
			panel := p.panelEnd(page)
			panel.X = lerp(float32(p.Options.Width), panel.X, t)
			f.Layers = append(f.Layers, panel)
		case "coverflow":
			t := ease(progress(local, s.Frames))
			var prev Draw
			if s.FromMedia < 0 {
				prev = p.cardEnd(s.Page)
			} else {
				prev = p.mediaRect(s.FromMedia, portrait(p.Media[s.FromMedia]))
				prev.AssetID = fmt.Sprintf("exit-%d", s.FromMedia)
			}
			prev.X = lerp(prev.X, -prev.Width-float32(p.Options.Width)*.12, t)
			prev.Tilt = -55 * t
			prev.Opacity = 1 - .5*t
			f.Layers = append(f.Layers, prev)
			if s.FromMedia >= 0 && portrait(p.Media[s.FromMedia]) {
				page := 0
				for _, preceding := range p.Segments {
					if (preceding.Kind == "image" || preceding.Kind == "video") && preceding.MediaIndex == s.FromMedia {
						page = ((preceding.Frames - 1) / (p.Options.FPS * 4)) % len(p.Pages)
						break
					}
				}
				panel := p.panel(page)
				if p.hasScrollPages() {
					panel = p.panelEnd(len(p.Pages) - 1)
				}
				panel.Opacity = 1 - t
				f.Layers = append(f.Layers, panel)
			}
			next := p.mediaRect(s.MediaIndex, false)
			next.X = lerp(float32(p.Options.Width)+next.Width*.15, next.X, t)
			next.Tilt = 55 * (1 - t)
			f.Layers = append(f.Layers, next)
		}
		return f
	}
	return f
}

func (p Plan) card(page int) Draw {
	return Draw{AssetID: fmt.Sprintf("card-%d", page), Width: float32(p.Options.Width), Height: float32(p.Options.Height), Opacity: 1}
}
func (p Plan) panel(page int) Draw {
	return Draw{AssetID: fmt.Sprintf("panel-%d", page), X: float32(p.Options.Width) / 2, Width: float32(p.Options.Width) / 2, Height: float32(p.Options.Height), Opacity: 1}
}
func (p Plan) mediaLayers(index, page int, exit bool) []Draw {
	d := p.mediaRect(index, portrait(p.Media[index]))
	if exit {
		d.AssetID = fmt.Sprintf("exit-%d", index)
	}
	out := []Draw{d}
	if portrait(p.Media[index]) {
		panel := p.panel(page)
		if p.hasScrollPages() {
			panel = p.panelEnd(page)
		}
		out = append(out, panel)
	}
	return out
}
func (p Plan) mediaRect(index int, split bool) Draw {
	m := p.Media[index]
	boxW, boxH := float32(p.Options.Width), float32(p.Options.Height)
	if split {
		boxW /= 2
	}
	scale := float32(math.Min(float64(boxW)/float64(m.Width), float64(boxH)/float64(m.Height)))
	w, h := float32(m.Width)*scale, float32(m.Height)*scale
	return Draw{AssetID: fmt.Sprintf("media-%d", index), X: (boxW - w) / 2, Y: (boxH - h) / 2, Width: w, Height: h, Opacity: 1}
}
func progress(k, n int) float32 {
	if n <= 1 {
		return 1
	}
	return float32(k) / float32(n-1)
}
func ease(t float32) float32       { return t * t * (3 - 2*t) }
func lerp(a, b, t float32) float32 { return a + (b-a)*t }
