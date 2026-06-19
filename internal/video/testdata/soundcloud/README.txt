SoundCloud Test Fixtures
=========================

This directory holds no committed binary audio or image files.

The GUNPEI integration test (TestIntegrationGUNPEI in
internal/video/audio_runtime_test.go) downloads a live SoundCloud
track and its JPEG artwork using yt-dlp.  These artifacts exist only
at runtime inside a temporary directory and are destroyed when the
test finishes.

Never commit downloaded audio or JPEG artwork to the repository.

Running the GUNPEI test
-----------------------
The test is gated behind two guards:

1. testing.Short() — the test skips when `go test -short` is active.
2. IMAGEPAD_RUN_NETWORK_TESTS environment variable must be set to "1".

To run:

    $env:IMAGEPAD_RUN_NETWORK_TESTS="1"
    rtk go test ./internal/video -run '^TestIntegrationGUNPEI$' -count=1 -v

Prerequisites:
  - yt-dlp on PATH (or IMAGEPAD_YTDLP)
  - ffmpeg on PATH (or IMAGEPAD_FFMPEG)
  - ffprobe on PATH (or IMAGEPAD_FFPROBE)
  - Internet access to soundcloud.com

What the test verifies
----------------------
1. yt-dlp downloads the track and writes a one-line manifest.
2. The manifest path points to an existing playable M4A file.
3. The M4A has no embedded attached_pic stream.
4. A 715x706 JPEG thumbnail exists alongside the audio.
5. Raw ffprobe tags contain the GUNPEI/CP932 mojibake pattern.
6. SoundCloud .info.json sidecar metadata is parsed correctly.
7. NormalizeEmbeddedTag repairs the CP932 mojibake.
8. The full audio analysis pipeline runs without error.
9. FFmpeg produces a valid HLS playlist and segments.
