Describe 'Actual encoded AirPlay output gates' {
    BeforeEach {
        . (Join-Path $PSScriptRoot 'airplay-output-assertions.ps1')
        $probe = [pscustomobject]@{
            streams = @(
                [pscustomobject]@{codec_type='video';codec_name='h264';width=640;height=360;r_frame_rate='30/1';pix_fmt='yuv420p';has_b_frames=0},
                [pscustomobject]@{codec_type='audio';codec_name='aac';profile='LC'}
            )
            frames = @(0..89 | ForEach-Object { [pscustomobject]@{media_type='video';pict_type='P';best_effort_timestamp_time=(1 + $_ / 30.0)} })
        }
    }
    It 'accepts a clean measured 360p30 video and AAC-LC output' {
        (Assert-AirPlayEncodedOutput -Probe $probe -Height 360).frameCount | Should Be 90
    }
    It 'rejects an ignored requested height' {
        { Assert-AirPlayEncodedOutput -Probe $probe -Height 720 } | Should Throw 'dimensions'
    }
    It 'rejects B-frames even if stream metadata claims none' {
        $probe.frames[20].pict_type = 'B'
        { Assert-AirPlayEncodedOutput -Probe $probe -Height 360 } | Should Throw 'B-frame'
    }
    It 'rejects 60fps delivery even if metadata says 30fps' {
        $probe.frames = @(0..89 | ForEach-Object { [pscustomobject]@{media_type='video';pict_type='P';best_effort_timestamp_time=(1 + $_ / 60.0)} })
        { Assert-AirPlayEncodedOutput -Probe $probe -Height 360 } | Should Throw 'cadence'
    }
    It 'rejects missing silence AAC when the sender has no audio' {
        $probe.streams = @($probe.streams[0])
        { Assert-AirPlayEncodedOutput -Probe $probe -Height 360 } | Should Throw 'AAC-LC'
    }
    It 'counts multi-slice IDR only once per encoded packet' {
        $trace = @(0..90 | ForEach-Object {
            '[trace_headers @ x] Packet: 100 bytes.'
            if ($_ -in @(0,30,60,90)) { '[trace_headers @ x] 3 nal_unit_type 00101 = 5'; '[trace_headers @ x] 3 nal_unit_type 00101 = 5' }
            else { '[trace_headers @ x] 3 nal_unit_type 00001 = 1' }
        })
        $actual = Assert-AirPlayIDRInterval -Lines $trace -GOP 30
        ($actual.idrPacketIndexes -join ',') | Should Be '0,30,60,90'
    }
    It 'rejects a key-frame flag without an H264 IDR NAL' {
        $trace = @(0..90 | ForEach-Object { '[trace_headers @ x] Packet: 100 bytes, key frame.'; '[trace_headers @ x] 3 nal_unit_type 00001 = 1' })
        { Assert-AirPlayIDRInterval -Lines $trace -GOP 30 } | Should Throw 'IDR'
    }
    It 'rejects the wrong actual IDR spacing' {
        $trace = @(0..120 | ForEach-Object { '[trace_headers @ x] Packet: 100 bytes.'; if ($_ -in @(0,60,120)) { '[trace_headers @ x] 3 nal_unit_type 00101 = 5' } })
        { Assert-AirPlayIDRInterval -Lines $trace -GOP 30 } | Should Throw 'spacing'
    }
    It 'classifies warnings against each decoder context, not the first global IDR' {
        $trace = @('[h264 @ aaa] nal_unit_type: 1(non-IDR)', '[h264 @ aaa] Missing reference picture, default is 0',
            '[h264 @ aaa] nal_unit_type: 5(IDR)', '[h264 @ bbb] nal_unit_type: 1(non-IDR)',
            '[h264 @ bbb] Missing reference picture, default is 0', '[h264 @ bbb] nal_unit_type: 5(IDR)')
        $result = Get-AirPlayJoinDiagnostics -Lines $trace
        $result.status | Should Be 'FAIL'
        $result.preIDRWarnings | Should Be 2
        $result.postIDRWarnings | Should Be 0
    }
    It 'does not excuse corruption after IDR as a join warning' {
        $result = Get-AirPlayJoinDiagnostics -Lines @('[h264 @ aaa] nal_unit_type: 5(IDR)', '[h264 @ aaa] Missing reference picture, default is 0')
        $result.status | Should Be 'FAIL'
        $result.postIDRWarnings | Should Be 1
    }
}
