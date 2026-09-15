#include "source_clock_video_watermark.h"
#include "test_check.h"

#include <stdio.h>
#include <string.h>

#define CHECK TEST_CHECK
#define ACCEPT SOURCE_CLOCK_VIDEO_WATERMARK_ACCEPTED
#define UPDATE SOURCE_CLOCK_VIDEO_WATERMARK_UPDATED
#define REJECT SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED
#define IGNORE SOURCE_CLOCK_VIDEO_WATERMARK_IGNORED
#define MORE SOURCE_CLOCK_VIDEO_WATERMARK_INCOMPLETE
#define CONFIG (SOURCE_CLOCK_FLAG_CONFIG | SOURCE_CLOCK_FLAG_DISCONTINUITY)

/* Hand-authored Annex-B fixtures, not outputs from the classifier under test. */
static const uint8_t config[] = {0,0,0,1,0x67,0x42,0,0x1e,0x80,
                               0,0,1,0x68,0xc0};
static const uint8_t idr[] = {0,0,0,1,0x65,0xb8};
static const uint8_t pframe[] = {0,0,1,0x41,0xe0};
static const uint8_t combined[] = {0,0,1,0x67,0x42,0,0x1e,0x80,
                                  0,0,0,1,0x68,0xc0,0,0,1,0x65,0xb8};
static const uint8_t aud_sei[] = {0,0,1,0x09,0xf0,0,0,1,0x06,0x80};
static const uint8_t h265[] = {0,0,1,0x26,0x01,0x80};

typedef struct Fixture {
  SourceClockVideoWatermark tracker;
  SourceClockVideoWatermarkConnection video;
} Fixture;

static SourceClockHeader header(uint8_t kind, uint8_t codec, uint16_t flags,
                                uint32_t seq, uint64_t ntp, size_t bytes) {
  SourceClockHeader h = {0};
  h.version = SOURCE_CLOCK_PROTOCOL_VERSION;
  h.header_bytes = SOURCE_CLOCK_HEADER_SIZE;
  h.stream_kind = kind;
  h.codec = codec;
  h.flags = flags;
  h.sequence = seq;
  h.remote_ntp_ns = ntp;
  h.payload_bytes = (uint32_t)bytes;
  return h;
}

static SourceClockVideoWatermarkResult control(Fixture *f,
    SourceClockVideoWatermarkConnection *c, uint8_t opcode, uint32_t gen) {
  SourceClockHeader h = header(SOURCE_CLOCK_STREAM_CONTROL, opcode, 0, gen, 0, 0);
  return source_clock_video_watermark_offer(&f->tracker, c, &h, NULL, 0);
}

static SourceClockVideoWatermarkResult media(Fixture *f,
    SourceClockVideoWatermarkConnection *c, uint8_t codec, uint16_t flags,
    uint32_t seq, uint64_t ntp, const uint8_t *data, size_t bytes) {
  SourceClockHeader h = header(SOURCE_CLOCK_STREAM_VIDEO, codec, flags, seq, ntp, bytes);
  return source_clock_video_watermark_offer(&f->tracker, c, &h, data, bytes);
}

#define H264(f, flags, seq, ntp, data) \
  media((f), &(f)->video, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, \
        (flags), (seq), (ntp), (data), sizeof(data))

static void expect(Fixture *f, uint32_t gen, uint32_t seq) {
  SourceClockVideoWatermarkSnapshot s = source_clock_video_watermark_snapshot(&f->tracker);
  CHECK(s.has == (gen != 0));
  CHECK(s.source_session_generation == gen);
  CHECK(s.source_video_sequence == seq);
}

static void init(Fixture *f, uint32_t gen) {
  source_clock_video_watermark_init(&f->tracker);
  source_clock_video_watermark_connection_init(&f->video, SOURCE_CLOCK_STREAM_VIDEO);
  expect(f, 0, 0);
  if (gen != 0) CHECK(control(f, &f->video, SOURCE_CLOCK_CONTROL_SESSION_START, gen) == ACCEPT);
}

