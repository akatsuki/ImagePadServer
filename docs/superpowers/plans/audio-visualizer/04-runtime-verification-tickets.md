# Runtime Verification and Release Tickets

> **Post-review gate:** The review at commit `5c5b872` rejected the original AV-700 completion claim. Execute AV-711 through AV-719 from `05-review-correction-tickets.md`, then rerun AV-700 from the beginning. AV-710 remains `WAITING_DEPENDENCY` until that rerun is verified.

### AV-700: Integrate, verify, and open correction tickets

**Dependencies:** AV-601, AV-602, AV-603, and AV-719 after the correction wave. **Parallel:** prohibited. **Owner:** active AI.

**Files:**
- No planned feature edits.
- Create correction tickets instead of silently fixing ownership violations.
- May update only `ticket-status.md` with evidence.

- [ ] Confirm every dependency commit is present in the master-plan merge order.
- [ ] Run fresh unit tests:

```powershell
rtk go test ./internal/video -count=1
rtk go test ./internal/server -count=1
rtk go test ./internal/library -count=1
rtk go test ./... -count=1
```

- [ ] Run static gates:

```powershell
rtk go vet ./...
rtk git diff --check
rtk git status --short
```

- [ ] Compare any `go vet` warning with AV-000. Reject new warnings; record known pre-existing Windows `unsafe.Pointer` warnings separately.
- [ ] Run race-enabled tests where supported:

```powershell
rtk go test -race ./internal/video ./internal/server ./internal/library -count=1
```

If the Windows toolchain cannot run `-race`, record `WAITING_EXTERNAL` for that evidence and do not convert it into a false pass.

- [ ] Run live verification:

```powershell
$env:IMAGEPAD_RUN_NETWORK_TESTS="1"
$env:IMAGEPAD_FFMPEG="$env:APPDATA\ImagePadServer\bin\ffmpeg.exe"
$env:IMAGEPAD_FFPROBE="$env:APPDATA\ImagePadServer\bin\ffprobe.exe"
$env:IMAGEPAD_YTDLP="$env:APPDATA\ImagePadServer\bin\yt-dlp.exe"
rtk go test ./internal/video -run '^TestIntegrationGUNPEI$' -count=1 -v
rtk powershell -File .\scripts\verify-audio-visualizer.ps1
```

- [ ] Probe generated HLS:

```powershell
rtk proxy "$env:IMAGEPAD_FFPROBE" -v error -show_streams -show_format -of json .\current.m3u8
rtk proxy "$env:IMAGEPAD_FFMPEG" -v error -i .\current.m3u8 -f null NUL
```

Required evidence: H.264, yuv420p, 30 fps, AAC, 48000 Hz, stereo, even 16:9 frame, exit code 0.

- [ ] Extract encoded frames at `0 s`, `3 s`, `4 s`, `50%`, and one second before the end.
- [ ] Compare layout coordinates with tolerance 1 px; verify text pause/scroll, progress synchronization, 24 bars, waveform order, and whole-track graph.
- [ ] Verify 720p, 1080p, and 360p outputs.
- [ ] Verify local no-art audio, local embedded-art audio, generic direct audio URL, SoundCloud, and unchanged uploaded-video paths.
- [ ] Mark each acceptance criterion in the design spec with a concrete ticket/evidence pointer.
- [ ] If any failure occurs, add a correction ticket with one owner and exact failing command; AV-700 remains `IN_PROGRESS`.

AV-700 has no commit unless it changes only the status ledger.

### AV-710: Version and build the Windows test artifact

**Dependencies:** AV-700 re-verified after AV-719. **Parallel:** prohibited.

**Files:**
- Modify: `internal/about/about.go`
- Modify: `winres/winres.json`
- Modify release notes only if the repository's existing release policy requires them.
- Create build output under the concrete versioned `dist` path produced by the repository's existing release script and the version selected in this ticket.

- [ ] Inspect existing tags and version files. Select the next monotonic dev version; do not invent a stable release.
- [ ] Update the semantic version and Windows file version consistently.
- [ ] Run the repository release script when its required shell exists. On this Windows environment, use the documented direct fallback only if `sh` is unavailable.
- [ ] Direct fallback command shape:

```powershell
rtk go build -trimpath -ldflags "-H=windowsgui" -o .\dist\verification\imagepadserver-windows-amd64.exe .\cmd\imagepadserver
```

- [ ] Verify the executable exists, is non-empty, and remains running after launch.
- [ ] Launch it with a temporary data directory and unused port using a hidden window.
- [ ] Verify localhost HTTP, enabled UI copy, local-audio upload to HLS, GUNPEI URL to HLS, playlist HTTP 200, and first segment HTTP 200.
- [ ] Stop the exact test process and remove only its temporary data directory after checking its resolved absolute path.
- [ ] Record SHA-256:

```powershell
rtk powershell -Command "Get-FileHash -Algorithm SHA256 -LiteralPath '.\dist\verification\imagepadserver-windows-amd64.exe' | Format-List Path,Hash"
```

- [ ] Commit version files and required release notes with `chore: prepare audio visualizer test build`.

Do not tag or publish a GitHub release unless the user separately authorizes publication.

## Completion evidence block

```text
Full test command and result:
Vet command and baseline comparison:
Race-test command and result/state:
GUNPEI command and result:
Local audio artifact:
Direct URL artifact:
SoundCloud artifact:
Uploaded-video regression artifact:
ffprobe JSON path:
Decode command and exit code:
Frame comparison paths:
Windows executable path:
Windows executable SHA-256:
Remaining blockers:
```
