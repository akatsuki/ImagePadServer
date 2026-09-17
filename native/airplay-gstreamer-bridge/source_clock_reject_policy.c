#include "source_clock_reject_policy.h"

void source_clock_reject_state_init(SourceClockRejectState *state) {
  if (state == NULL) return;
  state->consecutive = 0;
  state->first_us = 0;
  state->ever_authenticated = false;
}

void source_clock_reject_state_note_auth(SourceClockRejectState *state) {
  if (state == NULL) return;
  state->consecutive = 0;
  state->first_us = 0;
  state->ever_authenticated = true;
}

SourceClockRejectAction source_clock_reject_state_note_failure(
    SourceClockRejectState *state, SourceClockReaderStatus status, int64_t now_us) {
  if (state == NULL) return SOURCE_CLOCK_REJECT_ACTION_CONTINUE;
  /* A valid, authenticated peer frame that our own pipeline could not accept
   * is a pipeline failure, not a peer protocol violation. */
  if (status == SOURCE_CLOCK_READER_CALLBACK_ERROR) {
    return SOURCE_CLOCK_REJECT_ACTION_FATAL_PIPELINE;
  }
  if (state->consecutive == 0) state->first_us = now_us;
  if (state->consecutive < UINT32_MAX) state->consecutive++;
  /* The window is anchored at the first reject, so a slow trickle with >=3
   * rejects does escalate once >=5s have elapsed since the first reject.
   * ever_authenticated is sticky until init: a peer that ever authenticated
   * is never treated as an unauthenticated escalator. */
  if (!state->ever_authenticated &&
      state->consecutive >= SOURCE_CLOCK_REJECT_ESCALATE_AFTER &&
      now_us - state->first_us >= SOURCE_CLOCK_REJECT_ESCALATE_WINDOW_US) {
    return SOURCE_CLOCK_REJECT_ACTION_UNRECOVERABLE;
  }
  return SOURCE_CLOCK_REJECT_ACTION_CONTINUE;
}
