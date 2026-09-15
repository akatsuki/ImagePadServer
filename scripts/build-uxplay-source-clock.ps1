[CmdletBinding()]
param(
    [string]$SourceDirectory = (Join-Path $env:TEMP "imagepad-uxplay-source-clock"),
    [string]$BuildDirectory = (Join-Path (Get-Location) "build\uxplay-source-clock"),
    [string]$GStreamerRoot = "",
    [string]$BonjourSdkHome = "",
    [string]$PkgConfigExecutable = "",
    [switch]$TestOnly
)

$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$provenancePath = Join-Path $repoRoot "third_party\uxplay-windows\source.json"
$patchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0001-imagepad-source-clock-egress.patch"
$h265PatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0002-imagepad-h265-payload-bounds.patch"
$plistPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0003-imagepad-plist-response-ownership.patch"
$plistValueOwnershipPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0004-imagepad-libplist-value-ownership.patch"
$fragmentSafeProtocolPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0005-imagepad-fragment-safe-rtsp-protocol.patch"
$writerWaitPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0006-imagepad-source-clock-writer-wait.patch"
$headlessFPSPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0007-imagepad-source-clock-headless-fps.patch"
$videoBacklogPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0008-imagepad-source-clock-video-backlog.patch"
$audioFormatLockPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0009-imagepad-source-clock-audio-format-lock.patch"
$egressMetricsPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0010-imagepad-source-clock-egress-metrics.patch"
$metricsEmitterPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0011-imagepad-source-clock-metrics-emitter.patch"
$videoBootstrapPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0012-imagepad-source-clock-video-bootstrap-cache.patch"
$videoBootstrapReconnectPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0013-imagepad-source-clock-video-bootstrap-reconnect.patch"
$videoLossRecoveryPatchPath = Join-Path $repoRoot "third_party\uxplay-windows\patches\0014-imagepad-source-clock-video-loss-recovery.patch"
$provenance = Get-Content -LiteralPath $provenancePath -Raw | ConvertFrom-Json

function Invoke-Checked {
    param([string]$FilePath, [string[]]$ArgumentList)
    & $FilePath @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath failed with exit code $LASTEXITCODE"
    }
}

function Clear-UxPlaySourceClockCapabilities([string]$CapabilitiesPath) {
    if (Test-Path -LiteralPath $CapabilitiesPath -PathType Leaf) {
        Remove-Item -LiteralPath $CapabilitiesPath -Force
    }
}

function Test-GitPatchApplies(
    [string]$Repository,
    [object]$Step,
    [bool]$Reverse
) {
    $arguments = @("-C", $Repository, "apply") + @($Step.ApplyArgs)
    if ($Reverse) {
        $arguments += "--reverse"
    }
    $arguments += @("--check", $Step.Path)
    & git @arguments 2>$null | Out-Null
    return $LASTEXITCODE -eq 0
}

function Initialize-PinnedGitCheckout(
    [string]$RepositoryURL,
    [string]$Directory,
    [string]$ExpectedCommit
) {
    $created = $false
    if (-not (Test-Path -LiteralPath $Directory)) {
        Invoke-Checked "git" @("clone", $RepositoryURL, $Directory)
        $created = $true
    } elseif (-not (Test-Path -LiteralPath $Directory -PathType Container)) {
        throw "git checkout path is not a directory: $Directory"
    } elseif (-not (Test-Path -LiteralPath (Join-Path $Directory ".git"))) {
        $entries = @(Get-ChildItem -LiteralPath $Directory -Force -ErrorAction SilentlyContinue)
        if ($entries.Count -ne 0) {
            throw "git checkout path exists but is not empty: $Directory"
        }
        Invoke-Checked "git" @("clone", $RepositoryURL, $Directory)
        $created = $true
    }

    if ($created) {
        Invoke-Checked "git" @("-C", $Directory, "checkout", "--detach", $ExpectedCommit)
        return $true
    }

    $actualCommit = (& git -C $Directory rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "failed to read git HEAD: $Directory"
    }
    if (-not $actualCommit.Equals($ExpectedCommit, [StringComparison]::OrdinalIgnoreCase)) {
        throw "git checkout is not at the expected commit: $Directory (expected $ExpectedCommit, actual $actualCommit)"
    }
    return $false
}

