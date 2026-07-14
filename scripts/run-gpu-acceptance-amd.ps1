$env:IMAGEPAD_GPU_ADAPTER = "AMD"
$sidecar = Join-Path $PSScriptRoot "..\gpu\playlist-compositord\target\release\playlist-compositord.exe"
$evidence = Join-Path $PSScriptRoot "..\output\gpu-acceptance-amd-1800s.json"
& (Join-Path $PSScriptRoot "verify-gpu-acceptance.ps1") -Sidecar $sidecar -SoakSeconds 1800 -Evidence $evidence
