# Audio Visualizer Ticket Status

The active AI owns this ledger. Workers report evidence; they do not edit status rows.

| Ticket | Status | Dependencies | Base commit | Owner/worktree | Review commit | Evidence summary |
| --- | --- | --- | --- | --- | --- | --- |
| AV-000 | READY | none | current worktree | active AI | - | checkpoint dirty prototype |
| AV-001 | WAITING_DEPENDENCY | AV-000 | - | - | - | shared contracts |
| AV-100 | WAITING_DEPENDENCY | AV-001 | - | - | - | ffprobe toolchain |
| AV-101 | WAITING_DEPENDENCY | AV-001, AV-100 | - | - | - | media probe |
| AV-102 | WAITING_DEPENDENCY | AV-001 | - | - | - | metadata normalization |
| AV-103 | WAITING_DEPENDENCY | AV-001 | - | - | - | media limit |
| AV-104 | WAITING_DEPENDENCY | AV-001 | - | - | - | Noto fonts |
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
