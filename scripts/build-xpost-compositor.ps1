$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$manifestPath = Join-Path $repoRoot 'gpu/xpost-compositord/Cargo.toml'
$targetPath = Join-Path $repoRoot 'gpu/nico-compositord/target'
$outputDirectory = Join-Path $repoRoot 'build/xpost-compositord'
$outputPath = Join-Path $outputDirectory 'xpost-compositord.exe'
$previousTarget = $env:CARGO_TARGET_DIR

try {
    $env:CARGO_TARGET_DIR = $targetPath
    & cargo build --locked --manifest-path $manifestPath --release
    if ($LASTEXITCODE -ne 0) {
        throw "cargo build failed with exit code $LASTEXITCODE"
    }

    New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $targetPath 'release/xpost-compositord.exe') -Destination $outputPath -Force
    Write-Output $outputPath
} finally {
    if ($null -eq $previousTarget) {
        Remove-Item Env:CARGO_TARGET_DIR -ErrorAction SilentlyContinue
    } else {
        $env:CARGO_TARGET_DIR = $previousTarget
    }
}
