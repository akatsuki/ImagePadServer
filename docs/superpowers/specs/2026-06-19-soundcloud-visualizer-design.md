# Audio Visualizer and Media Ingest Design Specification

Date: 2026-06-19  
Status: Approved design, pending implementation plan

## 1. Purpose

Replace the current simple SoundCloud artwork-and-waveform video with a deterministic 16:9 music visualizer shared by SoundCloud tracks, local audio files, and direct audio-file URLs. The result is rendered into the HLS video itself. It is not an interactive player UI.

The visual direction is inspired by Apple Music and Apple Human Interface Guidelines: clear hierarchy, restrained decoration, consistent spacing, legible typography, and artwork-derived color. Do not copy Apple Music assets or its screen verbatim.

## 2. Non-goals

- Do not add clickable, draggable, or touch-operable controls.
- Do not seek audio when the progress indicator is displayed.
- Do not add artwork reflections.
- Do not depend on browser Canvas, WebAudio, or client-side animation.
- Do not silently use an operating-system fallback font.
- Do not support non-16:9 output in this design.
- Do not call SoundCloud, yt-dlp metadata lookup, or any other artwork-discovery service for local audio files or generic direct audio-file URLs.

## 3. Coordinate system and scaling

Use a canonical design canvas of `1280 x 720` pixels. The origin `(0, 0)` is the top-left corner. X increases to the right. Y increases downward.

For an output frame `W x H`:

1. Require `W / H = 16 / 9`, allowing only integer rounding error.
2. Compute `S = W / 1280`.
3. Multiply every X coordinate, Y coordinate, width, height, font size, line width, corner radius, shadow offset, and blur radius in this document by `S`.
4. Round final pixel coordinates consistently. Do not scale X and Y by different factors.

The expected outputs include `1920 x 1080`, `1280 x 720`, and `640 x 360`.

## 4. Required input data

The renderer must receive or precompute:

- `title`: non-empty track title.
- `artist`: non-empty artist name.
- `album`: album name; may be empty.
- `source_name`: original local filename or remote filename preserved independently from temporary storage names.
- `artwork`: optional square or rectangular image.
- `duration_seconds`: finite number greater than zero.
- `current_seconds`: value clamped to `0..duration_seconds` for each frame.
- `spectrum_24`: 24 normalized real-time frequency magnitudes in `0..1` for each frame.
- `realtime_waveform`: the existing real-time waveform data for each frame.
- `loudness_envelope`: precomputed whole-track loudness values normalized to `0..1`.
- `audio_features`: BPM, integrated loudness in LUFS, low-frequency energy ratio, spectral centroid, and 64 whole-track frequency-band averages.

Resolve metadata according to section 4.4 before rendering. The renderer must not invent metadata beyond the explicit filename and SoundCloud-user fallbacks defined there.

### 4.1 Supported audio formats

Do not maintain a fixed extension allowlist for audio. A local file or downloaded direct-URL payload is a supported audio input when the bundled FFmpeg/ffprobe build can open it and reports at least one playable audio stream.

Classify media using decoded stream information, not only file extension or browser-supplied MIME type:

1. Preserve the existing image and camera-RAW detection path first.
2. Probe every remaining uploaded file with ffprobe while video-player mode is enabled.
3. Ignore video streams marked `attached_pic` when deciding whether a file is audio or video.
4. If at least one non-`attached_pic` video stream exists, classify the file as video.
5. Otherwise, if at least one audio stream exists, classify the file as audio.
6. Otherwise, reject the file as unsupported media.

This rule intentionally includes every audio container and codec recognized by the bundled FFmpeg build, including formats not known to the browser or operating system.

### 4.2 Input-source kinds

Use distinct source kinds:

- `soundcloud`: a URL recognized as SoundCloud and downloaded through the SoundCloud-specific path.
- `local_audio`: an audio file uploaded from local storage.
- `remote_audio`: a direct HTTP or HTTPS audio-file URL that is not a SoundCloud page URL.
- Existing image, RAW, uploaded-video, remote-video, and OBS kinds remain unchanged.

After acquisition, `local_audio` and `remote_audio` must use the same metadata, artwork, visualizer, queue, history, publish, and HLS conversion path. `remote_audio` differs only in how the source bytes are obtained.

### 4.3 Artwork-resolution priority

Resolve artwork once, before visualizer rendering, using this exact order:

