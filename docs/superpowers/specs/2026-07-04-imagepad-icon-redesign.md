# ImagePadServer Icon Redesign

Date: 2026-07-04

## Direction

Use the "simple diamond PAD" composition selected during visual review.

The redesigned icon should show a luminous diamond-shaped PAD on the floor projecting a floating photo upward. One edge of the photo should break into small voxel-like square boxes to suggest the image is being encoded into data, not damaged or dissolving.

## Design Intent

ImagePadServer is a local helper that serves media from a PC to VRChat-facing tools. The icon should communicate:

- image or media delivery
- local relay/projection from a PAD-like source
- image encoding or transfer as data

The final mark should keep those ideas in a clear layered form:

- background: rounded square app tile with blue-to-cyan depth
- floor layer: simple diamond-shaped glowing PAD
- projection layer: subtle cyan beam from the PAD to the photo
- foreground layer: floating photo card with mountain-and-sun media glyph
- data accent: several small square boxes emerging from one photo edge and following the projection axis

## Apple Design Translation

Apple Design guidance is applied as design intent, not as a Mac-only workflow:

- use a 1024px master composition
- keep layers visually distinct by role
- prefer vector source shapes
- reduce detail for small sizes
- keep the PAD as a simple diamond rather than a literal tablet or complex perspective shape
- make voxel boxes read as encoded image data by keeping them square, ordered, and visually tied to the photo edge

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

At small sizes, preserve the diamond PAD silhouette, the floating photo silhouette, and a small cluster of square data boxes. Drop shadows, inner highlights, fine beam details, and excess boxes may be removed. The tray/menu-bar template should be its own simplified asset, not a downscaled full-color icon.

## Visual Decisions

- Use the simple diamond PAD option, not the layered or bold diamond variants.
- Avoid iPad-like hardware details, sensors, or rounded-rectangle tablet cues.
- Avoid text, binary digits, cracks, dust, or irregular fragments for the data effect.
- Keep the square boxes numerous enough to communicate encoding, but organized enough to avoid noise in taskbar and tray sizes.

## Boundaries

Do not change app behavior, version metadata, release scripts, server routes, or unrelated UI code. Existing unrelated worktree changes must remain untouched.

## Verification

After implementation:

- inspect the generated 1024px, 256px, and tray/menu-bar assets visually
- confirm updated files are the intended icon asset paths only
- run `git diff --check`
- if code generation for embedded app icons is needed, run the repo's existing generation path before building
