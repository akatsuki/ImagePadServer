Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if (-not ('NicoVerifiedArtifactDelete' -as [type])) {
    $source = @'
using System;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using Microsoft.Win32.SafeHandles;

public static class NicoVerifiedArtifactDelete
{
    private const uint GenericRead = 0x80000000;
    private const uint DeleteAccess = 0x00010000;
    private const uint ReadAttributes = 0x00000080;
    private const uint OpenExisting = 3;
    private const uint OpenReparsePoint = 0x00200000;
    private const uint FileFlagBackupSemantics = 0x02000000;
    private const uint ShareReadWriteDelete = 0x00000007;
    private const uint FileAttributeDirectory = 0x00000010;
    private const uint FileAttributeReparsePoint = 0x00000400;
    private const int FileDispositionInfoClass = 4;

    [StructLayout(LayoutKind.Sequential)]
    private struct HandleFileInformation
    {
        public uint Attributes;
        public System.Runtime.InteropServices.ComTypes.FILETIME CreationTime;
        public System.Runtime.InteropServices.ComTypes.FILETIME LastAccessTime;
        public System.Runtime.InteropServices.ComTypes.FILETIME LastWriteTime;
        public uint VolumeSerialNumber;
        public uint FileSizeHigh;
        public uint FileSizeLow;
        public uint NumberOfLinks;
        public uint FileIndexHigh;
        public uint FileIndexLow;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct FileDispositionInfo
    {
        [MarshalAs(UnmanagedType.U1)]
        public byte DeleteFile;
    }

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true, EntryPoint = "CreateFileW")]
    private static extern SafeFileHandle CreateFile(
        string name, uint access, uint share, IntPtr security, uint creation,
        uint flags, IntPtr template);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool GetFileInformationByHandle(
        SafeFileHandle handle, out HandleFileInformation information);

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true, EntryPoint = "GetFinalPathNameByHandleW")]
    private static extern uint GetFinalPathNameByHandle(
        SafeFileHandle handle, StringBuilder path, uint length, uint flags);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool SetFileInformationByHandle(
        SafeFileHandle handle, int informationClass,
        ref FileDispositionInfo information, uint size);

    private static string NormalizePath(string path)
    {
        if (path.StartsWith(@"\\?\UNC\", StringComparison.OrdinalIgnoreCase))
            path = @"\" + path.Substring(7);
        else if (path.StartsWith(@"\\?\", StringComparison.OrdinalIgnoreCase))
            path = path.Substring(4);
        return Path.GetFullPath(path).TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
    }

    private static string FinalPath(SafeFileHandle handle)
    {
        var buffer = new StringBuilder(32768);
        uint count = GetFinalPathNameByHandle(handle, buffer, (uint)buffer.Capacity, 0);
        if (count == 0 || count >= buffer.Capacity)
            throw new Win32Exception(Marshal.GetLastWin32Error(), "Could not resolve the opened artifact handle path.");
        return NormalizePath(buffer.ToString());
    }

    private static bool IsInsideRoot(string root, string candidate)
    {
        string relative = Path.GetRelativePath(root, candidate);
        return relative != "." && !Path.IsPathRooted(relative) && relative != ".." &&
            !relative.StartsWith(".." + Path.DirectorySeparatorChar, StringComparison.Ordinal) &&
            !relative.StartsWith(".." + Path.AltDirectorySeparatorChar, StringComparison.Ordinal);
    }

    private static bool IsParentOf(string parent, string child)
    {
        string relative = Path.GetRelativePath(parent, child);
        return relative != "." && !Path.IsPathRooted(relative) && relative != ".." &&
            !relative.StartsWith(".." + Path.DirectorySeparatorChar, StringComparison.Ordinal) &&
            !relative.StartsWith(".." + Path.AltDirectorySeparatorChar, StringComparison.Ordinal);
    }

    private static string[] ValidateTrustedAncestors(string root, string[] trustedAncestors)
    {
        if (trustedAncestors == null) trustedAncestors = new string[0];
        string[] normalized = new string[trustedAncestors.Length];
        for (int i = 0; i < trustedAncestors.Length; i++)
        {
            string ancestor = NormalizePath(trustedAncestors[i]);
            if (!IsParentOf(ancestor, root))
                throw new IOException("Trusted reparse path must be a strict ancestor of the declared root: " + ancestor);
            FileAttributes attributes = File.GetAttributes(ancestor);
            if ((attributes & FileAttributes.Directory) == 0 || (attributes & FileAttributes.ReparsePoint) == 0)
                throw new IOException("Trusted ancestor must be an existing reparse directory: " + ancestor);
            for (int j = 0; j < i; j++)
                if (String.Equals(normalized[j], ancestor, StringComparison.OrdinalIgnoreCase))
                    throw new IOException("Duplicate trusted reparse ancestor: " + ancestor);
            normalized[i] = ancestor;
        }
        return normalized;
    }

    private static void RejectReparseTraversal(string fullPath, string ownedRoot, string[] trustedAncestors)
    {
        string full = Path.GetFullPath(fullPath);
        string current = Path.GetPathRoot(full);
        if (String.IsNullOrEmpty(current)) throw new IOException("Path has no volume root.");
        string rest = full.Substring(current.Length);
        foreach (string part in rest.Split(new[] { Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar }, StringSplitOptions.RemoveEmptyEntries))
        {
            current = Path.Combine(current, part);
            FileAttributes attributes = File.GetAttributes(current);
            if ((attributes & FileAttributes.ReparsePoint) != 0)
            {
                bool trusted = false;
                foreach (string ancestor in trustedAncestors)
                    if (String.Equals(ancestor, current, StringComparison.OrdinalIgnoreCase)) trusted = true;
                if (!trusted || !IsParentOf(current, ownedRoot))
                    throw new IOException("Reparse traversal is not allowed: " + current);
            }
        }
    }

    public static long DeleteIfUnchanged(string path, string root, long expectedSize, string expectedSha256)
    {
        return DeleteIfUnchanged(path, root, new string[0], expectedSize, expectedSha256);
    }

    private static string ResolveDirectoryFinalPath(string path)
    {
        using (SafeFileHandle handle = CreateFile(path, ReadAttributes, ShareReadWriteDelete, IntPtr.Zero,
            OpenExisting, FileFlagBackupSemantics, IntPtr.Zero))
        {
            if (handle.IsInvalid)
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Could not open the declared artifact root directory.");
            HandleFileInformation info;
            if (!GetFileInformationByHandle(handle, out info))
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Could not inspect the declared artifact root directory.");
            if ((info.Attributes & FileAttributeDirectory) == 0 || (info.Attributes & FileAttributeReparsePoint) != 0)
                throw new IOException("Declared artifact root must resolve to a non-reparse directory.");
            return FinalPath(handle);
        }
    }

    public static long DeleteIfUnchanged(string path, string root, string[] trustedAncestors, long expectedSize, string expectedSha256)
    {
        string fullRoot = NormalizePath(root);
        string fullPath = NormalizePath(path);
        if (!IsInsideRoot(fullRoot, fullPath))
            throw new IOException("Opened artifact is not a child file of the declared root.");
        string[] trusted = ValidateTrustedAncestors(fullRoot, trustedAncestors);

        // Recheck the complete path immediately before opening. The file handle then
        // pins the file identity; sharing is disabled so the file cannot be replaced
        // or changed while its digest is checked and its disposition is set.
        RejectReparseTraversal(fullRoot, fullRoot, trusted);
        string finalRoot = ResolveDirectoryFinalPath(fullRoot);
        RejectReparseTraversal(fullPath, fullRoot, trusted);
        using (SafeFileHandle handle = CreateFile(fullPath,
            GenericRead | DeleteAccess | ReadAttributes, 0, IntPtr.Zero,
            OpenExisting, OpenReparsePoint, IntPtr.Zero))
        {
            if (handle.IsInvalid)
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Could not acquire exclusive artifact handle.");

            HandleFileInformation info;
            if (!GetFileInformationByHandle(handle, out info))
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Could not inspect opened artifact handle.");
            if ((info.Attributes & FileAttributeDirectory) != 0 || (info.Attributes & FileAttributeReparsePoint) != 0)
                throw new IOException("Opened artifact is a directory or reparse point.");
            if (info.NumberOfLinks != 1)
                throw new IOException("Opened artifact has multiple hard links; refusing ambiguous ownership and reclaimed-byte accounting.");

            string finalPath = FinalPath(handle);
            string expectedFinalPath = NormalizePath(Path.Combine(finalRoot, Path.GetRelativePath(fullRoot, fullPath)));
            if (!String.Equals(finalPath, expectedFinalPath, StringComparison.OrdinalIgnoreCase) || !IsInsideRoot(finalRoot, finalPath))
                throw new IOException("Opened file identity no longer resolves to the manifest path under its root.");

            RejectReparseTraversal(fullRoot, fullRoot, trusted);
            RejectReparseTraversal(fullPath, fullRoot, trusted);
            using (var stream = new FileStream(handle, FileAccess.Read, 4096, false))
            {
                long actualSize = stream.Length;
                string actualHash;
                using (SHA256 sha = SHA256.Create())
                    actualHash = BitConverter.ToString(sha.ComputeHash(stream)).Replace("-", "").ToLowerInvariant();
                if (actualSize != expectedSize || !String.Equals(actualHash, expectedSha256, StringComparison.OrdinalIgnoreCase))
                    throw new IOException("Artifact changed between manifest validation and deletion.");

                // Recheck parent/leaf metadata after hashing and before deleting the
                // pinned handle. Deletion is by handle, never by re-resolved path.
                RejectReparseTraversal(fullRoot, fullRoot, trusted);
                RejectReparseTraversal(fullPath, fullRoot, trusted);
                if (!String.Equals(FinalPath(handle), expectedFinalPath, StringComparison.OrdinalIgnoreCase))
                    throw new IOException("Artifact path identity changed before deletion.");
                var disposition = new FileDispositionInfo { DeleteFile = 1 };
                if (Marshal.SizeOf(typeof(FileDispositionInfo)) != 1)
                    throw new InvalidOperationException("FILE_DISPOSITION_INFO must be exactly one byte.");
                if (!SetFileInformationByHandle(handle, FileDispositionInfoClass, ref disposition,
                    1))
                    throw new Win32Exception(Marshal.GetLastWin32Error(), "Could not delete the verified artifact handle.");
                return actualSize;
            }
        }
    }
}
'@
    Add-Type -TypeDefinition $source -Language CSharp -ErrorAction Stop
}

function Test-NicoReparseMetadata {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][object]$Item)
    if ($null -eq $Item -or -not $Item.PSObject.Properties['Attributes']) { return $true }
    try {
        $attributes = [System.IO.FileAttributes]$Item.Attributes
        if (($attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { return $true }
    }
    catch { return $true }
    foreach ($propertyName in @('LinkType', 'Target')) {
        $property = $Item.PSObject.Properties[$propertyName]
        if ($null -ne $property -and $null -ne $property.Value -and
            -not [string]::IsNullOrWhiteSpace([string]$property.Value)) { return $true }
    }
    return $false
}

function Assert-NicoNoReparseTraversal {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [string[]]$TrustedAncestors = @(),
        [string]$OwnedRoot = '',
        [switch]$AllowHardLinkLeaf
    )
    $full = [System.IO.Path]::GetFullPath($Path)
    $trusted = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
    foreach ($ancestor in $TrustedAncestors) { [void]$trusted.Add([System.IO.Path]::GetFullPath($ancestor)) }
    $current = [System.IO.Path]::GetPathRoot($full)
    if ([string]::IsNullOrWhiteSpace($current)) { throw "Path has no volume root: $Path" }
    $tail = $full.Substring($current.Length)
    foreach ($part in @($tail.Split([char[]]@([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar), [System.StringSplitOptions]::RemoveEmptyEntries))) {
        $current = [System.IO.Path]::Combine($current, $part)
        if (-not [System.IO.File]::Exists($current) -and -not [System.IO.Directory]::Exists($current)) {
            throw "Path component does not exist: $current"
        }
        $item = Get-Item -LiteralPath $current -Force -ErrorAction Stop
        if (Test-NicoReparseMetadata $item) {
            $isHardLinkLeaf = $AllowHardLinkLeaf -and
                [string]::Equals([System.IO.Path]::GetFullPath($current), $full, [System.StringComparison]::OrdinalIgnoreCase) -and
                $item -is [System.IO.FileInfo] -and
                (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -eq 0) -and
                [string]::Equals([string]$item.LinkType, 'HardLink', [System.StringComparison]::OrdinalIgnoreCase) -and
                ($null -eq $item.PSObject.Properties['Target'] -or [string]::IsNullOrWhiteSpace([string]$item.Target))
            if ($isHardLinkLeaf) { continue }
            $relativeToRoot = if (-not [string]::IsNullOrWhiteSpace($OwnedRoot)) { [System.IO.Path]::GetRelativePath($current, $OwnedRoot) } else { '.' }
            $isStrictRootAncestor = -not [string]::IsNullOrWhiteSpace($OwnedRoot) -and
                $relativeToRoot -ne '.' -and -not [System.IO.Path]::IsPathRooted($relativeToRoot) -and
                $relativeToRoot -ne '..' -and -not $relativeToRoot.StartsWith('..' + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::Ordinal) -and
                -not $relativeToRoot.StartsWith('..' + [System.IO.Path]::AltDirectorySeparatorChar, [System.StringComparison]::Ordinal)
            if (-not $trusted.Contains([System.IO.Path]::GetFullPath($current)) -or -not $isStrictRootAncestor) {
                throw "Reparse point traversal is not allowed: $current"
            }
        }
    }
}

function Get-NicoTrustedAncestors([string]$Root, [object]$Declaration) {
    if ($null -eq $Declaration) {
        throw 'trusted_ancestors must be a JSON array of explicit absolute ancestor paths.'
    }
    if ($Declaration -isnot [System.Array] -and $Declaration -isnot [System.Collections.IList]) {
        throw 'trusted_ancestors must be a JSON array of explicit absolute ancestor paths.'
    }
    $rootFull = [System.IO.Path]::GetFullPath($Root).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
    $trusted = [System.Collections.Generic.List[string]]::new()
    foreach ($declaration in $Declaration) {
        if ($declaration -isnot [string] -or [string]::IsNullOrWhiteSpace($declaration) -or
            -not [System.IO.Path]::IsPathFullyQualified($declaration)) {
            throw 'Each trusted_ancestors entry must be an absolute directory path.'
        }
        $ancestor = [System.IO.Path]::GetFullPath($declaration).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
        $relativeToRoot = [System.IO.Path]::GetRelativePath($ancestor, $rootFull)
        if ($relativeToRoot -eq '.' -or [System.IO.Path]::IsPathRooted($relativeToRoot) -or $relativeToRoot -eq '..' -or
            $relativeToRoot.StartsWith('..' + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::Ordinal) -or
            $relativeToRoot.StartsWith('..' + [System.IO.Path]::AltDirectorySeparatorChar, [System.StringComparison]::Ordinal)) {
            throw "trusted_ancestors entries must be strict ancestors of Root: $ancestor"
        }
        if (-not $seen.Add($ancestor)) { throw "Duplicate trusted_ancestors path: $ancestor" }
        if (-not [System.IO.Directory]::Exists($ancestor)) { throw "Trusted ancestor directory does not exist: $ancestor" }
        $item = Get-Item -LiteralPath $ancestor -Force -ErrorAction Stop
        if ($item -isnot [System.IO.DirectoryInfo] -or -not (Test-NicoReparseMetadata $item)) {
            throw "Trusted ancestor must be an existing reparse directory: $ancestor"
        }
        $trusted.Add($ancestor)
    }
    return $trusted.ToArray()
}

function Get-NicoCanonicalFilePath {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Label
    )
    if (-not [System.IO.Path]::IsPathFullyQualified($Path)) { throw "$Label must be an absolute path: $Path" }
    $full = [System.IO.Path]::GetFullPath($Path)
    Assert-NicoNoReparseTraversal $full
    if (-not [System.IO.File]::Exists($full)) { throw "$Label is not a regular file: $Path" }
    $item = Get-Item -LiteralPath $full -Force -ErrorAction Stop
    if ($item -isnot [System.IO.FileInfo] -or (Test-NicoReparseMetadata $item)) {
        throw "$Label must be a regular non-reparse file: $Path"
    }
    return $item.FullName
}

function Get-NicoRelativeChildPath {
    param([string]$Root, [string]$Candidate)
    $relative = [System.IO.Path]::GetRelativePath($Root, $Candidate)
    if ($relative -eq '.' -or [System.IO.Path]::IsPathRooted($relative) -or $relative -eq '..' -or
        $relative.StartsWith('..' + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::Ordinal) -or
        $relative.StartsWith('..' + [System.IO.Path]::AltDirectorySeparatorChar, [System.StringComparison]::Ordinal)) {
        throw "Artifact path is outside the declared root: $Candidate"
    }
    return $relative
}

function Test-NicoIntegerValue([object]$Value) {
    return ($Value -is [byte] -or $Value -is [int16] -or $Value -is [int] -or $Value -is [long]) -and $Value -isnot [bool]
}

function Get-NicoSha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
}

