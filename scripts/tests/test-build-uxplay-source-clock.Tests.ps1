$scriptPath = (Resolve-Path (Join-Path $PSScriptRoot "..\build-uxplay-source-clock.ps1")).Path
$headlessScriptPath = (Resolve-Path (Join-Path $PSScriptRoot "..\test-uxplay-headless-fps.ps1")).Path

function Get-UxPlayBuildFunctionDefinition([string]$Name) {
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

Describe "UxPlay source-clock format and metrics patch stack" {
    BeforeAll {
        . ([scriptblock]::Create((Get-UxPlayBuildFunctionDefinition "Invoke-Checked")))
        . ([scriptblock]::Create((Get-UxPlayBuildFunctionDefinition "Clear-UxPlaySourceClockCapabilities")))
        . ([scriptblock]::Create((Get-UxPlayBuildFunctionDefinition "Initialize-PinnedGitCheckout")))
        . ([scriptblock]::Create((Get-UxPlayBuildFunctionDefinition "Invoke-PatchStepsTransactional")))
    }

    It "places format and metrics patches after 0008 and records their capabilities" {
        $text = Get-Content -LiteralPath $scriptPath -Raw
        $patch8 = $text.IndexOf('0008-imagepad-source-clock-video-backlog.patch')
        $patch9 = $text.IndexOf('0009-imagepad-source-clock-audio-format-lock.patch')
        $patch10 = $text.IndexOf('0010-imagepad-source-clock-egress-metrics.patch')
        $patch11 = $text.IndexOf('0011-imagepad-source-clock-metrics-emitter.patch')
        $patch12 = $text.IndexOf('0012-imagepad-source-clock-video-bootstrap-cache.patch')

        ($patch8 -ge 0) | Should Be $true
        ($patch9 -gt $patch8) | Should Be $true
        ($patch10 -gt $patch9) | Should Be $true
        ($patch11 -gt $patch10) | Should Be $true
        ($patch12 -gt $patch11) | Should Be $true
        ($text.Contains('"audio-format-lock"')) | Should Be $true
        ($text.Contains('"egress-metrics-v2"')) | Should Be $true
        ($text.Contains('"egress-metrics-v1"')) | Should Be $false
    }

    It "applies the video bootstrap cache patch after the metrics emitter patch" {
        $text = Get-Content -LiteralPath $scriptPath -Raw
        $metrics = $text.IndexOf('$metricsEmitterPatchPath')
        $video = $text.IndexOf('$videoBootstrapPatchPath')

        ($metrics -ge 0) | Should Be $true
        ($video -gt $metrics) | Should Be $true
    }

    It "keeps the bootstrap cache session-scoped and separate from writer integration" {
        $patchPath = Join-Path (Split-Path -Parent (Split-Path -Parent $scriptPath)) `
            "third_party\uxplay-windows\patches\0012-imagepad-source-clock-video-bootstrap-cache.patch"
        $text = Get-Content -LiteralPath $patchPath -Raw

        $text.Contains('SOURCE_CLOCK_VIDEO_BOOTSTRAP_MAX_PARAMETER_SETS 32U') | Should Be $true
        $text.Contains('SOURCE_CLOCK_VIDEO_BOOTSTRAP_MAX_NAL_SIZE (128U * 1024U)') | Should Be $true
        $text.Contains('SOURCE_CLOCK_VIDEO_BOOTSTRAP_MAX_TOTAL_SIZE (256U * 1024U)') | Should Be $true
        $text.Contains('SOURCE_CLOCK_VIDEO_BOOTSTRAP_MAX_INPUT_SIZE (64U * 1024U * 1024U)') | Should Be $true
        $text.Contains('source_clock_video_bootstrap_push_annexb_for_session') | Should Be $true
        $text.Contains('source_clock_video_bootstrap_snapshot_copy_for_pps') | Should Be $true
        $text.Contains('source_clock_video_bootstrap_include_order_test.c') | Should Be $true
        $text.Contains('SOURCE_CLOCK_VIDEO_BOOTSTRAP_ACCEPTED') | Should Be $false
        $text.Contains('source_clock_video_bootstrap_set_codec(') | Should Be $false
        $text.Contains('diff --git a/renderers/source_clock_renderer.c') | Should Be $false
    }

    It "applies reconnect bootstrap only after the standalone cache patch" {
        $text = Get-Content -LiteralPath $scriptPath -Raw
        $cache = $text.IndexOf('0012-imagepad-source-clock-video-bootstrap-cache.patch')
        $reconnect = $text.IndexOf('0013-imagepad-source-clock-video-bootstrap-reconnect.patch')

        ($cache -ge 0) | Should Be $true
        ($reconnect -gt $cache) | Should Be $true
        $text.Contains('"video-bootstrap-reconnect"') | Should Be $true
    }

    It "keeps reconnect bootstrap scoped to the video writer and a dedicated test" {
        $patchPath = Join-Path (Split-Path -Parent (Split-Path -Parent $scriptPath)) `
            "third_party\uxplay-windows\patches\0013-imagepad-source-clock-video-bootstrap-reconnect.patch"
        Test-Path -LiteralPath $patchPath | Should Be $true
        $text = Get-Content -LiteralPath $patchPath -Raw

        $text.Contains('diff --git a/renderers/source_clock_renderer.c') | Should Be $true
        $text.Contains('source_clock_video_receiver_bootstrap_test.c') | Should Be $true
        $text.Contains('diff --git a/renderers/audio_renderer.c') | Should Be $false
    }

    It "passes the required metrics receiver id in the headless smoke" {
        $text = Get-Content -LiteralPath $headlessScriptPath -Raw

        ($text.Contains('"-ipscid"')) | Should Be $true
    }

    It "rebuilds an applied prefix and then applies the remaining suffix" {
        $text = Get-Content -LiteralPath $scriptPath -Raw

        ($text.Contains('$lastAppliedIndex = -1')) | Should Be $true
        ($text.Contains('for ($index = $patchSteps.Count - 1; $index -ge 0; $index--)')) | Should Be $true
        ($text.Contains('Invoke-PatchStepsTransactional -Repository $libPath -Steps $patchSteps -StartIndex ($lastAppliedIndex + 1)')) | Should Be $true
        ($text.Contains('UxPlay lib checkout has unexpected changes')) | Should Be $true
    }

    It "invalidates an earlier capability before a failing build stage" {
        $capabilitiesPath = Join-Path $TestDrive "imagepad-source-clock-capabilities.json"
        Set-Content -LiteralPath $capabilitiesPath -Value '{"schema":1}' -Encoding UTF8
        $message = $null

        Clear-UxPlaySourceClockCapabilities -CapabilitiesPath $capabilitiesPath
        try {
            Invoke-Checked "cmd.exe" @("/d", "/c", "exit", "23")
        } catch {
            $message = $_.Exception.Message
        }

        (Test-Path -LiteralPath $capabilitiesPath) | Should Be $false
        $message | Should Match "23"
    }

    It "clears capability before CMake configure" {
        $text = Get-Content -LiteralPath $scriptPath -Raw
        $clear = $text.LastIndexOf('Clear-UxPlaySourceClockCapabilities')
        $configure = $text.IndexOf('Invoke-Checked "cmake"')

        ($clear -ge 0) | Should Be $true
        ($clear -lt $configure) | Should Be $true
    }

    It "does not move an existing checkout that is not at the pinned commit" {
        $repository = Join-Path $TestDrive "wrong-head"
        New-Item -ItemType Directory -Path $repository | Out-Null
        & git -C $repository init --quiet
        & git -C $repository config user.email "imagepad-test@example.invalid"
        & git -C $repository config user.name "ImagePad Test"
        Set-Content -LiteralPath (Join-Path $repository "state.txt") -Value "first" -Encoding ascii
        & git -C $repository add state.txt
        & git -C $repository commit --quiet -m "first"
        $pinned = (& git -C $repository rev-parse HEAD).Trim()
        Set-Content -LiteralPath (Join-Path $repository "state.txt") -Value "second" -Encoding ascii
        & git -C $repository add state.txt
        & git -C $repository commit --quiet -m "second"
        $before = (& git -C $repository rev-parse HEAD).Trim()
        $message = $null

        try {
            Initialize-PinnedGitCheckout -RepositoryURL $repository -Directory $repository -ExpectedCommit $pinned | Out-Null
        } catch {
            $message = $_.Exception.Message
        }

        $after = (& git -C $repository rev-parse HEAD).Trim()
        $after | Should Be $before
        (Get-Content -LiteralPath (Join-Path $repository "state.txt") -Raw).Trim() | Should Be "second"
        $message | Should Match "expected commit"
    }

    It "rolls back only patches newly applied by a failed suffix" {
        $repository = Join-Path $TestDrive "patch-rollback"
        New-Item -ItemType Directory -Path $repository | Out-Null
        & git -C $repository init --quiet
        & git -C $repository config user.email "imagepad-test@example.invalid"
        & git -C $repository config user.name "ImagePad Test"
        & git -C $repository config core.autocrlf false
        Set-Content -LiteralPath (Join-Path $repository "sample.txt") -Value "base" -Encoding ascii
        & git -C $repository add sample.txt
        & git -C $repository commit --quiet -m "base"
        $firstPatch = Join-Path $TestDrive "first.patch"
        $brokenPatch = Join-Path $TestDrive "broken.patch"
        Set-Content -LiteralPath $firstPatch -Encoding ascii -Value @'
diff --git a/sample.txt b/sample.txt
--- a/sample.txt
+++ b/sample.txt
@@ -1 +1 @@
-base
+one
'@
        Set-Content -LiteralPath $brokenPatch -Encoding ascii -Value @'
diff --git a/sample.txt b/sample.txt
--- a/sample.txt
+++ b/sample.txt
@@ -1 +1 @@
-missing
+two
'@
        $steps = @(
            [pscustomobject]@{ Path = $firstPatch; ApplyArgs = @() },
            [pscustomobject]@{ Path = $brokenPatch; ApplyArgs = @() }
        )
        $message = $null

        try {
            Invoke-PatchStepsTransactional -Repository $repository -Steps $steps -StartIndex 0
        } catch {
            $message = $_.Exception.Message
        }

        (Get-Content -LiteralPath (Join-Path $repository "sample.txt") -Raw).Trim() | Should Be "base"
        @(git -C $repository status --porcelain).Count | Should Be 0
        $message | Should Match "failed"
    }
}
