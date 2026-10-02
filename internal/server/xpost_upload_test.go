package server

import "testing"

func TestClassifyXPostURLRoute(t *testing.T) {
	cases := []struct {
		name               string
		url                string
		videoPlayerEnabled bool
		intent             string
		want               xPostURLRoute
	}{
		{name: "image tab uses tweet image renderer", url: "https://x.com/u/status/2105746593664426206?s=20", videoPlayerEnabled: true, intent: "image", want: xPostURLRouteImage},
		{name: "video tab uses video URL route", url: "https://x.com/u/status/2105746593664426206?s=20", videoPlayerEnabled: true, intent: "video", want: xPostURLRouteVideo},
		{name: "music tab keeps its separate media route", url: "https://twitter.com/u/status/1", videoPlayerEnabled: true, intent: "music", want: xPostURLRouteMusic},
		{name: "legacy request keeps video URL routing", url: "https://x.com/u/status/1/video/1", videoPlayerEnabled: true, want: xPostURLRouteVideo},
		{name: "disabled video player uses image route", url: "https://x.com/u/status/1", intent: "video", want: xPostURLRouteImage},
		{name: "non-post X URL is not a tweet route", url: "https://x.com/u", videoPlayerEnabled: true, intent: "image", want: xPostURLRouteNone},
		{name: "non-X URL is not a tweet route", url: "https://youtube.com/watch?v=1", videoPlayerEnabled: true, intent: "video", want: xPostURLRouteNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyXPostURLRoute(tc.url, tc.videoPlayerEnabled, tc.intent); got != tc.want {
				t.Fatalf("classifyXPostURLRoute(%q, %v, %q) = %v, want %v", tc.url, tc.videoPlayerEnabled, tc.intent, got, tc.want)
			}
		})
	}
}

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
