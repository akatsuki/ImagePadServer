package video

import (
	"strings"
	"testing"
)

func TestParsePlaylistGPUArtifactProbeJSONAcceptsBT709LimitedAVMatch(t *testing.T) {
	data := []byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":640,"height":360,"pix_fmt":"yuv420p","color_space":"bt709","color_range":"tv","nb_read_frames":"3","duration":"0.100000"},{"codec_type":"audio","codec_name":"aac","duration":"0.110000"}]}`)
	report, err := ParsePlaylistGPUArtifactProbeJSON(data, 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !report.BT709Limited || report.VideoFrames != 3 || report.DurationDeltaFrames > 1 {
		t.Fatalf("unexpected artifact report: %+v", report)
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParsePlaylistGPUArtifactProbeJSONRejectsGateViolations(t *testing.T) {
	base := `{"streams":[{"codec_type":"video","codec_name":"h264","width":640,"height":360,"pix_fmt":"yuv420p","color_space":"bt709","color_range":"tv","nb_read_frames":"3","duration":"0.100000"},{"codec_type":"audio","codec_name":"aac","duration":"0.110000"}]}`
	cases := []struct {
		name   string
		mutate func(string) string
	}{
		{name: "wrong frame count", mutate: func(s string) string { return strings.Replace(s, `"nb_read_frames":"3"`, `"nb_read_frames":"2"`, 1) }},
		{name: "wrong color range", mutate: func(s string) string { return strings.Replace(s, `"color_range":"tv"`, `"color_range":"pc"`, 1) }},
		{name: "duration mismatch", mutate: func(s string) string { return strings.Replace(s, `"duration":"0.110000"`, `"duration":"0.200000"`, 1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePlaylistGPUArtifactProbeJSON([]byte(tc.mutate(base)), 30, 3); err == nil {
				t.Fatal("artifact gate violation must fail closed")
			}
		})
	}
}
