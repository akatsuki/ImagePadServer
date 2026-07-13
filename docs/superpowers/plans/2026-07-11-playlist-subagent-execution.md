# Playlist Subagent Execution Plan

> **Controller contract:** The main session owns sequencing, integration, status, and final acceptance. Subagents work only in isolated forks/worktrees, never directly in the user's dirty checkout.

**Source plans:**
- `docs/superpowers/plans/2026-07-11-playlist-streaming-remediation.md`
- `docs/superpowers/plans/2026-07-11-playlist-hybrid-overlay.md`
- GitHub parent issue `#12`, execution issues `#13` through `#20`

## Model Roles

- **Sol:** Architecture-sensitive implementation, timestamp/codec reasoning, runtime acceptance design, integration and final reviews.
- **Terra:** Bounded multi-file Go implementation with explicit interfaces and focused tests.
- **Luna:** Read-only inventory, fixture preparation, test command execution, documentation/status updates, and other mechanical work that does not decide runtime behavior.
- Luna must not independently change RTSP publishing, FFmpeg argument construction, timestamp logic, media ownership, retry policy, or compatibility decisions.
- Any agent reports `DONE`, `DONE_WITH_CONCERNS`, `NEEDS_CONTEXT`, or `BLOCKED`. Concerns affecting correctness are resolved before review.

## Isolation and Review Contract

1. Record the integration base commit before each implementation task.
2. Give one implementation agent exclusive ownership of the task's write set in an isolated fork/worktree.
3. Require TDD evidence: focused test fails for the intended reason, implementation passes it, then package tests pass.
4. Generate a complete base-to-head diff; do not review only the last commit.
5. A different agent reviews specification compliance and code quality.
6. Critical or Important findings return to the implementer and are reviewed again.
7. The main session integrates only an approved task and reruns focused tests in the integration workspace.
8. Close the GitHub child issue only after integration verification. Update parent `#12` immediately.
9. Never run two implementation agents against overlapping files. Read-only audits and baseline tests may run in parallel.
10. Preserve the existing RTSP copy path until Issue `#19` passes its acceptance gate; each RTSP-affecting task receives Sol review.

## Assignment Matrix

| Order | Issue | Scope | Implementer | Reviewer | Gate |
|---|---:|---|---|---|---|
| 0 | #12 | Plan consistency and integration risk audit | Sol (read-only) | Main session | Before first integration |
| 1 | #13 | Saved-media ownership and materialization | Terra | Sol | None |
| 2 | #15 | Publisher/fallback bounded failure state and track-generation completion | Terra | Sol | Integrate after #13 |
| 3 | #14 | RadioRenderRecipe, save-time manifest and canonical resolution, gated from legacy copy | Sol | Fresh Sol reviewer | Requires #13 and #15 completion signal |
| 4 | #17A | Immutable active-session snapshot | Terra | Sol | Requires #14 interfaces; precedes #16 |
| 5 | #16 | Protocol-aware RTSP/HLS readiness and URL publication | Terra | Sol | Requires #15 and #17A |
| 6 | #17B | Desired versus active API/UI | Terra | Sol | Requires #16 |
| 7 | #18 | Sequential playback cursor | Terra | Sol | Integrate after #17B to avoid playlist/server overlap |
| 8 | #20A | RTSP reader and current copy-lane baseline | Sol | Fresh Sol reviewer | Requires #18; precedes #19 |
| 9 | #19 | Persistent ProgramClock, compositor, encoder and overlays | Sol | Fresh Sol reviewer | Requires #13 through #18 and #20A |
| 10 | #20B | Five-profile RTSP/HLS acceptance and soak gate | Sol | Fresh Sol reviewer | Requires #19 |

## Luna Sidecar Tasks

- Before #15: inventory existing publisher/fallback seams and run short focused baseline tests.
- Before #18: inventory cursor/currentID behavior and enumerate reorder/remove/loop/shuffle tests.
- Before #14: prepare manifest fixture inventory and a table of receiver-visible versus volatile FFmpeg options; no production-code changes.
- Before #16: enumerate all five profiles and expected readiness artifacts/status transitions.
- Before #20: prepare the smoke-test checklist, environment-variable matrix, and result-recording template.
- After each integration: run the exact focused verification command and report output only. Sol or Terra diagnoses any failure.

## Promotion Rules

- Promote a Terra task to Sol when it reveals timestamp arithmetic, codec/muxer ambiguity, lock-order or goroutine-lifecycle risk, or a required interface change spanning three or more ownership areas.
- Promote a Luna task to Terra when it requires production-code edits or behavioral judgment.
- Sol remains mandatory for implementation of #14, #19, and #20 and for review of every RTSP-affecting diff.
- A blocked agent is not retried unchanged: provide missing context, narrow the task, or promote the model.

## Branch and Ownership Map

- `codex/playlist-13-media-ownership`: `internal/playlist/*`, minimum required playlist server handlers/tests.
- `codex/playlist-15-radio-errors`: `internal/obsrtmp/radio*`, minimum status propagation handlers/tests.
- `codex/playlist-18-cursor`: queue cursor and playlist handler/tests only.
- `codex/playlist-14-asset-contract`: `internal/video` recipe/render, playlist manifest/store, canonical settings/API/tests.
- `codex/playlist-16-readiness`: MediaMTX readiness, radio status cache, URL publication/UI/tests.
- `codex/playlist-17-active-contract`: settings snapshot, restart-required state, settings/playlist UI/tests.
- `codex/playlist-19-program-clock`: persistent radio program pipeline and overlay boundaries/tests.
- `codex/playlist-20-acceptance`: runtime acceptance tests and smoke documentation.

If an implementation discovers that it must edit another task's owned files, it stops with `NEEDS_CONTEXT`; the main session adjusts ownership before work continues.

## Current Dispatch

- #13 completed: Terra implementation, four Sol review rounds, final spec/quality approval, integration, and package tests. GitHub #13 is closed.
- #15 completed: Terra implementation, Sol review/fix loop, final approval, integration, and package tests. GitHub #15 is closed.
- Sol: #14 implementation from tracked integration snapshot `25dcba9f66676173eb68586a06611b2fd44c8582`, split into recipe/settings and manifest/store commits.
- Luna sidecars completed for #14 contract inventory, #16 readiness inventory, and #20A RTSP baseline design. No E2E, tool download, or production-code edit was performed.

No later implementation task starts until #14 completes a fresh Sol review and integration gate.

## Final Gate

- All focused and package tests pass in the integration workspace.
- Sol performs a whole-branch review against both source plans and Issues #12-#20.
- Real RTSP/TCP consumption remains connected through fallback, track, pause, resume, and next-track transitions.
- All five delivery profiles pass their protocol-specific readiness and media checks.
- A 30-minute soak records no reconnect, non-monotonic DTS/PTS, codec-parameter change, post-readiness 404/502, or audio/video disappearance.
