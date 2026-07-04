# ImagePadServer Icon Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current ImagePadServer icon assets with the approved inside-screen composition.

**Architecture:** Keep the icon as a vector master in `assets/imagepad-icon.svg`, then generate all raster/icon derivatives from that source. Do not change runtime behavior or version/release metadata.

**Tech Stack:** SVG, Python/Pillow, local Chrome or available SVG rasterizer, Go embedded byte array.

---

### Task 1: Create Master Vector

**Files:**
- Create: `assets/imagepad-icon.svg`

- [x] Add a 1024x1024 SVG with a transparent canvas, rounded blue/cyan app tile, simplified computer screen, mountain-and-sun media glyph inside the screen, and a single broadcast arc.
- [x] Visually inspect the SVG rendered at app-icon scale.

### Task 2: Generate Raster Derivatives

**Files:**
- Modify: `assets/imagepad-icon.png`
- Modify: `assets/imagepad-icon-256.png`
- Modify: `docs/assets/imagepad-icon-256.png`
- Modify: `internal/steamvr/imagepad-icon-256.png`
- Modify: `internal/appicon/imagepad-menubar-template.png`

- [x] Render the SVG to 1024x1024 and 256x256 PNGs.
- [x] Copy the 256px PNG to docs and SteamVR asset paths.
- [x] Generate a separate monochrome 36x36 tray/menu-bar template from the simplified screen + media + broadcast shape.

### Task 3: Generate Platform Icon Containers

**Files:**
- Modify: `assets/imagepad-icon.ico`
- Modify: `assets/imagepad-icon.icns`
- Modify: `internal/appicon/icon.go`

- [x] Generate ICO sizes from the master icon.
- [x] Generate ICNS if Pillow supports it locally.
- [x] Regenerate `internal/appicon.IconICO` from the new ICO bytes while preserving the existing `MenuBarTemplatePNG` embed.

### Task 4: Verify

**Files:**
- Inspect all modified icon assets.

- [x] Confirm image dimensions for 1024, 256, and 36px outputs.
- [x] Visually inspect the app icon and tray/menu-bar template.
- [x] Run `git diff --check`.
- [x] Run a targeted Go test/build command if icon embedding syntax changed.
