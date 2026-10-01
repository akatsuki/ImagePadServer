[CmdletBinding(DefaultParameterSetName = 'Inventory')]
param(
    [Parameter(Mandatory = $true, ParameterSetName = 'Inventory')][string]$SourceVideo,
    [Parameter(Mandatory = $true, ParameterSetName = 'Inventory')][string]$Snapshot,
    [Parameter(Mandatory = $true, ParameterSetName = 'Inventory')][string]$Ffmpeg,
    [Parameter(Mandatory = $true, ParameterSetName = 'Inventory')][string]$Worker,
    [Parameter(Mandatory = $true, ParameterSetName = 'Inventory')][string]$Helper,
    [Parameter(Mandatory = $true, ParameterSetName = 'Inventory')][string]$SourceRoot,
    [Parameter(Mandatory = $true, ParameterSetName = 'Inventory')][string]$SourceManifest,
    [Parameter(Mandatory = $true, ParameterSetName = 'Inventory')][string]$OutputPath,
    [Parameter(Mandatory = $true, ParameterSetName = 'ValidateManifest')][string]$RunManifest,
    [Parameter(Mandatory = $true, ParameterSetName = 'ValidateManifest')][ValidateSet('cold', 'warm')][string]$RequestedSessionMode,
    [Parameter(Mandatory = $true, ParameterSetName = 'ValidateManifest')][ValidateSet('NCT1', 'NCT2')][string]$RequestedStreamProtocol,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][switch]$RunColdCase,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$RunSourceVideo,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$RunSnapshot,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$RunFfmpeg,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$BaselineWorker,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$CandidateWorker,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$RunHelper,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateSet('baseline', 'candidate')][string]$SelectedWorker,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateSet('cold', 'warm')][string]$SessionMode,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateSet('NCT1', 'NCT2')][string]$StreamProtocol,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$BaselineId,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$CandidateId,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$PairId,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string[]]$RawOwnedManifests,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string[]]$BuildOwnedManifests,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateRange(1, 16384)][int]$Width,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateRange(1, 16384)][int]$Height,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateRange(1, 3600000)][int]$DurationMs,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateRange(1, 1000000)][int]$FpsNum,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateRange(1, 1000000)][int]$FpsDen,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateSet('nvenc', 'x264')][string]$Encoder,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateSet('tee', 'mp4', 'hls')][string]$OutputMode,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateSet('vulkan', 'd3d12', 'metal')][string]$GpuBackend,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateSet(0, 20)][int]$CpuLimit,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][ValidateRange(1, 3600)][int]$TimeoutSeconds,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$RunRoot,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$RunManifestPath,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$GoExecutable,
    [Parameter(Mandatory = $true, ParameterSetName = 'RunColdCase')][string]$GoArgumentsJson
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if (-not ('NicoColdCaseCancelSignal' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Threading;
public static class NicoColdCaseCancelSignal
{
    private static bool requested;
    public static bool Requested { get { return Volatile.Read(ref requested); } }
    public static void Register() { Console.CancelKeyPress += Handle; }
    public static void Unregister() { Console.CancelKeyPress -= Handle; Volatile.Write(ref requested, false); }
    private static void Handle(object sender, ConsoleCancelEventArgs args)
    {
        args.Cancel = true;
        Volatile.Write(ref requested, true);
    }
}
'@
}

function Resolve-RegularFile([string]$Path, [string]$Label) {
    if ([string]::IsNullOrWhiteSpace($Path)) { throw "$Label path is empty." }
    $resolved = Resolve-Path -LiteralPath $Path -ErrorAction Stop
    $item = Get-Item -LiteralPath $resolved.ProviderPath -Force -ErrorAction Stop
    if ($item -isnot [System.IO.FileInfo] -or
        ($item.Attributes -band [System.IO.FileAttributes]::Directory) -ne 0) {
        throw "$Label must resolve to a regular file: $Path"
    }
    return $item
}

function Get-Sha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
}

function Test-HashString([object]$Value) {
    return ($Value -is [string]) -and ($Value -cmatch '^[0-9a-fA-F]{64}$')
}

function Test-NonEmptyString([object]$Value) {
    return ($Value -is [string]) -and -not [string]::IsNullOrWhiteSpace($Value)
}

