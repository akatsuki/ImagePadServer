package video

import (
	"strings"
	"testing"
)

func TestResolveQualityForMusicIsMoreCompressedThanUpload(t *testing.T) {
	music := ResolveQualityForMusic("720", 0, 0)
	upload := ResolveQualityForUpload("720", 0, 0)

	if music.CRF <= upload.CRF {
		t.Fatalf("music CRF %d should be higher than upload CRF %d", music.CRF, upload.CRF)
	}
	if music.MaxRate == "" || upload.MaxRate == "" {
		t.Fatal("missing maxrate")
	}
	musicMax := parseBitrateK(t, music.MaxRate)
	uploadMax := parseBitrateK(t, upload.MaxRate)
	if musicMax >= uploadMax {
		t.Fatalf("music maxrate %dk should be lower than upload maxrate %dk", musicMax, uploadMax)
	}
	// 720p upload MaxRate is 3000k; music 30% -> 900k.
	if musicMax != 900 {
		t.Fatalf("music maxrate = %dk, want 900k", musicMax)
	}
}

func TestResolveQualityForMusicTargetsSmallLongFiles(t *testing.T) {
	preset := ResolveQualityForMusic("auto", 100, 20)
	// 5 minutes at the 1080p music ceiling: ~1560k * 300 / 8 / 1024 = ~57 MB max,
	// but CRF 28 keeps the average far below that. For 720p the ceiling is
	// ~900k -> ~33 MB max.
	if preset.CRF < 26 || preset.CRF > 40 {
		t.Fatalf("CRF = %d, want 26-40", preset.CRF)
	}
}

func TestAdaptQualityPresetToSourceDoesNotUpscale(t *testing.T) {
	preset := ResolveQualityForUpload("1080", 0, 0)
	probe := MediaProbe{Streams: []MediaStream{
		{CodecType: "video", Width: 1280, Height: 720},
	}}

	adapted := AdaptQualityPresetToSource(preset, probe)

	if adapted.Height != 720 {
		t.Fatalf("height = %d, want source height 720", adapted.Height)
	}
	if adapted.Effective != "720" {
		t.Fatalf("effective = %q, want 720", adapted.Effective)
	}
	if adapted.VideoBitrate != ResolveQualityForUpload("720", 0, 0).VideoBitrate {
		t.Fatalf("video bitrate = %q, want 720p bitrate", adapted.VideoBitrate)
	}
	if adapted.CRF != ResolveQualityForUpload("720", 0, 0).CRF {
		t.Fatalf("CRF = %d, want 720p CRF", adapted.CRF)
	}
	if adapted.Mode != "1080" {
		t.Fatalf("mode = %q, want original setting 1080", adapted.Mode)
	}
}

func TestAdaptQualityPresetToSourceKeepsLowerSetting(t *testing.T) {
	preset := ResolveQualityForUpload("720", 0, 0)
	probe := MediaProbe{Streams: []MediaStream{
		{CodecType: "video", Width: 1920, Height: 1080},
	}}

	adapted := AdaptQualityPresetToSource(preset, probe)

	if adapted.Height != 720 || adapted.Effective != "720" {
		t.Fatalf("adapted = %+v, want 720p unchanged", adapted)
	}
}

func TestAdaptQualityPresetToSourceRoundsOddSourceHeight(t *testing.T) {
	preset := ResolveQualityForUpload("1080", 0, 0)
	probe := MediaProbe{Streams: []MediaStream{
		{CodecType: "video", Width: 853, Height: 481},
	}}

	adapted := AdaptQualityPresetToSource(preset, probe)

	if adapted.Height != 480 || adapted.Effective != "480" {
		t.Fatalf("adapted = %+v, want even 480p source cap", adapted)
	}
}

func TestAdaptQualityPresetToSourceMarksInterlacedInput(t *testing.T) {
	preset := ResolveQualityForUpload("1080", 0, 0)
	probe := MediaProbe{Streams: []MediaStream{
		{CodecType: "video", Width: 1920, Height: 1080, FieldOrder: "tt"},
	}}

	adapted := AdaptQualityPresetToSource(preset, probe)

	if !adapted.Deinterlace {
		t.Fatal("expected interlaced source to request deinterlace")
	}
}

func TestUploadedHLSArgsInsertYADIFForInterlacedInput(t *testing.T) {
	preset := ResolveQualityForUpload("1080", 0, 0)
	preset.Deinterlace = true
	args := uploadedHLSArgsWithEncoder("source.mp4", "media-id", preset, CPUVideoEncoder(EncoderStandard))
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-vf yadif,scale=") {
		t.Fatalf("uploaded HLS args should deinterlace before scaling: %s", joined)
	}
}

func TestUploadedVideoQueueUsesAdaptedQualityLabel(t *testing.T) {
	outDir := t.TempDir()
	preset := ResolveQualityForUpload("1080", 0, 0)
	adapted := AdaptQualityPresetToSource(preset, MediaProbe{Streams: []MediaStream{
		{CodecType: "video", Width: 1280, Height: 720},
	}})
	EnqueueUploadedVideoForID("source.mp4", outDir, "media-id", "source.mp4", adapted, 0)
	defer CancelQueue(outDir)

	items := QueueStatus(outDir)
	if len(items) == 0 {
		t.Fatal("queue is empty")
	}
	if items[0].Quality != "720" {
		t.Fatalf("queue quality = %q, want adapted 720", items[0].Quality)
	}
}

func parseBitrateK(t *testing.T, s string) int {
	t.Helper()
	s = strings.TrimSpace(s)
	unit := ""
	if s[len(s)-1] < '0' || s[len(s)-1] > '9' {
		unit = s[len(s)-1:]
		s = s[:len(s)-1]
	}
	v := 0
	for _, c := range s {
		v = v*10 + int(c-'0')
	}
	if unit == "m" || unit == "M" {
		v *= 1000
	}
	return v
}
