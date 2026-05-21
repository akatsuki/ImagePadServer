$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$manifest = Join-Path $root "steamvr\imagepadserver.vrmanifest"
$appConfig = "C:\Program Files (x86)\Steam\config\appconfig.json"

if (-not (Test-Path -LiteralPath $appConfig)) {
  throw "SteamVR app config not found: $appConfig"
}

$manifestPath = (Resolve-Path -LiteralPath $manifest).Path
$config = Get-Content -LiteralPath $appConfig -Raw | ConvertFrom-Json
$paths = @($config.manifest_paths) | Where-Object { $_ -ne $manifestPath }

$backup = "$appConfig.imagepadserver.bak"
Copy-Item -LiteralPath $appConfig -Destination $backup -Force
$config.manifest_paths = @($paths)
$config | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $appConfig -Encoding UTF8

Write-Host "Unregistered SteamVR manifest: $manifestPath"
Write-Host "Backup written: $backup"
Write-Host "Restart SteamVR if ImagePadServer still appears."