function Write-ColdRunManifest([string]$Path, [object]$Value) {
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $temporary = $Path + '.' + [Guid]::NewGuid().ToString('N') + '.tmp'
    $bytes = $utf8.GetBytes((ConvertTo-Json -InputObject $Value -Depth 8) + "`n")
    try {
        $stream = [System.IO.File]::Open($temporary, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        try { $stream.Write($bytes, 0, $bytes.Length) }
        finally { $stream.Dispose() }
        [System.IO.File]::Move($temporary, $Path)
    }
    finally {
        if ([System.IO.File]::Exists($temporary)) {
            [System.IO.File]::Delete($temporary)
        }
    }
}

function Write-OwnedArtifactManifest([string]$Root, [string]$Path) {
    $records = [System.Collections.Generic.List[object]]::new()
    $pendingDirectories = [System.Collections.Generic.Stack[string]]::new()
    $pendingDirectories.Push($Root)
    while ($pendingDirectories.Count -gt 0) {
        $directory = $pendingDirectories.Pop()
        foreach ($item in Get-ChildItem -LiteralPath $directory -Force) {
            if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Run output contains a reparse point: $($item.FullName)"
            }
            if ($item -is [System.IO.DirectoryInfo]) {
                $pendingDirectories.Push($item.FullName)
            }
            elseif ($item -is [System.IO.FileInfo]) {
                $records.Add([ordered]@{
                    path = $item.FullName
                    size_bytes = [long]$item.Length
                    sha256 = Get-Sha256 $item.FullName
                    purpose = 'T0.4 cold-case child output or diagnostic'
                })
            }
        }
    }
    $manifest = [ordered]@{ schema_version = 1; root = $Root; artifacts = $records.ToArray() }
    $json = ConvertTo-Json -InputObject $manifest -Depth 6
    [System.IO.File]::WriteAllText($Path, $json + "`n", [System.Text.UTF8Encoding]::new($false))
    Import-Module (Join-Path $PSScriptRoot 'next-speed-artifacts.psm1') -Force
    return Test-NicoOwnedArtifacts -Root $Root -ManifestPath $Path
}

function Get-FileEvidence([string]$Path, [string]$Label) {
    $item = Resolve-RegularFile $Path $Label
    return [ordered]@{ path = $item.FullName; size_bytes = [long]$item.Length; sha256 = Get-Sha256 $item.FullName }
}

function Get-OwnedCapacityEvidence([string[]]$RawPaths, [string[]]$BuildPaths, [scriptblock]$FreeSpaceSnapshotProvider = $null) {
    if ($RawPaths.Count -eq 0 -or $BuildPaths.Count -eq 0) {
        throw 'At least one validated raw owned-artifact manifest and one build owned-artifact manifest are required.'
    }
    Import-Module (Join-Path $PSScriptRoot 'next-speed-artifacts.psm1') -Force
    $manifestEvidence = [System.Collections.Generic.List[object]]::new()
    $categoryEvidence = @{}
    $volumeSnapshots = [System.Collections.Generic.Dictionary[string, object]]::new([System.StringComparer]::OrdinalIgnoreCase)
    foreach ($category in @(
        @{ Name = 'raw'; Paths = $RawPaths },
        @{ Name = 'build'; Paths = $BuildPaths }
    )) {
        $total = [long]0
        $volumes = [System.Collections.Generic.Dictionary[string, object]]::new([System.StringComparer]::OrdinalIgnoreCase)
        foreach ($manifestPath in $category.Paths) {
            $manifestItem = Resolve-RegularFile $manifestPath "$($category.Name) owned-artifact manifest"
            $owned = Test-NicoOwnedArtifacts -Root ((Get-Content -LiteralPath $manifestItem.FullName -Raw -Encoding UTF8 | ConvertFrom-Json).root) -ManifestPath $manifestItem.FullName
            $total += [long]$owned.total_bytes
            $rootDrive = [System.IO.DriveInfo]::new([System.IO.Path]::GetPathRoot($owned.root))
            if (-not $volumeSnapshots.ContainsKey($rootDrive.Name)) {
                $sampledUtc = [DateTime]::UtcNow.ToString('o')
                $freeBytes = if ($null -ne $FreeSpaceSnapshotProvider) {
                    [long](& $FreeSpaceSnapshotProvider $rootDrive.Name)
                }
                else {
                    [long]$rootDrive.AvailableFreeSpace
                }
                if ($freeBytes -lt 0) { throw "Volume free-space snapshot is invalid for $($rootDrive.Name)." }
                $volumeSnapshots[$rootDrive.Name] = [ordered]@{
                    volume_id = $rootDrive.Name; free_bytes = $freeBytes; sampled_utc = $sampledUtc
                }
            }
            $snapshot = $volumeSnapshots[$rootDrive.Name]
            $volumes[$rootDrive.Name] = $snapshot
            $manifestEvidence.Add([ordered]@{
                category = $category.Name; path = $manifestItem.FullName; sha256 = Get-Sha256 $manifestItem.FullName
                root = $owned.root; total_bytes = [long]$owned.total_bytes; volume_id = $rootDrive.Name
                free_bytes = [long]$snapshot.free_bytes; free_space_sampled_utc = $snapshot.sampled_utc
            })
        }
        if ($volumes.Count -ne 1) { throw "T0.4 requires $($category.Name) owned manifests to be on one validated volume." }
        $categoryEvidence[$category.Name] = [ordered]@{
            total_bytes = $total; volume_id = @($volumes.Keys)[0]; free_bytes = [long]@($volumes.Values)[0].free_bytes
        }
    }
    return [ordered]@{
        manifests = $manifestEvidence.ToArray()
        raw = $categoryEvidence.raw
        build = $categoryEvidence.build
        volume_snapshots = @($volumeSnapshots.Values)
    }
}

