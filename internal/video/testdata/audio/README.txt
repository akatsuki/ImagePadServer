Audio Test Fixtures
====================

This directory does NOT contain committed audio files.

Test audio fixtures (silent M4A, tone M4A, short WAV) are generated
dynamically at test time by TestGenerateAudioFixtures in
internal/video/audio_runtime_test.go using FFmpeg's lavfi (Libavfilter
input) pseudo-device.

Motivation
----------
Committed binary audio files bloat the repository and may carry
copyright restrictions even when generated synthetically.  Generating
fixtures on the fly keeps the working tree free of binary artifacts
and ensures every test starts from a known base.

Usage
-----
Run:

    rtk go test ./internal/video -run '^TestGenerateAudioFixtures$' -count=1 -v

This requires ffmpeg on PATH (or IMAGEPAD_FFMPEG).  The test creates
fixtures under t.TempDir(), which Go removes automatically.

Generated files (each ~5-30 KB)
--------------------------------
- silent.m4a  — 3 seconds of silence, AAC, stereo, 48 kHz
- tone.m4a    — 3 seconds of a 440 Hz sine wave, AAC, stereo, 48 kHz
- short.wav   — 1 second of silence, 16-bit PCM, stereo, 48 kHz
