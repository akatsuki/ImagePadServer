param([string]$Sdk = 'build/nico-mask-probe/deps/ffmpeg-9.0.1-full_build-shared')
$ErrorActionPreference = 'Stop'
$sdkPath = (Resolve-Path -LiteralPath $Sdk).Path
$rootPath = (Resolve-Path -LiteralPath "$PSScriptRoot/../../..").Path
$outputPath = Join-Path $rootPath 'build/nico-mask-probe'
New-Item -ItemType Directory -Force -Path $outputPath | Out-Null
Set-Location -LiteralPath $rootPath
# GNU ld on Windows does not reliably accept non-ASCII absolute -L paths.
$sdkRelative = [IO.Path]::GetRelativePath($rootPath, $sdkPath)
$env:PATH = "C:/msys64/ucrt64/bin;$env:PATH"
$compiler = 'C:/msys64/ucrt64/bin/g++.exe'
& $compiler '-std=c++17' '-O3' '-DNDEBUG' '-Wall' '-Wextra' '-static-libgcc' '-static-libstdc++' "-I$sdkRelative/include" 'scripts/experiments/nico-mask-probe/main.cpp' "-L$sdkRelative/lib" '-lavformat' '-lavcodec' '-lavfilter' '-lavutil' '-lpsapi' '-o' 'build/nico-mask-probe/mask-probe.exe'
if ($LASTEXITCODE -ne 0) { throw 'probe build failed' }
& $compiler '-std=c++17' '-O2' '-Wall' '-Wextra' 'scripts/experiments/nico-mask-probe/selftest.cpp' '-o' 'build/nico-mask-probe/renderer-selftest.exe'
if ($LASTEXITCODE -ne 0) { throw 'renderer selftest build failed' }
Write-Output "Built $outputPath/mask-probe.exe"
