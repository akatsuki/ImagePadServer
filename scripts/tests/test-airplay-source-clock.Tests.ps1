$runnerPath = Join-Path $PSScriptRoot "..\test-airplay-source-clock.ps1"

function Get-RunnerFunctionDefinition([string]$Name) {
    $tokens = $null
    $parseErrors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile(
        $runnerPath,
        [ref]$tokens,
        [ref]$parseErrors
    )
    if ($parseErrors.Count -ne 0) {
        throw "runner parse failed: $($parseErrors[0].Message)"
    }
    $function = $ast.Find({
        param($node)
        $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
            $node.Name -eq $Name
    }, $true)
    if ($null -eq $function) {
        throw "runner function was not found: $Name"
    }
    return $function.Extent.Text
}

Describe "AirPlay source-clock runner no-audio contract" {
    It "passes only -no-audio for finite video-only scenarios" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Get-FixtureAudioArguments")))

        foreach ($scenario in @("video-only", "video-only-forever")) {
            $got = @(Get-FixtureAudioArguments -Scenario $scenario -Endpoint "127.0.0.1:9" -AudioPath "missing.aac")
            ($got -contains "-no-audio") | Should Be $true
            ($got -contains "-audio") | Should Be $false
            ($got -contains "-audio-file") | Should Be $false
            ($got -contains "127.0.0.1:9") | Should Be $false
            ($got -contains "missing.aac") | Should Be $false
        }
    }

    It "keeps endpoint and file arguments for audio-enabled scenarios" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Get-FixtureAudioArguments")))

        $got = @(Get-FixtureAudioArguments -Scenario "steady-60" -Endpoint "127.0.0.1:9" -AudioPath "audio.aac")
        ($got -contains "-audio") | Should Be $true
        ($got -contains "127.0.0.1:9") | Should Be $true
        ($got -contains "-audio-file") | Should Be $true
        ($got -contains "audio.aac") | Should Be $true
        ($got -contains "-no-audio") | Should Be $false
    }

    It "rejects activity in a no-audio fixture report" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-FixtureAudioReport")))

        $threw = $false
        try {
            Assert-FixtureAudioReport -NoAudio $true -Audio ([pscustomobject]@{
                enabled = $false
                fileOpened = $false
                connections = 1
                sent = 0
            })
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }

    It "accepts the four no-audio zero values" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-FixtureAudioReport")))

        Assert-FixtureAudioReport -NoAudio $true -Audio ([pscustomobject]@{
            enabled = $false
            fileOpened = $false
            connections = 0
            sent = 0
        })
    }

    It "rejects missing no-audio evidence fields" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-FixtureAudioReport")))

        $threw = $false
        try {
            Assert-FixtureAudioReport -NoAudio $true -Audio ([pscustomobject]@{})
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }

    It "rejects null no-audio evidence values" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-FixtureAudioReport")))

        $threw = $false
        try {
            Assert-FixtureAudioReport -NoAudio $true -Audio ([pscustomobject]@{
                enabled = $null
                fileOpened = $false
                connections = 0
                sent = 0
            })
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }

    It "rejects string-shaped no-audio evidence values" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-FixtureAudioReport")))

        $threw = $false
        try {
            Assert-FixtureAudioReport -NoAudio $true -Audio ([pscustomobject]@{
                enabled = "false"
                fileOpened = "false"
                connections = "0"
                sent = "0"
            })
        } catch {
            $threw = $true
        }
        $threw | Should Be $true
    }
}

