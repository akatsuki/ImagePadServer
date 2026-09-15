[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ReceiverBuildDirectory,
    [Parameter(Mandatory = $true)]
    [string]$BridgeBuildDirectory,
    [Parameter(Mandatory = $true)]
    [string]$GStreamerRuntimeRoot,
    [string]$MinGWRuntimeRoot = "",
    [string]$CompilerRuntimeRoot = "",
    [string]$LibplistRuntimePath = "",
    [Parameter(Mandatory = $true)]
    [string]$OutputDirectory,
    [string]$Version = "source-clock-dev",
    [switch]$RequireFixedPcmUDPCandidate
)

$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot 'airplay-h264-contract.ps1')

function Assert-FixedPcmUDPCandidateBuild([string]$BridgeRoot) {
    $bridgePath = Join-Path $BridgeRoot "airplay-gstreamer-bridge.exe"
    $provenancePath = Join-Path $BridgeRoot "imagepad-airplay-gstreamer-bridge-build.json"
    if (-not (Test-Path -LiteralPath $bridgePath -PathType Leaf)) {
        throw "fixed PCM UDP candidate bridge executable is missing: $bridgePath"
    }
    if (-not (Test-Path -LiteralPath $provenancePath -PathType Leaf)) {
        throw "fixed PCM UDP candidate requires executable-bound build provenance: $provenancePath"
    }
    try {
        $provenance = Get-Content -LiteralPath $provenancePath -Raw | ConvertFrom-Json
    } catch {
        throw "fixed PCM UDP candidate build provenance is invalid JSON: $($_.Exception.Message)"
    }
    if ($provenance.schema -ne 1) {
        throw "fixed PCM UDP candidate build provenance schema is unsupported"
    }
    if ($provenance.configuration -ne "Release") {
        throw "fixed PCM UDP candidate requires Release configuration provenance"
    }
    if ($provenance.fixedPcmAudio -ne $true) {
        throw "fixed PCM UDP candidate requires fixedPcmAudio=true provenance"
    }
    if ($provenance.rtspPublishTransport -ne "udp") {
        throw "fixed PCM UDP candidate requires rtspPublishTransport=udp provenance"
    }
    if ($provenance.executable -ne (Split-Path -Leaf $bridgePath)) {
        throw "fixed PCM UDP candidate build provenance names a different executable"
    }
    if ($provenance.executableSha256 -notmatch '^[0-9a-fA-F]{64}$') {
        throw "fixed PCM UDP candidate build provenance has an invalid executable hash"
    }
    $actualHash = (Get-FileHash -LiteralPath $bridgePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($provenance.executableSha256.ToLowerInvariant() -ne $actualHash) {
        throw "fixed PCM UDP candidate executable hash does not match build provenance"
    }
    return [pscustomobject]@{
        fixedPcmAudio = $true
        rtspPublishTransport = "udp"
        executableSha256 = $actualHash
        provenancePath = $provenancePath
    }
}

function Assert-PackagedFixedPcmUDPCandidate(
    [string]$PackagedBridgeRoot,
    [string]$ExpectedExecutableSha256
) {
    try {
        $packaged = Assert-FixedPcmUDPCandidateBuild -BridgeRoot $PackagedBridgeRoot
    } catch {
        throw "packaged fixed PCM UDP candidate validation failed: $($_.Exception.Message)"
    }
    if ($packaged.executableSha256 -ne $ExpectedExecutableSha256.ToLowerInvariant()) {
        throw "packaged fixed PCM UDP candidate hash differs from the validated source executable"
    }
    return $packaged
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$receiverRoot = (Resolve-Path -LiteralPath $ReceiverBuildDirectory).Path
$bridgeRoot = (Resolve-Path -LiteralPath $BridgeBuildDirectory).Path
$runtimeRoot = (Resolve-Path -LiteralPath $GStreamerRuntimeRoot).Path
$outputRoot = [IO.Path]::GetFullPath($OutputDirectory)
Assert-AirPlayH264BuildContract -BridgeRoot $bridgeRoot | Out-Null
$bridgeProfile = "unverified"
if ($RequireFixedPcmUDPCandidate.IsPresent) {
    $candidateBuild = Assert-FixedPcmUDPCandidateBuild -BridgeRoot $bridgeRoot
    $bridgeProfile = "fixed-pcm-udp"
}
$receiver = Join-Path $receiverRoot "uxplay-source-clock.exe"
$receiverManifestPath = Join-Path $receiverRoot "imagepad-source-clock-capabilities.json"
$probe = Join-Path $bridgeRoot "airplay-gstreamer-capability-probe.exe"
$sourceMetadata = Join-Path $repoRoot "third_party\uxplay-windows\source.json"
$sourceReadme = Join-Path $repoRoot "third_party\uxplay-windows\README.md"
$sourcePatches = @(
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0001-imagepad-source-clock-egress.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0002-imagepad-h265-payload-bounds.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0003-imagepad-plist-response-ownership.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0004-imagepad-libplist-value-ownership.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0005-imagepad-fragment-safe-rtsp-protocol.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0006-imagepad-source-clock-writer-wait.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0007-imagepad-source-clock-headless-fps.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0008-imagepad-source-clock-video-backlog.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0009-imagepad-source-clock-audio-format-lock.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0010-imagepad-source-clock-egress-metrics.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0011-imagepad-source-clock-metrics-emitter.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0012-imagepad-source-clock-video-bootstrap-cache.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0013-imagepad-source-clock-video-bootstrap-reconnect.patch"
    Join-Path $repoRoot "third_party\uxplay-windows\patches\0014-imagepad-source-clock-video-loss-recovery.patch"
)
$licenseManifestPath = Join-Path $outputRoot "license-manifest.json"
$sourceOfferPath = Join-Path $outputRoot "SOURCE-OFFER.md"
$runtimeDescriptorPath = Join-Path $outputRoot "imagepad-airplay-runtime.json"
$licensesReadmePath = Join-Path $outputRoot "licenses\README.md"

function Get-Sha256Hex([string]$Path) {
    $stream = [IO.File]::OpenRead($Path)
    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        $hash = $sha256.ComputeHash($stream)
        return (($hash | ForEach-Object { $_.ToString("x2") }) -join "")
    } finally {
        $sha256.Dispose()
        $stream.Dispose()
    }
}

if ($MinGWRuntimeRoot) {
    if (-not $CompilerRuntimeRoot) {
        $CompilerRuntimeRoot = $MinGWRuntimeRoot
    }
    if (-not $LibplistRuntimePath) {
        $LibplistRuntimePath = Join-Path $MinGWRuntimeRoot "libplist-2.0.dll"
    }
}
if (-not $CompilerRuntimeRoot) {
    throw "compiler runtime directory must be passed explicitly"
}
if (-not $LibplistRuntimePath) {
    throw "libplist runtime path must be passed explicitly"
}
$CompilerRuntimeRoot = (Resolve-Path -LiteralPath $CompilerRuntimeRoot).Path
$LibplistRuntimePath = (Resolve-Path -LiteralPath $LibplistRuntimePath).Path
$compilerRuntimeNames = @(
    "libstdc++-6.dll",
    "libgcc_s_seh-1.dll",
    "libwinpthread-1.dll"
)

function Normalize-DirectoryPath([string]$Path) {
    return ([IO.Path]::GetFullPath($Path)).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
}

function Test-SameOrUnder([string]$Candidate, [string]$Parent) {
    $candidateFull = Normalize-DirectoryPath $Candidate
    $parentFull = Normalize-DirectoryPath $Parent
    return $candidateFull.Equals($parentFull, [StringComparison]::OrdinalIgnoreCase) -or
        $candidateFull.StartsWith($parentFull + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)
}

$repoRootFull = Normalize-DirectoryPath $repoRoot
$outputRoot = Normalize-DirectoryPath $outputRoot
$userProfileRoot = Normalize-DirectoryPath ([Environment]::GetFolderPath("UserProfile"))
$installedReceiverRoot = Normalize-DirectoryPath (Join-Path $env:APPDATA "ImagePadServer\modules\uxplay\2.0.0.1736")
if ($outputRoot.Equals($repoRootFull, [StringComparison]::OrdinalIgnoreCase)) {
    throw "source-clock package output must not be the repository root"
}
if ($outputRoot.Equals($userProfileRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw "source-clock package output must not be the user profile root"
}
if (Test-SameOrUnder $outputRoot $installedReceiverRoot) {
    throw "source-clock package output must not overwrite the installed UxPlay module"
}
if (Test-SameOrUnder $outputRoot $runtimeRoot) {
    throw "source-clock package output must not overwrite the GStreamer development/runtime root"
}
if (Test-SameOrUnder $outputRoot $CompilerRuntimeRoot) {
    throw "source-clock package output must not overwrite the compiler runtime root"
}

foreach ($path in @($receiver, $receiverManifestPath, $probe, $sourceMetadata, $sourceReadme) + $sourcePatches) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "source-clock package input is missing: $path"
    }
}
foreach ($path in @((Join-Path $runtimeRoot "bin"), (Join-Path $runtimeRoot "lib\gstreamer-1.0"))) {
    if (-not (Test-Path -LiteralPath $path -PathType Container)) {
        throw "GStreamer runtime directory is missing: $path"
    }
}
foreach ($name in $compilerRuntimeNames) {
    $path = Join-Path $CompilerRuntimeRoot $name
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "UxPlay receiver runtime dependency is missing: $path"
    }
}
if (-not (Test-Path -LiteralPath $LibplistRuntimePath -PathType Leaf)) {
    throw "UxPlay receiver libplist dependency is missing: $LibplistRuntimePath"
}
if (Test-Path -LiteralPath $outputRoot -PathType Leaf) {
    throw "source-clock package output must be a directory: $outputRoot"
}
if ((Test-Path -LiteralPath $outputRoot -PathType Container) -and
    @(Get-ChildItem -LiteralPath $outputRoot -Force).Count -gt 0) {
    throw "source-clock package output must be an empty staging directory: $outputRoot"
}

