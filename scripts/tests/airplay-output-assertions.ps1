# Test-only gates for ffprobe/trace_headers observations, not publisher arguments.
function Assert-AirPlayEncodedOutput {
    param($Probe, [int]$Height)
    $widths = @{360=640;720=1280;1080=1920}
    $video = @($Probe.streams | Where-Object codec_type -eq 'video')
    $audio = @($Probe.streams | Where-Object codec_type -eq 'audio')
    if ($video.Count -ne 1 -or $video[0].codec_name -ne 'h264' -or
        $video[0].height -ne $Height -or $video[0].width -ne $widths[$Height] -or
        $video[0].pix_fmt -ne 'yuv420p') { throw 'encoded video codec/dimensions/pixel format mismatch' }
    if ($audio.Count -ne 1 -or $audio[0].codec_name -ne 'aac' -or $audio[0].profile -ne 'LC') {
        throw 'expected one AAC-LC track, including silence for video-only input'
    }
    $frames = @($Probe.frames | Where-Object media_type -eq 'video')
    if ($frames.Count -lt 60) { throw 'insufficient decoded video frames' }
    if ($video[0].has_b_frames -ne 0 -or @($frames | Where-Object pict_type -eq 'B').Count) {
        throw 'unexpected encoded B-frame'
    }
    if ($video[0].r_frame_rate -ne '30/1') { throw 'unexpected advertised output cadence' }
    $deltas = [Collections.Generic.List[double]]::new()
    $previous = $null
    foreach ($frame in $frames) {
        $pts = 0.0
        if (-not [double]::TryParse([string]$frame.best_effort_timestamp_time, [Globalization.NumberStyles]::Float,
                [Globalization.CultureInfo]::InvariantCulture, [ref]$pts)) { throw 'missing measured frame timestamp' }
        if ($null -ne $previous) {
            $delta = $pts - $previous
            # 90kHz TS time base rounds 30fps by much less than 0.1ms.
            if ($delta -lt (1.0/30 - 0.0001)) { throw 'output cadence is faster than 30fps or nonmonotonic' }
            $deltas.Add($delta)
        }
        $previous = $pts
    }
    $median = @($deltas | Sort-Object)[[int][Math]::Floor($deltas.Count / 2)]
    if ([Math]::Abs($median - 1.0/30) -gt 0.0001) { throw 'output cadence is not sustained 30fps' }
    return [ordered]@{
        width=[int]$video[0].width; height=$Height; codec='h264'; pixelFormat='yuv420p'
        advertisedFPS='30/1'; frameCount=$frames.Count; bFrames=0; audio='AAC-LC'
        minFrameIntervalSeconds=($deltas | Measure-Object -Minimum).Minimum
        maxFrameIntervalSeconds=($deltas | Measure-Object -Maximum).Maximum
        medianFrameIntervalSeconds=$median
    }
}

function Assert-AirPlayIDRInterval {
    param([string[]]$Lines, [int]$GOP)
    $packet = -1
    $idr = [Collections.Generic.List[int]]::new()
    foreach ($line in $Lines) {
        if ($line -match '\[trace_headers[^\]]*\] Packet:') { $packet++ }
        elseif ($packet -ge 0 -and $line -match '\bnal_unit_type\s+\d+\s*=\s*5\s*$') {
            if ($idr.Count -eq 0 -or $idr[$idr.Count-1] -ne $packet) { $idr.Add($packet) }
        }
    }
    if ($idr.Count -lt 3) { throw 'insufficient measured H264 IDR packets' }
    for ($i=1; $i -lt $idr.Count; $i++) {
        if ($idr[$i] - $idr[$i-1] -ne $GOP) { throw 'encoded IDR spacing differs from requested GOP' }
    }
    return [ordered]@{packetCount=$packet+1; idrPacketIndexes=@($idr); gopFrames=$GOP}
}

function Get-AirPlayJoinDiagnostics {
    param([string[]]$Lines)
    $contexts = @{}; $before = 0; $after = 0; $unknown = 0
    foreach ($line in $Lines) {
        if ($line -match '\[h264 @ ([a-zA-Z0-9]+)\]') {
            $key = $Matches[1]
            if (-not $contexts.ContainsKey($key)) { $contexts[$key] = @{seenIDR=$false;firstVCL=$null} }
            if ($line -match 'nal_unit_type:\s*(1|5)\(') {
                if ($null -eq $contexts[$key].firstVCL) { $contexts[$key].firstVCL = [int]$Matches[1] }
                if ($Matches[1] -eq '5') { $contexts[$key].seenIDR = $true }
            }
            if ($line -match 'Missing reference picture|decode_slice_header error|concealing.*errors') {
                if ($contexts[$key].seenIDR) { $after++ } else { $before++ }
            }
        } elseif ($line -match 'Missing reference picture|decode_slice_header error|concealing.*errors') { $unknown++ }
    }
    $seenIDR = @($contexts.Values | Where-Object seenIDR).Count -gt 0
    return [ordered]@{
        status=if($before+$after+$unknown -gt 0){'FAIL'}elseif($seenIDR){'CLEAN_OBSERVED'}else{'UNKNOWN'}
        preIDRWarnings=$before;postIDRWarnings=$after;unattributedWarnings=$unknown
        contexts=@($contexts.Values)
        # Counts log-message lines; FFmpeg's repeated-message compression may
        # represent additional occurrences. These are not exact damaged-frame counts.
    }
}
