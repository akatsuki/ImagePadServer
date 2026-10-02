package server

import "testing"

func TestShouldUseVideoURLRoute(t *testing.T) {
	cases := []struct {
		name               string
		videoPlayerEnabled bool
		intent             string
		want               bool
	}{
		{name: "image intent uses image processing", videoPlayerEnabled: true, intent: "image", want: false},
		{name: "video intent keeps video processing", videoPlayerEnabled: true, intent: "video", want: true},
		{name: "music intent keeps media player processing", videoPlayerEnabled: true, intent: "music", want: true},
		{name: "missing intent preserves legacy video routing", videoPlayerEnabled: true, want: true},
		{name: "video route is unavailable when the player is disabled", intent: "video", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldUseVideoURLRoute(tc.videoPlayerEnabled, tc.intent); got != tc.want {
				t.Fatalf("shouldUseVideoURLRoute(%v, %q) = %v, want %v", tc.videoPlayerEnabled, tc.intent, got, tc.want)
			}
		})
	}
}
