[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$PackageDirectory,
    [Parameter(Mandatory = $true)][string]$ReceiverBinary,
    [Parameter(Mandatory = $true)][string]$BridgeBinary
)
$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$root = (Resolve-Path -LiteralPath $PackageDirectory).Path
$manifest = Get-Content -LiteralPath (Join-Path $root "imagepad-airplay-source-clock-manifest.json") -Raw | ConvertFrom-Json
$capabilities = Get-Content -LiteralPath (Join-Path $root "uxplay-source-clock/imagepad-source-clock-capabilities.json") -Raw | ConvertFrom-Json
if ($capabilities.features -notcontains "video-bootstrap-reconnect") { throw "receiver lacks the reconnect bootstrap capability" }

# Check actual packaged bytes, not the packaging script's source text. Omitting
# either reconnect patch must fail even if the package's own manifest agrees.
$required = [ordered]@{
    "uxplay-source-clock/uxplay-source-clock.exe" = $ReceiverBinary
    "gstreamer/airplay-gstreamer-bridge.exe" = $BridgeBinary
    "uxplay-source-clock/patches/0012-imagepad-source-clock-video-bootstrap-cache.patch" = (Join-Path $repoRoot "third_party/uxplay-windows/patches/0012-imagepad-source-clock-video-bootstrap-cache.patch")
    "uxplay-source-clock/patches/0013-imagepad-source-clock-video-bootstrap-reconnect.patch" = (Join-Path $repoRoot "third_party/uxplay-windows/patches/0013-imagepad-source-clock-video-bootstrap-reconnect.patch")
}
foreach ($entry in $required.GetEnumerator()) {
    $path = Join-Path $root $entry.Key
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "required packaged artifact is missing: $($entry.Key)" }
    $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
    $expected = (Get-FileHash -LiteralPath $entry.Value -Algorithm SHA256).Hash
    $records = @($manifest.files | Where-Object path -eq $entry.Key)
    if ($actual -ne $expected -or $records.Count -ne 1 -or $records[0].sha256 -ne $actual) {
        throw "packaged artifact is stale or missing from the file manifest: $($entry.Key)"
    }
}
$packagedReceiverHash = (Get-FileHash -LiteralPath (Join-Path $root "uxplay-source-clock/uxplay-source-clock.exe") -Algorithm SHA256).Hash
if ($capabilities.binarySha256 -ne $packagedReceiverHash) { throw "receiver capability manifest hash mismatch" }
$smoke = Get-Content -LiteralPath (Join-Path $root "qualification/control-smoke-result.json") -Raw | ConvertFrom-Json
if ($smoke.status -ne "PASS" -or @($smoke.cases).Count -ne 2 -or
    @($smoke.cases | Where-Object { $_.status -ne "PASS" -or $_.completedIterations -ne 1 }).Count -ne 0) {
    throw "packaged control smoke is incomplete or failed"
}
[ordered]@{status="PASS"; layer="package artifacts and bounded control smoke"; productionReady=$false; artifactsChecked=$required.Count; package=$root} | ConvertTo-Json
