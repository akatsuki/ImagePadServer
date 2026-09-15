#ifndef IMAGEPAD_SOURCE_CLOCK_AUDIO_FRAME_CLOCK_H
#define IMAGEPAD_SOURCE_CLOCK_AUDIO_FRAME_CLOCK_H

#include <stdint.h>

typedef struct SourceClockAudioFrameClock {
  uint64_t remainder;
} SourceClockAudioFrameClock;

void source_clock_audio_frame_clock_init(SourceClockAudioFrameClock *clock);
uint64_t source_clock_audio_frame_duration_ns(SourceClockAudioFrameClock *clock,
                                              uint32_t sample_count,
                                              uint32_t sample_rate);

#endif