function Test-NicoCapacityReservation {
    [CmdletBinding()]
    param(
        [long]$ExistingRawBytes = 0,
        [long]$ReserveRawBytes = 0,
        [string]$RawVolumeId = '',
        [long]$RawVolumeFreeBytes = 0,
        [long]$ExistingBuildBytes = 0,
        [long]$ReserveBuildBytes = 0,
        [string]$BuildVolumeId = '',
        [long]$BuildVolumeFreeBytes = 0
    )
    $numbers = @($ExistingRawBytes, $ReserveRawBytes, $RawVolumeFreeBytes, $ExistingBuildBytes, $ReserveBuildBytes, $BuildVolumeFreeBytes)
    if ($numbers | Where-Object { $_ -lt 0 }) { throw 'Capacity byte values must not be negative.' }
    $gib = [long]1GB
    $reasons = [System.Collections.Generic.List[string]]::new()
    $rawTotal = $ExistingRawBytes + $ReserveRawBytes
    $buildTotal = $ExistingBuildBytes + $ReserveBuildBytes
    $aggregate = $rawTotal + $buildTotal
    if ($rawTotal -gt 12 * $gib) { $reasons.Add('raw_budget_exceeded') }
    if ($buildTotal -gt 24 * $gib) { $reasons.Add('build_budget_exceeded') }
    if ($aggregate -gt 36 * $gib) { $reasons.Add('combined_budget_exceeded') }
    if ($ReserveRawBytes -gt 0) {
        if ([string]::IsNullOrWhiteSpace($RawVolumeId)) { throw 'RawVolumeId is required when reserving raw bytes.' }
        if ($RawVolumeFreeBytes -lt 20 * $gib) { $reasons.Add('raw_start_free_below_minimum') }
    }
    if ($ReserveBuildBytes -gt 0) {
        if ([string]::IsNullOrWhiteSpace($BuildVolumeId)) { throw 'BuildVolumeId is required when reserving build bytes.' }
        if ($BuildVolumeFreeBytes -lt 40 * $gib) { $reasons.Add('build_start_free_below_minimum') }
    }

    $volumes = [System.Collections.Generic.List[object]]::new()
    if ($ReserveRawBytes -gt 0) {
        $volumes.Add([pscustomobject]@{ volume_id = $RawVolumeId; free_bytes = $RawVolumeFreeBytes; reserved_bytes = $ReserveRawBytes })
    }
    if ($ReserveBuildBytes -gt 0) {
        $same = $null
        foreach ($volume in $volumes) {
            if ([string]::Equals($volume.volume_id, $BuildVolumeId, [System.StringComparison]::OrdinalIgnoreCase)) { $same = $volume; break }
        }
        if ($null -ne $same) {
            if ($same.free_bytes -ne $BuildVolumeFreeBytes) { $reasons.Add('same_volume_free_space_disagrees') }
            $same.reserved_bytes += $ReserveBuildBytes
        }
        else {
            $volumes.Add([pscustomobject]@{ volume_id = $BuildVolumeId; free_bytes = $BuildVolumeFreeBytes; reserved_bytes = $ReserveBuildBytes })
        }
    }
    foreach ($volume in $volumes) {
        $remaining = $volume.free_bytes - $volume.reserved_bytes
        if ($remaining -lt 8 * $gib) { $reasons.Add('volume_free_below_8gib_after_reservation') }
        $volume | Add-Member -NotePropertyName remaining_bytes -NotePropertyValue $remaining
    }
    [pscustomobject]@{
        accepted = ($reasons.Count -eq 0)
        rejection_codes = @($reasons.ToArray() | Select-Object -Unique)
        raw_projected_bytes = $rawTotal
        build_projected_bytes = $buildTotal
        aggregate_projected_bytes = $aggregate
        volume_checks = @($volumes.ToArray())
    }
}

