# OBS ultra-low-latency feasibility for VRChat AVPro

## Goal

Determine which stream formats ImagePadServer can emit for OBS mode, and which of those are realistic for playback inside VRChat's AVPro video player with very low latency.

Target latency under discussion: 0.1-0.3 seconds end-to-end inside VRChat.

Hard requirement: the same approach must work on standalone VR headsets running VRChat for Android/Quest, not only PCVR.

## Current evidence

### VRChat video player constraints

VRChat worlds use either `VRCAVProVideoPlayer` or `VRCUnityVideoPlayer`. VRChat's creator documentation states that AVPro supports live streams and Unity Video Player does not support those live stream cases. URLs are loaded through VRChat's video player URL flow, not by arbitrary application code.

Important VRChat-side constraints:

- Non-allowlisted hosts require users to enable "Allow Untrusted URLs".
- Android/Quest requires HTTPS for video URLs outside the trusted flow.
- A user can handle a new video player URL only once every five seconds globally.
- AVPro playback must be tested in a built VRChat client; it does not play in the Unity editor.
- Quest/Android compatibility is not optional for ImagePadServer OBS mode.

Sources:

- https://creators.vrchat.com/worlds/udon/video-players/
- https://creators.vrchat.com/worlds/udon/video-players/www-whitelist/

### AVPro protocol support

AVPro's streaming support is platform-dependent.

Relevant Android support:

- AVPro's Android backend uses Android `MediaPlayer` and `media3-ExoPlayer`.
- AVPro lists HLS as supported on Android.
- AVPro lists MPEG-DASH as supported on Android via ExoPlayer.
- AVPro lists RTSP as supported on Android via ExoPlayer or MediaPlayer, but MediaPlayer is "not fully featured".
- AVPro lists RTMP as supported on Android via ExoPlayer only and notes known address resolution issues.
- Android streaming over HTTP on Android 9+ requires HTTPS or an app manifest cleartext exception. In VRChat we cannot assume a cleartext exception, and VRChat's own docs say Android video will not play without HTTPS.

Relevant ExoPlayer/Media3 support:

- HLS MPEG-TS: supported.
- HLS fMP4/CMAF: supported.
- Apple Low-Latency HLS: supported.
- Community LHLS: not supported.
- DASH: supported.
- RTSP: supported by ExoPlayer, but it requires the RTSP module in app code; we cannot assume VRChat includes or exposes this path beyond what AVPro uses.

Relevant Windows/PCVR support:

- HLS (`.m3u8`): supported on Windows 10 native APIs, or DirectShow with suitable filters.
- MPEG-DASH (`.mpd`): supported on Windows 10 native APIs, or DirectShow with suitable filters.
- RTSP: limited native support. AVPro documentation says real-time formats are not a strong focus and OS support is not consistently good.
- RTMP: Windows support is only via DirectShow with suitable third-party filters such as LAV Filters.

AVPro's Windows options expose:

- `videoApi`
- `useLowLatency`
- `useLowLiveLatency`
- `startWithHighestBitrate`

AVPro documentation specifically recommends WinRT for adaptive streams on Windows and shows `useLowLiveLatency = true` as WinRT-only for live streams.

Sources:

- https://www.renderheads.com/content/docs/AVProVideo/articles/feature-streaming.html
- https://www.renderheads.com/content/docs/AVProVideo-v3/api/RenderHeads.Media.AVProVideo.MediaPlayer.OptionsWindows.html
- https://www.renderheads.com/content/docs/AVProVideo-v3/articles/platform-android.html
- https://www.renderheads.com/content/docs/AVProVideo-v3/articles/supportedmedia.html
- https://developer.android.com/guide/topics/media/exoplayer/supported-formats

## Candidate formats

