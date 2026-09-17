param([string]$OutputDirectory = '', [switch]$Stage)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
if (!$OutputDirectory) { $OutputDirectory = Join-Path $root 'build/nico-compositor' }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
[IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
$vs = & $vswhere -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (!$vs) { throw 'Visual Studio C++ Build Tools (x64) are required to build the Nico compositor' }
$vcvars = Join-Path $vs 'VC/Auxiliary/Build/vcvars64.bat'
$exe = Join-Path $OutputDirectory 'nico-compositor.exe'
$source = Join-Path $root 'native/nico-compositor/main.cpp'
$batch = Join-Path $OutputDirectory 'compile.cmd'
$batchText = @"
@echo off
chcp 65001 >nul
call "$vcvars" >nul
if errorlevel 1 exit /b 1
cl /nologo /std:c++17 /O2 /EHsc /MT /W4 /D_CRT_SECURE_NO_WARNINGS "$source" /Fo"$OutputDirectory/native.obj" /Fe"$exe" /link d3d11.lib d3dcompiler.lib dxgi.lib /DYNAMICBASE /NXCOMPAT
if errorlevel 1 exit /b 1
dumpbin /nologo /dependents "$exe" > "$OutputDirectory/dependencies.txt"
"@
[IO.File]::WriteAllText($batch, $batchText.Replace("`r`n","`n").Replace("`n","`r`n")+"`r`n", [Text.UTF8Encoding]::new($false))
if (Get-Command rtk -ErrorAction SilentlyContinue) {
    rtk proxy cmd /d /c $batch
} else {
    # CI builders need MSVC, not the developer's output-filter utility.
    & $env:ComSpec /d /c $batch
}
if ($LASTEXITCODE -ne 0) { throw 'Nico compositor compilation failed' }
$deps = Get-Content -LiteralPath (Join-Path $OutputDirectory 'dependencies.txt') -Raw
$dlls = [regex]::Matches($deps,'(?im)^\s+([A-Za-z0-9_.-]+\.dll)\s*$') | ForEach-Object { $_.Groups[1].Value }
$allowed = @('KERNEL32.dll','USER32.dll','ADVAPI32.dll','d3d11.dll','D3DCOMPILER_47.dll','dxgi.dll')
foreach ($dll in $dlls) { if ($dll -notin $allowed -and $dll -notlike 'api-ms-win-*.dll') { throw "Unexpected external dependency: $dll" } }
if (!$dlls) { throw 'Empty dependency inspection' }
$previousPath = $env:PATH
try {
    $env:PATH = "$env:SystemRoot/System32;$env:SystemRoot"
    $selftest = & $exe --self-test
    if ($LASTEXITCODE -ne 0 -or $selftest -ne 'NICO_COMPOSITOR 1 NPS3 WARP') { throw 'PATH-free compositor self-test failed' }
} finally { $env:PATH = $previousPath }
$manifest = [ordered]@{
    schema=1; protocol='NPS3'; architecture='amd64'; backend='WARP'; compiler='MSVC /MT';
    sha256=(Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant();
    sourceSha256=(Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToLowerInvariant();
    dependencies=@($dlls); selfTest='passed';
}
$manifestPath = Join-Path $OutputDirectory 'nico-compositor.json'
$manifest | ConvertTo-Json | Set-Content -LiteralPath $manifestPath -Encoding utf8NoBOM
if ($Stage) {
    $payload = Join-Path $root 'internal/nicorender/payload'
    [IO.Directory]::CreateDirectory($payload) | Out-Null
    Copy-Item -LiteralPath $exe -Destination (Join-Path $payload 'nico-compositor.exe')
    Copy-Item -LiteralPath $manifestPath -Destination (Join-Path $payload 'nico-compositor.json')
}
Write-Output "Built and verified: $exe"