Describe "AirPlay source-clock runner audio format-change contract" {
    It "passes the secondary AAC file only for the format-change scenario" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Get-FixtureAudioArguments")))

        $changed = @(Get-FixtureAudioArguments -Scenario "audio-format-change" -Endpoint "127.0.0.1:9" `
            -AudioPath "primary.aac" -AudioFormatChangePath "secondary.aac")
        ($changed -contains "-audio-format-change-file") | Should Be $true
        ($changed -contains "secondary.aac") | Should Be $true

        $steady = @(Get-FixtureAudioArguments -Scenario "steady-60" -Endpoint "127.0.0.1:9" `
            -AudioPath "primary.aac" -AudioFormatChangePath "secondary.aac")
        ($steady -contains "-audio-format-change-file") | Should Be $false
        ($steady -contains "secondary.aac") | Should Be $false
    }

    It "accepts the exact fixture format-change report for the selected primary channel count" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-FixtureAudioFormatChangeReport")))

        $stereo = [pscustomobject]@{
            formatChanges = 1
            initialSampleRate = 44100
            initialChannels = 2
            finalSampleRate = 48000
            finalChannels = 1
            secondaryFileOpened = $true
        }
        $mono = $stereo.PSObject.Copy()
        $mono.initialChannels = 1
        Assert-FixtureAudioFormatChangeReport -Audio $stereo -ExpectedInitialChannels 2
        Assert-FixtureAudioFormatChangeReport -Audio $mono -ExpectedInitialChannels 1

        foreach ($bad in @(
            [pscustomobject]@{ formatChanges = 0; initialSampleRate = 44100; initialChannels = 2; finalSampleRate = 48000; finalChannels = 1; secondaryFileOpened = $true },
            [pscustomobject]@{ formatChanges = 1; initialSampleRate = 44100; initialChannels = 2; finalSampleRate = 44100; finalChannels = 2; secondaryFileOpened = $true },
            [pscustomobject]@{ formatChanges = 1; initialSampleRate = 44100; initialChannels = 2; finalSampleRate = 48000; finalChannels = 1; secondaryFileOpened = $false }
        )) {
            $threw = $false
            try { Assert-FixtureAudioFormatChangeReport -Audio $bad -ExpectedInitialChannels 2 } catch { $threw = $true }
            $threw | Should Be $true
        }

        $threw = $false
        try { Assert-FixtureAudioFormatChangeReport -Audio $mono -ExpectedInitialChannels 2 } catch { $threw = $true }
        $threw | Should Be $true
    }

    It "parses and distinguishes the expected one-kilohertz and six-hundred-hertz windows" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Parse-AudioZeroCrossingRate")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-AudioToneTransition")))

        $primary = Parse-AudioZeroCrossingRate "[Parsed_astats_0] Zero crossings rate: 0.041667"
        $secondary = Parse-AudioZeroCrossingRate "[Parsed_astats_0] Zero crossings rate: 0.025000"
        $primary | Should Be 0.041667
        $secondary | Should Be 0.025
        (Test-AudioToneTransition -PrimaryRate $primary -SecondaryRate $secondary) | Should Be $true
        (Test-AudioToneTransition -PrimaryRate $primary -SecondaryRate $primary) | Should Be $false
        (Test-AudioToneTransition -PrimaryRate $secondary -SecondaryRate $primary) | Should Be $false
    }

    It "declares the scenario and measures the finalized recording" {
        $text = Get-Content -LiteralPath $runnerPath -Raw

        ($text.Contains('"audio-format-change"')) | Should Be $true
        ($text.Contains('audio-format-change-tones.txt')) | Should Be $true
        $publisherExit = $text.IndexOf('Wait-PublisherForCleanExit -Publisher $publisher')
        $recording = $text.IndexOf('$currentStage = "recording"')
        $toneMeasurement = $text.IndexOf('$currentStage = "audio-format-change-output"')
        ($publisherExit -ge 0) | Should Be $true
        ($recording -gt $publisherExit) | Should Be $true
        ($toneMeasurement -gt $recording) | Should Be $true
    }

    It "requires a bounded clean publisher exit before recording inspection" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Wait-PublisherForCleanExit")))

        $clean = [pscustomobject]@{ ExitCode = 0; Waited = 0 }
        $clean | Add-Member ScriptMethod WaitForExit { param([int]$Milliseconds); $this.Waited = $Milliseconds; return $true }
        Wait-PublisherForCleanExit -Publisher $clean -TimeoutMilliseconds 17
        $clean.Waited | Should Be 17

        $timedOut = [pscustomobject]@{ ExitCode = 0 }
        $timedOut | Add-Member ScriptMethod WaitForExit { param([int]$Milliseconds); return $false }
        $threw = $false
        try { Wait-PublisherForCleanExit -Publisher $timedOut -TimeoutMilliseconds 17 } catch { $threw = $true }
        $threw | Should Be $true

        $failed = [pscustomobject]@{ ExitCode = 22 }
        $failed | Add-Member ScriptMethod WaitForExit { param([int]$Milliseconds); return $true }
        $threw = $false
        try { Wait-PublisherForCleanExit -Publisher $failed -TimeoutMilliseconds 17 } catch { $threw = $true }
        $threw | Should Be $true
    }
}

