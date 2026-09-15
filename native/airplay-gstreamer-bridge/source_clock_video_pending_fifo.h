#ifndef IMAGEPAD_SOURCE_CLOCK_VIDEO_PENDING_FIFO_H
#define IMAGEPAD_SOURCE_CLOCK_VIDEO_PENDING_FIFO_H

#include "source_clock_protocol.h"

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define SOURCE_CLOCK_VIDEO_PENDING_CAPACITY 64u
#define SOURCE_CLOCK_VIDEO_PENDING_MAX_BYTES (64u * 1024u * 1024u)
#define SOURCE_CLOCK_VIDEO_PENDING_MAX_AGE_NS 600000000ULL

typedef struct SourceClockPendingFrame {
  bool present;
  SourceClockHeader header;
  uint64_t arrival_ns;
  uint8_t *payload;
  size_t payload_bytes;
} SourceClockPendingFrame;

typedef enum SourceClockVideoPendingResult {
  SOURCE_CLOCK_VIDEO_PENDING_ACCEPTED = 0,
  SOURCE_CLOCK_VIDEO_PENDING_REJECTED = 1,
  SOURCE_CLOCK_VIDEO_PENDING_OOM = 2,
} SourceClockVideoPendingResult;

typedef struct SourceClockVideoPendingFifo {
  SourceClockPendingFrame frames[SOURCE_CLOCK_VIDEO_PENDING_CAPACITY];
  size_t head;
  size_t count;
  size_t bytes;
  bool waiting_for_idr;
} SourceClockVideoPendingFifo;

void source_clock_video_pending_frame_clear(SourceClockPendingFrame *frame);
void source_clock_video_pending_fifo_init(SourceClockVideoPendingFifo *fifo);
void source_clock_video_pending_fifo_clear(SourceClockVideoPendingFifo *fifo);
SourceClockVideoPendingResult source_clock_video_pending_fifo_enqueue(
    SourceClockVideoPendingFifo *fifo, const SourceClockHeader *header,
    const uint8_t *payload, size_t payload_bytes, uint64_t arrival_ns);
bool source_clock_video_pending_fifo_pop(SourceClockVideoPendingFifo *fifo,
                                         SourceClockPendingFrame *out);

#endif
