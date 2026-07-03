package ytdlplab

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClassifyYTDLPOutput(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "bot check",
			text: "ERROR: [youtube] id: Sign in to confirm you're not a bot. Use --cookies-from-browser or --cookies",
			want: ClassBotCheck,
		},
		{
			name: "format unavailable",
			text: "ERROR: [youtube] id: Requested format is not available. Use --list-formats",
			want: ClassFormatUnavailable,
		},
		{
			name: "storyboard only",
			text: "[info] Available formats:\nID EXT\nsb0 mhtml 320x180 | mhtml | images storyboard\nsb1 mhtml 160x90 | mhtml | images storyboard",
			want: ClassStoryboardOnly,
		},
		{
			name: "po token",
			text: "web client https formats require a PO Token which was not provided",
			want: ClassPOTokenMissing,
		},
		{
			name: "sabr",
			text: "YouTube is forcing SABR streaming for this client",
			want: ClassSABR,
		},
		{
			name: "n challenge",
			text: "n challenge solving failed: Some formats may be missing",
			want: ClassNChallenge,
		},
		{
			name: "impersonate unavailable",
			text: `yt_dlp.utils.YoutubeDLError: Impersonate target "safari" is not available. Use --list-impersonate-targets to see available targets. You may be missing dependencies required to support this target.`,
			want: ClassImpersonateUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.text)
			if got != tc.want {
				t.Fatalf("Classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestYTDLPVersionAllowsSlowStartup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture is POSIX-only")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 11\necho 2026.06.09\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := ytdlpVersion(context.Background(), script); got != "2026.06.09" {
		t.Fatalf("ytdlpVersion() = %q, want %q", got, "2026.06.09")
	}
}

func TestBuildStrategies(t *testing.T) {
	strategies := BuildStrategies(Options{UseCookies: true, CookiePath: "cookies.txt"})
	if len(strategies) < 13 {
		t.Fatalf("got %d strategies, want at least 13", len(strategies))
	}
	foundCookie := false
	foundMWeb := false
	foundSelectorProbe := false
	foundExactProbe := false
	for _, strategy := range strategies {
		for i := 0; i < len(strategy.Args)-1; i++ {
			if strategy.Args[i] == "--cookies" && strategy.Args[i+1] == "cookies.txt" {
				foundCookie = true
			}
			if strategy.Args[i] == "--extractor-args" && strategy.Args[i+1] == "youtube:player_client=mweb" {
				foundMWeb = true
			}
			if strategy.Args[i] == "-f" && strategy.Args[i+1] == AppFormatSelector && strategy.Probe == "app-selector-simulate" {
				foundSelectorProbe = true
			}
			if strategy.Args[i] == "--impersonate" && strategy.Args[i+1] == "safari" && strategy.Probe == "app-exact-simulate" {
				foundExactProbe = true
			}
		}
	}
	if !foundCookie {
		t.Fatal("cookie-enabled strategies should include --cookies")
	}
	if !foundMWeb {
		t.Fatal("strategies should include mweb client for PO token experiments")
	}
	if !foundSelectorProbe {
		t.Fatal("strategies should include the ImagePadServer app selector simulation")
	}
	if !foundExactProbe {
		t.Fatal("strategies should include the exact ImagePadServer impersonation simulation")
	}
}

func TestParseFormatStats(t *testing.T) {
	text := `[info] Available formats:
ID  EXT   RESOLUTION | PROTO | VCODEC      ACODEC
sb0 mhtml 48x27      | mhtml | images storyboard
18  mp4   640x360    | https | avc1.42001E mp4a.40.2
137 mp4   1920x1080  | https | avc1.640028 video only
140 m4a   audio only | https | audio only mp4a.40.2`
	stats := parseFormatStats(text)
	if stats == nil {
		t.Fatal("parseFormatStats returned nil")
	}
	if stats.Storyboard != 1 || stats.TotalNonStoryboard != 3 || stats.Combined != 1 || stats.Video != 1 || stats.Audio != 1 || stats.MaxHeight != 1080 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}
