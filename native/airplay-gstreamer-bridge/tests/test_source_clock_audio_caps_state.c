#include "source_clock_audio_caps_state.h"

#include <assert.h>

int main(void) {
  SourceClockAudioCapsState state;
  source_clock_audio_caps_state_init(&state);

  assert(source_clock_audio_caps_state_should_update(&state, 16, 44100, 2));
  assert(!source_clock_audio_caps_state_should_update(&state, 16, 44100, 2));

  assert(source_clock_audio_caps_state_should_update(&state, 16, 48000, 2));
  assert(!source_clock_audio_caps_state_should_update(&state, 16, 48000, 2));

  assert(source_clock_audio_caps_state_should_update(&state, 18, 48000, 2));
  assert(source_clock_audio_caps_state_should_update(&state, 18, 48000, 1));
  assert(!source_clock_audio_caps_state_should_update(&state, 18, 48000, 1));
  return 0;
}
