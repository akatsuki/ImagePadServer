package xpostvideo

import (
	"fmt"
	"math"
	"unicode/utf8"

	"imagepadserver/internal/xpostmodel"
)

type ScrollStep struct{ Start, End, From, To float64 }
type PageScroll struct{ Card, Panel []ScrollStep }

func finiteScroll(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func makeScrollTrack(view *xpostmodel.ScrollView, speech xpostmodel.Speech, text string) ([]ScrollStep, error) {
	if view == nil {
		return nil, nil
	}
	if view.Width <= 0 || view.Height <= 0 || view.ContentHeight <= 0 || view.X < 0 || view.Y < 0 {
		return nil, fmt.Errorf("X video: invalid scrolling viewport")
	}
	bottom := 0
	for _, tile := range view.Tiles {
		if tile.Path == "" || tile.Top != bottom || tile.Height <= 0 || tile.Height > 1024 {
			return nil, fmt.Errorf("X video: invalid scrolling tile")
		}
		bottom += tile.Height
	}
	if bottom != view.ContentHeight {
		return nil, fmt.Errorf("X video: incomplete scrolling tiles")
	}
	runes := utf8.RuneCountInString(text)
	previousRune, previousY := 0, 0.0
	for _, line := range view.Lines {
		if line.StartRune < previousRune || line.EndRune < line.StartRune || line.EndRune > runes || !finiteScroll(line.Top) || !finiteScroll(line.Bottom) || line.Top < previousY || line.Bottom <= line.Top || line.Bottom > float64(view.ContentHeight)+1 {
			return nil, fmt.Errorf("X video: invalid scrolling line map")
		}
		previousRune, previousY = line.EndRune, line.Bottom
	}
	duration := speech.Duration
	if speech.Samples > 0 && speech.SampleRate > 0 {
		duration = float64(speech.Samples) / float64(speech.SampleRate)
	}
	return scrollSteps(view, speech.Cues, runes, duration)
}

func scrollSteps(view *xpostmodel.ScrollView, cues []xpostmodel.SpeechCue, runes int, duration float64) ([]ScrollStep, error) {
	previousRune, previousTime := 0, 0.0
	for _, cue := range cues {
		if cue.StartRune < previousRune || cue.EndRune <= cue.StartRune || cue.EndRune > runes || !finiteScroll(cue.StartSeconds) || !finiteScroll(cue.EndSeconds) || cue.StartSeconds < previousTime || cue.EndSeconds <= cue.StartSeconds || cue.EndSeconds > duration+1e-6 {
			return nil, fmt.Errorf("X video: invalid narration scroll timing")
		}
		previousRune, previousTime = cue.EndRune, cue.EndSeconds
	}
	limit := float64(max(0, view.ContentHeight-view.Height))
	if limit > 0 && len(cues) == 0 {
		return nil, fmt.Errorf("X video: overflowing body requires narration timing")
	}
	if limit == 0 {
		return nil, nil
	}
	var steps []ScrollStep
	goal := 0.0
	for _, line := range view.Lines {
		if line.EndRune == line.StartRune {
			continue
		}
		target := min(limit, max(0, line.Bottom-float64(view.Height)*.65))
		if target <= goal {
			continue
		}
		start := max(0, timeForRune(cues, line.StartRune)-.12)
		if len(steps) > 0 && start <= steps[len(steps)-1].Start {
			steps[len(steps)-1].To = target
			goal = target
			continue
		}
		steps = append(steps, ScrollStep{Start: start, End: min(start+.45, cueEndForRune(cues, line.StartRune)), From: goal, To: target})
		goal = target
	}
	if goal < limit {
		start := max(0, min(cues[len(cues)-1].EndSeconds, duration-.45))
		if len(steps) > 0 && start <= steps[len(steps)-1].Start {
			steps[len(steps)-1].To = limit
		} else {
			steps = append(steps, ScrollStep{Start: start, End: duration, From: goal, To: limit})
		}
	}
	for i := range steps {
		steps[i].End = min(steps[i].End, duration)
		if i+1 < len(steps) {
			steps[i].End = min(steps[i].End, steps[i+1].Start)
		}
	}
	return steps, nil
}

func timeForRune(cues []xpostmodel.SpeechCue, r int) float64 {
	for _, cue := range cues {
		if r < cue.StartRune {
			return cue.StartSeconds
		}
		if r < cue.EndRune {
			return cue.StartSeconds + (cue.EndSeconds-cue.StartSeconds)*float64(r-cue.StartRune)/float64(cue.EndRune-cue.StartRune)
		}
	}
	return cues[len(cues)-1].EndSeconds
}

func cueEndForRune(cues []xpostmodel.SpeechCue, r int) float64 {
	for _, cue := range cues {
		if r < cue.EndRune {
			return cue.EndSeconds
		}
	}
	return cues[len(cues)-1].EndSeconds
}

func scrollOffset(steps []ScrollStep, t float64) float64 {
	value := 0.0
	for _, step := range steps {
		if t < step.Start {
			break
		}
		if t >= step.End || step.End <= step.Start {
			value = step.To
			continue
		}
		p := (t - step.Start) / (step.End - step.Start)
		return step.From + (step.To-step.From)*p*p*(3-2*p)
	}
	return value
}

func (p Plan) hasScrollPages() bool { return len(p.Pages) > 0 && p.Pages[0].CardScroll != nil }
func (p Plan) cardEnd(page int) Draw {
	d := p.card(page)
	if p.Pages[page].CardEndPath != "" {
		d.AssetID = fmt.Sprintf("card-end-%d", page)
	}
	return d
}
func (p Plan) panelEnd(page int) Draw {
	d := p.panel(page)
	if p.Pages[page].PanelEndPath != "" {
		d.AssetID = fmt.Sprintf("panel-end-%d", page)
	}
	return d
}

func (p Plan) narrationLayers(page int, t float64, split bool) ([]Draw, string) {
	view := p.Pages[page].CardScroll
	base := p.card(page)
	kind := "card"
	var steps []ScrollStep
	if page < len(p.Scroll) {
		steps = p.Scroll[page].Card
	}
	layers := []Draw{base}
	if split {
		view = p.Pages[page].PanelScroll
		kind = "panel"
		layers = []Draw{p.mediaRect(0, true), p.panel(page)}
		if page < len(p.Scroll) {
			steps = p.Scroll[page].Panel
		}
	}
	if view == nil || len(steps) == 0 {
		return layers, fmt.Sprintf("%s-%d-static", kind, page)
	}
	offset := scrollOffset(steps, t)
	sx, sy := float32(p.Options.Width)/1920, float32(p.Options.Height)/1080
	translate := float32(0)
	if split {
		translate = float32(p.Options.Width) / 2
	}
	for i, tile := range view.Tiles {
		start, end := max(offset, float64(tile.Top)), min(offset+float64(view.Height), float64(tile.Top+tile.Height))
		if end <= start {
			continue
		}
		uv := &[4]float32{0, float32((start - float64(tile.Top)) / float64(tile.Height)), 1, float32((end - float64(tile.Top)) / float64(tile.Height))}
		layers = append(layers, Draw{AssetID: fmt.Sprintf("%s-body-%d-%d", kind, page, i), X: translate + float32(view.X)*sx, Y: (float32(view.Y) + float32(start-offset)) * sy, Width: float32(view.Width) * sx, Height: float32(end-start) * sy, Opacity: 1, UV: uv})
	}
	return layers, fmt.Sprintf("%s-%d-scroll-%016x", kind, page, math.Float64bits(offset))
}
