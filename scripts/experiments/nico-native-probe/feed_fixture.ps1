param(
    [Parameter(Mandatory = $true)][string]$Compositor,
    [Parameter(Mandatory = $true)][string]$Fixture,
    [Parameter(Mandatory = $true)][string]$Output
)

$ErrorActionPreference = 'Stop'
$stderrPath = "$Output.stderr"
$psi = [Diagnostics.ProcessStartInfo]::new()
$psi.FileName = (Resolve-Path -LiteralPath $Compositor).Path
$psi.Arguments = '--stdin'
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$process = [Diagnostics.Process]::new()
$process.StartInfo = $psi
$input = [IO.File]::OpenRead((Resolve-Path -LiteralPath $Fixture).Path)
$outputStream = [IO.File]::Create((Resolve-Path -LiteralPath (Split-Path -Parent $Output)).Path + '\' + (Split-Path -Leaf $Output))
$stderrTask = $null
$started = $false
try {
    if (-not $process.Start()) { throw 'compositor process did not start' }
    $started = $true
    $stdoutTask = $process.StandardOutput.BaseStream.CopyToAsync($outputStream)
    $stderrTask = $process.StandardError.ReadToEndAsync()
    $input.CopyTo($process.StandardInput.BaseStream)
    $process.StandardInput.Close()
    $process.WaitForExit()
    $stdoutTask.GetAwaiter().GetResult() | Out-Null
    [IO.File]::WriteAllText($stderrPath, $stderrTask.GetAwaiter().GetResult())
    exit $process.ExitCode
}
finally {
    $input.Dispose()
    $outputStream.Dispose()
    if ($started -and -not $process.HasExited) { $process.Kill() }
    $process.Dispose()
}
