$helper = Join-Path $PSScriptRoot "..\airplay-h264-contract.ps1"
if (Test-Path -LiteralPath $helper) { . $helper }

Describe "AirPlay package H264 contract" {
    BeforeEach {
        $root = Join-Path $TestDrive "bridge"
        New-Item -ItemType Directory -Path $root -Force | Out-Null
        $bridge = Join-Path $root "airplay-gstreamer-bridge.exe"
        [IO.File]::WriteAllBytes($bridge, [byte[]](1, 2, 3))
        $proof = Join-Path $root "imagepad-airplay-gstreamer-bridge-build.json"
        $record = @{
            schema = 1
            configuration = "Release"
            executable = "airplay-gstreamer-bridge.exe"
            executableSha256 = (Get-FileHash -LiteralPath $bridge -Algorithm SHA256).Hash.ToLowerInvariant()
            videoContract = @{
                id = "rtsp-h264-single-slice-v1"
                slicesPerFrame = 1
                testName = "airplay_source_clock_single_slice"
                testPassed = $true
            }
        }
    }

    It "accepts a tested executable with a matching hash" {
        $record | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $proof -Encoding UTF8
        $actual = Assert-AirPlayH264BuildContract -BridgeRoot $root
        $actual.executableSha256 | Should Be $record.executableSha256
    }

    It "rejects old provenance without a video contract" {
        $record.Remove("videoContract")
        $record | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $proof -Encoding UTF8
        { Assert-AirPlayH264BuildContract -BridgeRoot $root } | Should Throw "H.264"
    }

    It "rejects multi-slice and failed or differently named tests" {
        foreach ($change in @(@{slicesPerFrame=8}, @{slicesPerFrame=$true}, @{testPassed=$false}, @{testPassed="true"}, @{testName="other"}, @{id="old"})) {
            $candidate = $record.Clone()
            $candidate.videoContract = $record.videoContract.Clone()
            foreach ($key in $change.Keys) { $candidate.videoContract[$key] = $change[$key] }
            $candidate | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $proof -Encoding UTF8
            { Assert-AirPlayH264BuildContract -BridgeRoot $root } | Should Throw "H.264"
        }
    }

    It "rejects an executable changed after its passing test" {
        $record | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $proof -Encoding UTF8
        [IO.File]::WriteAllBytes($bridge, [byte[]](4, 5, 6))
        { Assert-AirPlayH264BuildContract -BridgeRoot $root } | Should Throw "hash"
    }

    It "rejects a missing proof" {
        if (Test-Path -LiteralPath $proof) { Remove-Item -LiteralPath $proof }
        { Assert-AirPlayH264BuildContract -BridgeRoot $root } | Should Throw "H.264"
    }
}
