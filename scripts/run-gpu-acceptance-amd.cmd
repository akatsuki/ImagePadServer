@echo off
setlocal
set IMAGEPAD_GPU_ADAPTER=AMD
cd /d "%~dp0.."
pwsh.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0verify-gpu-acceptance.ps1" -Sidecar "%~dp0..\gpu\playlist-compositord\target\release\playlist-compositord.exe" -SoakSeconds 1800 -Evidence "%~dp0..\output\gpu-acceptance-amd-1800s.json"
endlocal