/* Flag-only classifiers must not invent VCL, or hide VCL behind CONFIG. */
static void test_payload_classification(void) {
  Fixture f;
  init(&f, 7);
  CHECK(H264(&f, CONFIG, 10, 100, config) == ACCEPT);
  expect(&f, 0, 0);
  CHECK(H264(&f, SOURCE_CLOCK_FLAG_KEYFRAME, 11, 101, aud_sei) == ACCEPT);
  expect(&f, 0, 0);
  CHECK(H264(&f, 0, 12, 102, idr) == UPDATE);
  expect(&f, 7, 12);
  CHECK(H264(&f, CONFIG, 14, 103, combined) == UPDATE);
  expect(&f, 7, 14);
  CHECK(H264(&f, SOURCE_CLOCK_FLAG_KEYFRAME, 15, 104, pframe) == UPDATE);
  expect(&f, 7, 15);
  CHECK(H264(&f, CONFIG, 16, 105, pframe) == UPDATE);
  expect(&f, 7, 16);
}

/* All and only base H264 VCL types 1..5 count, without downstream IDR gating. */
static void test_vcl_types_and_non_vcl_high_water(void) {
  Fixture f;
  uint8_t nal[] = {0,0,1,0x41,0x80};
  init(&f, 1);
  for (uint8_t type = 1; type <= 5; ++type) {
    nal[3] = (uint8_t)(0x60 | type);
    CHECK(H264(&f, 0, type, 100, nal) == UPDATE);
    expect(&f, 1, type);
  }
  for (uint8_t type = 6; type <= 12; ++type) {
    nal[3] = type;
    CHECK(H264(&f, 0, type, 100, nal) == ACCEPT);
    expect(&f, 1, 5);
  }
  CHECK(H264(&f, 0, 11, 101, idr) == REJECT);
  CHECK(H264(&f, 0, 12, 101, idr) == REJECT);
  CHECK(H264(&f, 0, 13, 101, pframe) == UPDATE);
}

static void test_bootstrap_pair_once(void) {
  Fixture f;
  init(&f, 3);
  CHECK(H264(&f, CONFIG, 10, 700, config) == ACCEPT);
  expect(&f, 0, 0);
  CHECK(H264(&f, 0, 10, 700, idr) == UPDATE);
  expect(&f, 3, 10);
  CHECK(H264(&f, 0, 10, 700, idr) == REJECT);
  CHECK(H264(&f, CONFIG, 10, 700, config) == REJECT);
  CHECK(H264(&f, 0, 18, 701, pframe) == UPDATE);
  CHECK(H264(&f, CONFIG, 20, 702, config) == ACCEPT);
  CHECK(H264(&f, SOURCE_CLOCK_FLAG_CONFIG | SOURCE_CLOCK_FLAG_KEYFRAME,
             20, 702, combined) == UPDATE);
  expect(&f, 3, 20);
  CHECK(H264(&f, CONFIG, 20, 702, combined) == REJECT);
}

static void test_pair_requires_actual_idr_and_matching_ntp(void) {
  Fixture f;
  init(&f, 3);
  CHECK(H264(&f, CONFIG, 10, 700, config) == ACCEPT);
  CHECK(H264(&f, SOURCE_CLOCK_FLAG_KEYFRAME, 10, 700, pframe) == REJECT);
  CHECK(H264(&f, 0, 10, 700, idr) == REJECT);
  CHECK(H264(&f, CONFIG, 11, 701, config) == ACCEPT);
  CHECK(H264(&f, 0, 11, 702, idr) == REJECT);
  CHECK(H264(&f, 0, 11, 701, idr) == REJECT);
  expect(&f, 0, 0);
}

static void test_duplicate_config_revokes_pair(void) {
  Fixture f;
  init(&f, 1);
  CHECK(H264(&f, CONFIG, 10, 100, config) == ACCEPT);
  CHECK(H264(&f, CONFIG, 10, 100, config) == REJECT);
  CHECK(H264(&f, 0, 10, 100, idr) == REJECT);
  CHECK(H264(&f, 0, 11, 101, idr) == UPDATE);
}

static void test_only_bootstrap_config_arms_pair(void) {
  Fixture f;
  const uint16_t flags[] = {0, SOURCE_CLOCK_FLAG_KEYFRAME, CONFIG, CONFIG};
  const uint8_t *data[] = {config, config, aud_sei, config};
  const size_t bytes[] = {sizeof(config), sizeof(config), sizeof(aud_sei), 9};
  for (size_t i = 0; i < 4; ++i) {
    init(&f, 1);
    CHECK(media(&f, &f.video, 1, flags[i], 10, 100, data[i], bytes[i]) == ACCEPT);
    CHECK(H264(&f, 0, 10, 100, idr) == REJECT);
  }
}

