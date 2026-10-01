[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$Snapshot,
    [Parameter(Mandatory = $true)][string]$FFmpeg,
    [Parameter(Mandatory = $true)][string]$Helper,
    [Parameter(Mandatory = $true)][string]$OutputRoot,
    [string]$Browser = '',
    [string]$LegacyHelper = '',
    [ValidateSet('x264', 'nvenc')][string]$Encoder = 'x264',
    [ValidateSet(0, 20)][int]$CpuPercent = 0,
    [ValidateSet('auto', 'dx12', 'vulkan', 'metal')][string]$GpuBackend = 'vulkan',
    [ValidateSet('warp', 'hardware')][string]$NativeMode = 'warp',
    [ValidateRange(5, 100)][int]$Repeats = 5,
    [ValidateRange(64, 3840)][int]$Width = 1920,
    [ValidateRange(64, 2160)][int]$Height = 1080,
    [ValidateRange(1, 60000)][int]$FPSNum = 30,
    [ValidateRange(1, 1001)][int]$FPSDen = 1,
    [ValidateRange(1, 3600000)][int]$DurationMs = 6000,
    [ValidateSet(1, 2, 3)][int]$ReadbackSlots = 3,
    [ValidateSet('no-comments', 'browser', 'native', 'timeline-slots1', 'timeline-slots2', 'timeline-slots3', 'timeline-a', 'timeline-b', 'timeline-sync', 'timeline-pbo', 'timeline-separate', 'timeline-atlas')]
    [string[]]$Variants = @('no-comments', 'browser', 'native', 'timeline-slots1', 'timeline-slots2', 'timeline-slots3')
)

$ErrorActionPreference = 'Stop'

function Resolve-RequiredFile([string]$Path, [string]$Label) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Label is not an existing file: $Path"
    }
    return (Resolve-Path -LiteralPath $Path).Path
}

function Quote-NativeArgument([string]$Value) {
    return '"' + $Value.Replace('"', '\"') + '"'
}

$Source = Resolve-RequiredFile $Source 'Source'
$Snapshot = Resolve-RequiredFile $Snapshot 'Snapshot'
$FFmpeg = Resolve-RequiredFile $FFmpeg 'FFmpeg'
$Helper = Resolve-RequiredFile $Helper 'WGPU helper'
if ($Browser -ne '') { $Browser = Resolve-RequiredFile $Browser 'Browser' }
if ($LegacyHelper -ne '') { $LegacyHelper = Resolve-RequiredFile $LegacyHelper 'Legacy compositor' }
if ($Variants -notcontains 'no-comments') { throw 'Variants must include no-comments so paired overhead can be computed' }

if (-not (Test-Path -LiteralPath $OutputRoot -PathType Container)) {
    New-Item -ItemType Directory -Path $OutputRoot -Force | Out-Null
}
$OutputRoot = (Resolve-Path -LiteralPath $OutputRoot).Path
$runName = 'run-{0:yyyyMMdd-HHmmss}-{1}' -f (Get-Date), ([guid]::NewGuid().ToString('N').Substring(0, 8))
$runRoot = Join-Path $OutputRoot $runName
New-Item -ItemType Directory -Path $runRoot | Out-Null

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..\..')).Path
$videoTest = Join-Path $runRoot 'video.test.exe'
$budgetRunner = Join-Path $runRoot 'nico-budget-runner.exe'
$summarizer = Join-Path $PSScriptRoot 'summarize.py'
$summaryPath = Join-Path $runRoot 'summary.json'
$ffprobeCommand = Get-Command ffprobe -ErrorAction SilentlyContinue
if ($null -eq $ffprobeCommand) { throw 'ffprobe was not found on PATH' }
$ffprobe = $ffprobeCommand.Source
$pythonCommand = Get-Command python -ErrorAction SilentlyContinue
if ($null -eq $pythonCommand) { throw 'python was not found on PATH' }
$python = $pythonCommand.Source
$rtk = Get-Command rtk -ErrorAction SilentlyContinue
if ($null -eq $rtk) { throw 'rtk was not found on PATH; build commands must use the repository RTK wrapper' }

$build = Start-Process -FilePath $rtk.Source -ArgumentList @(
    'proxy', 'go', 'test', '-c', '-o', (Quote-NativeArgument $videoTest), './internal/video'
) -WorkingDirectory $repoRoot -WindowStyle Hidden -Wait -PassThru
if ($build.ExitCode -ne 0) { throw "building the one-run test binary failed with exit code $($build.ExitCode)" }