Describe "AirPlay source-clock runner fixed-PCM one-format gate" {
    It "accepts only the expected one-kilohertz zero-crossing band" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-PrimaryAudioTone")))

        (Test-PrimaryAudioTone 0.041667) | Should Be $true
        (Test-PrimaryAudioTone 0.035) | Should Be $true
        (Test-PrimaryAudioTone 0.048) | Should Be $true
        (Test-PrimaryAudioTone 0.0349) | Should Be $false
        (Test-PrimaryAudioTone 0.0481) | Should Be $false
    }

    It "keeps mono generation and tone measurement opt-in" {
        $text = Get-Content -LiteralPath $runnerPath -Raw

        ($text.Contains('[ValidateSet(1, 2)]')) | Should Be $true
        ($text.Contains('[int]$PrimaryAudioChannels = 2')) | Should Be $true
        ($text.Contains('[switch]$RequirePrimaryAudioTone')) | Should Be $true
        ($text.Contains('-ac $PrimaryAudioChannels')) | Should Be $true
    }

    It "measures the primary tone only after clean recording finalization" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $publisherExit = $text.IndexOf('Wait-PublisherForCleanExit -Publisher $publisher')
        $recording = $text.IndexOf('$currentStage = "recording"')
        $tone = $text.IndexOf('$currentStage = "primary-audio-tone"')

        ($publisherExit -ge 0) | Should Be $true
        ($recording -gt $publisherExit) | Should Be $true
        ($tone -gt $recording) | Should Be $true
    }
}

