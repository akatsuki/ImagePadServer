# ffprobe Self-Healing Design

Date: 2026-06-20  
Status: Approved for review

## 1. Goal

Audio and remote-media operations must obtain a usable ffprobe without failing merely because ffprobe is absent, an old ImagePadServer installation contains only ffmpeg, PATH differs between launch methods, or an explicitly configured ffprobe path has become stale.

## 2. Confirmed causes

1. ffprobe resolution is duplicated between `internal/video` and `internal/server`.
2. The server resolver checks only `IMAGEPAD_FFPROBE` and the directory containing the resolved ffmpeg. It does not use the application bin directory or PATH fallbacks already implemented by the video package.
3. An old application bin directory may contain `ffmpeg.exe` without `ffprobe.exe`.
4. During a fresh installation, ffmpeg is currently published before ffprobe. A concurrent request can observe the incomplete pair and fail.
5. A stale `IMAGEPAD_FFPROBE` currently stops resolution instead of allowing automatic recovery.

## 3. Single ownership

Add this public entry point to `internal/video/toolchain.go`:

```go
func EnsureFFprobe() (string, error)
```

All production callers, including `internal/server`, use this function. Remove the server-owned `findFFprobe` and platform-specific ffprobe-name logic.

## 4. Resolution and recovery order

`EnsureFFprobe` performs these steps:

1. Use `IMAGEPAD_FFPROBE` only when it points to a regular executable file that passes `ffprobe -version`.
2. Check for ffprobe beside the currently resolved ffmpeg.
3. Check `%APPDATA%\ImagePadServer\bin\ffprobe.exe` or the platform-equivalent application bin path.
4. Check PATH.
5. If no usable candidate exists on Windows or macOS, acquire the existing trusted FFmpeg distribution, extract ffprobe, install it into the application bin directory, validate it with `-version`, and return that local path.
6. On unsupported automatic-install platforms, return the existing installation hint only after all local candidates fail.

A missing, stale, non-regular, or non-executable `IMAGEPAD_FFPROBE` value is diagnostic context, not an immediate terminal error. Recovery continues and the final error includes that context only if local acquisition also fails.

## 5. Legacy and partial installations

- If application-local ffmpeg exists but ffprobe does not, install and validate local ffprobe instead of accepting the partial toolchain or reporting `ffprobe not found`.
- Do not delete a working ffmpeg while repairing only ffprobe.
- If both tools must be installed, stage both under unique temporary names. Publish ffprobe first and ffmpeg last, so the presence of the final local ffmpeg indicates a complete pair.
- Remove temporary downloads and staging files after success or failure.

## 6. Concurrency

Use one process-wide mutex around toolchain repair and re-check all candidates after acquiring it. Only one goroutine downloads or extracts the bundle. Waiting goroutines reuse the validated installed path.

Final publication uses same-directory atomic renames. Unique temporary staging paths prevent two processes or interrupted previous installs from being mistaken for completed binaries.

## 7. Validation and errors

- A path is usable only when it is a regular file and `ffprobe -version` exits successfully.
- Do not execute directories, zero-length files, stale temporary files, or incomplete downloads.
- Do not report a plain “ffprobe not found” error before attempting supported automatic recovery.
- Return an error only when candidate resolution and automatic recovery both fail. Include candidate failures and the acquisition failure without exposing unrelated environment values.

## 8. Tests

Tests must cover:

1. Valid configured ffprobe wins.
2. Invalid configured ffprobe falls through to a valid sibling, application-local, or PATH candidate.
3. A legacy application bin containing ffmpeg but no ffprobe invokes repair and returns validated local ffprobe.
4. A candidate that exists but fails `-version` is rejected and repaired.
5. Concurrent `EnsureFFprobe` calls invoke the installer exactly once and all return the same path.
6. Archive installation does not publish ffmpeg before ffprobe.
7. Failed extraction leaves no completed-looking partial pair.
8. Server upload, direct URL, and SoundCloud acquisition paths use `video.EnsureFFprobe` and contain no second resolver.
9. Existing ffmpeg, yt-dlp, media-probe, and full package tests remain green.

Network access is not required by unit tests. Installer behavior is exercised through a deterministic injected installer and fixture archive.

## 9. Completion

The change is complete when all production ffprobe users share `video.EnsureFFprobe`, legacy partial installs self-repair, concurrent calls cannot observe a half-installed pair, package tests pass, and a real local invocation resolves and executes ffprobe successfully from both PATH-present and PATH-absent test environments.