static void test_intervening_frames_break_pair(void) {
  Fixture f;
  init(&f, 1);
  CHECK(H264(&f, CONFIG, 10, 100, config) == ACCEPT);
  CHECK(H264(&f, 0, 11, 101, aud_sei) == ACCEPT);
  CHECK(H264(&f, 0, 10, 100, idr) == REJECT);
  CHECK(H264(&f, CONFIG, 12, 102, config) == ACCEPT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_HEARTBEAT, 1) == IGNORE);
  CHECK(H264(&f, 0, 12, 102, idr) == REJECT);
  CHECK(H264(&f, CONFIG, 13, 103, config) == ACCEPT);
  CHECK(media(&f, &f.video, 2, 0, 13, 103, h265, sizeof(h265)) == IGNORE);
  CHECK(H264(&f, 0, 13, 103, idr) == REJECT);
}

/* H265 must not exhaust the H264 sequence space or create a final tuple. */
static void test_h265_ignored(void) {
  Fixture f;
  init(&f, 1);
  CHECK(media(&f, &f.video, 2, 0, UINT32_MAX, 100, h265, sizeof(h265)) == IGNORE);
  expect(&f, 0, 0);
  CHECK(H264(&f, 0, 1, 101, pframe) == UPDATE);
  CHECK(media(&f, &f.video, 2, CONFIG, 0, 0, idr, sizeof(idr)) == IGNORE);
  expect(&f, 1, 1);
}

static void test_zero_gap_regression_and_ntp_not_ordering(void) {
  Fixture f;
  init(&f, 1);
  CHECK(H264(&f, 0, 0, 100, idr) == REJECT);
  CHECK(H264(&f, CONFIG, 0, 100, config) == REJECT);
  CHECK(H264(&f, 0, 5, 900, pframe) == UPDATE);
  CHECK(H264(&f, 0, 100, 800, pframe) == UPDATE);
  CHECK(H264(&f, 0, 99, 1000, idr) == REJECT);
  CHECK(H264(&f, 0, 100, 1000, idr) == REJECT);
  expect(&f, 1, 100);
}

static void test_max_pair_and_no_wrap_across_lifecycle(void) {
  Fixture f;
  init(&f, 1);
  CHECK(H264(&f, 0, UINT32_MAX - 1, 1, pframe) == UPDATE);
  CHECK(H264(&f, CONFIG, UINT32_MAX, 2, config) == ACCEPT);
  CHECK(H264(&f, 0, UINT32_MAX, 2, idr) == UPDATE);
  CHECK(H264(&f, 0, UINT32_MAX, 2, idr) == REJECT);
  source_clock_video_watermark_disconnect(&f.video);
  source_clock_video_watermark_connection_init(&f.video, SOURCE_CLOCK_STREAM_VIDEO);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 1) == ACCEPT);
  CHECK(H264(&f, 0, 0, 3, idr) == REJECT);
  CHECK(H264(&f, 0, 1, 3, idr) == REJECT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_END, 1) == ACCEPT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 2) == ACCEPT);
  CHECK(H264(&f, 0, 1, 4, idr) == REJECT);
  expect(&f, 1, UINT32_MAX);
}

static void test_no_session_and_audio_cannot_supply_generation(void) {
  Fixture f;
  SourceClockVideoWatermarkConnection audio;
  init(&f, 0);
  source_clock_video_watermark_connection_init(&audio, SOURCE_CLOCK_STREAM_AUDIO);
  CHECK(control(&f, &audio, SOURCE_CLOCK_CONTROL_SESSION_START, 900) == IGNORE);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_HELLO, 800) == IGNORE);
  CHECK(H264(&f, 0, UINT32_MAX, 100, idr) == REJECT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 0) == REJECT);
  expect(&f, 0, 0);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 2) == ACCEPT);
  CHECK(H264(&f, 0, 1, 100, pframe) == UPDATE);
  CHECK(control(&f, &audio, SOURCE_CLOCK_CONTROL_SESSION_END, 900) == IGNORE);
  CHECK(H264(&f, 0, 2, 100, pframe) == UPDATE);
  expect(&f, 2, 2);
}

