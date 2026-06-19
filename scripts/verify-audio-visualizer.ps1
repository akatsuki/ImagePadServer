<#
.SYNOPSIS
    Verify that all tools required by the audio visualizer pipeline are
    installed and functional.

.DESCRIPTION
    Checks for ffmpeg, ffprobe, and yt-dlp on the system.  Accepts optional
    paths to a test audio file and an HLS playlist for basic probe and
    decode verification.

    Tool resolution priority:
      1. IMAGEPAD_FFMPEG / IMAGEPAD_FFPROBE / IMAGEPAD_YTDLP env vars
      2. PATH lookup

.PARAMETER TestFilePath
    Path to an audio file to probe with ffprobe (optional).

.PARAMETER HlsPlaylistPath
    Path to an HLS playlist (.m3u8) to verify (optional).

.EXAMPLE
    .\scripts\verify-audio-visualizer.ps1

.EXAMPLE
    .\scripts\verify-audio-visualizer.ps1 -TestFilePath "C:\tmp\test.m4a"

.EXAMPLE
    .\scripts\verify-audio-visualizer.ps1 -HlsPlaylistPath "C:\out\playlist.m3u8"
#>

param(
    [string]$TestFilePath,
    [string]$HlsPlaylistPath
)

$ErrorActionPreference = "Stop"

function Get-ToolPath {
    param([string]$EnvVar, [string]$ToolName)

    # Priority 1: environment variable
    $path = [Environment]::GetEnvironmentVariable($EnvVar)
    if ($path -and (Test-Path -LiteralPath $path -PathType Leaf)) {
        return $path
    }

    # Priority 2: PATH
    try {
        $resolved = Get-Command $ToolName -ErrorAction Stop
        return $resolved.Source
    } catch {
        return $null
    }
}

# ---------------------------------------------------------------------------
# Phase 1 — Locate tools
# ---------------------------------------------------------------------------
Write-Host "=== Audio Visualizer Tool Verification ===" -ForegroundColor Cyan
Write-Host ""

$ffmpeg  = Get-ToolPath -EnvVar "IMAGEPAD_FFMPEG"  -ToolName "ffmpeg"
$ffprobe = Get-ToolPath -EnvVar "IMAGEPAD_FFPROBE" -ToolName "ffprobe"
$ytdlp   = Get-ToolPath -EnvVar "IMAGEPAD_YTDLP"   -ToolName "yt-dlp"

$missing = @()
if (-not $ffmpeg)  { $missing += "ffmpeg" }
if (-not $ffprobe) { $missing += "ffprobe" }
if (-not $ytdlp)   { $missing += "yt-dlp" }

if ($missing.Count -gt 0) {
    Write-Host "ERROR: Missing required tools: $($missing -join ', ')" -ForegroundColor Red
    Write-Host ""
    Write-Host "Set the corresponding environment variable or add the tool to PATH."
    exit 1
}

Write-Host "ffmpeg : $ffmpeg"  -ForegroundColor Green
Write-Host "ffprobe: $ffprobe" -ForegroundColor Green
Write-Host "yt-dlp : $ytdlp"   -ForegroundColor Green
Write-Host ""

# ---------------------------------------------------------------------------
# Phase 2 — Quick version checks
# ---------------------------------------------------------------------------
Write-Host "--- Version checks ---" -ForegroundColor Cyan
try {
    $ffmpegVer = & $ffmpeg -version
    $firstLine = ($ffmpegVer -split "`n")[0]
    Write-Host "ffmpeg : $firstLine"
} catch {
    Write-Host "ffmpeg version check failed: $_" -ForegroundColor Yellow
}

try {
    $ffprobeVer = & $ffprobe -version
    $firstLine = ($ffprobeVer -split "`n")[0]
    Write-Host "ffprobe: $firstLine"
} catch {
    Write-Host "ffprobe version check failed: $_" -ForegroundColor Yellow
}

try {
    $ytdlpVer = & $ytdlp --version
    Write-Host "yt-dlp : $ytdlpVer"
} catch {
    Write-Host "yt-dlp version check failed: $_" -ForegroundColor Yellow
}
Write-Host ""

# ---------------------------------------------------------------------------
# Phase 3 — Probe test file (if provided)
# ---------------------------------------------------------------------------
if ($TestFilePath) {
    Write-Host "--- Probing test file ---" -ForegroundColor Cyan
    if (-not (Test-Path -LiteralPath $TestFilePath)) {
        Write-Host "ERROR: Test file not found: $TestFilePath" -ForegroundColor Red
        exit 1
    }
    Write-Host "Probing: $TestFilePath"
    $probeResult = & $ffprobe -v error -show_streams -show_format -of json $TestFilePath
    if ($LASTEXITCODE -ne 0) {
        Write-Host "ERROR: ffprobe failed on $TestFilePath (exit code $LASTEXITCODE)" -ForegroundColor Red
        exit 1
    }
    Write-Host "ffprobe succeeded." -ForegroundColor Green
    Write-Host ""
}

# ---------------------------------------------------------------------------
# Phase 4 — Verify HLS playlist (if provided)
# ---------------------------------------------------------------------------
if ($HlsPlaylistPath) {
    Write-Host "--- Verifying HLS output ---" -ForegroundColor Cyan
    if (-not (Test-Path -LiteralPath $HlsPlaylistPath)) {
        Write-Host "ERROR: HLS playlist not found: $HlsPlaylistPath" -ForegroundColor Red
        exit 1
    }
    Write-Host "Playlist: $HlsPlaylistPath"

    # Probe the playlist.
    $hlsProbe = & $ffprobe -v error -show_streams -show_format -of json $HlsPlaylistPath
    if ($LASTEXITCODE -ne 0) {
        Write-Host "ERROR: ffprobe failed on HLS playlist" -ForegroundColor Red
        exit 1
    }
    Write-Host "ffprobe on HLS playlist succeeded." -ForegroundColor Green

    # Verify the playlist is valid by attempting to read it.
    $playlistContent = Get-Content -LiteralPath $HlsPlaylistPath -Raw
    if ($playlistContent -match '\.ts') {
        Write-Host "Playlist references .ts segments." -ForegroundColor Green
    } else {
        Write-Host "WARNING: Playlist does not contain .ts segment references." -ForegroundColor Yellow
    }

    # Attempt to decode the HLS stream (null output).
    Write-Host "Decoding HLS (dry run) ..."
    $nullOutput = if ($IsWindows -or $env:OS -eq "Windows_NT") { "NUL" } else { "/dev/null" }
    $decodeResult = & $ffmpeg -v error -i $HlsPlaylistPath -f null $nullOutput
    if ($LASTEXITCODE -eq 0) {
        Write-Host "HLS decode dry run succeeded." -ForegroundColor Green
    } else {
        Write-Host "ERROR: HLS decode dry run failed (exit code $LASTEXITCODE)" -ForegroundColor Red
        exit 1
    }
    Write-Host ""
}

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
Write-Host "=== All checks passed ===" -ForegroundColor Cyan
exit 0
