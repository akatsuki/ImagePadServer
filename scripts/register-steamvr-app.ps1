$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$manifest = Join-Path $root "steamvr\imagepadserver.vrmanifest"
$exe = Join-Path $root "dist\imagepadserver-windows-amd64.exe"
$appConfig = "C:\Program Files (x86)\Steam\config\appconfig.json"

if (-not (Test-Path -LiteralPath $exe)) {
  throw "Executable not found: $exe. Build dist\imagepadserver-windows-amd64.exe first."
}
if (-not (Test-Path -LiteralPath $manifest)) {
  throw "SteamVR manifest not found: $manifest"
}
if (-not (Test-Path -LiteralPath $appConfig)) {
  throw "SteamVR app config not found: $appConfig"
}

$manifestPath = (Resolve-Path -LiteralPath $manifest).Path
$config = Get-Content -LiteralPath $appConfig -Raw | ConvertFrom-Json
if ($null -eq $config.manifest_paths) {
  $config | Add-Member -MemberType NoteProperty -Name manifest_paths -Value @()
}

$paths = @($config.manifest_paths)
if ($paths -notcontains $manifestPath) {
  $backup = "$appConfig.imagepadserver.bak"
  Copy-Item -LiteralPath $appConfig -Destination $backup -Force
  $config.manifest_paths = @($paths + $manifestPath)
  $config | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $appConfig -Encoding UTF8
  Write-Host "Registered SteamVR manifest: $manifestPath"
  Write-Host "Backup written: $backup"
} else {
  Write-Host "SteamVR manifest already registered: $manifestPath"
}

Write-Host "Restart SteamVR if ImagePadServer does not appear immediately."