1. Extract embedded artwork from the acquired audio file.
2. If and only if `source_kind == soundcloud`, use artwork downloaded from the SoundCloud track page when embedded artwork was absent or invalid.
3. Generate the audio-analysis fallback artwork when neither previous candidate is usable.

For embedded artwork:

- Prefer an image explicitly tagged as front cover.
- If no front-cover tag exists, select the valid embedded image with the largest pixel area.
- If pixel area ties, select the image with the largest byte size.
- Ignore undecodable or zero-dimension embedded images and continue to the next candidate.
- Use embedded art from ID3 APIC, FLAC Picture, MP4/M4A `covr`, Ogg-family metadata, or any equivalent attachment exposed by FFmpeg.

Never perform SoundCloud artwork lookup for `local_audio` or `remote_audio`.

### 4.4 Metadata-resolution priority

Resolve title, artist, and album independently.

For `local_audio` and `remote_audio`:

1. Use non-empty embedded audio tags.
2. If title is still empty, use the source filename without its final extension.
3. If artist or album is still empty, leave that field empty and do not render its line.

For `soundcloud`:

1. Use non-empty embedded audio tags.
2. For any field still empty, use the corresponding SoundCloud track metadata.
3. If artist is still empty, use the SoundCloud uploader/user name.
4. If album is still empty, use a SoundCloud album value only when the track exposes one; otherwise leave it empty.
5. If title is still empty, use the downloaded source filename without its final extension.

Do not write a generic `Unknown Artist` or `Unknown Album` string into the video.

### 4.5 Upload UI and size limits

When video-player mode is enabled:

- Change the file-mode tab label from `画像/動画` to `画像/音声/動画`.
- Change the section heading from `画像アップロード` to `メディアアップロード`.
- Change drop-zone and global-drop hints to mention images, camera RAW, audio, and video.
- Describe the URL field as a media URL and allow SoundCloud pages, direct audio files, and the existing image/video URL inputs.
- Do not use a restrictive file-input `accept` allowlist. The server-side image decoder and ffprobe result are authoritative.
- Keep publish, queue, history, cancellation, and HLS URL behavior consistent across local audio, remote audio, SoundCloud audio, and video.

When video-player mode is disabled, preserve the existing image/RAW-only UI and behavior.

Use one media-source limit for uploaded and remotely downloaded audio and video:

- Maximum accepted bytes: `4,294,967,295` (`4 GiB - 1 byte`), matching the maximum single-file size representable on FAT32.
- Reject a source before conversion when its known Content-Length exceeds the limit.
- When length is unknown, stop streaming after the limit plus one byte and reject the source.
- Apply the same limit to local upload, direct audio-file URL download, SoundCloud download, and video download.

### 4.6 Direct audio-file URLs

A non-SoundCloud HTTP or HTTPS URL may resolve to an audio file. Preserve the existing remote-URL security checks, redirect checks, timeouts, and private-network restrictions.

1. Download the source to a unique temporary file without assuming audio from its extension or Content-Type.
   Preserve the response `Content-Disposition` filename when valid; otherwise preserve the final URL-path basename as `source_name`.
2. Enforce the `4,294,967,295`-byte limit while downloading.
3. Probe the completed file with ffprobe.
4. If it contains audio and no non-`attached_pic` video stream, classify it as `remote_audio`.
5. Process it through the exact `local_audio` path.
6. Extract embedded art and tags, but do not perform SoundCloud artwork or metadata lookup.
7. If the URL resolves to video, preserve the existing remote-video path.
8. If it resolves to neither supported image, audio, nor video, reject it.

### 4.7 SoundCloud download outputs and sidecar isolation

The SoundCloud downloader must produce and bind three logical outputs:

- the acquired audio file;
- the downloaded SoundCloud artwork, when present;
- the yt-dlp information JSON used for SoundCloud metadata fallback.

Do not identify the audio file as the largest non-image file in a directory. An information JSON, subtitle, temporary file, or another yt-dlp sidecar must never become the audio source, even when it is larger than a very short audio file.

Use this deterministic procedure:

