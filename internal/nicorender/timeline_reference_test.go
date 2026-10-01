package nicorender

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

type timelineFixtureCatalog struct {
	SchemaVersion int                    `json:"schemaVersion"`
	BundleSHA256  string                 `json:"bundleSha256"`
	RealInput     timelineRealInput      `json:"realInput"`
	Fixtures      []timelineFixtureEntry `json:"fixtures"`
}

type timelineRealInput struct {
	VideoID        string `json:"videoId"`
	CommentCount   int    `json:"commentCount"`
	SnapshotSHA256 string `json:"snapshotSha256"`
	SourceSHA256   string `json:"sourceSha256"`
	Capture        struct {
		Width       int     `json:"width"`
		Height      int     `json:"height"`
		FPSNum      int64   `json:"fpsNum"`
		FPSDen      int64   `json:"fpsDen"`
		DurationsMs []int64 `json:"durationsMs"`
		Frames      []int64 `json:"frames"`
	} `json:"capture"`
}

type timelineFixtureEntry struct {
	Name               string             `json:"name"`
	Disposition        string             `json:"disposition"`
	Tags               []string           `json:"tags"`
	Description        string             `json:"description"`
	UnsupportedFeature string             `json:"unsupportedFeature,omitempty"`
	Snapshot           niconico.Snapshot  `json:"snapshot"`
	Render             timelineRenderCase `json:"render,omitempty"`
}

type timelineRenderCase struct {
	Width      int   `json:"width"`
	Height     int   `json:"height"`
	DurationMs int64 `json:"durationMs"`
	FPSNum     int64 `json:"fpsNum"`
	FPSDen     int64 `json:"fpsDen"`
}

type timelineReferenceDraw struct {
	VPos         int64    `json:"vpos"`
	CommentIndex int      `json:"commentIndex"`
	CommentID    string   `json:"commentId,omitempty"`
	Owner        bool     `json:"owner"`
	PositionX    *float64 `json:"positionX,omitempty"`
	PositionY    *float64 `json:"positionY,omitempty"`
	ImagePadding *float64 `json:"imagePadding,omitempty"`
}

type timelineReferenceFrame struct {
	Frame       int64                   `json:"frame"`
	VPos        int64                   `json:"vpos"`
	RGBA8SHA256 string                  `json:"rgba8Sha256"`
	DrawOrder   []timelineReferenceDraw `json:"drawOrder"`
}

// TimelineReference is emitted by the opt-in capture test. Pixel bytes are
// lossless premultiplied RGBA8 rows in top-left image order, gzip-compressed
// outside the repository. Frame hashes and draw invocations remain readable
// in the adjacent JSON manifest.
type TimelineReference struct {
	Width            int                      `json:"width"`
	Height           int                      `json:"height"`
	FrameCount       int64                    `json:"frameCount"`
	FPSNum           int64                    `json:"fpsNum"`
	FPSDen           int64                    `json:"fpsDen"`
	PixelFormat      string                   `json:"pixelFormat"`
	PixelStream      string                   `json:"pixelStream"`
	PixelStreamSHA   string                   `json:"pixelStreamSha256"`
	BrowserUserAgent string                   `json:"browserUserAgent"`
	FrameRefs        []timelineReferenceFrame `json:"frames"`
	NonZeroAlpha     uint64                   `json:"nonZeroAlphaPixels"`
	PartialAlpha     uint64                   `json:"partialAlphaPixels"`
}

type timelineReferenceSink struct {
	writer       *gzip.Writer
	streamHash   hash.Hash
	frameBytes   int
	frames       []timelineReferenceFrame
	nonZeroAlpha uint64
	partialAlpha uint64
}

func (s *timelineReferenceSink) WriteRGBA(_ context.Context, sequence uint64, pixels []byte) error {
	if int(sequence) != len(s.frames) {
		return fmt.Errorf("reference frame sequence=%d, want %d", sequence, len(s.frames))
	}
	if len(pixels) != s.frameBytes {
		return fmt.Errorf("reference frame %d bytes=%d, want %d", sequence, len(pixels), s.frameBytes)
	}
	frameHash := sha256.Sum256(pixels)
	if _, err := s.writer.Write(pixels); err != nil {
		return err
	}
	if _, err := s.streamHash.Write(pixels); err != nil {
		return err
	}
	for i := 3; i < len(pixels); i += 4 {
		a := pixels[i]
		if a != 0 {
			s.nonZeroAlpha++
		}
		if a != 0 && a != 255 {
			s.partialAlpha++
		}
	}
	s.frames = append(s.frames, timelineReferenceFrame{
		Frame:       int64(sequence),
		RGBA8SHA256: hex.EncodeToString(frameHash[:]),
	})
	return nil
}

