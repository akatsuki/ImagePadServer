#include "video_scheduler.h"

#include <string.h>

void source_clock_video_scheduler_init(SourceClockVideoScheduler *scheduler, uint64_t cadence_ns) {
  if (scheduler == NULL) return;
  memset(scheduler, 0, sizeof(*scheduler));
  scheduler->cadence_ns = cadence_ns;
}

bool source_clock_video_scheduler_offer(SourceClockVideoScheduler *scheduler,
                                        SourceClockVideoFrame frame) {
  if (scheduler == NULL) return false;
  if (scheduler->queue_size == SOURCE_CLOCK_VIDEO_SCHEDULER_CAPACITY) {
    memmove(&scheduler->queue[0], &scheduler->queue[1],
            (SOURCE_CLOCK_VIDEO_SCHEDULER_CAPACITY - 1) * sizeof(scheduler->queue[0]));
    scheduler->queue_size--;
  }
  scheduler->queue[scheduler->queue_size++] = frame;
  return true;
}

SourceClockVideoTick source_clock_video_scheduler_tick(SourceClockVideoScheduler *scheduler,
                                                       uint64_t tick_ns) {
  SourceClockVideoTick result;
  size_t selected = SIZE_MAX;
  memset(&result, 0, sizeof(result));
  if (scheduler == NULL) return result;
  for (size_t i = 0; i < scheduler->queue_size; ++i) {
    if (scheduler->queue[i].pts_ns <= tick_ns) selected = i;
  }
  if (selected != SIZE_MAX) {
    scheduler->last = scheduler->queue[selected];
    scheduler->has_last = true;
    memmove(&scheduler->queue[0], &scheduler->queue[selected + 1],
            (scheduler->queue_size - selected - 1) * sizeof(scheduler->queue[0]));
    scheduler->queue_size -= selected + 1;
    result.kind = SOURCE_CLOCK_VIDEO_TICK_FRAME;
    result.frame = scheduler->last;
    return result;
  }
  if (scheduler->has_last) {
    result.kind = SOURCE_CLOCK_VIDEO_TICK_HOLD;
    result.frame = scheduler->last;
  } else {
    result.kind = SOURCE_CLOCK_VIDEO_TICK_BLACK;
  }
  return result;
}

size_t source_clock_video_scheduler_depth(const SourceClockVideoScheduler *scheduler) {
  return scheduler == NULL ? 0 : scheduler->queue_size;
}
