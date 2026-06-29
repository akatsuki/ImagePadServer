# MediaMTX Startup Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove only ImagePadServer-owned stale MediaMTX processes during application startup.

**Architecture:** Add a reusable command-line ownership matcher beside the existing FFmpeg cleanup code, then add a MediaMTX-specific PID ledger and cleanup facade in `internal/obsrtmp`. Startup invokes it after FFmpeg cleanup; every kill requires live executable and `imagepad-mediamtx-` command-line validation.

**Tech Stack:** Go 1.25, Windows CIM/taskkill, Linux `/proc`, atomic JSON persistence, existing ImagePadServer process registry.

---

### Task 1: Add safe owned-process cleanup primitives

**Files:**
- Modify: `internal/video/process_registry.go`
- Create: `internal/video/process_registry_test.go`

- [ ] **Step 1: Write failing matcher and cleanup tests**

Cover quoted/unquoted `mediamtx.exe` command lines containing `imagepad-mediamtx-`, rejection of other executables, rejection without the marker, deduplication, and kill-error aggregation. Inject process listing, command-line reading, and tree termination so tests never kill a real process.

- [ ] **Step 2: Verify RED**

```powershell
rtk go test ./internal/video -run 'OwnedProcess|KillOwned' -count=1 -v
```

Expected: build failure because the owned-process matcher and cleanup function do not exist.

- [ ] **Step 3: Implement the minimal generic primitive**

Add:

```go
func KillOwnedProcesses(executableBase, requiredMarker string, preferredPIDs []int) (int, error)
```

The implementation must validate each live command line with both:

```go
strings.EqualFold(filepath.Base(executable), executableBase)
strings.Contains(strings.ToLower(commandLine), strings.ToLower(requiredMarker))
```

Enumerate matching process names after preferred ledger PIDs, deduplicate PIDs, and call the existing process-tree terminator only after validation.

- [ ] **Step 4: Verify GREEN**

```powershell
rtk go test ./internal/video -run 'OwnedProcess|KillOwned' -count=1 -v
```

- [ ] **Step 5: Commit**

```powershell
git add internal/video/process_registry.go internal/video/process_registry_test.go
git commit -m "feat: safely clean owned helper processes"
```

### Task 2: Track and clean MediaMTX process IDs

**Files:**
- Create: `internal/obsrtmp/mediamtx_process_registry.go`
- Create: `internal/obsrtmp/mediamtx_process_registry_test.go`
- Modify: `internal/obsrtmp/mediamtx.go`

- [ ] **Step 1: Write failing PID-ledger tests**

Cover atomic save/load, duplicate PID removal, malformed ledger recovery, PID reuse protection through live ownership validation, registration after start, and removal after process exit.

- [ ] **Step 2: Verify RED**

```powershell
rtk go test ./internal/obsrtmp -run 'MediaMTXProcess|StaleMediaMTX' -count=1 -v
```

Expected: build failure because the MediaMTX process registry does not exist.

- [ ] **Step 3: Implement the ledger and cleanup facade**

Store JSON at `settings.Dir()/mediamtx-processes.json` via write-to-temp plus rename. Add:

```go
func CleanupStaleMediaMTX() (int, error)
func registerMediaMTXProcess(pid int) error
func unregisterMediaMTXProcess(pid int) error
```

`CleanupStaleMediaMTX` passes ledger PIDs to `video.KillOwnedProcesses("mediamtx.exe", "imagepad-mediamtx-", pids)` on Windows (`mediamtx` elsewhere), then removes the ledger. A ledger PID never bypasses live command-line ownership validation.

Extend `managedProcess` with `pid() int`. Register immediately after a real MediaMTX start and unregister in the process wait goroutine on every exit path.

- [ ] **Step 4: Verify GREEN**

```powershell
rtk go test ./internal/obsrtmp -run 'MediaMTXProcess|StaleMediaMTX' -count=1 -v
```

- [ ] **Step 5: Commit**

```powershell
git add internal/obsrtmp/mediamtx.go internal/obsrtmp/mediamtx_process_registry.go internal/obsrtmp/mediamtx_process_registry_test.go
git commit -m "feat: track MediaMTX child processes"
```

### Task 3: Invoke cleanup during startup

**Files:**
- Modify: `internal/app/app.go`
- Create: `internal/app/app_test.go`
- Test: `internal/obsrtmp/mediamtx_process_registry_test.go`

- [ ] **Step 1: Add a failing startup-sequence seam test**

Extract the stale-helper cleanup sequence into a function whose dependencies can be injected. Assert FFmpeg tracked cleanup, FFmpeg port cleanup, and MediaMTX cleanup run in that order, and that one cleanup error does not suppress later cleanup calls.

- [ ] **Step 2: Verify RED**

```powershell
rtk go test ./internal/app ./internal/obsrtmp -run 'StartupCleanup|StaleMediaMTX' -count=1 -v
```

- [ ] **Step 3: Add MediaMTX startup cleanup**

Call `obsrtmp.CleanupStaleMediaMTX()` immediately after `video.KillFFmpegOnPort(1935)`. Log errors and stopped-process counts using the same wording pattern as FFmpeg cleanup. Do not abort startup on cleanup failure.

- [ ] **Step 4: Run verification**

```powershell
rtk go test ./internal/video ./internal/obsrtmp ./internal/app -count=1
rtk go test ./... -count=1
rtk git diff --check
```

- [ ] **Step 5: Commit only the cleanup changes**

```powershell
git add internal/app/app.go internal/app/app_test.go internal/video/process_registry.go internal/video/process_registry_test.go internal/obsrtmp/mediamtx.go internal/obsrtmp/mediamtx_process_registry.go internal/obsrtmp/mediamtx_process_registry_test.go
git commit -m "fix: clean stale MediaMTX on startup"
```

## Completion Audit

- Every kill is guarded by executable-name and ImagePadServer runtime-marker checks.
- PID reuse cannot authorize termination.
- Missing or malformed ledgers do not block process scanning.
- Cleanup errors remain non-fatal and visible in startup logs.
- Existing ordered MediaMTX shutdown remains unchanged.
- All tests pass from a fresh run.