1. Create a unique output prefix for each download job.
2. Run yt-dlp with `--write-thumbnail` and `--write-info-json`.
3. Invoke yt-dlp with `--print-to-file after_move:filepath <job-specific-manifest>` so the final moved audio path is written directly to a job-specific manifest instead of being parsed from progress output.
4. Require the manifest to contain exactly one non-empty path line, then read the audio path from it. Do not derive it by scanning for the largest file.
5. Resolve both the job directory and manifest path to canonical absolute paths, then require the media path to remain inside the job output directory.
6. Require the manifest path to exist, be a regular file, remain within the `4,294,967,295`-byte limit, and contain a playable audio stream according to ffprobe.
7. Resolve the information JSON only from the same unique output prefix and the `.info.json` suffix.
8. Resolve SoundCloud artwork only from the same unique output prefix and supported image files that successfully decode with non-zero dimensions.
9. Treat `.json`, `.part`, `.ytdl`, subtitle files, manifests, and all other sidecars as metadata or temporary artifacts, never as media candidates.
10. Remove job-specific temporary and sidecar files after their required data has been copied into persistent media state.

If the audio manifest is absent or invalid, fail the download instead of guessing. If the information JSON is absent or invalid, continue with embedded metadata and filename fallbacks. If the artwork is absent or invalid, continue through the artwork priority in section 4.3.

### 4.8 Legacy metadata encoding normalization

Treat yt-dlp information JSON as UTF-8. Treat embedded audio tags returned by ffprobe as untrusted text that may contain legacy Japanese bytes misinterpreted as ISO-8859-1 Unicode code points.

Normalize each embedded title, artist, and album value independently:

1. Trim leading and trailing Unicode whitespace and NUL characters.
2. If the value contains no Unicode C1 control characters in `U+0080..U+009F`, has no replacement character `U+FFFD`, and at least 90% of its non-whitespace characters are printable, keep it unchanged.
3. Otherwise, attempt the legacy-Japanese repair only when every code point is in `U+0000..U+00FF`.
4. Convert those code points back to bytes using the one-byte ISO-8859-1 mapping.
5. Decode the bytes as strict Windows-31J/CP932. Do not replace invalid byte sequences.
6. Accept the repaired candidate only when decoding succeeds, it contains no `U+FFFD`, it contains fewer C1/control characters than the original, and at least 90% of its non-whitespace characters are printable.
7. If the repaired candidate is not strictly better, discard it and keep the original only when the original passed step 2; otherwise treat the tag as empty.
8. Apply the normal source-specific metadata fallback from section 4.4 after normalization.

Do not blindly reinterpret every Latin-1-looking value as CP932. Ordinary ASCII, valid Japanese Unicode, valid Chinese/Korean Unicode, and valid accented Latin metadata must remain unchanged.

The verified SoundCloud fixture `https://soundcloud.com/hujikopro/hujiko-pro-gunpei` must normalize these observed ffprobe values:

- title `GUNPEI` remains `GUNPEI`;
- artist mojibake represented by bytes `93 A1 8E 71 96 BC 90 6C` becomes `藤子名人`;
- album mojibake represented by bytes `94 5A 93 78` becomes `濃度`.

For this fixture, the acquired M4A has no embedded `attached_pic`; therefore its downloaded SoundCloud JPEG must be selected after embedded-artwork lookup returns empty.

## 5. Layer order

Render back to front in this exact order:

1. Full-frame background.
2. Readability overlay.
3. Artwork shadow.
4. Artwork or fallback artwork tile.
5. Metadata text.
6. Spectrum bars.
7. Real-time waveform.
8. Whole-track loudness guide lines.
9. Whole-track loudness curve.
10. Decorative progress track and position marker.
11. Time text.

## 6. Base layout at 1280 x 720

| Element | X | Y | Width | Height |
| --- | ---: | ---: | ---: | ---: |
| Full frame | 0 | 0 | 1280 | 720 |
| Artwork tile | 96 | 152 | 288 | 288 |
| Song-title viewport | 432 | 152 | 752 | 58 |
| Artist viewport | 432 | 224 | 752 | 34 |
| Album viewport | 432 | 264 | 752 | 30 |
| Spectrum and real-time waveform | 432 | 320 | 752 | 168 |
| Whole-track loudness graph | 64 | 548 | 1000 | 80 |
| Decorative progress track | 64 | 650 | 1000 | 8 |
| Time text | 1088 | 632 | 128 | 32 |

The artwork and title share the top coordinate `Y = 152`. All right-side content is left-aligned to `X = 432`. The artwork tile is not vertically or horizontally moved in response to metadata length.

If `album` is empty, do not render the album line. Do not move the title, artist, visualizer, or artwork to fill the unused space.

## 7. Artwork-present state

Use the first valid artwork selected by section 4.3.

