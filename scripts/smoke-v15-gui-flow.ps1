param(
  [string]$Base = "http://127.0.0.1:18083/",
  [string]$RemoteImageURL = "https://www.google.com/images/branding/googlelogo/1x/googlelogo_color_272x92dp.png"
)

$ErrorActionPreference = "Stop"
$work = Join-Path ([System.IO.Path]::GetTempPath()) ("imagepad-final-flow-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $work | Out-Null

try {
  Add-Type -AssemblyName System.Drawing
  $png = Join-Path $work "flow.png"
  $bitmap = New-Object System.Drawing.Bitmap 32, 32
  for ($y = 0; $y -lt 32; $y++) {
    for ($x = 0; $x -lt 32; $x++) {
      $bitmap.SetPixel($x, $y, [System.Drawing.Color]::FromArgb(255, ($x * 7) % 256, ($y * 7) % 256, (($x + $y) * 4) % 256))
    }
  }
  $bitmap.Save($png, [System.Drawing.Imaging.ImageFormat]::Png)
  $bitmap.Dispose()

  $uploadRaw = & curl.exe -sS -F "image=@$png;type=image/png" -F "format=webp" -F "quality=high" ($Base + "api/upload")
  if ($LASTEXITCODE -ne 0) { throw "upload failed" }
  $state = $uploadRaw | ConvertFrom-Json
  $id = [string]$state.current.id
  if (-not $id) { throw "upload returned no current id" }

  Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($Base + "api/history/favorite") -ContentType "application/json" -Body (@{ id = $id; favorite = $true } | ConvertTo-Json -Compress) -TimeoutSec 5 | Out-Null
  Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($Base + "api/video-player") -ContentType "application/json" -Body (@{ enabled = $false } | ConvertTo-Json -Compress) -TimeoutSec 10 | Out-Null

  $stateAfterFavorite = (Invoke-WebRequest -UseBasicParsing -Uri ($Base + "api/state") -TimeoutSec 5).Content | ConvertFrom-Json
  $imageURLForURLFlow = $RemoteImageURL
  if ($stateAfterFavorite.tunnel -and $stateAfterFavorite.tunnel.ok -and $stateAfterFavorite.tunnel.url) {
    $imageURLForURLFlow = ([string]$stateAfterFavorite.tunnel.url).TrimEnd("/") + "/image/current"
  }
  $urlBody = @{
    url = $imageURLForURLFlow
    format = "webp"
    quality = "high"
    maxDimension = "2048"
    maxMB = "30"
  } | ConvertTo-Json -Compress
  Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($Base + "api/upload-url") -ContentType "application/json" -Body $urlBody -TimeoutSec 30 | Out-Null

  Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($Base + "api/video-player") -ContentType "application/json" -Body (@{ enabled = $true } | ConvertTo-Json -Compress) -TimeoutSec 10 | Out-Null

  $queueRaw = & curl.exe -sS -F "image=@$png;type=image/png" -F "format=webp" -F "quality=high" ($Base + "api/upload-queue")
  if ($LASTEXITCODE -ne 0) { throw "queue failed" }
  Invoke-WebRequest -UseBasicParsing -Method Post -Uri ($Base + "api/obs/latency") -ContentType "application/json" -Body (@{ mode = "lhls"; dvr = $true } | ConvertTo-Json -Compress) -TimeoutSec 30 | Out-Null

  $final = (Invoke-WebRequest -UseBasicParsing -Uri ($Base + "api/state") -TimeoutSec 5).Content | ConvertFrom-Json
  if (-not $final.current.id) { throw "state has no current media" }
  if (-not $final.history -or $final.history.Count -lt 1) { throw "state has no history" }
  if (-not $final.videoPlayer.enabled) { throw "video player did not stay enabled" }
  if ($final.obs.latency.mode -ne "rtsp-low") { throw "OBS latency did not update to rtsp-low" }
  $favoriteCount = @($final.history | Where-Object { $_.favorite }).Count
  if ($favoriteCount -lt 1) { throw "state has no favorite item" }

  Write-Output ("gui flow seed ok current={0} history={1} queue={2} favorites={3} obs={4}" -f $final.current.id, $final.history.Count, $final.videoQueue.Count, $favoriteCount, $final.obs.latency.mode)
} finally {
  Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