if ($CpuPercent -eq 20) {
    $buildRunner = Start-Process -FilePath $rtk.Source -ArgumentList @(
        'proxy', 'go', 'build', '-o', (Quote-NativeArgument $budgetRunner), './cmd/nico-budget-runner'
    ) -WorkingDirectory $repoRoot -WindowStyle Hidden -Wait -PassThru
    if ($buildRunner.ExitCode -ne 0) { throw "building the CPU budget runner failed with exit code $($buildRunner.ExitCode)" }
}

$envNames = @(
    'IMAGEPAD_NICO_TIMELINE_PERF', 'IMAGEPAD_NICO_PERF_SOURCE', 'IMAGEPAD_NICO_PERF_SNAPSHOT',
    'IMAGEPAD_NICO_PERF_FFMPEG', 'IMAGEPAD_NICO_PERF_FFPROBE', 'IMAGEPAD_NICO_PERF_CPU_PERCENT',
    'IMAGEPAD_NICO_PERF_RUNNER_RECORD', 'IMAGEPAD_NICO_PERF_LEGACY_HELPER', 'IMAGEPAD_NICO_PERF_NATIVE_MODE',
    'IMAGEPAD_NICO_TIMELINE_COMPOSITOR', 'IMAGEPAD_NICO_TIMELINE_GPU_BACKEND', 'NICO_TIMELINE_RUN_DIR',
    'NICO_TIMELINE_RUN_ID', 'NICO_TIMELINE_PAIR_ID', 'NICO_TIMELINE_VARIANT', 'NICO_TIMELINE_ENCODER',
    'NICO_TIMELINE_PRESET', 'NICO_TIMELINE_WIDTH', 'NICO_TIMELINE_HEIGHT', 'NICO_TIMELINE_FPS_NUM',
    'NICO_TIMELINE_FPS_DEN', 'NICO_TIMELINE_DURATION_MS', 'NICO_TIMELINE_CRF', 'NICO_TIMELINE_BROWSER',
    'NICO_TIMELINE_ASSET_READBACK', 'NICO_TIMELINE_ASSET_LAYOUT', 'NICO_TIMELINE_READBACK_SLOTS'
)
$oldEnv = @{}
foreach ($name in $envNames) { $oldEnv[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }

try {
    $env:IMAGEPAD_NICO_TIMELINE_PERF = '1'
    $env:IMAGEPAD_NICO_PERF_SOURCE = $Source
    $env:IMAGEPAD_NICO_PERF_SNAPSHOT = $Snapshot
    $env:IMAGEPAD_NICO_PERF_FFMPEG = $FFmpeg
    $env:IMAGEPAD_NICO_PERF_FFPROBE = $ffprobe
    $env:IMAGEPAD_NICO_PERF_CPU_PERCENT = if ($CpuPercent -eq 0) { 'unlimited' } else { [string]$CpuPercent }
    $env:IMAGEPAD_NICO_PERF_LEGACY_HELPER = $LegacyHelper
    $env:IMAGEPAD_NICO_PERF_NATIVE_MODE = $NativeMode
    $env:IMAGEPAD_NICO_TIMELINE_COMPOSITOR = $Helper
    $env:IMAGEPAD_NICO_TIMELINE_GPU_BACKEND = $GpuBackend
    $env:NICO_TIMELINE_ENCODER = $Encoder
    $env:NICO_TIMELINE_PRESET = if ($Encoder -eq 'nvenc') { 'p4' } else { 'veryfast' }
    $env:NICO_TIMELINE_WIDTH = [string]$Width
    $env:NICO_TIMELINE_HEIGHT = [string]$Height
    $env:NICO_TIMELINE_FPS_NUM = [string]$FPSNum
    $env:NICO_TIMELINE_FPS_DEN = [string]$FPSDen
    $env:NICO_TIMELINE_DURATION_MS = [string]$DurationMs
    $env:NICO_TIMELINE_CRF = '26'
    $env:NICO_TIMELINE_BROWSER = $Browser

    $variants = $Variants
    $hasReadbackPair = ($variants -contains 'timeline-sync') -and ($variants -contains 'timeline-pbo')
    for ($repeat = 1; $repeat -le $Repeats; $repeat++) {
        $pairId = '{0}-cpu{1}-r{2:D2}' -f $Encoder, $CpuPercent, $repeat
        if ($hasReadbackPair) {
            $otherVariants = @($variants | Where-Object { $_ -notin @('no-comments', 'timeline-sync', 'timeline-pbo') })
            if (($repeat % 2) -eq 1) {
                $runVariants = @('no-comments', 'timeline-sync', 'timeline-pbo') + $otherVariants
            }
            else {
                $runVariants = @('no-comments', 'timeline-pbo', 'timeline-sync') + $otherVariants
            }
        }
        else {
            $runVariants = @()
            for ($offset = 0; $offset -lt $variants.Count; $offset++) {
                $runVariants += $variants[($offset + $repeat - 1) % $variants.Count]
            }
        }
        for ($offset = 0; $offset -lt $runVariants.Count; $offset++) {
            $variant = $runVariants[$offset]
            $runId = '{0}-{1}-{2}' -f $pairId, $variant, ([guid]::NewGuid().ToString('N').Substring(0, 6))
            $runDir = Join-Path $runRoot ('{0}-{1}' -f $pairId, $variant)
            New-Item -ItemType Directory -Path $runDir | Out-Null
            $runnerRecord = Join-Path $runDir 'runner.json'
            $env:NICO_TIMELINE_RUN_DIR = $runDir
            $env:NICO_TIMELINE_RUN_ID = $runId
            $env:NICO_TIMELINE_PAIR_ID = $pairId
            $env:NICO_TIMELINE_VARIANT = $variant
    $env:NICO_TIMELINE_ASSET_READBACK = if ($variant -eq 'timeline-pbo') { 'pbo' } else { 'sync' }
    $env:NICO_TIMELINE_ASSET_LAYOUT = if ($variant -eq 'timeline-atlas') { 'atlas' } else { 'separate' }
    $env:NICO_TIMELINE_READBACK_SLOTS = [string]$ReadbackSlots
            $env:IMAGEPAD_NICO_PERF_RUNNER_RECORD = if ($CpuPercent -eq 20) { $runnerRecord } else { '' }

            $stdout = Join-Path $runDir 'stdout.log'
            $stderr = Join-Path $runDir 'stderr.log'
            if ($CpuPercent -eq 20) {
                $argumentLine = @(
                    '--cpu-percent', '20', '--record', (Quote-NativeArgument $runnerRecord), '--close-stdin', '--',
                    (Quote-NativeArgument $videoTest), '-test.run=^TestNicoTimelinePerformance$', '-test.count=1', '-test.v'
                ) -join ' '
                $process = Start-Process -FilePath $budgetRunner -ArgumentList $argumentLine -WorkingDirectory $repoRoot `
                    -RedirectStandardOutput $stdout -RedirectStandardError $stderr -WindowStyle Hidden -Wait -PassThru
            } else {
                $process = Start-Process -FilePath $videoTest -ArgumentList @(
                    '-test.run=^TestNicoTimelinePerformance$', '-test.count=1', '-test.v'
                ) -WorkingDirectory $repoRoot -RedirectStandardOutput $stdout -RedirectStandardError $stderr `
                    -WindowStyle Hidden -Wait -PassThru
            }
            if ($process.ExitCode -ne 0) {
                $outText = if (Test-Path -LiteralPath $stdout) { Get-Content -LiteralPath $stdout -Raw } else { '' }
                $errText = if (Test-Path -LiteralPath $stderr) { Get-Content -LiteralPath $stderr -Raw } else { '' }
                throw "run failed: $runId (exit=$($process.ExitCode))`n$outText`n$errText"
            }
            if (-not (Test-Path -LiteralPath (Join-Path $runDir 'run.json') -PathType Leaf)) {
                throw "successful process did not write run.json: $runId"
            }
            Write-Host ("completed {0}/{1}: {2}" -f ((($repeat - 1) * $variants.Count) + $offset + 1), ($Repeats * $variants.Count), $runId)
        }
    }

    $summarize = Start-Process -FilePath $python -ArgumentList @(
        (Quote-NativeArgument $summarizer), '--root', (Quote-NativeArgument $runRoot),
        '--output', (Quote-NativeArgument $summaryPath), '--limit-seconds', '4.0'
    ) -WorkingDirectory $repoRoot -WindowStyle Hidden -Wait -PassThru
    if ($summarize.ExitCode -ne 0) { throw "summarizing benchmark results failed with exit code $($summarize.ExitCode)" }
    Write-Host "benchmark artifacts: $runRoot"
    Write-Host "summary: $summaryPath"
}
finally {
    foreach ($name in $envNames) {
        [Environment]::SetEnvironmentVariable($name, $oldEnv[$name], 'Process')
    }
}
