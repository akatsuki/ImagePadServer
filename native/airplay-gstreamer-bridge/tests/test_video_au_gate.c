#include "source_clock_protocol.h"
#include "video_au_gate.h"

#ifdef NDEBUG
#undef NDEBUG
#endif
#include <assert.h>

static const uint8_t idr[] = {0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
                              0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2,
                              0, 0, 0, 1, 0x65, 0x88, 0x84};
static const uint8_t pframe[] = {0, 0, 0, 1, 0x41, 0x9a, 0x20};
static const uint8_t changed_sps_idr[] = {0, 0, 0, 1, 0x67, 0x4d, 0, 0x20,
                                          0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2,
                                          0, 0, 0, 1, 0x65, 0x88, 0x84};
static const uint8_t changed_sps_only[] = {0, 0, 0, 1, 0x67, 0x64, 0, 0x20};
static const uint8_t h265_idr[] = {0, 0, 0, 1, 0x40, 0x01, 0xaa,
                                   0, 0, 0, 1, 0x42, 0x01, 0xbb,
                                   0, 0, 0, 1, 0x44, 0x01, 0xcc,
                                   0, 0, 0, 1, 0x26, 0x01, 0xdd};
static const uint8_t h265_pframe[] = {0, 0, 0, 1, 0x02, 0x01, 0xee};

static void test_config_only_change_notifies_next_idr(void) {
  const uint8_t config_a[] = {0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
                              0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2};
  const uint8_t config_b[] = {0, 0, 0, 1, 0x67, 0x4d, 0, 0x20,
                              0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2};
  const uint8_t bare_idr[] = {0, 0, 0, 1, 0x65, 0x88, 0x84};
  const uint8_t hevc_config_a[] = {0, 0, 0, 1, 0x40, 0x01, 0xaa,
                                   0, 0, 0, 1, 0x42, 0x01, 0xbb,
                                   0, 0, 0, 1, 0x44, 0x01, 0xcc};
  const uint8_t hevc_config_b[] = {0, 0, 0, 1, 0x40, 0x01, 0xab,
                                   0, 0, 0, 1, 0x42, 0x01, 0xbc,
                                   0, 0, 0, 1, 0x44, 0x01, 0xcd};
  const uint8_t hevc_bare_idr[] = {0, 0, 0, 1, 0x26, 0x01, 0xdd};
  const struct {
    uint8_t codec;
    const uint8_t *config_a, *config_b, *keyframe, *delta;
    size_t config_bytes, keyframe_bytes, delta_bytes;
  } cases[] = {
      {SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, config_a, config_b, bare_idr, pframe,
       sizeof(config_a), sizeof(bare_idr), sizeof(pframe)},
      {SOURCE_CLOCK_CODEC_H265_ANNEXB_AU, hevc_config_a, hevc_config_b,
       hevc_bare_idr, h265_pframe, sizeof(hevc_config_a), sizeof(hevc_bare_idr),
       sizeof(h265_pframe)},
  };
  for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); ++i) {
    SourceClockVideoAUGate gate;
    source_clock_video_au_gate_init(&gate);
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].config_a, cases[i].config_bytes, SOURCE_CLOCK_FLAG_CONFIG) ==
        SOURCE_CLOCK_VIDEO_AU_ACCEPT);
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].keyframe, cases[i].keyframe_bytes, SOURCE_CLOCK_FLAG_KEYFRAME) ==
        SOURCE_CLOCK_VIDEO_AU_ACCEPT);
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].config_b, cases[i].config_bytes, SOURCE_CLOCK_FLAG_CONFIG) ==
        SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE);
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].keyframe, cases[i].keyframe_bytes, SOURCE_CLOCK_FLAG_KEYFRAME) ==
        SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE);
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].delta, cases[i].delta_bytes, 0) == SOURCE_CLOCK_VIDEO_AU_ACCEPT);

    /* Repeated CONFIG and rejected P-frames must not consume the notification. */
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].config_a, cases[i].config_bytes, SOURCE_CLOCK_FLAG_CONFIG) ==
        SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE);
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].config_a, cases[i].config_bytes, SOURCE_CLOCK_FLAG_CONFIG) ==
        SOURCE_CLOCK_VIDEO_AU_DROP);
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].delta, cases[i].delta_bytes, 0) == SOURCE_CLOCK_VIDEO_AU_DROP);
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].keyframe, cases[i].keyframe_bytes, SOURCE_CLOCK_FLAG_KEYFRAME) ==
        SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE);
    assert(source_clock_video_au_gate_offer(&gate, cases[i].codec,
        cases[i].delta, cases[i].delta_bytes, 0) == SOURCE_CLOCK_VIDEO_AU_ACCEPT);
  }
}

int main(void) {
  test_config_only_change_notifies_next_idr();
  SourceClockVideoAUGate gate;
  source_clock_video_au_gate_init(&gate);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, idr, sizeof(idr),
                                           SOURCE_CLOCK_FLAG_KEYFRAME) == SOURCE_CLOCK_VIDEO_AU_ACCEPT);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, pframe, sizeof(pframe), 0) == SOURCE_CLOCK_VIDEO_AU_ACCEPT);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, changed_sps_idr, sizeof(changed_sps_idr),
                                           SOURCE_CLOCK_FLAG_KEYFRAME) == SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, pframe, sizeof(pframe), 0) == SOURCE_CLOCK_VIDEO_AU_ACCEPT);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, changed_sps_only, sizeof(changed_sps_only), 0) ==
         SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, pframe, sizeof(pframe), 0) == SOURCE_CLOCK_VIDEO_AU_DROP);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, changed_sps_idr, sizeof(changed_sps_idr),
                                           SOURCE_CLOCK_FLAG_KEYFRAME) == SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, pframe, sizeof(pframe), 0) == SOURCE_CLOCK_VIDEO_AU_ACCEPT);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, pframe, sizeof(pframe) - 1,
                                           SOURCE_CLOCK_FLAG_DISCONTINUITY) == SOURCE_CLOCK_VIDEO_AU_DROP);

  source_clock_video_au_gate_init(&gate);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H265_ANNEXB_AU,
                                           h265_idr, sizeof(h265_idr),
                                           SOURCE_CLOCK_FLAG_KEYFRAME) == SOURCE_CLOCK_VIDEO_AU_ACCEPT);
  assert(source_clock_video_au_gate_offer(&gate, SOURCE_CLOCK_CODEC_H265_ANNEXB_AU,
                                           h265_pframe, sizeof(h265_pframe), 0) ==
         SOURCE_CLOCK_VIDEO_AU_ACCEPT);
  return 0;
}
