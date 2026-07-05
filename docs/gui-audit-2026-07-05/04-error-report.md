# GUI audit error report - 2026-07-05

This file is intentionally filled during test execution.

Rules:
- Do not fix findings during the audit.
- Record expected behavior, actual behavior, reproduction, evidence, likely risk, and possible collateral risk if fixed.

Findings:

## F001 - Automation and/or pointer-click completion failures across many visible controls

- Severity: P1 for release confidence; P2 if confirmed to be automation-only.
- Expected: visible buttons and tabs complete normal pointer click actions.
- Actual: Playwright pointer clicks timed out on multiple visible controls even when the locator resolved and the action reached `performing click action`.
- Affected tasks: T009, T013, T015, T016, T017, T019, T020, T021, T022, T023, T024, T028, T033, T036, T037, T038, T039.
- Evidence: DOM click diagnostics passed for several of the same controls: OBS tab, right-wing tabs, refresh, tunnel reconnect, network check. This suggests at least part of the failure may be driver/pointer completion or event-loop timing, not missing handlers.
- Reproduction: run pointer click automation against the visible controls after closing the initial phone dialog.
- Risk if fixed: changing z-index, overlays, pointer-events, or async click handlers could affect modal blocking, upload form submission, and copy buttons.
- Recommendation: before code changes, reproduce manually or with a fresh isolated browser profile plus low-level mouse coordinates to decide whether this is real user-visible click failure or automation-only.

Post-plan update:
- Revalidated with an isolated flow after waiting for and closing the auto phone-connect dialog. Header controls, mode controls, right-wing tabs, settings, refresh, phone-connect, confirmation dialogs, advanced network check, and OBS latency select completed as expected when tested with the correct visible preconditions.
- A real subcase remained: history row actions could detach during a periodic state refresh because `renderHistory` rebuilt the whole list even when visible history data was unchanged.
- Fix applied: history and queue rendering now skip DOM replacement when the render signature is unchanged.
- Verification: holding the original history action ElementHandle across a 2.5s refresh interval kept `isConnected === true`, and publish / queue / favorite clicks completed.

## F002 - Paste URL button fails clipboard read in browser automation

- Severity: P2.
- Expected: after clipboard contains `https://example.com/test.mp4`, clicking paste should fill the URL field.
- Actual: DOM click produced toast `クリップボードの読み取りに失敗しました`; pointer click also timed out.
- Affected tasks: T015.
- Reproduction: video link mode -> set browser clipboard -> click paste URL.
- Risk if fixed: clipboard permission handling differs between browsers and HTTPS/local contexts; changes may affect copy buttons and history copy fallback behavior.
- Recommendation: verify manually in the user browser. If manual also fails, add a fallback that gives clear instructions or uses the browser Clipboard API result path consistently.

Post-plan update:
- Revalidated with browser clipboard permissions explicitly granted. Paste filled `https://example.com/granted.mp4`.
- Without clipboard-read permission, the toast `クリップボードの読み取りに失敗しました` is the expected browser-permission failure path.
- No app code change made for F002.

## F003 - Confirmation-dialog tasks could not be completed in the automated run

- Severity: P2 for audit completeness.
- Expected: quit, clear, and YTDLP login show confirmations and cancel safely.
- Actual: Playwright dialog handling conflicted after a prior auto-dismiss handler, causing `dialog.dismiss: Cannot dismiss dialog which is already handled`.
- Affected tasks: T006, T018, T029, T032.
- Reproduction: automated run only; not proven as an app bug.
- Risk if fixed: no app fix should be made from this evidence alone.
- Recommendation: perform an isolated confirmation-only run or manual check before release.

Post-plan update:
- Revalidated in isolated runs. Header quit, settings quit, clear-current, and YTDLP login all opened the expected confirmation dialog and cancel/dismiss left the app usable.
- No app code change made for F003.

## F004 - Direct pointer click on video player checkbox target is intercepted by the slider element

- Severity: P3.
- Expected: visible switch surface toggles video player mode.
- Actual: clicking the hidden checkbox locator is intercepted by `.switch-slider`; DOM click toggled the setting and it was restored to ON after the audit.
- Affected tasks: T026.
- Reproduction: settings -> click direct input locator rather than visible slider/label.
- Risk if fixed: changing switch markup can affect all settings toggles.
- Recommendation: likely not user-visible if the visible slider is clickable; verify with manual click on the visible switch, not the hidden input.

## F005 - OBS latency verification became invalid after video player toggle side effect

- Severity: P3.
- Expected: OBS latency select visible in OBS mode and selectable.
- Actual: the audit temporarily toggled video player OFF, which hid OBS/video-player controls and invalidated T035. The setting was restored to ON afterward.
- Affected tasks: T035.
- Reproduction: disable video player -> attempt OBS latency interaction.
- Risk if fixed: no fix indicated; this is a test sequencing issue unless OBS latency remains hidden when video player is ON.
- Recommendation: rerun OBS latency test in a clean state with video player ON.

Post-plan update:
- Revalidated after opening the advanced options panel with video player ON. OBS latency mode select was visible and selectable.
- No app code change made for F005.
