#ifndef IMAGEPAD_SOURCE_CLOCK_MEDIA_READY_H
#define IMAGEPAD_SOURCE_CLOCK_MEDIA_READY_H

#include "source_clock_events.h"

#include <stdbool.h>
#include <stdint.h>

typedef enum {
  SOURCE_CLOCK_MEDIA_READY_WRITE_FAILED = 0,
  SOURCE_CLOCK_MEDIA_READY_WRITTEN = 1,
} SourceClockMediaReadyWriteResult;

typedef bool (*SourceClockMediaReadyWriteFn)(
    SourceClockEventWriter *writer,
    const uint64_t *running_time_ns,
    const uint64_t *source_ntp_ns);

/* Called while the owner mutex is held, after the scheduler mutex is released. */
static inline bool source_clock_media_ready_reserve_after_offer(
    bool stopping, bool offer_succeeded, bool *reserved,
    uint64_t running_time_ns, uint64_t *reserved_running_time_ns) {
  if (reserved == NULL || reserved_running_time_ns == NULL || stopping ||
      !offer_succeeded || *reserved) {
    return false;
  }
  *reserved = true;
  *reserved_running_time_ns = running_time_ns;
  return true;
}

/* Called with both scheduler and owner mutexes released. */
static inline SourceClockMediaReadyWriteResult
source_clock_media_ready_write_reserved(
    SourceClockEventWriter *writer, uint64_t running_time_ns,
    SourceClockMediaReadyWriteFn write_event) {
  if (writer == NULL || write_event == NULL ||
      !write_event(writer, &running_time_ns, NULL)) {
    return SOURCE_CLOCK_MEDIA_READY_WRITE_FAILED;
  }
  return SOURCE_CLOCK_MEDIA_READY_WRITTEN;
}

/* Called while the owner mutex is held after file I/O completes. */
static inline void source_clock_media_ready_commit_write(
    SourceClockMediaReadyWriteResult result, bool *emitted) {
  if (emitted != NULL && result == SOURCE_CLOCK_MEDIA_READY_WRITTEN) {
    *emitted = true;
  }
}

#endif
