$helperPath = Join-Path $PSScriptRoot "..\..\build\codex-diagnostic-launcher.ps1"

if (Test-Path -LiteralPath $helperPath -PathType Leaf) {
    . $helperPath
}

function New-TestDiagnosticOperations {
    param(
        [hashtable]$State,
        [string]$CandidatePath,
        [int]$StartedPid = 9001,
        [switch]$HealthFailure,
        [int]$ListenerAfterStart = 0
    )

    $State.StartedPid = $StartedPid
    $State.HealthFailure = $HealthFailure.IsPresent
    foreach ($key in @(
        "WrittenManifest", "FailureEvidence", "ProcessIdentity", "ListenerPid", "QuitPorts", "RevalidatedPids",
        "WaitedPids", "TreeTerminationPids", "ProcessExited", "QuitError", "RevalidateMismatchAt",
        "RevalidateNullAt", "RevalidateCallCount", "TreeTerminationSucceeded", "TreeTerminated", "ReplacementEvidence",
        "CapturedTree", "CapturedRootPids", "InitialMatchingPids", "PostTerminationMatchingPids",
        "MatchQueryMismatchAt", "MatchQueryCount"
    )) {
        if (-not $State.ContainsKey($key)) { $State[$key] = $null }
    }
    if ($null -eq $State.QuitPorts) { $State.QuitPorts = @() }
    if ($null -eq $State.RevalidatedPids) { $State.RevalidatedPids = @() }
    if ($null -eq $State.WaitedPids) { $State.WaitedPids = @() }
    if ($null -eq $State.TreeTerminationPids) { $State.TreeTerminationPids = @() }
    if ($null -eq $State.ProcessExited) { $State.ProcessExited = $true }
    if ($null -eq $State.RevalidateMismatchAt) { $State.RevalidateMismatchAt = 0 }
    if ($null -eq $State.RevalidateNullAt) { $State.RevalidateNullAt = 0 }
    if ($null -eq $State.RevalidateCallCount) { $State.RevalidateCallCount = 0 }
    if ($null -eq $State.TreeTerminationSucceeded) { $State.TreeTerminationSucceeded = $true }
    if ($null -eq $State.TreeTerminated) { $State.TreeTerminated = $false }
    if ($null -eq $State.CapturedRootPids) { $State.CapturedRootPids = @() }
    if ($null -eq $State.MatchQueryMismatchAt) { $State.MatchQueryMismatchAt = 0 }
    if ($null -eq $State.MatchQueryCount) { $State.MatchQueryCount = 0 }
    $operations = @{
        AcquireScopeLock = {
            [pscustomobject]@{ Name = "test-lock" }
        }
        ReleaseScopeLock = {
            param($Lock)
            $State.LockReleases++
        }
        ReadManifest = {
            $State.Manifest
        }
        WriteManifest = {
            param($Path, $Manifest)
            $State.WrittenManifest = $Manifest
        }
        CreateRunRoot = {
            param($Path)
            $State.RunRoots += $Path
            $Path
        }
        GetProcessIdentity = {
            param($ProcessId)
            $State.ProcessIdentity
        }
        GetListenerOwner = {
            param($Port)
            $State.ListenerPid
        }
        EndAirPlay = {
            param($Port)
            $State.EndedPorts += $Port
        }
        QuitServer = {
            param($Port)
            $State.QuitPorts += $Port
            if (-not [string]::IsNullOrWhiteSpace([string]$State.QuitError)) { throw [string]$State.QuitError }
        }
        RevalidateProcessIdentity = {
            param($Identity)
            $State.RevalidateCallCount++
            $State.RevalidatedPids += [int]$Identity.Pid
            if ([int]$State.RevalidateMismatchAt -eq [int]$State.RevalidateCallCount) { throw "identity mismatch after wait" }
            if ([int]$State.RevalidateNullAt -eq [int]$State.RevalidateCallCount -or $State.TreeTerminated) { return $null }
            $Identity
        }
        CaptureOwnedProcessTree = {
            param($Identity)
            $State.CapturedRootPids += [int]$Identity.Pid
            if ($null -ne $State.CapturedTree) { return @($State.CapturedTree) }
            return @([pscustomobject]@{
                Pid = [int]$Identity.Pid
                ParentPid = 0
                ProcessStartTimeUtc = $Identity.ProcessStartTimeUtc
                ExecutablePath = $Identity.ExecutablePath
            })
        }
        GetStillMatchingCapturedIdentities = {
            param($Captured)
            $State.MatchQueryCount++
            if ([int]$State.MatchQueryMismatchAt -eq [int]$State.MatchQueryCount) { throw "captured identity mismatch or became unverifiable" }
            $matchingPids = if ($State.TreeTerminated) {
                if ($null -eq $State.PostTerminationMatchingPids) { @() } else { @($State.PostTerminationMatchingPids) }
            } elseif ($null -ne $State.InitialMatchingPids) {
                @($State.InitialMatchingPids)
            } elseif ($State.ProcessExited) {
                @()
            } else {
                @($Captured | ForEach-Object { [int]$_.Pid })
            }
            return @($Captured | Where-Object { $matchingPids -contains [int]$_.Pid })
        }
        WaitForProcessExit = {
            param($Identity, $TimeoutMilliseconds)
            $State.WaitedPids += [int]$Identity.Pid
            [bool]$State.ProcessExited
        }
        TerminateOwnedProcessTree = {
            param($Identity)
            $State.TreeTerminationPids += [int]$Identity.Pid
            if ($State.TreeTerminationSucceeded) { $State.TreeTerminated = $true }
            [pscustomobject]@{ Succeeded = [bool]$State.TreeTerminationSucceeded; ExitCode = if ($State.TreeTerminationSucceeded) { 0 } else { 1 } }
        }
        StopProcess = {
            param($Process)
            $State.StoppedPids += [int]$Process.Pid
        }
        StartProcess = {
            param($Context)
            $State.StartContexts += $Context
            $State.ListenerPid = if ($ListenerAfterStart -gt 0) { $ListenerAfterStart } else { $State.StartedPid }
            [pscustomobject]@{
                Pid = $State.StartedPid
                ProcessStartTimeUtc = "2026-09-07T00:00:00.0000000Z"
                ExecutablePath = $CandidatePath
            }
        }
        WaitForHealth = {
            param($Context, $Process)
            if ($State.HealthFailure) {
                throw "fake health failure"
            }
            $true
        }
        GetState = {
            param($Context)
            [pscustomobject]@{ airplay = [pscustomobject]@{ receiverName = $Context.ReceiverName } }
        }
        WriteFailureEvidence = {
            param($Path, $Evidence)
            $State.FailureEvidence = $Evidence
        }
        WriteReplacementEvidence = {
            param($Path, $Evidence)
            $State.ReplacementEvidence = $Evidence
        }
    }
    foreach ($key in @($operations.Keys)) {
        $operations[$key] = $operations[$key].GetNewClosure()
    }
    return $operations
}

