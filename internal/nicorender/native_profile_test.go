//go:build nico_native_profile

package nicorender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

type nicoNativeProfileBrowser struct {
	ConstructorCalls int     `json:"constructorCalls"`
	ConstructorMs    float64 `json:"constructorMs"`
	StrokeTextCalls  int     `json:"strokeTextCalls"`
	StrokeTextMs     float64 `json:"strokeTextMs"`
	FillTextCalls    int     `json:"fillTextCalls"`
	FillTextMs       float64 `json:"fillTextMs"`
	MeasureTextCalls int     `json:"measureTextCalls"`
	MeasureTextMs    float64 `json:"measureTextMs"`
	DrawImageCalls   int     `json:"drawImageCalls"`
	DrawImageMs      float64 `json:"drawImageMs"`
	ReadPixelsCalls  int     `json:"readPixelsCalls"`
	ReadPixelsMs     float64 `json:"readPixelsMs"`
	TexImage2DCalls  int     `json:"texImage2DCalls"`
	TexImage2DMs     float64 `json:"texImage2DMs"`
}

type nicoNativeProfileBatch struct {
	FirstFrame int64                    `json:"firstFrame"`
	FrameCount int                      `json:"frameCount"`
	TimesMs    []int64                  `json:"timesMs"`
	DrawMs     float64                  `json:"drawMs"`
	TakeMs     float64                  `json:"takeMs"`
	DecodeMs   float64                  `json:"decodeMs"`
	WriteMs    float64                  `json:"writeToDiscardMs"`
	Textures   int                      `json:"textures"`
	Commands   int                      `json:"commands"`
	Browser    nicoNativeProfileBrowser `json:"browser"`
}

type nicoNativeProfileReport struct {
	Schema             int                      `json:"schema"`
	Format             string                   `json:"format"`
	Snapshot           string                   `json:"snapshot"`
	SnapshotHash       string                   `json:"snapshotHash"`
	Width              int                      `json:"width"`
	Height             int                      `json:"height"`
	FPSNum             int64                    `json:"fpsNum"`
	FPSDen             int64                    `json:"fpsDen"`
	DurationMs         int64                    `json:"durationMs"`
	FrameCount         int64                    `json:"frameCount"`
	BrowserStartupMs   float64                  `json:"browserStartupMs"`
	BrowserWaitReadyMs float64                  `json:"browserWaitReadyMs"`
	InitialBrowser     nicoNativeProfileBrowser `json:"initialBrowser"`
	InclusiveNote      string                   `json:"inclusiveNote"`
	Batches            []nicoNativeProfileBatch `json:"batches"`
}

func TestNicoNativeProfile(t *testing.T) {
	if strings.TrimSpace(os.Getenv("NICO_NATIVE_PROFILE_OUT")) == "" {
		t.Skip("set NICO_NATIVE_PROFILE_OUT to run the opt-in browser profile")
	}
	if err := runNicoNativeProfile(); err != nil {
		t.Fatal(err)
	}
}

