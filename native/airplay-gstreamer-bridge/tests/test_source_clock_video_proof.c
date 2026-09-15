#include "source_clock_video_proof.h"
#include "test_check.h"
#include <stdio.h>

#define CHECK TEST_CHECK

/* A missing barrier would admit P/old frames, duplicate decoder/output work,
 * or complete proof before appsrc has returned success. Literal identities
 * deliberately do not involve timestamps or media frame rates. */
static void test_reserves_only_one_post_watermark_idr(void) {
  SourceClockVideoProof p;
  CHECK(source_clock_video_proof_init(&p, 7, 100));
  CHECK(!source_clock_video_proof_select(&p, 7, 101, false));
  CHECK(!source_clock_video_proof_select(&p, 7, 100, true));
  CHECK(!source_clock_video_proof_select(&p, 7, 99, true));
  CHECK(source_clock_video_proof_select(&p, 7, 105, true));
  CHECK(!source_clock_video_proof_select(&p, 7, 106, true));
  CHECK(p.source_generation == 7 && p.selected_sequence == 105);
  CHECK(!source_clock_video_proof_complete(&p));
  CHECK(!source_clock_video_proof_take_output(&p));
}

static void commit(SourceClockVideoProof *p, SourceClockVideoProofEvent event) {
  CHECK(source_clock_video_proof_take_event(p) == event);
  CHECK(source_clock_video_proof_take_event(p) == SOURCE_CLOCK_PROOF_EVENT_NONE);
  CHECK(source_clock_video_proof_finish_event(p, event, true));
}

static void selected(SourceClockVideoProof *p) {
  CHECK(source_clock_video_proof_init(p, 7, 100));
  CHECK(source_clock_video_proof_select(p, 7, 101, true));
}

static void test_callback_can_precede_push_return(void) {
  SourceClockVideoProof p;
  selected(&p);
  CHECK(source_clock_video_proof_decoded(&p, true));
  CHECK(source_clock_video_proof_take_event(&p) == SOURCE_CLOCK_PROOF_EVENT_NONE);
  CHECK(!source_clock_video_proof_take_output(&p));
  CHECK(source_clock_video_proof_input_result(&p, true));
  commit(&p, SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR);
  CHECK(!source_clock_video_proof_take_output(&p));
  commit(&p, SOURCE_CLOCK_PROOF_EVENT_DECODED);
  CHECK(source_clock_video_proof_take_output(&p));
  CHECK(!source_clock_video_proof_take_output(&p));
  CHECK(source_clock_video_proof_encoded(&p, true));
  CHECK(source_clock_video_proof_take_event(&p) == SOURCE_CLOCK_PROOF_EVENT_NONE);
  CHECK(source_clock_video_proof_output_result(&p, true));
  CHECK(!source_clock_video_proof_complete(&p));
  commit(&p, SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR);
  CHECK(source_clock_video_proof_complete(&p));
  CHECK(source_clock_video_proof_take_event(&p) == SOURCE_CLOCK_PROOF_EVENT_NONE);
}

static void test_push_can_precede_callback(void) {
  SourceClockVideoProof p;
  selected(&p);
  CHECK(source_clock_video_proof_input_result(&p, true));
  commit(&p, SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR);
  CHECK(source_clock_video_proof_take_event(&p) == SOURCE_CLOCK_PROOF_EVENT_NONE);
  CHECK(source_clock_video_proof_decoded(&p, true));
  commit(&p, SOURCE_CLOCK_PROOF_EVENT_DECODED);
  CHECK(source_clock_video_proof_take_output(&p));
  CHECK(source_clock_video_proof_output_result(&p, true));
  CHECK(source_clock_video_proof_take_event(&p) == SOURCE_CLOCK_PROOF_EVENT_NONE);
  CHECK(source_clock_video_proof_encoded(&p, true));
  commit(&p, SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR);
  CHECK(source_clock_video_proof_complete(&p));
}

static void test_failed_or_stopped_proof_never_retries(void) {
  for (int failure = 0; failure < 10; ++failure) {
    SourceClockVideoProof p;
    selected(&p);
    if (failure == 0) CHECK(!source_clock_video_proof_input_result(&p, false));
    if (failure == 1) CHECK(!source_clock_video_proof_decoded(&p, false));
    if (failure == 2) source_clock_video_proof_stop(&p);
    if (failure == 3) CHECK(!source_clock_video_proof_encoded(&p, true));
    if (failure >= 4) {
      CHECK(source_clock_video_proof_input_result(&p, true));
      CHECK(source_clock_video_proof_take_event(&p) == SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR);
      if (failure == 4) CHECK(!source_clock_video_proof_finish_event(&p, SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR, false));
      if (failure == 5) CHECK(!source_clock_video_proof_finish_event(&p, SOURCE_CLOCK_PROOF_EVENT_DECODED, true));
      if (failure >= 6) {
        CHECK(source_clock_video_proof_finish_event(&p, SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR, true));
        CHECK(source_clock_video_proof_decoded(&p, true));
        if (failure == 6) CHECK(!source_clock_video_proof_decoded(&p, true));
        if (failure >= 7) {
          commit(&p, SOURCE_CLOCK_PROOF_EVENT_DECODED);
          CHECK(source_clock_video_proof_take_output(&p));
          if (failure == 7) CHECK(!source_clock_video_proof_output_result(&p, false));
          if (failure == 8) CHECK(!source_clock_video_proof_encoded(&p, false));
          if (failure == 9) {
            CHECK(source_clock_video_proof_output_result(&p, true));
            CHECK(source_clock_video_proof_encoded(&p, true));
            CHECK(source_clock_video_proof_take_event(&p) == SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR);
            source_clock_video_proof_stop(&p);
            CHECK(!source_clock_video_proof_finish_event(&p, SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR, true));
          }
        }
      }
    }
    CHECK(!source_clock_video_proof_complete(&p));
    CHECK(source_clock_video_proof_take_event(&p) == SOURCE_CLOCK_PROOF_EVENT_NONE);
    CHECK(!source_clock_video_proof_select(&p, 7, 102, true));
    CHECK(!source_clock_video_proof_take_output(&p));
    CHECK(!source_clock_video_proof_input_result(&p, true));
  }
}

static void test_invalid_identity(void) {
  SourceClockVideoProof p = {0};
  CHECK(!source_clock_video_proof_select(&p, 7, 101, true));
  CHECK(!source_clock_video_proof_init(&p, 0, 100));
  CHECK(!source_clock_video_proof_init(&p, 7, 0));
  CHECK(source_clock_video_proof_init(&p, 7, UINT32_MAX));
  CHECK(!source_clock_video_proof_select(&p, 7, 0, true));
  CHECK(!source_clock_video_proof_select(&p, 7, UINT32_MAX, true));
  CHECK(!source_clock_video_proof_complete(&p));
  CHECK(source_clock_video_proof_init(&p, 7, 100));
  CHECK(!source_clock_video_proof_select(&p, 8, 101, true));
  CHECK(!source_clock_video_proof_select(&p, 7, 102, true));
  CHECK(source_clock_video_proof_init(&p, UINT32_MAX, UINT32_MAX - 1));
  CHECK(source_clock_video_proof_select(&p, UINT32_MAX, UINT32_MAX, true));
}

int main(void) {
  test_reserves_only_one_post_watermark_idr();
  test_callback_can_precede_push_return();
  test_push_can_precede_callback();
  test_failed_or_stopped_proof_never_retries();
  test_invalid_identity();
  puts("source-clock candidate video proof tests: PASS");
  return 0;
}
