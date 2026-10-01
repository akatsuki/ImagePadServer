[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$Snapshot,
    [Parameter(Mandatory = $true)][string]$FFmpeg,
    [Parameter(Mandatory = $true)][string]$Helper,
    [Parameter(Mandatory = $true)][string]$OutputRoot,
    [string]$Browser = '',
    [ValidateSet('x264', 'nvenc')][string]$Encoder = 'nvenc',
    [ValidateSet(0, 20)][int]$CpuPercent = 0,
    [ValidateSet('auto', 'dx12', 'vulkan', 'metal')][string]$GpuBackend = 'vulkan',
    [ValidateSet('pbo', 'atlas')][string]$Comparison = 'pbo',
    [ValidateRange(5, 100)][int]$Repeats = 5,
    [ValidateRange(64, 3840)][int]$Width = 1920,
    [ValidateRange(64, 2160)][int]$Height = 1080,
    [ValidateRange(1, 60000)][int]$FPSNum = 30,
    [ValidateRange(1, 1001)][int]$FPSDen = 1,
    [ValidateRange(1, 3600000)][int]$DurationMs = 6000
)

$ErrorActionPreference = 'Stop'
$benchmark = Join-Path $PSScriptRoot 'benchmark.ps1'
$comparisonVariants = if ($Comparison -eq 'atlas') {
    @('no-comments', 'timeline-separate', 'timeline-atlas')
} else {
    @('no-comments', 'timeline-sync', 'timeline-pbo')
}
$arguments = @{
    Source = $Source
    Snapshot = $Snapshot
    FFmpeg = $FFmpeg
    Helper = $Helper
    OutputRoot = $OutputRoot
    Encoder = $Encoder
    CpuPercent = $CpuPercent
    GpuBackend = $GpuBackend
    Repeats = $Repeats
    Width = $Width
    Height = $Height
    FPSNum = $FPSNum
    FPSDen = $FPSDen
    DurationMs = $DurationMs
    Variants = $comparisonVariants
}
if ($Comparison -eq 'atlas') {
    $arguments.ReadbackSlots = if ($Encoder -eq 'x264') { 2 } else { 3 }
}
if ($Browser -ne '') { $arguments.Browser = $Browser }
& $benchmark @arguments
