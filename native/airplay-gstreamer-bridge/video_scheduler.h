#ifndef IMAGEPAD_VIDEO_SCHEDULER_H
#define IMAGEPAD_VIDEO_SCHEDULER_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

/* One second at the maximum 60fps output cadence.  The shared source clock can
 * legitimately map video hundreds of milliseconds ahead of pipeline running
 * time; a three-frame queue evicted every frame before it became due. */
#define SOURCE_CLOCK_VIDEO_SCHEDULER_CAPACITY 64u

typedef struct SourceClockVideoFrame {
  uint64_t id;
  uint64_t pts_ns;
  void *opaque;
} SourceClockVideoFrame;

typedef enum SourceClockVideoTickKind {
  SOURCE_CLOCK_VIDEO_TICK_BLACK = 0,
  SOURCE_CLOCK_VIDEO_TICK_FRAME = 1,
  SOURCE_CLOCK_VIDEO_TICK_HOLD = 2,
} SourceClockVideoTickKind;

typedef struct SourceClockVideoTick {
  SourceClockVideoTickKind kind;
  SourceClockVideoFrame frame;
} SourceClockVideoTick;

typedef struct SourceClockVideoScheduler {
  SourceClockVideoFrame queue[SOURCE_CLOCK_VIDEO_SCHEDULER_CAPACITY];
  size_t queue_size;
  bool has_last;
  SourceClockVideoFrame last;
  uint64_t cadence_ns;
} SourceClockVideoScheduler;

void source_clock_video_scheduler_init(SourceClockVideoScheduler *scheduler, uint64_t cadence_ns);
bool source_clock_video_scheduler_offer(SourceClockVideoScheduler *scheduler,
                                        SourceClockVideoFrame frame);
SourceClockVideoTick source_clock_video_scheduler_tick(SourceClockVideoScheduler *scheduler,
                                                       uint64_t tick_ns);
size_t source_clock_video_scheduler_depth(const SourceClockVideoScheduler *scheduler);

#endif
