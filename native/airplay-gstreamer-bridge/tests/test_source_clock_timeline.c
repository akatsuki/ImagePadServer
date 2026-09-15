#include "source_clock_timeline.h"
#include "test_check.h"

#include <stdint.h>
#include <inttypes.h>

static void test_common_origin(void) {
  SourceClockTimeline t;
  source_clock_timeline_init(&t);
  SourceClockSessionResult session_result = source_clock_timeline_session_start(&t, 1);
  TEST_CHECK(session_result == SOURCE_CLOCK_SESSION_NEW);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_AUDIO, 1000000000ULL, 10);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_VIDEO, 1050000000ULL, 20);
  int ready = source_clock_timeline_ready(&t, 2000000000ULL);
  TEST_CHECK(ready);

  uint64_t mapped = 0;
  SourceClockMapResult map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 1000000000ULL, 30,
                                                              2100000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
  TEST_CHECK(mapped == 2100000000ULL);
  map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, 1050000000ULL, 40,
                                         2150000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
  TEST_CHECK(mapped == 2150000000ULL);
}

static void test_warmup_and_duplicate_generation(void) {
  SourceClockTimeline t;
  source_clock_timeline_init(&t);
  SourceClockSessionResult session_result = source_clock_timeline_session_start(&t, 7);
  TEST_CHECK(session_result == SOURCE_CLOCK_SESSION_NEW);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_AUDIO, 100, 1000000000ULL);
  int ready = source_clock_timeline_ready(&t, 1499999999ULL);
  TEST_CHECK(!ready);
  ready = source_clock_timeline_ready(&t, 1500000000ULL);
  TEST_CHECK(ready);
  uint64_t origin = t.output_origin_ns;
  session_result = source_clock_timeline_session_start(&t, 7);
  TEST_CHECK(session_result == SOURCE_CLOCK_SESSION_DUPLICATE);
  TEST_CHECK(t.output_origin_ns == origin);
}

static void test_video_late_and_clock_jump(void) {
  SourceClockTimeline t;
  source_clock_timeline_init(&t);
  source_clock_timeline_session_start(&t, 1);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_AUDIO, 0, 0);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_VIDEO, 0, 0);
  int ready = source_clock_timeline_ready(&t, 1000000000ULL);
  TEST_CHECK(ready);
  uint64_t mapped = 0;
  SourceClockMapResult map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, 0, 0, 1200000001ULL,
                                                              &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_DROP_LATE);
  map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, 5000000000ULL, 5000000000ULL,
                                         5100000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
  map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, 6000000000ULL,
                                         2000000000ULL, 6100000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_RESET_EPOCH);
  TEST_CHECK(t.source_origin_ns == 6000000000ULL);
  TEST_CHECK(t.output_origin_ns == 6200000000ULL);
  TEST_CHECK(mapped == 6200000000ULL);
}

static void test_delayed_packet_does_not_reset_epoch(void) {
  SourceClockTimeline t;
  uint64_t mapped = 0;
  source_clock_timeline_init(&t);
  source_clock_timeline_session_start(&t, 1);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_AUDIO, 1000000000ULL, 0);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_VIDEO, 1000000000ULL, 0);
  int ready = source_clock_timeline_ready(&t, 0);
  TEST_CHECK(ready);
  SourceClockMapResult map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 2000000000ULL,
                                                              1000000000ULL, 1000000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
  uint64_t epoch = t.output_origin_ns;
  map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 1500000000ULL,
                                         1500000000ULL, 1500000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_DROP_LATE);
  TEST_CHECK(t.output_origin_ns == epoch);
  map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 2100000000ULL,
                                         1100000000ULL, 1200000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
  TEST_CHECK(mapped == 1200000000ULL);
  TEST_CHECK(t.source_origin_ns == 1000000000ULL);
  TEST_CHECK(t.output_origin_ns == epoch);
}

static void test_long_progression_and_reset_floor(void) {
  SourceClockTimeline t;
  source_clock_timeline_init(&t);
  source_clock_timeline_session_start(&t, 10);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_AUDIO, 1000000000ULL, 1000000000ULL);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_VIDEO, 1000000000ULL, 1000000000ULL);
  int ready = source_clock_timeline_ready(&t, 1000000000ULL);
  TEST_CHECK(ready);
  uint64_t mapped = 0;
  SourceClockMapResult map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 1801000000000ULL,
                                                              1801000000000ULL, 1801100000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
  TEST_CHECK(mapped == 1801100000000ULL);

  SourceClockSessionResult session_result = source_clock_timeline_session_start(&t, 11);
  TEST_CHECK(session_result == SOURCE_CLOCK_SESSION_NEW);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_AUDIO, 50, 1801200000000ULL);
  ready = source_clock_timeline_ready(&t, 1801700000000ULL);
  TEST_CHECK(ready);
  TEST_CHECK(t.output_origin_ns > mapped);
  map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 50, 1801200000000ULL,
                                         t.output_origin_ns, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
  TEST_CHECK(mapped > 1801100000000ULL);
}

