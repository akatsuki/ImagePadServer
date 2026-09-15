$runnerPath = Join-Path $PSScriptRoot "..\test-airplay-lifecycle.ps1"

function Get-RunnerFunctionDefinition([string]$Name) {
    $tokens = $null
    $parseErrors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile($runnerPath, [ref]$tokens, [ref]$parseErrors)
    if ($parseErrors.Count -ne 0) { throw "runner parse failed: $($parseErrors[0].Message)" }
    $function = $ast.Find({
        param($node)
        $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $Name
    }, $true)
    if ($null -eq $function) { throw "runner function was not found: $Name" }
    $function.Extent.Text
}

Describe "AirPlay lifecycle isolated runner contract" {
    BeforeEach {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-PublisherRestartScenario")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-InitialConfigOnlyScenario")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-StaticVideoScenario")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-NoNextIDRScenario")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-RotationAfterReconnectScenario")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-RotationAfterReconnectTrace")))
    }

    It "allocates a free non-default TCP port and releases the reservation" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Get-FreeTcpPort")))
        $port = Get-FreeTcpPort
        $port | Should Not Be 8080
        $port | Should Not Be 8081
        $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $port)
        try { $listener.Start() } finally { $listener.Stop() }
    }

    It "allows an explicit isolated HTTP port without using the live app ports" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Resolve-LifecycleHTTPPort")))
        (Resolve-LifecycleHTTPPort -RequestedPort 64021) | Should Be 64021
        foreach ($livePort in @(8080, 8081)) {
            $threw = $false
            try { Resolve-LifecycleHTTPPort -RequestedPort $livePort | Out-Null } catch { $threw = $true }
            $threw | Should Be $true
        }
    }

    It "builds a fully isolated synthetic environment" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $root = Join-Path $TestDrive "data"
        $env = New-LifecycleEnvironment -DataRoot $root -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -Scenario "video-only" `
            -Duration ([TimeSpan]::FromSeconds(12)) -FixtureReport "fixture.json"

        $env.IMAGEPAD_TEST_ISOLATED_LIFECYCLE | Should Be "1"
        $env.IMAGEPAD_DATA_DIR | Should Be $root
        $env.IMAGEPAD_HOST | Should Be "127.0.0.1"
        $env.IMAGEPAD_PORT | Should Be "49152"
        $env.IMAGEPAD_AIRPLAY_PIPELINE | Should Be "source-clock"
        $env.IMAGEPAD_AIRPLAY_RECEIVER | Should Be "fixture.exe"
        $env.IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE | Should Be "bridge.exe"
        $env.IMAGEPAD_AIRPLAY_FIXTURE_ADAPTER | Should Be "1"
        $env.IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO | Should Be "1"
        $env.IMAGEPAD_AIRPLAY_FIXTURE_DURATION | Should Be "12000ms"
        $env.IMAGEPAD_SOURCE_CLOCK_DEBUG | Should Be "1"
    }

    It "uses the official Windows GStreamer plugin scanner location" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $runtimeRoot = Join-Path $TestDrive "gstreamer"
        $env = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -Scenario "video-only" `
            -Duration ([TimeSpan]::FromSeconds(12)) -FixtureReport "fixture.json" `
            -GStreamerRuntimeRoot $runtimeRoot

        $env.GST_PLUGIN_SCANNER | Should Be (Join-Path $runtimeRoot "libexec\gstreamer-1.0\gst-plugin-scanner.exe")
    }

    It "enables a real AAC fixture only for the delayed-audio scenario" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $env = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -AudioFile "audio.aac" `
            -Scenario "audio-start-after-silence" -Duration ([TimeSpan]::FromSeconds(24)) `
            -FixtureReport "fixture.json"

        $env.IMAGEPAD_AIRPLAY_FIXTURE_AUDIO_FILE | Should Be "audio.aac"
        $env.Contains("IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO") | Should Be $false
    }

    It "keeps the publisher restart scenario video-only" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $env = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -Scenario "publisher-restart-once" `
            -Duration ([TimeSpan]::FromSeconds(24)) -FixtureReport "fixture.json"
        $env.IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO | Should Be "1"
        $env.Contains("IMAGEPAD_AIRPLAY_FIXTURE_AUDIO_FILE") | Should Be $false
    }

    It "maps the publisher restart feasibility scenarios to isolated fixture controls" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $initialOnly = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -Scenario "publisher-restart-initial-config-only" `
            -Duration ([TimeSpan]::FromSeconds(24)) -FixtureReport "fixture.json" -VideoTraceFile "video-trace.jsonl"
        $combined = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -Scenario "publisher-restart-static-initial-config-only" `
            -Duration ([TimeSpan]::FromSeconds(24)) -FixtureReport "fixture.json"

        $initialOnly.IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO | Should Be "1"
        $initialOnly.IMAGEPAD_AIRPLAY_FIXTURE_INITIAL_CONFIG_ONLY | Should Be "1"
        $initialOnly.IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_TRACE | Should Be "video-trace.jsonl"
        $combined.IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO | Should Be "1"
        $combined.IMAGEPAD_AIRPLAY_FIXTURE_INITIAL_CONFIG_ONLY | Should Be "1"
    }

    It "maps the no-next-IDR publisher restart scenario to video-only initial-config controls" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $env = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -Scenario "publisher-restart-no-next-idr-initial-config-only" `
            -Duration ([TimeSpan]::FromSeconds(24)) -FixtureReport "fixture.json"

        $env.IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO | Should Be "1"
        $env.IMAGEPAD_AIRPLAY_FIXTURE_INITIAL_CONFIG_ONLY | Should Be "1"
    }

    It "maps the rotation-after-reconnect scenario to a separate portrait fixture" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $env = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "landscape.h264" -RotatedVideoFile "portrait.h264" `
            -Scenario "publisher-restart-rotation-initial-config-only" -Duration ([TimeSpan]::FromSeconds(24)) `
            -FixtureReport "fixture.json"

        (Test-RotationAfterReconnectScenario -Scenario "publisher-restart-rotation-initial-config-only") | Should Be $true
        $env.IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO | Should Be "1"
        $env.IMAGEPAD_AIRPLAY_FIXTURE_INITIAL_CONFIG_ONLY | Should Be "1"
        $env.IMAGEPAD_AIRPLAY_FIXTURE_ROTATED_VIDEO_FILE | Should Be "portrait.h264"
        $env.IMAGEPAD_AIRPLAY_FIXTURE_ROTATE_AFTER_RECONNECT | Should Be "1"
    }

    It "requires rotation then cached config then fresh IDR on connection two" {
        $events = @(
            [pscustomobject]@{ event = "connection-open"; connection = 2; sourceSequence = 0; connectionWatermarkSourceSequence = 120 },
            [pscustomobject]@{ event = "rotation-after-reconnect"; connection = 2; sourceSequence = 0; connectionWatermarkSourceSequence = 120 },
            [pscustomobject]@{ event = "bootstrap_config"; connection = 2; sourceSequence = 121; connectionWatermarkSourceSequence = 120; configSource = "session-cache" },
            [pscustomobject]@{ event = "post_watermark_idr"; connection = 2; sourceSequence = 121; connectionWatermarkSourceSequence = 120; completeIDR = $true }
        )

        $evidence = Assert-RotationAfterReconnectTrace -Events $events
        $evidence.connection | Should Be 2
        $evidence.sourceSequence | Should Be 121
        $evidence.configSource | Should Be "session-cache"

        $threw = $false
        try {
            Assert-RotationAfterReconnectTrace -Events @($events[0], $events[2], $events[3]) | Out-Null
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }

    It "generates a first-IDR-only H264 fixture for the no-next-IDR scenario" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $text.Contains("publisher-restart-no-next-idr-initial-config-only") | Should Be $true
        $text.Contains("min-keyint") | Should Be $true
        $text.Contains("scenecut=0") | Should Be $true
        $text.Contains("open-gop=0") | Should Be $true
        $text.Contains("keyint=") | Should Be $true
    }

    It "records an AAC HLS probe without leaking credentials" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Convert-LifecycleEvidenceURL")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Convert-HLSFFprobeAACEvidence")))
        $probe = [pscustomobject]@{
            streams = @(
                [pscustomobject]@{ index = 0; codec_type = "video"; codec_name = "h264" },
                [pscustomobject]@{ index = 1; codec_type = "audio"; codec_name = "aac"; sample_rate = "48000"; channels = 2 }
            )
        }
        $evidence = Convert-HLSFFprobeAACEvidence -PlaylistURL "http://user:secret@127.0.0.1:9999/live.m3u8?token=private" -Probe $probe

        $evidence.hasAACStream | Should Be $true
        $evidence.audioStreamCount | Should Be 1
        $evidence.playlistURL | Should Be "http://127.0.0.1:9999/live.m3u8"
        ($evidence | ConvertTo-Json -Depth 8) -match "secret|token|user" | Should Be $false
    }

    It "accepts gen2 waiting-for-IDR without treating publisher-ready as media recovery" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-PublisherRestartWaitingForIDR")))
        $state = [pscustomobject]@{
            airplay = [pscustomobject]@{
                running = $true
                receiverRunning = $true
                bridgeRunning = $true
                mediaReady = $false
                phase = "waiting-media"
            }
            obs = [pscustomobject]@{ mediaID = "session-a" }
        }
        $events = @(
            [pscustomobject]@{ schema = 2; sessionId = "session-a"; publisherGeneration = 2; event = "publisher-ready"; at = "2026-09-08T00:00:00Z" }
        )

        $evidence = Assert-PublisherRestartWaitingForIDR -State $state -Events $events `
            -SessionID "session-a" -Generation 2 -StableSeconds 10

        $evidence.waitingForIDR | Should Be $true
        $evidence.realVideoRecovered | Should Be $false
        $evidence.mediaReady | Should Be $false
        $evidence.publisherEvents | Should Be @("publisher-ready")
        $evidence.restartLoopDetected | Should Be $false
    }

    It "allows the waiting-for-IDR observer to request one explicit publisher retry" {
        $definition = Get-RunnerFunctionDefinition "Wait-PublisherRestartWaitingForIDR"
        $definition.Contains('api/airplay/retry') | Should Be $true
        $definition.Contains('$retryRequested = $false') | Should Be $true
        $definition.Contains('-not $retryRequested') | Should Be $true
    }

    It "keeps the no-next-IDR result fields explicit and does not require fresh HLS" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleResult")))
        $result = New-LifecycleResult -Scenario "publisher-restart-no-next-idr-initial-config-only" -EvidenceDirectory "evidence"
        $result.waitingForIDR | Should Be $false
        $result.realVideoRecovered | Should Be $false
        $result.fallbackHLSContinued | Should Be $null

        $text = Get-Content -LiteralPath $runnerPath -Raw
        $text.Contains('hls-preview-recovered') | Should Be $true
        $text.Contains('fresh HLS') | Should Be $false
        $text.Contains('if ((Test-InitialConfigOnlyScenario -Scenario $Scenario) -and') | Should Be $true
    }

    It "falls back to the established HLS URL and requires it to remain readable while waiting for IDR" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Resolve-HLSFallbackPreviewPath")))
        Resolve-HLSFallbackPreviewPath -CurrentPreviewPath "" -EstablishedPreviewPath "http://127.0.0.1/live.m3u8" |
            Should Be "http://127.0.0.1/live.m3u8"
        Resolve-HLSFallbackPreviewPath -CurrentPreviewPath "/new.m3u8" -EstablishedPreviewPath "/old.m3u8" |
            Should Be "/new.m3u8"
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $text.Contains('Assert-HLSFallbackContinued -Observation $result.fallbackHLS') | Should Be $true
        $text.Contains('did not keep fallback HLS readable') | Should Be $true
    }

    It "recognizes a readable fallback HLS playlist without binding boolean operators as parameters" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Convert-LifecycleEvidenceURL")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Convert-HTTPContentText")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-HLSMasterPlaylist")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-HLSMediaPlaylist")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Get-HLSFallbackObservation")))
        function Invoke-WebRequest {
            param([switch]$UseBasicParsing, [string]$Uri, [int]$TimeoutSec)
            [pscustomobject]@{
                StatusCode = 200
                Content = "#EXTM3U`n#EXT-X-VERSION:9`n#EXT-X-STREAM-INF:BANDWIDTH=1200000`nvideo.m3u8`n"
            }
        }

        $observation = Get-HLSFallbackObservation -BaseURL "http://127.0.0.1:9000/" `
            -PreviewPath "/stream/live.m3u8?token=secret"

        $observation.observed | Should Be $true
        $observation.continued | Should Be $true
        $observation.statusCode | Should Be 200
        $observation.playlistURL | Should Be "http://127.0.0.1:9000/stream/live.m3u8"
    }

    It "accepts a no-next-IDR fixture only when reconnect has no post-watermark IDR" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-LifecycleScenarioResult")))
        $fixture = [pscustomobject]@{
            video = [pscustomobject]@{
                sent = 100
                reconnects = 1
                inputConfigFrames = 1
                cacheConfigReplays = 0
                configSent = 1
                postWatermarkIDRs = 0
                firstPostWatermarkIDRSourceSequence = 0
                traceComplete = $true
                traceEventsDropped = 0
            }
            audio = [pscustomobject]@{ enabled = $false; sent = 0 }
        }
        { Assert-LifecycleScenarioResult -Scenario "publisher-restart-no-next-idr-initial-config-only" `
            -Fixture $fixture -MediaReadyAfterStartMS 1000 } | Should Not Throw

        $fixture.video.postWatermarkIDRs = 1
        $threw = $false
        try {
            Assert-LifecycleScenarioResult -Scenario "publisher-restart-no-next-idr-initial-config-only" `
                -Fixture $fixture -MediaReadyAfterStartMS 1000
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }

    It "rejects missing observations or premature media publication in no-initial-media" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-NoInitialMediaState")))
        $state = [pscustomobject]@{
            airplay = [pscustomobject]@{ mediaReady = $false; running = $true; receiverRunning = $true; bridgeRunning = $true }
            obs = [pscustomobject]@{ connected = $false; publishing = $false; previewURL = ""; publicHLSURL = ""; rtsptURL = "" }
            current = [pscustomobject]@{ id = "" }
            history = @()
        }
        Assert-NoInitialMediaState -State $state
        $state.current = $null # Actual IPS has no current object before the first media.
        Assert-NoInitialMediaState -State $state
        $state.current = [pscustomobject]@{ id = "" }
        $state.obs.publishing = $true # Direct publisher is armed; no accepted media or public URL yet.
        Assert-NoInitialMediaState -State $state
        $threw = $false
        try { Assert-NoInitialMediaState -State $null } catch { $threw = $true }
        $threw | Should Be $true
        $state.airplay.mediaReady = $true
        $threw = $false
        try { Assert-NoInitialMediaState -State $state } catch { $threw = $true }
        $threw | Should Be $true
        $state.airplay.mediaReady = $false
        foreach ($field in @("previewURL", "publicHLSURL", "rtsptURL")) {
            $state.obs.$field = "unexpected-output"
            $threw = $false
            try { Assert-NoInitialMediaState -State $state } catch { $threw = $true }
            $threw | Should Be $true
            $state.obs.$field = ""
        }
        $state.history = @([pscustomobject]@{ id = "premature-recording" })
        $threw = $false
        try { Assert-NoInitialMediaState -State $state } catch { $threw = $true }
        $threw | Should Be $true
        $state.history = @()
        $state.current.id = "premature-current"
        $threw = $false
        try { Assert-NoInitialMediaState -State $state } catch { $threw = $true }
        $threw | Should Be $true
    }

    It "enables a bounded no-initial-media case without real audio" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $env = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -Scenario "no-initial-media" `
            -Duration ([TimeSpan]::FromSeconds(24)) -FixtureReport "fixture.json" -TestNoSignalSeconds 4
        $env.IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO | Should Be "1"
        $env.IMAGEPAD_TEST_AIRPLAY_NO_SIGNAL_SECONDS | Should Be "4"
        $env.IMAGEPAD_TEST_ISOLATED_LIFECYCLE | Should Be "1"
    }

    It "can exercise the production deadline without inheriting a shortened override" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $env = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -Scenario "no-initial-media" `
            -Duration ([TimeSpan]::FromSeconds(200)) -FixtureReport "fixture.json" -UseProductionNoSignalTimeout
        $env.IMAGEPAD_TEST_AIRPLAY_NO_SIGNAL_SECONDS | Should Be ""
    }

    It "sets a short no-signal timeout only for the isolated no-signal scenario" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleEnvironment")))
        $env = New-LifecycleEnvironment -DataRoot "data" -Port 49152 -ReceiverAdapterPath "fixture.exe" `
            -BridgePath "bridge.exe" -MediaMTXPath "mediamtx.exe" -FFmpegPath "ffmpeg.exe" `
            -FFprobePath "ffprobe.exe" -VideoFile "video.h264" -Scenario "no-signal-after-media" `
            -Duration ([TimeSpan]::FromSeconds(24)) -FixtureReport "fixture.json" -TestNoSignalSeconds 4
        $env.IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO | Should Be "1"
        $env.IMAGEPAD_TEST_AIRPLAY_NO_SIGNAL_SECONDS | Should Be "4"
        $env.IMAGEPAD_TEST_ISOLATED_LIFECYCLE | Should Be "1"
    }

    It "keeps synthetic evidence below production readiness" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-LifecycleResult")))
        $result = New-LifecycleResult -Scenario "video-only" -EvidenceDirectory "evidence"
        $result.layer | Should Be "IPS + fixture receiver"
        $result.evidenceTier | Should Be "SYN"
        $result.productionReady | Should Be $false
        $result.status | Should Be "NOT_RUN"
    }

    It "installs the fixture adapter with the required source-clock capability manifest" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Install-FixtureReceiverAdapter")))
        $source = Join-Path $TestDrive "fixture.exe"
        [IO.File]::WriteAllBytes($source, [byte[]](1, 2, 3, 4))
        $receiverRoot = Join-Path $TestDrive "receiver"

        $installed = Install-FixtureReceiverAdapter -SourcePath $source -DestinationDirectory $receiverRoot
        $manifest = Get-Content -LiteralPath (Join-Path $receiverRoot "imagepad-source-clock-capabilities.json") -Raw | ConvertFrom-Json

        $installed | Should Be (Join-Path $receiverRoot "uxplay-source-clock.exe")
        $manifest.schema | Should Be 1
        $manifest.protocolVersion | Should Be 1
        $manifest.binary | Should Be "uxplay-source-clock.exe"
        $manifest.binarySha256 | Should Be ((Get-FileHash -LiteralPath $installed -Algorithm SHA256).Hash.ToLowerInvariant())
        @($manifest.features) -contains "egress-metrics-v2" | Should Be $true
    }

    It "requires all candidate executables before startup" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-CandidatePaths")))
        $missing = Join-Path $TestDrive "missing.exe"
        $threw = $false
        try {
            Assert-CandidatePaths -IPSPath $missing -BridgePath $missing -ReceiverAdapterPath $missing `
                -MediaMTXPath $missing -FFmpegPath $missing -FFprobePath $missing
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }

    It "owns cleanup by process object and uses no global process kill" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $text.Contains('Stop-OwnedLifecycleProcess') | Should Be $true
        $text.Contains('Save-OwnedLifecycleProcessOutput') | Should Be $true
        $text.Contains('ReadToEndAsync') | Should Be $true
        ($text -notmatch 'Get-Process\s+.*(ffmpeg|mediamtx|imagepad)') | Should Be $true
        ($text -notmatch 'taskkill') | Should Be $true
    }

    It "generates frame-delimited H264 input rather than one oversized access unit" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $text.Contains('aud=1:repeat-headers=1') | Should Be $true
        $text.Contains('testsrc2=size=640x360:rate=60') | Should Be $true
    }

    It "uses a motionless source for static publisher restart feasibility" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $text.Contains('color=c=blue:size=640x360:rate=60') | Should Be $true
        $text.Contains('Test-PublisherRestartScenario') | Should Be $true
    }

    It "accepts only a real HLS media playlist with at least one segment" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-HLSMediaPlaylist")))
        (Test-HLSMediaPlaylist -Content "#EXTM3U`n#EXT-X-TARGETDURATION:1`n#EXTINF:1.0,`nsegment0.ts") | Should Be $true
        (Test-HLSMediaPlaylist -Content "#EXTM3U`n#EXT-X-TARGETDURATION:1") | Should Be $false
        (Test-HLSMediaPlaylist -Content "#EXTM3U`n#EXT-X-STREAM-INF:BANDWIDTH=1000000`nvideo1_stream.m3u8") | Should Be $false
        (Test-HLSMediaPlaylist -Content "not hls") | Should Be $false
    }

    It "recognizes an HLS master playlist and returns only its stream variants" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-HLSMasterPlaylist")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Get-HLSStreamVariantReferences")))
        $master = @"