func runNicoNativeProfile() error {
	out := strings.TrimSpace(os.Getenv("NICO_NATIVE_PROFILE_OUT"))
	if out == "" {
		return errors.New("nico native profile: NICO_NATIVE_PROFILE_OUT is required")
	}
	snapshotPath := strings.TrimSpace(os.Getenv("NICO_NATIVE_PROFILE_SNAPSHOT"))
	if snapshotPath == "" {
		snapshotPath = filepath.Join("build", "nico-mask-probe", "fixtures", "real.snapshot.json")
	}
	snapshotData, err := os.ReadFile(snapshotPath)
	if err != nil {
		return fmt.Errorf("nico native profile: read snapshot: %w", err)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		return fmt.Errorf("nico native profile: decode snapshot: %w", err)
	}
	snapshot, err = niconico.NormalizeSnapshot(snapshot)
	if err != nil {
		return err
	}
	durationMs := int64(6000)
	if value := strings.TrimSpace(os.Getenv("NICO_NATIVE_PROFILE_DURATION_MS")); value != "" {
		durationMs, err = strconv.ParseInt(value, 10, 64)
		if err != nil || durationMs <= 0 {
			return fmt.Errorf("nico native profile: invalid duration %q", value)
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	bundle, cleanup, err := materializeBundle("")
	if err != nil {
		return err
	}
	defer cleanup()
	profilePath := filepath.Join(mustWorkingDirectory(), "scripts", "experiments", "nico-native-probe", "browser-profile.js")
	profileScript, err := os.ReadFile(profilePath)
	if err != nil {
		return fmt.Errorf("nico native profile: read browser profile: %w", err)
	}
	page, err := writeRendererPage(1920, 1080, bundle, snapshot.RendererThreads(), nil)
	if err != nil {
		return err
	}
	defer os.Remove(page)
	if err := injectNicoNativeProfile(page, profileScript); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	startup := time.Now()
	session, err := startBrowser(ctx, "", page)
	startupMs := elapsedMs(startup)
	if err != nil {
		return err
	}
	defer session.close()
	wait := time.Now()
	if err := session.waitReady(ctx, false); err != nil {
		return err
	}
	waitMs := elapsedMs(wait)
	var initialBrowser nicoNativeProfileBrowser
	if err := spriteEvaluate(ctx, session, "window.__nicoNativeProfileTake()", &initialBrowser); err != nil {
		return fmt.Errorf("nico native profile: initial browser profile: %w", err)
	}
	if err := spriteEvaluate(ctx, session, spriteCaptureScript, nil); err != nil {
		return fmt.Errorf("nico native profile: install sprite capture: %w", err)
	}
	if err := spriteEvaluate(ctx, session, "window.__nicoSpritesDeflate=true", nil); err != nil {
		return err
	}
	var ready bool
	if err := spriteEvaluate(ctx, session, "window.__nicoSpritesReady === true && window.__nicoNativeProfileReady === true", &ready); err != nil || !ready {
		if err != nil {
			return err
		}
		return errors.New("nico native profile: browser hooks are not ready")
	}
	clock, err := niconico.NewFrameClock(60, 1)
	if err != nil {
		return err
	}
	frameCount := clock.FrameCountForDurationMs(durationMs)
	if err := writeSpriteHeader(io.Discard, 1920, 1080, uint32(frameCount), 60, 1); err != nil {
		return err
	}
	active := make(map[uint32]int64)
	var activeBytes int64
	var nextID uint32
	var report nicoNativeProfileReport
	report = nicoNativeProfileReport{
		Schema: 1, Format: "NICO_NATIVE_PROFILE", Snapshot: snapshotPath,
		SnapshotHash: digestNativeProfile(snapshotData), Width: 1920, Height: 1080,
		FPSNum: 60, FPSDen: 1, DurationMs: durationMs, FrameCount: frameCount,
		BrowserStartupMs: startupMs, BrowserWaitReadyMs: waitMs,
		InitialBrowser: initialBrowser,
		InclusiveNote:  "Go drawMs/takeMs/decodeMs/writeToDiscardMs are sequential per batch and may be summed; browser Canvas2D/WebGL counters are nested inside browser drawMs and must not be added to it.",
	}
	var scratch bytes.Buffer
	var commandScratch []byte
	for first := int64(0); first < frameCount; first += 30 {
		last := first + 30
		if last > frameCount {
			last = frameCount
		}
		times := make([]int64, last-first)
		for i := range times {
			times[i] = clock.CommentTimeMs(first + int64(i))
		}
		timesJSON, _ := json.Marshal(times)
		batchReport := nicoNativeProfileBatch{FirstFrame: first, FrameCount: len(times), TimesMs: append([]int64(nil), times...)}
		timer := time.Now()
		if err := spriteEvaluate(ctx, session, "window.__nicoSpritesDraw("+string(timesJSON)+")", nil); err != nil {
			return fmt.Errorf("nico native profile: draw %d-%d: %w", first, last-1, err)
		}
		batchReport.DrawMs = elapsedMs(timer)
		timer = time.Now()
		var raw spriteJSONBatch
		if err := spriteEvaluate(ctx, session, "window.__nicoSpritesTake()", &raw); err != nil {
			return fmt.Errorf("nico native profile: take %d-%d: %w", first, last-1, err)
		}
		batchReport.TakeMs = elapsedMs(timer)
		timer = time.Now()
		batch, _, _, err := decodeSpriteBatch(raw, uint64(first), times)
		if err != nil {
			return fmt.Errorf("nico native profile: decode %d-%d: %w", first, last-1, err)
		}
		batchReport.DecodeMs = elapsedMs(timer)
		batchReport.Textures = len(batch.Textures)
		for _, frame := range batch.Frames {
			batchReport.Commands += len(frame.Commands)
		}
		if err := validateSpriteIDs(batch.Textures, batch.Deletes, active, activeBytes, nextID); err != nil {
			return err
		}
		if len(batch.Textures) > 0 {
			nextID = batch.Textures[len(batch.Textures)-1].ID
		}
		timer = time.Now()
		if err := writeSpriteBatchReuseScratch(io.Discard, batch, &scratch, &commandScratch); err != nil {
			return fmt.Errorf("nico native profile: write %d-%d: %w", first, last-1, err)
		}
		batchReport.WriteMs = elapsedMs(timer)
		for _, texture := range batch.Textures {
			bytes := int64(len(texture.RGBA))
			active[texture.ID] = bytes
			activeBytes += bytes
		}
		for _, id := range batch.Deletes {
			activeBytes -= active[id]
			delete(active, id)
		}
		if err := spriteEvaluate(ctx, session, "window.__nicoNativeProfileTake()", &batchReport.Browser); err != nil {
			return fmt.Errorf("nico native profile: profile batch %d-%d: %w", first, last-1, err)
		}
		report.Batches = append(report.Batches, batchReport)
	}
	if err := writeSpriteTerminator(io.Discard); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "nico-native-profile.json"), append(data, '\n'), 0o644)
}

func injectNicoNativeProfile(page string, profile []byte) error {
	data, err := os.ReadFile(page)
	if err != nil {
		return err
	}
	marker := []byte("<script>\n(() => {\n  try {")
	index := bytes.Index(data, marker)
	if index < 0 {
		return errors.New("nico native profile: renderer bootstrap marker not found")
	}
	injected := make([]byte, 0, len(data)+len(profile)+32)
	injected = append(injected, data[:index]...)
	injected = append(injected, []byte("<script>\n")...)
	injected = append(injected, profile...)
	injected = append(injected, []byte("\n</script>\n")...)
	injected = append(injected, data[index:]...)
	return os.WriteFile(page, injected, 0o600)
}

func mustWorkingDirectory() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func elapsedMs(start time.Time) float64 { return float64(time.Since(start).Microseconds()) / 1000 }

func digestNativeProfile(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