1. Crop the resolved artwork to a square from its center.
2. Render it at `(96, 152)` with size `288 x 288`.
3. Use corner radius `24`.
4. Do not apply color correction to the foreground artwork.
5. Do not draw a border or reflection.
6. Draw a black shadow with 20% opacity, blur radius `24`, X offset `0`, and Y offset `8`.

## 8. Background and foreground-color selection

### 8.1 Artwork-present background

1. Scale the artwork with a `cover` operation to fill `1280 x 720` while preserving aspect ratio.
2. Center the scaled image.
3. Crop overflow equally from opposite edges.
4. Apply Gaussian blur radius `64`.

### 8.2 Global foreground mode

Measure the average relative luminance `L` of the blurred background.

- If `L < 0.50`, use light mode:
  - Foreground RGB: `255, 255, 255`.
  - Normal foreground opacity: 88%.
  - Readability overlay: black at 36% opacity.
- If `L >= 0.50`, use dark mode:
  - Foreground RGB: `0, 0, 0`.
  - Normal foreground opacity: 88%.
  - Readability overlay: white at 28% opacity.

Measure contrast in both the metadata area and bottom graph area. If either region is below a `4.5:1` contrast ratio, increase the readability-overlay opacity in 5-percentage-point increments, up to 60%, until both regions pass. Do not change text weight to compensate for inadequate contrast.

Use one global foreground mode for the entire frame. Do not mix black and white text within one frame.

## 9. Typography

Bundle the required Noto Sans CJK font files and address them by explicit file path.

| Role | Font | Size | Weight | Opacity |
| --- | --- | ---: | ---: | ---: |
| Track title | Noto Sans CJK JP | 48 | SemiBold 600 | 88% |
| Artist | Noto Sans CJK JP | 28 | Medium 500 | 88% |
| Album | Noto Sans CJK JP | 24 | Regular 400 | 88% |
| Time | Noto Sans CJK JP | 22 | Medium 500 | 88% |

Use line height `1.2 x font size`. Vertically center each line inside its viewport. Left-align title, artist, and album. Center-align the time string inside the time rectangle.

Do not wrap, truncate, add an ellipsis, or reduce font size.

### 9.1 Long-text rolling behavior

Apply this behavior independently to title, artist, and album:

1. Measure the rendered text width using the final font and weight.
2. If text width is less than or equal to viewport width, keep the text stationary at the viewport's left edge.
3. If text width exceeds viewport width, clip drawing to the viewport rectangle.
4. Hold the text at its initial left-aligned position for `3.0` seconds.
5. Move it left at exactly `40` canonical pixels per second.
6. Continue until the text's right edge is aligned with the viewport's right edge.
7. Immediately reset it to the initial left-aligned position.
8. Repeat from step 4 for the rest of the video.

For overflow distance `D = text_width - viewport_width`, one cycle lasts `3.0 + D / 40` seconds. Base each field's animation on media time so that rerendering the same timestamp produces the same frame.

### 9.2 Text antialiasing

Enable the text renderer's native antialiasing and font hinting. Do not apply a visible Gaussian blur to the text body. If native antialiasing is unavailable or fails final-video QA, render text at a minimum of 2x resolution and downsample with Lanczos. Verify the encoded `yuv420p` output, not only the pre-encode image.

## 10. Real-time spectrum

Use 24 fixed logarithmic frequency bands spanning `20 Hz..20 kHz`.

- Container: `(432, 320, 752, 168)`.
- Bar count: `24`.
- Bar width: `18`.
- Gap between bars: `13`.
- First bar X: `443`.
- Common bottom Y: `488`.
- Minimum visible height: `4`.
- Maximum height: `152`.
- Bar height: `4 + magnitude * 148`, with magnitude clamped to `0..1`.
- Color: global foreground RGB.
- Maximum bar opacity: 82%.
- Frame rate: 30 fps.

Apply a vertical alpha gradient to each bar. Alpha is zero at the bottom edge and reaches the maximum bar opacity 20% of the bar height above the bottom. Keep maximum opacity above that point. This is a transparency fade, not a glow or reflection.

Use faster attack than release so bars rise promptly and fall more slowly. The exact smoothing constants belong in the implementation plan, but the result must not flicker from frame to frame.

## 11. Existing real-time waveform

Reuse the existing real-time waveform behavior rather than designing a second waveform algorithm.