function Invoke-PatchStepsTransactional(
    [string]$Repository,
    [object[]]$Steps,
    [int]$StartIndex
) {
    $newlyAppliedSteps = [Collections.Generic.List[object]]::new()
    try {
        for ($index = $StartIndex; $index -lt $Steps.Count; $index++) {
            $step = $Steps[$index]
            Invoke-Checked "git" (@("-C", $Repository, "apply") + @($step.ApplyArgs) + @("--check", $step.Path))
            Invoke-Checked "git" (@("-C", $Repository, "apply") + @($step.ApplyArgs) + @($step.Path))
            $newlyAppliedSteps.Add($step)
        }
    } catch {
        $applicationError = $_
        $rollbackErrors = [Collections.Generic.List[string]]::new()
        for ($index = $newlyAppliedSteps.Count - 1; $index -ge 0; $index--) {
            $step = $newlyAppliedSteps[$index]
            try {
                Invoke-Checked "git" (@("-C", $Repository, "apply") + @($step.ApplyArgs) + @("--reverse", $step.Path))
            } catch {
                $rollbackErrors.Add($_.Exception.Message)
            }
        }
        if ($rollbackErrors.Count -ne 0) {
            throw "patch application failed: $($applicationError.Exception.Message); rollback failed: $($rollbackErrors -join '; ')"
        }
        throw $applicationError
    }
}

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

$sourceCreated = Initialize-PinnedGitCheckout -RepositoryURL $provenance.wrapperRepository -Directory $SourceDirectory -ExpectedCommit $provenance.wrapperCommit
$sourcePath = (Resolve-Path $SourceDirectory).Path
$wrapperStatus = @(git -C $sourcePath status --porcelain)
$unexpectedWrapperStatus = @($wrapperStatus | Where-Object { $_ -notmatch '^\s*[MADRCU?]{1,2}\s+libuxplay$' })
if ($unexpectedWrapperStatus.Count -gt 0) {
    throw "UxPlay wrapper checkout is dirty: $sourcePath"
}

$libPath = Join-Path $sourcePath "libuxplay"
$libCreated = Initialize-PinnedGitCheckout -RepositoryURL $provenance.libuxplayRepository -Directory $libPath -ExpectedCommit $provenance.libuxplayCommit
$libStatus = @(git -C $libPath status --porcelain)
$patchSteps = @(
    [pscustomobject]@{ Path = $patchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $h265PatchPath; ApplyArgs = @("--ignore-whitespace") },
    [pscustomobject]@{ Path = $plistPatchPath; ApplyArgs = @("--unidiff-zero") },
    [pscustomobject]@{ Path = $plistValueOwnershipPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $fragmentSafeProtocolPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $writerWaitPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $headlessFPSPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $videoBacklogPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $audioFormatLockPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $egressMetricsPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $metricsEmitterPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $videoBootstrapPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $videoBootstrapReconnectPatchPath; ApplyArgs = @() },
    [pscustomobject]@{ Path = $videoLossRecoveryPatchPath; ApplyArgs = @() }
)
if ($libStatus.Count -eq 0) {
    Invoke-PatchStepsTransactional -Repository $libPath -Steps $patchSteps -StartIndex 0
} else {
    $lastAppliedIndex = -1
    for ($index = $patchSteps.Count - 1; $index -ge 0; $index--) {
        if (Test-GitPatchApplies -Repository $libPath -Step $patchSteps[$index] -Reverse $true) {
            $lastAppliedIndex = $index
            break
        }
    }
    if ($lastAppliedIndex -lt 0) {
        throw "UxPlay lib checkout has unexpected changes: $libPath"
    }
    $reversedSteps = [Collections.Generic.List[object]]::new()
    try {
        for ($index = $lastAppliedIndex; $index -ge 0; $index--) {
            $step = $patchSteps[$index]
            if (-not (Test-GitPatchApplies -Repository $libPath -Step $step -Reverse $true)) {
                throw "patch is in a partial or non-prefix application state: $($step.Path)"
            }
            $arguments = @("-C", $libPath, "apply") + @($step.ApplyArgs) + @("--reverse", $step.Path)
            Invoke-Checked "git" $arguments
            $reversedSteps.Add($step)
        }
        $remainingStatus = @(git -C $libPath status --porcelain)
        if ($remainingStatus.Count -ne 0) {
            throw "UxPlay lib checkout has unexpected changes: $libPath"
        }
    }
    finally {
        for ($index = $reversedSteps.Count - 1; $index -ge 0; $index--) {
            $step = $reversedSteps[$index]
            $arguments = @("-C", $libPath, "apply") + @($step.ApplyArgs) + @($step.Path)
            Invoke-Checked "git" $arguments
        }
    }
    Invoke-PatchStepsTransactional -Repository $libPath -Steps $patchSteps -StartIndex ($lastAppliedIndex + 1)
}

