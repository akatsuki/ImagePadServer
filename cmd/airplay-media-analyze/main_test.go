package main

import (
	"testing"
	"time"
)

func TestMatchMarkersMeasuresOffsetAndDrift(t *testing.T) {
	video := []Marker{{PTS: time.Second}, {PTS: 2 * time.Second}, {PTS: 3 * time.Second}}
	audio := []Marker{{PTS: 950 * time.Millisecond}, {PTS: 1950 * time.Millisecond}, {PTS: 2950 * time.Millisecond}}
	got, err := MatchMarkers(video, audio, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got.FirstOffset != 50*time.Millisecond || got.LastOffset != 50*time.Millisecond || got.Drift != 0 || got.MaxAbsoluteOffset != 50*time.Millisecond {
		t.Fatalf("%#v", got)
	}
}

func TestMatchMarkersRejectsMissingAndDistantMarkers(t *testing.T) {
	if _, err := MatchMarkers(nil, []Marker{{PTS: time.Second}}, 200*time.Millisecond); err == nil {
		t.Fatal("missing video markers accepted")
	}
	if _, err := MatchMarkers([]Marker{{PTS: time.Second}}, []Marker{{PTS: 2 * time.Second}}, 200*time.Millisecond); err == nil {
		t.Fatal("distant markers accepted")
	}
}

func TestParseVideoMarkersUsesRisingThreshold(t *testing.T) {
	metadata := `frame:0 pts:0 pts_time:0.000
lavfi.signalstats.YAVG=10
frame:1 pts:1 pts_time:0.950
lavfi.signalstats.YAVG=221
frame:2 pts:2 pts_time:1.000
lavfi.signalstats.YAVG=240
frame:3 pts:3 pts_time:1.200
lavfi.signalstats.YAVG=20
frame:4 pts:4 pts_time:1.950
lavfi.signalstats.YAVG=225
`
	got, err := ParseVideoMarkers(metadata, 220)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].PTS != 950*time.Millisecond || got[1].PTS != 1950*time.Millisecond {
		t.Fatalf("%#v", got)
	}
}

func TestParseAudioMarkersUsesRisingThreshold(t *testing.T) {
	metadata := `frame:0 pts:0 pts_time:0.000
lavfi.astats.Overall.RMS_level=-80
frame:1 pts:1 pts_time:0.900
lavfi.astats.Overall.RMS_level=-11
frame:2 pts:2 pts_time:0.950
lavfi.astats.Overall.RMS_level=-8
frame:3 pts:3 pts_time:1.200
lavfi.astats.Overall.RMS_level=-60
frame:4 pts:4 pts_time:1.900
lavfi.astats.Overall.RMS_level=-10
`
	got, err := ParseAudioMarkers(metadata, -12)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].PTS != 900*time.Millisecond || got[1].PTS != 1900*time.Millisecond {
		t.Fatalf("%#v", got)
	}
}

func TestAspectWithinToleranceHandlesPortraitAndLandscape(t *testing.T) {
	if !AspectWithinTolerance(360, 640, 720, 1280, 0.02) {
		t.Fatal("portrait aspect rejected")
	}
	if !AspectWithinTolerance(640, 360, 1280, 720, 0.02) {
		t.Fatal("landscape aspect rejected")
	}
	if AspectWithinTolerance(360, 640, 1280, 720, 0.02) {
		t.Fatal("freely stretched portrait accepted")
	}
}
