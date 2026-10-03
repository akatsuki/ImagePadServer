package xpostvideo

import (
	"math"
	"strings"
	"testing"

	"imagepadserver/internal/xpostmodel"
)

func TestCompileRejectsIncompleteScrollAndInvalidSampleClock(t *testing.T) {
	p := scrollingPlanFixture(t)
	for _, name := range []string{"card-end", "panel-end", "cues", "card-bounds", "panel-bounds", "sample-overflow", "partial-samples", "negative-samples"} {
		t.Run(name, func(t *testing.T) {
			page, speech := p.Pages[0], p.Narration[0]
			card, panel := *page.CardScroll, *page.PanelScroll
			page.CardScroll, page.PanelScroll = &card, &panel
			switch name {
			case "card-end":
				page.CardEndPath = ""
			case "panel-end":
				page.PanelEndPath = ""
			case "cues":
				speech.Cues = nil
			case "card-bounds":
				card.X = 1900
			case "panel-bounds":
				panel.X = 900
			case "sample-overflow":
				speech.Samples, speech.SampleRate = math.MaxInt64, 1
			case "partial-samples":
				speech.Samples, speech.SampleRate = 10, 0
			case "negative-samples":
				speech.Samples, speech.SampleRate = -1, 24000
			}
			if _, err := Compile([]xpostmodel.Page{page}, []xpostmodel.Speech{speech}, nil, DefaultOptions()); err == nil {
				t.Fatal("invalid scrolling metadata was silently accepted")
			}
		})
	}
}

func TestScrollingPortraitPanelScalesAndHoldsFinalView(t *testing.T) {
	p := scrollingPlanFixture(t)
	o := DefaultOptions()
	o.Width, o.Height = 640, 1080
	media := []xpostmodel.Media{{Kind: "image", Width: 720, Height: 1280}}
	plan, err := Compile(p.Pages, p.Narration, media, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range plan.FrameAt(9 * o.FPS).Layers[2:] {
		if layer.X < 420 || layer.X+layer.Width > 586.68 || layer.Y < 300 || layer.Y+layer.Height > 600.01 {
			t.Fatalf("scaled panel body escaped its viewport: %+v", layer)
		}
	}
	for _, seg := range plan.Segments {
		if seg.Kind == "coverflow" {
			t.Fatal("single portrait acquired CoverFlow")
		}
		if seg.Kind == "image" {
			layers := plan.FrameAt(seg.Start).Layers
			if len(layers) != 2 || layers[1].AssetID != "panel-end-0" {
				t.Fatal("portrait loses final scroll state")
			}
		}
	}
}

func TestNarrationAudioUsesValidatedSampleDuration(t *testing.T) {
	p := scrollingPlanFixture(t)
	speech := p.Narration[0]
	speech.Duration, speech.Path = 0, "narration.wav"
	speech.SampleRate, speech.Samples = 24000, 12*24000
	plan, err := Compile(p.Pages, []xpostmodel.Speech{speech}, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Audio) != 1 || plan.Audio[0].Frames != 12*30 || plan.Audio[0].Path != speech.Path {
		t.Fatal("sample-clock narration lost its audio")
	}
}

func scrollingPlanFixture(t *testing.T) Plan {
	t.Helper()
	view := &xpostmodel.ScrollView{X: 300, Y: 300, Width: 500, Height: 300, ContentHeight: 960, Tiles: []xpostmodel.ScrollTile{{Path: "body", Top: 0, Height: 960}}}
	for i := 0; i < 16; i++ {
		view.Lines = append(view.Lines, xpostmodel.ScrollLine{StartRune: i * 5, EndRune: (i + 1) * 5, Top: float64(i * 60), Bottom: float64((i + 1) * 60)})
	}
	p := xpostmodel.Page{CardPath: "card", PanelPath: "panel", CardEndPath: "card-end", PanelEndPath: "panel-end", SpeechText: strings.Repeat("あ", 80), CardScroll: view, PanelScroll: view}
	s := xpostmodel.Speech{Duration: 12, Cues: []xpostmodel.SpeechCue{{StartRune: 0, EndRune: 40, StartSeconds: 1, EndSeconds: 3}, {StartRune: 40, EndRune: 80, StartSeconds: 7, EndSeconds: 11}}}
	plan, err := Compile([]xpostmodel.Page{p}, []xpostmodel.Speech{s}, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestNarrationScrollUsesMeasuredCuesAndClipsInsideViewport(t *testing.T) {
	p := scrollingPlanFixture(t)
	before, spoken, pause := p.FrameAt(0), p.FrameAt(3*30), p.FrameAt(6*30)
	if len(spoken.Layers) < 2 {
		t.Fatal("narration is still a static paginated card")
	}
	if before.StaticKey == "" || spoken.StaticKey == "" || pause.StaticKey == "" {
		t.Fatal("settled scroll positions lost frame reuse")
	}
	if spoken.StaticKey != pause.StaticKey || before.StaticKey == spoken.StaticKey {
		t.Fatal("scroll follows elapsed/character-count time instead of voice pauses")
	}
	for k := 0; k < p.TotalFrames; k++ {
		f := p.FrameAt(k)
		for _, layer := range f.Layers[1:] {
			if layer.X < 300 || layer.X+layer.Width > 800.01 || layer.Y < 300 || layer.Y+layer.Height > 600.01 {
				t.Fatalf("body escaped fixed viewport at %d: %+v", k, layer)
			}
		}
	}
}

func TestShortNarrationRemainsStaticAndCoverflowKeepsFinalScrollState(t *testing.T) {
	p := scrollingPlanFixture(t)
	short := p.Pages[0]
	short.CardScroll = &xpostmodel.ScrollView{X: 300, Y: 300, Width: 500, Height: 300, ContentHeight: 60, Tiles: []xpostmodel.ScrollTile{{Path: "short", Height: 60}}}
	speech := xpostmodel.Speech{Duration: 4, Cues: []xpostmodel.SpeechCue{{StartRune: 0, EndRune: 80, StartSeconds: .1, EndSeconds: 3.9}}}
	plan, err := Compile([]xpostmodel.Page{short}, []xpostmodel.Speech{speech}, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if plan.FrameAt(0).StaticKey == "" || plan.FrameAt(0).StaticKey != plan.FrameAt(90).StaticKey {
		t.Fatal("short body moves or redraws while speaking")
	}
	media := []xpostmodel.Media{{Kind: "image", Width: 1280, Height: 720, Duration: 1}}
	plan, err = Compile(p.Pages, []xpostmodel.Speech{{Duration: 12, Cues: []xpostmodel.SpeechCue{{StartRune: 0, EndRune: 80, StartSeconds: .1, EndSeconds: 11.9}}}}, media, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range plan.Segments {
		if seg.Kind == "coverflow" {
			frame := plan.FrameAt(seg.Start)
			if len(frame.Layers) == 0 || frame.Layers[0].AssetID != "card-end-0" {
				t.Fatal("CoverFlow jumps back to initial body")
			}
			return
		}
	}
	t.Fatal("missing CoverFlow")
}