if (-not $GStreamerRoot) {
    $GStreamerRoot = "C:\Users\$env:USERNAME\AppData\Local\Programs\gstreamer\1.0\msvc_x86_64"
}
if (-not (Test-Path -LiteralPath (Join-Path $GStreamerRoot "include\gstreamer-1.0\gst\gst.h"))) {
    throw "GStreamer development headers were not found under $GStreamerRoot"
}
if (-not $BonjourSdkHome) {
    $BonjourSdkHome = $env:BONJOUR_SDK_HOME
}
if (-not $BonjourSdkHome) {
    $BonjourSdkHome = "C:\Program Files\Bonjour SDK"
}
if (-not (Test-Path -LiteralPath (Join-Path $BonjourSdkHome "Include\dns_sd.h"))) {
    throw "Bonjour SDK header was not found under $BonjourSdkHome"
}

$cc = (Get-Command gcc -ErrorAction Stop).Source
$cxx = (Get-Command g++ -ErrorAction Stop).Source
if (-not $PkgConfigExecutable) {
    $pkgCommand = Get-Command pkg-config -ErrorAction SilentlyContinue
    if ($pkgCommand) {
        $PkgConfigExecutable = $pkgCommand.Source
    } else {
        $PkgConfigExecutable = "C:\msys64\mingw64\bin\pkg-config.exe"
    }
}
if (-not (Test-Path -LiteralPath $PkgConfigExecutable)) {
    throw "pkg-config executable was not found: $PkgConfigExecutable"
}
$pkg = (Resolve-Path $PkgConfigExecutable).Path
$env:PKG_CONFIG_PATH = "$GStreamerRoot\lib\pkgconfig;"
$env:BONJOUR_SDK_HOME = $BonjourSdkHome

New-Item -ItemType Directory -Force -Path $BuildDirectory | Out-Null
$buildPath = (Resolve-Path $BuildDirectory).Path
$capabilitiesPath = Join-Path $buildPath "imagepad-source-clock-capabilities.json"
Clear-UxPlaySourceClockCapabilities -CapabilitiesPath $capabilitiesPath
Invoke-Checked "cmake" @(
    "-S", $libPath,
    "-B", $buildPath,
    "-G", "MinGW Makefiles",
    "-DCMAKE_C_COMPILER=$cc",
    "-DCMAKE_CXX_COMPILER=$cxx",
    "-DPKG_CONFIG_EXECUTABLE=$pkg",
    "-DPKG_CONFIG_USE_CMAKE_PREFIX_PATH=OFF",
    "-DNO_MARCH_NATIVE=ON",
    "-DBUILD_TESTING=ON",
    "-DBUILD_SOURCE_CLOCK_CLI=ON",
    "-DCMAKE_BUILD_TYPE=Release",
    "-DCMAKE_PREFIX_PATH=$GStreamerRoot"
)
Invoke-Checked "cmake" @("--build", $buildPath, "--config", "Release", "--parallel", "4")
$oldTestPath = $env:PATH
try {
    $testRuntimePaths = @(
        (Split-Path -Parent $cc),
        (Split-Path -Parent $pkg),
        (Join-Path $GStreamerRoot "bin")
    ) | Select-Object -Unique
    $env:PATH = (($testRuntimePaths -join ";") + ";" + $oldTestPath)
    Invoke-Checked "ctest" @("--test-dir", $buildPath, "-C", "Release", "--output-on-failure")
}
finally {
    $env:PATH = $oldTestPath
}

$receiverBinary = Join-Path $buildPath "uxplay-source-clock.exe"
if (-not (Test-Path -LiteralPath $receiverBinary)) {
    throw "source-clock receiver binary was not produced: $receiverBinary"
}
$receiverHash = Get-Sha256Hex $receiverBinary
$capabilities = [ordered]@{
    schema = 1
    protocolVersion = [int]$provenance.protocolVersion
    wrapperCommit = $provenance.wrapperCommit
    libuxplayCommit = $provenance.libuxplayCommit
    binary = (Split-Path -Leaf $receiverBinary)
    binarySha256 = $receiverHash
    features = @("video-au", "audio-frame", "remote-ntp", "bounded-writer", "idle-wait", "headless-fps", "video-backlog-64", "audio-format-lock", "egress-metrics-v2", "plist-owned-free", "fragment-safe-rtsp", "video-bootstrap-reconnect", "video-loss-recovery")
}
$capabilities | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $capabilitiesPath -Encoding utf8

if (-not $TestOnly) {
    Write-Warning "The pinned Windows GUI wrapper requires Qt6; this script validates the patched libuxplay and source-clock renderer only."
}
Write-Output "source-clock lib build: $buildPath"
Write-Output "source-clock receiver: $receiverBinary"
Write-Output "capabilities: $capabilitiesPath"
Write-Output "patched source: $sourcePath"