$capabilities = Get-Content -LiteralPath $receiverManifestPath -Raw | ConvertFrom-Json
if ($capabilities.schema -ne 1 -or $capabilities.protocolVersion -ne 1 -or
    $capabilities.binary -ne (Split-Path -Leaf $receiver)) {
    throw "source-clock receiver capability manifest is incompatible"
}
$expectedHash = Get-Sha256Hex $receiver
if ($capabilities.binarySha256.ToLowerInvariant() -ne $expectedHash) {
    throw "source-clock receiver capability hash does not match the binary"
}
$features = @($capabilities.features)
foreach ($feature in @("video-au", "audio-frame", "remote-ntp", "bounded-writer", "idle-wait", "headless-fps", "video-backlog-64", "audio-format-lock", "egress-metrics-v2", "plist-owned-free", "fragment-safe-rtsp")) {
    if ($features -notcontains $feature) {
        throw "source-clock receiver capability is missing: $feature"
    }
}

$oldPath = $env:PATH
$oldPluginPath = $env:GST_PLUGIN_PATH
try {
    $env:PATH = (Join-Path $runtimeRoot "bin") + ";" + $oldPath
    $env:GST_PLUGIN_PATH = Join-Path $runtimeRoot "lib\gstreamer-1.0"
    $probeOutput = (& $probe | Out-String).Trim()
    $probeExitCode = $LASTEXITCODE
    if ($probeExitCode -ne 0) {
        throw "GStreamer capability probe failed with exit code $probeExitCode"
    }
    try {
        $probeResult = $probeOutput | ConvertFrom-Json
    } catch {
        throw "GStreamer capability probe returned invalid JSON: $($_.Exception.Message)"
    }
    if ($probeResult.schema -ne 1 -or $probeResult.gstreamer -ne $true -or
        $probeResult.sourceClock.videoBuffer -ne $true -or $probeResult.sourceClock.videoEOS -ne $true -or
        $probeResult.sourceClock.audioBuffer -ne $true -or $probeResult.sourceClock.audioEOS -ne $true) {
        throw "GStreamer capability probe did not prove the source-clock media contract"
    }
}
finally {
    $env:PATH = $oldPath
    $env:GST_PLUGIN_PATH = $oldPluginPath
}

