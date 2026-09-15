#ifndef AIRPLAY_SOURCE_CLOCK_AUDIO_CAPS_STATE_H
#define AIRPLAY_SOURCE_CLOCK_AUDIO_CAPS_STATE_H

#include <stdbool.h>
#include <stdint.h>

typedef struct {
  bool configured;
  uint8_t codec;
  uint32_t sample_rate;
  uint16_t channels;
} SourceClockAudioCapsState;

void source_clock_audio_caps_state_init(SourceClockAudioCapsState *state);

bool source_clock_audio_caps_state_should_update(SourceClockAudioCapsState *state,
                                                 uint8_t codec,
                                                 uint32_t sample_rate,
                                                 uint16_t channels);

#endif
