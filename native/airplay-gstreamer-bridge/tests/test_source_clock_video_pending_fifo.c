#include "source_clock_video_pending_fifo.h"

#include <assert.h>
#include <string.h>

static SourceClockHeader header(uint16_t flags) {
  SourceClockHeader result = {0};
  result.stream_kind = SOURCE_CLOCK_STREAM_VIDEO;
  result.codec = SOURCE_CLOCK_CODEC_H264_ANNEXB_AU;
  result.flags = flags;
  return result;
}

static void test_order_and_bounds(void) {
  SourceClockVideoPendingFifo fifo;
  SourceClockPendingFrame out = {0};
  uint8_t payload[3] = {1, 2, 3};
  source_clock_video_pending_fifo_init(&fifo);
  assert(source_clock_video_pending_fifo_enqueue(
             &fifo, &header(SOURCE_CLOCK_FLAG_KEYFRAME), payload, sizeof(payload), 100) ==
         SOURCE_CLOCK_VIDEO_PENDING_ACCEPTED);
  for (size_t i = 1; i < SOURCE_CLOCK_VIDEO_PENDING_CAPACITY; ++i)
    assert(source_clock_video_pending_fifo_enqueue(&fifo, &header(0), payload, sizeof(payload),
                                                  100 + i) == SOURCE_CLOCK_VIDEO_PENDING_ACCEPTED);
  assert(fifo.count == SOURCE_CLOCK_VIDEO_PENDING_CAPACITY);
  assert(source_clock_video_pending_fifo_enqueue(&fifo, &header(0), payload, sizeof(payload), 200) ==
         SOURCE_CLOCK_VIDEO_PENDING_REJECTED);
  assert(fifo.count == 0 && fifo.bytes == 0 && fifo.waiting_for_idr);
  assert(source_clock_video_pending_fifo_pop(&fifo, &out) == false);
  source_clock_video_pending_fifo_clear(&fifo);
}

static void test_age_and_recovery_idr(void) {
  SourceClockVideoPendingFifo fifo;
  SourceClockPendingFrame out = {0};
  uint8_t payload[2] = {4, 5};
  source_clock_video_pending_fifo_init(&fifo);
  assert(source_clock_video_pending_fifo_enqueue(
             &fifo, &header(SOURCE_CLOCK_FLAG_KEYFRAME), payload, sizeof(payload), 100) ==
         SOURCE_CLOCK_VIDEO_PENDING_ACCEPTED);
  assert(source_clock_video_pending_fifo_enqueue(&fifo, &header(0), payload, sizeof(payload),
                                                 100 + SOURCE_CLOCK_VIDEO_PENDING_MAX_AGE_NS + 1) ==
         SOURCE_CLOCK_VIDEO_PENDING_REJECTED);
  assert(fifo.waiting_for_idr && fifo.count == 0);
  assert(source_clock_video_pending_fifo_enqueue(
             &fifo, &header(SOURCE_CLOCK_FLAG_KEYFRAME), payload, sizeof(payload), 800000000) ==
         SOURCE_CLOCK_VIDEO_PENDING_ACCEPTED);
  assert(source_clock_video_pending_fifo_pop(&fifo, &out));
  assert(out.present && out.payload_bytes == sizeof(payload));
  source_clock_video_pending_frame_clear(&out);
  source_clock_video_pending_fifo_clear(&fifo);
}

int main(void) {
  test_order_and_bounds();
  test_age_and_recovery_idr();
  return 0;
}
