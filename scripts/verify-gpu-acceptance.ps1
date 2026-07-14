[CmdletBinding()]
param(
  [string]$Sidecar = "",
  [string]$Evidence = "",
  [int]$SoakSeconds = 0
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $PSScriptRoot
$sidecarDir = Join-Path $repo "gpu\playlist-compositord"
if (-not $Evidence) {
  $Evidence = Join-Path $repo "output\gpu-acceptance-$(Get-Date -Format yyyyMMdd-HHmmss).json"
}
New-Item -ItemType Directory -Force (Split-Path -Parent $Evidence) | Out-Null

if (-not $Sidecar) {
  $Sidecar = Join-Path $sidecarDir "target\release\playlist-compositord.exe"
  if (-not (Test-Path $Sidecar)) {
    $Sidecar = Join-Path $sidecarDir "target\debug\playlist-compositord.exe"
  }
}

$record = [ordered]@{
  schema = 1
  status = "BLOCKED"
  reason = "sidecar_not_built"
  os = [System.Runtime.InteropServices.RuntimeInformation]::OSDescription
  commit = (git -C $repo rev-parse HEAD 2>$null)
  adapter_name = ""
  soak_seconds = $SoakSeconds
  soak_frames = 0
  device_type = ""
  backend = ""
  protocol_version = 0
  sidecar = $Sidecar
  captured_at = (Get-Date).ToUniversalTime().ToString("o")
}

if (Test-Path $Sidecar) {
  $record.reason = "gpu_renderer_unavailable"
  $psi = [Diagnostics.ProcessStartInfo]::new()
  $psi.FileName = $Sidecar
  $psi.UseShellExecute = $false
  $psi.RedirectStandardInput = $true
  $psi.RedirectStandardOutput = $true
  $psi.CreateNoWindow = $true
  $p = [Diagnostics.Process]::Start($psi)
  $p.StandardInput.WriteLine('{"type":"hello","version":1,"session":"acceptance"}')
  $p.StandardInput.Flush()
  $line = $p.StandardOutput.ReadLine()
  if ($line) {
    try {
      $response = $line | ConvertFrom-Json
      $record.protocol_version = [int]$response.version
      if ($response.type -eq "hello_ack" -and $response.adapter) {
        $p.StandardInput.WriteLine('{"type":"health"}')
        $p.StandardInput.Flush()
        $healthLine = $p.StandardOutput.ReadLine()
        $health = $healthLine | ConvertFrom-Json
        $record.status = if ($health.ready -eq $true) { "ADAPTER_PASS" } else { "BLOCKED" }
        $record.reason = if ($health.ready -eq $true) { "hardware_adapter_ready" } else { "adapter_not_ready" }
        $record.adapter_name = [string]$response.adapter
          if ($record.status -eq "ADAPTER_PASS") {
          $p.StandardInput.WriteLine('{"type":"render","version":1,"width":16,"height":16,"sequence":1,"pts_ns":0}')
          $p.StandardInput.Flush()
          $frameLine = $p.StandardOutput.ReadLine()
          $frame = $frameLine | ConvertFrom-Json
          $gpuFrame = $frame.frame
          if ($frame.type -ne "frame" -or [int]$gpuFrame.width -ne 16 -or [int]$gpuFrame.height -ne 16 -or [string]::IsNullOrWhiteSpace([string]$gpuFrame.payload)) {
            $record.status = "BLOCKED"
            $record.reason = "render_frame_contract_failed"
          } else {
            $record.reason = "hardware_adapter_and_render_ready"
          }
          if ($record.status -eq "ADAPTER_PASS" -and $SoakSeconds -gt 0) {
            $deadline = [DateTime]::UtcNow.AddSeconds($SoakSeconds)
            $sequence = 2
            while ([DateTime]::UtcNow -lt $deadline) {
              $p.StandardInput.WriteLine((('{"type":"render","version":1,"width":16,"height":16,"sequence":' + $sequence + ',"pts_ns":' + ($sequence * 33333333) + '}')))
              $p.StandardInput.Flush()
              $soakLine = $p.StandardOutput.ReadLine()
              $soak = $soakLine | ConvertFrom-Json
              if ($soak.type -ne "frame" -or $soak.frame.sequence -ne $sequence) { $record.status = "BLOCKED"; $record.reason = "soak_frame_contract_failed"; break }
              $record.soak_frames++
              $sequence++
            }
            if ($record.status -eq "ADAPTER_PASS") { $record.reason = "hardware_adapter_render_soak_ready" }
          }
        }
      } elseif ($response.code) {
        $record.reason = [string]$response.code
      }
    } catch {
      $record.reason = "invalid_sidecar_response"
    }
  }
  try {
    $p.StandardInput.WriteLine('{"type":"shutdown"}')
    $p.StandardInput.Flush()
    $p.WaitForExit(1500) | Out-Null
  } catch { }
  if (-not $p.HasExited) { $p.Kill() }
}

$record | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $Evidence -Encoding UTF8
Write-Output ($record | ConvertTo-Json -Depth 5)
if ($record.status -eq "BLOCKED") { exit 2 }
exit 0