Describe "AirPlay source-clock runner observation contract" {
    It "keeps an unmeasured observation null" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-SourceClockObservation")))

        $got = New-SourceClockObservation -State "NOT_MEASURED"
        $got.state | Should Be "NOT_MEASURED"
        $got.at | Should Be $null
        $got.details | Should Be $null
    }

    It "creates all seven observation fields as explicit NOT_MEASURED values" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-SourceClockObservation")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-SourceClockObservationSet")))

        $got = New-SourceClockObservationSet
        $got.Keys.Count | Should Be 7
        foreach ($name in @("firstInputIDR", "firstDecodedVideo", "firstRealEncodedVideo", "firstRTSPDecodedVideo", "firstAudioInput", "firstPCM", "firstAudioOutput")) {
            $got.Contains($name) | Should Be $true
            $got[$name].state | Should Be "NOT_MEASURED"
            $got[$name].at | Should Be $null
        }
    }

    It "imports the first native input decoded and encoded video stage events" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-SourceClockObservation")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-MeasuredSourceClockObservation")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-SourceClockObservationSet")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Import-SourceClockVideoStageObservations")))

        $eventLog = Join-Path $TestDrive "events.jsonl"
        @'
{"event":"publisher-ready","at":"2026-09-07T00:00:00.000Z"}
{"event":"video-input-idr","at":"2026-09-07T00:00:01.000Z","runningTimeNs":100,"sourceNtpNs":200}
{"event":"video-decoded","at":"2026-09-07T00:00:02.000Z","runningTimeNs":300}
{"event":"video-encoded","at":"2026-09-07T00:00:03.000Z","runningTimeNs":400}
{"event":"video-encoded","at":"2026-09-07T00:00:04.000Z","runningTimeNs":500}
'@ | Set-Content -LiteralPath $eventLog -Encoding utf8
        $observations = New-SourceClockObservationSet

        Import-SourceClockVideoStageObservations -EventLogPath $eventLog -Observations $observations

        $observations.firstInputIDR.state | Should Be "MEASURED"
        $observations.firstInputIDR.at.ToUniversalTime().ToString("o") | Should Match "^2026-09-07T00:00:01"
        $observations.firstInputIDR.details.runningTimeNs | Should Be 100
        $observations.firstInputIDR.details.sourceNtpNs | Should Be 200
        $observations.firstDecodedVideo.state | Should Be "MEASURED"
        $observations.firstDecodedVideo.details.runningTimeNs | Should Be 300
        $observations.firstRealEncodedVideo.state | Should Be "MEASURED"
        $observations.firstRealEncodedVideo.details.runningTimeNs | Should Be 400
        $observations.firstRTSPDecodedVideo.state | Should Be "NOT_MEASURED"
    }

    It "rejects an encoded stage timestamp outside the bounded running-time order" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Assert-SourceClockVideoStageTiming")))

        Assert-SourceClockVideoStageTiming -Observations ([ordered]@{
            firstInputIDR = [ordered]@{ state = "MEASURED"; details = [ordered]@{ runningTimeNs = 100000000 } }
            firstDecodedVideo = [ordered]@{ state = "MEASURED"; details = [ordered]@{ runningTimeNs = 300000000 } }
            firstRealEncodedVideo = [ordered]@{ state = "MEASURED"; details = [ordered]@{ runningTimeNs = 500000000 } }
        }) -MaximumStageSpanSeconds 10

        $threw = $false
        try {
            Assert-SourceClockVideoStageTiming -Observations ([ordered]@{
                firstInputIDR = [ordered]@{ state = "MEASURED"; details = [ordered]@{ runningTimeNs = 100000000 } }
                firstDecodedVideo = [ordered]@{ state = "MEASURED"; details = [ordered]@{ runningTimeNs = 300000000 } }
                firstRealEncodedVideo = [ordered]@{ state = "MEASURED"; details = [ordered]@{ runningTimeNs = 3600000033333333 } }
            }) -MaximumStageSpanSeconds 10
        } catch { $threw = $true }
        $threw | Should Be $true
    }

    It "recognizes the expected late-audio silence transition" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Parse-LateAudioSilenceTransition")))

        $got = Parse-LateAudioSilenceTransition @"
