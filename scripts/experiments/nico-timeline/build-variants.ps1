param(
    [string]$Mode = "",
    [string]$Target = "",
    [string]$RunRoot = "",
    [string]$PgoRunRoot = "",
    [switch]$ValidateOnly
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($Mode)) {
    throw "Mode is required. Use release, release-thin, release-thin-one, or one of those modes with -pgo."
}
$baseModes = @("release", "release-thin", "release-thin-one")
$supportedModes = @($baseModes) + @($baseModes | ForEach-Object { "$_-pgo" })
if ($Mode -notin $supportedModes) {
    throw "Unsupported Mode '$Mode'. Use release, release-thin, release-thin-one, or one of those modes with -pgo."
}
$pgoBuild = $Mode.EndsWith("-pgo", [System.StringComparison]::Ordinal)
$cargoMode = if ($pgoBuild) { $Mode.Substring(0, $Mode.Length - 4) } else { $Mode }
if ($pgoBuild -and [string]::IsNullOrWhiteSpace($PgoRunRoot)) {
    throw "PgoRunRoot is required for a -pgo build."
}
if (-not $pgoBuild -and -not [string]::IsNullOrWhiteSpace($PgoRunRoot)) {
    throw "PgoRunRoot is only valid with a -pgo build."
}

$root = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $PSScriptRoot))
$temporaryRoot = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd([System.IO.Path]::DirectorySeparatorChar)
if ([string]::IsNullOrWhiteSpace($RunRoot)) {
    $RunRoot = Join-Path $temporaryRoot ("nct-next-speed-" + [guid]::NewGuid().ToString("N"))
}
$RunRoot = [System.IO.Path]::GetFullPath($RunRoot)
$requiredPrefix = $temporaryRoot + [System.IO.Path]::DirectorySeparatorChar
if (-not $RunRoot.StartsWith($requiredPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "RunRoot must be below the current user's TEMP directory."
}

function Get-NicoBuildTreeInventory([string]$Path, [string]$OwnedRoot) {
    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $rootFull = [System.IO.Path]::GetFullPath($OwnedRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar)
    $rootPrefix = $rootFull + [System.IO.Path]::DirectorySeparatorChar
    if ($fullPath -ne $rootFull -and
        -not $fullPath.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Build inventory path escaped its owned root: $fullPath"
    }
    $rootItem = Get-Item -LiteralPath $fullPath -Force -ErrorAction Stop
    if (-not $rootItem.PSIsContainer -or
        (($rootItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0)) {
        throw "Build inventory root is not a regular directory: $fullPath"
    }
    $entries = @(Get-ChildItem -LiteralPath $fullPath -Force -Recurse)
    foreach ($entry in $entries) {
        if (($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Build inventory contains a reparse point: $($entry.FullName)"
        }
    }
    $files = @($entries | Where-Object { -not $_.PSIsContainer } | Sort-Object FullName)
    $rows = [System.Collections.Generic.List[string]]::new()
    [long]$totalBytes = 0
    foreach ($file in $files) {
        $relative = [System.IO.Path]::GetRelativePath($fullPath, $file.FullName).Replace([string][System.IO.Path]::DirectorySeparatorChar, '/')
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $file.FullName).Hash.ToLowerInvariant()
        $totalBytes += [long]$file.Length
        $rows.Add("$relative`t$($file.Length)`t$hash")
    }
    $treePayload = [string]::Join("`n", $rows) + "`n"
    $treeHash = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData([System.Text.Encoding]::UTF8.GetBytes($treePayload))).ToLowerInvariant()
    return [ordered]@{ files = $files.Count; bytes = $totalBytes; treeSha256 = $treeHash }
}

function Save-NicoBuildOwner([string]$Path, [object]$Value) {
    $temporary = $Path + ".tmp"
    $Value | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $temporary -Encoding utf8NoBOM
    Move-Item -LiteralPath $temporary -Destination $Path -Force
}

function ConvertTo-NicoCanonicalValue($Value) {
    if ($null -eq $Value) { return $null }
    if ($Value -is [System.Collections.IDictionary]) {
        $sorted = [ordered]@{}
        $keys = [string[]]@($Value.Keys)
        [Array]::Sort($keys, [System.StringComparer]::Ordinal)
        foreach ($key in $keys) { $sorted[$key] = ConvertTo-NicoCanonicalValue $Value[$key] }
        return ,$sorted
    }
    if ($Value -is [array]) {
        $items = @()
        foreach ($item in $Value) { $items += ,(ConvertTo-NicoCanonicalValue $item) }
        return ,$items
    }
    return $Value
}

function Get-NicoCanonicalSha256($Value) {
    $json = ConvertTo-Json -InputObject (ConvertTo-NicoCanonicalValue $Value) -Depth 100 -Compress
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Get-NicoSourceSnapshotSha256([string]$Path) {
    $records = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json -AsHashtable -Depth 100
    if ($records -isnot [array] -or $records.Count -eq 0) { throw 'Build source snapshot must be a non-empty JSON array.' }
    $paths = [string[]]@($records | ForEach-Object { [string]$_.path })
    [Array]::Sort($paths, [System.StringComparer]::Ordinal)
    $byPath = @{}
    foreach ($record in $records) {
        if (-not $record.Contains('path') -or -not $record.Contains('sha256') -or
            [string]$record.sha256 -cnotmatch '^[0-9a-f]{64}$') {
            throw 'Build source snapshot contains an invalid path or SHA-256 record.'
        }
        $byPath[[string]$record.path] = [string]$record.sha256
    }
    $builder = [System.Text.StringBuilder]::new()
    foreach ($path in $paths) { [void]$builder.Append($path).Append([char]0).Append($byPath[$path]).Append("`n") }
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($builder.ToString())
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Resolve-NicoPgoFile([string]$Path, [string]$Description, [string]$OwnedRoot) {
    if ([string]::IsNullOrWhiteSpace($Path) -or -not [System.IO.Path]::IsPathRooted($Path)) {
        throw "$Description path must be absolute."
    }
    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $rootFull = [System.IO.Path]::GetFullPath($OwnedRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $prefix = $rootFull + [System.IO.Path]::DirectorySeparatorChar
    if (-not $fullPath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Description escaped its PGO-owned root: $fullPath"
    }
    $driveRoot = [System.IO.Path]::GetPathRoot($fullPath)
    $relative = $fullPath.Substring($driveRoot.Length)
    $parts = @($relative -split '[\\/]') | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    $cursor = $driveRoot
    for ($index = 0; $index -lt $parts.Count; $index++) {
        $cursor = Join-Path $cursor $parts[$index]
        if (-not (Test-Path -LiteralPath $cursor)) { throw "$Description does not exist: $fullPath" }
        $item = Get-Item -LiteralPath $cursor -Force -ErrorAction Stop
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "$Description path traverses a reparse point: $cursor"
        }
        if ($index -lt ($parts.Count - 1) -and -not $item.PSIsContainer) {
            throw "$Description parent is not a directory: $cursor"
        }
    }
    $leaf = Get-Item -LiteralPath $fullPath -Force -ErrorAction Stop
    if ($leaf.PSIsContainer) { throw "$Description must be a regular file: $fullPath" }
    return $fullPath
}

function Read-NicoPgoJson([string]$Path, [string]$Description, [long]$MaximumBytes = 8MB) {
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if ($item.Length -gt $MaximumBytes) { throw "$Description exceeds $MaximumBytes bytes." }
    try {
        $value = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json -AsHashtable -Depth 100
    } catch {
        throw "$Description is not valid JSON: $($_.Exception.Message)"
    }
    if ($value -isnot [System.Collections.IDictionary]) { throw "$Description must be a JSON object." }
    return $value
}

function Assert-NicoPgoString([string]$Actual, [string]$Expected, [string]$Description) {
    if (-not [string]::Equals($Actual, $Expected, [System.StringComparison]::Ordinal)) {
        throw "$Description does not match the selected PGO training run."
    }
}

function Resolve-NicoPgoTraining([string]$PgoRoot, [string]$BaseMode, [string]$BuildTarget) {
    $temporaryRoot = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    if (-not [System.IO.Path]::IsPathRooted($PgoRoot)) { throw 'PgoRunRoot must be an absolute path.' }
    $fullRoot = [System.IO.Path]::GetFullPath($PgoRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $tempPrefix = $temporaryRoot + [System.IO.Path]::DirectorySeparatorChar
    if (-not $fullRoot.StartsWith($tempPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'PgoRunRoot must be below the current user TEMP directory.'
    }
    $rootItem = Get-Item -LiteralPath $fullRoot -Force -ErrorAction Stop
    if (-not $rootItem.PSIsContainer -or (($rootItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0)) {
        throw 'PgoRunRoot must be an existing regular directory.'
    }
    foreach ($manifestName in @('pgo-owner.json', 'pgo-result.json')) {
        [void](Resolve-NicoPgoFile (Join-Path $fullRoot $manifestName) "PGO $manifestName" $fullRoot)
    }
    $owner = Read-NicoPgoJson (Join-Path $fullRoot 'pgo-owner.json') 'PGO owner manifest'
    $result = Read-NicoPgoJson (Join-Path $fullRoot 'pgo-result.json') 'PGO result manifest'
    if ($owner.schema -ne 1 -or $owner.kind -cne 'nico-pgo-training' -or $owner.status -cne 'complete' -or $owner.stage -cne 'complete') {
        throw 'PGO owner manifest is not a completed supported training run.'
    }
    if ($result.schema -ne 1 -or $result.kind -cne 'nico-pgo-training-result') {
        throw 'PGO result manifest has an unsupported schema or kind.'
    }
    Assert-NicoPgoString ([System.IO.Path]::GetFullPath([string]$owner.runRoot)) $fullRoot 'PGO owner RunRoot'
    Assert-NicoPgoString ([System.IO.Path]::GetFullPath([string]$result.runRoot)) $fullRoot 'PGO result RunRoot'
    Assert-NicoPgoString ([string]$owner.mode) $BaseMode 'PGO build mode'
    Assert-NicoPgoString ([string]$result.mode) $BaseMode 'PGO result mode'
    Assert-NicoPgoString ([string]$owner.target) $BuildTarget 'PGO training target'
    Assert-NicoPgoString ([string]$result.target) $BuildTarget 'PGO result target'
    foreach ($stageName in @('cargo', 'training', 'merge')) {
        if (-not $owner.stages.Contains($stageName) -or $owner.stages[$stageName].exitCode -ne 0 -or
            -not $result.stages.Contains($stageName) -or $result.stages[$stageName].exitCode -ne 0) {
            throw "PGO stage '$stageName' did not complete successfully."
        }
    }

    $trainingPath = Join-Path $fullRoot 'training.json'
    $evaluationPath = Join-Path $fullRoot 'evaluation.json'
    foreach ($entry in @(
        @{ Path = $trainingPath; Value = $owner.trainingManifestPath },
        @{ Path = $trainingPath; Value = $result.trainingManifestPath },
        @{ Path = $evaluationPath; Value = $owner.evaluationManifestPath },
        @{ Path = $evaluationPath; Value = $result.evaluationManifestPath }
    )) {
        Assert-NicoPgoString ([System.IO.Path]::GetFullPath([string]$entry.Value)) $entry.Path 'PGO manifest path'
    }
    [void](Resolve-NicoPgoFile $trainingPath 'PGO training manifest' $fullRoot)
    [void](Resolve-NicoPgoFile $evaluationPath 'PGO evaluation manifest' $fullRoot)
    $training = Read-NicoPgoJson $trainingPath 'PGO training manifest' 1MB
    $trainingHash = Get-NicoCanonicalSha256 $training
    Assert-NicoPgoString ([string]$owner.trainingManifestSha256) $trainingHash 'PGO owner training manifest hash'
    Assert-NicoPgoString ([string]$result.trainingManifestSha256) $trainingHash 'PGO result training manifest hash'

    $profilePath = Join-Path $fullRoot 'profile.profdata'
    [void](Resolve-NicoPgoFile $profilePath 'Merged LLVM profile' $fullRoot)
    $profileItem = Get-Item -LiteralPath $profilePath -Force
    $profileHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $profilePath).Hash.ToLowerInvariant()
    foreach ($record in @($owner.profile, $result.profile)) {
        if ($null -eq $record -or $record.instrumented -ne $false -or [long]$record.bytes -ne [long]$profileItem.Length) {
            throw 'Merged LLVM profile metadata is missing, instrumented, or has the wrong size.'
        }
        Assert-NicoPgoString ([System.IO.Path]::GetFullPath([string]$record.path)) $profilePath 'Merged LLVM profile path'
        Assert-NicoPgoString ([string]$record.sha256) $profileHash 'Merged LLVM profile hash'
    }

    $instrumentedPath = Join-Path $fullRoot 'instrumented/nico-compositord.exe'
    [void](Resolve-NicoPgoFile $instrumentedPath 'Instrumented compositor helper' $fullRoot)
    $instrumentedHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $instrumentedPath).Hash.ToLowerInvariant()
    foreach ($record in @($owner.instrumentedHelper, $result.instrumentedHelper)) {
        if ($null -eq $record -or $record.instrumented -ne $true) { throw 'PGO instrumented helper metadata is missing.' }
        Assert-NicoPgoString ([System.IO.Path]::GetFullPath([string]$record.path)) $instrumentedPath 'Instrumented helper path'
        Assert-NicoPgoString ([string]$record.sha256) $instrumentedHash 'Instrumented helper hash'
        if ([long]$record.bytes -ne (Get-Item -LiteralPath $instrumentedPath).Length) { throw 'Instrumented helper size does not match its metadata.' }
    }

    $identity = $result.buildIdentity
    $ownerIdentity = $owner.buildIdentity
    if ($identity -isnot [System.Collections.IDictionary] -or $ownerIdentity -isnot [System.Collections.IDictionary]) {
        throw 'PGO training build identity is missing.'
    }
    foreach ($field in @('buildSourceSnapshotSha256', 'rustcVersion', 'cargoVersion', 'llvmVersion', 'target')) {
        Assert-NicoPgoString ([string]$identity[$field]) ([string]$ownerIdentity[$field]) "PGO build identity.$field"
    }
    Assert-NicoPgoString ([string]$training.buildIdentity.buildSourceSnapshotSha256) ([string]$identity.buildSourceSnapshotSha256) 'Training manifest source snapshot'
    Assert-NicoPgoString ([string]$training.buildIdentity.rustcVersion) ([string]$identity.rustcVersion) 'Training manifest rustc version'
    Assert-NicoPgoString ([string]$training.buildIdentity.cargoVersion) ([string]$identity.cargoVersion) 'Training manifest Cargo version'
    Assert-NicoPgoString ([string]$training.buildIdentity.llvmVersion) ([string]$identity.llvmVersion) 'Training manifest LLVM version'
    Assert-NicoPgoString ([string]$training.buildIdentity.target) ([string]$identity.target) 'Training manifest target'

    $rustcDetails = @(& rustc -vV 2>&1 | ForEach-Object { $_.ToString() })
    if ($LASTEXITCODE -ne 0) { throw 'rustc -vV failed while checking the PGO toolchain.' }
    $currentRustc = $rustcDetails | Where-Object { $_ -match '^rustc\s+' } | Select-Object -First 1
    $currentHost = $rustcDetails | Where-Object { $_ -match '^host:\s*' } | Select-Object -First 1
    $currentLlvm = $rustcDetails | Where-Object { $_ -match '^LLVM version:\s*' } | Select-Object -First 1
    if (-not $currentRustc -or -not $currentHost -or -not $currentLlvm) { throw 'rustc -vV did not report its complete build identity.' }
    Assert-NicoPgoString $currentRustc.Trim() ([string]$identity.rustcVersion) 'Current rustc version'
    Assert-NicoPgoString (($currentHost -replace '^host:\s*', '').Trim()) $BuildTarget 'Current rustc host target'
    Assert-NicoPgoString (($currentLlvm -replace '^LLVM version:\s*', '').Trim()) ([string]$identity.llvmVersion) 'Current rustc LLVM version'
    $currentCargo = @(& cargo --version 2>&1 | ForEach-Object { $_.ToString() })
    if ($LASTEXITCODE -ne 0 -or $currentCargo.Count -eq 0) { throw 'cargo --version failed while checking the PGO toolchain.' }
    Assert-NicoPgoString $currentCargo[0].Trim() ([string]$identity.cargoVersion) 'Current Cargo version'

    $llvmPath = Resolve-NicoPgoFile ([string]$ownerIdentity.llvmProfdataExecutable) 'llvm-profdata executable' ([System.IO.Path]::GetPathRoot([string]$ownerIdentity.llvmProfdataExecutable))
    $llvmVersionText = (& $llvmPath --version 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'Recorded llvm-profdata executable failed its version check.' }
    $llvmMatch = [regex]::Match($llvmVersionText, 'LLVM(?: version)?\s*:?\s*([0-9]+\.[0-9]+\.[0-9]+)')
    if (-not $llvmMatch.Success -or $llvmMatch.Groups[1].Value -cne [string]$identity.llvmVersion) {
        throw 'Recorded llvm-profdata version differs from the PGO training LLVM version.'
    }
    & $llvmPath show --detailed-summary $profilePath *> $null
    if ($LASTEXITCODE -ne 0) { throw 'Merged profile is not readable by the matching llvm-profdata tool.' }

    $pgoScript = Join-Path $PSScriptRoot 'pgo.ps1'
    $pwsh = Get-Command -Name 'pwsh' -CommandType Application -ErrorAction Stop | Select-Object -First 1
    $validationOutput = & $pwsh.Source -NoLogo -NoProfile -NonInteractive -File $pgoScript -ValidateManifests -TrainingManifest $trainingPath -EvaluationManifest $evaluationPath 2>&1
    if ($LASTEXITCODE -ne 0 -or (($validationOutput | Out-String) -notmatch 'manifests_valid=true')) {
        throw "PGO training/evaluation manifest validation failed: $($validationOutput | Out-String)"
    }

    return [pscustomobject]@{
        runRoot = $fullRoot
        mode = $BaseMode
        target = $BuildTarget
        trainingManifestPath = $trainingPath
        trainingManifestSha256 = $trainingHash
        profilePath = $profilePath
        profileSha256 = $profileHash
        instrumentedHelperPath = $instrumentedPath
        instrumentedHelperSha256 = $instrumentedHash
        buildSourceSnapshotSha256 = [string]$identity.buildSourceSnapshotSha256
        rustcVersion = [string]$identity.rustcVersion
        cargoVersion = [string]$identity.cargoVersion
        llvmVersion = [string]$identity.llvmVersion
        llvmProfdataPath = $llvmPath
    }
}

if ([string]::IsNullOrWhiteSpace($Target)) {
    Push-Location $root
    try {
        $rustcDetails = & rustc -vV
        if ($LASTEXITCODE -ne 0) { throw "rustc -vV failed" }
        $hostLine = $rustcDetails | Where-Object { $_ -match '^host: ' } | Select-Object -First 1
        if (-not $hostLine) { throw "rustc did not report its host target" }
        $Target = ($hostLine -replace '^host: ', '').Trim()
    } finally {
        Pop-Location
    }
}

$overrideNames = @(Get-ChildItem Env: | Where-Object {
    $name = $_.Name.ToUpperInvariant()
    $name -in @(
        "RUSTFLAGS", "CARGO_ENCODED_RUSTFLAGS", "CARGO_HOME", "RUSTC", "RUSTC_WRAPPER",
        "RUSTC_WORKSPACE_WRAPPER", "RUSTUP_HOME", "RUSTUP_TOOLCHAIN", "CARGO_TARGET_DIR",
        "CARGO_BUILD_TARGET"
    ) -or $name.StartsWith("CARGO_PROFILE_") -or
        ($name.StartsWith("CARGO_TARGET_") -and $name.EndsWith("_RUSTFLAGS"))
} | Sort-Object Name)
$overrideRecords = @()
foreach ($entry in $overrideNames) {
    $valueBytes = [System.Text.Encoding]::UTF8.GetBytes([string]$entry.Value)
    $valueHash = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($valueBytes)).ToLowerInvariant()
    $overrideRecords += [ordered]@{ name = $entry.Name; value_sha256 = $valueHash }
}

$scrubNames = @($overrideNames | Where-Object {
    $name = $_.Name.ToUpperInvariant()
    $name -in @(
        "RUSTFLAGS", "CARGO_ENCODED_RUSTFLAGS", "RUSTC", "RUSTC_WRAPPER",
        "RUSTC_WORKSPACE_WRAPPER", "CARGO_TARGET_DIR", "CARGO_BUILD_TARGET"
    ) -or $name.StartsWith("CARGO_PROFILE_") -or
        ($name.StartsWith("CARGO_TARGET_") -and $name.EndsWith("_RUSTFLAGS"))
} | ForEach-Object { $_.Name })
foreach ($record in $overrideRecords) {
    $record.action = if ($scrubNames -contains $record.name) { "scrubbed" } else { "preserved" }
}

$pgoValidation = $null
if ($pgoBuild) {
    $pgoValidation = Resolve-NicoPgoTraining $PgoRunRoot $cargoMode $Target
}

if ($ValidateOnly) {
    [ordered]@{
        mode = $Mode
        cargoProfile = $cargoMode
        target = $Target
        runRoot = $RunRoot
        pgoRunRoot = if ($null -ne $pgoValidation) { $pgoValidation.runRoot } else { $null }
        trainingManifestSha256 = if ($null -ne $pgoValidation) { $pgoValidation.trainingManifestSha256 } else { $null }
        environmentOverrides = $overrideRecords
        scrubbedEnvironmentNames = $scrubNames
    } | ConvertTo-Json -Depth 6 -Compress
    exit 0
}

$driveName = [System.IO.Path]::GetPathRoot($RunRoot).TrimEnd('\').TrimEnd(':')
$drive = Get-PSDrive -Name $driveName -ErrorAction Stop
$gib = [long]1073741824
$buildReserveBytes = 16 * $gib
if ($drive.Free -lt (40 * $gib)) {
    throw "Build reservation requires at least 40 GiB free; found $($drive.Free) bytes."
}
if (($drive.Free - $buildReserveBytes) -lt (8 * $gib)) {
    throw "Build reservation would leave less than 8 GiB free."
}

$ownerPath = Join-Path $RunRoot "build-variants-owned.json"
$owner = $null
if (Test-Path -LiteralPath $RunRoot) {
    if (-not (Test-Path -LiteralPath $ownerPath -PathType Leaf)) {
        throw "Existing RunRoot is not owned by this build runner: $RunRoot"
    }
    $owner = Get-Content -LiteralPath $ownerPath -Raw -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
    if ($owner.schema -ne 1 -or $owner.kind -ne "nico-build-variants" -or
        -not [string]::Equals([System.IO.Path]::GetFullPath([string]$owner.runRoot), $RunRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Existing build ownership manifest does not match RunRoot."
    }
} else {
    [void](New-Item -ItemType Directory -Path $RunRoot)
    $owner = [ordered]@{
        schema = 1
        kind = "nico-build-variants"
        runRoot = $RunRoot
        createdUtc = [DateTime]::UtcNow.ToString("o")
        builds = @()
    }
}
if (@($owner.builds | Where-Object { $_.mode -eq $Mode -and $_.target -eq $Target }).Count -gt 0) {
    throw "This RunRoot already contains a build for $Mode/$Target."
}

$targetDir = Join-Path $RunRoot (Join-Path "cargo" $Mode)
$artifactDir = Join-Path $RunRoot (Join-Path "artifacts" (Join-Path $Target $Mode))
$buildLog = Join-Path $RunRoot ("cargo-" + $Target + "-" + $Mode + ".log")
$overridesPath = Join-Path $RunRoot ("environment-" + $Target + "-" + $Mode + ".json")
$sourceInputsPath = Join-Path $RunRoot ("source-inputs-" + $Target + "-" + $Mode + ".json")
$buildRecord = [ordered]@{
    mode = $Mode
    cargoProfile = $cargoMode
    target = $Target
    status = "started"
    targetDirectory = $targetDir
    buildReserveBytes = $buildReserveBytes
    buildLog = $buildLog
    sourceInputs = $sourceInputsPath
    artifactDirectory = $artifactDir
}
if ($pgoBuild) {
    $buildRecord.pgoRunRoot = $pgoValidation.runRoot
    $buildRecord.trainingManifestSha256 = $pgoValidation.trainingManifestSha256
    $buildRecord.profileSha256 = $pgoValidation.profileSha256
}
$owner.builds = @($owner.builds) + @($buildRecord)
Save-NicoBuildOwner $ownerPath $owner
$overrideJson = ConvertTo-Json -InputObject ([object[]]@($overrideRecords)) -Depth 5
Set-Content -LiteralPath $overridesPath -Value $overrideJson -Encoding utf8NoBOM

$cargoManifest = Join-Path $root "gpu\nico-compositord\Cargo.toml"
$manifestScript = Join-Path $root "scripts\nico_timeline_manifest.py"
$originalEnvironment = @{}
foreach ($name in $scrubNames) {
    $originalEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
    Remove-Item -LiteralPath ("Env:" + $name) -ErrorAction SilentlyContinue
}
if (-not $originalEnvironment.ContainsKey("CARGO_TARGET_DIR")) { $originalEnvironment["CARGO_TARGET_DIR"] = $null }
if (-not $originalEnvironment.ContainsKey("CARGO_BUILD_TARGET")) { $originalEnvironment["CARGO_BUILD_TARGET"] = $null }
$hadCargoTargetDir = $null -ne $originalEnvironment["CARGO_TARGET_DIR"]
$hadCargoBuildTarget = $null -ne $originalEnvironment["CARGO_BUILD_TARGET"]
$buildSucceeded = $false
$cargoProcessFinished = $false
Push-Location $root
try {
    if (Test-Path -LiteralPath $targetDir) {
        throw "Target directory already exists and is not owned by this build: $targetDir"
    }
    $existingInventory = Get-NicoBuildTreeInventory $RunRoot $RunRoot
    if (($existingInventory.bytes + $buildReserveBytes) -gt (24 * $gib)) {
        throw "Existing owned output plus build reserve exceeds the 24 GiB build budget."
    }
    $env:CARGO_TARGET_DIR = $targetDir
    $env:CARGO_BUILD_TARGET = $Target
    & python $manifestScript source-inputs --source-root $root --output $sourceInputsPath
    if ($LASTEXITCODE -ne 0) { throw "build source input snapshot failed" }
    if ($pgoBuild) {
        $currentSourceSnapshot = Get-NicoSourceSnapshotSha256 $sourceInputsPath
        Assert-NicoPgoString $currentSourceSnapshot $pgoValidation.buildSourceSnapshotSha256 'Current build source snapshot'
    }
    $buildTimer = [System.Diagnostics.Stopwatch]::StartNew()
    $cargoArguments = @(
        'build', '--profile', $cargoMode, '--locked', '--manifest-path', $cargoManifest,
        '--target-dir', $targetDir, '--target', $Target
    )
    if ($pgoBuild) {
        $profileFlags = ConvertTo-Json -InputObject @("-Cprofile-use=$($pgoValidation.profilePath)") -Compress
        $cargoArguments += @('--bin', 'nico-compositord', '--config', "build.rustflags=$profileFlags")
    }
    $cargoArguments += '-vv'
    $buildOutput = & cargo @cargoArguments 2>&1
    $cargoExit = $LASTEXITCODE
    $buildTimer.Stop()
    $cargoProcessFinished = $true
    $buildOutput | ForEach-Object { $_.ToString() } | Set-Content -LiteralPath $buildLog -Encoding utf8NoBOM
    $owner.builds[-1].cargoBuildSeconds = [Math]::Round($buildTimer.Elapsed.TotalSeconds, 3)
    if ($cargoExit -ne 0) { throw "cargo build failed for $Target/$Mode (exit $cargoExit)" }
    $afterBuildInventory = Get-NicoBuildTreeInventory $RunRoot $RunRoot
    if ($afterBuildInventory.bytes -gt (24 * $gib)) {
        throw "Build output exceeded the 24 GiB owned-build budget."
    }

    $binaryName = "nico-compositord"
    if ($Target -match "windows") { $binaryName += ".exe" }
    $builtBinary = Join-Path $targetDir (Join-Path $Target (Join-Path $cargoMode $binaryName))
    if (-not (Test-Path -LiteralPath $builtBinary -PathType Leaf)) {
        throw "built compositor is missing: $builtBinary"
    }
    [void](New-Item -ItemType Directory -Force -Path $artifactDir)
    $payload = Join-Path $artifactDir $binaryName
    Copy-Item -LiteralPath $builtBinary -Destination $payload
    if ($pgoBuild) {
        $payloadHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $payload).Hash.ToLowerInvariant()
        if ($payloadHash -ceq $pgoValidation.instrumentedHelperSha256) {
            throw 'Instrumented training helper was copied into the profile-use build artifact.'
        }
        $buildOutputText = [string]::Join("`n", @($buildOutput | ForEach-Object { $_.ToString() }))
        if ($buildOutputText -match '(?i)-C\s*profile-generate(?:=|\s+)' -or
            $buildOutputText -notmatch '(?i)-C\s*profile-use(?:=|\s+)') {
            throw 'Cargo output does not show a profile-use build without profile-generate instrumentation.'
        }
    }
    $runtimeManifest = Join-Path $artifactDir "runtime-manifest.json"
    & python $manifestScript --binary $payload --target $Target --output $runtimeManifest --cargo-manifest $cargoManifest --notices-output-directory $artifactDir
    if ($LASTEXITCODE -ne 0) { throw "runtime manifest generation failed" }

    $buildManifest = Join-Path $artifactDir "build-manifest.json"
    $provenanceArguments = @(
        $manifestScript, 'build-provenance', '--binary', $payload, '--target', $Target,
        '--mode', $Mode, '--source-root', $root, '--source-inputs', $sourceInputsPath,
        '--build-log', $buildLog, '--overrides-json', $overridesPath, '--output', $buildManifest
    )
    if ($pgoBuild) {
        $provenanceArguments += @(
            '--training-manifest-sha256', $pgoValidation.trainingManifestSha256,
            '--profile-sha256', $pgoValidation.profileSha256
        )
    }
    & python @provenanceArguments
    if ($LASTEXITCODE -ne 0) { throw "build provenance generation failed" }
    $owner.builds[-1].status = "complete"
    $owner.builds[-1].binarySha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $payload).Hash.ToLowerInvariant()
    $owner.builds[-1].binaryBytes = (Get-Item -LiteralPath $payload).Length
    $buildSucceeded = $true
} finally {
    Pop-Location
    foreach ($name in $scrubNames) {
        Remove-Item -LiteralPath ("Env:" + $name) -ErrorAction SilentlyContinue
        $prior = $originalEnvironment[$name]
        if ($null -ne $prior) { [Environment]::SetEnvironmentVariable($name, $prior, "Process") }
    }
    if (-not $hadCargoTargetDir) {
        Remove-Item Env:CARGO_TARGET_DIR -ErrorAction SilentlyContinue
    }
    if (-not $hadCargoBuildTarget) {
        Remove-Item Env:CARGO_BUILD_TARGET -ErrorAction SilentlyContinue
    }
    if ($cargoProcessFinished -and (Test-Path -LiteralPath $targetDir -PathType Container)) {
        try {
            $targetInventory = Get-NicoBuildTreeInventory $targetDir $RunRoot
            $owner.builds[-1].targetCleanup = [ordered]@{
                path = [System.IO.Path]::GetFullPath($targetDir)
                files = $targetInventory.files
                bytes = $targetInventory.bytes
                treeSha256 = $targetInventory.treeSha256
                status = "inventory_recorded"
            }
            Save-NicoBuildOwner $ownerPath $owner
            $targetFull = [System.IO.Path]::GetFullPath($targetDir)
            $rootFull = [System.IO.Path]::GetFullPath($RunRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar)
            if (-not $targetFull.StartsWith(($rootFull + [System.IO.Path]::DirectorySeparatorChar), [System.StringComparison]::OrdinalIgnoreCase)) {
                throw "Cargo target cleanup escaped its owned root: $targetFull"
            }
            Remove-Item -LiteralPath $targetFull -Recurse -Force
            if (Test-Path -LiteralPath $targetFull) { throw "Cargo target cleanup did not remove $targetFull" }
            $owner.builds[-1].targetCleanup.status = "removed"
            $owner.builds[-1].targetCleanup.removedUtc = [DateTime]::UtcNow.ToString("o")
            Save-NicoBuildOwner $ownerPath $owner
        } catch {
            $owner.builds[-1].status = "cleanup_failed"
            $owner.builds[-1].cleanupError = $_.Exception.Message
            Save-NicoBuildOwner $ownerPath $owner
            throw
        }
    }
    $owner.builds[-1].status = if ($buildSucceeded) { "complete" } else { "failed" }
    Save-NicoBuildOwner $ownerPath $owner
}

[ordered]@{
    mode = $Mode
    cargoProfile = $cargoMode
    target = $Target
    pgoRunRoot = if ($null -ne $pgoValidation) { $pgoValidation.runRoot } else { $null }
    trainingManifestSha256 = if ($null -ne $pgoValidation) { $pgoValidation.trainingManifestSha256 } else { $null }
    profileSha256 = if ($null -ne $pgoValidation) { $pgoValidation.profileSha256 } else { $null }
    runtimeManifest = $runtimeManifest
    buildManifest = $buildManifest
    runRoot = $RunRoot
} | ConvertTo-Json -Depth 5 -Compress
