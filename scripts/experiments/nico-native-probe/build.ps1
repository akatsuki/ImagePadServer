param()
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath "$PSScriptRoot/../../..").Path
$output = Join-Path $root 'build/nico-native-probe'
[IO.Directory]::CreateDirectory($output) | Out-Null
$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
$vs = & $vswhere -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (!$vs) { throw 'Visual Studio C++ Build Tools (x64) are required' }
$vcvars = Join-Path $vs 'VC/Auxiliary/Build/vcvars64.bat'
$probeSource = Join-Path $root 'scripts/experiments/nico-native-probe/main.cpp'
$failureSource = Join-Path $root 'scripts/experiments/nico-native-probe/failing_compositor.cpp'
$baselineSource = Join-Path $root 'native/nico-compositor/main.cpp'
$probeExe = Join-Path $output 'nico-native-probe.exe'
$failureExe = Join-Path $output 'nico-native-failing-compositor.exe'
$baselineExe = Join-Path $output 'baseline.exe'
$batch = Join-Path $output 'compile.cmd'
$text = @"
@echo off
chcp 65001 >nul
call "$vcvars" >nul
if errorlevel 1 exit /b 1
cl /nologo /std:c++17 /O2 /EHsc /MT /W4 /D_CRT_SECURE_NO_WARNINGS "$probeSource" /Fo"$output/probe.obj" /Fe"$probeExe" /link d3d11.lib d3dcompiler.lib dxgi.lib /DYNAMICBASE /NXCOMPAT
if errorlevel 1 exit /b 1
cl /nologo /std:c++17 /O2 /EHsc /MT /W4 /D_CRT_SECURE_NO_WARNINGS "$failureSource" /Fo"$output/failure.obj" /Fe"$failureExe" /link /DYNAMICBASE /NXCOMPAT
if errorlevel 1 exit /b 1
cl /nologo /std:c++17 /O2 /EHsc /MT /W4 /D_CRT_SECURE_NO_WARNINGS "$baselineSource" /Fo"$output/baseline.obj" /Fe"$baselineExe" /link d3d11.lib d3dcompiler.lib dxgi.lib /DYNAMICBASE /NXCOMPAT
if errorlevel 1 exit /b 1
dumpbin /nologo /dependents "$probeExe" > "$output/probe-dependencies.txt"
dumpbin /nologo /dependents "$failureExe" > "$output/failure-dependencies.txt"
dumpbin /nologo /dependents "$baselineExe" > "$output/baseline-dependencies.txt"
"@
[IO.File]::WriteAllText($batch, $text.Replace("`r`n","`n").Replace("`n","`r`n")+"`r`n", [Text.UTF8Encoding]::new($false))
if (Get-Command rtk -ErrorAction SilentlyContinue) { rtk proxy cmd /d /c $batch } else { & $env:ComSpec /d /c $batch }
if ($LASTEXITCODE -ne 0) { throw 'native probe compilation failed' }
$pathBefore = $env:PATH
try {
  $env:PATH = "$env:SystemRoot/System32;$env:SystemRoot"
  $probeSelf = & $probeExe --self-test
  if ($LASTEXITCODE -ne 0 -or $probeSelf -ne 'NICO_COMPOSITOR 1 NPS3 WARP') { throw 'probe WARP self-test failed' }
  $failureSelf = & $failureExe --self-test
  if ($LASTEXITCODE -ne 0 -or $failureSelf -ne 'NICO_COMPOSITOR 1 NPS3 WARP') { throw 'failure compositor self-test failed' }
  $baseSelf = & $baselineExe --self-test
  if ($LASTEXITCODE -ne 0 -or $baseSelf -ne 'NICO_COMPOSITOR 1 NPS3 WARP') { throw 'baseline WARP self-test failed' }
} finally { $env:PATH = $pathBefore }
Write-Output "Built and verified: $probeExe, $failureExe, $baselineExe"
