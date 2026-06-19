# Audio Visualizer Low-Trust Dispatch Contract

This contract applies to every DeepSeek V4 Flash worker and every reviewing agent.

## Worker prompt preamble

Construct the prompt from the concrete ticket row, ledger base commit, and ticket write allowlist. This AV-101 example shows the required wording; use the same structure with the target ticket's exact values and never dispatch unresolved tokens:

```text
You are implementing exactly one ImagePadServer ticket: AV-101 Parse ffprobe output and classify streams.
Base commit: use the exact AV-101 base commit recorded in ticket-status.md; stop if the worktree HEAD differs.
Worktree: use the exact absolute AV-101 worktree recorded in ticket-status.md; stop if the current directory differs.
Read first:
1. docs/superpowers/specs/2026-06-19-soundcloud-visualizer-design.md
2. docs/superpowers/plans/2026-06-19-audio-visualizer-implementation.md
3. the complete AV-101 section in 01-foundation-and-ingest-tickets.md

You may write only these paths:
- internal/video/media_probe.go
- internal/video/media_probe_test.go

Rules:
- Prefix every shell command with rtk.
- Do not edit files outside the allowlist.
- Do not change public contracts from audio_types.go.
- Do not update versions, README, release files, or unrelated formatting.
- Write the named failing test first and run it to capture RED.
- RED must be an assertion/compile failure caused by missing ticket behavior; tool absence, network failure, and missing fixtures are not valid RED.
- Implement only enough code to satisfy this ticket.
- Run the focused GREEN command and the package command.
- Run rtk git diff --check.
- Create exactly one logical commit.
- If any required function/type/path differs from the ticket, stop and report BLOCKED_CONTRACT_MISMATCH. Do not invent a substitute.
- Do not claim success without command output.

Return the required handoff block exactly.
```

## Worktree and branch rules

The active AI creates worktrees only after AV-000 and AV-001 are merged. Use the `superpowers:using-git-worktrees` skill at execution time.

- Branch names follow the concrete pattern shown by `codex/av-101-media-probe`; use the assigned ticket number and its existing title slug.
- Worktree directory: a sibling or configured worktree root, never nested inside this checkout.
- One worktree contains one active ticket.
- Two agents never share a worktree.
- A worker may read all repository files but may write only its allowlist.
- The active AI records the base commit in `ticket-status.md` before dispatch.

## Status model

Normal flow:

```text
DRAFT -> READY -> IN_PROGRESS -> REVIEW -> VERIFIED -> MERGED
```

Exceptional states:

- `WAITING_DEPENDENCY`: dependency is not merged.
- `WAITING_EXTERNAL`: requires network, toolchain, or runtime state not currently available.
- `BLOCKED_CONTRACT_MISMATCH`: plan names do not match merged code.
- `BLOCKED_TEST_ENVIRONMENT`: focused test cannot run for an environmental reason.
- `REJECTED`: reviewer found out-of-scope changes or invalid evidence.
- `SUPERSEDED`: replaced by a correction ticket.

Only the active AI edits status to `VERIFIED`, `MERGED`, `REJECTED`, or `SUPERSEDED` after reviewing evidence.

## Required handoff block

```text
Ticket:
Base commit:
Result status: REVIEW | BLOCKED_CONTRACT_MISMATCH | BLOCKED_TEST_ENVIRONMENT | WAITING_EXTERNAL
Commit:
Files changed:
Tests added:
RED command:
RED observed result:
GREEN commands:
GREEN observed results:
Runtime commands:
Runtime artifacts:
Spec acceptance criteria covered:
Known limitations:
Contract deviations:
```

Blank evidence fields are not allowed. For example, use `not applicable because AV-101 has no runtime component` when a field genuinely does not apply.

## Reviewer checklist

- [ ] Commit is based on the recorded base commit.
- [ ] Diff touches only the ticket allowlist.
- [ ] No generated binary, downloaded track, JPEG, temp file, or secret is committed.
- [ ] Test was observed RED before implementation.
- [ ] Test is GREEN after implementation.
- [ ] Package test is GREEN.
- [ ] `rtk git diff --check` is clean.
- [ ] No contract was renamed or duplicated.
- [ ] Error paths are asserted, not only happy paths.
- [ ] No version or documentation claim was changed outside its ticket.
- [ ] Runtime-facing behavior has runtime evidence when required.

## Merge protocol

1. Active AI reviews the worker commit and handoff.
2. Active AI reruns the focused GREEN command in the worker worktree.
3. Active AI marks `VERIFIED` only after rerun succeeds.
4. Active AI merges or cherry-picks in the master-plan merge order.
5. Active AI reruns the package gate after merge.
6. Active AI records the merged commit and observed output in the ledger.
7. If integration fails, revert or create a correction ticket. Do not silently patch another worker's ticket while leaving its status `VERIFIED`.

## Parallel dispatch limits

Dispatch together only when:

- dependencies are already merged;
- write allowlists are disjoint;
- neither ticket edits `publisher.go`, `server.go`, `ui.go`, `soundcloud.go`, or the shared contracts at the same time;
- neither ticket consumes temporary runtime ports or the same output directory.

AV-501 and AV-502 are always sequential. AV-700 is always single-owner.
