#include "source_clock_video_pending_fifo.h"

#include <glib.h>

#include <string.h>

void source_clock_video_pending_frame_clear(SourceClockPendingFrame *frame) {
  if (frame == NULL) return;
  g_free(frame->payload);
  memset(frame, 0, sizeof(*frame));
}

void source_clock_video_pending_fifo_init(SourceClockVideoPendingFifo *fifo) {
  if (fifo != NULL) memset(fifo, 0, sizeof(*fifo));
}

void source_clock_video_pending_fifo_clear(SourceClockVideoPendingFifo *fifo) {
  if (fifo == NULL) return;
  for (size_t i = 0; i < SOURCE_CLOCK_VIDEO_PENDING_CAPACITY; ++i)
    source_clock_video_pending_frame_clear(&fifo->frames[i]);
  fifo->head = 0;
  fifo->count = 0;
  fifo->bytes = 0;
  fifo->waiting_for_idr = false;
}

static void reject_and_resync(SourceClockVideoPendingFifo *fifo) {
  source_clock_video_pending_fifo_clear(fifo);
  fifo->waiting_for_idr = true;
}

SourceClockVideoPendingResult source_clock_video_pending_fifo_enqueue(
    SourceClockVideoPendingFifo *fifo, const SourceClockHeader *header,
    const uint8_t *payload, size_t payload_bytes, uint64_t arrival_ns) {
  size_t tail;
  uint8_t *copy;
  bool keyframe;
  if (fifo == NULL || header == NULL || payload == NULL || payload_bytes == 0)
    return SOURCE_CLOCK_VIDEO_PENDING_REJECTED;
  keyframe = (header->flags & SOURCE_CLOCK_FLAG_KEYFRAME) != 0;
  if (fifo->waiting_for_idr && !keyframe) return SOURCE_CLOCK_VIDEO_PENDING_REJECTED;
  if (fifo->count != 0) {
    const SourceClockPendingFrame *oldest = &fifo->frames[fifo->head];
    if (arrival_ns < oldest->arrival_ns ||
        arrival_ns - oldest->arrival_ns > SOURCE_CLOCK_VIDEO_PENDING_MAX_AGE_NS) {
      reject_and_resync(fifo);
      if (!keyframe) return SOURCE_CLOCK_VIDEO_PENDING_REJECTED;
    }
  }
  if (payload_bytes > SOURCE_CLOCK_VIDEO_PENDING_MAX_BYTES ||
      fifo->count >= SOURCE_CLOCK_VIDEO_PENDING_CAPACITY ||
      fifo->bytes > SOURCE_CLOCK_VIDEO_PENDING_MAX_BYTES - payload_bytes) {
    reject_and_resync(fifo);
    if (!keyframe || payload_bytes > SOURCE_CLOCK_VIDEO_PENDING_MAX_BYTES)
      return SOURCE_CLOCK_VIDEO_PENDING_REJECTED;
  }
  copy = (uint8_t *)g_malloc(payload_bytes);
  if (copy == NULL) return SOURCE_CLOCK_VIDEO_PENDING_OOM;
  memcpy(copy, payload, payload_bytes);
  tail = (fifo->head + fifo->count) % SOURCE_CLOCK_VIDEO_PENDING_CAPACITY;
  source_clock_video_pending_frame_clear(&fifo->frames[tail]);
  fifo->frames[tail].present = true;
  fifo->frames[tail].header = *header;
  fifo->frames[tail].arrival_ns = arrival_ns;
  fifo->frames[tail].payload = copy;
  fifo->frames[tail].payload_bytes = payload_bytes;
  fifo->count++;
  fifo->bytes += payload_bytes;
  fifo->waiting_for_idr = false;
  return SOURCE_CLOCK_VIDEO_PENDING_ACCEPTED;
}

bool source_clock_video_pending_fifo_pop(SourceClockVideoPendingFifo *fifo,
                                         SourceClockPendingFrame *out) {
  SourceClockPendingFrame *frame;
  if (fifo == NULL || out == NULL || fifo->count == 0) return false;
  frame = &fifo->frames[fifo->head];
  *out = *frame;
  memset(frame, 0, sizeof(*frame));
  fifo->head = (fifo->head + 1) % SOURCE_CLOCK_VIDEO_PENDING_CAPACITY;
  fifo->count--;
  fifo->bytes -= out->payload_bytes;
  if (fifo->count == 0) fifo->head = 0;
  return true;
}
