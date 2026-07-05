# GUI component inventory - 2026-07-05

Scope: ImagePadServer dashboard at `http://127.0.0.1:8080/`.

Method:
- Browser path was available and used for baseline page identity, DOM snapshot, console logs, and state extraction.
- Code-side inventory came from dashboard HTML and visible DOM structure.
- Visual/intuition expectations are recorded separately; this file is only the component list.

Baseline:
- URL: `http://127.0.0.1:8080/`
- Title: `ImagePadServer`
- Console errors/warnings at baseline: none
- Initial auto modal: phone connect dialog can be open on first desktop load.

Header controls:
- `phoneConnectButton` - スマホ接続
- `settingsButton` - settings gear icon
- `quitHeaderButton` - power/quit icon, negative color

Media intent controls:
- `imageIntentButton` - 静止画
- `videoIntentButton` - 動画
- `musicIntentButton` - ミュージック, hidden in current state
- `musicModeMenu` choices - シングル, プレイリスト, パーティーモード, hidden unless music is enabled/open

Upload source tabs:
- `fileModeButton` - ファイル
- `linkModeButton` - リンク
- `obsModeButton` - OBS, hidden in image mode and visible in video mode

File/link/OBS inputs:
- `imageInput` - file input for images/videos
- `imageURLInput` - URL input for image/video link
- `pasteURLButton` - paste URL from clipboard
- OBS Server copy button
- `obsKeyRevealButton` - reveal stream key
- Stream Key copy button
- `obsKeyRotateButton` - rotate/update stream key

Conversion options:
- advanced options `details` / `summary`
- max dimension select
- `formatSelect`
- `qualitySelect`
- max MB select
- `qualityMode`
- `networkCheckButton`
- `obsLatencyMode`

Publish controls:
- `uploadButton`
- `obsLatencyDetailButton`
- `queueUploadButton`
- upload progress panel
- mobile progress panel

Toast controls:
- `toastYTDLPLoginButton`
- `toastCopyButton`
- `toastCloseButton`

Right wing tabs and row actions:
- `historyTabButton`
- `favoritesTabButton`
- `queueTabButton`
- history row publish action
- history row queue/convert action
- history row favorite/unfavorite heart action

Preview/current controls:
- public/share URL copy button
- `refreshButton`
- `clearButton`
- video info panel
- music player controls: `musicPlayerPlayButton`, `musicPlayerStopButton`, `musicPlayerRepeatButton`, `musicPlayerShuffleButton`, hidden unless music mode/player is active

Dialogs and modal controls:
- Phone connect dialog: phone URL copy buttons, `phoneConnectCloseButton`
- Settings modal: theme buttons OS/ライト/ダーク, `videoPlayerToggle`, `musicModeToggle`, `tunnelReconnectButton`, phone URL copy buttons, `ytdlpLoginButton`, `ytdlpCookieDeleteButton`, Open source notices details, `quitButton`, `settingsCloseButton`
- RTSP risk dialog: `rtspRiskCancel`, `rtspRiskConfirm`
- OBS key risk dialog: `obsKeyEditInput`, `obsKeyRiskCancel`, `obsKeyRiskConfirm`
- Media candidate dialog: `mediaCandidateClose`
- OBS connections dialog: `obsConnectionsClose`

State observations:
- Image/file state shows file input, image transform options, and `画像を公開`.
- Image/link state shows URL input and paste button, and `リンク画像を公開`.
- Video/file state shows file input, video quality controls, queue button, OBS tab, and `動画を変換して公開`.
- Video/link state shows URL input, video quality controls, queue button, OBS tab, and `リンク動画を変換して公開`.
- OBS state shows OBS server/key controls, OBS latency select, share label `配信用URL`, clear title for ending/VOD flow, and disabled/waiting publish state.
- Settings modal overlays the current mode without changing that mode.
- Phone connect modal overlays the current mode without changing that mode.
