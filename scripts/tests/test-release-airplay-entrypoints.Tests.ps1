Describe "AirPlay release entrypoint contract" {
    BeforeAll {
        $scriptPath = (Resolve-Path (Join-Path $PSScriptRoot "..\build-release.sh")).Path
        $text = Get-Content -LiteralPath $scriptPath -Raw
    }

    It "does not invent a runtime URL when the pinned asset is absent" {
        ($text.Contains('AIRPLAY_RUNTIME_ARCHIVE_URL="${AIRPLAY_RUNTIME_ARCHIVE_URL:-}"')) | Should Be $true
        ($text.Contains('NOT_CONFIGURED')) | Should Be $true
    }

    It "accepts only an HTTPS pinned runtime asset and validates its hashes" {
        ($text.Contains('https://*)')) | Should Be $true
        ($text.Contains('AIRPLAY_RUNTIME_ARCHIVE_SHA256 must be 64 hexadecimal characters')) | Should Be $true
        ($text.Contains('AIRPLAY_RUNTIME_ARCHIVE_SIZE must be a positive integer')) | Should Be $true
    }

    It "embeds the bootstrap in the executable and keeps the ZIP executable-only" {
        ($text.Contains('airplay-runtime-bootstrap.json')) | Should Be $true
        ($text.Contains('embeddedRuntimeBootstrapBase64')) | Should Be $true
        ($text.Contains('zip -q -j -X "$archive" "$exe"')) | Should Be $true
    }

    It "requires the H264 archive contract before staging the embedded ZIP" {
        $gate = $text.IndexOf('python3 "$ROOT_DIR/scripts/verify-airplay-h264-archive.py"')
        $copy = $text.IndexOf('cp "$AIRPLAY_RUNTIME_ARCHIVE_PATH" "$payload_dir/runtime.zip"')
        ($gate -ge 0 -and $gate -lt $copy) | Should Be $true
    }
}
