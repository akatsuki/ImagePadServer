package server

import (
	"context"
	"errors"
	"fmt"
	"imagepadserver/internal/library"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
	"imagepadserver/internal/xpostexport"
	"imagepadserver/internal/xpostimage"
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xpostvideo"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type xPostRequest struct {
	Mode  string            `json:"mode"`
	Voice *xpostmodel.Voice `json:"voice,omitempty"`
}

func (s *Server) handleXPostVideoUpload(w http.ResponseWriter, r *http.Request, url, theme string, x xPostRequest, queue bool) bool {
	if x.Mode == "" || x.Mode == "image" {
		return false
	}
	if x.Mode != "video" || !xpostimage.IsPostURL(url) {
		http.Error(w, "X動画にはXの投稿URLを入力してください", http.StatusBadRequest)
		return true
	}
	if !s.videoPlayerEnabled() {
		http.Error(w, "X動画は動画プレイヤー有効時のみ利用できます", http.StatusBadRequest)
		return true
	}
	if !s.tryBeginIngest(ingestProcessing, "Xの投稿動画を生成中") {
		http.Error(w, "他のメディアを処理中です", http.StatusConflict)
		return true
	}
	defer s.clearIngest()
	result, err := s.processXPostVideoURL(r, url, theme, x.Voice, queue)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	writeJSON(w, result)
	return true
}

func findXPostCompositor() (string, error) {
	name := "xpost-compositord"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if override := strings.TrimSpace(os.Getenv("IMAGEPAD_XPOST_COMPOSITOR")); override != "" {
		if stat, err := os.Stat(override); err == nil && !stat.IsDir() {
			return filepath.Abs(override)
		}
		return "", errors.New("IMAGEPAD_XPOST_COMPOSITORの実行ファイルがありません")
	}
	exe, _ := os.Executable()
	candidates := []string{filepath.Join(filepath.Dir(exe), name), filepath.Join(filepath.Dir(exe), "xpost-compositord", name), filepath.Join("build", "xpost-compositord", name)}
	for _, p := range candidates {
		if stat, err := os.Stat(p); err == nil && !stat.IsDir() {
			return filepath.Abs(p)
		}
	}
	return "", errors.New("X動画用のwgpu描画エンジンがありません。scripts/build-xpost-compositor.ps1 または .sh でビルドしてください")
}

func (s *Server) processXPostVideoURL(r *http.Request, url, theme string, voice *xpostmodel.Voice, queue bool) (map[string]interface{}, error) {
	appSettings, err := settings.Load()
	if err != nil {
		return nil, err
	}
	ffmpeg, err := ensureFFmpeg()
	if err != nil {
		return nil, err
	}
	ffprobe, err := video.EnsureFFprobe()
	if err != nil {
		return nil, err
	}
	compositor, err := findXPostCompositor()
	if err != nil {
		return nil, err
	}
	fonts, err := video.VisualizerFonts()
	if err != nil {
		return nil, err
	}
	runID := "run-" + randomSuffix()
	mediaID := "xpost-" + randomSuffix()
	staging, err := filepath.Abs(filepath.Join(s.store.Dir(), ".xpost-export-"+runID))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(staging, 0700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	expectedRevision := s.store.PublishedRevision()
	preset := s.videoQualityPreset()
	options := xpostvideo.DefaultOptions()
	if appSettings.VideoQualityMode == "360" || appSettings.VideoQualityMode == "720" {
		options.Height = preset.Height
		options.Width = options.Height * 16 / 9
		options.Width -= options.Width % 2
	}
	request := xpostexport.Request{JobID: runID, URL: url, Theme: theme, FontPath: fonts.Regular400, WorkDir: staging, FFmpeg: ffmpeg, FFprobe: ffprobe, Compositor: compositor, Voice: voice, Options: options, EncoderMode: settings.NormalizeEncoderMode(appSettings.EncoderMode), CRF: preset.CRF, AudioBitrate: preset.AudioBitrate}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.lifecycleContext(), cancel)
	defer stop()
	result, budget, err := xpostRunWorker(ctx, request, s.setIngestProgress)
	if err != nil {
		return nil, err
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	publicName := "current-video.mp4"
	if queue {
		publicName = "queued-video.mp4"
	}
	committed, err := s.store.CommitPreparedVideo(library.PreparedVideo{
		Info:       library.CurrentImage{ID: mediaID, Kind: "video", FileName: "xpost-video-" + runID + ".mp4", PublicName: publicName, ContentType: "video/mp4", OriginalName: fmt.Sprintf("X投稿 %s", result.PostID), Width: request.Options.Width, Height: request.Options.Height},
		SourcePath: result.OutputPath, ThumbnailPath: result.ThumbnailPath, SnapshotPath: result.SnapshotPath, HLSDir: result.HLSDir,
		RunID: runID, SelectCurrent: !queue, ExpectedRevision: expectedRevision, Resolution: "xpost", SnapshotPrefix: "xpost-snapshot",
	})
	if err != nil {
		return nil, err
	}
	s.setIngestProgress(100, "X投稿動画を生成しました")
	state := s.state(r)
	state["xPostVideo"] = map[string]interface{}{"id": committed.ID, "frames": result.Frames, "rendererCalls": result.RendererCalls, "duration": result.Duration, "adapter": result.Adapter, "backend": result.Backend, "encoder": result.Encoder, "cpuBudget": budget}
	if !queue {
		state = s.withClipboardResult(r, state)
	}
	return state, nil
}
