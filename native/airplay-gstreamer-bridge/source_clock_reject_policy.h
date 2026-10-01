#ifndef IMAGEPAD_SOURCE_CLOCK_REJECT_POLICY_H
#define IMAGEPAD_SOURCE_CLOCK_REJECT_POLICY_H

#include <stdbool.h>
#include <stdint.h>
#include "source_clock_reader.h"

typedef enum {
  SOURCE_CLOCK_REJECT_ACTION_CONTINUE = 0,       /* close this connection only */
  SOURCE_CLOCK_REJECT_ACTION_FATAL_PIPELINE = 1, /* exit 22, restartable */
  SOURCE_CLOCK_REJECT_ACTION_UNRECOVERABLE = 2,  /* exit 21, no restart */
} SourceClockRejectAction;

/* Per-publisher shared state; not internally synchronized: the caller must guard it with a mutex. Re-init per publisher, not per connection. */
typedef struct {
  uint32_t consecutive;
  int64_t first_us;
  bool ever_authenticated;
} SourceClockRejectState;

#define SOURCE_CLOCK_REJECT_ESCALATE_AFTER 3u
#define SOURCE_CLOCK_REJECT_ESCALATE_WINDOW_US (5 * 1000 * 1000)

void source_clock_reject_state_init(SourceClockRejectState *state);
void source_clock_reject_state_note_auth(SourceClockRejectState *state);
SourceClockRejectAction source_clock_reject_state_note_failure(
    SourceClockRejectState *state, SourceClockReaderStatus status, int64_t now_us);

#endif