static void test_stream_interleave_does_not_drop_audio(void) {
  SourceClockTimeline t;
  uint64_t mapped = 0;
  source_clock_timeline_init(&t);
  source_clock_timeline_session_start(&t, 1);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_AUDIO, 1000000000ULL, 0);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_VIDEO, 1000000000ULL, 0);
  int ready = source_clock_timeline_ready(&t, 0);
  TEST_CHECK(ready);
  SourceClockMapResult map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 1010000000ULL,
                                                              10000000ULL, 101000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
  map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, 1016000000ULL,
                                         16000000ULL, 101600000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
  map_result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 1020000000ULL,
                                         20000000ULL, 102000000ULL, &mapped);
  TEST_CHECK(map_result == SOURCE_CLOCK_MAPPED);
}

/* Keep checking independent cases after a behavioral failure so the RED log
 * records every reset branch and the actual origin/last values. */
static void expect_value(const char *case_name, const char *field, uint64_t actual,
                         uint64_t expected, unsigned *failures) {
  if (actual != expected) {
    fprintf(stderr, "%s: %s actual=%" PRIu64 " expected=%" PRIu64 "\n",
            case_name, field, actual, expected);
    ++*failures;
  }
}

static void setup_video_epoch(SourceClockTimeline *t, bool near_overflow) {
  source_clock_timeline_init(t);
  TEST_CHECK(source_clock_timeline_session_start(t, 30) == SOURCE_CLOCK_SESSION_NEW);
  uint64_t arrival = near_overflow ? UINT64_MAX - 800000000ULL : 1000000000ULL;
  uint64_t ready_at = near_overflow ? UINT64_MAX - 300000000ULL : 1500000000ULL;
  uint64_t output = near_overflow ? UINT64_MAX - 200000000ULL : 1600000000ULL;
  source_clock_timeline_offer_first(t, SOURCE_CLOCK_STREAM_VIDEO, 10000000000ULL, arrival);
  TEST_CHECK(!source_clock_timeline_ready(t, ready_at - 1));
  TEST_CHECK(source_clock_timeline_ready(t, ready_at));
  TEST_CHECK(t->source_origin_ns == 10000000000ULL);
  TEST_CHECK(t->output_origin_ns == output);
  uint64_t mapped = 0;
  TEST_CHECK(source_clock_timeline_map(t, SOURCE_CLOCK_STREAM_VIDEO, 10000000000ULL,
                                     arrival, output, &mapped) == SOURCE_CLOCK_MAPPED);
  TEST_CHECK(mapped == output);
  TEST_CHECK(source_clock_timeline_map(t, SOURCE_CLOCK_STREAM_VIDEO, 10020000000ULL,
                                     output + 20000000ULL, output + 20000000ULL,
                                     &mapped) == SOURCE_CLOCK_MAPPED);
  TEST_CHECK(mapped == (near_overflow ? UINT64_MAX - 180000000ULL : 1620000000ULL));
}