function Test-NicoOwnedArtifacts {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Root,
        [Parameter(Mandatory = $true)][string]$ManifestPath,
        [string[]]$ArtifactPath
    )
    if (-not [System.IO.Path]::IsPathFullyQualified($Root)) { throw 'Root must be an absolute directory path.' }
    $rootFull = [System.IO.Path]::GetFullPath($Root).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    if (-not [System.IO.Directory]::Exists($rootFull)) { throw "Root is not an existing directory: $Root" }
    $manifestFull = Get-NicoCanonicalFilePath $ManifestPath 'ManifestPath'
    $json = Get-Content -LiteralPath $manifestFull -Raw -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
    $fields = @($json.PSObject.Properties | ForEach-Object { $_.Name })
    if (-not ($fields | Where-Object { [string]::Equals($_, 'schema_version', [System.StringComparison]::Ordinal) }) -or
        -not ($fields | Where-Object { [string]::Equals($_, 'root', [System.StringComparison]::Ordinal) }) -or
        -not ($fields | Where-Object { [string]::Equals($_, 'artifacts', [System.StringComparison]::Ordinal) })) {
        throw 'Manifest must contain schema_version, root, and artifacts.'
    }
    if (-not (Test-NicoIntegerValue $json.schema_version) -or $json.schema_version -ne 1) { throw 'Manifest schema_version must be integer 1.' }
    if ($json.root -isnot [string] -or [string]::IsNullOrWhiteSpace($json.root) -or
        -not [System.IO.Path]::IsPathFullyQualified($json.root) -or
        -not [string]::Equals([System.IO.Path]::GetFullPath($json.root).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar), $rootFull, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'Manifest root must equal the caller-supplied absolute root.'
    }
    $trustedDeclaration = $json.PSObject.Properties['trusted_ancestors']
    $trustedAncestors = if ($null -eq $trustedDeclaration) { @() } else { @(Get-NicoTrustedAncestors $rootFull $trustedDeclaration.Value) }
    $capacityDeclaration = $json.PSObject.Properties['capacity_only']
    $capacityOnly = $false
    if ($null -ne $capacityDeclaration) {
        if ($capacityDeclaration.Value -isnot [bool]) { throw 'capacity_only must be a JSON boolean when present.' }
        $capacityOnly = [bool]$capacityDeclaration.Value
    }
    Assert-NicoNoReparseTraversal $rootFull $trustedAncestors $rootFull
    if ($json.artifacts -isnot [System.Array] -and $json.artifacts -isnot [System.Collections.IList]) { throw 'Manifest artifacts must be a JSON array.' }

    $manifestHash = Get-NicoSha256 $manifestFull
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
    $validated = [System.Collections.Generic.List[object]]::new()
    foreach ($row in @($json.artifacts)) {
        if ($null -eq $row) { throw 'Manifest entry must be a JSON object.' }
        $rowFields = @($row.PSObject.Properties | ForEach-Object { $_.Name })
        foreach ($name in @('path', 'size_bytes', 'sha256', 'purpose')) {
            if (-not ($rowFields | Where-Object { [string]::Equals($_, $name, [System.StringComparison]::Ordinal) })) {
                throw "Manifest entry missing required field: $name"
            }
        }
        if ($row.path -isnot [string] -or [string]::IsNullOrWhiteSpace($row.path) -or
            -not [System.IO.Path]::IsPathFullyQualified($row.path)) { throw 'Artifact path must be a non-empty absolute file path.' }
        if (-not (Test-NicoIntegerValue $row.size_bytes) -or $row.size_bytes -lt 0) { throw 'Artifact size_bytes must be a non-negative JSON integer.' }
        if ($row.sha256 -isnot [string] -or $row.sha256 -cnotmatch '^[0-9a-fA-F]{64}$') { throw 'Artifact sha256 must be a 64-digit SHA-256 string.' }
        if ($row.purpose -isnot [string] -or [string]::IsNullOrWhiteSpace($row.purpose)) { throw 'Artifact purpose must be a non-empty string.' }
        $fullPath = [System.IO.Path]::GetFullPath($row.path)
        [void](Get-NicoRelativeChildPath $rootFull $fullPath)
        if (-not $seen.Add($fullPath)) { throw "Duplicate manifest artifact path: $fullPath" }
        Assert-NicoNoReparseTraversal $fullPath $trustedAncestors $rootFull -AllowHardLinkLeaf:$capacityOnly
        if (-not [System.IO.File]::Exists($fullPath)) { throw "Manifest artifact is missing or is not a file: $fullPath" }
        $item = Get-Item -LiteralPath $fullPath -Force -ErrorAction Stop
        $isCapacityHardLink = $capacityOnly -and $item -is [System.IO.FileInfo] -and
            (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -eq 0) -and
            [string]::Equals([string]$item.LinkType, 'HardLink', [System.StringComparison]::OrdinalIgnoreCase) -and
            ($null -eq $item.PSObject.Properties['Target'] -or [string]::IsNullOrWhiteSpace([string]$item.Target))
        if ($item -isnot [System.IO.FileInfo] -or ((Test-NicoReparseMetadata $item) -and -not $isCapacityHardLink)) {
            throw "Manifest artifact is a directory, reparse point, or unauthorized hard link: $fullPath"
        }
        $actualSize = [long]$item.Length
        $actualHash = Get-NicoSha256 $fullPath
        if ($actualSize -ne [long]$row.size_bytes -or $actualHash -cne ([string]$row.sha256).ToLowerInvariant()) {
            throw "Manifest artifact size/hash mismatch: $fullPath"
        }
        $validated.Add([pscustomobject]@{
            path = $fullPath
            size_bytes = $actualSize
            sha256 = $actualHash
            purpose = [string]$row.purpose
        })
    }

    if ($PSBoundParameters.ContainsKey('ArtifactPath')) {
        if ($ArtifactPath.Count -eq 0) { throw 'ArtifactPath cannot be an empty selection.' }
        $selected = [System.Collections.Generic.List[object]]::new()
        $requested = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
        foreach ($target in $ArtifactPath) {
            if (-not [System.IO.Path]::IsPathFullyQualified($target)) { throw "ArtifactPath must be absolute: $target" }
            $canonicalTarget = [System.IO.Path]::GetFullPath($target)
            if (-not $requested.Add($canonicalTarget)) { throw "Duplicate ArtifactPath selection: $canonicalTarget" }
            $match = $null
            foreach ($item in $validated) { if ([string]::Equals($item.path, $canonicalTarget, [System.StringComparison]::OrdinalIgnoreCase)) { $match = $item; break } }
            if ($null -eq $match) { throw "Artifact path is unowned by the manifest: $canonicalTarget" }
            $selected.Add($match)
        }
        $validated = $selected
    }
    [long]$totalBytes = 0
    foreach ($artifact in $validated) { $totalBytes += [long]$artifact.size_bytes }
    [pscustomobject]@{
        valid = $true
        root = $rootFull
        manifest_path = $manifestFull
        manifest_sha256 = $manifestHash
        trusted_ancestors = @($trustedAncestors)
        capacity_only = $capacityOnly
        artifacts = @($validated.ToArray())
        total_bytes = $totalBytes
    }
}

