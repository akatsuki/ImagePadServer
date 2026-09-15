$scriptPath = Join-Path $PSScriptRoot "..\test-uxplay-airplay-control.ps1"
. $scriptPath

Describe "UxPlay AirPlay control runner helpers" {
    It "formats Windows exception exit codes as unsigned hexadecimal" {
        Format-UnsignedExitCode 3221226356 | Should Be "0xc0000374"
        Format-UnsignedExitCode 3221225477 | Should Be "0xc0000005"
        Format-UnsignedExitCode -1073741515 | Should Be "0xc0000135"
        Format-UnsignedExitCode 0 | Should Be "0x00000000"
    }

    It "classifies an unexpected receiver exit as a failure" {
        $got = New-ControlCaseResult -FragmentSize 1 -Status "FAIL" -ExitCode 3221226356 -ErrorText "receiver exited" -CompletedIterations 17
        $got.layer | Should Be "uxplay-control"
        $got.status | Should Be "FAIL"
        $got.exitCodeHex | Should Be "0xc0000374"
        $got.fragmentSize | Should Be 1
        $got.completedIterations | Should Be 17
    }

    It "includes a packaged sibling GStreamer bin directory in the child PATH" {
        $root = Join-Path $TestDrive "package"
        $receiverDir = Join-Path $root "uxplay-source-clock"
        $gstBin = Join-Path $root "gstreamer\bin"
        New-Item -ItemType Directory -Force -Path $receiverDir, $gstBin | Out-Null
        $paths = @(Get-ReceiverRuntimePaths (Join-Path $receiverDir "uxplay-source-clock.exe"))
        $paths.Count | Should Be 2
        $paths[0] | Should Be $receiverDir
        $paths[1] | Should Be $gstBin
    }
}
