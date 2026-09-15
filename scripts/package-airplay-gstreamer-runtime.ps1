param(
    [Parameter(Mandatory = $true)]
    [string]$GStreamerRuntimeRoot,
    [Parameter(Mandatory = $true)]
    [string]$BridgeBuildDirectory,
    [Parameter(Mandatory = $true)]
    [string]$OutputDirectory,
    [string]$Version = "1.26.11"
)

$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot 'airplay-h264-contract.ps1')
$runtimeRoot = (Resolve-Path -LiteralPath $GStreamerRuntimeRoot).Path
$bridgeRoot = (Resolve-Path -LiteralPath $BridgeBuildDirectory).Path
$outputRoot = [IO.Path]::GetFullPath($OutputDirectory)
$h264Build = Assert-AirPlayH264BuildContract -BridgeRoot $bridgeRoot

if (-not (Test-Path -LiteralPath (Join-Path $bridgeRoot "airplay-gstreamer-bridge.exe"))) {
    throw "airplay-gstreamer-bridge.exe was not found under $bridgeRoot"
}
if (-not (Test-Path -LiteralPath (Join-Path $runtimeRoot "bin"))) {
    throw "GStreamer runtime bin directory was not found under $runtimeRoot"
}
if (-not (Test-Path -LiteralPath (Join-Path $runtimeRoot "lib\gstreamer-1.0"))) {
    throw "GStreamer runtime plugin directory was not found under $runtimeRoot"
}

New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
Copy-Item -LiteralPath (Join-Path $bridgeRoot "airplay-gstreamer-bridge.exe") -Destination $outputRoot -Force
Copy-Item -LiteralPath $h264Build.provenancePath -Destination $outputRoot -Force
$packagedH264 = Assert-AirPlayH264BuildContract -BridgeRoot $outputRoot
if ($packagedH264.executableSha256 -ne $h264Build.executableSha256) {
    throw 'H.264 packaged executable differs from the tested source build'
}
if (Test-Path -LiteralPath (Join-Path $bridgeRoot "airplay-gstreamer-capability-probe.exe")) {
    Copy-Item -LiteralPath (Join-Path $bridgeRoot "airplay-gstreamer-capability-probe.exe") -Destination $outputRoot -Force
}

# Keep the runtime self-contained. The official Windows runtime layout puts
# import DLLs in bin and plugins under lib; copying the complete runtime avoids
# an accidental dependency on a developer machine's PATH.
foreach ($directory in @("bin", "etc", "lib", "libexec", "share")) {
    $source = Join-Path $runtimeRoot $directory
    if (Test-Path -LiteralPath $source) {
        Copy-Item -LiteralPath $source -Destination $outputRoot -Recurse -Force
    }
}

# The development bundle carries large static archives and build metadata
# beside the DLL runtime. They are not loaded by the bridge and would make an
# embedded EXE unnecessarily large, so retain only redistributable runtime
# files and notices in the staged package.
$developmentExtensions = @(".a", ".lib", ".def", ".h", ".cmake", ".pc", ".py", ".pyi")
Get-ChildItem -LiteralPath $outputRoot -Recurse -File |
    Where-Object { $developmentExtensions -contains $_.Extension.ToLowerInvariant() } |
    Remove-Item -Force

$files = @(
    Get-ChildItem -LiteralPath $outputRoot -Recurse -File |
        Where-Object { $_.Name -notin @("airplay-gstreamer-manifest.json") } |
        ForEach-Object {
            $relative = [IO.Path]::GetRelativePath($outputRoot, $_.FullName).Replace("\", "/")
            $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            [ordered]@{ path = $relative; size = $_.Length; sha256 = $hash }
        }
)
$manifest = [ordered]@{
    schema = 1
    version = $Version
    architecture = "windows-amd64"
    files = $files
}
$manifest | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $outputRoot "airplay-gstreamer-manifest.json") -Encoding UTF8
Write-Host "Packaged AirPlay GStreamer runtime: $outputRoot ($($files.Count) files)"