func TestTimelineReferenceFixtureCatalog(t *testing.T) {
	catalog := readTimelineFixtureCatalog(t)
	if catalog.SchemaVersion != 1 {
		t.Fatalf("fixture schema version=%d, want 1", catalog.SchemaVersion)
	}
	if got := sha256Hex(bundledRenderer); got != catalog.BundleSHA256 {
		t.Fatalf("bundled renderer SHA-256=%s, want pinned %s", got, catalog.BundleSHA256)
	}

	got := make(map[string]struct{}, len(catalog.Fixtures))
	for _, fixture := range catalog.Fixtures {
		if fixture.Name == "" || fixture.Disposition == "" {
			t.Fatalf("fixture requires name and disposition: %+v", fixture)
		}
		if _, exists := got[fixture.Name]; exists {
			t.Fatalf("duplicate fixture %q", fixture.Name)
		}
		got[fixture.Name] = struct{}{}
		loaded := loadTimelineFixture(t, fixture.Name)
		if fixture.Disposition == "unsupported" && fixture.UnsupportedFeature == "" {
			t.Errorf("unsupported fixture %q has no feature reason", fixture.Name)
		}
		if fixture.Disposition != "supported" && fixture.Disposition != "unsupported" {
			t.Errorf("fixture %q has invalid disposition %q", fixture.Name, fixture.Disposition)
		}
		if fixture.Name != "no-comments" && loaded.CommentCount == 0 {
			t.Errorf("fixture %q unexpectedly normalized to zero comments", fixture.Name)
		}
	}
	for _, name := range []string{
		"ordinary-naka", "ordinary-ue", "ordinary-shita", "same-text-reuse",
		"overlap-a-b-a", "multiline-color-size-tile", "negative-vpos",
		"early-visible", "aspect-4-3", "aspect-square", "no-comments",
		"no-visible-comments", "unsupported-flash", "unsupported-nicoscript-reverse",
		"unsupported-decoration", "unsupported-comment-limit",
	} {
		if _, ok := got[name]; !ok {
			t.Errorf("fixture catalog is missing %q", name)
		}
	}
	if catalog.RealInput.VideoID != "sm9" || catalog.RealInput.CommentCount != 362 {
		t.Fatalf("real input identity=%q/%d, want sm9/362", catalog.RealInput.VideoID, catalog.RealInput.CommentCount)
	}
	if len(catalog.RealInput.SnapshotSHA256) != 64 || len(catalog.RealInput.SourceSHA256) != 64 {
		t.Fatalf("real input hashes must be SHA-256: %+v", catalog.RealInput)
	}
}

func TestTimelineReferenceClock(t *testing.T) {
	clock := niconico.MustFrameClock(30, 1)
	for frame, want := range []int64{0, 3, 6, 10} {
		got := timelineReferenceVPos(clock, int64(frame))
		if got != want {
			t.Fatalf("frame=%d got=%d want=%d", frame, got, want)
		}
	}
	if got := timelineReferenceVPos(clock, -1); got != -4 {
		t.Fatalf("negative vpos=%d, want -4", got)
	}
	ntsc := niconico.MustFrameClock(60000, 1001)
	for _, tc := range []struct{ frame, want int64 }{{-1, -2}, {0, 0}, {1, 1}, {2, 3}, {60, 100}} {
		if got := timelineReferenceVPos(ntsc, tc.frame); got != tc.want {
			t.Errorf("60000/1001 frame=%d vpos=%d, want %d", tc.frame, got, tc.want)
		}
	}
}

func timelineReferenceVPos(clock niconico.FrameClock, frame int64) int64 {
	return int64(math.Floor(float64(clock.CommentTimeMs(frame)) / 10))
}

func readTimelineFixtureCatalog(t *testing.T) timelineFixtureCatalog {
	t.Helper()
	data, err := os.ReadFile("testdata/timeline/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog timelineFixtureCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("decode timeline fixture catalog: %v", err)
	}
	return catalog
}

func loadTimelineFixture(t *testing.T, name string) niconico.Snapshot {
	t.Helper()
	if name == "sm9-362" {
		path := requiredTimelineEnv(t, "NICO_TIMELINE_SNAPSHOT")
		assertOutsideTimelineRepo(t, path, "snapshot")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read external sm9 snapshot: %v", err)
		}
		catalog := readTimelineFixtureCatalog(t)
		if got := sha256Hex(data); got != catalog.RealInput.SnapshotSHA256 {
			t.Fatalf("sm9 snapshot SHA-256=%s, want %s", got, catalog.RealInput.SnapshotSHA256)
		}
		var snapshot niconico.Snapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			t.Fatalf("decode external sm9 snapshot: %v", err)
		}
		return normalizeTimelineFixture(t, name, snapshot, catalog.RealInput.CommentCount)
	}

	catalog := readTimelineFixtureCatalog(t)
	for _, fixture := range catalog.Fixtures {
		if fixture.Name == name {
			return normalizeTimelineFixture(t, name, fixture.Snapshot, -1)
		}
	}
	t.Fatalf("timeline fixture %q is not declared", name)
	return niconico.Snapshot{}
}

