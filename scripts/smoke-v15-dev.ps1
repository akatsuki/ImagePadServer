param(
  [string]$Exe = "dist/1.5.0/dev/dev1/win/imagepadserver-v1.5.0-dev1-windows-amd64.exe",
  [int]$Port = 18081
)

$ErrorActionPreference = "Stop"
$src = (Resolve-Path $Exe).Path
$tempExe = Join-Path ([System.IO.Path]::GetTempPath()) ("imagepadserver-smoke-" + [guid]::NewGuid().ToString("N") + ".exe")
$dataDir = Join-Path ([System.IO.Path]::GetTempPath()) ("ImagePadServer-smoke-" + [guid]::NewGuid().ToString("N"))
$work = Join-Path ([System.IO.Path]::GetTempPath()) ("ImagePadServer-upload-" + [guid]::NewGuid().ToString("N"))
$oldEnv = @{
  IMAGEPAD_DATA_DIR = $env:IMAGEPAD_DATA_DIR
  IMAGEPAD_PORT = $env:IMAGEPAD_PORT
  IMAGEPAD_HOST = $env:IMAGEPAD_HOST
  IMAGEPAD_FFMPEG = $env:IMAGEPAD_FFMPEG
  IMAGEPAD_FFPROBE = $env:IMAGEPAD_FFPROBE
}

Copy-Item -LiteralPath $src -Destination $tempExe -Force
New-Item -ItemType Directory -Force -Path $work | Out-Null
$env:IMAGEPAD_DATA_DIR = $dataDir
$env:IMAGEPAD_PORT = [string]$Port
$env:IMAGEPAD_HOST = "127.0.0.1"
if ($ffmpeg = Get-Command ffmpeg -ErrorAction SilentlyContinue) { $env:IMAGEPAD_FFMPEG = $ffmpeg.Source }
if ($ffprobe = Get-Command ffprobe -ErrorAction SilentlyContinue) { $env:IMAGEPAD_FFPROBE = $ffprobe.Source }

$process = Start-Process -FilePath $tempExe -PassThru -WindowStyle Hidden
try {
  $base = "http://127.0.0.1:$Port/"
  $ready = $false
  for ($i = 0; $i -lt 40; $i++) {
    try {
      if ((Invoke-WebRequest -UseBasicParsing -Uri ($base + "healthz") -TimeoutSec 2).StatusCode -eq 200) {
        $ready = $true
        break
      }
    } catch {}
    Start-Sleep -Milliseconds 500
  }
  if (-not $ready) { throw "healthz did not respond" }

  $html = (Invoke-WebRequest -UseBasicParsing -Uri $base -TimeoutSec 5).Content
  if ($html -notmatch "themeSelect" -or $html -notmatch "data-theme") {
    throw "theme controls missing from HTML"
  }

  $png = Join-Path $work "tiny.png"
  Add-Type -AssemblyName System.Drawing
  $bitmap = New-Object System.Drawing.Bitmap 2, 2
  $bitmap.SetPixel(0, 0, [System.Drawing.Color]::FromArgb(255, 40, 80, 160))
  $bitmap.SetPixel(1, 0, [System.Drawing.Color]::FromArgb(255, 240, 240, 240))
  $bitmap.SetPixel(0, 1, [System.Drawing.Color]::FromArgb(255, 20, 20, 20))
  $bitmap.SetPixel(1, 1, [System.Drawing.Color]::FromArgb(255, 200, 80, 40))
  $bitmap.Save($png, [System.Drawing.Imaging.ImageFormat]::Png)
  $bitmap.Dispose()
  $uploadRaw = & curl.exe -sS -F "image=@$png;type=image/png" -F "format=webp" -F "quality=balanced" -F "maxDimension=source" -F "maxMB=none" ($base + "api/upload")
  if ($LASTEXITCODE -ne 0) { throw "curl upload failed" }
  try {
    $upload = $uploadRaw | ConvertFrom-Json
  } catch {
    throw "upload did not return JSON: $uploadRaw"
  }
  $currentID = [string]$upload.current.id
  if (-not $currentID) { throw "upload did not return a current id" }

  $image = Invoke-WebRequest -UseBasicParsing -Uri ($base + "image/current") -TimeoutSec 5
  if ($image.Headers["Content-Type"] -notmatch "image/webp") {
    throw "current image content type was $($image.Headers["Content-Type"])"
  }

  Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($base + "api/history/favorite") -ContentType "application/json" -Body (@{ id = $currentID; favorite = $true } | ConvertTo-Json -Compress) -TimeoutSec 5 | Out-Null
  Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($base + "api/history/select") -ContentType "application/json" -Body (@{ id = $currentID } | ConvertTo-Json -Compress) -TimeoutSec 5 | Out-Null
  & curl.exe -sS -F "image=@$png;type=image/png" -F "format=webp" -F "quality=balanced" -F "maxDimension=source" -F "maxMB=none" ($base + "api/upload-queue") | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "curl queue failed" }
  Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($base + "api/video-player") -ContentType "application/json" -Body (@{ enabled = $false } | ConvertTo-Json -Compress) -TimeoutSec 5 | Out-Null
  Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($base + "api/obs/latency") -ContentType "application/json" -Body (@{ mode = "hls"; dvr = $false } | ConvertTo-Json -Compress) -TimeoutSec 5 | Out-Null

  $state = (Invoke-WebRequest -UseBasicParsing -Uri ($base + "api/state") -TimeoutSec 5).Content | ConvertFrom-Json
  if ($state.version -ne "v1.5.0-dev1") { throw "state version was $($state.version)" }
  if (-not $state.history -or $state.history.Count -lt 1) { throw "history missing" }

  try { Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($base + "api/quit") -TimeoutSec 3 | Out-Null } catch {}
  Write-Output "ui/http smoke ok current=$currentID contentType=$($image.Headers["Content-Type"]) history=$($state.history.Count)"
} finally {
  if ($process -and -not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
  Remove-Item -LiteralPath $tempExe -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $dataDir -Recurse -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
  foreach ($entry in $oldEnv.GetEnumerator()) {
    if ($null -eq $entry.Value) {
      Remove-Item "Env:$($entry.Key)" -ErrorAction SilentlyContinue
    } else {
      Set-Item "Env:$($entry.Key)" $entry.Value
    }
  }
}
