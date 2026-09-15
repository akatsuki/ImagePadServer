#include "video_scheduler.h"

#include <stdio.h>

#define CHECK(condition)                                                        \
  do {                                                                          \
    if (!(condition)) {                                                         \
      fprintf(stderr, "video scheduler check failed at line %d: %s\n",       \
              __LINE__, #condition);                                            \
      return 1;                                                                 \
    }                                                                           \
  } while (0)

int main(void) {
  SourceClockVideoScheduler scheduler;
  SourceClockVideoTick tick;
  uint64_t cadence = 16666666;
  size_t i;
  source_clock_video_scheduler_init(&scheduler, cadence);
  CHECK(source_clock_video_scheduler_tick(&scheduler, 0).kind == SOURCE_CLOCK_VIDEO_TICK_BLACK);
  source_clock_video_scheduler_offer(&scheduler, (SourceClockVideoFrame){1, 100});
  source_clock_video_scheduler_offer(&scheduler, (SourceClockVideoFrame){2, 120});
  tick = source_clock_video_scheduler_tick(&scheduler, 116);
  CHECK(tick.kind == SOURCE_CLOCK_VIDEO_TICK_FRAME && tick.frame.id == 1);
  tick = source_clock_video_scheduler_tick(&scheduler, 133);
  CHECK(tick.kind == SOURCE_CLOCK_VIDEO_TICK_FRAME && tick.frame.id == 2);
  tick = source_clock_video_scheduler_tick(&scheduler, 150);
  CHECK(tick.kind == SOURCE_CLOCK_VIDEO_TICK_HOLD && tick.frame.id == 2);
  source_clock_video_scheduler_init(&scheduler, cadence);
  for (i = 0; i <= SOURCE_CLOCK_VIDEO_SCHEDULER_CAPACITY; ++i) {
    source_clock_video_scheduler_offer(
        &scheduler, (SourceClockVideoFrame){i + 1, 101 + i, NULL});
  }
  CHECK(source_clock_video_scheduler_depth(&scheduler) == SOURCE_CLOCK_VIDEO_SCHEDULER_CAPACITY);
  CHECK(source_clock_video_scheduler_tick(&scheduler, 101).kind ==
        SOURCE_CLOCK_VIDEO_TICK_BLACK);

  /* A real AirPlay session can map video several hundred milliseconds ahead
   * of pipeline running time.  At 60fps the scheduler must retain the first
   * due frame instead of continuously evicting it before its presentation
   * timestamp arrives. */
  source_clock_video_scheduler_init(&scheduler, cadence);
  for (i = 0; i < 25; ++i) {
    uint64_t now = i * cadence;
    source_clock_video_scheduler_offer(
        &scheduler,
        (SourceClockVideoFrame){i + 1, 400000000ULL + now, NULL});
    CHECK(source_clock_video_scheduler_tick(&scheduler, now).kind ==
          SOURCE_CLOCK_VIDEO_TICK_BLACK);
  }
  tick = source_clock_video_scheduler_tick(&scheduler, 400000000ULL);
  CHECK(tick.kind == SOURCE_CLOCK_VIDEO_TICK_FRAME && tick.frame.id == 1);
  return 0;
}
