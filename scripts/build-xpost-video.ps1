$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$outputDirectory = Join-Path $repoRoot 'build/xpost-video'
$executable = Join-Path $outputDirectory 'imagepadserver.exe'
$fetcherSource = Join-Path $repoRoot 'internal/xpostimage'

& (Join-Path $PSScriptRoot 'build-xpost-compositor.ps1')
if (-not (Test-Path -LiteralPath (Join-Path $fetcherSource 'node_modules/react-tweet'))) {
    & npm ci --prefix $fetcherSource
    if ($LASTEXITCODE -ne 0) { throw 'X post fetcher dependency install failed' }
}
New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null
& npm run build:fetcher --prefix $fetcherSource
if ($LASTEXITCODE -ne 0) { throw 'X post embedded fetcher build failed' }
Push-Location $repoRoot
try {
    & go build '-ldflags=-H=windowsgui' -o $executable ./cmd/imagepadserver
    if ($LASTEXITCODE -ne 0) { throw 'X post application build failed' }
} finally { Pop-Location }
Copy-Item -LiteralPath (Join-Path $repoRoot 'build/xpost-compositord/xpost-compositord.exe') -Destination $outputDirectory -Force
Write-Output $executable
