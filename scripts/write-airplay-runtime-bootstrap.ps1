[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ArchivePath,
    [Parameter(Mandatory = $true)]
    [string]$RuntimeSetID,
    [Parameter(Mandatory = $true)]
    [string]$ArchiveURL,
    [Parameter(Mandatory = $true)]
    [string]$OutputPath
)

$ErrorActionPreference = "Stop"

function Get-Sha256Hex([byte[]]$Data) {
    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        return (($sha256.ComputeHash($Data) | ForEach-Object { $_.ToString("x2") }) -join "")
    } finally {
        $sha256.Dispose()
    }
}

function Read-ZipEntryBytes([IO.Compression.ZipArchiveEntry]$Entry) {
    if ($null -eq $Entry -or $Entry.Length -gt 4MB) {
        throw "runtime metadata entry is missing or too large"
    }
    $stream = $Entry.Open()
    $memory = [IO.MemoryStream]::new()
    try {
        $stream.CopyTo($memory)
        if ($memory.Length -ne $Entry.Length) {
            throw "runtime metadata entry size changed while reading"
        }
        return $memory.ToArray()
    } finally {
        $memory.Dispose()
        $stream.Dispose()
    }
}

function Write-AirPlayRuntimeBootstrap(
    [string]$ArchivePath,
    [string]$RuntimeSetID,
    [string]$ArchiveURL,
    [string]$OutputPath
) {
    $archive = (Resolve-Path -LiteralPath $ArchivePath).Path
    $id = $RuntimeSetID.Trim()
    if ($id -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') {
        throw "runtimeSetID is invalid"
    }
    $url = [Uri]$ArchiveURL
    if (-not $url.IsAbsoluteUri -or $url.Scheme -ne "https" -or [string]::IsNullOrWhiteSpace($url.Host)) {
        throw "runtime archive URL must be an HTTPS URL"
    }
    $archiveInfo = Get-Item -LiteralPath $archive
    if (-not $archiveInfo.PSIsContainer -and $archiveInfo.Length -gt 2GB) {
        throw "runtime archive exceeds the supported archive size"
    }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($archive)
    try {
        $licenseEntry = @($zip.Entries | Where-Object FullName -eq "license-manifest.json") | Select-Object -First 1
        $sourceEntry = @($zip.Entries | Where-Object FullName -eq "SOURCE-OFFER.md") | Select-Object -First 1
        if ($null -eq $licenseEntry -or $null -eq $sourceEntry) {
            throw "runtime archive is missing license-manifest.json or SOURCE-OFFER.md"
        }
        $licenseBytes = Read-ZipEntryBytes $licenseEntry
        $sourceBytes = Read-ZipEntryBytes $sourceEntry
        $licenseText = [Text.Encoding]::UTF8.GetString($licenseBytes).TrimStart([char]0xFEFF)
        $sourceText = [Text.Encoding]::UTF8.GetString($sourceBytes).TrimStart([char]0xFEFF)
        $license = ($licenseText | ConvertFrom-Json)
        if ($license.schema -ne 1 -or $license.runtimeSetID -ne $id) {
            throw "license manifest does not match runtimeSetID"
        }
        if (-not $sourceText.Contains($id)) {
            throw "SOURCE-OFFER.md does not name runtimeSetID"
        }
    } finally {
        $zip.Dispose()
    }

    $record = [ordered]@{
        schema = 1
        runtimeSetID = $id
        archiveUrl = $url.AbsoluteUri
        archiveSha256 = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
        archiveSize = [int64]$archiveInfo.Length
        licenseManifestSha256 = Get-Sha256Hex $licenseBytes
        sourceOfferSha256 = Get-Sha256Hex $sourceBytes
    }
    $destination = [IO.Path]::GetFullPath($OutputPath)
    $parent = Split-Path -Parent $destination
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
    $temporary = Join-Path $parent ("." + [IO.Path]::GetFileName($destination) + ".tmp-" + [Guid]::NewGuid().ToString("N"))
    try {
        $record | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $temporary -Encoding UTF8
        Move-Item -LiteralPath $temporary -Destination $destination -Force
    } finally {
        if (Test-Path -LiteralPath $temporary -PathType Leaf) {
            Remove-Item -LiteralPath $temporary -Force
        }
    }
    Write-Output $destination
}

Write-AirPlayRuntimeBootstrap -ArchivePath $ArchivePath -RuntimeSetID $RuntimeSetID -ArchiveURL $ArchiveURL -OutputPath $OutputPath