- Draw inside `(432, 320, 752, 168)`.
- Center the waveform around `Y = 404`.
- Line width: `2`.
- Color: global foreground RGB.
- Opacity: 55%.
- No area fill.
- Draw in front of the spectrum bars.
- Render at 30 fps and derive the frame from media time.

## 12. Whole-track loudness graph

This graph communicates where quiet, loud, and chorus-like sections occur across the full track.

1. Analyze the complete audio before final video rendering.
2. Generate a normalized loudness envelope for the entire duration.
3. Resample it to exactly 1000 horizontal samples, one for each canonical pixel of graph width.
4. Draw the graph in `(64, 548, 1000, 80)`.
5. Map value `0` to `Y = 620` and value `1` to `Y = 548`.
6. Draw one continuous line with width `2`, global foreground RGB, and 80% opacity.
7. Do not scroll, translate, or animate this graph.

Draw four horizontal guide lines across X `64..1064` at Y values `554`, `576`, `598`, and `620`. Each guide line is 1 pixel wide and uses global foreground RGB at 22% opacity. These lines are visual scale marks only; they do not represent separate frequency bands.

## 13. Decorative playback-position display

The playback-position display looks like a seek bar but is not a control.

- Track rectangle: `(64, 650, 1000, 8)`.
- Corner radius: `4`.
- Fill: global foreground RGB at 35% opacity.
- Marker: circle with diameter `18`.
- Marker center Y: `654`.
- Marker center X: `64 + 1000 * current_seconds / duration_seconds`.
- Marker fill: global foreground RGB at 88% opacity.
- Update at 30 fps.

Clamp marker center X to `64..1064`. The time text uses `elapsed / total`, for example `2:35 / 5:12`. Round displayed time down to whole seconds. The marker position and displayed elapsed time must be derived from the same `current_seconds` value.

Never attach click, pointer, drag, keyboard, or seek behavior to this display.

## 14. Artwork-missing state

### 14.1 Palette classification

Select exactly one palette using this first-match order:

1. High energy: `BPM >= 130` or integrated loudness `>= -11 LUFS`.
   - Start: `#7A1D4F`.
   - End: `#FF6B35`.
2. Bass-focused: energy at `20..250 Hz` divided by energy at `20 Hz..20 kHz` is `>= 0.45`.
   - Start: `#24103F`.
   - End: `#7C3AED`.
3. Bright: spectral centroid `>= 3500 Hz`.
   - Start: `#0B5563`.
   - End: `#20C7C9`.
4. Calm: `BPM < 95` and integrated loudness `<= -16 LUFS`.
   - Start: `#1F2A44`.
   - End: `#5E5CE6`.
5. Balanced/default:
   - Start: `#173B57`.
   - End: `#3A86FF`.

If analysis fails, use the balanced/default palette. Do not use random colors. The same audio analysis must always select the same palette.

### 14.2 Fallback artwork tile

Render a `288 x 288` tile at `(96, 152)` with corner radius `24` and the same shadow as normal artwork.

1. Fill the tile with a linear gradient from palette start at the top-left to palette end at the bottom-right.
2. Compute 64 logarithmic whole-track frequency-band averages across `20 Hz..20 kHz`.
3. Normalize the 64 values together to `0..1`.
4. For band index `i` from `0` through `63`, compute angle `-90 + i * 360 / 64` degrees.
5. Draw a round-capped radial line from radius `54` to radius `54 + 58 * value` around tile center `(144, 144)`.
6. Use line width `3`, global foreground RGB, and 26% opacity for these 64 lines.
7. Draw the glyph `♪` in front of the radial fingerprint using Noto Sans CJK JP.
8. Use font size `168`, global foreground RGB, and 88% opacity.
9. Center the rendered glyph's visual bounding box, not its baseline box, on `(144, 144)`.
10. Apply native text antialiasing to the glyph.

The radial fingerprint remains visible behind the large music note. Do not add track initials, an app logo, a second icon, a reflection, or a border.

### 14.3 Artwork-missing background

Use the completed fallback tile as the background source. Scale it with `cover`, center-crop it to `1280 x 720`, apply Gaussian blur radius `64`, then apply the normal foreground-mode readability overlay. The note and fingerprint may become indistinct after the strong blur; do not redraw them separately on the background.

All metadata, spectrum, waveform, loudness graph, progress display, and time rules remain unchanged.

## 15. Determinism and failure handling

