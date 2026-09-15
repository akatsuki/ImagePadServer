$scriptPath = (Resolve-Path (Join-Path $PSScriptRoot "..\package-airplay-source-clock.ps1")).Path

function Get-PackageFunctionDefinition([string]$Name) {
    $tokens = $null
    $errors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$errors)
    if ($errors.Count -ne 0) {
        throw "package script parse failed: $($errors[0].Message)"
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

Describe "AirPlay source-clock package provenance" {
    BeforeAll {
        . ([scriptblock]::Create((Get-PackageFunctionDefinition "Assert-FixedPcmUDPCandidateBuild")))
        . ([scriptblock]::Create((Get-PackageFunctionDefinition "Assert-PackagedFixedPcmUDPCandidate")))
    }

    It "accepts a Release executable whose candidate provenance and hash match" {
        $root = Join-Path $TestDrive "candidate"
        $release = Join-Path $root "Release"
        New-Item -ItemType Directory -Force -Path $release | Out-Null
        $bridge = Join-Path $release "airplay-gstreamer-bridge.exe"
        [IO.File]::WriteAllBytes($bridge, [byte[]](1, 2, 3, 4, 5))
        [ordered]@{
            schema = 1
            configuration = "Release"
            fixedPcmAudio = $true
            rtspPublishTransport = "udp"
            executable = "airplay-gstreamer-bridge.exe"
            executableSha256 = (Get-FileHash -LiteralPath $bridge -Algorithm SHA256).Hash.ToLowerInvariant()
        } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $release "imagepad-airplay-gstreamer-bridge-build.json") -Encoding UTF8

        $result = Assert-FixedPcmUDPCandidateBuild -BridgeRoot $release

        $result.fixedPcmAudio | Should Be $true
        $result.rtspPublishTransport | Should Be "udp"
        $result.executableSha256 | Should Be ((Get-FileHash -LiteralPath $bridge -Algorithm SHA256).Hash.ToLowerInvariant())
    }

    It "rejects a stale executable whose hash does not match the provenance" {
        $root = Join-Path $TestDrive "partial"
        $release = Join-Path $root "Release"
        New-Item -ItemType Directory -Force -Path $release | Out-Null
        $bridge = Join-Path $release "airplay-gstreamer-bridge.exe"
        [IO.File]::WriteAllBytes($bridge, [byte[]](6, 7, 8))
        [ordered]@{
            schema = 1
            configuration = "Release"
            fixedPcmAudio = $true
            rtspPublishTransport = "udp"
            executable = "airplay-gstreamer-bridge.exe"
            executableSha256 = ("0" * 64)
        } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $release "imagepad-airplay-gstreamer-bridge-build.json") -Encoding UTF8
        $message = $null

        try { Assert-FixedPcmUDPCandidateBuild -BridgeRoot $release | Out-Null } catch { $message = $_.Exception.Message }

        ($message -like "*hash does not match*") | Should Be $true
    }

    It "rejects a provenance record with the wrong flags or configuration" {
        $release = Join-Path $TestDrive "wrong-profile\Release"
        New-Item -ItemType Directory -Force -Path $release | Out-Null
        $bridge = Join-Path $release "airplay-gstreamer-bridge.exe"
        [IO.File]::WriteAllBytes($bridge, [byte[]](9, 10, 11))
        [ordered]@{
            schema = 1
            configuration = "Debug"
            fixedPcmAudio = $true
            rtspPublishTransport = "udp"
            executable = "airplay-gstreamer-bridge.exe"
            executableSha256 = (Get-FileHash -LiteralPath $bridge -Algorithm SHA256).Hash.ToLowerInvariant()
        } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $release "imagepad-airplay-gstreamer-bridge-build.json") -Encoding UTF8
        $message = $null

        try { Assert-FixedPcmUDPCandidateBuild -BridgeRoot $release | Out-Null } catch { $message = $_.Exception.Message }

        ($message -like "*Release configuration*") | Should Be $true
    }

    It "rejects a candidate build without executable-bound provenance" {
        $release = Join-Path $TestDrive "missing\Release"
        New-Item -ItemType Directory -Force -Path $release | Out-Null
        [IO.File]::WriteAllBytes((Join-Path $release "airplay-gstreamer-bridge.exe"), [byte[]](12, 13))
        $message = $null

        try { Assert-FixedPcmUDPCandidateBuild -BridgeRoot $release | Out-Null } catch { $message = $_.Exception.Message }

        ($message -like "*build provenance*") | Should Be $true
    }

    It "rejects a copied executable that differs from the pre-copy candidate hash" {
        $release = Join-Path $TestDrive "copy-mismatch\Release"
        New-Item -ItemType Directory -Force -Path $release | Out-Null
        $bridge = Join-Path $release "airplay-gstreamer-bridge.exe"
        [IO.File]::WriteAllBytes($bridge, [byte[]](21, 22, 23))
        $sourceHash = (Get-FileHash -LiteralPath $bridge -Algorithm SHA256).Hash.ToLowerInvariant()
        [ordered]@{
            schema = 1
            configuration = "Release"
            fixedPcmAudio = $true
            rtspPublishTransport = "udp"
            executable = "airplay-gstreamer-bridge.exe"
            executableSha256 = $sourceHash
        } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $release "imagepad-airplay-gstreamer-bridge-build.json") -Encoding UTF8
        [IO.File]::WriteAllBytes($bridge, [byte[]](24, 25, 26))
        $message = $null

        try {
            Assert-PackagedFixedPcmUDPCandidate -PackagedBridgeRoot $release -ExpectedExecutableSha256 $sourceHash | Out-Null
        } catch {
            $message = $_.Exception.Message
        }

        ($message -like "*packaged*hash*") | Should Be $true
    }

    It "validates before output mutation and records the package bridge selection" {
        $text = Get-Content -LiteralPath $scriptPath -Raw
        $guard = $text.IndexOf('$candidateBuild = Assert-FixedPcmUDPCandidateBuild')
        $outputMutation = $text.IndexOf('New-Item -ItemType Directory -Force -Path $outputRoot')

        ($text.Contains('[switch]$RequireFixedPcmUDPCandidate')) | Should Be $true
        ($guard -ge 0) | Should Be $true
        ($guard -lt $outputMutation) | Should Be $true
        ($text.Contains('IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE = "<package>\\gstreamer\\airplay-gstreamer-bridge.exe"')) | Should Be $true
        ($text.Contains('bridgeProfile = $bridgeProfile')) | Should Be $true
        ($text.Contains('bridgeBuildProvenance = if ($RequireFixedPcmUDPCandidate.IsPresent)')) | Should Be $true
        ($text.LastIndexOf('Assert-PackagedFixedPcmUDPCandidate') -gt $text.IndexOf('package-airplay-gstreamer-runtime.ps1')) | Should Be $true
    }

    It "requires the v2 receiver metrics contract before packaging" {
        $text = Get-Content -LiteralPath $scriptPath -Raw

        ($text.Contains('"egress-metrics-v2"')) | Should Be $true
        ($text.Contains('"egress-metrics-v1"')) | Should Be $false
        ($text.Contains('0009-imagepad-source-clock-audio-format-lock.patch')) | Should Be $true
        ($text.Contains('0010-imagepad-source-clock-egress-metrics.patch')) | Should Be $true
        ($text.Contains('0011-imagepad-source-clock-metrics-emitter.patch')) | Should Be $true
    }

    It "keeps compiler runtime and libplist runtime inputs separate" {
        $text = Get-Content -LiteralPath $scriptPath -Raw

        ($text.Contains('[string]$CompilerRuntimeRoot = ""')) | Should Be $true
        ($text.Contains('[string]$LibplistRuntimePath = ""')) | Should Be $true
        ($text.Contains('$compilerRuntimeNames = @(')) | Should Be $true
        ($text.Contains('Join-Path $CompilerRuntimeRoot $name')) | Should Be $true
        ($text.Contains('Copy-Item -LiteralPath $LibplistRuntimePath -Destination $receiverOutput -Force')) | Should Be $true
        ($text.Contains('compilerRuntimeRoot = $CompilerRuntimeRoot')) | Should Be $true
        ($text.Contains('libplistRuntimePath = $LibplistRuntimePath')) | Should Be $true
    }

    It "refuses to append into a non-empty package staging directory" {
        $text = Get-Content -LiteralPath $scriptPath -Raw
        ($text.Contains('source-clock package output must be an empty staging directory')) | Should Be $true
        ($text.IndexOf('source-clock package output must be an empty staging directory') -lt
            $text.IndexOf('New-Item -ItemType Directory -Force -Path $outputRoot')) | Should Be $true
    }
}
