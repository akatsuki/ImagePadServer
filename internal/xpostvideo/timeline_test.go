package xpostvideo

import (
	"imagepadserver/internal/xpostmodel"
	"testing"
)

func TestCompilePreservesBackgroundTheme(t *testing.T) {
	for _, theme := range []string{"", "light", "dark"} {
		o := DefaultOptions()
		o.Theme = theme
		plan, err := Compile([]xpostmodel.Page{{CardPath: "card", PanelPath: "panel"}}, []xpostmodel.Speech{{}}, nil, o)
		if err != nil || plan.Options.Theme != theme {
			t.Fatalf("theme %q was lost: %+v, %v", theme, plan.Options, err)
		}
	}
	o := DefaultOptions()
	o.Theme = "unknown"
	if _, err := Compile([]xpostmodel.Page{{}}, []xpostmodel.Speech{{}}, nil, o); err == nil {
		t.Fatal("unknown theme silently became black")
	}
}

func TestSinglePortraitWaitsForNarrationWithoutCoverFlow(t *testing.T) {
	pages := []xpostmodel.Page{{CardPath: "card", PanelPath: "panel"}}
	speech := []xpostmodel.Speech{{Path: "tts", Duration: 1.25, SampleRate: 24000, Samples: 30000}}
	media := []xpostmodel.Media{{Kind: "video", Width: 720, Height: 1280, Duration: 2, HasAudio: true, Path: "video"}}
	plan, err := Compile(pages, speech, media, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Segments) != 2 || plan.Segments[0].Kind != "narration" || plan.Segments[1].Kind != "video" {
		t.Fatalf("single portrait has intro/transition: %+v", plan.Segments)
	}
	first := plan.FrameAt(0)
	if first.VideoFrame >= 0 || len(first.Layers) != 2 || first.Layers[0].X >= 960 || first.Layers[1].X < 960 {
		t.Fatalf("expected frozen split: %+v", first)
	}
	v := plan.Segments[1]
	if v.Start < 53 || plan.FrameAt(v.Start).VideoFrame != 0 {
		t.Fatalf("video started before speech/tail: %+v", v)
	}
	if len(plan.Audio) != 2 || plan.Audio[1].StartFrame != v.Start {
		t.Fatalf("source audio starts early: %+v", plan.Audio)
	}
}

func TestMixedMediaUsesCoverFlowAndPortraitSlide(t *testing.T) {
	pages := []xpostmodel.Page{{CardPath: "card", PanelPath: "panel"}}
	media := []xpostmodel.Media{{Kind: "image", Width: 800, Height: 1200, Path: "image"}, {Kind: "video", Width: 1920, Height: 1080, Duration: 1, Path: "video"}}
	p, err := Compile(pages, []xpostmodel.Speech{{}}, media, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{"narration", "coverflow", "slide", "image", "coverflow", "video"}
	if len(p.Segments) != len(kinds) {
		t.Fatalf("segments: %+v", p.Segments)
	}
	expected := 0
	for i, s := range p.Segments {
		if s.Kind != kinds[i] || s.Start != expected {
			t.Fatalf("bad segment %d: %+v", i, s)
		}
		expected += s.Frames
	}
	if p.TotalFrames != expected {
		t.Fatal("frame gap")
	}
	last := p.FrameAt(p.TotalFrames - 1)
	if !last.Direct || len(last.Layers) != 1 {
		t.Fatalf("landscape requires unnecessary overlay: %+v", last)
	}
	for _, s := range p.Segments {
		if s.Kind == "coverflow" || s.Kind == "slide" {
			if p.FrameAt(s.Start).VideoFrame >= 0 {
				t.Fatal("video runs during transition")
			}
		}
	}
}

func TestEmptyTextOnlyAndInvalidInput(t *testing.T) {
	p, err := Compile([]xpostmodel.Page{{CardPath: "c", PanelPath: "p"}}, []xpostmodel.Speech{{}}, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if p.TotalFrames != 45 || len(p.Audio) != 0 {
		t.Fatalf("empty narration: %+v", p)
	}
	if _, err := Compile(nil, nil, nil, DefaultOptions()); err == nil {
		t.Fatal("missing pages accepted")
	}
	if _, err := Compile([]xpostmodel.Page{{}}, nil, nil, DefaultOptions()); err == nil {
		t.Fatal("speech mismatch accepted")
	}
}

func TestCoverFlowStartsWithCurrentPortraitPanel(t *testing.T) {
	pages := []xpostmodel.Page{{}, {}, {}}
	media := []xpostmodel.Media{{Kind: "video", Width: 720, Height: 1280, Duration: 5}, {Kind: "image", Width: 1920, Height: 1080}}
	p, err := Compile(pages, make([]xpostmodel.Speech, 3), media, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Segments {
		if s.Kind == "coverflow" && s.FromMedia == 0 {
			last := p.FrameAt(s.Start - 1)
			first := p.FrameAt(s.Start)
			if last.Layers[1].AssetID != first.Layers[1].AssetID {
				t.Fatalf("portrait panel jumps at CoverFlow: %s -> %s", last.Layers[1].AssetID, first.Layers[1].AssetID)
			}
		}
	}
}