[Parsed_silencedetect_0] silence_start: 0
[Parsed_silencedetect_0] silence_end: 18.94975 | silence_duration: 18.94975
"@

        $got.valid | Should Be $true
        $got.silenceStartSeconds | Should Be 0
        $got.firstAudioOutputSeconds | Should Be 18.94975
    }

    It "rejects missing and implausibly early late-audio output" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Parse-LateAudioSilenceTransition")))

        (Parse-LateAudioSilenceTransition "[silencedetect] silence_start: 0").valid | Should Be $false
        (Parse-LateAudioSilenceTransition @"
[silencedetect] silence_start: 0
[silencedetect] silence_end: 4.0 | silence_duration: 4.0
"@).valid | Should Be $false
    }

    It "does not allow a MEASURED observation without a timestamp" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-SourceClockObservation")))
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "New-MeasuredSourceClockObservation")))

        $threw = $false
        try { New-MeasuredSourceClockObservation -At $null } catch { $threw = $true }
        $threw | Should Be $true
    }

    It "classifies bounded RTSP reader outcomes separately" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Get-RTSPReaderStatus")))

        (Get-RTSPReaderStatus -TimedOut $true -StopFailed $false -ExitCode 0 -Valid $false) | Should Be "TIMEOUT"
        (Get-RTSPReaderStatus -TimedOut $true -StopFailed $true -ExitCode 0 -Valid $false) | Should Be "STOP_FAILED"
        (Get-RTSPReaderStatus -TimedOut $false -StopFailed $false -ExitCode 22 -Valid $false) | Should Be "ERROR"
        (Get-RTSPReaderStatus -TimedOut $false -StopFailed $false -ExitCode 0 -Valid $false) | Should Be "NO_PROGRESS"
        (Get-RTSPReaderStatus -TimedOut $false -StopFailed $false -ExitCode 0 -Valid $true) | Should Be "PASS"
    }

    It "preserves an absolute DateTime deadline after Nullable parameter binding" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Resolve-RTSPReaderDeadline")))

        $absolute = [DateTime]::UtcNow.AddSeconds(7)
        $resolved = Resolve-RTSPReaderDeadline -Deadline $absolute -TimeoutSeconds 1

        $resolved.GetType().FullName | Should Be "System.DateTime"
        $resolved.Ticks | Should Be $absolute.Ticks
    }

    It "uses bounded waits before and after killing an owned process" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Stop-OwnedProcess")))

        $fake = [pscustomobject]@{ HasExited = $false; WaitValues = @(); Killed = $false }
        $fake | Add-Member ScriptMethod CloseMainWindow { return $false }
        $fake | Add-Member ScriptMethod WaitForExit {
            param([int]$Milliseconds)
            $this.WaitValues += $Milliseconds
            return $false
        }
        $fake | Add-Member ScriptMethod Kill { $this.Killed = $true }
        $stopped = Stop-OwnedProcess -Process $fake -GracefulTimeoutMilliseconds 3 -KillTimeoutMilliseconds 5

        $stopped | Should Be $false
        $fake.Killed | Should Be $true
        $fake.WaitValues.Count | Should Be 2
        $fake.WaitValues[0] | Should Be 3
        $fake.WaitValues[1] | Should Be 5
    }

    It "rejects a single decoded RTSP frame as progress" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Parse-RTSPFrameMd5")))

        $got = Parse-RTSPFrameMd5 "0, 0, 0, 1, 3110400, aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        $got.valid | Should Be $false
        $got.frameCount | Should Be 1
    }

    It "rejects repeated copies of one decoded RTSP frame" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Parse-RTSPFrameMd5")))

        $got = Parse-RTSPFrameMd5 @"
0, 0, 0, 1, 3110400, aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
0, 1, 1, 1, 3110400, aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
"@
        $got.valid | Should Be $false
        $got.frameCount | Should Be 2
        $got.patternValues.Count | Should Be 1
    }

    It "accepts multiple advancing decoded RTSP pattern values" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Parse-RTSPFrameMd5")))

        $got = Parse-RTSPFrameMd5 @"