function Invoke-ColdCase {
    $root = [System.IO.Path]::GetFullPath($RunRoot)
    $manifestPath = [System.IO.Path]::GetFullPath($RunManifestPath)
    if ([System.IO.Directory]::Exists($root) -or [System.IO.File]::Exists($root)) { throw "RunRoot must be a new path: $root" }
    if ([System.IO.Directory]::Exists($manifestPath) -or [System.IO.File]::Exists($manifestPath)) { throw "RunManifestPath must be a new path: $manifestPath" }
    $manifestRelative = [System.IO.Path]::GetRelativePath($root, $manifestPath)
    $manifestIsInsideRoot = $manifestRelative -eq '.' -or (
        -not [System.IO.Path]::IsPathRooted($manifestRelative) -and
        $manifestRelative -ne '..' -and
        -not $manifestRelative.StartsWith('..' + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase) -and
        -not $manifestRelative.StartsWith('..' + [System.IO.Path]::AltDirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)
    )
    if ($manifestIsInsideRoot) {
        throw 'RunManifestPath must be outside RunRoot.'
    }
    $parent = [System.IO.Path]::GetDirectoryName($root)
    if (-not [System.IO.Directory]::Exists($parent)) { throw "RunRoot parent must already exist: $parent" }
    $manifestParent = [System.IO.Path]::GetDirectoryName($manifestPath)
    if (-not [System.IO.Directory]::Exists($manifestParent)) { throw "Run manifest parent must already exist: $manifestParent" }
    $workingDirectory = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../../..'))
    [void][System.IO.Directory]::CreateDirectory($root)

    $selectedPath = if ($SelectedWorker -ceq 'baseline') { $BaselineWorker } else { $CandidateWorker }
    $candidateInfo = $null
    $failure = $null
    $status = 'failed'
    $skipReason = $null
    $exitCode = $null
    $started = [DateTime]::UtcNow
    $childLog = [System.IO.Path]::Combine($root, 'child.log')
    $ownedManifestPath = [System.IO.Path]::Combine($root, 'owned-artifacts.json')
    $ownedBytes = $null
    $ownedManifestHash = $null
    $executableHash = $null
    $executableSize = $null
    $capacity = $null
    $capacityEvidence = $null
    $inputEvidence = $null
    $childCommand = $null
    $workerConditions = [ordered]@{
        width = $Width; height = $Height; duration_ms = $DurationMs; fps_num = $FpsNum; fps_den = $FpsDen
        encoder = $Encoder; output_mode = $OutputMode; gpu_backend = $GpuBackend; cpu_limit = $CpuLimit
    }
    $process = $null
    $cancelRegistered = $false
    try {
        foreach ($id in @(@{ name = 'BaselineId'; value = $BaselineId }, @{ name = 'CandidateId'; value = $CandidateId }, @{ name = 'PairId'; value = $PairId })) {
            if ($id.value -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$') { throw "$($id.name) has invalid format." }
        }
        if ($SessionMode -cne 'cold') {
            $status = 'skipped'; $skipReason = 'T0.4 only implements cold sessions.'
        }
        elseif ($StreamProtocol -cne 'NCT1') {
            $status = 'skipped'; $skipReason = 'T0.4 only implements NCT1.'
        }
        else {
            $capacityEvidence = Get-OwnedCapacityEvidence $RawOwnedManifests $BuildOwnedManifests
            $drive = [System.IO.DriveInfo]::new([System.IO.Path]::GetPathRoot($root))
            if (-not [string]::Equals($capacityEvidence.raw.volume_id, $drive.Name, [System.StringComparison]::OrdinalIgnoreCase)) {
                throw 'RunRoot must be on the same volume as the validated raw owned-artifact manifests.'
            }
            $rawDrive = [System.IO.DriveInfo]::new($capacityEvidence.raw.volume_id)
            $buildDrive = [System.IO.DriveInfo]::new($capacityEvidence.build.volume_id)
            $capacityEvidence.raw.free_bytes = [long]$rawDrive.AvailableFreeSpace
            $capacityEvidence.build.free_bytes = if ([string]::Equals($rawDrive.Name, $buildDrive.Name, [System.StringComparison]::OrdinalIgnoreCase)) {
                [long]$capacityEvidence.raw.free_bytes
            }
            else { [long]$buildDrive.AvailableFreeSpace }
            $capacity = Test-NicoCapacityReservation -ExistingRawBytes $capacityEvidence.raw.total_bytes -ReserveRawBytes (4GB) `
                -RawVolumeId $drive.Name -RawVolumeFreeBytes ([long]$capacityEvidence.raw.free_bytes) `
                -ExistingBuildBytes $capacityEvidence.build.total_bytes -BuildVolumeId $capacityEvidence.build.volume_id `
                -BuildVolumeFreeBytes $capacityEvidence.build.free_bytes
            if (-not $capacity.accepted) { throw "Capacity reservation rejected: $($capacity.rejection_codes -join ',')" }
            $inputEvidence = [ordered]@{
                source_video = Get-FileEvidence $RunSourceVideo 'source video'
                snapshot = Get-FileEvidence $RunSnapshot 'snapshot'
                ffmpeg = Get-FileEvidence $RunFfmpeg 'FFmpeg'
                helper = Get-FileEvidence $RunHelper 'helper'
            }
            if (Test-Path -LiteralPath $selectedPath -PathType Leaf) {
                $candidateInfo = Resolve-RegularFile $selectedPath 'selected worker'
                $executableHash = Get-Sha256 $candidateInfo.FullName
                $executableSize = [long]$candidateInfo.Length
            }
            else {
                $failure = 'selected worker candidate is missing'
            }

            if ($null -eq $candidateInfo) { $status = 'failed' }
            else {
            $resolvedGo = Resolve-RegularFile $GoExecutable 'Go test executable'
            $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
            $startInfo.FileName = $resolvedGo.FullName
            $startInfo.WorkingDirectory = $workingDirectory
            $startInfo.UseShellExecute = $false
            $startInfo.RedirectStandardOutput = $true
            $startInfo.RedirectStandardError = $true
            $arguments = ConvertFrom-Json -InputObject $GoArgumentsJson
            if ($arguments -isnot [System.Array] -or $arguments.Count -eq 0) { throw 'GoArgumentsJson must be a non-empty JSON string array.' }
            foreach ($argument in $arguments) {
                if ($argument -isnot [string]) { throw 'GoArgumentsJson entries must be strings.' }
                $startInfo.ArgumentList.Add($argument)
            }
            $childCommand = [ordered]@{
                executable = $resolvedGo.FullName; size_bytes = [long]$resolvedGo.Length
                sha256 = Get-Sha256 $resolvedGo.FullName; arguments = @($arguments); working_directory = $workingDirectory
            }
            foreach ($environmentName in @($startInfo.Environment.Keys)) {
                if ($environmentName.StartsWith('NICO_TIMELINE_WORKER_', [System.StringComparison]::OrdinalIgnoreCase)) {
                    [void]$startInfo.Environment.Remove($environmentName)
                }
            }
            $startInfo.Environment['NICO_TIMELINE_WORKER_PERF'] = '1'
            $startInfo.Environment['NICO_TIMELINE_WORKER_EXE'] = $candidateInfo.FullName
            $startInfo.Environment['NICO_TIMELINE_WORKER_SOURCE'] = $inputEvidence.source_video.path
            $startInfo.Environment['NICO_TIMELINE_WORKER_SNAPSHOT'] = $inputEvidence.snapshot.path
            $startInfo.Environment['NICO_TIMELINE_WORKER_FFMPEG'] = $inputEvidence.ffmpeg.path
            $startInfo.Environment['NICO_TIMELINE_WORKER_HELPER'] = $inputEvidence.helper.path
            $startInfo.Environment['NICO_TIMELINE_WORKER_OUTPUT_ROOT'] = [System.IO.Path]::Combine($root, 'worker-output')
            $startInfo.Environment['NICO_TIMELINE_WORKER_WIDTH'] = [string]$Width
            $startInfo.Environment['NICO_TIMELINE_WORKER_HEIGHT'] = [string]$Height
            $startInfo.Environment['NICO_TIMELINE_WORKER_DURATION_MS'] = [string]$DurationMs
            $startInfo.Environment['NICO_TIMELINE_WORKER_FPS_NUM'] = [string]$FpsNum
            $startInfo.Environment['NICO_TIMELINE_WORKER_FPS_DEN'] = [string]$FpsDen
            $startInfo.Environment['NICO_TIMELINE_WORKER_ENCODER'] = $Encoder
            $startInfo.Environment['NICO_TIMELINE_WORKER_OUTPUT_MODE'] = $OutputMode
            $startInfo.Environment['NICO_TIMELINE_WORKER_GPU_BACKEND'] = $GpuBackend
            $startInfo.Environment['NICO_TIMELINE_WORKER_TEST_CPU_PERCENT'] = [string]$CpuLimit
            $process = [System.Diagnostics.Process]::new()
            $process.StartInfo = $startInfo
            [NicoColdCaseCancelSignal]::Register()
            $cancelRegistered = $true
            try {
                if (-not $process.Start()) { throw 'Could not start the cold-case child process.' }
                $stdoutTask = $process.StandardOutput.ReadToEndAsync()
                $stderrTask = $process.StandardError.ReadToEndAsync()
                $terminationReason = $null
                $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
                while (-not $process.WaitForExit(100)) {
                    if ([NicoColdCaseCancelSignal]::Requested) { $terminationReason = 'cancelled'; break }
                    if ([DateTime]::UtcNow -ge $deadline) { $terminationReason = 'timeout'; break }
                }
                if ($terminationReason) {
                    if (-not $process.HasExited) { $process.Kill($true) }
                    if (-not $process.WaitForExit(15000)) { throw 'The child process did not exit after entire-tree termination.' }
                    $stdout = $stdoutTask.GetAwaiter().GetResult()
                    $stderr = $stderrTask.GetAwaiter().GetResult()
                    $exitCode = $process.ExitCode
                    $failure = if ($terminationReason -eq 'timeout') { "child timed out after $TimeoutSeconds seconds" } else { 'runner cancelled the child process' }
                    [System.IO.File]::WriteAllText($childLog, "termination=$terminationReason`nstdout:`n$stdout`nstderr:`n$stderr", [System.Text.UTF8Encoding]::new($false))
                }
                else {
                $stdout = $stdoutTask.GetAwaiter().GetResult()
                $stderr = $stderrTask.GetAwaiter().GetResult()
                $exitCode = $process.ExitCode
                [System.IO.File]::WriteAllText($childLog, "stdout:`n$stdout`nstderr:`n$stderr", [System.Text.UTF8Encoding]::new($false))
                if ($exitCode -eq 0) { $status = 'completed' }
                else { $failure = "child exited with code $exitCode" }
                }
            }
            finally {
                if ($cancelRegistered) { [NicoColdCaseCancelSignal]::Unregister() }
                if ($process -and -not $process.HasExited) {
                    try { $process.Kill($true) } catch { }
                    if (-not $process.WaitForExit(15000)) { throw 'The child process tree remained after cleanup.' }
                }
                if ($process) { $process.Dispose() }
            }
            }
        }
    }
    catch { $failure = $_.Exception.Message }
    finally {
        if (-not [System.IO.File]::Exists($childLog)) {
            $reason = if (-not [string]::IsNullOrWhiteSpace($skipReason)) { $skipReason } else { $failure }
            [System.IO.File]::WriteAllText($childLog, "child_not_started=$reason`n", [System.Text.UTF8Encoding]::new($false))
        }
        try {
            $owned = Write-OwnedArtifactManifest $root $ownedManifestPath
            $ownedBytes = [long]$owned.total_bytes
            $ownedManifestHash = Get-Sha256 $ownedManifestPath
        }
        catch {
            $status = 'failed'
            if ([string]::IsNullOrWhiteSpace($failure)) { $failure = "owned artifact validation failed: $($_.Exception.Message)" }
            else { $failure += "; owned artifact validation failed: $($_.Exception.Message)" }
        }
        $run = [ordered]@{
            plan_version = 4; task_id = 'T0.4'; candidate_id = $CandidateId; baseline_id = $BaselineId
            pair_id = $PairId; session_mode = $SessionMode
            process_generation = 1; browser_reused = $false; stream_protocol = $StreamProtocol
            build_profile = 'release'; training_manifest_hash = $null
            worker_conditions = $workerConditions
            inputs = $inputEvidence
            run_status = $status; sample_eligible = ($status -ceq 'completed')
            started_utc = $started.ToString('o'); finished_utc = [DateTime]::UtcNow.ToString('o')
            working_directory = $workingDirectory; timeout_seconds = $TimeoutSeconds
            child_command = $childCommand
            executable = [ordered]@{
                selected_role = $SelectedWorker; path = if ($null -ne $candidateInfo) { $candidateInfo.FullName } else { $null }
                size_bytes = $executableSize
                sha256 = $executableHash
            }
            child_exit_code = $exitCode; child_log = if ([System.IO.File]::Exists($childLog)) { $childLog } else { $null }
            owned_artifact_manifest = $ownedManifestPath; owned_artifact_manifest_sha256 = $ownedManifestHash
            owned_artifact_bytes = $ownedBytes
            capacity_evidence = $capacityEvidence; capacity_reservation = $capacity
            skip_reason = $skipReason; failure_reason = $failure
        }
        Write-ColdRunManifest $manifestPath $run
    }
    if ($status -ceq 'skipped') { Write-Output "run_status=skipped sample_eligible=false reason=$skipReason"; exit 0 }
    if ($status -cne 'completed') { [Console]::Error.WriteLine("T0.4 cold case failed: $failure"); exit 1 }
    Write-Output "run_status=completed sample_eligible=true executable=$($candidateInfo.FullName) sha256=$(Get-Sha256 $candidateInfo.FullName)"
    exit 0
}

