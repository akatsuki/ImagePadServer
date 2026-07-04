# ImagePadServer Icon Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current ImagePadServer icon assets with the approved simple diamond PAD composition.

**Architecture:** Keep the backgroundless Windows/Linux icon as the vector master in `assets/imagepad-icon.svg`. Generate Windows/Linux PNG and ICO assets with transparent backgrounds, and generate only the macOS ICNS from a background-backed derivative. Do not change runtime behavior or version/release metadata.

**Tech Stack:** SVG, Python/Pillow, local Chrome or available SVG rasterizer, Go embedded byte array.

---

### Task 1: Create Master Vector

**Files:**
- Modify: `assets/imagepad-icon.svg`
- Create: `assets/imagepad-icon-mac.svg`

- [x] Replace `assets/imagepad-icon.svg` with the selected A composition: a transparent canvas, a simple glowing diamond PAD on the floor, a subtle cyan projection beam, a floating photo card, and square data boxes emerging from one photo edge.
- [x] Add `assets/imagepad-icon-mac.svg` as the same composition with a rounded blue app-tile background for macOS only.
- [x] Visually inspect both SVGs rendered at app-icon scale.

### Task 2: Generate Raster Derivatives

**Files:**
- Modify: `assets/imagepad-icon.png`
- Modify: `assets/imagepad-icon-256.png`
- Modify: `docs/assets/imagepad-icon-256.png`
- Modify: `internal/steamvr/imagepad-icon-256.png`
- Modify: `internal/appicon/imagepad-menubar-template.png`

- [x] Render the backgroundless SVG to 1024x1024 and 256x256 PNGs.
- [x] Copy the backgroundless 256px PNG to docs and SteamVR asset paths.
- [x] Generate a separate monochrome 36x36 tray/menu-bar template from the simplified diamond PAD + photo + data-box silhouette.

### Task 3: Generate Platform Icon Containers

**Files:**
- Modify: `assets/imagepad-icon.ico`
- Modify: `assets/imagepad-icon.icns`
- Modify: `internal/appicon/icon.go`

- [x] Generate ICO sizes from the backgroundless master icon for Windows.
- [x] Generate ICNS from the Mac background SVG only.
- [x] Regenerate `internal/appicon.IconICO` from the new backgroundless ICO bytes while preserving the existing `MenuBarTemplatePNG` embed.

### Task 4: Verify

**Files:**
- Inspect all modified icon assets.

- [x] Confirm image dimensions for 1024, 256, ICO, ICNS, and 36px outputs.
- [x] Confirm Windows/Linux PNG and ICO assets have transparent corner pixels.
- [x] Confirm macOS ICNS has an opaque rounded app-tile background.
- [x] Visually inspect the app icon and tray/menu-bar template.
- [x] Run `git diff --check`.
- [x] Run a targeted Go test/build command if icon embedding syntax changed.
