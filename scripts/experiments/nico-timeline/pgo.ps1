[CmdletBinding()]
param(
    [switch]$ValidateManifests,
    [switch]$RunTraining,
    [switch]$BuildProfileUse,
    [string]$TrainingManifest,
    [string]$EvaluationManifest,
    [ValidateSet('release', 'release-thin', 'release-thin-one')]
    [string]$Mode = 'release',
    [string]$Target = '',
    [string]$RunRoot = '',
    [string]$PgoRunRoot = '',
    [string]$WorkloadCatalog = '',
    [string]$TrainingRunner = '',
    [string[]]$TrainingRunnerArguments = @(),
    [string]$CargoExecutable = 'cargo',
    [string]$RustcExecutable = 'rustc',
    [string]$PythonExecutable = 'python',
    [string]$LlvmProfdataExecutable = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Fail([string]$Message) {
    throw $Message
}

function Require-Object($Value, [string]$Path) {
    if ($null -eq $Value -or $Value -isnot [System.Collections.IDictionary]) {
        Fail "$Path must be a JSON object."
    }
}

function Require-Fields($Object, [string[]]$Fields, [string]$Path) {
    Require-Object $Object $Path
    foreach ($field in $Fields) {
        if (-not $Object.Contains($field)) { Fail "$Path.$field is required." }
    }
    foreach ($key in $Object.Keys) {
        if ($Fields -cnotcontains [string]$key) { Fail "$Path.$key is not a supported field." }
    }
}

function Require-AllowedFields($Object, [string[]]$Required, [string[]]$Allowed, [string]$Path) {
    Require-Object $Object $Path
    foreach ($field in $Required) {
        if (-not $Object.Contains($field)) { Fail "$Path.$field is required." }
    }
    foreach ($key in $Object.Keys) {
        if ($Allowed -cnotcontains [string]$key) { Fail "$Path.$key is not a supported field." }
    }
}

function Require-Sha256($Value, [string]$Path) {
    if ($Value -isnot [string] -or $Value -cnotmatch '^[0-9a-fA-F]{64}$') {
        Fail "$Path must be a 64-character SHA256 hex digest."
    }
}

function Canonicalize($Value) {
    if ($null -eq $Value) { return $null }
    if ($Value -is [System.Collections.IDictionary]) {
        $sorted = [ordered]@{}
        $keys = [string[]]@($Value.Keys)
        [Array]::Sort($keys, [System.StringComparer]::Ordinal)
        foreach ($key in $keys) {
            $sorted[[string]$key] = Canonicalize $Value[$key]
        }
        return ,$sorted
    }
    if ($Value -is [array]) {
        $items = @()
        foreach ($item in $Value) { $items += ,(Canonicalize $item) }
        return ,$items
    }
    return $Value
}

function Get-CanonicalSha256($Value) {
    $json = ConvertTo-Json -InputObject (Canonicalize $Value) -Depth 100 -Compress
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
    $sha = [System.Security.Cryptography.SHA256]::HashData($bytes)
    return [Convert]::ToHexString($sha).ToLowerInvariant()
}

function Read-Manifest(
    [string]$Path,
    [string]$Role,
    [switch]$AllowMissingBuildIdentity,
    [switch]$AllowMissingProfileMetadata
) {
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        Fail "$Role manifest path does not exist: $Path"
    }
    try {
        $manifest = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json -AsHashtable -Depth 100
    } catch {
        Fail "$Role manifest is not valid JSON: $($_.Exception.Message)"
    }
    $requiredFields = @('schemaVersion', 'role', 'materials')
    $allowedFields = @('schemaVersion', 'role', 'materials', 'buildIdentity')
    if (-not $AllowMissingBuildIdentity) { $requiredFields += 'buildIdentity' }
    if ($Role -eq 'evaluation') {
        $allowedFields += 'profileMetadata'
        if (-not $AllowMissingProfileMetadata) { $requiredFields += 'profileMetadata' }
    }
    Require-AllowedFields $manifest $requiredFields $allowedFields "$Role manifest"
    if ($manifest.schemaVersion -isnot [long] -and $manifest.schemaVersion -isnot [int]) {
        Fail "$Role manifest.schemaVersion must be integer 1."
    }
    if ($manifest.schemaVersion -ne 1) { Fail "$Role manifest.schemaVersion must be 1." }
    if ($manifest.role -cne $Role) { Fail "$Role manifest.role must be '$Role'." }

    if ($manifest.Contains('buildIdentity')) {
        $identityFields = @('buildSourceSnapshotSha256', 'rustcVersion', 'cargoVersion', 'llvmVersion', 'target')
        Require-Fields $manifest.buildIdentity $identityFields "$Role manifest.buildIdentity"
        Require-Sha256 $manifest.buildIdentity.buildSourceSnapshotSha256 "$Role manifest.buildIdentity.buildSourceSnapshotSha256"
        foreach ($field in @('rustcVersion', 'cargoVersion', 'llvmVersion', 'target')) {
            if ($manifest.buildIdentity[$field] -isnot [string] -or [string]::IsNullOrWhiteSpace($manifest.buildIdentity[$field])) {
                Fail "$Role manifest.buildIdentity.$field must be a non-empty string."
            }
        }
    }
    if ($manifest.materials -isnot [array] -or $manifest.materials.Count -eq 0) {
        Fail "$Role manifest.materials must be a non-empty array."
    }
    $coverageRequired = @('short', 'long', 'dense', 'NCT1', 'NCT2')
    $seenCoverage = @{}
    $materialFields = @('id', 'coverage', 'sourceSha256', 'snapshotSha256', 'seed')
    for ($i = 0; $i -lt $manifest.materials.Count; $i++) {
        $item = $manifest.materials[$i]
        $itemPath = "$Role manifest.materials[$i]"
        Require-Fields $item $materialFields $itemPath
        foreach ($field in @('id', 'coverage')) {
            if ($item[$field] -isnot [string] -or [string]::IsNullOrWhiteSpace($item[$field])) {
                Fail "$itemPath.$field must be a non-empty string."
            }
        }
        Require-Sha256 $item.sourceSha256 "$itemPath.sourceSha256"
        Require-Sha256 $item.snapshotSha256 "$itemPath.snapshotSha256"
        if ($item.seed -isnot [long] -and $item.seed -isnot [int]) { Fail "$itemPath.seed must be an integer." }
        $seenCoverage[[string]$item.coverage] = $true
    }
    foreach ($coverage in $coverageRequired) {
        if (-not $seenCoverage.ContainsKey($coverage)) { Fail "$Role manifest is missing required coverage '$coverage'." }
    }
    return $manifest
}

function Assert-MaterialSeparation($Training, $Evaluation) {
    $trainingMaterials = @{}
    foreach ($item in $Training.materials) {
        $key = $item.sourceSha256.ToLowerInvariant() + ':' + $item.snapshotSha256.ToLowerInvariant()
        $trainingMaterials[$key] = $true
    }
    foreach ($item in $Evaluation.materials) {
        $key = $item.sourceSha256.ToLowerInvariant() + ':' + $item.snapshotSha256.ToLowerInvariant()
        if ($trainingMaterials.ContainsKey($key)) {
            Fail "training/evaluation materials overlap for sourceSha256+snapshotSha256 pair (evaluation id '$($item.id)'). Seed does not distinguish material identity."
        }
    }
}

function Get-CheckedInputFile([string]$Path, [string]$Description, [long]$MaximumBytes = [long]::MaxValue) {
    if ([string]::IsNullOrWhiteSpace($Path) -or -not [System.IO.Path]::IsPathRooted($Path)) {
        Fail "$Description path must be absolute."
    }
    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $root = [System.IO.Path]::GetPathRoot($fullPath)
    $relative = $fullPath.Substring($root.Length)
    $parts = @($relative -split '[\\/]') | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    $cursor = $root
    for ($index = 0; $index -lt $parts.Count; $index++) {
        $cursor = Join-Path $cursor $parts[$index]
        if (-not (Test-Path -LiteralPath $cursor)) { Fail "$Description does not exist: $fullPath" }
        $item = Get-Item -LiteralPath $cursor -Force -ErrorAction Stop
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            Fail "$Description path traverses a reparse point: $cursor"
        }
        if ($index -lt ($parts.Count - 1) -and -not $item.PSIsContainer) {
            Fail "$Description parent is not a directory: $cursor"
        }
    }
    $leaf = Get-Item -LiteralPath $fullPath -Force -ErrorAction Stop
    if ($leaf.PSIsContainer) { Fail "$Description must be a regular file: $fullPath" }
    if ([long]$leaf.Length -gt $MaximumBytes) { Fail "$Description exceeds the $MaximumBytes-byte limit: $fullPath" }
    return [pscustomobject]@{ Path = $fullPath; Bytes = [long]$leaf.Length }
}

