# Visualizer Adaptive Colors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split visualizer foreground colors into contrast-safe monochrome text and artwork-derived chromatic graph accents.

**Architecture:** Add deterministic artwork palette analysis to `visualizer_color.go`, select one overlay plus primary/accent colors in `visualizer_background.go`, and migrate renderers to explicit color roles. Preserve the existing blur and 35 percent overlay cap while deriving hue from a lightly processed source image and legibility from the final composited background.

**Tech Stack:** Go, `image`/`image/color`, `golang.org/x/image/draw`, FFmpeg-generated PNG fixtures, Go tests.

---

### Task 1: Lock the color-role API and artwork accent extraction

**Files:**
- Modify: `internal/video/visualizer_color.go`
- Create: `internal/video/visualizer_adaptive_color_test.go`

- [ ] **Step 1: Write failing tests** for 32-pixel analysis scaling, neutral rejection, saturated outlier resistance, circular hue averaging, deterministic 180-degree hue rotation, and grayscale returning no accent.
- [ ] **Step 2: Run RED** with `rtk go test ./internal/video -run '^TestArtworkAccent' -count=1 -v`; expect compile failures for `artworkAccent`.
- [ ] **Step 3: Implement constants and helpers** for the 3x3 box blur, OKLCH filtering (`A >= 128`, `0.08 <= L <= 0.92`, `C >= 0.04`), 24 hue bins, capped chroma weighting, circular mean, and `C <= 0.12` complement output.
- [ ] **Step 4: Run GREEN** with the same command; expect all artwork-accent tests to pass.

### Task 2: Select overlay, primary, and accent colors

**Files:**
- Modify: `internal/video/visualizer_background.go`
- Modify: `internal/video/visualizer_background_test.go`
- Modify: `internal/video/neutral_foreground_test.go`
- Modify: `internal/video/visualizer_adaptive_color_test.go`

- [ ] **Step 1: Write failing tests** for `ForegroundMode{PrimaryColor, AccentColor, Overlay}`, monochrome primary selection, accent lightness/gamut search, 4.5:1 region contrast, neutral accent fallback, and the 35 percent cap.
- [ ] **Step 2: Run RED** with `rtk go test ./internal/video -run 'TestAdaptiveForeground|TestNeutralBackground' -count=1 -v`; expect missing fields and selector failures.
- [ ] **Step 3: Implement `AdaptiveForeground`** accepting the strong-blur background, light-analysis image, and explicit primary/accent rectangles. Search primary pairs first, composite the selected overlay logically, then search accent lightness and chroma. Preserve compatibility through `Color` only until all callers migrate, then remove it.
- [ ] **Step 4: Update `PrepareVisualizerBase`** to create the light-analysis image from the cover crop and call `AdaptiveForeground` before drawing the overlay and foreground artwork.
- [ ] **Step 5: Run GREEN** with the Task 2 command and the complete `internal/video` package.

### Task 3: Migrate renderers to explicit roles

**Files:**
- Modify: `internal/video/visualizer_ass.go`
- Modify: `internal/video/visualizer_ass_test.go`
- Modify: `internal/video/audio_visualizer.go`
- Modify: `internal/video/audio_visualizer_test.go`
- Modify: `internal/video/fallback_artwork.go`
- Modify: `internal/video/fallback_artwork_test.go`

- [ ] **Step 1: Write failing assertions** proving ASS metadata/time use `PrimaryColor`, while waveform, spectrum, loudness, progress, fingerprint, and note use `AccentColor`.
- [ ] **Step 2: Run RED** with `rtk go test ./internal/video -run 'Test.*(PrimaryColor|AccentColor|ForegroundRole)' -count=1 -v`; expect old single-color behavior.
- [ ] **Step 3: Replace each `mode.Color` use** according to the design role table without changing existing element alpha values.
- [ ] **Step 4: Run GREEN** with the focused tests, then `rtk go test ./internal/video -count=1`.

### Task 4: Reconcile legacy tests and remove duplicate policy

**Files:**
- Modify: `internal/video/visualizer_background.go`
- Modify: `internal/video/visualizer_background_test.go`
- Modify: `internal/video/neutral_foreground_test.go`

- [ ] **Step 1: Identify tests tied to the superseded single complementary-color contract**, especially the mid-gray raw-color expectation.
- [ ] **Step 2: Replace those assertions with the approved two-role contract**: monochrome primary, neutral accent fallback, chromatic accent for colorful source artwork, and post-overlay contrast.
- [ ] **Step 3: Remove unused `ComplementaryForeground` or `SelectForegroundMode` paths** only after repository search proves there are no callers.
- [ ] **Step 4: Run** `rtk go test ./internal/video -count=3`; expect three clean passes.

### Task 5: Full verification

**Files:**
- Modify only if verification exposes a defect covered by a new failing test.

- [ ] **Step 1: Format** with `rtk proxy gofmt -w` on changed Go files.
- [ ] **Step 2: Run** `rtk go test ./... -count=1`; expect zero failures.
- [ ] **Step 3: Run** `rtk go vet ./...`; compare any output with the existing baseline.
- [ ] **Step 4: Run** `rtk go build ./...`; expect exit code zero.
- [ ] **Step 5: Inspect** `rtk git diff --check` and `rtk git diff --stat`; ensure unrelated user changes remain intact and unstaged.
