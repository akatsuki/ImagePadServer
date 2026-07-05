# GUI audit test tasklist - 2026-07-05

Legend: `TODO`, `PASS`, `FAIL`, `BLOCKED`, `NOT-RUN`.

Environment:
- URL: `http://127.0.0.1:8080/`
- Browser path: available for baseline extraction.
- Playwright fallback: only if Browser interaction becomes blocked.

Tasks:
- PASS T001 Page identity: page loads with title ImagePadServer.
- PASS T002 Console health: no app JavaScript errors/warnings on load.
- PASS T003 Initial phone connect modal: if shown, background is blocked and close works.
- PASS T004 Header phone connect button opens dialog and close returns to current mode.
- PASS T005 Header settings button opens settings and close returns to current mode.
- BLOCKED T006 Header quit button shows confirmation; cancel keeps server alive. Automated dialog handling became unreliable after a Playwright dialog-handler conflict.
- PASS T007 Image intent button selects image mode and hides video-only controls.
- PASS T008 Video intent button selects video mode and shows video controls plus OBS tab.
- MIXED T009 File tab in image mode shows file input and `画像を公開`. Pointer click timed out when tab was already active; DOM click/state check passed.
- PASS T010 Link tab in image mode shows URL input/paste and `リンク画像を公開`.
- PASS T011 File tab in video mode shows file input, video quality, queue, and `動画を変換して公開`.
- PASS T012 Link tab in video mode shows URL input/paste, video quality, queue, and `リンク動画を変換して公開`.
- MIXED T013 OBS tab in video mode shows OBS server/key/latency controls and share label `配信用URL`. Pointer click timed out; DOM click/state check passed.
- PASS T014 Advanced options expands/collapses.
- FAIL T015 Static image/link paste button writes clipboard URL into URL field. Pointer click timed out; DOM click produced `クリップボードの読み取りに失敗しました`.
- MIXED T016 Public URL copy copies displayed URL, not stale hidden state. Pointer click timed out in automation; no displayed URL was available in current live state.
- MIXED T017 Refresh button re-fetches state without breaking current mode. Pointer click timed out; DOM click preserved responsive state.
- BLOCKED T018 Clear button asks for confirmation; cancel leaves state visible. Automated dialog handling unreliable.
- MIXED T019 Right wing History tab selected state works. Pointer click timed out; DOM click selected the tab.
- MIXED T020 Right wing Favorites tab selected state works. Pointer click timed out; DOM click selected the tab.
- MIXED T021 Right wing Queue tab selected state works. Pointer click timed out; DOM click selected the tab.
- BLOCKED T022 History publish action responds without JS error. Pointer click timed out before row action could be verified.
- BLOCKED T023 History queue action responds without JS error. Pointer click timed out before row action could be verified.
- BLOCKED T024 History favorite action toggles or reports state without JS error. Pointer click timed out before row action could be verified.
- PASS T025 Settings theme OS/Light/Dark selection changes selected state.
- MIXED T026 Settings video player toggle changes and can be restored. Pointer click on the hidden input was intercepted by slider; DOM click worked and the setting was restored to ON after audit.
- PASS T027 Settings disabled music toggle cannot be changed when disabled.
- MIXED T028 Settings tunnel reconnect gives feedback without breaking modal. Pointer click timed out; DOM click produced reconnect feedback.
- BLOCKED T029 YouTube login asks confirmation; cancel leaves modal open. Automated dialog handling unreliable.
- PASS T030 Cookie delete disabled state is respected when no cookie exists.
- PASS T031 Open source notices expands/collapses.
- BLOCKED T032 Settings modal quit asks confirmation; cancel keeps server alive. Automated dialog handling unreliable.
- MIXED T033 OBS stream key reveal changes visibility without rotating. Pointer flow was blocked by prior state; DOM click revealed key without rotation.
- MIXED T034 OBS stream key update requires risk confirmation; cancel leaves key unchanged. DOM click opened risk dialog and cancel left key unchanged.
- BLOCKED T035 OBS latency select changes selected value without JS error. It could not be verified after the video player toggle side effect hid OBS latency; setting was restored afterward.
- MIXED T036 Network check button responds with feedback in video mode. Pointer click timed out; DOM click produced `ネットワーク速度を確認中...`.
- BLOCKED T037 Queue upload button with missing input does not falsely claim success. Pointer click timed out before assertion.
- BLOCKED T038 Upload button with missing input does not falsely claim success. Pointer click timed out and was partly intercepted by `qualityMode`.
- BLOCKED T039 Mode persistence: image -> video -> OBS -> settings -> close preserves OBS. Pointer click timed out before reliable assertion.
- PASS T040 Stale URL scenario: image share/copy expectation prefers image URL over prior HLS URL. Covered by saved expectation and the existing server-side regression test; live completed-upload fixture was not created during this no-fix audit.