#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",URI="audio1_stream.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1000000,CODECS="avc1.640028,mp4a.40.2",AUDIO="audio"
video1_stream.m3u8
"@

        (Test-HLSMasterPlaylist -Content $master) | Should Be $true
        @(Get-HLSStreamVariantReferences -Content $master) | Should Be @("video1_stream.m3u8")
        (Test-HLSMasterPlaylist -Content "#EXTM3U`n#EXTINF:1.0,`nsegment0.ts") | Should Be $false
    }

    It "decodes byte-array HTTP playlist content as UTF-8" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Convert-HTTPContentText")))
        $bytes = [Text.Encoding]::UTF8.GetBytes("#EXTM3U`nsegment0.ts")
        (Convert-HTTPContentText -Content $bytes) | Should Be "#EXTM3U`nsegment0.ts"
    }

    It "keeps the filename from a relative HLS segment reference" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Get-LifecycleSHA256")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-HLSPlaylistMarker")))
        $marker = New-HLSPlaylistMarker -PlaylistContent "#EXTM3U`n#EXT-X-MEDIA-SEQUENCE:7`nsegment7.mp4" `
            -SegmentReference "segment7.mp4?token=redacted" -SegmentBytes ([byte[]](1, 2, 3)) -ObservedAt (Get-Date)

        $marker.segmentName | Should Be "segment7.mp4"
        $marker.mediaSequence | Should Be 7
    }

    It "records HLS and the finalized fixture report in the result schema" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $text.Contains('hls = $null') | Should Be $true
        $text.Contains('$currentStage = "hls-preview"') | Should Be $true
        $text.Contains('-StateURI ($baseURL + "api/state")') | Should Be $true
        $text.Contains('$result.fixture = Read-LifecycleJSON -Path $fixtureReport') | Should Be $true
        $text.Contains('fixtureVideoTrace = @()') | Should Be $true
        $text.Contains('$result.fixtureVideoTrace = @(Read-LifecycleJSONLines') | Should Be $true
        $text.Contains('parseErrors = @()') | Should Be $true
    }

    It "preserves publisher stage artifacts before the AirPlay teardown removes them" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $copyCall = $text.LastIndexOf('Copy-LifecyclePublisherEvidence -SourceRoot')
        $teardownCall = $text.LastIndexOf('api/airplay/end')

        $text.Contains('publisherEvidenceDirectory = $null') | Should Be $true
        $text.Contains('(Join-Path $dataRoot "media\airplay")') | Should Be $true
        $copyCall | Should BeGreaterThan -1
        $teardownCall | Should BeGreaterThan $copyCall
    }

    It "reads generation events from the direct publisher artifact root" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $text.Contains('Join-Path $DataRoot ("media\airplay\{0}\publisher-{1:D4}.events.jsonl"') | Should Be $true
        $text.Contains('publisher generation stages did not complete before the deadline (last=$lastObservation)') | Should Be $true
    }

    It "projects API state to a credential-free evidence schema" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Convert-LifecycleEvidenceState")))
        $state = [pscustomobject]@{
            airplay = [pscustomobject]@{
                running = $true
                receiverRunning = $true
                bridgeRunning = $true
                mediaReady = $false
                phase = "waiting-media"
                message = "waiting"
                receiverPID = 123
                receiverPath = "C:\Users\private\receiver.exe"
            }
            obs = [pscustomobject]@{
                connected = $true
                publishing = $true
                mediaID = "session-a"
                previewURL = "http://127.0.0.1:9999/live.m3u8?token=secret-token"
                rtsptURL = "rtsp://user:password@127.0.0.1:8554/live"
            }
            urls = [pscustomobject]@{ phoneURL = "http://lan/?token=secret-token" }
        }

        $safe = Convert-LifecycleEvidenceState -State $state
        $json = $safe | ConvertTo-Json -Depth 8 -Compress

        $safe.airplay.phase | Should Be "waiting-media"
        $safe.obs.mediaID | Should Be "session-a"
        $json.Contains("secret-token") | Should Be $false
        $json.Contains("password") | Should Be $false
        $json.Contains("private") | Should Be $false
        $json.Contains("previewURL") | Should Be $false
        $json.Contains("phoneURL") | Should Be $false
    }

    It "keeps valid fixture evidence when JSON or JSONL contains a broken record" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Read-LifecycleJSON")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Read-LifecycleJSONLines")))
        $good = Join-Path $TestDrive "fixture.json"
        $bad = Join-Path $TestDrive "broken.json"
        $lines = Join-Path $TestDrive "trace.jsonl"
        [IO.File]::WriteAllText($good, '{"sent":4}')
        [IO.File]::WriteAllText($bad, '{broken')
        [IO.File]::WriteAllText($lines, "{`"sequence`":1}`n{broken`n{`"sequence`":2}`n")
        $errors = [Collections.ArrayList]::new()

        (Read-LifecycleJSON -Path $good -Errors $errors).sent | Should Be 4
        (Read-LifecycleJSON -Path $bad -Errors $errors) | Should Be $null
        $trace = @(Read-LifecycleJSONLines -Path $lines -Errors $errors)
        $trace.Count | Should Be 2
        $trace[0].sequence | Should Be 1
        $trace[1].sequence | Should Be 2
        $errors.Count | Should Be 2
    }

    It "rejects malformed publisher-generation JSONL instead of accepting later lines" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Read-StrictLifecycleJSONLines")))
        $path = Join-Path $TestDrive "publisher.events.jsonl"
        [IO.File]::WriteAllLines($path, @('{"event":"publisher-ready"}', '{broken', '{"event":"video-decoded"}'))
        $threw = $false
        try {
            [void](Read-StrictLifecycleJSONLines -Path $path)
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }

    It "uses collision-resistant evidence directories and atomic result replacement" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $text.Contains('[Guid]::NewGuid().ToString("N")') | Should Be $true
        $text.Contains('[IO.File]::Move($temporaryPath, $Path, $true)') | Should Be $true
    }

    It "requires delayed audio to begin only after video media readiness" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-LifecycleScenarioResult")))
        $fixture = [pscustomobject]@{
            video = [pscustomobject]@{ sent = 100 }
            audio = [pscustomobject]@{ enabled = $true; fileOpened = $true; connections = 1; sent = 20 }
        }
        { Assert-LifecycleScenarioResult -Scenario "audio-start-after-silence" -Fixture $fixture `
            -MediaReadyAfterStartMS 1200 -ScheduledAudioStartMS 18000 } | Should Not Throw
        $threw = $false
        try {
            Assert-LifecycleScenarioResult -Scenario "audio-start-after-silence" -Fixture $fixture `
                -MediaReadyAfterStartMS 18500 -ScheduledAudioStartMS 18000
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }

    It "accepts publisher recovery only with the same receiver identity, exited old bridge, replacement bridge, and next generation" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-PublisherRecovery")))
        $created = [DateTime]::Parse("2026-09-08T00:00:00Z").ToUniversalTime()
        { Assert-PublisherRecovery -ReceiverBefore 101 -ReceiverAfter 101 -BridgeBefore 202 -BridgeAfter 303 `
            -SessionBefore "session-a" -SessionAfter "session-a" -RecoveringObserved $true `
            -ReceiverCreationBefore $created -ReceiverCreationAfter $created -OldBridgeExitConfirmed $true `
            -GenerationBefore 1 -GenerationAfter 2 } | Should Not Throw
        $threw = $false
        try {
            Assert-PublisherRecovery -ReceiverBefore 101 -ReceiverAfter 101 -BridgeBefore 202 -BridgeAfter 303 `
                -SessionBefore "session-a" -SessionAfter "session-a" -RecoveringObserved $true `
                -ReceiverCreationBefore $created -ReceiverCreationAfter $created.AddSeconds(1) -OldBridgeExitConfirmed $true `
                -GenerationBefore 1 -GenerationAfter 2
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }

    It "rejects a recovered HLS marker that reuses the old segment or predates encoded video" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-FreshHLSMarker")))
        $encodedAt = [DateTime]::Parse("2026-09-08T00:00:03Z").ToUniversalTime()
        $baseline = [pscustomobject]@{
            mediaSequence = 10
            segmentName = "seg10.ts"
            playlistSha256 = "playlist-old"
            segmentSha256 = "segment-old"
            observedAtUtc = "2026-09-08T00:00:01Z"
        }
        $fresh = [pscustomobject]@{
            mediaSequence = 11
            segmentName = "seg11.ts"
            playlistSha256 = "playlist-new"
            segmentSha256 = "segment-new"
            observedAtUtc = "2026-09-08T00:00:04Z"
        }
        (Test-FreshHLSMarker -Baseline $baseline -Candidate $fresh -NotBefore $encodedAt) | Should Be $true
        (Test-FreshHLSMarker -Baseline $baseline -Candidate $baseline -NotBefore $encodedAt) | Should Be $false
        $tooEarly = $fresh.PSObject.Copy()
        $tooEarly.observedAtUtc = "2026-09-08T00:00:02Z"
        (Test-FreshHLSMarker -Baseline $baseline -Candidate $tooEarly -NotBefore $encodedAt) | Should Be $false
    }

    It "requires ordered generation events tied to the fixture post-watermark IDR" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-PublisherGenerationStages")))
        $events = @(
            [pscustomobject]@{ schema = 2; sessionId = "session-a"; publisherGeneration = 2; event = "publisher-ready"; at = "2026-09-08T00:00:00Z" },
            [pscustomobject]@{ schema = 2; sessionId = "session-a"; publisherGeneration = 2; event = "video-input-idr"; at = "2026-09-08T00:00:01Z"; runningTimeNs = 10; sourceNtpNs = 900 },
            [pscustomobject]@{ schema = 2; sessionId = "session-a"; publisherGeneration = 2; event = "video-decoded"; at = "2026-09-08T00:00:02Z"; runningTimeNs = 20 },
            [pscustomobject]@{ schema = 2; sessionId = "session-a"; publisherGeneration = 2; event = "video-encoded"; at = "2026-09-08T00:00:03Z"; runningTimeNs = 30 }
        )
        { Assert-PublisherGenerationStages -Events $events -SessionID "session-a" -Generation 2 -FixtureRemoteNTPNS 900 } | Should Not Throw
        $wrongNTP = $false
        try {
            Assert-PublisherGenerationStages -Events $events -SessionID "session-a" -Generation 2 -FixtureRemoteNTPNS 901
        } catch {
            $wrongNTP = $true
        }
        $wrongNTP | Should Be $true
        $wrongOrder = @($events[0], $events[2], $events[1], $events[3])
        $orderRejected = $false
        try {
            Assert-PublisherGenerationStages -Events $wrongOrder -SessionID "session-a" -Generation 2 -FixtureRemoteNTPNS 900
        } catch {
            $orderRejected = $true
        }
        $orderRejected | Should Be $true
    }

    It "accepts fixture bootstrap evidence only as cache CONFIG followed by post-watermark IDR" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Get-FixtureBootstrapEvidence")))
        $events = @(
            [pscustomobject]@{ event = "connection-open"; connection = 2 },
            [pscustomobject]@{ event = "bootstrap_config"; connection = 2; configSource = "session-cache"; sourceSequence = 12; connectionWatermarkSourceSequence = 10; remoteNtpNs = 900 },
            [pscustomobject]@{ event = "post_watermark_idr"; connection = 2; completeIDR = $true; sourceSequence = 12; connectionWatermarkSourceSequence = 10; remoteNtpNs = 900 }
        )
        $evidence = Get-FixtureBootstrapEvidence -Events $events
        $evidence.connection | Should Be 2
        $evidence.postWatermarkIDRSourceSequence | Should Be 12
        $evidence.remoteNtpNs | Should Be 900
        $bad = @($events[0], $events[2], $events[1])
        $threw = $false
        try {
            [void](Get-FixtureBootstrapEvidence -Events $bad)
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }
}