function Add-NicoAuditEvent {
    param([string]$LogPath, [hashtable]$Record, [string[]]$TrustedAncestors = @(), [string]$OwnedRoot = '')
    $full = [System.IO.Path]::GetFullPath($LogPath)
    if (-not [System.IO.Path]::IsPathFullyQualified($LogPath)) { throw 'LogPath must be absolute.' }
    $parent = [System.IO.Path]::GetDirectoryName($full)
    if (-not [System.IO.Directory]::Exists($parent)) { throw "Audit log parent directory does not exist: $parent" }
    Assert-NicoNoReparseTraversal $parent $TrustedAncestors $OwnedRoot
    if ([System.IO.File]::Exists($full)) {
        Assert-NicoNoReparseTraversal $full $TrustedAncestors $OwnedRoot
        if ((Get-Item -LiteralPath $full -Force) -isnot [System.IO.FileInfo]) { throw 'Audit log path must be a regular file.' }
    }
    $line = ConvertTo-Json -InputObject $Record -Compress -Depth 6
    [System.IO.File]::AppendAllText($full, $line + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
}

function Write-NicoAuditRecord {
    param([scriptblock]$Writer, [string]$LogPath, [hashtable]$Record, [string[]]$TrustedAncestors = @(), [string]$OwnedRoot = '')
    if ($null -eq $Writer) { Add-NicoAuditEvent $LogPath $Record $TrustedAncestors $OwnedRoot }
    else { & $Writer $LogPath $Record }
}

function Remove-NicoOwnedArtifacts {
    [CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
    param(
        [Parameter(Mandatory = $true)][string]$Root,
        [Parameter(Mandatory = $true)][string]$ManifestPath,
        [Parameter(Mandatory = $true)][string[]]$ArtifactPath,
        [Parameter(Mandatory = $true)][string]$LogPath,
        [scriptblock]$AuditWriter
    )
    if (-not [System.IO.Path]::IsPathFullyQualified($LogPath)) { throw 'LogPath must be an absolute path.' }
    $logFull = [System.IO.Path]::GetFullPath($LogPath)
    if ([System.IO.File]::Exists($logFull) -or [System.IO.Directory]::Exists($logFull)) {
        throw "Audit LogPath must be a fresh, nonexistent per-call JSONL path: $logFull"
    }
    $plan = Test-NicoOwnedArtifacts -Root $Root -ManifestPath $ManifestPath -ArtifactPath $ArtifactPath
    if ($plan.capacity_only) { throw 'capacity_only manifests are read-only and cannot authorize artifact cleanup.' }
    if ([string]::Equals($logFull, $plan.manifest_path, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'Audit LogPath cannot alias the ownership manifest.'
    }
    $relativeLog = [System.IO.Path]::GetRelativePath($plan.root, $logFull)
    if ($relativeLog -eq '.' -or (-not [System.IO.Path]::IsPathRooted($relativeLog) -and $relativeLog -ne '..' -and -not $relativeLog.StartsWith('..' + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::Ordinal))) {
        throw 'Audit LogPath must be outside the artifact root.'
    }
    $logParent = [System.IO.Path]::GetDirectoryName($logFull)
    if (-not [System.IO.Directory]::Exists($logParent)) { throw "Audit log parent directory does not exist: $logParent" }
    Assert-NicoNoReparseTraversal $logParent $plan.trusted_ancestors $plan.root
    try {
        $reservation = [System.IO.File]::Open($logFull, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        $reservation.Dispose()
    }
    catch {
        throw "Audit LogPath must be a fresh, nonexistent per-call JSONL path: $logFull ($($_.Exception.Message))"
    }
    Assert-NicoNoReparseTraversal $logFull $plan.trusted_ancestors $plan.root
    $deletedPaths = [System.Collections.Generic.List[string]]::new()
    $failedPaths = [System.Collections.Generic.List[object]]::new()
    $cancelledPaths = [System.Collections.Generic.List[string]]::new()
    $auditFailures = [System.Collections.Generic.List[object]]::new()
    [long]$reclaimed = 0
    foreach ($artifact in $plan.artifacts) {
        try {
            Assert-NicoNoReparseTraversal $plan.root $plan.trusted_ancestors $plan.root
            Assert-NicoNoReparseTraversal $artifact.path $plan.trusted_ancestors $plan.root
            if ((Get-NicoSha256 $plan.manifest_path) -cne $plan.manifest_sha256) { throw 'Ownership manifest changed after validation.' }
            $prepared = @{ utc = [DateTime]::UtcNow.ToString('o'); event = 'delete_prepared'; path = $artifact.path; size_bytes = $artifact.size_bytes; sha256 = $artifact.sha256 }
            try { Write-NicoAuditRecord $AuditWriter $logFull $prepared $plan.trusted_ancestors $plan.root }
            catch {
                $auditFailures.Add([pscustomobject]@{ path = $artifact.path; event = 'delete_prepared'; error = $_.Exception.Message })
                break
            }
            if (-not $PSCmdlet.ShouldProcess($artifact.path, 'Delete manifest-owned artifact')) {
                $cancelledPaths.Add($artifact.path)
                try { Write-NicoAuditRecord $AuditWriter $logFull @{ utc = [DateTime]::UtcNow.ToString('o'); event = 'cancelled'; path = $artifact.path; size_bytes = $artifact.size_bytes; sha256 = $artifact.sha256 } $plan.trusted_ancestors $plan.root }
                catch { $auditFailures.Add([pscustomobject]@{ path = $artifact.path; event = 'cancelled'; error = $_.Exception.Message }) }
                continue
            }
            $bytes = [NicoVerifiedArtifactDelete]::DeleteIfUnchanged($artifact.path, $plan.root, [string[]]$plan.trusted_ancestors, $artifact.size_bytes, $artifact.sha256)
            $reclaimed += $bytes
            $deletedPaths.Add($artifact.path)
            try { Write-NicoAuditRecord $AuditWriter $logFull @{ utc = [DateTime]::UtcNow.ToString('o'); event = 'deleted'; path = $artifact.path; size_bytes = $bytes; sha256 = $artifact.sha256 } $plan.trusted_ancestors $plan.root }
            catch {
                $auditFailures.Add([pscustomobject]@{ path = $artifact.path; event = 'deleted'; error = $_.Exception.Message })
                break
            }
        }
        catch {
            $message = $_.Exception.Message
            $failedPaths.Add([pscustomobject]@{ path = $artifact.path; error = $message })
            try { Write-NicoAuditRecord $AuditWriter $logFull @{ utc = [DateTime]::UtcNow.ToString('o'); event = 'delete_failed'; path = $artifact.path; size_bytes = $artifact.size_bytes; sha256 = $artifact.sha256; error = $message } $plan.trusted_ancestors $plan.root }
            catch { $auditFailures.Add([pscustomobject]@{ path = $artifact.path; event = 'delete_failed'; error = $_.Exception.Message }) }
            break
        }
    }
    $status = if ($failedPaths.Count -gt 0) { if ($reclaimed -gt 0) { 'partial_failure' } else { 'failed' } }
        elseif ($auditFailures.Count -gt 0) {
            if ($reclaimed -eq 0) { 'audit_failure' }
            elseif ($deletedPaths.Count -lt $plan.artifacts.Count) { 'partial_audit_failure' }
            else { 'completed_with_audit_failure' }
        }
        elseif ($cancelledPaths.Count -gt 0) { if ($reclaimed -gt 0) { 'partial_cancelled' } else { 'cancelled' } }
        else { 'completed' }
    [pscustomobject]@{
        status = $status
        reclaimed_bytes = $reclaimed
        deleted_paths = @($deletedPaths.ToArray())
        failed_paths = @($failedPaths.ToArray())
        audit_failures = @($auditFailures.ToArray())
        cancelled_paths = @($cancelledPaths.ToArray())
    }
}

Export-ModuleMember -Function Test-NicoCapacityReservation, Test-NicoOwnedArtifacts, Remove-NicoOwnedArtifacts, Test-NicoReparseMetadata