$tempPath = $null
try {
    if ($PSCmdlet.ParameterSetName -eq 'RunColdCase') { Invoke-ColdCase }
    if ($PSCmdlet.ParameterSetName -eq 'ValidateManifest') {
        $runManifestItem = Resolve-RegularFile $RunManifest 'run manifest'
        $run = Get-Content -LiteralPath $runManifestItem.FullName -Raw -Encoding UTF8 | ConvertFrom-Json
        $requiredFields = @(
            'plan_version', 'task_id', 'candidate_id', 'baseline_id', 'pair_id',
            'session_mode', 'process_generation', 'browser_reused', 'stream_protocol',
            'build_profile', 'training_manifest_hash', 'run_status', 'skip_reason'
        )
        $actualNames = @($run.PSObject.Properties | ForEach-Object { $_.Name })
        foreach ($field in $requiredFields) {
            if (-not ($actualNames | Where-Object { [string]::Equals($_, $field, [System.StringComparison]::Ordinal) })) {
                throw "run manifest missing required field: $field"
            }
        }
        if ($run.plan_version -isnot [int] -and $run.plan_version -isnot [long] -or $run.plan_version -ne 4) {
            throw 'plan_version must be integer 4.'
        }
        if (-not (Test-NonEmptyString $run.task_id) -or $run.task_id -cnotmatch '^T[0-9]+(\.[0-9]+)?$') {
            throw 'task_id must be a non-empty T0 or T0.1-style string.'
        }
        foreach ($field in @('candidate_id', 'baseline_id', 'pair_id')) {
            if (-not (Test-NonEmptyString $run.$field)) { throw "$field must be a non-empty string." }
        }
        if ($run.session_mode -isnot [string] -or $run.session_mode -cnotin @('cold', 'warm')) {
            throw 'session_mode must be exactly cold or warm.'
        }
        if ($run.session_mode -cne $RequestedSessionMode) {
            throw "session_mode requested=$RequestedSessionMode actual=$($run.session_mode)."
        }
        if ($run.process_generation -isnot [int] -and $run.process_generation -isnot [long] -or $run.process_generation -lt 1) {
            throw 'process_generation must be a positive integer.'
        }
        if ($run.browser_reused -isnot [bool]) { throw 'browser_reused must be a JSON boolean.' }
        if ($run.stream_protocol -isnot [string] -or $run.stream_protocol -cnotin @('NCT1', 'NCT2')) {
            throw 'stream_protocol must be exactly NCT1 or NCT2.'
        }
        if ($run.stream_protocol -cne $RequestedStreamProtocol) {
            throw "stream_protocol requested=$RequestedStreamProtocol actual=$($run.stream_protocol)."
        }
        $allowedProfiles = @(
            'release', 'release-thin', 'release-thin-one',
            'release-pgo', 'release-thin-pgo', 'release-thin-one-pgo'
        )
        if ($run.build_profile -isnot [string] -or $run.build_profile -cnotin $allowedProfiles) {
            throw 'build_profile must be release, release-thin, release-thin-one, or one of those profiles with -pgo appended.'
        }
        if ($run.build_profile.EndsWith('-pgo', [System.StringComparison]::Ordinal)) {
            if (-not (Test-HashString $run.training_manifest_hash)) {
                throw 'training_manifest_hash must be a 64-digit SHA-256 string when build_profile ends in -pgo.'
            }
        }
        elseif ($null -ne $run.training_manifest_hash) {
            throw 'training_manifest_hash must be JSON null for base profiles without -pgo.'
        }
        if ($run.run_status -isnot [string] -or $run.run_status -cnotin @('completed', 'skipped', 'failed')) {
            throw 'run_status must be exactly completed, skipped, or failed.'
        }
        if ($null -ne $run.skip_reason -and $run.skip_reason -isnot [string]) {
            throw 'skip_reason must be a JSON string or null.'
        }
        if ($run.run_status -ceq 'skipped') {
            $expectedSkipReason = $null
            if ($run.session_mode -cne 'cold') {
                $expectedSkipReason = 'T0.4 only implements cold sessions.'
            }
            elseif ($run.stream_protocol -cne 'NCT1') {
                $expectedSkipReason = 'T0.4 only implements NCT1.'
            }
            if ($null -eq $expectedSkipReason) {
                throw 'skip_reason is not valid because the requested target is testable.'
            }
            if ([string]::IsNullOrWhiteSpace($run.skip_reason)) {
                throw 'skip_reason must be a non-empty reason for the unsupported target.'
            }
            if ($run.skip_reason -cne $expectedSkipReason) {
                throw "skip_reason does not match the unsupported target; expected '$expectedSkipReason'."
            }
        }
        elseif ($null -ne $run.skip_reason) {
            throw 'skip_reason must be JSON null unless run_status is skipped.'
        }
        $sampleEligible = $run.run_status -ceq 'completed'
        $sampleText = if ($sampleEligible) { 'true' } else { 'false' }
        Write-Output "run_status=$($run.run_status)"
        Write-Output "sample_eligible=$sampleText"
        exit 0
    }

    # Resolve every caller-supplied artifact explicitly before computing an ID.
    $videoItem = Resolve-RegularFile $SourceVideo 'source video'
    $snapshotItem = Resolve-RegularFile $Snapshot 'snapshot'
    $ffmpegItem = Resolve-RegularFile $Ffmpeg 'FFmpeg'
    $workerItem = Resolve-RegularFile $Worker 'worker'
    $helperItem = Resolve-RegularFile $Helper 'helper'
    $manifestItem = Resolve-RegularFile $SourceManifest 'source manifest'

    $rootItem = Get-Item -LiteralPath (Resolve-Path -LiteralPath $SourceRoot -ErrorAction Stop).ProviderPath -Force
    if ($rootItem -isnot [System.IO.DirectoryInfo]) {
        throw "SourceRoot must be an existing directory: $SourceRoot"
    }
    $rootFull = [System.IO.Path]::GetFullPath($rootItem.FullName).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $rootPrefix = $rootFull + [System.IO.Path]::DirectorySeparatorChar

    $outputFull = [System.IO.Path]::GetFullPath($OutputPath)
    $outputParent = [System.IO.Path]::GetDirectoryName($outputFull)
    if (-not [System.IO.Directory]::Exists($outputParent)) { throw "Output directory must already exist: $outputParent" }
    if ([System.IO.File]::Exists($outputFull) -or [System.IO.Directory]::Exists($outputFull)) { throw "Refusing to overwrite existing output: $outputFull" }

    $manifest = Get-Content -LiteralPath $manifestItem.FullName -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($manifest.schema_version -ne 1 -or $manifest.fingerprint_algorithm.target_count -ne 695) {
        throw 'Source manifest schema or target count is not the expected T0.1 inventory.'
    }
    $files = @($manifest.files)
    if ($files.Count -ne 695) { throw "Source manifest must contain 695 files; found $($files.Count)." }
    if ($manifest.fingerprint_algorithm.aggregate -cne 'SHA-256 over the concatenation of each contribution in files array order.' -or
        $manifest.fingerprint_algorithm.contribution_bytes -cne 'UTF-8(relative_path with forward slashes) + bytes 5c30 + raw per-file SHA-256 (32 bytes) + bytes 5c6e') {
        throw 'Source manifest fingerprint algorithm does not match the documented T0.1 algorithm.'
    }
    if (-not (Test-HashString $manifest.aggregate_fingerprint_sha256)) { throw 'Source manifest aggregate fingerprint is missing or malformed.' }

    $aggregate = [System.Security.Cryptography.IncrementalHash]::CreateHash([System.Security.Cryptography.HashAlgorithmName]::SHA256)
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $separatorStart = [byte[]](0x5c, 0x30)
    $separatorEnd = [byte[]](0x5c, 0x6e)
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
    $verifiedRows = [System.Collections.Generic.List[object]]::new()
    try {
        foreach ($row in $files) {
            if ($null -eq $row -or [string]::IsNullOrWhiteSpace([string]$row.path) -or
                -not (Test-HashString $row.sha256) -or $null -eq $row.size_bytes -or
                [long]$row.size_bytes -lt 0 -or [string]::IsNullOrWhiteSpace([string]$row.kind)) {
                throw 'Source manifest contains a row with missing path, size, hash, or kind.'
            }
            $relative = [string]$row.path
            if ($relative.Contains('\') -or [System.IO.Path]::IsPathRooted($relative) -or
                @($relative.Split('/')) -contains '..' -or $relative.StartsWith('/')) {
                throw "Source manifest path is not a safe root-relative forward-slash path: $relative"
            }
            if (-not $seen.Add($relative)) { throw "Source manifest contains a duplicate path: $relative" }
            $candidate = [System.IO.Path]::GetFullPath([System.IO.Path]::Combine($rootFull, $relative.Replace('/', [System.IO.Path]::DirectorySeparatorChar)))
            if (-not $candidate.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
                throw "Source manifest path escapes SourceRoot: $relative"
            }
            $current = $rootFull
            $parts = $relative.Split('/')
            for ($index = 0; $index -lt $parts.Length; $index++) {
                $current = [System.IO.Path]::Combine($current, $parts[$index])
                if (-not [System.IO.File]::Exists($current) -and -not [System.IO.Directory]::Exists($current)) {
                    throw "Source manifest file is missing: $relative"
                }
                $partItem = Get-Item -LiteralPath $current -Force
                if ($partItem.LinkType -or $partItem.Target) {
                    throw "Source manifest path traverses a symbolic link or junction: $relative"
                }
                if ($index -lt ($parts.Length - 1) -and $partItem -isnot [System.IO.DirectoryInfo]) {
                    throw "Source manifest parent is not a directory: $relative"
                }
                if ($index -eq ($parts.Length - 1) -and $partItem -isnot [System.IO.FileInfo]) {
                    throw "Source manifest target is not a regular file: $relative"
                }
            }
            $actualSize = (Get-Item -LiteralPath $candidate).Length
            $actualHash = Get-Sha256 $candidate
            if ($actualSize -ne [long]$row.size_bytes -or $actualHash -cne ([string]$row.sha256).ToLowerInvariant()) {
                throw "Source file size/hash mismatch: $relative"
            }
            $rawDigest = [Convert]::FromHexString($actualHash)
            $aggregate.AppendData($utf8.GetBytes($relative))
            $aggregate.AppendData($separatorStart)
            $aggregate.AppendData($rawDigest)
            $aggregate.AppendData($separatorEnd)
            $verifiedRows.Add([pscustomobject]@{
                path = $relative
                size_bytes = [long]$actualSize
                sha256 = $actualHash
                kind = [string]$row.kind
            })
        }
        $actualAggregate = [Convert]::ToHexString($aggregate.GetHashAndReset()).ToLowerInvariant()
    }
    finally { $aggregate.Dispose() }
    if ($actualAggregate -cne ([string]$manifest.aggregate_fingerprint_sha256).ToLowerInvariant()) {
        throw 'Source aggregate fingerprint does not match the source manifest.'
    }

    $videoHash = Get-Sha256 $videoItem.FullName
    $snapshotHash = Get-Sha256 $snapshotItem.FullName
    $ffmpegHash = Get-Sha256 $ffmpegItem.FullName
    $helperHash = Get-Sha256 $helperItem.FullName
    $workerHash = Get-Sha256 $workerItem.FullName
    $sourceManifestHash = Get-Sha256 $manifestItem.FullName
    $tuple = "plan=3|video=$videoHash|snapshot=$snapshotHash|ffmpeg=$ffmpegHash|helper=$helperHash|worker=$workerHash|source=$actualAggregate|width=1920|height=1080|fps=30/1|duration_ms=6000|slots=3"
    $tupleHash = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($utf8.GetBytes($tuple))).ToLowerInvariant()
    $expectedSuffix = '2dfe24f70069'
    if ($tupleHash.Substring(0, 12) -cne $expectedSuffix) { throw "Configuration tuple differs from the expected T0.1 inputs: $tupleHash" }
    $configurationId = "nct-v3-sm9-1080p30-20260925-$($tupleHash.Substring(0, 12))"

    $inventory = [ordered]@{
        schema_version = 1
        task_id = 'T0.1'
        plan_version = 3
        created_utc = [DateTime]::UtcNow.ToString('o')
        baseline_id = $configurationId
        configuration_tuple_sha256 = $tupleHash
        configuration_tuple = $tuple
        id_is_performance_pass = $false
        source_root = $rootFull
        source_manifest = [ordered]@{ path = $manifestItem.FullName; size_bytes = $manifestItem.Length; sha256 = $sourceManifestHash; aggregate_fingerprint_sha256 = $actualAggregate; file_count = $verifiedRows.Count; files = $verifiedRows.ToArray() }
        inputs = [ordered]@{
            video = [ordered]@{ path = $videoItem.FullName; size_bytes = $videoItem.Length; sha256 = $videoHash }
            snapshot = [ordered]@{ path = $snapshotItem.FullName; size_bytes = $snapshotItem.Length; sha256 = $snapshotHash }
            ffmpeg = [ordered]@{ path = $ffmpegItem.FullName; size_bytes = $ffmpegItem.Length; sha256 = $ffmpegHash }
        }
        build = [ordered]@{
            worker = [ordered]@{ path = $workerItem.FullName; size_bytes = $workerItem.Length; sha256 = $workerHash }
            helper = [ordered]@{ path = $helperItem.FullName; size_bytes = $helperItem.Length; sha256 = $helperHash }
            source_manifest_sha256 = $sourceManifestHash
        }
        output = [ordered]@{ width = 1920; height = 1080; fps = '30/1'; duration_ms = 6000; slots = 3 }
    }
    $json = ConvertTo-Json -InputObject $inventory -Depth 12
    $bytes = $utf8.GetBytes($json + "`n")
    $tempPath = $outputFull + '.' + [Guid]::NewGuid().ToString('N') + '.tmp'
    $stream = [System.IO.File]::Open($tempPath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    try { $stream.Write($bytes, 0, $bytes.Length) }
    finally { $stream.Dispose() }
    [System.IO.File]::Move($tempPath, $outputFull)
    $tempPath = $null
    Write-Output "inventory=$outputFull"
    Write-Output "baseline_id=$configurationId"
    Write-Output "source_files=$($verifiedRows.Count) aggregate_sha256=$actualAggregate"
    exit 0
}
catch {
    if ($tempPath -and [System.IO.File]::Exists($tempPath)) { [System.IO.File]::Delete($tempPath) }
    $phase = switch ($PSCmdlet.ParameterSetName) {
        'RunColdCase' { 'T0.4 cold case' }
        'ValidateManifest' { 'T0.2 manifest validation' }
        default { 'T0.1 inventory' }
    }
    [Console]::Error.WriteLine("$phase failed: $($_.Exception.Message)")
    exit 1
}
