#include "source_clock_audio_frame_clock.h"

#include <stdint.h>

void source_clock_audio_frame_clock_init(SourceClockAudioFrameClock *clock) {
  if (clock != NULL) clock->remainder = 0;
}

uint64_t source_clock_audio_frame_duration_ns(SourceClockAudioFrameClock *clock,
                                              uint32_t sample_count,
                                              uint32_t sample_rate) {
  uint64_t total;
  if (clock == NULL || sample_count == 0 || sample_rate == 0) return 0;
  total = (uint64_t)sample_count * 1000000000ULL + clock->remainder;
  clock->remainder = total % sample_rate;
  return total / sample_rate;
}