#format: frame checksums
0, 0, 0, 1, 3110400, aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
0, 1, 1, 1, 3110400, bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
"@
        $got.valid | Should Be $true
        $got.frameCount | Should Be 2
        $got.patternValues.Count | Should Be 2
    }

    It "starts HLS and RTSP observation only after the fixture" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $fixtureStart = $text.IndexOf('$fixture = Start-OwnedProcess $fixtureBinary')
        $hlsPoll = $text.IndexOf('if (Test-HLSReady $hlsURL)')
        $reader = $text.IndexOf('$rtspFrameObservation = Invoke-BoundedRTSPVideoReader')
        $fixtureWait = $text.IndexOf('$fixture.WaitForExit()')

        ($fixtureStart -ge 0) | Should Be $true
        ($hlsPoll -gt $fixtureStart) | Should Be $true
        ($reader -gt $fixtureStart) | Should Be $true
        ($reader -lt $fixtureWait) | Should Be $true
    }

    It "bounds ffprobe and checks fixture survival after RTSP and HLS observations" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $fixtureStart = $text.IndexOf('$fixture = Start-OwnedProcess $fixtureBinary')
        $fixtureWait = $text.IndexOf('$fixture.WaitForExit()')
        $ffprobeStart = $text.IndexOf('$rtspProbeProcess = Start-OwnedProcess')
        $ffprobeWait = $text.IndexOf('$rtspProbeProcess.WaitForExit($observationTimeoutSeconds * 1000)')
        $ffprobeSurvival = $text.IndexOf('fixture exited during ffprobe observation')
        $hlsPoll = $text.IndexOf('if (Test-HLSReady $hlsURL)')
        $hlsSurvival = $text.IndexOf('fixture exited during HLS observation')

        foreach ($position in @($ffprobeStart, $ffprobeWait, $ffprobeSurvival, $hlsPoll, $hlsSurvival)) {
            ($position -gt $fixtureStart) | Should Be $true
            ($position -lt $fixtureWait) | Should Be $true
        }
    }

    It "preserves the TimeSpan input and Go-compatible duration value" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        ($text -match '\[TimeSpan\]\$Duration') | Should Be $true
        ($text -match '\$Duration\.TotalMilliseconds') | Should Be $true
        ($text -match '"\{0\}ms"') | Should Be $true
    }

    It "uses the schema-2 publisher identity and fixed recording sidecars" {
        $text = Get-Content -LiteralPath $runnerPath -Raw

        foreach ($argument in @("--session-id", "--publisher-generation", "--event-log")) {
            ($text.Contains($argument)) | Should Be $true
        }
        ($text.Contains('$ready = $recording + ".publisher-ready"')) | Should Be $true
        ($text.Contains('$mediaReadyFile = $recording + ".media-ready"')) | Should Be $true
        ($text.Contains('$eventLog = $recording + ".events.jsonl"')) | Should Be $true
        ($text.Contains('$readyJson.pipelineStartAccepted')) | Should Be $true
    }

    It "uses one bounded RTSP frame observation after API readiness" {
        $text = Get-Content -LiteralPath $runnerPath -Raw

        ($text.Contains('$observationDeadline = (Get-Date).AddSeconds($observationTimeoutSeconds)')) | Should Be $true
        ($text.Contains('-Deadline $observationDeadline')) | Should Be $true
        [regex]::Matches($text, '(?m)^\s*\$rtspFrameObservation = Invoke-BoundedRTSPVideoReader').Count | Should Be 1
    }
}

