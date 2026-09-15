$scriptPath = Join-Path $PSScriptRoot "..\test-airplay-iphone-model.ps1"
. $scriptPath

Describe "Merge-CaseResults" {
    It "reports the first failing layer and blocks dependent layers" {
        $got = Merge-CaseResults @(
            [pscustomobject]@{ layer="uxplay-control"; status="FAIL" },
            [pscustomobject]@{ layer="media"; status="NOT_RUN" }
        )
        $got.status | Should Be "FAIL"
        $got.firstFailureLayer | Should Be "uxplay-control"
    }

    It "passes when every case passes" {
        $got = Merge-CaseResults @(
            [pscustomobject]@{ layer="native"; status="PASS" },
            [pscustomobject]@{ layer="media"; status="PASS" }
        )
        $got.status | Should Be "PASS"
        $got.firstFailureLayer | Should Be ""
    }
}
