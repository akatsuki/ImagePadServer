package video

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClassifyMediaProbeAudioOnly(t *testing.T) {
	p := MediaProbe{Streams: []MediaStream{
		{Index: 0, CodecType: "audio", CodecName: "aac"},
	}}
	if got := ClassifyMediaProbe(p); got != MediaAudio {
		t.Fatalf("ClassifyMediaProbe(audio only) = %q; want %q", got, MediaAudio)
	}
}

func TestClassifyMediaProbeAttachedPictureIsAudio(t *testing.T) {
	p := MediaProbe{Streams: []MediaStream{
		{Index: 0, CodecType: "audio", CodecName: "aac"},
		{Index: 1, CodecType: "video", CodecName: "mjpeg", AttachedPic: true},
	}}
	if got := ClassifyMediaProbe(p); got != MediaAudio {
		t.Fatalf("ClassifyMediaProbe(audio + attached_pic) = %q; want %q", got, MediaAudio)
	}
}

func TestClassifyMediaProbeRealVideo(t *testing.T) {
	p := MediaProbe{Streams: []MediaStream{
		{Index: 0, CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080},
		{Index: 1, CodecType: "audio", CodecName: "aac"},
	}}
	if got := ClassifyMediaProbe(p); got != MediaVideo {
		t.Fatalf("ClassifyMediaProbe(real video + audio) = %q; want %q", got, MediaVideo)
	}
}

func TestClassifyMediaProbeNoPlayableStream(t *testing.T) {
	p := MediaProbe{Streams: nil}
	if got := ClassifyMediaProbe(p); got != MediaUnsupported {
		t.Fatalf("ClassifyMediaProbe(no streams) = %q; want %q", got, MediaUnsupported)
	}
}

func TestParseMediaProbeJSON(t *testing.T) {
	data := []byte(`{"streams":[{"index":0,"codec_type":"audio","codec_name":"aac","tags":{"title":"Song"}}],"format":{"duration":"180.0","tags":{"artist":"Someone"}}}`)
	probe, err := ParseMediaProbeJSON(data)
	if err != nil {
		t.Fatalf("ParseMediaProbeJSON: %v", err)
	}
	if len(probe.Streams) != 1 {
		t.Fatalf("got %d streams; want 1", len(probe.Streams))
	}
	if probe.Streams[0].CodecType != "audio" || probe.Streams[0].CodecName != "aac" {
		t.Fatalf("stream = %+v", probe.Streams[0])
	}
	if probe.Streams[0].Tags["title"] != "Song" {
		t.Fatalf("stream title = %q", probe.Streams[0].Tags["title"])
	}
	if probe.Duration != 180.0 {
		t.Fatalf("duration = %f; want 180.0", probe.Duration)
	}
	if probe.FormatTags["artist"] != "Someone" {
		t.Fatalf("format artist = %q", probe.FormatTags["artist"])
	}
}

func TestParseMediaProbeJSONInvalid(t *testing.T) {
	_, err := ParseMediaProbeJSON([]byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestProbeMedia(t *testing.T) {
	dir := t.TempDir()
	ffprobePath := mustWriteFakeFFprobe(t, dir)

	ctx := context.Background()
	probe, err := ProbeMedia(ctx, ffprobePath, filepath.Join(dir, "test.mp3"))
	if err != nil {
		t.Fatalf("ProbeMedia: %v", err)
	}
	if len(probe.Streams) == 0 {
		t.Fatal("no streams returned")
	}
	if probe.Streams[0].CodecType != "audio" {
		t.Fatalf("first stream codec_type = %q; want audio", probe.Streams[0].CodecType)
	}
}

// mustWriteFakeFFprobe creates a fake ffprobe script that always outputs a
// valid ffprobe JSON response.  Returns the path to the script.
func mustWriteFakeFFprobe(t *testing.T, dir string) string {
	t.Helper()
	var path string
	var content string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, "ffprobe_test.bat")
		content = "@echo off\r\n" +
			`echo {"streams":[{"index":0,"codec_type":"audio","codec_name":"aac","tags":{"title":"Song"}}],"format":{"duration":"180.0","tags":{"artist":"Someone"}}}` + "\r\n"
	} else {
		path = filepath.Join(dir, "ffprobe_test")
		content = "#!/bin/sh\n" +
			`echo '{"streams":[{"index":0,"codec_type":"audio","codec_name":"aac","tags":{"title":"Song"}}],"format":{"duration":"180.0","tags":{"artist":"Someone"}}}'` + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}
