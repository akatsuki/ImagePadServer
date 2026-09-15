#include "video_au_gate.h"

#include "source_clock_protocol.h"

#include <string.h>

static uint32_t fnv1a(const uint8_t *data, size_t length) {
  uint32_t hash = 2166136261u;
  for (size_t i = 0; i < length; ++i) {
    hash ^= data[i];
    hash *= 16777619u;
  }
  return hash;
}

static bool next_start_code(const uint8_t *data, size_t length, size_t from, size_t *start,
                            size_t *code_bytes) {
  for (size_t i = from; i + 3 <= length; ++i) {
    if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 1) {
      *start = i;
      *code_bytes = 3;
      return true;
    }
    if (i + 4 <= length && data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 0 &&
        data[i + 3] == 1) {
      *start = i;
      *code_bytes = 4;
      return true;
    }
  }
  return false;
}

void source_clock_video_au_gate_init(SourceClockVideoAUGate *gate) {
  if (gate != NULL) memset(gate, 0, sizeof(*gate));
}

void source_clock_video_au_gate_mark_loss(SourceClockVideoAUGate *gate) {
  if (gate == NULL) return;
  gate->waiting_for_idr = true;
  gate->format_change_pending = true;
}

SourceClockVideoAUResult source_clock_video_au_gate_offer(SourceClockVideoAUGate *gate,
                                                          uint8_t codec,
                                                          const uint8_t *payload,
                                                          size_t payload_bytes,
                                                          uint16_t flags) {
  size_t cursor = 0;
  bool has_vcl = false;
  bool has_idr = false;
  bool has_vps = false;
  bool has_sps = false;
  bool has_pps = false;
  uint32_t vps_hash = 0;
  uint32_t sps_hash = 0;
  uint32_t pps_hash = 0;
#define DROP_LOSS() do { if (gate != NULL) gate->waiting_for_idr = true; return SOURCE_CLOCK_VIDEO_AU_DROP; } while (0)
  if (gate == NULL || payload == NULL || payload_bytes == 0 ||
      (codec != SOURCE_CLOCK_CODEC_H264_ANNEXB_AU &&
       codec != SOURCE_CLOCK_CODEC_H265_ANNEXB_AU)) {
    DROP_LOSS();
  }
  if (gate->codec != 0 && gate->codec != codec) source_clock_video_au_gate_init(gate);
  gate->codec = codec;
  while (cursor < payload_bytes) {
    size_t start;
    size_t code_bytes;
    size_t next;
    if (!next_start_code(payload, payload_bytes, cursor, &start, &code_bytes)) {
      DROP_LOSS();
    }
    next = start + code_bytes;
    if (next >= payload_bytes) DROP_LOSS();
    size_t end = payload_bytes;
    size_t next_start;
    size_t next_code;
    if (next_start_code(payload, payload_bytes, next, &next_start, &next_code)) end = next_start;
    if (end <= next) DROP_LOSS();
    uint8_t type = codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU
                       ? (uint8_t)((payload[next] >> 1) & 0x3f)
                       : (uint8_t)(payload[next] & 0x1f);
    /* A NAL header alone is not a decodable random-access point. */
    if ((codec == SOURCE_CLOCK_CODEC_H264_ANNEXB_AU && type == 5 && end - next < 2) ||
        (codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU &&
         (type == 19 || type == 20 || type == 21) && end - next < 3)) DROP_LOSS();
    if ((codec == SOURCE_CLOCK_CODEC_H264_ANNEXB_AU && type == 5) ||
        (codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU &&
         (type == 19 || type == 20 || type == 21))) {
      has_vcl = true;
      has_idr = true;
    } else if ((codec == SOURCE_CLOCK_CODEC_H264_ANNEXB_AU && type >= 1 && type <= 5) ||
               (codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU && type <= 31)) {
      has_vcl = true;
    } else if (codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU && type == 32) {
      has_vps = true;
      vps_hash = fnv1a(payload + next, end - next);
    } else if ((codec == SOURCE_CLOCK_CODEC_H264_ANNEXB_AU && type == 7) ||
               (codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU && type == 33)) {
      has_sps = true;
      sps_hash = fnv1a(payload + next, end - next);
    } else if ((codec == SOURCE_CLOCK_CODEC_H264_ANNEXB_AU && type == 8) ||
               (codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU && type == 34)) {
      has_pps = true;
      pps_hash = fnv1a(payload + next, end - next);
    }
    cursor = end;
  }
  if ((flags & SOURCE_CLOCK_FLAG_DISCONTINUITY) != 0) gate->waiting_for_idr = true;
  if (!has_vcl && !has_vps && !has_sps && !has_pps) DROP_LOSS();
  bool format_change = gate->initialized &&
                       ((has_vps && gate->has_vps && vps_hash != gate->vps_hash) ||
                       (has_sps && gate->has_sps && sps_hash != gate->sps_hash) ||
                       (has_pps && gate->has_pps && pps_hash != gate->pps_hash));
  if (has_vps) {
    gate->has_vps = true;
    gate->vps_hash = vps_hash;
  }
  if (has_sps) {
    gate->has_sps = true;
    gate->sps_hash = sps_hash;
  }
  if (has_pps) {
    gate->has_pps = true;
    gate->pps_hash = pps_hash;
  }
  if (format_change) {
    /* A format-change AU containing an IDR is immediately decodable. Keep
     * accepting following P-frames; only a format-change without an IDR must
     * wait for a later random access point. */
    gate->waiting_for_idr = !has_idr;
    gate->format_change_pending = !has_idr;
    if (has_idr) gate->initialized = true;
    return SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE;
  }
  if (has_idr) {
    gate->waiting_for_idr = false;
    gate->initialized = true;
    /* CONFIG-only notification must also reach the recovery IDR, even when
     * its parameter sets already match the hashes saved by that CONFIG. */
    if (gate->format_change_pending) {
      gate->format_change_pending = false;
      return SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE;
    }
    return SOURCE_CLOCK_VIDEO_AU_ACCEPT;
  }
  if (gate->waiting_for_idr || (has_vcl && !gate->initialized)) {
    return SOURCE_CLOCK_VIDEO_AU_DROP;
  }
  return SOURCE_CLOCK_VIDEO_AU_ACCEPT;
#undef DROP_LOSS
}
