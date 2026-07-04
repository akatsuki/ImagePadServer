# ImagePadServer Icon Redesign

Date: 2026-07-04

## Direction

Use the "inside screen" composition selected during visual review.

The redesigned icon should show a simplified computer screen inside the app-icon tile, with a minimal media symbol inside the screen. Do not place a separate photo card on top of the monitor, because a slight offset reads as an accidental overwrite instead of intentional depth.

## Design Intent

ImagePadServer is a local helper that serves media from a PC to VRChat-facing tools. The icon should communicate:

- local PC/helper context
- image or media delivery
- network sharing

The final mark should keep those ideas in a simpler layered form:

- background: rounded square app tile with blue-to-cyan depth
- middle layer: simplified screen shape
- foreground within screen: mountain-and-sun media glyph
- accent: one broadcast arc in the upper-right

## Apple Design Translation

Apple Design guidance is applied as design intent, not as a Mac-only workflow:

- use a 1024px master composition
- keep layers visually distinct by role
- prefer vector source shapes
- reduce detail for small sizes
- avoid ambiguous overlap between foreground and background layers

## Production Assets

The implementation should create or update:

- master vector source under `assets/`
- `assets/imagepad-icon.png` at 1024x1024
- `assets/imagepad-icon-256.png` at 256x256
- `docs/assets/imagepad-icon-256.png`
- `internal/steamvr/imagepad-icon-256.png`
- `assets/imagepad-icon.ico` if local tooling can generate it
- `assets/imagepad-icon.icns` if local tooling can generate it
- `internal/appicon/imagepad-menubar-template.png` as a simplified monochrome tray/menu-bar mark

## Small-Size Rule

At small sizes, preserve the screen silhouette, the internal mountain shape, and the broadcast arc. Drop shadows, inner highlights, and fine glass effects may be removed. The tray/menu-bar template should be its own simplified asset, not a downscaled full-color icon.

## Boundaries

Do not change app behavior, version metadata, release scripts, server routes, or unrelated UI code. Existing unrelated worktree changes must remain untouched.

## Verification

After implementation:

- inspect the generated 1024px, 256px, and tray/menu-bar assets visually
- confirm updated files are the intended icon asset paths only
- run `git diff --check`
- if code generation for embedded app icons is needed, run the repo's existing generation path before building