New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
$receiverOutput = Join-Path $outputRoot "uxplay-source-clock"
New-Item -ItemType Directory -Force -Path $receiverOutput | Out-Null
Copy-Item -LiteralPath $receiver -Destination $receiverOutput -Force
Copy-Item -LiteralPath $receiverManifestPath -Destination $receiverOutput -Force
Copy-Item -LiteralPath $sourceMetadata -Destination $receiverOutput -Force
Copy-Item -LiteralPath $sourceReadme -Destination $receiverOutput -Force
foreach ($name in $compilerRuntimeNames) {
    Copy-Item -LiteralPath (Join-Path $CompilerRuntimeRoot $name) -Destination $receiverOutput -Force
}
Copy-Item -LiteralPath $LibplistRuntimePath -Destination $receiverOutput -Force
New-Item -ItemType Directory -Force -Path (Join-Path $receiverOutput "patches") | Out-Null
foreach ($sourcePatch in $sourcePatches) {
    Copy-Item -LiteralPath $sourcePatch -Destination (Join-Path $receiverOutput "patches") -Force
}

& (Join-Path $repoRoot "scripts\package-airplay-gstreamer-runtime.ps1") `
    -GStreamerRuntimeRoot $runtimeRoot `
    -BridgeBuildDirectory $bridgeRoot `
    -OutputDirectory (Join-Path $outputRoot "gstreamer") `
    -Version $Version