func normalizeTimelineFixture(t *testing.T, name string, snapshot niconico.Snapshot, wantCount int) niconico.Snapshot {
	t.Helper()
	normalized, err := niconico.NormalizeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("normalize timeline fixture %q: %v", name, err)
	}
	if wantCount >= 0 && normalized.CommentCount != wantCount {
		t.Fatalf("timeline fixture %q comments=%d, want %d", name, normalized.CommentCount, wantCount)
	}
	return normalized
}

func requiredTimelineEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("NICO_TIMELINE_INTEGRATION=1 requires %s", key)
	}
	return value
}

func assertOutsideTimelineRepo(t *testing.T, path, label string) {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("absolute %s path: %v", label, err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root from test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		t.Fatalf("%s path must stay outside repository: %s", label, abs)
	}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func TestCaptureTimelineReferences(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_INTEGRATION") != "1" {
		t.Skip("set NICO_TIMELINE_INTEGRATION=1 with the real input paths to capture browser references")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	catalog := readTimelineFixtureCatalog(t)
	snapshotPath := requiredTimelineEnv(t, "NICO_TIMELINE_SNAPSHOT")
	sourcePath := requiredTimelineEnv(t, "NICO_TIMELINE_SOURCE")
	ffmpegPath := requiredTimelineEnv(t, "NICO_TIMELINE_FFMPEG")
	artifactDir := requiredTimelineEnv(t, "NICO_TIMELINE_ARTIFACTS")
	assertOutsideTimelineRepo(t, snapshotPath, "snapshot")
	assertOutsideTimelineRepo(t, sourcePath, "source")
	assertOutsideTimelineRepo(t, artifactDir, "artifact output")
	for _, p := range []string{snapshotPath, sourcePath, ffmpegPath} {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			if err == nil {
				err = fmt.Errorf("path is a directory")
			}
			t.Fatalf("required timeline input %s: %v", p, err)
		}
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("create artifact directory: %v", err)
	}
	inputSnapshotHash, err := sha256File(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	inputSourceHash, err := sha256File(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if inputSnapshotHash != catalog.RealInput.SnapshotSHA256 || inputSourceHash != catalog.RealInput.SourceSHA256 {
		t.Fatalf("real input hashes differ from fixture lock: snapshot=%s source=%s", inputSnapshotHash, inputSourceHash)
	}
	ffmpegVersion, err := timelineToolVersion(ctx, ffmpegPath, "-version")
	if err != nil {
		t.Fatalf("read FFmpeg version: %v", err)
	}
	ffmpegHash, err := sha256File(ffmpegPath)
	if err != nil {
		t.Fatalf("hash FFmpeg: %v", err)
	}

	browserPath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER"))
	if browserPath == "" {
		browserPath, err = findBrowser()
		if err != nil {
			t.Fatalf("NICO_TIMELINE_INTEGRATION=1 requires a usable Chromium browser: %v", err)
		}
	}
	if info, statErr := os.Stat(browserPath); statErr != nil || info.IsDir() {
		t.Fatalf("browser executable is unavailable: %s (%v)", browserPath, statErr)
	}
	browserHash, err := sha256File(browserPath)
	if err != nil {
		t.Fatalf("hash browser executable: %v", err)
	}
	gpuMode := strings.TrimSpace(os.Getenv("IMAGEPAD_NICONICO_RENDER_GPU"))
	if gpuMode != "enabled" && gpuMode != "swiftshader" && gpuMode != "swiftshader-inprocess" {
		t.Fatalf("WebGL readPixels capture requires IMAGEPAD_NICONICO_RENDER_GPU=enabled, swiftshader, or swiftshader-inprocess; got %q", gpuMode)
	}

	snapshot := loadTimelineFixture(t, "sm9-362")
	if snapshot.VideoID != catalog.RealInput.VideoID || snapshot.CommentCount != catalog.RealInput.CommentCount {
		t.Fatalf("sm9 fixture identity=%s/%d, want %s/%d", snapshot.VideoID, snapshot.CommentCount, catalog.RealInput.VideoID, catalog.RealInput.CommentCount)
	}
	options := RenderOptions{
		Width:       catalog.RealInput.Capture.Width,
		Height:      catalog.RealInput.Capture.Height,
		DurationMs:  catalog.RealInput.Capture.DurationsMs[len(catalog.RealInput.Capture.DurationsMs)-1],
		FPSNum:      catalog.RealInput.Capture.FPSNum,
		FPSDen:      catalog.RealInput.Capture.FPSDen,
		BrowserPath: browserPath,
		Transport:   "binary",
		BatchFrames: 30,
	}
	reference := captureTimelineReference(ctx, t, snapshot, options)
	if reference.FrameCount != catalog.RealInput.Capture.Frames[len(catalog.RealInput.Capture.Frames)-1] {
		t.Fatalf("captured %d frames, want %d", reference.FrameCount, catalog.RealInput.Capture.Frames[len(catalog.RealInput.Capture.Frames)-1])
	}
	for i, want := range catalog.RealInput.Capture.Frames {
		if want > reference.FrameCount || (i > 0 && catalog.RealInput.Capture.DurationsMs[i] > options.DurationMs) {
			t.Fatalf("capture manifest frame/duration entry %d is outside captured stream", i)
		}
	}
	manifest := struct {
		SchemaVersion int    `json:"schemaVersion"`
		VideoID       string `json:"videoId"`
		CommentCount  int    `json:"commentCount"`
		SnapshotSHA   string `json:"snapshotSha256"`
		SourceSHA     string `json:"sourceSha256"`
		BundleSHA     string `json:"bundleSha256"`
		BrowserPath   string `json:"browserPath"`
		BrowserSHA    string `json:"browserSha256"`
		BrowserAgent  string `json:"browserUserAgent"`
		GPUCapture    string `json:"webglCaptureMode"`
		FFmpegPath    string `json:"ffmpegPath"`
		FFmpegSHA     string `json:"ffmpegSha256"`
		FFmpegVersion string `json:"ffmpegVersion"`
		Captures      []struct {
			DurationMs int64 `json:"durationMs"`
			FrameCount int64 `json:"frameCount"`
			FirstFrame int64 `json:"firstFrame"`
			LastFrame  int64 `json:"lastFrameExclusive"`
		} `json:"captures"`
		Reference TimelineReference `json:"reference"`
	}{
		SchemaVersion: 1,
		VideoID:       snapshot.VideoID,
		CommentCount:  snapshot.CommentCount,
		SnapshotSHA:   inputSnapshotHash,
		SourceSHA:     inputSourceHash,
		BundleSHA:     sha256Hex(bundledRenderer),
		BrowserPath:   browserPath,
		BrowserSHA:    browserHash,
		BrowserAgent:  reference.BrowserUserAgent,
		GPUCapture:    gpuMode,
		FFmpegPath:    ffmpegPath,
		FFmpegSHA:     ffmpegHash,
		FFmpegVersion: ffmpegVersion,
		Reference:     reference,
	}
	for i, durationMs := range catalog.RealInput.Capture.DurationsMs {
		manifest.Captures = append(manifest.Captures, struct {
			DurationMs int64 `json:"durationMs"`
			FrameCount int64 `json:"frameCount"`
			FirstFrame int64 `json:"firstFrame"`
			LastFrame  int64 `json:"lastFrameExclusive"`
		}{DurationMs: durationMs, FrameCount: catalog.RealInput.Capture.Frames[i], FirstFrame: 0, LastFrame: catalog.RealInput.Capture.Frames[i]})
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(artifactDir, "sm9-1920x1080-30fps-reference.json")
	if err := writeTimelineArtifactExclusive(manifestPath, manifestBytes); err != nil {
		t.Fatalf("write reference manifest: %v", err)
	}
	t.Logf("captured frames=%d pixel_stream_sha256=%s artifacts=%s", reference.FrameCount, reference.PixelStreamSHA, artifactDir)
}

func captureTimelineReference(ctx context.Context, t *testing.T, snapshot niconico.Snapshot, options RenderOptions) TimelineReference {
	return captureTimelineReferenceWithRandomSeed(ctx, t, snapshot, options, nil)
}

func captureTimelineReferenceWithRandomSeed(ctx context.Context, t *testing.T, snapshot niconico.Snapshot, options RenderOptions, seed *uint32) TimelineReference {
	t.Helper()
	if err := options.validate(); err != nil {
		t.Fatalf("validate reference render options: %v", err)
	}
	if options.Transport != "binary" {
		t.Fatal("reference capture requires binary WebGL readPixels transport")
	}
	if options.BatchFrames < 1 || options.BatchFrames > 120 {
		t.Fatalf("reference batch size=%d, want 1..120", options.BatchFrames)
	}
	artifactDir := requiredTimelineEnv(t, "NICO_TIMELINE_ARTIFACTS")
	frameName := fmt.Sprintf("sm9-%dx%d-%dfps-%df.rgba.gz", options.Width, options.Height, options.FPSNum/options.FPSDen, niconico.MustFrameClock(options.FPSNum, options.FPSDen).FrameCountForDurationMs(options.DurationMs))
	pixelPath := filepath.Join(artifactDir, frameName)
	pixelFile, err := os.OpenFile(pixelPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create raw frame artifact %s: %v", pixelPath, err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = pixelFile.Close()
			_ = os.Remove(pixelPath)
		}
	}()
	gz, err := gzip.NewWriterLevel(pixelFile, gzip.BestSpeed)
	if err != nil {
		t.Fatalf("create gzip writer: %v", err)
	}
	gz.Header.ModTime = time.Unix(0, 0)
	sink := &timelineReferenceSink{writer: gz, streamHash: sha256.New(), frameBytes: options.Width * options.Height * 4}

	bundlePath, cleanupBundle, err := materializeBundle(options.BundlePath)
	if err != nil {
		t.Fatalf("materialize existing renderer bundle: %v", err)
	}
	defer cleanupBundle()
	transport, err := newFrameTransport(options.Width, options.Height)
	if err != nil {
		t.Fatalf("open existing binary frame transport: %v", err)
	}
	defer transport.close()
	transport.config.SparseFrames = false
	transport.config.CaptureMode = ""
	transport.config.Force2D = false
	pagePath, err := writeRendererPageWithRandomSeed(options.Width, options.Height, bundlePath, snapshot.RendererThreads(), &transport.config, seed)
	if err != nil {
		t.Fatalf("write existing renderer page: %v", err)
	}
	defer os.Remove(pagePath)
	session, err := startBrowser(ctx, options.BrowserPath, pagePath)
	if err != nil {
		t.Fatalf("start existing Chromium renderer: %v", err)
	}
	defer session.close()
	if err := session.waitReady(ctx, true); err != nil {
		transport.stop(nil, "protocol-error")
		t.Fatalf("wait for existing renderer: %v", err)
	}
	var browserState struct {
		UserAgent     string `json:"userAgent"`
		WebGL         bool   `json:"webgl"`
		Alpha         bool   `json:"alpha"`
		Premult       bool   `json:"premultipliedAlpha"`
		Renderer      string `json:"renderer"`
		Vendor        string `json:"vendor"`
		DrawingSpace  string `json:"drawingBufferColorSpace"`
		ColorEncoding string `json:"colorEncoding"`
		DitherEnabled bool   `json:"ditherEnabled"`
		BlendEnabled  bool   `json:"blendEnabled"`
		RedBits       int    `json:"redBits"`
		GreenBits     int    `json:"greenBits"`
		BlueBits      int    `json:"blueBits"`
		AlphaBits     int    `json:"alphaBits"`
	}
	if err := spriteEvaluate(ctx, session, `(()=>{const canvas=document.getElementById('canvas');const gl=canvas&&canvas.getContext('webgl2');const attrs=gl&&gl.getContextAttributes();if(!gl)return {userAgent:navigator.userAgent,webgl:false};const debug=gl.getExtension('WEBGL_debug_renderer_info');let colorEncoding='unavailable';try{gl.bindFramebuffer(gl.FRAMEBUFFER,null);colorEncoding=String(gl.getFramebufferAttachmentParameter(gl.FRAMEBUFFER,gl.BACK,gl.FRAMEBUFFER_ATTACHMENT_COLOR_ENCODING));}catch(e){colorEncoding='error:'+String(e)}return {userAgent:navigator.userAgent,webgl:true,alpha:!!(attrs&&attrs.alpha),premultipliedAlpha:!!(attrs&&attrs.premultipliedAlpha),renderer:debug?gl.getParameter(debug.UNMASKED_RENDERER_WEBGL):String(gl.getParameter(gl.RENDERER)),vendor:debug?gl.getParameter(debug.UNMASKED_VENDOR_WEBGL):String(gl.getParameter(gl.VENDOR)),drawingBufferColorSpace:gl.drawingBufferColorSpace||'',colorEncoding,ditherEnabled:gl.isEnabled(gl.DITHER),blendEnabled:gl.isEnabled(gl.BLEND),redBits:gl.getParameter(gl.RED_BITS),greenBits:gl.getParameter(gl.GREEN_BITS),blueBits:gl.getParameter(gl.BLUE_BITS),alphaBits:gl.getParameter(gl.ALPHA_BITS)};})()`, &browserState); err != nil {
		t.Fatalf("inspect WebGL readPixels context: %v", err)
	}
	t.Logf("WebGL reference context: %+v", browserState)
	if !browserState.WebGL || !browserState.Alpha || !browserState.Premult || browserState.UserAgent == "" {
		t.Fatalf("reference needs WebGL alpha/premultiplication; browser state=%+v", browserState)
	}
	if err := spriteEvaluate(ctx, session, timelineReferenceDrawHook, nil); err != nil {
		t.Fatalf("install test-only draw-order observer: %v", err)
	}
	fragmentTracePath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_FRAGMENT_TRACE_PATH"))
	fragmentTraceEnabled := fragmentTracePath != ""
	if fragmentTraceEnabled {
		const fragmentTraceHook = `(()=>{
  const canvas=document.getElementById('canvas'),gl=canvas&&canvas.getContext('webgl2');
  if(!gl)throw Error('fragment trace needs the renderer WebGL2 context');
  const trace={pending:[],current:null,frames:{},clearAssignments:[],sourceFormat:'unavailable'};
  const clear=gl.clear.bind(gl),draw=gl.drawArrays.bind(gl);
  const savedDrawFbo=gl.getParameter(gl.DRAW_FRAMEBUFFER_BINDING),savedReadFbo=gl.getParameter(gl.READ_FRAMEBUFFER_BINDING),savedActive=gl.getParameter(gl.ACTIVE_TEXTURE);
  gl.activeTexture(gl.TEXTURE0);const savedTexture0=gl.getParameter(gl.TEXTURE_BINDING_2D);
  function createScratch(internalFormat,type,formatName){const texture=gl.createTexture(),framebuffer=gl.createFramebuffer();gl.bindTexture(gl.TEXTURE_2D,texture);gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_MIN_FILTER,gl.NEAREST);gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_MAG_FILTER,gl.NEAREST);gl.texImage2D(gl.TEXTURE_2D,0,internalFormat,canvas.width,canvas.height,0,gl.RGBA,type,null);gl.bindFramebuffer(gl.FRAMEBUFFER,framebuffer);gl.framebufferTexture2D(gl.FRAMEBUFFER,gl.COLOR_ATTACHMENT0,gl.TEXTURE_2D,texture,0);const complete=gl.checkFramebufferStatus(gl.FRAMEBUFFER)===gl.FRAMEBUFFER_COMPLETE;return {texture,framebuffer,complete,type,formatName};}
  const floatExt=gl.getExtension('EXT_color_buffer_float');let scratch=floatExt?createScratch(gl.RGBA32F,gl.FLOAT,'rgba32f'):null;
  if(!scratch||!scratch.complete){if(scratch){gl.deleteFramebuffer(scratch.framebuffer);gl.deleteTexture(scratch.texture);}scratch=createScratch(gl.RGBA8,gl.UNSIGNED_BYTE,'rgba8');}
  if(!scratch.complete)throw Error('fragment trace scratch framebuffer is incomplete');
  trace.sourceFormat=scratch.formatName;
  gl.bindFramebuffer(gl.DRAW_FRAMEBUFFER,savedDrawFbo);gl.bindFramebuffer(gl.READ_FRAMEBUFFER,savedReadFbo);gl.bindTexture(gl.TEXTURE_2D,savedTexture0);gl.activeTexture(savedActive);
  gl.clear=function(...args){const out=clear(...args);if(trace.pending.length){trace.current=trace.pending.shift();trace.frames[trace.current]=[];trace.clearAssignments.push(trace.current);}return out;};
  gl.drawArrays=function(...args){const out=draw(...args);if(trace.current===74||trace.current===109){const callIndex=trace.frames[trace.current].length,targets=trace.current===74?[[1076,645]]:[[492,467],[406,469],[406,472]];const pixels=targets.map(([x,y])=>{const rgba=new Uint8Array(4);gl.readPixels(x,canvas.height-1-y,1,1,gl.RGBA,gl.UNSIGNED_BYTE,rgba);return {xy:[x,y],rgba:Array.from(rgba)};});const row={drawCall:callIndex,pixels};const wantsSource=(trace.current===74&&(callIndex===20||callIndex===22))||(trace.current===109&&(callIndex===9||callIndex===10));if(wantsSource){const program=gl.getParameter(gl.CURRENT_PROGRAM),rectLoc=gl.getUniformLocation(program,'uRect'),alphaLoc=gl.getUniformLocation(program,'uAlpha'),projLoc=gl.getUniformLocation(program,'uProjection'),rect=Array.from(gl.getUniform(program,rectLoc)),alpha=gl.getUniform(program,alphaLoc),projection=Array.from(gl.getUniform(program,projLoc)),drawFbo=gl.getParameter(gl.DRAW_FRAMEBUFFER_BINDING),readFbo=gl.getParameter(gl.READ_FRAMEBUFFER_BINDING),viewport=Array.from(gl.getParameter(gl.VIEWPORT)),clearColor=Array.from(gl.getParameter(gl.COLOR_CLEAR_VALUE)),blend=gl.isEnabled(gl.BLEND);gl.bindFramebuffer(gl.FRAMEBUFFER,scratch.framebuffer);gl.viewport(0,0,canvas.width,canvas.height);gl.disable(gl.BLEND);gl.clearColor(0,0,0,0);clear(gl.COLOR_BUFFER_BIT);draw(...args);row.uniforms={rect,alpha,projection};row.sourcePixels=targets.map(([x,y])=>{if(scratch.type===gl.FLOAT){const rgba=new Float32Array(4);gl.readPixels(x,canvas.height-1-y,1,1,gl.RGBA,gl.FLOAT,rgba);return {xy:[x,y],rgba:Array.from(rgba)};}const rgba=new Uint8Array(4);gl.readPixels(x,canvas.height-1-y,1,1,gl.RGBA,gl.UNSIGNED_BYTE,rgba);return {xy:[x,y],rgba:Array.from(rgba)};});gl.bindFramebuffer(gl.DRAW_FRAMEBUFFER,drawFbo);gl.bindFramebuffer(gl.READ_FRAMEBUFFER,readFbo);gl.viewport(...viewport);gl.clearColor(...clearColor);if(blend)gl.enable(gl.BLEND);else gl.disable(gl.BLEND);}trace.frames[trace.current].push(row);}return out;};
  window.__nicoT13FragmentTrace=trace;
  window.__nicoT13SetFrameQueue=frames=>{trace.pending=frames.slice();};
  return {installed:true,renderer:gl.getParameter(gl.RENDERER)};
})()`
		if err := spriteEvaluate(ctx, session, fragmentTraceHook, nil); err != nil {
			t.Fatalf("install opt-in fragment trace: %v", err)
		}
	}
	if probePath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_LAYOUT_PROBE_PATH")); probePath != "" {
		var probe json.RawMessage
		const layoutProbe = `(()=>{const n=window.__niconi;const canvas=document.getElementById('canvas');const rect=canvas&&canvas.getBoundingClientRect();return {userAgent:navigator.userAgent,devicePixelRatio:window.devicePixelRatio,fontsStatus:document.fonts&&document.fonts.status,fonts:Array.from(document.fonts||[]).map(f=>({family:f.family,status:f.status,style:f.style,weight:f.weight})),renderer:n.renderer&&n.renderer.rendererName,processedCommentIndex:n.processedCommentIndex,nextUnprocessedCommentIndex:n.nextUnprocessedCommentIndex,commentCount:n.comments.length,canvas:{width:canvas&&canvas.width,height:canvas&&canvas.height,clientWidth:canvas&&canvas.clientWidth,clientHeight:canvas&&canvas.clientHeight,rectWidth:rect&&rect.width,rectHeight:rect&&rect.height},config:{canvasWidth:n.ctx.config.canvasWidth,canvasHeight:n.ctx.config.canvasHeight},comments:n.comments.map(c=>({index:c.index,id:String(c.comment&&c.comment.id||''),vpos:c.vpos,loc:c.loc,long:c.long,width:c.width,height:c.height,posY:c.posY,x:Number.isFinite(c.pos&&c.pos.x)?c.pos.x:null,invisible:!!c.invisible}))};})()`
		if err := spriteEvaluate(ctx, session, layoutProbe, &probe); err != nil {
			t.Fatalf("read browser layout probe: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(probePath), 0o755); err != nil {
			t.Fatalf("create layout probe directory: %v", err)
		}
		if err := writeTimelineArtifactExclusive(probePath, probe); err != nil {
			t.Fatalf("write browser layout probe: %v", err)
		}
	}
	conn, err := transport.waitConn(ctx)
	if err != nil {
		t.Fatalf("wait for binary render connection: %v", err)
	}
	clock := niconico.MustFrameClock(options.FPSNum, options.FPSDen)
	frameCount := clock.FrameCountForDurationMs(options.DurationMs)
	reference := TimelineReference{
		Width:            options.Width,
		Height:           options.Height,
		FrameCount:       frameCount,
		FPSNum:           options.FPSNum,
		FPSDen:           options.FPSDen,
		PixelFormat:      "rgba8-unorm-premultiplied-top-left",
		PixelStream:      frameName,
		BrowserUserAgent: browserState.UserAgent,
		FrameRefs:        make([]timelineReferenceFrame, 0, frameCount),
	}
	orderByVPos := make(map[int64][]timelineReferenceDraw)
	for first := int64(0); first < frameCount; first += int64(options.BatchFrames) {
		count := min(int64(options.BatchFrames), frameCount-first)
		t.Logf("browser reference batch start=%d count=%d", first, count)
		times := make([]int64, count)
		for i := range times {
			times[i] = clock.CommentTimeMs(first + int64(i))
		}
		if fragmentTraceEnabled {
			frameIndexes := make([]int64, count)
			for i := range frameIndexes {
				frameIndexes[i] = first + int64(i)
			}
			queueJSON, marshalErr := json.Marshal(frameIndexes)
			if marshalErr != nil {
				t.Fatalf("encode fragment trace frame queue: %v", marshalErr)
			}
			if err := spriteEvaluate(ctx, session, "window.__nicoT13SetFrameQueue("+string(queueJSON)+")", nil); err != nil {
				t.Fatalf("set fragment trace frame queue: %v", err)
			}
		}
		if err := requestBinaryBatch(ctx, session, uint64(first), times, true); err != nil {
			transport.stop(conn, "protocol-error")
			t.Fatalf("request reference frames %d..%d: %v", first, first+count, err)
		}
		t.Logf("browser reference batch requested start=%d count=%d", first, count)
		for offset, timeMs := range times {
			frame := first + int64(offset)
			packet, err := transport.receivePacket(ctx, conn)
			if err != nil {
				transport.stop(conn, "protocol-error")
				t.Fatalf("receive reference frame %d: %v", frame, err)
			}
			header, pixels, err := decodeFramePacket(packet, frameHeader{Sequence: uint64(frame), TimeMs: uint64(timeMs), Width: uint32(options.Width), Height: uint32(options.Height)})
			if err != nil {
				transport.stop(conn, "protocol-error")
				t.Fatalf("decode reference frame %d: %v", frame, err)
			}
			if header.Kind != frameKindFull {
				t.Fatalf("reference frame %d was not fully captured (kind=%d)", frame, header.Kind)
			}
			var drawCalls []timelineReferenceDraw
			if err := spriteEvaluate(ctx, session, "window.__timelineReferenceOrder.splice(0)", &drawCalls); err != nil {
				t.Fatalf("read draw order after frame %d: %v", frame, err)
			}
			for _, call := range drawCalls {
				orderByVPos[call.VPos] = append(orderByVPos[call.VPos], call)
			}
			vpos := timelineReferenceVPos(clock, frame)
			drawOrder := append([]timelineReferenceDraw(nil), orderByVPos[vpos]...)
			delete(orderByVPos, vpos)
			if err := sink.WriteRGBA(ctx, uint64(frame), pixels); err != nil {
				t.Fatalf("save reference frame %d: %v", frame, err)
			}
			frameReference := sink.frames[len(sink.frames)-1]
			frameReference.VPos = vpos
			frameReference.DrawOrder = drawOrder
			reference.FrameRefs = append(reference.FrameRefs, frameReference)
			if err := transport.acknowledge(conn, uint64(frame)); err != nil {
				t.Fatalf("acknowledge reference frame %d: %v", frame, err)
			}
		}
	}
	if fragmentTraceEnabled {
		var trace json.RawMessage
		if err := spriteEvaluate(ctx, session, "window.__nicoT13FragmentTrace", &trace); err != nil {
			t.Fatalf("read opt-in fragment trace: %v", err)
		}
		assertOutsideTimelineRepo(t, fragmentTracePath, "fragment trace output")
		if err := os.MkdirAll(filepath.Dir(fragmentTracePath), 0o755); err != nil {
			t.Fatalf("create fragment trace directory: %v", err)
		}
		if err := writeTimelineArtifactExclusive(fragmentTracePath, trace); err != nil {
			t.Fatalf("write opt-in fragment trace: %v", err)
		}
	}
	transport.stop(conn, "completed")
	if len(orderByVPos) != 0 {
		t.Fatalf("unassigned draw order remains for vposes %v", mapKeys(orderByVPos))
	}
	if int64(len(sink.frames)) != frameCount {
		t.Fatalf("saved %d frames, expected %d", len(sink.frames), frameCount)
	}
	if sink.partialAlpha == 0 || sink.nonZeroAlpha == 0 {
		t.Fatalf("blank/non-antialiased reference: alpha pixels=%d partial=%d", sink.nonZeroAlpha, sink.partialAlpha)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("finish compressed raw RGBA stream: %v", err)
	}
	if err := pixelFile.Sync(); err != nil {
		t.Fatalf("flush raw RGBA stream: %v", err)
	}
	if err := pixelFile.Close(); err != nil {
		t.Fatalf("close raw RGBA stream: %v", err)
	}
	reference.PixelStreamSHA = hex.EncodeToString(sink.streamHash.Sum(nil))
	reference.NonZeroAlpha = sink.nonZeroAlpha
	reference.PartialAlpha = sink.partialAlpha
	complete = true
	return reference
}

const timelineReferenceDrawHook = `(()=>{
  const n=window.__niconi;
  if(!n||typeof n._drawComments!=='function')throw Error('niconi _drawComments observer target is unavailable');
  window.__timelineReferenceOrder=[];
  const original=n._drawComments;
  n._drawComments=function(items,...args){
    for(const item of items||[]){
      if(!item||item.__timelineReferenceWrapped)continue;
      const draw=item.draw;
		item.draw=function(vpos,...drawArgs){
			const result=draw.call(this,vpos,...drawArgs);
			const pos=this.pos||{};
			const comment=this.comment||{};
			const config=n.ctx.config||{};
			const visible=Number.isFinite(pos.x)&&Number.isFinite(pos.y)&&Number.isFinite(comment.width)&&Number.isFinite(comment.height)&&comment.width>0&&comment.height>0&&pos.x<config.canvasWidth&&pos.y<config.canvasHeight&&pos.x+comment.width>0&&pos.y+comment.height>0;
			const image=this.image||(typeof this.getTextImage==='function'?this.getTextImage():null);
			const padding=image&&typeof image.getImagePadding==='function'?image.getImagePadding():0;
			if(visible)window.__timelineReferenceOrder.push({vpos:Math.floor(vpos),commentIndex:Number(this.index),commentId:String(comment.id||''),owner:!!comment.owner,positionX:pos.x,positionY:pos.y,imagePadding:Number.isFinite(padding)?padding:null});
			return result;
      };
      Object.defineProperty(item,'__timelineReferenceWrapped',{value:true});
    }
    return original.call(this,items,...args);
  };
})()`

func mapKeys[V any](values map[int64]V) []int64 {
	keys := make([]int64, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func timelineToolVersion(ctx context.Context, path, arg string) (string, error) {
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionCtx, path, arg).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", path, arg, err, strings.TrimSpace(string(output)))
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "", fmt.Errorf("%s %s returned no version text", path, arg)
}

func writeTimelineArtifactExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}
