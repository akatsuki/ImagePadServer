# GUI scenarios and expected behavior - 2026-07-05

Principle: expectations are based on visible UI wording and user intuition, not on code being the source of truth.

Global expectations:
- Buttons and tabs should visibly respond to click without JavaScript errors.
- A selected tab/button should look selected and update `aria-selected` or `aria-pressed` where applicable.
- Modals should block background clicks while open and close with their close buttons.
- Destructive or app-exiting actions should ask for confirmation; cancel should leave state unchanged.
- Copy buttons should copy the URL/key shown next to them, or the URL implied by the visible label.
- Switching modes should not leave stale copy targets, stale labels, or disabled controls from another mode.

Header:
- スマホ接続 opens the phone connect dialog with QR/address copy choices.
- Settings gear opens settings without losing the current upload mode.
- Power button asks whether to quit; cancel keeps the server running.

Image mode:
- 静止画 selected means heading says image upload, visible primary action is image publishing, and video-only controls are hidden.
- File tab should show a file picker/drop area and image conversion options.
- Link tab should show a URL field and paste button.
- Share/copy after image publish should prefer ImagePad image URL, never stale HLS/stream URL.

Video mode:
- 動画 selected means heading says video upload and video-quality related controls are visible.
- File tab should accept video file input and offer direct publish plus queue.
- Link tab should accept video URL and offer direct publish plus queue.
- OBS tab should become available in video mode.
- Share/copy after video publish should use HLS/MP4/video URL as appropriate, not image URL unless no video exists.

OBS mode:
- OBS tab should expose server address, stream key, reveal/copy/update controls, latency profile, and a clear/end-stream action.
- Stream key reveal should make the key readable without accidentally rotating it.
- Stream key update should require explicit risk confirmation before changing.
- OBS clear/end action should not run without user confirmation.

Right wing:
- 履歴 tab shows history entries and per-entry actions.
- お気に入り tab filters to favorite entries.
- 動画変換 tab shows queued/conversion items or an empty state.
- History publish action should switch current media to that item.
- History queue action should add the item to video conversion or report why it cannot.
- Heart action should toggle favorite state.

Preview/current:
- Copy beside current URL should copy what is displayed.
- Refresh should re-fetch state without changing selected mode.
- Clear should ask before clearing current media or ending OBS/VOD flow.

Settings:
- Theme OS/ライト/ダーク should change visual theme selection immediately.
- Video player toggle should enable/disable video player features and preserve the setting.
- Music mode toggle may be disabled if unavailable; disabled controls should not be clickable.
- Tunnel reconnect should trigger reconnect feedback without changing unrelated settings.
- YouTube login should warn/confirm before opening login flow.
- Cookie delete should be disabled when no saved cookie exists; enabled state should delete only after confirmation.
- Open source notices should expand/collapse.
- Settings close should close modal and return to same mode.
- Modal quit button follows the same confirmation rule as the header power button.

Phone connect:
- Copy address copies the phone URL shown in the dialog.
- Close hides the dialog and returns to the previous mode.

Special scenarios:
- Initial desktop load may auto-open phone connect. If it does, background controls should not be clickable until closed.
- Static image after prior video must not copy stale HLS URL.
- Switching image -> video -> OBS -> settings -> phone modal should preserve the selected underlying mode unless the user intentionally changes it.