| Candidate | URL shape | AVPro Windows support | VRChat practicality | Latency feasibility |
| --- | --- | --- | --- | --- |
| Standard HLS TS | `https://host/stream/id/current-id.m3u8` | Supported | Already used by this app | Realistic: 1-10s depending on player buffer |
| Short-segment HLS TS | same as HLS, 0.2-0.5s segments | Supported in principle | Implemented as dev2 ultra mode | Not guaranteed; player may buffer multiple segments |
| HLS fMP4/CMAF | `.m3u8` with fMP4 segments | Android ExoPlayer supports HLS fMP4/CMAF | Strong Quest candidate | Needs implementation and direct testing |
| Apple LL-HLS/CMAF | `.m3u8` with partial segments | Android ExoPlayer supports Apple LL-HLS | Best Quest-compatible low-latency candidate | Possible only if VRChat's AVPro/ExoPlayer path honors LL-HLS parts |
| MPEG-DASH | `.mpd` | Android ExoPlayer supports DASH | Quest candidate, but not guaranteed in VRChat URL path | Could be competitive; direct testing required |
| RTSP | `rtsp://host/...` | Android AVPro lists support, but implementation path is less certain | Weak because VRChat Android requires HTTPS for video hosts and URL handling may reject non-HTTP schemes | Experimental only |
| RTMP | `rtmp://host/live/key` | Android AVPro lists ExoPlayer-only support with known address resolution issues | Weak for Quest and not HTTPS | Experimental only, not a main path |
| SRT | `srt://...` | Not listed as AVPro-supported media | Not viable for VRCAVProVideoPlayer | Not viable |
| WebRTC/WHEP | `https://.../whep` etc. | Not listed as AVPro-supported media | Not viable for VRCAVProVideoPlayer | Not viable in stock VRChat player |

## Feasibility assessment

### 0.1-0.3s guaranteed in VRChat

Not established. Based on current public AVPro and VRChat documentation, ImagePadServer cannot guarantee this target from the server side alone.

The blockers are:

- VRChat controls the embedded AVPro configuration and URL loading path.
- AVPro/OS player buffering is not controlled by ImagePadServer.
- The low-live-latency option exists in AVPro, but we need to verify whether VRChat exposes/enables it in the world/player configuration being used.
- WebRTC and SRT are the normal protocol choices for this latency range, but they are not documented as accepted by VRCAVProVideoPlayer.
- Android/Quest requires HTTPS for non-allowlisted video hosts, so any Quest-compatible live output must be served over HTTPS or through a trusted HTTPS tunnel/CDN.

### Most plausible path

The most plausible Quest-compatible route is not WebRTC/SRT/RTSP/RTMP. It is:

1. Confirm whether the VRChat world's AVPro component can enable low live latency / WinRT behavior.
2. Test HLS variants:
   - TS HLS with 1s segments.
   - TS HLS with 0.5s segments.
   - TS HLS with 0.2s segments.
   - CMAF/fMP4 HLS.
   - LL-HLS with partial segments if ffmpeg/server support is added.
3. Test MPEG-DASH as a separate Android/PCVR candidate.
4. Test RTSP/RTMP only as non-primary experiments; do not depend on them for Quest support.

Expected result: 0.5-2s may be possible in favorable AVPro settings; 0.1-0.3s should be treated as unproven until a Quest build proves it.

## Required validation

For each candidate, record:

- VRChat platform: PCVR or Quest.
- World video player prefab/component: ProTV, VideoTXL, USharpVideo, SDK sample, or custom.
- AVPro settings exposed by the prefab, especially stream mode and low latency settings.
- URL accepted or rejected.
- Time to first frame.
- Measured end-to-end latency using an on-screen clock in OBS.
- Stability for 5 minutes.
- Audio/video sync.
- Whether HTTPS was used.
- Whether "Allow Untrusted URLs" was enabled.

## Implementation implications

ImagePadServer can support a feasibility test matrix without committing to a false guarantee.

Recommended next development step:

- Add an "experimental outputs" section for OBS mode.
- Emit multiple test URLs for the same OBS input:
  - HLS TS normal.
  - HLS TS ultra.
  - HLS fMP4/CMAF.
  - Apple LL-HLS/CMAF.
  - DASH.
- Keep RTSP/RTMP out of the main Quest path unless a Quest build proves they work.
- Add a measurement overlay option or document an OBS clock test.

Do not label any mode as guaranteed 0.1-0.3s until the VRChat playback path is measured.

## Current Android/Quest conclusion

For standalone headset compatibility, the viable investigation path is HTTPS HLS first:

1. Standard HLS TS baseline.
2. Short-segment HLS TS.
3. HLS fMP4/CMAF.
4. Apple LL-HLS with partial segments.
5. DASH as a secondary candidate.

RTSP, RTMP, WebRTC, and SRT should not be treated as product paths for Quest unless direct VRChat Quest testing proves otherwise.
