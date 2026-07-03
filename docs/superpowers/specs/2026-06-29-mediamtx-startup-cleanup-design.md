# MediaMTX Startup Cleanup Design

## Goal

Prevent an ImagePadServer-owned MediaMTX process left by a crash from surviving the next app startup, without terminating unrelated MediaMTX processes.

## Ownership Contract

A MediaMTX process is eligible for startup cleanup only when both conditions hold:

1. Its executable is MediaMTX.
2. Its command line references an ImagePadServer runtime configuration under a directory whose name starts with `imagepad-mediamtx-`.

Executable-name matching alone is insufficient. Listening-port matching is also insufficient because MediaMTX uses dynamically allocated ports.

## Cleanup Flow

1. ImagePadServer confirms no healthy local server is already running.
2. Existing FFmpeg tracked-process and RTMP-port cleanup runs unchanged.
3. Startup enumerates MediaMTX candidates and kills only candidates satisfying the ownership contract.
4. Cleanup logs the number stopped and any inspection or termination errors.
5. Normal tool warm-up and server startup continue. Cleanup failure is reported but does not abort startup.

## PID Ledger

The MediaMTX runtime records its child PID in an app-data ledger after process start and removes it after normal process exit. Startup checks ledger entries first, validates current process identity and ownership markers, then removes stale entries. A process scan using the same ownership contract covers crashes before ledger persistence or corrupted/missing ledgers.

The ledger is advisory and never authorizes a kill without live process identity validation, preventing PID reuse from terminating an unrelated process.

## Platform Behavior

- Windows: enumerate process command lines through CIM and terminate the owned process tree with `taskkill /T /F`.
- Linux: inspect `/proc/<pid>/cmdline` and terminate the validated process.
- Unsupported platforms: return a descriptive non-fatal cleanup error.

## Testing

- Reject a process named MediaMTX without the ImagePadServer runtime marker.
- Reject a marked command line whose executable is not MediaMTX.
- Accept quoted and unquoted MediaMTX command lines with the runtime marker.
- Validate stale PID-ledger entries before killing and remove stale/invalid entries.
- Verify startup invokes MediaMTX cleanup after FFmpeg cleanup and continues on cleanup errors.
- Run focused process/OBS tests and `go test ./... -count=1`.

## Non-Goals

- Killing every MediaMTX process by name.
- Reusing the sidecar's random ports across app launches.
- Changing normal ordered shutdown or MediaMTX streaming configuration.
