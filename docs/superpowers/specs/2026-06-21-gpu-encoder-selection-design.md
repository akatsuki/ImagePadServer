# GPU Encoder Selection Design

## Goal

Prefer supported hardware H.264 encoders for every ImagePadServer FFmpeg path that produces H.264 video, while retaining a reliable `libx264` fallback and preserving current output bandwidth, HLS, latency, and compatibility contracts.

## Scope

Hardware encoder selection applies to:

- Audio visualizer HLS.
- SoundCloud artwork/waveform HLS.
- Uploaded-video HLS.
- Still-image MP4 and HLS.
- OBS/RTMP receiver HLS.

It does not apply to FFmpeg operations that do not encode H.264 video:

- Audio analysis, LUFS measurement, and loudness normalization.
- Artwork extraction and thumbnail/image generation.
- Background blur, glyph rendering, and text measurement.
- Audio-only encoding.
- `ffprobe` operations.

## Platform Priority

The default priority is:

- Windows: `h264_nvenc`, then `h264_qsv`, then `h264_amf`, then `libx264`.
- macOS: `h264_videotoolbox`, then `libx264`.
- Other platforms: `libx264` until a separately validated hardware backend is added.

The first encoder that passes a real probe is selected. The result is cached by FFmpeg executable path for the lifetime of the process. It is not persisted across ImagePadServer restarts, allowing driver and hardware changes to be detected on the next launch.

## Capability Probe

Encoder discovery has two stages:

1. Run `ffmpeg -hide_banner -encoders` and retain only encoders advertised by the installed binary.
2. For each advertised candidate in platform priority order, run a short synthetic H.264 encode using a generated color frame and the candidate's actual baseline arguments.

An encoder is available only if the probe process exits successfully and produces a non-empty output. Merely appearing in `-encoders` output is insufficient because FFmpeg builds may advertise an encoder when its driver, device, or runtime is unavailable.

Probe commands use hidden windows, bounded execution time, and temporary files that are removed after the probe. Concurrent callers share one in-flight probe and one cached result.

## Encoder Profiles

A new video package component owns encoder selection and argument construction. Callers request either a standard or low-latency profile and receive:

- Encoder name for `-c:v`.
- Encoder-specific preset and rate-control arguments.
- Required pixel format.
- Whether the profile is hardware accelerated.

Existing `QualityPreset.VideoBitrate`, `MaxRate`, `BufferSize`, output dimensions, audio settings, frame rate, GOP, and HLS segment rules remain authoritative.

Standard hardware profiles use bitrate-constrained rate control so the existing network-quality calculation remains meaningful. `libx264` retains its existing CRF and preset behavior. OBS uses a separate low-latency profile with the closest supported low-latency settings for each encoder.

## Integration Boundaries

The following argument builders consume the selected profile instead of hard-coding `libx264`:

- `audioVisualizerFFmpegArgs`.
- SoundCloud HLS arguments.
- Still-image MP4 and HLS arguments.
- Uploaded-video HLS arguments.
- OBS manager FFmpeg arguments.

Encoder selection is passed explicitly into pure argument builders so their tests remain deterministic. Pure builders must not execute probes or access global hardware state.

## Runtime Fallback

Hardware can fail after a successful probe because of device loss, concurrent session limits, driver reset, or unsupported input parameters.

For non-OBS conversion jobs:

1. Run once with the selected hardware profile.
2. If it fails and the selected profile is hardware accelerated, remove only that job's incomplete MP4, playlist, and segment files.
3. Retry once with the corresponding `libx264` profile.
4. If the CPU retry fails, return an error containing both the hardware and CPU failures.

Cancellation does not trigger a CPU retry.

For OBS, an encoder failure terminates the current receiver process and retries startup once with `libx264`; it must not create a retry loop. Existing process ownership and shutdown behavior remain unchanged.

## Observability

The selected encoder is exposed in video-player state and OBS status using a stable encoder name and a hardware-accelerated boolean. User-visible conversion errors identify the failed hardware encoder when CPU fallback also fails. No GPU model, driver identifier, or device-specific data is persisted.

## Testing

Tests cover:

- Platform priority ordering.
- Parsing advertised FFmpeg encoders.
- Probe success, probe failure, timeout, and cache behavior.
- Standard and low-latency arguments for NVENC, QSV, AMF, VideoToolbox, and libx264.
- Every H.264-producing call path using an injected encoder profile.
- Hardware failure cleaning only job-owned partial output and retrying CPU exactly once.
- Cancellation skipping fallback.
- OBS retrying CPU once without looping.
- Linux/unknown platforms remaining on libx264.
- Existing quality, HLS, and progress tests remaining unchanged.

## Non-goals

- GPU rendering of visualizer frames.
- Zero-copy GPU frame transfer.
- CUDA, OpenCL, Metal, or Direct3D filter acceleration.
- Linux VAAPI support in this update.
- User-selectable encoder settings or persistent encoder overrides.