function Read-WorkloadCatalog([string]$Path, $Training, $Evaluation) {
    $catalogFile = Get-CheckedInputFile $Path 'Workload catalog' ([long](1MB))
    try {
        $catalog = Get-Content -LiteralPath $catalogFile.Path -Raw -Encoding UTF8 | ConvertFrom-Json -AsHashtable -Depth 30
    } catch {
        Fail "Workload catalog is not valid JSON: $($_.Exception.Message)"
    }
    Require-Fields $catalog @('schemaVersion', 'browserPath', 'training', 'evaluation') 'Workload catalog'
    if (($catalog.schemaVersion -isnot [long] -and $catalog.schemaVersion -isnot [int]) -or $catalog.schemaVersion -ne 1) {
        Fail 'Workload catalog.schemaVersion must be integer 1.'
    }
    if ($catalog.training -isnot [array] -or $catalog.evaluation -isnot [array]) {
        Fail 'Workload catalog training/evaluation must be arrays.'
    }
    $browser = Get-CheckedInputFile ([string]$catalog.browserPath) 'Workload catalog browserPath'
    $hashes = @{}
    $hashes[[string]$browser.Path] = (Get-FileHash -LiteralPath $browser.Path -Algorithm SHA256).Hash.ToLowerInvariant()
    foreach ($role in @(
        @{ Name = 'training'; Inputs = @($catalog.training); Manifest = $Training },
        @{ Name = 'evaluation'; Inputs = @($catalog.evaluation); Manifest = $Evaluation }
    )) {
        $roleName = [string]$role.Name
        $inputs = @($role.Inputs)
        $manifest = $role.Manifest
        if ($inputs.Count -ne $manifest.materials.Count) {
            Fail "Workload catalog.$roleName must contain exactly one input for every manifest material."
        }
        $materialsById = @{}
        foreach ($material in $manifest.materials) {
            if ($materialsById.ContainsKey([string]$material.id)) { Fail "$roleName manifest material IDs must be unique for workload mapping." }
            $materialsById[[string]$material.id] = $material
        }
        $seen = @{}
        $inputFields = @('id', 'coverage', 'sourcePath', 'snapshotPath', 'protocol', 'width', 'height', 'durationMs', 'fpsNum', 'fpsDen', 'backend', 'readbackSlots', 'assetLayout')
        for ($index = 0; $index -lt $inputs.Count; $index++) {
            $input = $inputs[$index]
            $inputPath = "Workload catalog.$roleName[$index]"
            Require-Fields $input $inputFields $inputPath
            if ($input.id -isnot [string] -or $input.id -notmatch '^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,127}$') {
                Fail "$inputPath.id is not a safe material ID."
            }
            if ($seen.ContainsKey([string]$input.id)) { Fail "$inputPath.id is duplicated." }
            $seen[[string]$input.id] = $true
            if (-not $materialsById.ContainsKey([string]$input.id)) { Fail "$inputPath.id is absent from the $roleName manifest." }
            $material = $materialsById[[string]$input.id]
            if ($input.coverage -isnot [string] -or $input.coverage -cne [string]$material.coverage) {
                Fail "$inputPath.coverage does not match its manifest material."
            }
            if ($input.protocol -isnot [string] -or $input.protocol -cnotin @('NCT1', 'NCT2')) {
                Fail "$inputPath.protocol must be NCT1 or NCT2."
            }
            if (($input.coverage -ceq 'NCT1' -and $input.protocol -cne 'NCT1') -or
                ($input.coverage -ceq 'NCT2' -and $input.protocol -cne 'NCT2')) {
                Fail "$inputPath.protocol does not match its protocol coverage."
            }
            foreach ($field in @('width', 'height', 'durationMs', 'fpsNum', 'fpsDen', 'readbackSlots')) {
                if ($input[$field] -isnot [long] -and $input[$field] -isnot [int]) { Fail "$inputPath.$field must be an integer." }
            }
            if ($input.width -lt 16 -or $input.width -gt 3840 -or $input.height -lt 16 -or $input.height -gt 2160 -or
                $input.durationMs -le 0 -or $input.fpsNum -le 0 -or $input.fpsDen -le 0 -or
                $input.readbackSlots -lt 1 -or $input.readbackSlots -gt 3) {
                Fail "$inputPath contains render bounds outside the supported range."
            }
            if ($input.backend -isnot [string] -or $input.backend -cnotin @('auto', 'dx12', 'vulkan', 'metal')) {
                Fail "$inputPath.backend is unsupported."
            }
            if ($input.assetLayout -isnot [string] -or $input.assetLayout -cnotin @('separate', 'atlas')) {
                Fail "$inputPath.assetLayout is unsupported."
            }
            foreach ($fileSpec in @(
                @{ Field = 'sourcePath'; Expected = [string]$material.sourceSha256 },
                @{ Field = 'snapshotPath'; Expected = [string]$material.snapshotSha256 }
            )) {
                $file = Get-CheckedInputFile ([string]$input[$fileSpec.Field]) "$inputPath.$($fileSpec.Field)"
                $key = $file.Path.ToUpperInvariant()
                if (-not $hashes.ContainsKey($key)) {
                    $hashes[$key] = (Get-FileHash -LiteralPath $file.Path -Algorithm SHA256).Hash.ToLowerInvariant()
                }
                if ($hashes[$key] -cne $fileSpec.Expected.ToLowerInvariant()) {
                    Fail "$inputPath.$($fileSpec.Field) SHA-256 does not match the $roleName manifest."
                }
            }
        }
    }
    return $catalog
}

