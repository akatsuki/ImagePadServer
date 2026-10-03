package xpostexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"imagepadserver/internal/video"
	"imagepadserver/internal/xpostcodec"
	"imagepadserver/internal/xpostimage"
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xposttts"
	"imagepadserver/internal/xpostvideo"
)

func Run(ctx context.Context, r Request, out, diagnostics io.Writer) (runErr error) {
	emit := func(e Event) error { e.JobID = r.JobID; return WriteEvent(out, e) }
	defer func() {
		if runErr != nil {
			_ = emit(Event{Type: "result", Error: runErr.Error()})
		}
	}()
	if err := r.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(r.WorkDir, 0700); err != nil {
		return err
	}
	progress := func(p int, m string) error { return emit(Event{Type: "progress", Percent: p, Message: m}) }
	if err := progress(2, "Xの投稿を取得中"); err != nil {
		return err
	}
	post, err := xpostimage.FetchVideoPost(ctx, r.URL)
	if err != nil {
		return err
	}
	bodies := []xpostmodel.Post{post}
	if post.Quoted != nil {
		bodies = append(bodies, *post.Quoted)
	}
	hasSpeech := false
	for _, body := range bodies {
		text, err := xposttts.BuildSpeechText(body)
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) != "" {
			hasSpeech = true
		}
	}
	var client *xposttts.Client
	credit := ""
	if hasSpeech {
		if r.Voice == nil {
			return errors.New("VOICEVOXの声を選択してください")
		}
		client, err = xposttts.NewClient(r.Voice.EngineURL)
		if err != nil {
			return err
		}
		speakers, err := client.Speakers(ctx)
		if err != nil {
			return fmt.Errorf("VOICEVOXを起動してから再実行してください: %w", err)
		}
		voice, err := ValidateVoice(*r.Voice, speakers)
		if err != nil {
			return err
		}
		r.Voice = &voice
		if err := emit(Event{Type: "voice", Voice: r.Voice}); err != nil {
			return err
		}
		credit = "VOICEVOX: " + voice.SpeakerName
	}
	if err := progress(8, "投稿ページと添付メディアを準備中"); err != nil {
		return err
	}
	assets, err := xpostimage.PrepareVideoPost(ctx, post, r.Theme, r.FontPath, r.WorkDir, credit)
	if err != nil {
		return err
	}
	media, err := xpostcodec.PrepareMediaWithTheme(ctx, r.FFmpeg, r.FFprobe, assets.Media, r.WorkDir, r.Options.Width, r.Options.Height, r.Options.FPS, r.Theme)
	if err != nil {
		return err
	}
	speeches := make([]xpostmodel.Speech, len(assets.Pages))
	for i, page := range assets.Pages {
		if strings.TrimSpace(page.SpeechText) == "" {
			continue
		}
		if client == nil {
			return errors.New("unexpected narration without voice")
		}
		if err := progress(15, "投稿内容を読み上げ音声に変換中"); err != nil {
			return err
		}
		speeches[i], err = client.SynthesizeTimed(ctx, page.SpeechText, *r.Voice, filepath.Join(r.WorkDir, fmt.Sprintf("speech-%d.wav", i)))
		if err != nil {
			return err
		}
	}
	r.Options.Theme = r.Theme
	plan, err := xpostvideo.Compile(assets.Pages, speeches, media, r.Options)
	if err != nil {
		return err
	}
	encoder, err := selectEncoder(ctx, r.FFmpeg, r.EncoderMode)
	if err != nil {
		return err
	}
	var progressErr error
	started := time.Now()
	frameProgress := frameProgress{started: started, lastUpdate: started}
	if err := progress(20, frameProgressText(0, plan.TotalFrames, 0)); err != nil {
		return err
	}
	encoded, err := xpostcodec.Encode(ctx, xpostcodec.Options{
		FFmpeg: r.FFmpeg, FFprobe: r.FFprobe, Compositor: r.Compositor, OutDir: r.WorkDir,
		OutputPath: filepath.Join(r.WorkDir, "output.mp4"), Encoder: encoder, CRF: r.CRF, AudioBitrate: r.AudioBitrate,
		Progress: func(done, total int) {
			if progressErr == nil {
				if message, ok := frameProgress.update(done, total, time.Now()); ok {
					progressErr = progress(20+done*70/total, message)
				}
			}
		},
	}, plan)
	if err != nil {
		return err
	}
	if progressErr != nil {
		return progressErr
	}
	if err := progress(95, fmt.Sprintf("最終準備中（%d / %d フレーム変換済み）", plan.TotalFrames, plan.TotalFrames)); err != nil {
		return err
	}
	thumb := filepath.Join(r.WorkDir, "thumbnail.jpg")
	if len(assets.Pages) == 0 {
		return errors.New("投稿ページがありません")
	}
	if err := makeThumbnail(assets.Pages[0].CardPath, thumb); err != nil {
		return err
	}
	snapshot := filepath.Join(r.WorkDir, "snapshot.json")
	data, err := json.MarshalIndent(struct {
		Request Request
		Post    xpostmodel.Post
		Plan    xpostvideo.Plan
	}{r, post, plan}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(snapshot, append(data, '\n'), 0600); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	result := &Result{OutputPath: encoded.OutputPath, HLSDir: encoded.HLSDir, SnapshotPath: snapshot, ThumbnailPath: thumb, PostID: post.ID, Frames: encoded.Frames, RendererCalls: encoded.RendererCalls, Duration: encoded.Duration, Adapter: encoded.Adapter, Backend: encoded.Backend, Encoder: encoder}
	return emit(Event{Type: "result", Result: result})
}

func selectEncoder(ctx context.Context, ffmpeg, mode string) (string, error) {
	if mode == "cpu" {
		return "libx264", nil
	}
	for _, name := range video.EncoderPriority(runtime.GOOS) {
		if name == "libx264" {
			continue
		}
		p := video.NewVideoEncoderProfile(name, video.EncoderStandard)
		if err := video.PreflightVideoEncoder(ctx, ffmpeg, p); err == nil {
			return name, nil
		}
	}
	return "", errors.New("利用できるGPUエンコーダーがありません。CPUエンコーダーへ切り替えてください")
}

func makeThumbnail(source, destination string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	im, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	err = jpeg.Encode(out, im, &jpeg.Options{Quality: 85})
	return errors.Join(err, out.Close())
}
