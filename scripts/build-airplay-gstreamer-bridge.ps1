param(
    [string]$GStreamerRoot = $env:GSTREAMER_1_0_ROOT_MSVC_X86_64,
    [string]$GStreamerRuntimeRoot = $env:GSTREAMER_1_0_RUNTIME_ROOT_MSVC_X86_64,
    [string]$BuildDirectory = "",
    [switch]$FixedPcmUDPCandidate,
    [switch]$IncludeDiagnosticTests
)

$ErrorActionPreference = "Stop"

function Get-FixedPcmUDPCandidateCMakeArguments([bool]$Enabled) {
    $value = if ($Enabled) { "ON" } else { "OFF" }
    return @(
        "-DIMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO=$value"
        "-DIMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP=$value"
    )
}

function Get-AirPlayBridgeCTestArguments(
    [string]$BuildDirectory,
    [bool]$IncludeDiagnostics
) {
    $arguments = @(
        "--test-dir", $BuildDirectory,
        "-C", "Release",
        "--output-on-failure"
    )
    if (-not $IncludeDiagnostics) {
        $arguments += @("-E", "^airplay_source_clock_audio_bin$")
    }
    return $arguments
}

function Invoke-CheckedNativeCommand(
    [string]$Stage,
    [scriptblock]$Command
) {
    & $Command
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "$Stage failed with exit code $exitCode"
    }
}

function Assert-AirPlayBridgeCTestInventory([string]$InventoryJson) {
    $inventory = $InventoryJson | ConvertFrom-Json
    if (@($inventory.tests | Where-Object { $_.name -eq 'airplay_source_clock_single_slice' }).Count -ne 1) {
        throw 'Mandatory H.264 single-slice regression test is missing from CTest'
    }
}

function Clear-AirPlayBridgeBuildProvenance([string]$ProvenancePath) {
    if (Test-Path -LiteralPath $ProvenancePath -PathType Leaf) {
        Remove-Item -LiteralPath $ProvenancePath -Force
    }
}

function Get-GStreamerVersion([string]$RuntimeRoot) {
    $inspect = Join-Path $RuntimeRoot "bin\gst-inspect-1.0.exe"
    if (-not (Test-Path -LiteralPath $inspect -PathType Leaf)) {
        return "unknown"
    }
    $lines = @(& $inspect --version 2>$null)
    if ($LASTEXITCODE -ne 0) {
        return "unknown"
    }
    foreach ($line in $lines) {
        if ($line -match 'GStreamer\s+([0-9]+\.[0-9]+\.[0-9]+)') {
            return $Matches[1]
        }
    }
    return "unknown"
}

function Write-AirPlayBridgeBuildProvenance(
    [string]$BridgePath,
    [string]$ProvenancePath,
    [bool]$FixedPcmUDPCandidate,
    [string]$GStreamerVersion = "unknown"
) {
    if (-not (Test-Path -LiteralPath $BridgePath -PathType Leaf)) {
        throw "AirPlay GStreamer bridge executable was not produced: $BridgePath"
    }
    $record = [ordered]@{
        schema = 1
        configuration = "Release"
        sourceClockProtocolVersion = 1
        fixedPcmAudio = $FixedPcmUDPCandidate
        rtspPublishTransport = if ($FixedPcmUDPCandidate) { "udp" } else { "tcp" }
        gstreamerVersion = $GStreamerVersion
        videoContract = [ordered]@{
            id = "rtsp-h264-single-slice-v1"
            slicesPerFrame = 1
            testName = "airplay_source_clock_single_slice"
            testPassed = $true
        }
        buildFlags = [ordered]@{
            IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO = $FixedPcmUDPCandidate
            IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP = $FixedPcmUDPCandidate
        }
        audioProfiles = [ordered]@{
            accepted = @(
                "aac-lc-adts/44100/mono/1024",
                "aac-lc-adts/48000/mono/1024",
                "aac-eld-raw/44100/stereo/480"
            )
            rejected = @(
                "aac-lc-adts/44100/stereo/1024",
                "aac-lc-adts/48000/stereo/1024",
                "aac-lc-adts-with-codec-data/any/any/any"
            )
        }
        executable = Split-Path -Leaf $BridgePath
        executableSha256 = (Get-FileHash -LiteralPath $BridgePath -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $record | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath $ProvenancePath -Encoding UTF8
}

$repoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($GStreamerRoot)) {
    $knownRoot = Join-Path $env:LOCALAPPDATA "Programs\gstreamer\1.0\msvc_x86_64"
    if (Test-Path (Join-Path $knownRoot "include\gstreamer-1.0\gst\gst.h")) {
        $GStreamerRoot = $knownRoot
    } else {
        throw "GStreamer MSVC x64 development root is required. Set GSTREAMER_1_0_ROOT_MSVC_X86_64 or pass -GStreamerRoot."
    }
}
if ([string]::IsNullOrWhiteSpace($GStreamerRuntimeRoot)) {
    $GStreamerRuntimeRoot = $GStreamerRoot
}
if ([string]::IsNullOrWhiteSpace($BuildDirectory)) {
    $BuildDirectory = Join-Path $repoRoot "build\airplay-gstreamer-bridge"
}
$candidateArguments = Get-FixedPcmUDPCandidateCMakeArguments -Enabled $FixedPcmUDPCandidate.IsPresent
$bridgePath = Join-Path $BuildDirectory "Release\airplay-gstreamer-bridge.exe"
$provenancePath = Join-Path $BuildDirectory "Release\imagepad-airplay-gstreamer-bridge-build.json"
Clear-AirPlayBridgeBuildProvenance -ProvenancePath $provenancePath

Invoke-CheckedNativeCommand -Stage "CMake configure" -Command {
    cmake -S (Join-Path $repoRoot "native\airplay-gstreamer-bridge") `
        -B $BuildDirectory `
        "-DGSTREAMER_ROOT=$GStreamerRoot" `
        "-DGSTREAMER_RUNTIME_ROOT=$GStreamerRuntimeRoot" `
        -DBUILD_TESTING=ON `
        -DCMAKE_BUILD_TYPE=Release `
        @candidateArguments
}
Invoke-CheckedNativeCommand -Stage "CMake build" -Command {
    cmake --build $BuildDirectory --config Release --parallel
}
$ctestArguments = Get-AirPlayBridgeCTestArguments `
    -BuildDirectory $BuildDirectory `
    -IncludeDiagnostics $IncludeDiagnosticTests.IsPresent
$inventory = Invoke-CheckedNativeCommand -Stage "CTest inventory" -Command {
    & ctest --test-dir $BuildDirectory -C Release --show-only=json-v1
}
Assert-AirPlayBridgeCTestInventory -InventoryJson ($inventory -join "`n")
Invoke-CheckedNativeCommand -Stage "CTest gate" -Command {
    & ctest @ctestArguments
}
Write-AirPlayBridgeBuildProvenance `
    -BridgePath $bridgePath `
    -ProvenancePath $provenancePath `
    -FixedPcmUDPCandidate $FixedPcmUDPCandidate.IsPresent `
    -GStreamerVersion (Get-GStreamerVersion $GStreamerRuntimeRoot)
