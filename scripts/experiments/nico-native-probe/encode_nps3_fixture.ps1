param(
    [Parameter(Mandatory = $true)][string]$Compositor,
    [Parameter(Mandatory = $true)][string]$Fixture,
    [Parameter(Mandatory = $true)][string]$FFmpeg,
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$OutputDirectory,
    [int]$Width = 1920,
    [int]$Height = 1080,
    [int]$FPSNum = 60,
    [int]$FPSDen = 1,
    [int]$DurationMs = 6000
)

$ErrorActionPreference = 'Stop'

function Quote-NativeArgument([string]$Value) {
    if ($Value -notmatch '[\s"]') { return $Value }
    return '"' + $Value.Replace('"', '\"') + '"'
}

New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
$rawOutput = Join-Path $OutputDirectory 'frames.rgba'
$mp4Output = Join-Path $OutputDirectory 'out.mp4'
$hlsDirectory = Join-Path $OutputDirectory 'hls'
New-Item -ItemType Directory -Force -Path $hlsDirectory | Out-Null

$geometry = "${Width}x${Height}"
$fps = "${FPSNum}/${FPSDen}"
$duration = "{0}.{1:D3}" -f [math]::Floor($DurationMs / 1000), ($DurationMs % 1000)
$gop = [math]::Max(1, [math]::Ceiling(4 * $FPSNum / [double]$FPSDen))
$filter = "[0:v]scale=$Width`:$Height`:force_original_aspect_ratio=decrease,pad=$Width`:$Height`:(ow-iw)/2:(oh-ih)/2:color=black[base];[1:v]format=rgba[overlay];[base][overlay]overlay=0:0:format=auto,fps=$fps,format=yuv420p[v]"
$ffmpegArgs = @(
    '-hide_banner', '-loglevel', 'error', '-y',
    '-i', $Source,
    '-f', 'rawvideo', '-pix_fmt', 'rgba', '-video_size', $geometry, '-framerate', $fps, '-i', 'pipe:0',
    '-filter_complex', $filter,
    '-map', '[v]', '-map', '0:a?',
    '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '26',
    '-x264-params', 'sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0',
    '-g', $gop, '-keyint_min', $gop, '-sc_threshold', '0', '-force_key_frames', 'expr:gte(t,n_forced*4)',
    '-pix_fmt', 'yuv420p', '-c:a', 'aac', '-b:a', '160k', '-t', $duration, '-movflags', '+faststart', '-shortest', '-f', 'mp4', $mp4Output
)

$compositorInfo = [Diagnostics.ProcessStartInfo]::new()
$compositorInfo.FileName = (Resolve-Path -LiteralPath $Compositor).Path
$compositorInfo.Arguments = '--stdin'
$compositorInfo.UseShellExecute = $false
$compositorInfo.CreateNoWindow = $true
$compositorInfo.RedirectStandardInput = $true
$compositorInfo.RedirectStandardOutput = $true
$compositorInfo.RedirectStandardError = $true

$encoderInfo = [Diagnostics.ProcessStartInfo]::new()
$encoderInfo.FileName = (Resolve-Path -LiteralPath $FFmpeg).Path
$encoderInfo.Arguments = (($ffmpegArgs | ForEach-Object { Quote-NativeArgument ([string]$_) }) -join ' ')
$encoderInfo.UseShellExecute = $false
$encoderInfo.CreateNoWindow = $true
$encoderInfo.RedirectStandardInput = $true
$encoderInfo.RedirectStandardOutput = $false
$encoderInfo.RedirectStandardError = $true

$compositorProcess = [Diagnostics.Process]::new()
$compositorProcess.StartInfo = $compositorInfo
$encoderProcess = [Diagnostics.Process]::new()
$encoderProcess.StartInfo = $encoderInfo
$fixtureStream = [IO.File]::OpenRead((Resolve-Path -LiteralPath $Fixture).Path)
$compositorError = $null
$encoderError = $null
try {
    if (-not $encoderProcess.Start()) { throw 'ffmpeg did not start' }
    if (-not $compositorProcess.Start()) { throw 'compositor did not start' }
    $compositorErrorTask = $compositorProcess.StandardError.ReadToEndAsync()
    $encoderErrorTask = $encoderProcess.StandardError.ReadToEndAsync()
    $copyTask = $compositorProcess.StandardOutput.BaseStream.CopyToAsync($encoderProcess.StandardInput.BaseStream)
    $fixtureStream.CopyTo($compositorProcess.StandardInput.BaseStream)
    $compositorProcess.StandardInput.Close()
    $copyTask.GetAwaiter().GetResult()
    $encoderProcess.StandardInput.Close()
    $compositorProcess.WaitForExit()
    $encoderProcess.WaitForExit()
    $compositorError = $compositorErrorTask.GetAwaiter().GetResult()
    $encoderError = $encoderErrorTask.GetAwaiter().GetResult()
    [IO.File]::WriteAllText((Join-Path $OutputDirectory 'compositor.stderr'), $compositorError)
    [IO.File]::WriteAllText((Join-Path $OutputDirectory 'ffmpeg.stderr'), $encoderError)
    if ($compositorProcess.ExitCode -ne 0) { throw "compositor exit code $($compositorProcess.ExitCode): $compositorError" }
    if ($encoderProcess.ExitCode -ne 0) { throw "ffmpeg exit code $($encoderProcess.ExitCode): $encoderError" }
    if (-not (Test-Path -LiteralPath $mp4Output)) { throw 'MP4 output was not created' }

    $segmentPattern = Join-Path $hlsDirectory 'segment-%05d.ts'
    $playlist = Join-Path $hlsDirectory 'playlist.m3u8'
    & $FFmpeg -hide_banner -loglevel error -y -i $mp4Output -map 0:v:0 -map '0:a?' -c:v copy -c:a copy -f hls -hls_time 4 -hls_playlist_type vod -hls_flags independent_segments -start_number 0 -hls_segment_filename $segmentPattern $playlist 2> (Join-Path $OutputDirectory 'hls.stderr')
    if ($LASTEXITCODE -ne 0) { throw "HLS ffmpeg exit code $LASTEXITCODE" }
    if (-not (Test-Path -LiteralPath $playlist)) { throw 'HLS playlist was not created' }
}
finally {
    $fixtureStream.Dispose()
    if ($compositorProcess -and -not $compositorProcess.HasExited) { $compositorProcess.Kill() }
    if ($encoderProcess -and -not $encoderProcess.HasExited) { $encoderProcess.Kill() }
    $compositorProcess.Dispose()
    $encoderProcess.Dispose()
}
