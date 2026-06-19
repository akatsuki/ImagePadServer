# Audio Visualizer Ticket Status

The active AI owns this ledger. Workers report evidence; they do not edit status rows.

| Ticket | Status | Dependencies | Base commit | Owner/worktree | Review commit | Evidence summary |
| --- | --- | --- | --- | --- | --- | --- |
| AV-000 | MERGED | none | c35cc35 | active AI | c35cc35 | prototype checkpointed |
| AV-001 | MERGED | AV-000 | c35cc35 | active AI | dc7f807 | shared contracts defined |
| AV-100 | READY | AV-001 | dc7f807 | - | - | ffprobe toolchain |
| AV-101 | WAITING_DEPENDENCY | AV-001, AV-100 | - | - | - | media probe |
| AV-102 | READY | AV-001 | dc7f807 | - | - | metadata normalization |
| AV-103 | READY | AV-001 | dc7f807 | - | - | media limit |
| AV-104 | READY | AV-001 | dc7f807 | - | - | Noto fonts |
| AV-201 | WAITING_DEPENDENCY | AV-101, AV-102 | - | - | - | embedded artwork |
| AV-202 | WAITING_DEPENDENCY | AV-100, AV-101, AV-102, AV-103 | - | - | - | SoundCloud acquisition |
| AV-203 | WAITING_DEPENDENCY | AV-101, AV-103 | - | - | - | direct remote media |
| AV-204 | WAITING_DEPENDENCY | AV-100, AV-101 | - | - | - | audio analysis |
| AV-205 | WAITING_DEPENDENCY | AV-101, AV-103 | - | - | - | local classification |
| AV-301 | WAITING_DEPENDENCY | AV-104, AV-204 | - | - | - | fallback artwork |
| AV-302 | WAITING_DEPENDENCY | AV-100, AV-102, AV-104 | - | - | - | layout and ASS text |
| AV-303 | WAITING_DEPENDENCY | AV-201, AV-301 | - | - | - | artwork/background |
| AV-401 | WAITING_DEPENDENCY | AV-204, AV-302, AV-303 | - | - | - | HLS renderer |
| AV-501 | WAITING_DEPENDENCY | AV-401 | - | - | - | publisher queue |
| AV-502 | WAITING_DEPENDENCY | AV-202, AV-203, AV-205, AV-501 | - | - | - | server/store integration |
| AV-601 | WAITING_DEPENDENCY | AV-502 | - | - | - | UI |
| AV-602 | WAITING_DEPENDENCY | AV-502 | - | - | - | runtime fixtures |
| AV-603 | WAITING_DEPENDENCY | AV-502, runtime claims require AV-602 | - | - | - | README |
| AV-700 | WAITING_DEPENDENCY | AV-601, AV-602, AV-603 | - | - | - | final QA |
| AV-710 | WAITING_DEPENDENCY | AV-700 | - | - | - | versioned Windows build |

## Status update record

Append one block per transition:

```text
Timestamp:
Ticket:
Old status:
New status:
Base commit:
Worker/worktree:
Commit:
Commands rerun by active AI:
Observed result:
Evidence paths:
Reason:
```

Do not rewrite or delete older transition blocks.

```text
Timestamp: 2026-06-19
Ticket: AV-000
Old status: READY
New status: MERGED
Base commit: c35cc35
Worker/worktree: active AI (main)
Commit: c35cc35
Commands rerun by active AI: rtk git diff --check, rtk go test ./... -count=1
Observed result: git diff --check clean; go test 104 passed in 20 packages
Evidence paths: main worktree
Reason: prototype checkpoint completed and verified
```

```text
Timestamp: 2026-06-19
Ticket: AV-001
Old status: WAITING_DEPENDENCY
New status: MERGED
Base commit: c35cc35
Worker/worktree: active AI (main)
Commit: dc7f807
Commands rerun by active AI: rtk go test ./internal/video -run '^TestAudioContracts$' -count=1 -v
Observed result: RED (compile error) then GREEN (1 passed)
Evidence paths: main worktree
Reason: shared contracts defined and frozen
```
