# Visualizer Title Scroll Transition Design

Date: 2026-06-20  
Status: Approved

## Goal

Keep visualizer metadata on one line, show the complete end of long text, and
reset scrolling text without an abrupt visible jump.

## Single-line contract

- The generated ASS script uses `WrapStyle: 2`.
- Every scrolling metadata event uses the explicit `\q2` no-wrap override.
- Title, artist, and album text never introduces an automatic line break.
- Text remains clipped to its assigned horizontal and vertical viewport.

## Scroll timing

For text wider than its viewport:

1. Hold at the initial position for exactly 3.0 seconds.
2. Keep the existing speed of 40 canonical pixels per second, scaled by output
   width.
3. Compute the normal overflow duration as `overflow / scaledSpeed`.
4. Add exactly 2.0 seconds to the movement duration.
5. Continue moving at the same speed during those extra 2.0 seconds. At 720p,
   the text therefore travels 80 pixels beyond the normal endpoint.
6. The complete text end must pass visibly inside the viewport before reset.

The cycle duration is `3.0 + overflow/scaledSpeed + 2.0` seconds.

## Fade transition

- Apply a 300 ms fade-out during the final 300 ms of the scrolling movement.
- Do not stop movement while fading out.
- Reset the text to its initial position only after it reaches full
  transparency.
- Apply a 300 ms fade-in during the first 300 ms of the next initial hold.
- Do not insert an empty event or additional blank interval between cycles.
- If a track ends during a phase, clamp the event to the media duration.

## Verification

- Unit-test the 2.0-second movement extension at 360p, 720p, and 1080p.
- Assert that extra travel equals `scaledSpeed * 2.0`.
- Assert that scrolling events contain `\fad(0,300)` and initial hold events
  contain `\fad(300,0)`.
- Assert event times remain contiguous at the reset boundary.
- Render the reported China Advice title through FFmpeg/libass and verify it
  remains one line throughout hold, movement, fade-out, reset, and fade-in.

