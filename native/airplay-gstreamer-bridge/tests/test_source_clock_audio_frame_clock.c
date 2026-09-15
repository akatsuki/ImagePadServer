#include "source_clock_audio_frame_clock.h"

#include <assert.h>
#include <stdint.h>

int main(void) {
  SourceClockAudioFrameClock clock;
  uint64_t total = 0;
  source_clock_audio_frame_clock_init(&clock);
  for (int i = 0; i < 44100 / 1024; ++i) {
    total += source_clock_audio_frame_duration_ns(&clock, 1024, 44100);
  }
  assert(total == 44032ULL * 1000000000ULL / 44100ULL);
  assert(clock.remainder < 44100);
  assert(source_clock_audio_frame_duration_ns(&clock, 0, 44100) == 0);
  assert(source_clock_audio_frame_duration_ns(&clock, 1024, 0) == 0);
  return 0;
}
