#include "source_clock_reject_policy.h"
#ifdef NDEBUG
#undef NDEBUG
#endif
#include <assert.h>
#include <stdio.h>

static void test_callback_error_is_pipeline_fatal(void) {
  SourceClockRejectState s;
  source_clock_reject_state_init(&s);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_CALLBACK_ERROR, 0) ==
         SOURCE_CLOCK_REJECT_ACTION_FATAL_PIPELINE);
  assert(s.consecutive == 0 && s.first_us == 0);
}

static void test_transient_rejects_do_not_escalate(void) {
  SourceClockRejectState s;
  source_clock_reject_state_init(&s);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_TOKEN, 1000) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
  assert(s.consecutive == 1);
}

static void test_persistent_unauthenticated_escalates_after_window(void) {
  SourceClockRejectState s;
  source_clock_reject_state_init(&s);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 1000) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 1100) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 1200) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 7000000) ==
         SOURCE_CLOCK_REJECT_ACTION_UNRECOVERABLE);
}

static void test_authenticated_connection_resets_budget(void) {
  SourceClockRejectState s;
  source_clock_reject_state_init(&s);
  (void)source_clock_reject_state_note_failure(
      &s, SOURCE_CLOCK_READER_BAD_TOKEN, 1000);
  source_clock_reject_state_note_auth(&s);
  assert(s.consecutive == 0 && s.ever_authenticated);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_TOKEN, 9000000) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
}

static void test_null_state_is_safe(void) {
  source_clock_reject_state_init(NULL);
  source_clock_reject_state_note_auth(NULL);
  assert(source_clock_reject_state_note_failure(
             NULL, SOURCE_CLOCK_READER_BAD_TOKEN, 42) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
}

static void test_exact_window_boundary_escalates(void) {
  SourceClockRejectState s;
  source_clock_reject_state_init(&s);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 0) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 1) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 2) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 5000000) ==
         SOURCE_CLOCK_REJECT_ACTION_UNRECOVERABLE);
}

static void test_post_auth_rejects_never_escalate(void) {
  SourceClockRejectState s;
  source_clock_reject_state_init(&s);
  source_clock_reject_state_note_auth(&s);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 10000000) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 11000000) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
  assert(source_clock_reject_state_note_failure(
             &s, SOURCE_CLOCK_READER_BAD_PROTOCOL, 12000000) ==
         SOURCE_CLOCK_REJECT_ACTION_CONTINUE);
}

int main(void) {
  test_callback_error_is_pipeline_fatal();
  test_transient_rejects_do_not_escalate();
  test_persistent_unauthenticated_escalates_after_window();
  test_authenticated_connection_resets_budget();
  test_null_state_is_safe();
  test_exact_window_boundary_escalates();
  test_post_auth_rejects_never_escalate();
  puts("source-clock reject policy: PASS");
  return 0;
}