Describe "real-device static-audio diagnostic launcher hardening" {
    BeforeEach {
        $script:CandidatePath = Join-Path $TestDrive "imagepadserver.exe"
        Set-Content -LiteralPath $script:CandidatePath -Value "candidate" -Encoding Ascii
        $script:Candidates = [ordered]@{
            Server = $script:CandidatePath
            Receiver = $script:CandidatePath
            Bridge = $script:CandidatePath
            MediaMTX = $script:CandidatePath
            FFmpeg = $script:CandidatePath
            FFprobe = $script:CandidatePath
            GStreamerScanner = $script:CandidatePath
        }
        $script:ScopeRoot = Join-Path $TestDrive "scope"
    }

    It "fails closed for port 8080 before acquiring a lock or creating a root" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @() }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath

        { Invoke-ImagePadDiagnosticLauncher -Port 8080 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops } |
            Should Throw "8080"

        $state.RunRoots.Count | Should Be 0
        $state.LockReleases | Should Be 0
        $state.StartContexts.Count | Should Be 0
    }

    It "rejects an invalid port before acquiring a lock" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @() }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath

        { Invoke-ImagePadDiagnosticLauncher -Port 0 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops } |
            Should Throw "port"

        $state.RunRoots.Count | Should Be 0
        $state.LockReleases | Should Be 0
    }

    It "rejects an invalid candidate path before acquiring a lock" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @() }
        $invalidCandidates = [ordered]@{}
        foreach ($entry in $script:Candidates.GetEnumerator()) { $invalidCandidates[$entry.Key] = $entry.Value }
        $invalidCandidates.Server = Join-Path $TestDrive "missing-imagepadserver.exe"
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath

        { Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $invalidCandidates -ScopeRoot $script:ScopeRoot -Operations $ops } |
            Should Throw "candidate path"

        $state.RunRoots.Count | Should Be 0
        $state.LockReleases | Should Be 0
    }

    It "rejects a missing GStreamer scanner before acquiring a lock" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @() }
        $invalidCandidates = [ordered]@{}
        foreach ($entry in $script:Candidates.GetEnumerator()) { $invalidCandidates[$entry.Key] = $entry.Value }
        $invalidCandidates.GStreamerScanner = Join-Path $TestDrive "missing-gst-plugin-scanner.exe"
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath

        { Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $invalidCandidates -ScopeRoot $script:ScopeRoot -Operations $ops } |
            Should Throw "GStreamerScanner"

        $state.RunRoots.Count | Should Be 0
        $state.LockReleases | Should Be 0
    }

    It "resolves the GStreamer scanner from the packaged libexec directory" {
        $gstreamerRoot = Join-Path $TestDrive "gstreamer"

        $scanner = Get-ImagePadDiagnosticGStreamerScannerPath -GStreamerRoot $gstreamerRoot

        $scanner | Should Be (Join-Path $gstreamerRoot "libexec\gstreamer-1.0\gst-plugin-scanner.exe")
    }

    It "captures a parent with no children without querying process identity for PID zero" {
        $parent = [pscustomobject]@{
            Pid = 7100
            ProcessStartTimeUtc = "2026-09-07T04:49:12.8451700Z"
            ExecutablePath = $script:CandidatePath
        }
        Mock Get-CimInstance {
            @([pscustomobject]@{ ProcessId = 7100; ParentProcessId = 100 })
        }
        Mock Get-ImagePadDiagnosticProcessIdentity {
            param([int]$ProcessId)
            if ($ProcessId -eq 7100) { return $parent }
            throw "unexpected process identity query for PID $ProcessId"
        }
        $ops = New-ImagePadDiagnosticDefaultOperations

        $captured = @(& $ops.CaptureOwnedProcessTree $parent)

        $captured.Count | Should Be 1
        $captured[0].Pid | Should Be 7100
        Assert-MockCalled Get-ImagePadDiagnosticProcessIdentity -Times 0 -ParameterFilter { $ProcessId -eq 0 }
    }

    It "excludes a snapshot child when its PID is reused with a different start time" {
        $parent = [pscustomobject]@{
            Pid = 7100
            ProcessStartTimeUtc = "2026-09-07T04:49:12.8451700Z"
            ExecutablePath = $script:CandidatePath
        }
        $snapshotChildPath = Join-Path $TestDrive "original-child.exe"
        Mock Get-CimInstance {
            @(
                [pscustomobject]@{
                    ProcessId = 7100; ParentProcessId = 100
                    CreationDate = [DateTime]::Parse("2026-09-07T04:49:12.8451700Z")
                    ExecutablePath = $script:CandidatePath
                },
                [pscustomobject]@{
                    ProcessId = 7101; ParentProcessId = 7100
                    CreationDate = [DateTime]::Parse("2026-09-07T04:49:13.1234560Z")
                    ExecutablePath = $snapshotChildPath
                }
            )
        }
        Mock Get-ImagePadDiagnosticProcessIdentity {
            param([int]$ProcessId)
            if ($ProcessId -eq 7100) { return $parent }
            if ($ProcessId -eq 7101) {
                return [pscustomobject]@{
                    Pid = 7101
                    ProcessStartTimeUtc = "2026-09-07T04:49:14.1234560Z"
                    ExecutablePath = $snapshotChildPath
                }
            }
            throw "unexpected process identity query for PID $ProcessId"
        }
        $ops = New-ImagePadDiagnosticDefaultOperations

        $captured = @(& $ops.CaptureOwnedProcessTree $parent)

        @($captured | ForEach-Object { [int]$_.Pid }) | Should Be @(7100)
    }

    It "excludes a snapshot child when its PID is reused with a different executable path" {
        $parent = [pscustomobject]@{
            Pid = 7100
            ProcessStartTimeUtc = "2026-09-07T04:49:12.8451700Z"
            ExecutablePath = $script:CandidatePath
        }
        $snapshotChildPath = Join-Path $TestDrive "original-child.exe"
        $reusedChildPath = Join-Path $TestDrive "unrelated-child.exe"
        Mock Get-CimInstance {
            @(
                [pscustomobject]@{
                    ProcessId = 7100; ParentProcessId = 100
                    CreationDate = [DateTime]::Parse("2026-09-07T04:49:12.8451700Z")
                    ExecutablePath = $script:CandidatePath
                },
                [pscustomobject]@{
                    ProcessId = 7101; ParentProcessId = 7100
                    CreationDate = [DateTime]::Parse("2026-09-07T04:49:13.1234560Z")
                    ExecutablePath = $snapshotChildPath
                }
            )
        }
        Mock Get-ImagePadDiagnosticProcessIdentity {
            param([int]$ProcessId)
            if ($ProcessId -eq 7100) { return $parent }
            if ($ProcessId -eq 7101) {
                return [pscustomobject]@{
                    Pid = 7101
                    ProcessStartTimeUtc = "2026-09-07T04:49:13.1234560Z"
                    ExecutablePath = $reusedChildPath
                }
            }
            throw "unexpected process identity query for PID $ProcessId"
        }
        $ops = New-ImagePadDiagnosticDefaultOperations

        $captured = @(& $ops.CaptureOwnedProcessTree $parent)

        @($captured | ForEach-Object { [int]$_.Pid }) | Should Be @(7100)
    }

    It "accepts a snapshot child when only sub-microsecond start precision differs" {
        $parent = [pscustomobject]@{
            Pid = 7100
            ProcessStartTimeUtc = "2026-09-07T04:49:12.8451700Z"
            ExecutablePath = $script:CandidatePath
        }
        $childPath = Join-Path $TestDrive "owned-child.exe"
        Mock Get-CimInstance {
            @(
                [pscustomobject]@{
                    ProcessId = 7100; ParentProcessId = 100
                    CreationDate = [DateTime]::Parse("2026-09-07T04:49:12.8451700Z")
                    ExecutablePath = $script:CandidatePath
                },
                [pscustomobject]@{
                    ProcessId = 7101; ParentProcessId = 7100
                    CreationDate = [DateTime]::Parse("2026-09-07T04:49:13.1234560Z")
                    ExecutablePath = $childPath
                }
            )
        }
        Mock Get-ImagePadDiagnosticProcessIdentity {
            param([int]$ProcessId)
            if ($ProcessId -eq 7100) { return $parent }
            if ($ProcessId -eq 7101) {
                return [pscustomobject]@{
                    Pid = 7101
                    ProcessStartTimeUtc = "2026-09-07T04:49:13.1234569Z"
                    ExecutablePath = $childPath
                }
            }
            throw "unexpected process identity query for PID $ProcessId"
        }
        $ops = New-ImagePadDiagnosticDefaultOperations

        $captured = @(& $ops.CaptureOwnedProcessTree $parent)

        @($captured | ForEach-Object { [int]$_.Pid }) | Should Be @(7100, 7101)
    }

    It "accepts an exactly matching manifest for graceful replacement" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ProcessIdentity = $null; ListenerPid = 7100 }
        $prior = [pscustomobject]@{
            schema = 1
            scope = "imagepad-airplay-diagnostic"
            runID = "0123456789abcdef0123456789abcdef"
            pid = 7100
            processStartTimeUtc = "2026-09-06T23:00:00.0000000Z"
            candidatePath = $script:CandidatePath
            port = 63932
            listenerPid = 7100
            host = "127.0.0.1"
            receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $state.Manifest = $prior
        $state.ProcessIdentity = [pscustomobject]@{ Pid = 7100; ProcessStartTimeUtc = $prior.processStartTimeUtc; ExecutablePath = $script:CandidatePath }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        $result = Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops

        $state.QuitPorts | Should Be @(63932)
        $state.EndedPorts.Count | Should Be 0
        $state.StoppedPids.Count | Should Be 0
        $state.CapturedRootPids | Should Be @(7100)
        $state.WaitedPids.Count | Should Be 0
        $state.TreeTerminationPids.Count | Should Be 0
        $result.ReplacedPrior | Should Be $true
        $state.WrittenManifest.pid | Should Be 7200
    }

    It "replaces an owned prior diagnostic built from a different candidate path" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ProcessIdentity = $null; ListenerPid = 7100 }
        $priorCandidatePath = Join-Path $TestDrive "previous-imagepadserver.exe"
        Set-Content -LiteralPath $priorCandidatePath -Value "previous-candidate" -Encoding Ascii
        $prior = [pscustomobject]@{
            schema = 1
            scope = "imagepad-airplay-diagnostic"
            runID = "0123456789abcdef0123456789abcdef"
            pid = 7100
            processStartTimeUtc = "2026-09-06T23:00:00.0000000Z"
            candidatePath = $priorCandidatePath
            port = 63932
            listenerPid = 7100
            host = "127.0.0.1"
            receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $state.Manifest = $prior
        $state.ProcessIdentity = [pscustomobject]@{ Pid = 7100; ProcessStartTimeUtc = $prior.processStartTimeUtc; ExecutablePath = $priorCandidatePath }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        $result = Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops

        $state.QuitPorts | Should Be @(63932)
        $state.CapturedRootPids | Should Be @(7100)
        $result.ReplacedPrior | Should Be $true
    }

    It "accepts an exact start time after a real JSON DateTime roundtrip" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ProcessIdentity = $null; ListenerPid = 7100 }
        $priorJSON = [ordered]@{
            schema = 1
            scope = "imagepad-airplay-diagnostic"
            runID = "0123456789abcdef0123456789abcdef"
            pid = 7100
            processStartTimeUtc = "2026-09-07T04:49:12.8451700Z"
            candidatePath = $script:CandidatePath
            port = 63932
            listenerPid = 7100
            host = "127.0.0.1"
            receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        } | ConvertTo-Json -Depth 5
        $state.Manifest = $priorJSON | ConvertFrom-Json
        $state.Manifest.processStartTimeUtc | Should BeOfType ([DateTime])
        $state.ProcessIdentity = [pscustomobject]@{
            Pid = 7100
            ProcessStartTimeUtc = [DateTimeOffset]::ParseExact(
                "2026-09-07T04:49:12.8451700Z",
                "yyyy-MM-dd'T'HH:mm:ss.fffffff'Z'",
                [Globalization.CultureInfo]::InvariantCulture,
                [Globalization.DateTimeStyles]::AssumeUniversal
            )
            ExecutablePath = $script:CandidatePath
        }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        $result = Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops

        $result.ReplacedPrior | Should Be $true
        $state.QuitPorts | Should Be @(63932)
        $state.StartContexts.Count | Should Be 1
    }

    It "fails closed for an ambiguous start-time string" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 7100 }
        $state.Manifest = [pscustomobject]@{
            schema = 1; scope = "imagepad-airplay-diagnostic"; runID = "0123456789abcdef0123456789abcdef"; pid = 7100
            processStartTimeUtc = "09/07/2026 04:49:12"; candidatePath = $script:CandidatePath; port = 63932
            listenerPid = 7100; host = "127.0.0.1"; receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $state.ProcessIdentity = [pscustomobject]@{ Pid = 7100; ProcessStartTimeUtc = "2026-09-07T04:49:12.0000000Z"; ExecutablePath = $script:CandidatePath }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath

        { Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops } |
            Should Throw "start time"

        $state.QuitPorts.Count | Should Be 0
        $state.StartContexts.Count | Should Be 0
    }

    It "refuses every PID, start-time, path, and port mismatch without stopping the prior process" {
        foreach ($mismatch in @(
            @{ Name = "PID"; ProcessPid = 7101; ManifestPid = 7100; Start = "2026-09-06T23:00:00.0000000Z"; Path = $script:CandidatePath; Port = 63932; Listener = 7100 },
            @{ Name = "start"; ProcessPid = 7100; ManifestPid = 7100; Start = "2026-09-06T22:59:59.0000000Z"; Path = $script:CandidatePath; Port = 63932; Listener = 7100 },
            @{ Name = "path"; ProcessPid = 7100; ManifestPid = 7100; Start = "2026-09-06T23:00:00.0000000Z"; Path = (Join-Path $TestDrive "other.exe"); Port = 63932; Listener = 7100 },
            @{ Name = "port"; ProcessPid = 7100; ManifestPid = 7100; Start = "2026-09-06T23:00:00.0000000Z"; Path = $script:CandidatePath; Port = 63931; Listener = 7100 }
        )) {
            $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = $mismatch.Listener }
            $state.ProcessIdentity = [pscustomobject]@{ Pid = $mismatch.ProcessPid; ProcessStartTimeUtc = "2026-09-06T23:00:00.0000000Z"; ExecutablePath = $script:CandidatePath }
            $state.Manifest = [pscustomobject]@{
                schema = 1; scope = "imagepad-airplay-diagnostic"; runID = "0123456789abcdef0123456789abcdef"; pid = $mismatch.ManifestPid
                processStartTimeUtc = $mismatch.Start; candidatePath = $mismatch.Path; port = $mismatch.Port; listenerPid = 7100; host = "127.0.0.1"
                receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"; root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
            }
            $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath

            $thrown = $false
            try { Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops | Out-Null } catch { $thrown = $true }
            $thrown | Should Be $true
            $state.StoppedPids.Count | Should Be 0
            $state.EndedPorts.Count | Should Be 0
            $state.QuitPorts.Count | Should Be 0
            $state.WaitedPids.Count | Should Be 0
            $state.TreeTerminationPids.Count | Should Be 0
            $state.CapturedRootPids.Count | Should Be 0
            $state.StartContexts.Count | Should Be 0
        }
    }

    It "accepts a prior process that exits during graceful quit without waiting or killing" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 7100; ProcessExited = $true }
        $prior = [pscustomobject]@{
            schema = 1; scope = "imagepad-airplay-diagnostic"; runID = "0123456789abcdef0123456789abcdef"; pid = 7100
            processStartTimeUtc = "2026-09-06T23:00:00.0000000Z"; candidatePath = $script:CandidatePath; port = 63932
            listenerPid = 7100; host = "127.0.0.1"; receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $state.Manifest = $prior
        $state.ProcessIdentity = [pscustomobject]@{ Pid = 7100; ProcessStartTimeUtc = $prior.processStartTimeUtc; ExecutablePath = $script:CandidatePath }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops | Out-Null

        $state.QuitPorts | Should Be @(63932)
        $state.WaitedPids.Count | Should Be 0
        $state.TreeTerminationPids.Count | Should Be 0
    }

    It "kills the exactly revalidated prior process only after graceful quit timeout" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 7100; ProcessExited = $false }
        $prior = [pscustomobject]@{
            schema = 1; scope = "imagepad-airplay-diagnostic"; runID = "0123456789abcdef0123456789abcdef"; pid = 7100
            processStartTimeUtc = "2026-09-06T23:00:00.0000000Z"; candidatePath = $script:CandidatePath; port = 63932
            listenerPid = 7100; host = "127.0.0.1"; receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $state.Manifest = $prior
        $state.ProcessIdentity = [pscustomobject]@{ Pid = 7100; ProcessStartTimeUtc = $prior.processStartTimeUtc; ExecutablePath = $script:CandidatePath }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops | Out-Null

        $state.QuitPorts | Should Be @(63932)
        $state.CapturedRootPids | Should Be @(7100)
        $state.WaitedPids | Should Be @(7100)
        $state.TreeTerminationPids | Should Be @(7100)
    }

    It "records graceful quit failure and continues through verified tree fallback" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 7100; ProcessExited = $false; QuitError = "HTTP 500 from /api/quit" }
        $prior = [pscustomobject]@{
            schema = 1; scope = "imagepad-airplay-diagnostic"; runID = "0123456789abcdef0123456789abcdef"; pid = 7100
            processStartTimeUtc = "2026-09-06T23:00:00.0000000Z"; candidatePath = $script:CandidatePath; port = 63932
            listenerPid = 7100; host = "127.0.0.1"; receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $state.Manifest = $prior
        $state.ProcessIdentity = [pscustomobject]@{ Pid = 7100; ProcessStartTimeUtc = $prior.processStartTimeUtc; ExecutablePath = $script:CandidatePath }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops | Out-Null

        $state.QuitPorts | Should Be @(63932)
        $state.TreeTerminationPids | Should Be @(7100)
        $state.StartContexts.Count | Should Be 1
        $state.ReplacementEvidence.quit.error | Should Match "HTTP 500"
    }

    It "fails closed when parent identity changes after graceful wait timeout" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 7100; ProcessExited = $false; MatchQueryMismatchAt = 2 }
        $prior = [pscustomobject]@{
            schema = 1; scope = "imagepad-airplay-diagnostic"; runID = "0123456789abcdef0123456789abcdef"; pid = 7100
            processStartTimeUtc = "2026-09-06T23:00:00.0000000Z"; candidatePath = $script:CandidatePath; port = 63932
            listenerPid = 7100; host = "127.0.0.1"; receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $state.Manifest = $prior
        $state.ProcessIdentity = [pscustomobject]@{ Pid = 7100; ProcessStartTimeUtc = $prior.processStartTimeUtc; ExecutablePath = $script:CandidatePath }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        { Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops } |
            Should Throw "identity mismatch"

        $state.WaitedPids | Should Be @(7100)
        $state.TreeTerminationPids.Count | Should Be 0
        $state.StartContexts.Count | Should Be 0
    }

    It "does not start a replacement when process-tree termination fails" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 7100; ProcessExited = $false; TreeTerminationSucceeded = $false }
        $prior = [pscustomobject]@{
            schema = 1; scope = "imagepad-airplay-diagnostic"; runID = "0123456789abcdef0123456789abcdef"; pid = 7100
            processStartTimeUtc = "2026-09-06T23:00:00.0000000Z"; candidatePath = $script:CandidatePath; port = 63932
            listenerPid = 7100; host = "127.0.0.1"; receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $state.Manifest = $prior
        $state.ProcessIdentity = [pscustomobject]@{ Pid = 7100; ProcessStartTimeUtc = $prior.processStartTimeUtc; ExecutablePath = $script:CandidatePath }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        { Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops } |
            Should Throw "process-tree termination failed"

        $state.TreeTerminationPids | Should Be @(7100)
        $state.StartContexts.Count | Should Be 0
    }

    It "terminates a captured child that survives graceful parent exit" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 7100; ProcessExited = $true; InitialMatchingPids = @(7101); PostTerminationMatchingPids = @() }
        $prior = [pscustomobject]@{
            schema = 1; scope = "imagepad-airplay-diagnostic"; runID = "0123456789abcdef0123456789abcdef"; pid = 7100
            processStartTimeUtc = "2026-09-06T23:00:00.0000000Z"; candidatePath = $script:CandidatePath; port = 63932
            listenerPid = 7100; host = "127.0.0.1"; receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $parentIdentity = [pscustomobject]@{ Pid = 7100; ParentPid = 100; ProcessStartTimeUtc = $prior.processStartTimeUtc; ExecutablePath = $script:CandidatePath }
        $childIdentity = [pscustomobject]@{ Pid = 7101; ParentPid = 7100; ProcessStartTimeUtc = "2026-09-06T23:00:01.0000000Z"; ExecutablePath = (Join-Path $TestDrive "mediamtx.exe") }
        $state.Manifest = $prior
        $state.ProcessIdentity = $parentIdentity
        $state.CapturedTree = @($parentIdentity, $childIdentity)
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops | Out-Null

        $state.CapturedRootPids | Should Be @(7100)
        $state.TreeTerminationPids | Should Be @(7101)
        $state.StartContexts.Count | Should Be 1
    }

    It "fails closed when a captured child survives successful taskkill" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 7100; ProcessExited = $false; InitialMatchingPids = @(7100, 7101); PostTerminationMatchingPids = @(7101) }
        $prior = [pscustomobject]@{
            schema = 1; scope = "imagepad-airplay-diagnostic"; runID = "0123456789abcdef0123456789abcdef"; pid = 7100
            processStartTimeUtc = "2026-09-06T23:00:00.0000000Z"; candidatePath = $script:CandidatePath; port = 63932
            listenerPid = 7100; host = "127.0.0.1"; receiverName = "IPS-DIAG-RTSPTCP-63932-old12345"
            root = (Join-Path (Join-Path $script:ScopeRoot "runs") "0123456789abcdef0123456789abcdef")
        }
        $parentIdentity = [pscustomobject]@{ Pid = 7100; ParentPid = 100; ProcessStartTimeUtc = $prior.processStartTimeUtc; ExecutablePath = $script:CandidatePath }
        $childIdentity = [pscustomobject]@{ Pid = 7101; ParentPid = 7100; ProcessStartTimeUtc = "2026-09-06T23:00:01.0000000Z"; ExecutablePath = (Join-Path $TestDrive "mediamtx.exe") }
        $state.Manifest = $prior
        $state.ProcessIdentity = $parentIdentity
        $state.CapturedTree = @($parentIdentity, $childIdentity)
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 7200

        { Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops } |
            Should Throw "captured process identities remain"

        $state.TreeTerminationPids | Should Be @(7100)
        $state.StartContexts.Count | Should Be 0
    }

    It "rejects health that is served by another process and cleans only the new process" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 9002; ProcessExited = $false }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 9001 -ListenerAfterStart 9002
        $ops.GetListenerOwner = { param($Port) 9002 }.GetNewClosure()

        $thrown = $false
        try { Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops | Out-Null } catch { $thrown = $true }
        $thrown | Should Be $true

        $state.TreeTerminationPids | Should Be @(9001)
        $state.CapturedRootPids | Should Be @(9001)
        $state.StoppedPids.Count | Should Be 0
        $state.WrittenManifest | Should Be $null
    }

    It "generates unique receiver names, environment values, and per-run roots" {
        $runs = @()
        foreach ($i in 1..2) {
            $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 9000 + $i }
            $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid (9000 + $i)
            $result = Invoke-ImagePadDiagnosticLauncher -Port (63940 + $i) -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops
            $runs += [pscustomobject]@{ Result = $result; State = $state }
        }

        $runs[0].Result.ReceiverName | Should Match '^IPS-DIAG-RTSPTCP-63941-[A-Za-z0-9]{8}$'
        $runs[1].Result.ReceiverName | Should Match '^IPS-DIAG-RTSPTCP-63942-[A-Za-z0-9]{8}$'
        $runs[0].Result.ReceiverName | Should Not Be $runs[1].Result.ReceiverName
        $runs[0].State.StartContexts[0].Environment.IMAGEPAD_AIRPLAY_DIAGNOSTIC_RECEIVER_NAME | Should Be $runs[0].Result.ReceiverName
        $runs[0].State.StartContexts[0].Environment.IMAGEPAD_HOST | Should Be "127.0.0.1"
        $runs[0].Result.Root | Should Not Be $runs[1].Result.Root
    }

    It "forces inherited UxPlay and scanner environment values to validated candidates" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 9001 }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 9001
        $inherited = @{
            IMAGEPAD_UXPLAY = "C:\\stale\\uxplay.exe"
            GST_PLUGIN_SCANNER = "C:\\stale\\gst-plugin-scanner.exe"
        }

        Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops -BaseEnvironment $inherited | Out-Null

        $state.StartContexts[0].Environment.IMAGEPAD_UXPLAY | Should Be $script:CandidatePath
        $state.StartContexts[0].Environment.IMAGEPAD_AIRPLAY_RECEIVER | Should Be $script:CandidatePath
        $state.StartContexts[0].Environment.GST_PLUGIN_SCANNER | Should Be $script:CandidatePath
    }

    It "leaves failure evidence and stops only the newly created process on launch failure" {
        $state = @{ LockReleases = 0; RunRoots = @(); StartContexts = @(); EndedPorts = @(); StoppedPids = @(); Manifest = $null; ListenerPid = 9001; ProcessExited = $false }
        $ops = New-TestDiagnosticOperations -State $state -CandidatePath $script:CandidatePath -StartedPid 9001 -HealthFailure

        { Invoke-ImagePadDiagnosticLauncher -Port 63932 -CandidatePaths $script:Candidates -ScopeRoot $script:ScopeRoot -Operations $ops } |
            Should Throw "fake health failure"

        $state.TreeTerminationPids | Should Be @(9001)
        $state.CapturedRootPids | Should Be @(9001)
        $state.StoppedPids.Count | Should Be 0
        $state.FailureEvidence | Should Not Be $null
        $state.WrittenManifest | Should Be $null
    }
}
