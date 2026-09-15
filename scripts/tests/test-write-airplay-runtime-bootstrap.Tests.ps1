$scriptPath = (Resolve-Path (Join-Path $PSScriptRoot "..\write-airplay-runtime-bootstrap.ps1")).Path

function Get-BootstrapFunctionDefinition([string]$Name) {
    $tokens = $null
    $errors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$errors)
    if ($errors.Count -ne 0) { throw "bootstrap script parse failed: $($errors[0].Message)" }
    $definition = $ast.Find({
        param($node)
        $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $Name
    }, $true)
    if ($null -eq $definition) { throw "bootstrap function was not found: $Name" }
    $definition.Extent.Text
}

function New-TestRuntimeArchive([string]$Path, [string]$RuntimeSetID) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $file = [IO.File]::Create($Path)
    $zip = [IO.Compression.ZipArchive]::new($file, [IO.Compression.ZipArchiveMode]::Create, $false)
    try {
        $entries = [ordered]@{
            "license-manifest.json" = '{"schema":1,"runtimeSetID":"' + $RuntimeSetID + '"}'
            "SOURCE-OFFER.md" = "runtime source offer $RuntimeSetID"
        }
        foreach ($entryData in $entries.GetEnumerator()) {
            $entry = $zip.CreateEntry($entryData.Key)
            $writer = [IO.StreamWriter]::new($entry.Open(), [Text.Encoding]::UTF8)
            try { $writer.Write($entryData.Value) } finally { $writer.Dispose() }
        }
    } finally {
        $zip.Dispose()
        $file.Dispose()
    }
}

Describe "AirPlay runtime bootstrap writer" {
    BeforeAll {
        . ([scriptblock]::Create((Get-BootstrapFunctionDefinition "Get-Sha256Hex")))
        . ([scriptblock]::Create((Get-BootstrapFunctionDefinition "Read-ZipEntryBytes")))
        . ([scriptblock]::Create((Get-BootstrapFunctionDefinition "Write-AirPlayRuntimeBootstrap")))
    }

    It "writes archive size/hash and metadata hashes from the same ZIP" {
        $archive = Join-Path $TestDrive "runtime.zip"
        $output = Join-Path $TestDrive "airplay-runtime-bootstrap.json"
        New-TestRuntimeArchive -Path $archive -RuntimeSetID "test-set-1"

        Write-AirPlayRuntimeBootstrap -ArchivePath $archive -RuntimeSetID "test-set-1" `
            -ArchiveURL "https://example.invalid/airplay-runtime.zip" -OutputPath $output | Out-Null

        $record = Get-Content -LiteralPath $output -Raw | ConvertFrom-Json
        $record.schema | Should Be 1
        $record.runtimeSetID | Should Be "test-set-1"
        $record.archiveSize | Should Be ([int64](Get-Item -LiteralPath $archive).Length)
        $record.archiveSha256 | Should Be ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant())
        $record.licenseManifestSha256 | Should Match '^[0-9a-f]{64}$'
        $record.sourceOfferSha256 | Should Match '^[0-9a-f]{64}$'
    }

    It "rejects non-HTTPS URLs before producing an output" {
        $archive = Join-Path $TestDrive "runtime-http.zip"
        $output = Join-Path $TestDrive "not-written.json"
        New-TestRuntimeArchive -Path $archive -RuntimeSetID "test-set-2"
        $message = $null
        try {
            Write-AirPlayRuntimeBootstrap -ArchivePath $archive -RuntimeSetID "test-set-2" `
                -ArchiveURL "http://example.invalid/runtime.zip" -OutputPath $output | Out-Null
        } catch {
            $message = $_.Exception.Message
        }
        $message | Should Not Be $null
        (Test-Path -LiteralPath $output) | Should Be $false
    }
}