function Copy-WorkloadInputsToOwnedRoot($Catalog, [string]$OwnedRoot) {
    $copyCache = @{}
    foreach ($role in @('training', 'evaluation')) {
        $inputs = @($Catalog[$role])
        foreach ($workloadInput in $inputs) {
            foreach ($field in @('sourcePath', 'snapshotPath')) {
                $sourcePath = [System.IO.Path]::GetFullPath([string]$workloadInput[$field])
                $kind = if ($field -ceq 'sourcePath') { 'source' } else { 'snapshot' }
                $hash = (Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256).Hash.ToLowerInvariant()
                $key = $kind + ':' + $hash
                if (-not $copyCache.ContainsKey($key)) {
                    $extension = [System.IO.Path]::GetExtension($sourcePath)
                    if ($extension -notmatch '^\.[A-Za-z0-9]{1,12}$') { Fail "Workload input has an unsupported file extension: $sourcePath" }
                    $directory = Join-Path $OwnedRoot (Join-Path 'inputs' $kind)
                    [void][System.IO.Directory]::CreateDirectory($directory)
                    $destination = Join-Path $directory ($kind + '-' + $hash + $extension.ToLowerInvariant())
                    if ([System.IO.File]::Exists($destination)) {
                        $existing = Get-CheckedInputFile $destination 'Existing owned workload input'
                        if ((Get-FileHash -LiteralPath $existing.Path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $hash) {
                            Fail "Owned workload input hash-address collision: $destination"
                        }
                    } else {
                        [System.IO.File]::Copy($sourcePath, $destination, $false)
                    }
                    $copied = Get-CheckedInputFile $destination 'Copied owned workload input'
                    if ((Get-FileHash -LiteralPath $copied.Path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $hash) {
                        Fail "Workload input changed while being copied into the owned PGO root: $sourcePath"
                    }
                    $copyCache[$key] = $copied.Path
                }
                $workloadInput[$field] = [string]$copyCache[$key]
            }
        }
    }
    return $Catalog
}

function Get-OwnedOutputPath([string]$Path, [string]$Description, [string]$OwnedRoot, [long]$MaximumBytes = [long]::MaxValue) {
    $root = [System.IO.Path]::GetFullPath($OwnedRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $prefix = $root + [System.IO.Path]::DirectorySeparatorChar
    if ([string]::IsNullOrWhiteSpace($Path) -or -not [System.IO.Path]::IsPathRooted($Path)) {
        Fail "$Description path must be absolute and inside the owned PGO RunRoot."
    }
    $fullPath = [System.IO.Path]::GetFullPath($Path)
    if (-not $fullPath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        Fail "$Description escaped the owned PGO RunRoot: $fullPath"
    }
    $file = Get-CheckedInputFile $fullPath $Description $MaximumBytes
    return $file
}

function Assert-WorkloadEvidence(
    [string]$Path,
    [string]$OwnedRoot,
    [string]$HelperPath,
    [string]$HelperSha256,
    $Training,
    $Catalog
) {
    $evidenceFile = Get-OwnedOutputPath $Path 'PGO workload evidence' $OwnedRoot ([long](5MB))
    if ($evidenceFile.Bytes -le 0) { Fail 'PGO workload evidence is empty.' }
    try {
        $evidence = Get-Content -LiteralPath $evidenceFile.Path -Raw -Encoding UTF8 | ConvertFrom-Json -AsHashtable -Depth 30
    } catch {
        Fail "PGO workload evidence is not valid JSON: $($_.Exception.Message)"
    }
    Require-Fields $evidence @('schemaVersion', 'helperPath', 'helperSha256', 'runs') 'PGO workload evidence'
    if (($evidence.schemaVersion -isnot [long] -and $evidence.schemaVersion -isnot [int]) -or $evidence.schemaVersion -ne 1) {
        Fail 'PGO workload evidence schemaVersion must be integer 1.'
    }
    if (-not [string]::Equals([System.IO.Path]::GetFullPath([string]$evidence.helperPath), [System.IO.Path]::GetFullPath($HelperPath), [System.StringComparison]::OrdinalIgnoreCase)) {
        Fail 'PGO workload evidence helperPath does not match the instrumented helper.'
    }
    Require-Sha256 $evidence.helperSha256 'PGO workload evidence.helperSha256'
    if ($evidence.helperSha256 -cne $HelperSha256) { Fail 'PGO workload evidence helper hash does not match the instrumented helper.' }
    if ($evidence.runs -isnot [array] -or $evidence.runs.Count -ne $Training.materials.Count) {
        Fail 'PGO workload evidence must contain exactly one run per training material.'
    }

    $materialsById = @{}
    foreach ($material in $Training.materials) { $materialsById[[string]$material.id] = $material }
    $catalogById = @{}
    foreach ($input in $Catalog.training) { $catalogById[[string]$input.id] = $input }
    $seen = @{}
    $runFields = @('id', 'coverage', 'protocol', 'width', 'height', 'durationMs', 'fpsNum', 'fpsDen', 'backend', 'readbackSlots', 'assetLayout', 'sourcePath', 'sourceSha256', 'snapshotPath', 'snapshotSha256', 'scenePath', 'sceneBytes', 'sceneSha256', 'captureFrameCount', 'captureEligibleComments', 'captureAssets', 'captureDraws', 'runtimeReportPath', 'runtimeReport', 'wallSeconds')
    $runtimeFields = @('protocol', 'renderer', 'version', 'requestedBackend', 'backend', 'adapterName', 'readbackSlots', 'requestedAssetLayout', 'assetLayout', 'completedFrames', 'error')
    foreach ($run in $evidence.runs) {
        Require-Fields $run $runFields 'PGO workload evidence run'
        $id = [string]$run.id
        if (-not $materialsById.ContainsKey($id) -or -not $catalogById.ContainsKey($id) -or $seen.ContainsKey($id)) {
            Fail "PGO workload evidence run ID is missing, unexpected, or duplicated: $id"
        }
        $seen[$id] = $true
        $material = $materialsById[$id]
        $input = $catalogById[$id]
        foreach ($field in @('coverage', 'protocol', 'width', 'height', 'durationMs', 'fpsNum', 'fpsDen', 'backend', 'readbackSlots', 'assetLayout')) {
            if ([string]$run[$field] -cne [string]$input[$field]) { Fail "PGO workload evidence $id.$field does not match the catalog." }
        }
        foreach ($field in @('sourcePath', 'snapshotPath')) {
            $expectedPath = [System.IO.Path]::GetFullPath([string]$input[$field])
            $actualPath = [System.IO.Path]::GetFullPath([string]$run[$field])
            if (-not [string]::Equals($actualPath, $expectedPath, [System.StringComparison]::OrdinalIgnoreCase)) {
                Fail "PGO workload evidence $id.$field does not match the catalog."
            }
        }
        if ($run.coverage -cne [string]$material.coverage -or
            $run.sourceSha256 -cne [string]$material.sourceSha256 -or
            $run.snapshotSha256 -cne [string]$material.snapshotSha256) {
            Fail "PGO workload evidence $id material hashes or coverage do not match training.json."
        }
        Require-Sha256 $run.sceneSha256 "PGO workload evidence $id.sceneSha256"
        if ($run.sceneBytes -isnot [long] -and $run.sceneBytes -isnot [int]) { Fail "PGO workload evidence $id.sceneBytes must be an integer." }
        if ($run.sceneBytes -le 0) { Fail "PGO workload evidence $id scene must not be empty." }
        $scene = Get-OwnedOutputPath ([string]$run.scenePath) "PGO workload evidence $id scenePath" $OwnedRoot
        if ($scene.Bytes -ne [long]$run.sceneBytes -or
            (Get-FileHash -LiteralPath $scene.Path -Algorithm SHA256).Hash.ToLowerInvariant() -cne [string]$run.sceneSha256) {
            Fail "PGO workload evidence $id scene bytes or SHA-256 does not match the captured scene."
        }
        foreach ($field in @('captureFrameCount', 'captureEligibleComments', 'captureAssets', 'captureDraws')) {
            if ($run[$field] -isnot [long] -and $run[$field] -isnot [int]) { Fail "PGO workload evidence $id.$field must be an integer." }
            if ($run[$field] -le 0) { Fail "PGO workload evidence $id.$field must be positive." }
        }
        if ($run.wallSeconds -isnot [double] -and $run.wallSeconds -isnot [long] -and $run.wallSeconds -isnot [int]) {
            Fail "PGO workload evidence $id.wallSeconds must be numeric."
        }
        if ($run.wallSeconds -lt 0) { Fail "PGO workload evidence $id.wallSeconds must not be negative." }

        Require-Fields $run.runtimeReport $runtimeFields "PGO workload evidence $id.runtimeReport"
        $runtime = $run.runtimeReport
        if ($runtime.protocol -cne [string]$input.protocol -or $runtime.renderer -cne 'wgpu' -or
            [string]::IsNullOrWhiteSpace([string]$runtime.version) -or
            $runtime.requestedBackend -cne [string]$input.backend -or
            [string]::IsNullOrWhiteSpace([string]$runtime.backend) -or
            [string]::IsNullOrWhiteSpace([string]$runtime.adapterName) -or
            $runtime.readbackSlots -ne $input.readbackSlots -or
            $runtime.requestedAssetLayout -cne [string]$input.assetLayout -or
            $runtime.assetLayout -cne [string]$input.assetLayout -or
            $runtime.completedFrames -ne $run.captureFrameCount -or $null -ne $runtime.error) {
            Fail "PGO workload evidence $id runtime report does not prove the requested wgpu workload completed."
        }
        $runtimeReport = Get-OwnedOutputPath ([string]$run.runtimeReportPath) "PGO workload evidence $id runtimeReportPath" $OwnedRoot
        if ($runtimeReport.Bytes -le 0) { Fail "PGO workload evidence $id runtime report is empty." }
        try {
            $runtimeDocument = Get-Content -LiteralPath $runtimeReport.Path -Raw -Encoding UTF8 | ConvertFrom-Json -AsHashtable -Depth 10
        } catch {
            Fail "PGO workload evidence $id runtime report file is not valid JSON: $($_.Exception.Message)"
        }
        foreach ($field in $runtimeFields) {
            if ((ConvertTo-Json -InputObject $runtimeDocument[$field] -Compress -Depth 10) -cne
                (ConvertTo-Json -InputObject $runtime[$field] -Compress -Depth 10)) {
                Fail "PGO workload evidence $id runtime report file disagrees on $field."
            }
        }
    }
    return [ordered]@{
        path = $evidenceFile.Path
        bytes = $evidenceFile.Bytes
        sha256 = (Get-FileHash -LiteralPath $evidenceFile.Path -Algorithm SHA256).Hash.ToLowerInvariant()
        runCount = $evidence.runs.Count
    }
}

function Assert-ManifestsValid($Training, $Evaluation, [switch]$RequireProfileMetadata) {
    $identityFields = @('buildSourceSnapshotSha256', 'rustcVersion', 'cargoVersion', 'llvmVersion', 'target')
    $hasTrainingIdentity = $Training.Contains('buildIdentity')
    $hasEvaluationIdentity = $Evaluation.Contains('buildIdentity')
    if ($RequireProfileMetadata -or $hasTrainingIdentity -or $hasEvaluationIdentity) {
        if (-not $hasTrainingIdentity -or -not $hasEvaluationIdentity) {
            Fail 'Both training and evaluation manifests must contain buildIdentity.'
        }
        foreach ($field in $identityFields) {
            if ($Training.buildIdentity[$field] -cne $Evaluation.buildIdentity[$field]) {
                Fail "buildIdentity.$field differs between training and evaluation manifests."
            }
        }
    }

    Assert-MaterialSeparation $Training $Evaluation
    $hasProfile = $Evaluation.Contains('profileMetadata')
    if ($RequireProfileMetadata -or $hasProfile) {
        $profileFields = @('trainingManifestSha256', 'sourceSnapshotSha256', 'rustcVersion', 'cargoVersion', 'llvmVersion', 'target', 'profileSha256', 'instrumented')
        Require-Fields $Evaluation.profileMetadata $profileFields 'evaluation manifest.profileMetadata'
        $profile = $Evaluation.profileMetadata
        Require-Sha256 $profile.trainingManifestSha256 'profileMetadata.trainingManifestSha256'
        $expectedTrainingHash = Get-CanonicalSha256 $Training
        if ($profile.trainingManifestSha256 -cne $expectedTrainingHash) { Fail 'profileMetadata.trainingManifestSha256 does not match the canonical training manifest SHA256.' }
        Require-Sha256 $profile.profileSha256 'profileMetadata.profileSha256'
        $metadataIdentity = @{
            sourceSnapshotSha256 = 'buildSourceSnapshotSha256'
            rustcVersion = 'rustcVersion'
            cargoVersion = 'cargoVersion'
            llvmVersion = 'llvmVersion'
            target = 'target'
        }
        foreach ($field in $metadataIdentity.Keys) {
            if ($profile[$field] -cne $Evaluation.buildIdentity[$metadataIdentity[$field]]) {
                Fail "profileMetadata.$field does not match evaluation buildIdentity.$($metadataIdentity[$field])."
            }
        }
        if ($profile.instrumented -isnot [bool] -or $profile.instrumented -ne $false) {
            Fail 'profileMetadata.instrumented must be false for the evaluation profile.'
        }
    }
}

function Write-PgoJson([string]$Path, $Value) {
    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $temporaryPath = $fullPath + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    $json = ConvertTo-Json -InputObject $Value -Depth 100
    [System.IO.File]::WriteAllText($temporaryPath, $json + "`n", [System.Text.UTF8Encoding]::new($false))
    if ([System.IO.File]::Exists($fullPath)) {
        [System.IO.File]::Move($temporaryPath, $fullPath, $true)
    } else {
        [System.IO.File]::Move($temporaryPath, $fullPath)
    }
}

function Save-PgoOwner {
    if ($null -ne $script:PgoOwner -and -not [string]::IsNullOrWhiteSpace($script:PgoOwnedRoot)) {
        Write-PgoJson (Join-Path $script:PgoOwnedRoot 'pgo-owner.json') $script:PgoOwner
    }
}

function Set-PgoStage([string]$Stage) {
    $script:PgoStage = $Stage
    if ($null -ne $script:PgoOwner) {
        $script:PgoOwner.stage = $Stage
        Save-PgoOwner
    }
}

function Resolve-ExecutablePath([string]$Executable) {
    if ([string]::IsNullOrWhiteSpace($Executable)) { Fail 'An executable path is required.' }
    if (Test-Path -LiteralPath $Executable -PathType Leaf) {
        return [System.IO.Path]::GetFullPath($Executable)
    }
    $command = Get-Command -Name $Executable -ErrorAction Stop | Select-Object -First 1
    if ($command.CommandType -notin @('Application', 'ExternalScript')) {
        Fail "Executable did not resolve to an application or script: $Executable"
    }
    $resolved = if ($command.Source) { $command.Source } else { $command.Path }
    if ([string]::IsNullOrWhiteSpace($resolved)) { Fail "Could not resolve executable path: $Executable" }
    return [System.IO.Path]::GetFullPath($resolved)
}

function Invoke-External(
    [string]$Executable,
    [string[]]$Arguments = @(),
    [hashtable]$EnvironmentVariables = @{},
    [string[]]$ScrubEnvironmentNames = @(),
    [string]$WorkingDirectory = (Get-Location).Path,
    [string]$LogPath = ''
) {
    $resolved = Resolve-ExecutablePath $Executable
    $processPath = $resolved
    $processArguments = @()
    if ([System.IO.Path]::GetExtension($resolved) -ieq '.ps1') {
        $pwshCommand = Get-Command -Name 'pwsh' -CommandType Application -ErrorAction Stop | Select-Object -First 1
        $processPath = $pwshCommand.Source
        $processArguments = @('-NoLogo', '-NoProfile', '-NonInteractive', '-File', $resolved)
        $processArguments += $Arguments
    } else {
        $processArguments = $Arguments
    }

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $processPath
    $startInfo.WorkingDirectory = [System.IO.Path]::GetFullPath($WorkingDirectory)
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.StandardOutputEncoding = [System.Text.UTF8Encoding]::new($false)
    $startInfo.StandardErrorEncoding = [System.Text.UTF8Encoding]::new($false)
    foreach ($name in $ScrubEnvironmentNames) { [void]$startInfo.Environment.Remove($name) }
    foreach ($name in $EnvironmentVariables.Keys) {
        if ($null -eq $EnvironmentVariables[$name]) {
            [void]$startInfo.Environment.Remove([string]$name)
        } else {
            $startInfo.Environment[[string]$name] = [string]$EnvironmentVariables[$name]
        }
    }
    foreach ($argument in $processArguments) { $startInfo.ArgumentList.Add([string]$argument) }

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    if (-not $process.Start()) { Fail "Could not start process: $Executable" }
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $stderrTask = $process.StandardError.ReadToEndAsync()
    $process.WaitForExit()
    $stdout = $stdoutTask.GetAwaiter().GetResult()
    $stderr = $stderrTask.GetAwaiter().GetResult()
    $timer.Stop()
    $exitCode = $process.ExitCode
    $process.Dispose()

    if (-not [string]::IsNullOrWhiteSpace($LogPath)) {
        $log = "=== STDOUT ===`n$stdout`n=== STDERR ===`n$stderr"
        [System.IO.File]::WriteAllText($LogPath, $log, [System.Text.UTF8Encoding]::new($false))
    }
    return [pscustomobject]@{
        ExitCode = $exitCode
        StdOut = $stdout
        StdErr = $stderr
        Seconds = [Math]::Round($timer.Elapsed.TotalSeconds, 3)
        Executable = $resolved
    }
}

function Get-EnvironmentPolicy {
    $overrideNames = @(Get-ChildItem Env: | Where-Object {
        $name = $_.Name.ToUpperInvariant()
        $name -in @(
            'RUSTFLAGS', 'CARGO_ENCODED_RUSTFLAGS', 'CARGO_HOME', 'RUSTC', 'RUSTC_WRAPPER',
            'RUSTC_WORKSPACE_WRAPPER', 'RUSTUP_HOME', 'RUSTUP_TOOLCHAIN', 'CARGO_TARGET_DIR',
            'CARGO_BUILD_TARGET'
        ) -or $name.StartsWith('CARGO_PROFILE_') -or
            ($name.StartsWith('CARGO_TARGET_') -and $name.EndsWith('_RUSTFLAGS'))
    } | Sort-Object Name)
    $records = @()
    foreach ($entry in $overrideNames) {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes([string]$entry.Value)
        $hash = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
        $records += [ordered]@{ name = $entry.Name; valueSha256 = $hash }
    }
    $scrubNames = @($overrideNames | Where-Object {
        $name = $_.Name.ToUpperInvariant()
        $name -in @(
            'RUSTFLAGS', 'CARGO_ENCODED_RUSTFLAGS', 'RUSTC', 'RUSTC_WRAPPER',
            'RUSTC_WORKSPACE_WRAPPER', 'CARGO_TARGET_DIR', 'CARGO_BUILD_TARGET'
        ) -or $name.StartsWith('CARGO_PROFILE_') -or
            ($name.StartsWith('CARGO_TARGET_') -and $name.EndsWith('_RUSTFLAGS'))
    } | ForEach-Object { $_.Name })
    foreach ($record in $records) {
        $record.action = if ($scrubNames -contains $record.name) { 'scrubbed' } else { 'preserved' }
    }
    return [pscustomobject]@{ Records = @($records); ScrubNames = @($scrubNames) }
}

function Assert-NoReparsePath([string]$Root, [string]$TemporaryRoot) {
    $prefix = $TemporaryRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    if ([string]::Equals($Root, $TemporaryRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not $Root.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        Fail 'RunRoot must be a strict child of the current user TEMP directory.'
    }
    $relative = $Root.Substring($prefix.Length)
    $parts = $relative -split '[\\/]'
    $cursor = $TemporaryRoot
    for ($index = 0; $index -lt $parts.Length; $index++) {
        $cursor = Join-Path $cursor $parts[$index]
        if (Test-Path -LiteralPath $cursor) {
            $item = Get-Item -LiteralPath $cursor -Force -ErrorAction Stop
            if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                Fail "RunRoot traverses a reparse point: $cursor"
            }
            if ($index -eq ($parts.Length - 1)) {
                Fail "RunRoot already exists and is not available for a new PGO run: $Root"
            }
            if (-not $item.PSIsContainer) { Fail "RunRoot parent is not a directory: $cursor" }
        }
    }
}

function Get-OwnedTreeInventory([string]$Path, [string]$OwnedRoot) {
    $fullRoot = [System.IO.Path]::GetFullPath($OwnedRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $fullPath = [System.IO.Path]::GetFullPath($Path).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $prefix = $fullRoot + [System.IO.Path]::DirectorySeparatorChar
    if ($fullPath -ne $fullRoot -and -not $fullPath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        Fail "Owned-tree path escaped the PGO RunRoot: $fullPath"
    }
    if (-not (Test-Path -LiteralPath $fullPath -PathType Container)) {
        return [pscustomobject]@{ files = 0; bytes = [long]0 }
    }
    $files = [long]0
    $bytes = [long]0
    $pending = [System.Collections.Generic.Stack[string]]::new()
    $pending.Push($fullPath)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        foreach ($item in Get-ChildItem -LiteralPath $directory -Force -ErrorAction Stop) {
            $itemPath = [System.IO.Path]::GetFullPath($item.FullName)
            if (-not $itemPath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
                Fail "Owned-tree entry escaped the PGO RunRoot: $itemPath"
            }
            if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                Fail "Refusing to traverse a reparse point in owned output: $itemPath"
            }
            if ($item.PSIsContainer) {
                $pending.Push($itemPath)
            } else {
                $files++
                $bytes += [long]$item.Length
            }
        }
    }
    return [pscustomobject]@{ files = $files; bytes = $bytes }
}

function Remove-OwnedTree([string]$Path, [string]$OwnedRoot) {
    $fullRoot = [System.IO.Path]::GetFullPath($OwnedRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $fullPath = [System.IO.Path]::GetFullPath($Path).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    if (-not $fullPath.StartsWith(($fullRoot + [System.IO.Path]::DirectorySeparatorChar), [System.StringComparison]::OrdinalIgnoreCase)) {
        Fail "Refusing cleanup outside the owned PGO RunRoot: $fullPath"
    }
    if (-not (Test-Path -LiteralPath $fullPath)) { return }
    $item = Get-Item -LiteralPath $fullPath -Force -ErrorAction Stop
    if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { Fail "Refusing cleanup of reparse point: $fullPath" }
    if ($item.PSIsContainer) {
        [void](Get-OwnedTreeInventory $fullPath $fullRoot)
        Remove-Item -LiteralPath $fullPath -Recurse -Force -ErrorAction Stop
    } else {
        Remove-Item -LiteralPath $fullPath -Force -ErrorAction Stop
    }
}

function Get-SourceSnapshotSha256([string]$SourceInputsPath) {
    $records = Get-Content -LiteralPath $SourceInputsPath -Raw -Encoding UTF8 | ConvertFrom-Json -AsHashtable -Depth 100
    if ($records -isnot [array] -or $records.Count -eq 0) { Fail 'Source snapshot manifest must be a non-empty array.' }
    $paths = [string[]]@($records | ForEach-Object { [string]$_.path })
    [Array]::Sort($paths, [System.StringComparer]::Ordinal)
    $byPath = @{}
    foreach ($record in $records) {
        if (-not $record.Contains('path') -or -not $record.Contains('sha256')) { Fail 'Source snapshot record is missing path or sha256.' }
        Require-Sha256 $record.sha256 'source snapshot sha256'
        $byPath[[string]$record.path] = [string]$record.sha256
    }
    $builder = [System.Text.StringBuilder]::new()
    foreach ($path in $paths) { [void]$builder.Append($path).Append([char]0).Append($byPath[$path]).Append("`n") }
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($builder.ToString())
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Get-BuildIdentity([string]$SourceRoot, [string]$SourceInputsPath, [string]$TargetValue) {
    $rustc = Invoke-External $RustcExecutable @('-vV') -WorkingDirectory $SourceRoot
    if ($rustc.ExitCode -ne 0) { Fail "rustc version probe failed (exit $($rustc.ExitCode))." }
    $rustcLines = @($rustc.StdOut -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    $rustcVersion = $rustcLines | Where-Object { $_ -match '^rustc\s+' } | Select-Object -First 1
    $hostLine = $rustcLines | Where-Object { $_ -match '^host:\s*' } | Select-Object -First 1
    $rustcLlvmLine = $rustcLines | Where-Object { $_ -match '^LLVM version:\s*' } | Select-Object -First 1
    if (-not $rustcVersion -or -not $hostLine -or -not $rustcLlvmLine) { Fail 'rustc -vV did not report version, host, and LLVM version.' }
    $hostTarget = ($hostLine -replace '^host:\s*', '').Trim()
    if (-not [string]::Equals($hostTarget, $TargetValue, [System.StringComparison]::OrdinalIgnoreCase)) {
        Fail "PGO training target '$TargetValue' must match the local rustc host '$hostTarget'."
    }
    $rustcLlvmVersion = ($rustcLlvmLine -replace '^LLVM version:\s*', '').Trim()

    $cargo = Invoke-External $CargoExecutable @('--version') -WorkingDirectory $SourceRoot
    if ($cargo.ExitCode -ne 0) { Fail "cargo version probe failed (exit $($cargo.ExitCode))." }
    $cargoVersion = @($cargo.StdOut -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -First 1)[0]
    if ([string]::IsNullOrWhiteSpace($cargoVersion)) { Fail 'cargo --version returned no version string.' }

    $llvmExecutable = $LlvmProfdataExecutable
    if ([string]::IsNullOrWhiteSpace($llvmExecutable)) {
        $sysroot = Invoke-External $RustcExecutable @('--print', 'sysroot') -WorkingDirectory $SourceRoot
        if ($sysroot.ExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($sysroot.StdOut)) { Fail 'rustc --print sysroot failed.' }
        $llvmExecutable = Join-Path $sysroot.StdOut.Trim() (Join-Path 'lib/rustlib' (Join-Path $TargetValue 'bin/llvm-profdata.exe'))
    }
    $llvmExecutable = Resolve-ExecutablePath $llvmExecutable
    $llvm = Invoke-External $llvmExecutable @('--version') -WorkingDirectory $SourceRoot
    if ($llvm.ExitCode -ne 0) { Fail "llvm-profdata version probe failed (exit $($llvm.ExitCode))." }
    $llvmText = ($llvm.StdOut + "`n" + $llvm.StdErr).Trim()
    $llvmMatch = [regex]::Match($llvmText, 'LLVM(?: version)?\s*:?\s*([0-9]+\.[0-9]+\.[0-9]+)')
    $rustcLlvmMatch = [regex]::Match($rustcLlvmVersion, '^([0-9]+\.[0-9]+\.[0-9]+)')
    if (-not $llvmMatch.Success -or -not $rustcLlvmMatch.Success) { Fail 'Could not parse rustc and llvm-profdata LLVM versions.' }
    if ($llvmMatch.Groups[1].Value -cne $rustcLlvmMatch.Groups[1].Value) {
        Fail "llvm-profdata LLVM $($llvmMatch.Groups[1].Value) does not match rustc LLVM $($rustcLlvmMatch.Groups[1].Value)."
    }
    return [ordered]@{
        buildSourceSnapshotSha256 = Get-SourceSnapshotSha256 $SourceInputsPath
        rustcVersion = $rustcVersion.Trim()
        cargoVersion = $cargoVersion.Trim()
        llvmVersion = $rustcLlvmVersion
        target = $TargetValue
        rustcExecutable = Resolve-ExecutablePath $RustcExecutable
        cargoExecutable = Resolve-ExecutablePath $CargoExecutable
        llvmProfdataExecutable = $llvmExecutable
        llvmProfdataVersion = $llvmText
    }
}

function Invoke-RunTraining {
    if ([string]::IsNullOrWhiteSpace($TrainingManifest) -or [string]::IsNullOrWhiteSpace($EvaluationManifest)) {
        Fail 'Both -TrainingManifest and -EvaluationManifest are required.'
    }
    if ([string]::IsNullOrWhiteSpace($WorkloadCatalog)) { Fail '-WorkloadCatalog is required for -RunTraining.' }
    if ([string]::IsNullOrWhiteSpace($TrainingRunner)) { Fail '-TrainingRunner is required for -RunTraining.' }
    if ([string]::IsNullOrWhiteSpace($Target)) { Fail '-Target is required for -RunTraining.' }
    if ([string]::IsNullOrWhiteSpace($RunRoot)) { Fail '-RunRoot is required for -RunTraining.' }

    Set-PgoStage 'preflight-manifests'
    $trainingInput = Read-Manifest $TrainingManifest 'training' -AllowMissingBuildIdentity
    $evaluationInput = Read-Manifest $EvaluationManifest 'evaluation' -AllowMissingBuildIdentity -AllowMissingProfileMetadata
    Assert-MaterialSeparation $trainingInput $evaluationInput

    $temporaryRoot = [System.IO.Path]::GetFullPath($env:TEMP).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $script:PgoOwnedRoot = [System.IO.Path]::GetFullPath($RunRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    Assert-NoReparsePath $script:PgoOwnedRoot $temporaryRoot

    $rootDrive = [System.IO.Path]::GetPathRoot($script:PgoOwnedRoot)
    $driveName = $rootDrive.TrimEnd([char]'\').TrimEnd([char]':')
    if ([string]::IsNullOrWhiteSpace($driveName)) { Fail 'RunRoot must be on a local drive with available-space reporting.' }
    $drive = Get-PSDrive -Name $driveName -ErrorAction Stop
    $gib = [long]1073741824
    $buildReserveBytes = [long](16 * $gib)
    $maximumOwnedBytes = [long](24 * $gib)
    $minimumFreeBytes = [long](8 * $gib)
    if (($drive.Free - $buildReserveBytes) -lt $minimumFreeBytes) { Fail 'The PGO build reserve would leave less than 8 GiB free.' }

    [void][System.IO.Directory]::CreateDirectory($script:PgoOwnedRoot)
    $policy = Get-EnvironmentPolicy
    $script:PgoOwner = [ordered]@{
        schema = 1
        kind = 'nico-pgo-training'
        runRoot = $script:PgoOwnedRoot
        createdUtc = [DateTime]::UtcNow.ToString('o')
        status = 'running'
        stage = 'preflight'
        mode = $Mode
        target = $Target
        environmentOverrides = @($policy.Records)
        scrubbedEnvironmentNames = @($policy.ScrubNames)
        stages = [ordered]@{}
        cleanup = [ordered]@{}
    }
    Save-PgoOwner

    $sourceRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $PSScriptRoot))
    $sourceRoot = [System.IO.Path]::GetFullPath($sourceRoot)
    $manifestTool = Join-Path $sourceRoot 'scripts/nico_timeline_manifest.py'
    $sourceInputsPath = Join-Path $script:PgoOwnedRoot 'source-inputs.json'
    $trainingOutputPath = Join-Path $script:PgoOwnedRoot 'training.json'
    $evaluationOutputPath = Join-Path $script:PgoOwnedRoot 'evaluation.json'
    $workloadCatalogInputPath = Join-Path $script:PgoOwnedRoot 'workload-catalog-input.json'
    $workloadCatalogOutputPath = Join-Path $script:PgoOwnedRoot 'workload-catalog.json'
    $workloadEvidencePath = Join-Path $script:PgoOwnedRoot 'pgo-workload-runs.json'
    $targetDir = Join-Path $script:PgoOwnedRoot 'cargo-target'
    $instrumentedDir = Join-Path $script:PgoOwnedRoot 'instrumented'
    $helperPath = Join-Path $instrumentedDir 'nico-compositord.exe'
    $rawProfileDir = Join-Path $script:PgoOwnedRoot 'profraw'
    $profilePath = Join-Path $script:PgoOwnedRoot 'profile.profdata'
    $profilePattern = Join-Path $rawProfileDir '%m-%p.profraw'
    $ownerPath = Join-Path $script:PgoOwnedRoot 'pgo-owner.json'
    $resultPath = Join-Path $script:PgoOwnedRoot 'pgo-result.json'
    $cargoLogPath = Join-Path $script:PgoOwnedRoot 'cargo-training.log'
    $runnerLogPath = Join-Path $script:PgoOwnedRoot 'training.log'
    $mergeLogPath = Join-Path $script:PgoOwnedRoot 'profile-merge.log'
    $script:PgoTargetDir = $targetDir
    $script:PgoRawProfileDir = $rawProfileDir

    Set-PgoStage 'workload-catalog'
    $catalogSource = Get-CheckedInputFile $WorkloadCatalog 'Workload catalog' ([long](1MB))
    [System.IO.File]::Copy($catalogSource.Path, $workloadCatalogInputPath, $false)
    $catalogInputFile = Get-CheckedInputFile $workloadCatalogInputPath 'Copied workload catalog' ([long](1MB))
    $catalog = Read-WorkloadCatalog $workloadCatalogInputPath $trainingInput $evaluationInput
    $sourceCatalogSha256 = (Get-FileHash -LiteralPath $workloadCatalogInputPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $catalog = Copy-WorkloadInputsToOwnedRoot $catalog $script:PgoOwnedRoot
    Write-PgoJson $workloadCatalogOutputPath $catalog
    $catalog = Read-WorkloadCatalog $workloadCatalogOutputPath $trainingInput $evaluationInput
    $catalogOutputFile = Get-CheckedInputFile $workloadCatalogOutputPath 'Owned workload catalog' ([long](1MB))
    $inputCopies = Get-OwnedTreeInventory (Join-Path $script:PgoOwnedRoot 'inputs') $script:PgoOwnedRoot
    $script:PgoOwner.workloadCatalog = [ordered]@{
        path = $workloadCatalogOutputPath
        bytes = $catalogOutputFile.Bytes
        sha256 = (Get-FileHash -LiteralPath $workloadCatalogOutputPath -Algorithm SHA256).Hash.ToLowerInvariant()
        suppliedPath = $catalogSource.Path
        suppliedCatalogCopyPath = $workloadCatalogInputPath
        suppliedCatalogSha256 = $sourceCatalogSha256
        inputCopies = $inputCopies
        browserPath = [System.IO.Path]::GetFullPath([string]$catalog.browserPath)
        browserSha256 = (Get-FileHash -LiteralPath ([string]$catalog.browserPath) -Algorithm SHA256).Hash.ToLowerInvariant()
        trainingMaterialCount = $catalog.training.Count
        evaluationMaterialCount = $catalog.evaluation.Count
        sourceAndSnapshotHashesVerified = $true
    }
    Save-PgoOwner

    Set-PgoStage 'source-snapshot'
    $python = Resolve-ExecutablePath $PythonExecutable
    $snapshot = Invoke-External $python @($manifestTool, 'source-inputs', '--source-root', $sourceRoot, '--output', $sourceInputsPath) -WorkingDirectory $sourceRoot
    if ($snapshot.ExitCode -ne 0 -or -not (Test-Path -LiteralPath $sourceInputsPath -PathType Leaf)) {
        Fail "Source snapshot generation failed (exit $($snapshot.ExitCode))."
    }
    Set-PgoStage 'toolchain'
    $identity = Get-BuildIdentity $sourceRoot $sourceInputsPath $Target
    $script:PgoOwner.buildIdentity = $identity
    $script:PgoOwner.sourceInputsPath = $sourceInputsPath
    $script:PgoOwner.sourceInputsSha256 = (Get-FileHash -LiteralPath $sourceInputsPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $script:PgoOwner.toolchain = [ordered]@{
        rustcVersion = $identity.rustcVersion
        cargoVersion = $identity.cargoVersion
        llvmVersion = $identity.llvmVersion
        llvmProfdataVersion = $identity.llvmProfdataVersion
        target = $identity.target
    }

    $trainingOutput = [ordered]@{
        schemaVersion = 1
        role = 'training'
        materials = @($trainingInput.materials)
        buildIdentity = [ordered]@{
            buildSourceSnapshotSha256 = $identity.buildSourceSnapshotSha256
            rustcVersion = $identity.rustcVersion
            cargoVersion = $identity.cargoVersion
            llvmVersion = $identity.llvmVersion
            target = $identity.target
        }
    }
    $evaluationOutput = [ordered]@{
        schemaVersion = 1
        role = 'evaluation'
        materials = @($evaluationInput.materials)
        buildIdentity = [ordered]@{
            buildSourceSnapshotSha256 = $identity.buildSourceSnapshotSha256
            rustcVersion = $identity.rustcVersion
            cargoVersion = $identity.cargoVersion
            llvmVersion = $identity.llvmVersion
            target = $identity.target
        }
    }
    Write-PgoJson $trainingOutputPath $trainingOutput
    Write-PgoJson $evaluationOutputPath $evaluationOutput
    $script:PgoOwner.trainingManifestSha256 = Get-CanonicalSha256 $trainingOutput
    $script:PgoOwner.trainingManifestPath = $trainingOutputPath
    $script:PgoOwner.evaluationManifestPath = $evaluationOutputPath
    Save-PgoOwner

    Set-PgoStage 'cargo'
    [void][System.IO.Directory]::CreateDirectory($rawProfileDir)
    $rustflagsJson = ConvertTo-Json -InputObject @("-Cprofile-generate=$rawProfileDir") -Compress
    $cargoArguments = @(
        'build', '--profile', $Mode, '--locked', '--manifest-path', (Join-Path $sourceRoot 'gpu/nico-compositord/Cargo.toml'),
        '--target-dir', $targetDir, '--target', $Target, '--bin', 'nico-compositord', '--config', "build.rustflags=$rustflagsJson"
    )
    $cargoEnvironment = @{ LLVM_PROFILE_FILE = $profilePattern }
    $cargoBuild = Invoke-External $CargoExecutable $cargoArguments $cargoEnvironment $policy.ScrubNames $sourceRoot $cargoLogPath
    $script:PgoOwner.stages.cargo = [ordered]@{ exitCode = $cargoBuild.ExitCode; seconds = $cargoBuild.Seconds; log = $cargoLogPath }
    Save-PgoOwner
    if ($cargoBuild.ExitCode -ne 0) { Fail "Instrumented cargo build failed with exit $($cargoBuild.ExitCode)." }
    $candidateBinary = Join-Path $targetDir (Join-Path $Target (Join-Path $Mode 'nico-compositord.exe'))
    if (-not (Test-Path -LiteralPath $candidateBinary -PathType Leaf)) { Fail "Cargo did not produce the expected compositor helper: $candidateBinary" }
    $candidateItem = Get-Item -LiteralPath $candidateBinary -Force
    if (($candidateItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { Fail 'Instrumented helper is a reparse point.' }
    [void][System.IO.Directory]::CreateDirectory($instrumentedDir)
    Copy-Item -LiteralPath $candidateBinary -Destination $helperPath -Force -ErrorAction Stop
    $script:PgoOwner.instrumentedHelper = [ordered]@{
        path = $helperPath
        sha256 = (Get-FileHash -LiteralPath $helperPath -Algorithm SHA256).Hash.ToLowerInvariant()
        bytes = (Get-Item -LiteralPath $helperPath).Length
        instrumented = $true
    }
    $targetInventory = Get-OwnedTreeInventory $targetDir $script:PgoOwnedRoot
    $script:PgoOwner.cleanup.cargoTarget = [ordered]@{ path = $targetDir; files = $targetInventory.files; bytes = $targetInventory.bytes }
    Remove-OwnedTree $targetDir $script:PgoOwnedRoot
    $script:PgoOwner.cleanup.cargoTarget.removed = -not (Test-Path -LiteralPath $targetDir)
    Save-PgoOwner

    Set-PgoStage 'training'
    $runnerEnvironment = @{
        NICO_TIMELINE_WORKER_HELPER = $helperPath
        NICO_PGO_TRAINING_MANIFEST = $trainingOutputPath
        NICO_PGO_EVALUATION_MANIFEST = $evaluationOutputPath
        NICO_PGO_WORKLOAD_CATALOG = $workloadCatalogOutputPath
        NICO_PGO_RUN_ROOT = $script:PgoOwnedRoot
        LLVM_PROFILE_FILE = $profilePattern
    }
    $runnerResult = Invoke-External $TrainingRunner $TrainingRunnerArguments $runnerEnvironment @() $sourceRoot $runnerLogPath
    $runnerStage = if ($runnerResult.ExitCode -eq 130) { 'cancel' } else { 'training' }
    $script:PgoOwner.stages[$runnerStage] = [ordered]@{ exitCode = $runnerResult.ExitCode; seconds = $runnerResult.Seconds; log = $runnerLogPath }
    Save-PgoOwner
    if ($runnerResult.ExitCode -eq 130) { Set-PgoStage 'cancel'; Fail 'Training runner cancelled with exit 130.' }
    if ($runnerResult.ExitCode -ne 0) { Fail "Training runner failed with exit $($runnerResult.ExitCode)." }

    Set-PgoStage 'validate-workload-evidence'
    $workloadEvidence = Assert-WorkloadEvidence $workloadEvidencePath $script:PgoOwnedRoot $helperPath $script:PgoOwner.instrumentedHelper.sha256 $trainingOutput $catalog
    $script:PgoOwner.workloadEvidence = $workloadEvidence
    Save-PgoOwner

    $rawProfiles = @()
    foreach ($rawProfile in Get-ChildItem -LiteralPath $rawProfileDir -Filter '*.profraw' -File -Force -ErrorAction Stop) {
        $rawFullPath = [System.IO.Path]::GetFullPath($rawProfile.FullName)
        if (-not $rawFullPath.StartsWith(([System.IO.Path]::GetFullPath($rawProfileDir).TrimEnd('\') + '\'), [System.StringComparison]::OrdinalIgnoreCase)) {
            Fail "Raw profile escaped its owned directory: $rawFullPath"
        }
        if (($rawProfile.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or $rawProfile.Length -le 0) {
            Fail "Raw profile is a reparse point or empty: $rawFullPath"
        }
        $rawProfiles += [ordered]@{
            path = $rawFullPath
            bytes = [long]$rawProfile.Length
            sha256 = (Get-FileHash -LiteralPath $rawFullPath -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    }
    if ($rawProfiles.Count -eq 0) { Fail 'Training completed without producing any profraw files.' }
    $script:PgoOwner.rawProfiles = @($rawProfiles)

    Set-PgoStage 'merge'
    $mergeArguments = @('merge', '--output', $profilePath)
    $mergeArguments += @($rawProfiles | ForEach-Object { [string]$_.path })
    $merge = Invoke-External $identity.llvmProfdataExecutable $mergeArguments @{} @() $sourceRoot $mergeLogPath
    $script:PgoOwner.stages.merge = [ordered]@{ exitCode = $merge.ExitCode; seconds = $merge.Seconds; log = $mergeLogPath }
    Save-PgoOwner
    if ($merge.ExitCode -ne 0) { Fail "llvm-profdata merge failed with exit $($merge.ExitCode)." }
    if (-not (Test-Path -LiteralPath $profilePath -PathType Leaf) -or (Get-Item -LiteralPath $profilePath).Length -le 0) {
        Fail 'llvm-profdata merge did not create a non-empty profile.'
    }
    $profileSha = (Get-FileHash -LiteralPath $profilePath -Algorithm SHA256).Hash.ToLowerInvariant()

    Set-PgoStage 'validate'
    $evaluationOutput.profileMetadata = [ordered]@{
        trainingManifestSha256 = Get-CanonicalSha256 $trainingOutput
        sourceSnapshotSha256 = $identity.buildSourceSnapshotSha256
        rustcVersion = $identity.rustcVersion
        cargoVersion = $identity.cargoVersion
        llvmVersion = $identity.llvmVersion
        target = $identity.target
        profileSha256 = $profileSha
        instrumented = $false
    }
    Write-PgoJson $evaluationOutputPath $evaluationOutput
    $validatedTraining = Read-Manifest $trainingOutputPath 'training'
    $validatedEvaluation = Read-Manifest $evaluationOutputPath 'evaluation'
    Assert-ManifestsValid $validatedTraining $validatedEvaluation -RequireProfileMetadata

    $finalInventory = Get-OwnedTreeInventory $script:PgoOwnedRoot $script:PgoOwnedRoot
    if ($finalInventory.bytes -gt $maximumOwnedBytes) { Fail 'PGO outputs exceeded the 24 GiB owned-run budget.' }
    $script:PgoOwner.status = 'complete'
    $script:PgoOwner.stage = 'complete'
    $script:PgoOwner.completedUtc = [DateTime]::UtcNow.ToString('o')
    $script:PgoOwner.profile = [ordered]@{ path = $profilePath; sha256 = $profileSha; bytes = (Get-Item -LiteralPath $profilePath).Length; instrumented = $false }
    $script:PgoOwner.finalInventory = $finalInventory
    $result = [ordered]@{
        schema = 1
        kind = 'nico-pgo-training-result'
        runRoot = $script:PgoOwnedRoot
        mode = $Mode
        target = $Target
        trainingManifestPath = $trainingOutputPath
        trainingManifestSha256 = Get-CanonicalSha256 $trainingOutput
        evaluationManifestPath = $evaluationOutputPath
        workloadCatalog = $script:PgoOwner.workloadCatalog
        workloadEvidence = $workloadEvidence
        buildIdentity = $trainingOutput.buildIdentity
        instrumentedHelper = $script:PgoOwner.instrumentedHelper
        profile = $script:PgoOwner.profile
        stages = $script:PgoOwner.stages
        rawProfiles = @($rawProfiles)
        trainingBinaryExcludedFromPerformanceSamples = $true
    }
    Write-PgoJson $resultPath $result
    Save-PgoOwner
    $script:PgoSucceeded = $true
    Write-Output "pgo_training_complete=true"
    Write-Output "pgo_run_root=$script:PgoOwnedRoot"
    Write-Output "profile_sha256=$profileSha"
}

$script:PgoOwner = $null
$script:PgoOwnedRoot = ''
$script:PgoTargetDir = ''
$script:PgoRawProfileDir = ''
$script:PgoStage = 'preflight'
$script:PgoSucceeded = $false

try {
    $selectedModes = @($ValidateManifests, $RunTraining, $BuildProfileUse | Where-Object { $_ })
    if (@($selectedModes | Where-Object { $_ }).Count -ne 1) {
        Fail 'Select exactly one mode: -ValidateManifests, -RunTraining, or -BuildProfileUse.'
    }
    if ($ValidateManifests) {
        if ([string]::IsNullOrWhiteSpace($TrainingManifest) -or [string]::IsNullOrWhiteSpace($EvaluationManifest)) {
            Fail 'Both -TrainingManifest and -EvaluationManifest are required.'
        }
        $training = Read-Manifest $TrainingManifest 'training'
        $evaluation = Read-Manifest $EvaluationManifest 'evaluation'
        Assert-ManifestsValid $training $evaluation -RequireProfileMetadata
        Write-Output 'manifests_valid=true'
    } elseif ($RunTraining) {
        Invoke-RunTraining
    } else {
        if ([string]::IsNullOrWhiteSpace($PgoRunRoot)) { Fail '-PgoRunRoot is required for -BuildProfileUse.' }
        if ([string]::IsNullOrWhiteSpace($RunRoot)) { Fail '-RunRoot is required for the profile-use build output.' }
        $buildRunner = Join-Path $PSScriptRoot 'build-variants.ps1'
        $pwshCommand = Get-Command -Name 'pwsh' -CommandType Application -ErrorAction Stop | Select-Object -First 1
        $buildArguments = @(
            '-NoLogo', '-NoProfile', '-NonInteractive', '-File', $buildRunner,
            '-Mode', "$Mode-pgo", '-RunRoot', $RunRoot, '-PgoRunRoot', $PgoRunRoot
        )
        if (-not [string]::IsNullOrWhiteSpace($Target)) { $buildArguments += @('-Target', $Target) }
        & $pwshCommand.Source @buildArguments
        if ($LASTEXITCODE -ne 0) { Fail "Profile-use build selector failed with exit $LASTEXITCODE." }
    }
} catch {
    $errorMessage = $_.Exception.Message
    if ($null -ne $script:PgoOwner) {
        $script:PgoOwner.status = 'failed'
        $script:PgoOwner.stage = $script:PgoStage
        $script:PgoOwner.error = $errorMessage
        try {
            if (-not $script:PgoSucceeded -and -not [string]::IsNullOrWhiteSpace($script:PgoTargetDir)) {
                if (Test-Path -LiteralPath $script:PgoTargetDir) {
                    $targetInventory = Get-OwnedTreeInventory $script:PgoTargetDir $script:PgoOwnedRoot
                    $script:PgoOwner.cleanup.cargoTarget = [ordered]@{
                        path = $script:PgoTargetDir; files = $targetInventory.files; bytes = $targetInventory.bytes; removed = $true
                    }
                    Remove-OwnedTree $script:PgoTargetDir $script:PgoOwnedRoot
                }
            }
            if (-not $script:PgoSucceeded -and -not [string]::IsNullOrWhiteSpace($script:PgoRawProfileDir)) {
                if (Test-Path -LiteralPath $script:PgoRawProfileDir) {
                    $rawInventory = Get-OwnedTreeInventory $script:PgoRawProfileDir $script:PgoOwnedRoot
                    $script:PgoOwner.cleanup.profraw = [ordered]@{
                        path = $script:PgoRawProfileDir; files = $rawInventory.files; bytes = $rawInventory.bytes; removed = $true
                    }
                    Remove-OwnedTree $script:PgoRawProfileDir $script:PgoOwnedRoot
                }
            }
            if (-not $script:PgoSucceeded -and -not [string]::IsNullOrWhiteSpace($script:PgoOwnedRoot)) {
                Remove-OwnedTree (Join-Path $script:PgoOwnedRoot 'profile.profdata') $script:PgoOwnedRoot
                Remove-OwnedTree (Join-Path $script:PgoOwnedRoot 'pgo-result.json') $script:PgoOwnedRoot
            }
            Save-PgoOwner
        } catch {
            $cleanupError = $_.Exception.Message
            $script:PgoOwner.cleanupError = $cleanupError
            try { Save-PgoOwner } catch { }
            [Console]::Error.WriteLine("PGO cleanup warning: $cleanupError")
        }
    }
    [Console]::Error.WriteLine("PGO stage '$script:PgoStage' failed: $errorMessage")
    exit 1
}

exit 0