Describe "AirPlay source-clock API-gated RTSP observation" {
    It "declares a zero-default nonnegative pre-reader delay" {
        $text = Get-Content -LiteralPath $runnerPath -Raw

        ($text -match '\[ValidateRange\(0,\s*\[int\]::MaxValue\)\]\s*\[int\]\$RTSPReaderStartDelaySeconds\s*=\s*0') | Should Be $true
    }

    It "treats only an online HTTP 200 path response as API-ready" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Test-MediaMTXPathOnline")))

        $notFound = Test-MediaMTXPathOnline -Uri "http://127.0.0.1:19997/v3/paths/get/source_clock_test" -Request {
            [pscustomobject]@{ StatusCode = 404; Content = '{"ready":false}' }
        }
        $notReady = Test-MediaMTXPathOnline -Uri "http://127.0.0.1:19997/v3/paths/get/source_clock_test" -Request {
            [pscustomobject]@{ StatusCode = 200; Content = '{"ready":false}' }
        }
        $ready = Test-MediaMTXPathOnline -Uri "http://127.0.0.1:19997/v3/paths/get/source_clock_test" -Request {
            [pscustomobject]@{ StatusCode = 200; Content = '{"ready":true}' }
        }
        $readyBytes = Test-MediaMTXPathOnline -Uri "http://127.0.0.1:19997/v3/paths/get/source_clock_test" -Request {
            [pscustomobject]@{
                StatusCode = 200
                Content = [Text.Encoding]::UTF8.GetBytes('{"ready":true}')
            }
        }
        $nestedReady = Test-MediaMTXPathOnline -Uri "http://127.0.0.1:19997/v3/paths/get/source_clock_test" -Request {
            [pscustomobject]@{ StatusCode = 200; Content = '{"item":{"ready" : true}}' }
        }

        $notFound | Should Be $false
        $notReady | Should Be $false
        $ready | Should Be $true
        $readyBytes | Should Be $true
        $nestedReady | Should Be $true
    }

    It "polls the API until ready while checking publisher survival" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Wait-MediaMTXPathReady")))

        $publisher = [pscustomobject]@{ HasExited = $false }
        $state = @{ Calls = 0 }
        $ready = Wait-MediaMTXPathReady -Uri "http://127.0.0.1:19997/v3/paths/get/source_clock_test" `
            -Deadline (Get-Date).AddSeconds(1) -Publisher $publisher -PollMilliseconds 0 -PathReadyTest {
                $state.Calls++
                return $state.Calls -ge 2
            }

        $ready | Should Be $true
        $state.Calls | Should Be 2
    }

    It "fails distinctly when the publisher exits before any RTSP reader" {
        . ([scriptblock]::Create((Get-RunnerFunctionDefinition "Wait-MediaMTXPathReady")))

        $publisher = [pscustomobject]@{ HasExited = $true }
        $calls = 0
        $message = $null
        try {
            Wait-MediaMTXPathReady -Uri "http://127.0.0.1:19997/v3/paths/get/source_clock_test" `
                -Deadline (Get-Date).AddSeconds(1) -Publisher $publisher -PathReadyTest {
                    $script:calls++
                    return $false
                } | Out-Null
        } catch {
            $message = $_.Exception.Message
        }

        $message | Should Be "publisher exited before any RTSP reader was started while waiting for MediaMTX path readiness"
        $calls | Should Be 0
    }

    It "uses one observation deadline for API readiness, delay, and the bounded reader" {
        $text = Get-Content -LiteralPath $runnerPath -Raw

        ($text.Contains('$observationDeadline = (Get-Date).AddSeconds($observationTimeoutSeconds)')) | Should Be $true
        ($text.Contains('$readerStartAt = (Get-Date).AddSeconds($RTSPReaderStartDelaySeconds)')) | Should Be $true
        ($text.Contains('-Deadline $observationDeadline')) | Should Be $true
        ($text -match 'while \([^\r\n]*\$readerStartAt[^\r\n]*\$observationDeadline') | Should Be $true
        ($text.Contains('throw "RTSP reader start delay consumed the observation deadline"')) | Should Be $true
    }

    It "polls MediaMTX API before the sole RTSP reader invocation" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $apiGate = $text.IndexOf('$mediaPathReady = Wait-MediaMTXPathReady')
        $reader = $text.IndexOf('$rtspFrameObservation = Invoke-BoundedRTSPVideoReader')

        ($apiGate -ge 0) | Should Be $true
        ($reader -gt $apiGate) | Should Be $true
    }

    It "grants the isolated MediaMTX config API observation permission" {
        $text = Get-Content -LiteralPath $runnerPath -Raw

        ($text.Contains("- action: api")) | Should Be $true
        ($text.IndexOf("- action: api") -lt $text.IndexOf('$mediaPathAPIURL =')) | Should Be $true
    }

    It "gives the isolated MediaMTX UDP ingress a 64 MiB read buffer" {
        $text = Get-Content -LiteralPath $runnerPath -Raw

        ($text.Contains("udpReadBufferSize: 67108864")) | Should Be $true
    }

    It "has no RTSP reader retry loop after the API gate" {
        $text = Get-Content -LiteralPath $runnerPath -Raw
        $readerCalls = [regex]::Matches($text, '(?m)^\s*\$rtspFrameObservation = Invoke-BoundedRTSPVideoReader').Count
        $apiGate = $text.IndexOf('$mediaPathReady = Wait-MediaMTXPathReady')
        $reader = $text.IndexOf('$rtspFrameObservation = Invoke-BoundedRTSPVideoReader', $apiGate)
        $observationEnd = $text.IndexOf('$observationEvidence.rtspFrameCount', $reader)
        $observationBlock = $text.Substring($reader, $observationEnd - $reader)

        $readerCalls | Should Be 1
        ($observationBlock -notmatch '\bdo\s*\{|\bwhile\s*\(') | Should Be $true
        ($observationBlock -notmatch '404') | Should Be $true
    }
}
