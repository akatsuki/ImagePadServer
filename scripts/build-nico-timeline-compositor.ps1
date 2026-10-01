param(
    [string]$Target = "",
    [string]$OutputDirectory = "",
    [ValidateSet("release", "release-thin", "release-thin-one")]
    [string]$Profile = "release",
    [switch]$ForGoEmbed
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($Target)) {
    $rustcVersion = & rustc -vV
    if ($LASTEXITCODE -ne 0) { throw "rustc -vV failed" }
    $hostLine = $rustcVersion | Where-Object { $_ -match '^host: ' } | Select-Object -First 1
    if (-not $hostLine) { throw "rustc did not report its host target" }
    $Target = ($hostLine -replace '^host: ', '').Trim()
}

if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $root "build\nico-timeline\artifacts\$Target"
} elseif (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $root $OutputDirectory
}
if ($ForGoEmbed) {
    $OutputDirectory = Join-Path $root "internal\nicorender\timeline_payload"
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$targetDir = Join-Path $root "build\nico-timeline\cargo"
$cargoArgs = @(
    "build", "--profile", $Profile, "--locked",
    "--manifest-path", (Join-Path $root "gpu\nico-compositord\Cargo.toml"),
    "--target-dir", $targetDir,
    "--target", $Target
)
Push-Location $root
try {
    & cargo @cargoArgs
    if ($LASTEXITCODE -ne 0) { throw "cargo build failed for $Target" }
} finally {
    Pop-Location
}

$cargoBinary = "nico-compositord"
if ($Target -match 'windows') { $cargoBinary += ".exe" }
$builtBinary = Join-Path $targetDir (Join-Path $Target (Join-Path $Profile $cargoBinary))
if (-not (Test-Path -LiteralPath $builtBinary -PathType Leaf)) {
    throw "built compositor is missing: $builtBinary"
}
[void](New-Item -ItemType Directory -Force -Path $OutputDirectory)
$payload = Join-Path $OutputDirectory "nico-compositord.bin"
$payloadTemp = $payload + ".tmp"
Copy-Item -LiteralPath $builtBinary -Destination $payloadTemp -Force
Move-Item -LiteralPath $payloadTemp -Destination $payload -Force

$manifest = Join-Path $OutputDirectory "manifest.json"
$manifestScript = Join-Path $root "scripts\nico_timeline_manifest.py"
$cargoManifest = Join-Path $root "gpu\nico-compositord\Cargo.toml"
Push-Location $root
try {
    & python $manifestScript --binary $payload --target $Target --output $manifest --cargo-manifest $cargoManifest --notices-output-directory $OutputDirectory
    if ($LASTEXITCODE -ne 0) { throw "timeline compositor manifest generation failed" }
} finally {
    Pop-Location
}
Write-Output (Join-Path $OutputDirectory "manifest.json")