static void test_audio_drops_preserve_video_epoch(void) {
  const struct {
    const char *name;
    bool accepted_audio;
    bool near_overflow;
    uint64_t ntp;
    uint64_t now;
  } cases[] = {
    {"old_first_audio/source_lt_origin", false, false, 9999999999ULL, 1630000000ULL},
    {"audio_clock_mismatch", true, false, 12010000000ULL, 1630000000ULL},
    {"accepted_audio/source_lt_origin", true, false, 9999999999ULL, 1630000000ULL},
    {"audio_output_overflow", true, true, 10210000000ULL, UINT64_MAX - 170000000ULL},
    {"audio_delayed_packet", true, false, 9800000000ULL, 1630000000ULL},
    {"audio_mapped_regression", true, false, 10005000000ULL, 1630000000ULL},
    {"audio_running_late", true, false, 10020000000ULL, 1720000001ULL},
    {"first_audio_running_late", false, false, 10020000000ULL, 1720000001ULL},
  };
  unsigned failures = 0;
  for (unsigned i = 0; i < sizeof(cases) / sizeof(cases[0]); ++i) {
    SourceClockTimeline t;
    setup_video_epoch(&t, cases[i].near_overflow);
    uint64_t audio_time = cases[i].near_overflow ? UINT64_MAX - 190000000ULL : 1610000000ULL;
    uint64_t mapped = 0;
    if (cases[i].accepted_audio) {
      source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_AUDIO, 10010000000ULL, audio_time);
      TEST_CHECK(source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 10010000000ULL,
                                         audio_time, audio_time, &mapped) == SOURCE_CLOCK_MAPPED);
      TEST_CHECK(mapped == audio_time);
    } else {
      source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_AUDIO, cases[i].ntp, cases[i].now);
    }
    TEST_CHECK(source_clock_timeline_ready(&t, cases[i].now));
    TEST_CHECK(t.source_origin_ns == 10000000000ULL);
    TEST_CHECK(t.output_origin_ns == (cases[i].near_overflow ? UINT64_MAX - 200000000ULL : 1600000000ULL));
    TEST_CHECK(t.has_last_audio == cases[i].accepted_audio);
    TEST_CHECK(t.has_last_audio_mapped == cases[i].accepted_audio);
    TEST_CHECK(t.last_audio_ntp_ns == (cases[i].accepted_audio ? 10010000000ULL : 0));
    TEST_CHECK(t.last_audio_arrival_ns == (cases[i].accepted_audio ? audio_time : 0));
    TEST_CHECK(t.last_audio_mapped_ns == (cases[i].accepted_audio ? audio_time : 0));
    SourceClockTimeline before = t;
    SourceClockMapResult result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO,
                                                           cases[i].ntp, cases[i].now,
                                                           cases[i].now, &mapped);
    fprintf(stderr, "%s: result=%d origin=%" PRIu64 "/%" PRIu64
                    " audio(has/ntp/arrival/has_mapped/mapped)=%d/%" PRIu64 "/%" PRIu64
                    "/%d/%" PRIu64 " global_last=%" PRIu64 "\n",
            cases[i].name, (int)result, t.source_origin_ns, t.output_origin_ns,
            (int)t.has_last_audio, t.last_audio_ntp_ns, t.last_audio_arrival_ns,
            (int)t.has_last_audio_mapped, t.last_audio_mapped_ns, t.last_mapped_ns);
    expect_value(cases[i].name, "result", result, SOURCE_CLOCK_DROP_LATE, &failures);
    expect_value(cases[i].name, "source_origin", t.source_origin_ns, before.source_origin_ns, &failures);
    expect_value(cases[i].name, "output_origin", t.output_origin_ns, before.output_origin_ns, &failures);
    expect_value(cases[i].name, "has_last_audio", t.has_last_audio, before.has_last_audio, &failures);
    expect_value(cases[i].name, "last_audio_ntp", t.last_audio_ntp_ns, before.last_audio_ntp_ns, &failures);
    expect_value(cases[i].name, "last_audio_arrival", t.last_audio_arrival_ns, before.last_audio_arrival_ns, &failures);
    expect_value(cases[i].name, "has_last_audio_mapped", t.has_last_audio_mapped, before.has_last_audio_mapped, &failures);
    expect_value(cases[i].name, "last_audio_mapped", t.last_audio_mapped_ns, before.last_audio_mapped_ns, &failures);
    expect_value(cases[i].name, "has_last_mapped", t.has_last_mapped, before.has_last_mapped, &failures);
    expect_value(cases[i].name, "global_last", t.last_mapped_ns, before.last_mapped_ns, &failures);
    uint64_t next_video = cases[i].near_overflow ? UINT64_MAX - 160000000ULL : 1640000000ULL;
    result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, 10040000000ULL,
                                      next_video, cases[i].now, &mapped);
    fprintf(stderr, "%s: next_video result=%d mapped=%" PRIu64 "\n",
            cases[i].name, (int)result, mapped);
    expect_value(cases[i].name, "next_video_result", result, SOURCE_CLOCK_MAPPED, &failures);
    expect_value(cases[i].name, "next_video_mapped", mapped, next_video, &failures);
    expect_value(cases[i].name, "next_video_source_origin", t.source_origin_ns, before.source_origin_ns, &failures);
    expect_value(cases[i].name, "next_video_output_origin", t.output_origin_ns, before.output_origin_ns, &failures);
    /* A normal audio after a rejection must still compare with the accepted
     * point, not the rejected packet's source/arrival/mapped values. */
    uint64_t next_audio = cases[i].near_overflow ? UINT64_MAX - 150000000ULL : 1650000000ULL;
    result = source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_AUDIO, 10050000000ULL,
                                      next_audio, cases[i].now, &mapped);
    expect_value(cases[i].name, "next_audio_result", result, SOURCE_CLOCK_MAPPED, &failures);
    expect_value(cases[i].name, "next_audio_mapped", mapped, next_audio, &failures);
    expect_value(cases[i].name, "accepted_next_audio_ntp", t.last_audio_ntp_ns, 10050000000ULL, &failures);
    expect_value(cases[i].name, "accepted_next_audio_arrival", t.last_audio_arrival_ns, next_audio, &failures);
    expect_value(cases[i].name, "accepted_next_audio_mapped", t.last_audio_mapped_ns, next_audio, &failures);
  }
  TEST_CHECK(failures == 0);
}

