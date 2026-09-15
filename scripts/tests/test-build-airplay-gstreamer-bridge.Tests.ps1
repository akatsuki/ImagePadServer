$scriptPath = (Resolve-Path (Join-Path $PSScriptRoot "..\build-airplay-gstreamer-bridge.ps1")).Path

function Get-BuildFunctionDefinition([string]$Name) {
    $tokens = $null
    $errors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$errors)
    if ($errors.Count -ne 0) {
        throw "build script parse failed: $($errors[0].Message)"
    }
    $definition = $ast.Find({
        param($node)
        $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $Name
    }, $true)
    if ($null -eq $definition) {
        throw "function was not found: $Name"
    }
    return $definition.Extent.Text
}

Describe "AirPlay GStreamer candidate build switch" {
    BeforeAll {
        . ([scriptblock]::Create((Get-BuildFunctionDefinition "Get-FixedPcmUDPCandidateCMakeArguments")))
        . ([scriptblock]::Create((Get-BuildFunctionDefinition "Get-AirPlayBridgeCTestArguments")))
        . ([scriptblock]::Create((Get-BuildFunctionDefinition "Invoke-CheckedNativeCommand")))
        . ([scriptblock]::Create((Get-BuildFunctionDefinition "Clear-AirPlayBridgeBuildProvenance")))
        . ([scriptblock]::Create((Get-BuildFunctionDefinition "Write-AirPlayBridgeBuildProvenance")))
    }

    It "keeps both staged options explicitly OFF by default" {
        $args = @(Get-FixedPcmUDPCandidateCMakeArguments -Enabled $false)

        $args | Should Be @(
            "-DIMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO=OFF",
            "-DIMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP=OFF"
        )
    }

    It "enables both staged options together for the accepted candidate" {
        $args = @(Get-FixedPcmUDPCandidateCMakeArguments -Enabled $true)

        $args | Should Be @(
            "-DIMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO=ON",
            "-DIMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP=ON"
        )
    }

    It "feeds the combined arguments into CMake" {
        $text = Get-Content -LiteralPath $scriptPath -Raw

        ($text.Contains('[switch]$FixedPcmUDPCandidate')) | Should Be $true
        ($text.Contains('$candidateArguments = Get-FixedPcmUDPCandidateCMakeArguments')) | Should Be $true
        ($text.Contains('@candidateArguments')) | Should Be $true
    }

    It "excludes only the known dynamic-audio diagnostic from the default build gate" {
        $args = @(Get-AirPlayBridgeCTestArguments -BuildDirectory "build-dir" -IncludeDiagnostics $false)

        ($args -contains "-E") | Should Be $true
        ($args -contains "^airplay_source_clock_audio_bin$") | Should Be $true
        ($args -contains "--output-on-failure") | Should Be $true
    }

    It "runs the complete CTest inventory only when diagnostics are requested" {
        $args = @(Get-AirPlayBridgeCTestArguments -BuildDirectory "build-dir" -IncludeDiagnostics $true)

        ($args -contains "-E") | Should Be $false
        ($args -contains "^airplay_source_clock_audio_bin$") | Should Be $false
    }

    It "fails the build script when the selected CTest gate fails" {
        $text = Get-Content -LiteralPath $scriptPath -Raw

        ($text.Contains('[switch]$IncludeDiagnosticTests')) | Should Be $true
        ($text.Contains('Invoke-CheckedNativeCommand -Stage "CTest gate"')) | Should Be $true
    }

    It "stops immediately when a native build stage fails" {
        $events = [Collections.Generic.List[string]]::new()
        $message = $null

        try {
            Invoke-CheckedNativeCommand -Stage "configure" -Command {
                $events.Add("configure")
                & cmd.exe /d /c exit 17
            }
            $events.Add("build")
        } catch {
            $message = $_.Exception.Message
        }

        @($events) | Should Be @("configure")
        $message | Should Match "configure.*17"
    }

    It "checks configure and build before running the next stage" {
        $text = Get-Content -LiteralPath $scriptPath -Raw
        $configure = $text.IndexOf('Invoke-CheckedNativeCommand -Stage "CMake configure"')
        $build = $text.IndexOf('Invoke-CheckedNativeCommand -Stage "CMake build"')
        $ctest = $text.IndexOf('Invoke-CheckedNativeCommand -Stage "CTest gate"')
        $provenance = $text.LastIndexOf('Write-AirPlayBridgeBuildProvenance')

        ($configure -ge 0) | Should Be $true
        ($configure -lt $build) | Should Be $true
        ($build -lt $ctest) | Should Be $true
        ($ctest -lt $provenance) | Should Be $true
    }

    It "writes a Release provenance record bound to the executable hash and candidate flags" {
        $root = Join-Path $TestDrive "provenance"
        New-Item -ItemType Directory -Force -Path $root | Out-Null
        $bridge = Join-Path $root "airplay-gstreamer-bridge.exe"
        [IO.File]::WriteAllBytes($bridge, [byte[]](1, 2, 3, 4, 5))
        $recordPath = Join-Path $root "imagepad-airplay-gstreamer-bridge-build.json"

        Write-AirPlayBridgeBuildProvenance `
            -BridgePath $bridge `
            -ProvenancePath $recordPath `
            -FixedPcmUDPCandidate $true

        $record = Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json
        $record.schema | Should Be 1
        $record.configuration | Should Be "Release"
        $record.fixedPcmAudio | Should Be $true
        $record.rtspPublishTransport | Should Be "udp"
        $record.sourceClockProtocolVersion | Should Be 1
        $record.videoContract.id | Should Be "rtsp-h264-single-slice-v1"
        $record.videoContract.slicesPerFrame | Should Be 1
        $record.videoContract.testPassed | Should Be $true
        $record.videoContract.testName | Should Be "airplay_source_clock_single_slice"
        (@($record.audioProfiles.accepted) -contains "aac-lc-adts/44100/mono/1024") | Should Be $true
        (@($record.audioProfiles.accepted) -contains "aac-eld-raw/44100/stereo/480") | Should Be $true
        (@($record.audioProfiles.rejected) -contains "aac-lc-adts/44100/stereo/1024") | Should Be $true
        $record.executable | Should Be "airplay-gstreamer-bridge.exe"
        $record.executableSha256 | Should Be ((Get-FileHash -LiteralPath $bridge -Algorithm SHA256).Hash.ToLowerInvariant())
    }

    It "invalidates an earlier successful provenance before a failing stage" {
        $recordPath = Join-Path $TestDrive "stale-provenance.json"
        Set-Content -LiteralPath $recordPath -Value '{"schema":1}' -Encoding UTF8
        $message = $null

        Clear-AirPlayBridgeBuildProvenance -ProvenancePath $recordPath
        try {
            Invoke-CheckedNativeCommand -Stage "configure" -Command {
                & cmd.exe /d /c exit 19
            }
        } catch {
            $message = $_.Exception.Message
        }

        (Test-Path -LiteralPath $recordPath) | Should Be $false
        $message | Should Match "configure.*19"
    }

    It "clears provenance before configure begins" {
        $text = Get-Content -LiteralPath $scriptPath -Raw
        $clear = $text.LastIndexOf('Clear-AirPlayBridgeBuildProvenance')
        $configure = $text.IndexOf('Invoke-CheckedNativeCommand -Stage "CMake configure"')

        ($clear -ge 0) | Should Be $true
        ($clear -lt $configure) | Should Be $true
    }
}

Describe "Mandatory single-slice test inventory" {
    It "rejects a build with the regression test removed" {
        . ([scriptblock]::Create((Get-BuildFunctionDefinition "Assert-AirPlayBridgeCTestInventory")))
        { Assert-AirPlayBridgeCTestInventory '{"tests":[{"name":"unrelated"}]}' } | Should Throw
        { Assert-AirPlayBridgeCTestInventory '{"tests":[]}' } | Should Throw
        { Assert-AirPlayBridgeCTestInventory '{"tests":[{"name":"airplay_source_clock_single_slice"}]}' } | Should Not Throw
    }
}
