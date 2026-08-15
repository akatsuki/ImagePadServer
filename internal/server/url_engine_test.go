package server

import (
	"strings"
	"testing"

	"imagepadserver/internal/library"
	"imagepadserver/internal/obsrtmp"
)

// TestResolvedShareTargetsMatchCurrentMode は、サーバーが「現在はOBS配信中」と
// 知っている状態で、サーバー由来の状態（shareMode + current + obs）から正しい
// URL が導出されることを契約化する番人。リファクタ全体を通して、OBS 状態なら
// RTSP URL が返ることを守る（クライアント由来の uploadMode に依存させない）。
func TestResolvedShareTargetsMatchCurrentMode(t *testing.T) {
	state := map[string]interface{}{
		"shareMode": "obs", // サーバーが決定した真実源
		"current":   &library.CurrentImage{Kind: "video", SourceKind: "obs"},
		"obs":       obsrtmp.Status{Connected: true, RTSPTURL: "rtsp://127.0.0.1:8554/live"},
		// 実サーバーの obsLatencyProfile() と同様、Mode と Transport の両方を
		// セットした解決済みプロファイル（RTSP-over-TCP）。
		"obsLatency": obsrtmp.LatencyProfile{Mode: obsrtmp.LatencyModeRTSPLow, Transport: obsrtmp.LatencyModeRTSPT},
	}
	shareURL, _ := primaryShareURL(state)
	if !strings.HasPrefix(shareURL, "rtsp://") {
		t.Fatalf("expected OBS RTSP URL, got %q (shareMode/current source-of-truth mismatch)", shareURL)
	}
}

// TestShareContextResolve は型付きリゾルバ ShareContext の主要ケースを検証する。
func TestShareContextResolve(t *testing.T) {
	tests := []struct {
		name string
		ctx  ShareContext
		mode ShareMode
		want string // URL prefix。空なら「URL なし」を期待。
	}{
		{
			name: "image file mode resolves image URL",
			ctx:  ShareContext{Current: &library.CurrentImage{Kind: "image"}, ImageURL: "http://lan/image/current", LocalImageURL: "http://preview/image"},
			mode: ShareModeFile,
			want: "http://lan/image",
		},
		{
			name: "image link mode still resolves image URL",
			ctx:  ShareContext{Current: &library.CurrentImage{Kind: "image"}, ImageURL: "http://lan/image", PublicImageURL: "http://pub/image"},
			mode: ShareModeLink,
			want: "http://lan/image",
		},
		{
			name: "video link mode resolves HLS",
			ctx:  ShareContext{Current: &library.CurrentImage{Kind: "video"}, HLSURL: "http://lan/stream/x.m3u8", VideoPlayerEnabled: true},
			mode: ShareModeLink,
			want: "http://lan/stream",
		},
		{
			name: "music source resolves HLS",
			ctx:  ShareContext{Current: &library.CurrentImage{Kind: "video", SourceKind: "local_audio"}, HLSURL: "http://lan/radio.m3u8", VideoPlayerEnabled: true},
			mode: ShareModeLink,
			want: "http://lan/radio",
		},
		{
			name: "obs rtspt connected resolves RTSP",
			ctx: ShareContext{
				Current:    &library.CurrentImage{Kind: "video", SourceKind: "obs"},
				OBS:        obsrtmp.Status{Connected: true, RTSPTURL: "rtsp://127.0.0.1:8554/live"},
				OBSLatency: obsrtmp.LatencyProfile{Mode: obsrtmp.LatencyModeRTSPLow, Transport: obsrtmp.LatencyModeRTSPT},
			},
			mode: ShareModeOBS,
			want: "rtsp://",
		},
		{
			name: "video without HLS yields empty URL",
			ctx:  ShareContext{Current: &library.CurrentImage{Kind: "video"}},
			mode: ShareModeLink,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ctx.Resolve(tt.mode)
			if tt.want == "" {
				if got.URL != "" {
					t.Fatalf("expected no URL, got %q", got.URL)
				}
				return
			}
			if !strings.HasPrefix(got.URL, tt.want) {
				t.Fatalf("Resolve(%q) = %q, want prefix %q", tt.mode, got.URL, tt.want)
			}
		})
	}
}