static void test_video_epoch_session_generation(void) {
  SourceClockTimeline t;
  setup_video_epoch(&t, false);
  SourceClockTimeline before = t;
  TEST_CHECK(source_clock_timeline_session_start(&t, 30) == SOURCE_CLOCK_SESSION_DUPLICATE);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_VIDEO, 50, 1700000000ULL);
  TEST_CHECK(source_clock_timeline_ready(&t, 1700000000ULL));
  TEST_CHECK(t.source_origin_ns == before.source_origin_ns);
  TEST_CHECK(t.source_origin_ns == 10000000000ULL);
  TEST_CHECK(t.output_origin_ns == before.output_origin_ns);
  TEST_CHECK(t.output_origin_ns == 1600000000ULL);
  TEST_CHECK(t.last_video_ntp_ns == 10020000000ULL);
  TEST_CHECK(t.last_video_mapped_ns == 1620000000ULL);
  TEST_CHECK(source_clock_timeline_session_start(&t, 31) == SOURCE_CLOCK_SESSION_NEW);
  TEST_CHECK(!t.ready && !t.has_first_audio && !t.has_first_video);
  TEST_CHECK(!t.has_last_audio && !t.has_last_audio_mapped);
  TEST_CHECK(!t.has_last_video && !t.has_last_video_mapped);
  uint64_t mapped = 0;
  TEST_CHECK(source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, 50, 1, 1,
                                     &mapped) == SOURCE_CLOCK_NOT_READY);
  source_clock_timeline_offer_first(&t, SOURCE_CLOCK_STREAM_VIDEO, 50, 1);
  TEST_CHECK(!source_clock_timeline_ready(&t, 500000000ULL));
  TEST_CHECK(source_clock_timeline_ready(&t, 500000001ULL));
  TEST_CHECK(t.source_origin_ns == 50);
  /* New generation cannot move below the prior accepted global floor. */
  TEST_CHECK(t.output_origin_ns == 1620000001ULL);
  TEST_CHECK(source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, 50, 1,
                                     1620000001ULL, &mapped) == SOURCE_CLOCK_MAPPED);
  TEST_CHECK(mapped == 1620000001ULL);
}

static void test_video_origin_and_overflow_reset_recovery(void) {
  for (unsigned overflow = 0; overflow < 2; ++overflow) {
    SourceClockTimeline t;
    setup_video_epoch(&t, overflow != 0);
    uint64_t ntp = overflow ? 10210000000ULL : 9999999999ULL;
    uint64_t now = overflow ? UINT64_MAX - 170000000ULL : 1630000000ULL;
    uint64_t expected = overflow ? UINT64_MAX - 70000000ULL : 1730000000ULL;
    uint64_t mapped = 0;
    TEST_CHECK(source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, ntp, now,
                                       now, &mapped) == SOURCE_CLOCK_RESET_EPOCH);
    TEST_CHECK(t.source_origin_ns == ntp);
    TEST_CHECK(t.output_origin_ns == expected);
    TEST_CHECK(mapped == expected);
    TEST_CHECK(t.last_video_ntp_ns == ntp);
    TEST_CHECK(t.last_video_arrival_ns == now);
    TEST_CHECK(source_clock_timeline_map(&t, SOURCE_CLOCK_STREAM_VIDEO, ntp + 10000000ULL,
                                       now + 10000000ULL, now + 10000000ULL,
                                       &mapped) == SOURCE_CLOCK_MAPPED);
    TEST_CHECK(mapped == (overflow ? UINT64_MAX - 60000000ULL : 1740000000ULL));
    TEST_CHECK(t.source_origin_ns == ntp);
    TEST_CHECK(t.output_origin_ns == expected);
  }
}

int main(void) {
  test_common_origin();
  test_warmup_and_duplicate_generation();
  test_video_late_and_clock_jump();
  test_delayed_packet_does_not_reset_epoch();
  test_long_progression_and_reset_floor();
  test_stream_interleave_does_not_drop_audio();
  test_video_epoch_session_generation();
  test_video_origin_and_overflow_reset_recovery();
  test_audio_drops_preserve_video_epoch();
  return 0;
}