static void test_generation_interleave_and_old_tuple_preserved(void) {
  Fixture f;
  SourceClockVideoWatermarkConnection other;
  init(&f, 4);
  CHECK(H264(&f, 0, 10, 100, idr) == UPDATE);
  source_clock_video_watermark_connection_init(&other, SOURCE_CLOCK_STREAM_VIDEO);
  CHECK(control(&f, &other, SOURCE_CLOCK_CONTROL_SESSION_START, 5) == ACCEPT);
  CHECK(media(&f, &other, 1, CONFIG, 11, 101, config, sizeof(config)) == ACCEPT);
  expect(&f, 4, 10);
  CHECK(H264(&f, 0, 1000, 102, idr) == REJECT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 4) == REJECT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_END, 4) == REJECT);
  CHECK(media(&f, &other, 1, 0, 11, 101, idr, sizeof(idr)) == UPDATE);
  expect(&f, 5, 11);
}

static void test_same_generation_reconnect_keeps_high_water(void) {
  Fixture f;
  init(&f, 4);
  CHECK(H264(&f, 0, 10, 100, idr) == UPDATE);
  CHECK(H264(&f, CONFIG, 11, 101, config) == ACCEPT);
  source_clock_video_watermark_disconnect(&f.video);
  CHECK(H264(&f, 0, 12, 102, idr) == REJECT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 4) == REJECT);
  source_clock_video_watermark_connection_init(&f.video, SOURCE_CLOCK_STREAM_VIDEO);
  CHECK(H264(&f, 0, 12, 102, idr) == REJECT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 4) == ACCEPT);
  CHECK(H264(&f, 0, 11, 101, idr) == REJECT);
  CHECK(H264(&f, CONFIG, 11, 101, config) == REJECT);
  CHECK(H264(&f, CONFIG, 15, 105, config) == ACCEPT);
  CHECK(H264(&f, 0, 15, 105, idr) == UPDATE);
  expect(&f, 4, 15);
}

static void test_pair_cannot_cross_connection_or_session_start(void) {
  Fixture f;
  SourceClockVideoWatermarkConnection other;
  init(&f, 1);
  source_clock_video_watermark_connection_init(&other, SOURCE_CLOCK_STREAM_VIDEO);
  CHECK(control(&f, &other, SOURCE_CLOCK_CONTROL_SESSION_START, 1) == ACCEPT);
  CHECK(H264(&f, CONFIG, 10, 100, config) == ACCEPT);
  CHECK(media(&f, &other, 1, 0, 10, 100, idr, sizeof(idr)) == REJECT);
  CHECK(H264(&f, 0, 10, 100, idr) == UPDATE);
  CHECK(H264(&f, CONFIG, 11, 101, config) == ACCEPT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 1) == ACCEPT);
  CHECK(H264(&f, 0, 11, 101, idr) == REJECT);
  CHECK(H264(&f, CONFIG, 12, 102, config) == ACCEPT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 2) == ACCEPT);
  CHECK(H264(&f, 0, 12, 102, idr) == REJECT);
  expect(&f, 1, 10);
}

static void test_end_invalidates_generation_not_final_tuple(void) {
  Fixture f;
  SourceClockVideoWatermarkConnection other;
  init(&f, 1);
  CHECK(H264(&f, 0, 9, 99, pframe) == UPDATE);
  CHECK(H264(&f, CONFIG, 10, 100, config) == ACCEPT);
  source_clock_video_watermark_connection_init(&other, SOURCE_CLOCK_STREAM_VIDEO);
  CHECK(control(&f, &other, SOURCE_CLOCK_CONTROL_SESSION_START, 1) == ACCEPT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_END, 1) == ACCEPT);
  CHECK(H264(&f, 0, 10, 100, idr) == REJECT);
  CHECK(media(&f, &other, 1, 0, 11, 101, idr, sizeof(idr)) == REJECT);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 1) == REJECT);
  expect(&f, 1, 9);
  CHECK(control(&f, &f.video, SOURCE_CLOCK_CONTROL_SESSION_START, 2) == ACCEPT);
  CHECK(H264(&f, CONFIG, 11, 101, config) == ACCEPT);
  expect(&f, 1, 9);
  CHECK(H264(&f, 0, 11, 101, idr) == UPDATE);
  expect(&f, 2, 11);
}

