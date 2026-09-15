# Shared build/package boundary. Only the checked build script may create
# passing provenance, after the real encoder regression test has run.
function Assert-AirPlayH264BuildContract([string]$BridgeRoot) {
    $bridge = Join-Path $BridgeRoot 'airplay-gstreamer-bridge.exe'
    $proof = Join-Path $BridgeRoot 'imagepad-airplay-gstreamer-bridge-build.json'
    if (-not (Test-Path -LiteralPath $bridge -PathType Leaf) -or
        -not (Test-Path -LiteralPath $proof -PathType Leaf)) {
        throw 'H.264 package contract requires the executable and its tested build provenance'
    }
    try { $record = Get-Content -LiteralPath $proof -Raw | ConvertFrom-Json } catch {
        throw "H.264 build provenance is invalid JSON: $($_.Exception.Message)"
    }
    $contract = $record.videoContract
    if ($record.schema -ne 1 -or $record.configuration -ne 'Release' -or
        $record.executable -ne 'airplay-gstreamer-bridge.exe' -or
        $contract.id -ne 'rtsp-h264-single-slice-v1' -or
        $contract.slicesPerFrame -ne 1 -or
        -not ($contract.slicesPerFrame -is [int] -or $contract.slicesPerFrame -is [long]) -or
        $contract.testName -ne 'airplay_source_clock_single_slice' -or
        $contract.testPassed -isnot [bool] -or $contract.testPassed -ne $true) {
        throw 'H.264 single-slice contract is missing or unverified; rebuild with scripts/build-airplay-gstreamer-bridge.ps1'
    }
    $hash = (Get-FileHash -LiteralPath $bridge -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($record.executableSha256 -notmatch '^[0-9a-fA-F]{64}$' -or
        $record.executableSha256.ToLowerInvariant() -ne $hash) {
        throw 'H.264 build provenance executable hash mismatch'
    }
    return [pscustomobject]@{ executableSha256 = $hash; provenancePath = $proof }
}