if ($LASTEXITCODE -ne 0) {
    throw "GStreamer runtime packaging failed with exit code $LASTEXITCODE"
}
if ($RequireFixedPcmUDPCandidate.IsPresent) {
    Copy-Item -LiteralPath $candidateBuild.provenancePath `
        -Destination (Join-Path $outputRoot "gstreamer\imagepad-airplay-gstreamer-bridge-build.json") `
        -Force
    Assert-PackagedFixedPcmUDPCandidate `
        -PackagedBridgeRoot (Join-Path $outputRoot "gstreamer") `
        -ExpectedExecutableSha256 $candidateBuild.executableSha256 | Out-Null
}

$controlSmokeEvidence = Join-Path $env:TEMP ("imagepad-package-control-smoke-" + [Guid]::NewGuid().ToString("N"))
& (Join-Path $repoRoot "scripts\test-uxplay-airplay-control.ps1") `
    -ReceiverPath (Join-Path $receiverOutput "uxplay-source-clock.exe") `
    -Repeat 1 `
    -FragmentSizes @(0, 1) `
    -ReconnectDelayMilliseconds 0 `
    -EvidenceDirectory $controlSmokeEvidence | Out-Null
$controlSmokeResult = Join-Path $controlSmokeEvidence "result.json"
if (-not (Test-Path -LiteralPath $controlSmokeResult -PathType Leaf)) {
    throw "packaged receiver control smoke did not produce result.json"
}
$qualificationOutput = Join-Path $outputRoot "qualification"
New-Item -ItemType Directory -Force -Path $qualificationOutput | Out-Null
Copy-Item -LiteralPath $controlSmokeResult -Destination (Join-Path $qualificationOutput "control-smoke-result.json") -Force

& (Join-Path $repoRoot "scripts\test-uxplay-headless-fps.ps1") `
    -ReceiverPath (Join-Path $receiverOutput "uxplay-source-clock.exe") `
    -ExpectedFPS 60
if ($LASTEXITCODE -ne 0) {
    throw "packaged receiver headless FPS regression failed with exit code $LASTEXITCODE"
}

# Keep each runtime component's licensing and corresponding source offer
# explicit in the candidate package. This metadata does not relicense the
# ImagePadServer application or collapse third-party licenses into one.
$licenseManifest = [ordered]@{
    schema = 1
    runtimeSetID = $Version
    components = @(
        [ordered]@{
            name = "UxPlay source-clock receiver"
            license = "GPL-3.0-or-later"
            source = "https://github.com/FDH2/UxPlay"
            sourceMetadata = "uxplay-source-clock/source.json"
            patches = "uxplay-source-clock/patches/"
            binary = "uxplay-source-clock/uxplay-source-clock.exe"
        }
        [ordered]@{
            name = "GStreamer framework and plugins"
            license = "component-specific; see upstream GStreamer notices"
            source = "https://gstreamer.freedesktop.org/"
            binary = "gstreamer/"
        }
        [ordered]@{
            name = "ImagePad GStreamer bridge"
            license = "MIT (ImagePadServer repository)"
            source = "ImagePadServer source tree: native/airplay-gstreamer-bridge"
            binary = "gstreamer/airplay-gstreamer-bridge.exe"
        }
    )
}
$licenseManifest | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $licenseManifestPath -Encoding UTF8
@"
# ImagePadServer AirPlay runtime source offer

runtimeSetID: $Version

This candidate contains separately licensed runtime components. The root
ImagePadServer application remains under its own repository license; this
runtime metadata does not change that license.

## UxPlay source-clock

The receiver is a modified GPL-3.0-or-later component. The corresponding
source metadata and ImagePad patches are under 'uxplay-source-clock/'.
Reference source: https://github.com/FDH2/UxPlay

## GStreamer and bridge

GStreamer and its plugins retain their individual upstream licenses. Start at
https://gstreamer.freedesktop.org/documentation/frequently-asked-questions/licensing.html
and inspect the component notices before redistribution. The bridge source is
tracked by the ImagePadServer source tree and its build provenance.

This file is a source-offer index for the candidate runtime. It is not a legal
opinion; retain the corresponding source, patch, recipe, and notices for the
release identified by runtimeSetID and the package hashes.
"@ | Set-Content -LiteralPath $sourceOfferPath -Encoding UTF8

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $licensesReadmePath) | Out-Null
@"
# Runtime license index

The GStreamer package carries upstream license and notice files below
'share/licenses/'. The root license-manifest.json identifies the component
boundary and this index intentionally does not replace those upstream files.

The UxPlay GPL-3.0-or-later source metadata, patch set, and source offer are
under 'uxplay-source-clock/'. The ImagePad bridge remains covered by the
ImagePadServer repository license and its source path is recorded in the root
manifest.
"@ | Set-Content -LiteralPath $licensesReadmePath -Encoding UTF8

$runtimeDescriptor = [ordered]@{
    schema = 1
    runtimeSetID = $Version
    architecture = "windows-amd64"
    protocolVersion = 1
    receiver = "uxplay-source-clock/uxplay-source-clock.exe"
    bridge = "gstreamer/airplay-gstreamer-bridge.exe"
    gstreamerRoot = "gstreamer"
    gstreamerVersion = $probeResult.version
    capabilityProbe = "gstreamer/airplay-gstreamer-capability-probe.exe"
    licenseManifest = "license-manifest.json"
    sourceOffer = "SOURCE-OFFER.md"
}
$runtimeDescriptor | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $runtimeDescriptorPath -Encoding UTF8

$package = [ordered]@{
    schema = 1
    version = $Version
    architecture = "windows-amd64"
    protocolVersion = 1
    receiver = "uxplay-source-clock/uxplay-source-clock.exe"
    receiverCapabilities = "uxplay-source-clock/imagepad-source-clock-capabilities.json"
    bridge = "gstreamer/airplay-gstreamer-bridge.exe"
    bridgeProfile = $bridgeProfile
    bridgeBuildProvenance = if ($RequireFixedPcmUDPCandidate.IsPresent) {
        "gstreamer/imagepad-airplay-gstreamer-bridge-build.json"
    } else {
        $null
    }
    controlSmoke = "qualification/control-smoke-result.json"
    fileManifest = "imagepad-airplay-source-clock-manifest.json"
    runtimeDescriptor = "imagepad-airplay-runtime.json"
    licenseManifest = "license-manifest.json"
    sourceOffer = "SOURCE-OFFER.md"
    receiverRuntime = [ordered]@{
        compilerRuntimeRoot = $CompilerRuntimeRoot
        libplistRuntimePath = $LibplistRuntimePath
        libstdcxxSha256 = Get-Sha256Hex (Join-Path $CompilerRuntimeRoot "libstdc++-6.dll")
        libgccSha256 = Get-Sha256Hex (Join-Path $CompilerRuntimeRoot "libgcc_s_seh-1.dll")
        libwinpthreadSha256 = Get-Sha256Hex (Join-Path $CompilerRuntimeRoot "libwinpthread-1.dll")
        libplistSha256 = Get-Sha256Hex $LibplistRuntimePath
    }
    configuration = [ordered]@{
        IMAGEPAD_AIRPLAY_PIPELINE = "source-clock"
        IMAGEPAD_AIRPLAY_RECEIVER = "<package>\\uxplay-source-clock\\uxplay-source-clock.exe"
        IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE = "<package>\\gstreamer\\airplay-gstreamer-bridge.exe"
    }
}
$package | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $outputRoot "imagepad-airplay-source-clock-package.json") -Encoding UTF8
$manifestPath = Join-Path $outputRoot "imagepad-airplay-source-clock-manifest.json"
$files = @(
    Get-ChildItem -LiteralPath $outputRoot -Recurse -File |
        Where-Object { $_.FullName -ne $manifestPath } |
        ForEach-Object {
            $relative = [IO.Path]::GetRelativePath($outputRoot, $_.FullName).Replace("\", "/")
            [ordered]@{
                path = $relative
                size = $_.Length
                sha256 = Get-Sha256Hex $_.FullName
            }
        }
)
[ordered]@{
    schema = 1
    version = $Version
    architecture = "windows-amd64"
    files = $files
} | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $manifestPath -Encoding UTF8
Write-Output "AirPlay source-clock qualification package: $outputRoot"