/* Fully received invalid suffixes must not commit an earlier valid VCL. */
static void test_malformed_annexb_is_transactional(void) {
  static const uint8_t bad[][16] = {
    {0x65,0xb8}, {9,0,0,1,0x65,0xb8}, {0,0,1},
    {0,0,1,0,0,1,0x65,0xb8}, {0,0,1,0x65},
    {0,0,1,0xe5,0xb8}, {0,0,1,0x60,0x80}, {0,0,1,0x78,0x80},
    {0,0,1,0x65,0xb8,0,0,1},
    {0,0,1,0x65,0xb8,0,0,1,0xe8,0x80},
    {0,0,1,0x65,0xb8,0,0,2,0x80},
    {0,0,1,0x65,0xb8,0,0,3},
    {0,0,1,0x65,0xb8,0,0,3,4}
  };
  static const size_t sizes[] = {2,6,3,8,4,5,5,5,8,10,9,8,9};
  Fixture f;
  for (size_t i = 0; i < sizeof(sizes)/sizeof(sizes[0]); ++i) {
    init(&f, 1);
    CHECK(H264(&f, 0, 1, 100, pframe) == UPDATE);
    CHECK(media(&f, &f.video, 1, 0, UINT32_MAX, 101, bad[i], sizes[i]) == REJECT);
    expect(&f, 1, 1);
    CHECK(H264(&f, 0, 2, 102, pframe) == UPDATE);
  }
}

static void test_legal_start_codes_padding_and_emulation_prevention(void) {
  Fixture f;
  const uint8_t padded[] = {0,0,0,0,1,0x65,0xb8,0,0,3,1,0x80,0,0};
  init(&f, 1);
  CHECK(H264(&f, 0, 1, 100, padded) == UPDATE);
  expect(&f, 1, 1);
}

/* Reader owns reassembly: only complete declared payload may change state. */
static void test_fragmented_payload_and_header_validation(void) {
  Fixture f;
  SourceClockHeader h;
  init(&f, 1);
  CHECK(H264(&f, CONFIG, 10, 100, config) == ACCEPT);
  h = header(1, 1, 0, 10, 100, sizeof(combined));
  for (size_t n = 0; n < sizeof(combined); ++n) {
    CHECK(source_clock_video_watermark_offer(&f.tracker, &f.video, &h, combined, n) == MORE);
    expect(&f, 0, 0);
  }
  CHECK(source_clock_video_watermark_offer(&f.tracker, &f.video, &h, combined, sizeof(combined)) == UPDATE);
  expect(&f, 1, 10);
  for (int field = 0; field < 7; ++field) {
    h = header(1, 1, 0, UINT32_MAX, 100, sizeof(idr));
    switch (field) {
      case 0: h.version = 0; break;
      case 1: h.header_bytes = 39; break;
      case 2: h.reserved = 1; break;
      case 3: h.payload_bytes = SOURCE_CLOCK_MAX_VIDEO_PAYLOAD + 1u; break;
      case 4: h.payload_bytes = 1; break;
      case 5: h.stream_kind = 99; break;
      case 6: h.codec = 99; break;
    }
    CHECK(source_clock_video_watermark_offer(&f.tracker, &f.video, &h, idr, sizeof(idr)) == REJECT);
    expect(&f, 1, 10);
  }
  h = header(1, 1, 0, 11, 101, 0);
  CHECK(source_clock_video_watermark_offer(&f.tracker, &f.video, &h, NULL, 0) == REJECT);
  h.payload_bytes = sizeof(idr);
  CHECK(source_clock_video_watermark_offer(&f.tracker, &f.video, &h, NULL, sizeof(idr)) == REJECT);
  CHECK(H264(&f, 0, 11, 101, pframe) == UPDATE);
}

int main(void) {
  test_payload_classification();
  test_vcl_types_and_non_vcl_high_water();
  test_bootstrap_pair_once();
  test_pair_requires_actual_idr_and_matching_ntp();
  test_duplicate_config_revokes_pair();
  test_only_bootstrap_config_arms_pair();
  test_intervening_frames_break_pair();
  test_h265_ignored();
  test_zero_gap_regression_and_ntp_not_ordering();
  test_max_pair_and_no_wrap_across_lifecycle();
  test_no_session_and_audio_cannot_supply_generation();
  test_generation_interleave_and_old_tuple_preserved();
  test_same_generation_reconnect_keeps_high_water();
  test_pair_cannot_cross_connection_or_session_start();
  test_end_invalidates_generation_not_final_tuple();
  test_malformed_annexb_is_transactional();
  test_legal_start_codes_padding_and_emulation_prevention();
  test_fragmented_payload_and_header_validation();
  puts("source_clock_video_watermark: 18 cases PASS");
  return 0;
}