- The same source audio, source kind, resolved metadata, resolved artwork, output resolution, and timestamp must produce the same video frame.
- Clamp all normalized audio values to `0..1`.
- Clamp negative or non-finite current time to zero.
- Treat missing or invalid duration as a rendering error; do not divide by zero.
- If one embedded artwork candidate fails to decode, continue through the deterministic artwork priority in section 4.3.
- If every artwork candidate fails, use the artwork-missing state.
- Never contact SoundCloud for `local_audio` or `remote_audio`, including after artwork extraction or metadata parsing fails.
- Reject a media file when ffprobe cannot identify an image/RAW path, a playable audio stream, or a playable video stream.
- Reject audio and video sources larger than `4,294,967,295` bytes before enqueueing conversion.
- If palette audio analysis fails, use the balanced/default palette.
- If whole-track loudness analysis fails, fail the visualizer render rather than drawing a misleading flat graph.
- If the bundled font cannot be loaded, fail with an explicit font-loading error. Do not silently select another font.

## 16. Acceptance criteria

1. A 1280 x 720 reference frame places every element at the coordinates in this document.
2. 1080p and 360p outputs preserve the same composition through uniform scaling.
3. Artwork is square, center-cropped, rounded, shadowed, and never reflected.
4. The background uses the artwork or fallback tile, strong blur, and a contrast-correcting overlay.
5. Title, artist, album, and time use the specified Noto Sans CJK JP weights.
6. Long title, artist, and album fields pause for 3 seconds, scroll at 40 canonical pixels per second, reset, and repeat.
7. Encoded H.264/yuv420p text and the music-note glyph show no visible stair-step edges at normal viewing size.
8. Spectrum contains 24 bottom-faded bars and is visibly synchronized to the audio.
9. The existing real-time waveform is drawn in front of the spectrum.
10. The whole-track graph remains fixed and visibly communicates loudness changes across the song.
11. The position marker and elapsed-time text agree at every tested timestamp.
12. The position display cannot be interacted with.
13. Missing artwork produces a deterministic mood palette, a 64-line audio fingerprint, and a large centered `♪`.
14. No renderer path depends on a browser-side visualizer.
15. Local files and generic direct URLs are classified by ffprobe stream data instead of a fixed extension list.
16. An audio file with an `attached_pic` stream and no real video stream is classified as audio.
17. Embedded artwork wins over SoundCloud artwork for a SoundCloud-acquired track.
18. Local and generic direct-URL audio never trigger SoundCloud artwork or metadata lookup.
19. SoundCloud artist fallback uses the uploader/user name, and album is shown only when a real album value exists.
20. The enabled file tab reads `画像/音声/動画`, while disabled mode preserves the image/RAW-only state.
21. Audio and video inputs accept at most `4,294,967,295` bytes through every acquisition path.
22. Direct audio-file URLs use the local-audio visualization path after secure download and probing.
23. SoundCloud download selection uses the explicit yt-dlp final-path manifest; `.info.json` and other sidecars can never be selected as audio.
24. A test fixture whose information JSON is larger than its audio still selects the manifest-listed audio file.
25. Valid UTF-8 Japanese, Chinese, Korean, accented Latin, and ASCII tags pass through unchanged.
26. The verified GUNPEI fixture repairs the observed legacy artist and album values to `藤子名人` and `濃度`.
27. Invalid or ambiguous legacy-text repair is rejected and falls through to the source-specific metadata fallback.
28. The verified GUNPEI fixture selects the downloaded 715 x 706 SoundCloud JPEG because its M4A has no embedded image.

## 17. Implementation boundary

This document defines appearance and observable behavior only. The implementation plan must separately define:

- audio-analysis commands and smoothing constants;
- font bundling and license attribution;
- whether overlays are generated as FFmpeg filters, pre-rendered frame assets, or a narrow helper;
- cache keys for whole-track analysis and fallback artwork;
- ffprobe-based image/audio/video classification and `attached_pic` handling;
- embedded metadata and artwork extraction;
- strict legacy-tag normalization with unchanged-valid-Unicode tests and the verified GUNPEI regression fixture;
- yt-dlp final-path manifest handling and explicit exclusion of information JSON and other sidecars from media selection;
- source-kind-aware artwork and metadata resolution;
- secure direct-audio-URL acquisition and size enforcement;
- replacement of the current uncommitted SoundCloud-only path with a shared audio HLS pipeline;
- integration points with the existing SoundCloud, uploaded-media, history, queue, and HLS paths;
- unit, golden-image, and encoded-video verification.
