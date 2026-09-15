#include "source_clock_audio_caps_state.h"

#include <string.h>

void source_clock_audio_caps_state_init(SourceClockAudioCapsState *state) {
  if (state == NULL) return;
  memset(state, 0, sizeof(*state));
}

bool source_clock_audio_caps_state_should_update(SourceClockAudioCapsState *state,
                                                 uint8_t codec,
                                                 uint32_t sample_rate,
                                                 uint16_t channels) {
  if (state == NULL) return false;
  if (state->configured && state->codec == codec &&
      state->sample_rate == sample_rate && state->channels == channels) {
    return false;
  }
  state->configured = true;
  state->codec = codec;
  state->sample_rate = sample_rate;
  state->channels = channels;
  return true;
}
