param(
    [string]$SdkRoot,
    [string]$OutputRoot = (Join-Path $PSScriptRoot 'build'),
    [string]$ResultPath = ''
)

$ErrorActionPreference = 'Stop'
$outputRoot = [IO.Path]::GetFullPath($OutputRoot)
if ([string]::IsNullOrWhiteSpace($ResultPath)) {
    $ResultPath = Join-Path $outputRoot 'build-result.json'
}
$resultPath = [IO.Path]::GetFullPath($ResultPath)

function Write-Result([string]$Status, [string]$Reason, [int]$ExitCode) {
    $parent = Split-Path -Parent $resultPath
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
    $result = [ordered]@{
        schema_version = 1
        experiment = 'nico-gpu-nvenc'
        status = $Status
        reason = $Reason
        unavailable_reasons = @($Reason)
        checks = @{}
        gates = @{
            d3d11_texture_registration = $false
            nvenc_acceptance = $false
            pool_progress = $false
            pool_capacity_exceeded_before_eos = $false
        }
        counters = @{
            pool_capacity = 0
            submitted_before_eos = 0
            accepted_frames = 0
            texture_registrations = 0
            eos_sent = $false
            eos_completed = $false
        }
        toolchain = @{}
        commands = @()
        metadata = @{}
    }
    [IO.File]::WriteAllText($resultPath, ($result | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
    exit $ExitCode
}

if ([string]::IsNullOrWhiteSpace($SdkRoot)) {
    Write-Result 'unavailable' 'nvenc_sdk_root_not_provided' 2
}
$sdkRoot = [IO.Path]::GetFullPath($SdkRoot)
$headerCandidates = @(
    (Join-Path $sdkRoot 'Interface/nvEncodeAPI.h'),
    (Join-Path $sdkRoot 'include/nvEncodeAPI.h'),
    (Join-Path $sdkRoot 'nvEncodeAPI.h')
)
$header = $headerCandidates | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
if (!$header) {
    Write-Result 'unavailable' 'nvenc_header_missing' 2
}

$systemRoot = $env:SystemRoot
if ([string]::IsNullOrWhiteSpace($systemRoot)) {
    $systemRoot = $env:WINDIR
}
$runtime = Join-Path $systemRoot 'System32/NvEncodeAPI64.dll'
if (!(Test-Path -LiteralPath $runtime -PathType Leaf)) {
    Write-Result 'unavailable' 'nvenc_runtime_missing' 2
}

$vswherePath = $null
$vswhereCommand = Get-Command vswhere.exe -ErrorAction SilentlyContinue
if ($vswhereCommand) {
    $vswherePath = $vswhereCommand.Source
}
if (!$vswherePath) {
    $candidate = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
    if (Test-Path -LiteralPath $candidate -PathType Leaf) {
        $vswherePath = $candidate
    }
}
if (!$vswherePath) {
    Write-Result 'unavailable' 'msvc_missing' 2
}
$vsPath = & $vswherePath -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (!$vsPath) {
    Write-Result 'unavailable' 'msvc_x64_tools_missing' 2
}
$vcvars = Join-Path $vsPath 'VC/Auxiliary/Build/vcvars64.bat'
if (!(Test-Path -LiteralPath $vcvars -PathType Leaf)) {
    Write-Result 'unavailable' 'vcvars64_missing' 2
}

New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
$source = Join-Path $PSScriptRoot 'main.cpp'
$exe = Join-Path $outputRoot 'nico-gpu-nvenc.exe'
$object = Join-Path $outputRoot 'main.obj'
$compileScript = Join-Path $outputRoot 'compile.cmd'
$includeRoot = Split-Path -Parent $header
$compileText = @"
@echo off
call "$vcvars" >nul
if errorlevel 1 exit /b 1
cl /nologo /std:c++17 /O2 /EHsc /MT /W4 /DWIN32_LEAN_AND_MEAN /DNOMINMAX /I"$includeRoot" "$source" /Fo"$object" /Fe"$exe" /link d3d11.lib dxgi.lib dxguid.lib ole32.lib
exit /b %errorlevel%
"@
[IO.File]::WriteAllText($compileScript, $compileText.Replace("`n", "`r`n"), [Text.UTF8Encoding]::new($false))
try {
    & $env:ComSpec /d /c $compileScript
    if ($LASTEXITCODE -ne 0 -or !(Test-Path -LiteralPath $exe -PathType Leaf)) {
        Write-Result 'failed' 'native_compile_failed' 1
    }
} finally {
    Remove-Item -LiteralPath $compileScript -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $object -Force -ErrorAction SilentlyContinue
}
Write-Output (ConvertTo-Json ([ordered]@{status='built'; executable=$exe; sdk_header=$header}) -Compress)
