#include "video_input.h"

#include "source_clock_protocol.h"

#include <stdlib.h>
#include <string.h>

static bool is_start_code(const uint8_t *p, size_t n, size_t i, size_t *bytes) {
  if (p == NULL || bytes == NULL) return false;
  if (i + 3 <= n && p[i] == 0 && p[i + 1] == 0 && p[i + 2] == 1) {
    *bytes = 3;
    return true;
  }
  if (i + 4 <= n && p[i] == 0 && p[i + 1] == 0 && p[i + 2] == 0 && p[i + 3] == 1) {
    *bytes = 4;
    return true;
  }
  return false;
}

static int configuration_kind(uint8_t codec, const uint8_t *nal, size_t bytes) {
  if (nal == NULL || bytes == 0) return 0;
  uint8_t type = codec == SOURCE_CLOCK_CODEC_H264_ANNEXB_AU
                     ? (uint8_t)(nal[0] & 31)
                     : (uint8_t)((nal[0] >> 1) & 63);
  if (codec == SOURCE_CLOCK_CODEC_H264_ANNEXB_AU) {
    return type == 7 ? 2 : type == 8 ? 3 : 0; /* SPS, PPS */
  }
  return type == 32 ? 1 : type == 33 ? 2 : type == 34 ? 3 : 0; /* VPS/SPS/PPS */
}

static bool find_next_start_code(const uint8_t *payload, size_t bytes, size_t from,
                                 size_t *start, size_t *code_bytes) {
  if (payload == NULL || start == NULL || code_bytes == NULL) return false;
  for (size_t i = from; i < bytes; ++i) {
    if (is_start_code(payload, bytes, i, code_bytes)) {
      *start = i;
      return true;
    }
  }
  return false;
}

static void free_slot(uint8_t **slot, size_t *slot_bytes) {
  if (slot == NULL || slot_bytes == NULL) return;
  free(*slot);
  *slot = NULL;
  *slot_bytes = 0;
}

static bool append_bytes(uint8_t *out, size_t capacity, size_t *used,
                         const uint8_t *data, size_t bytes) {
  if (bytes == 0) return true;
  if (out == NULL || used == NULL || data == NULL || *used > capacity ||
      bytes > capacity - *used) return false;
  memcpy(out + *used, data, bytes);
  *used += bytes;
  return true;
}

static bool rebuild_configuration(SourceClockVideoBootstrap *bootstrap) {
  if (bootstrap == NULL || bootstrap->vps_bytes > SIZE_MAX - bootstrap->sps_bytes ||
      bootstrap->vps_bytes + bootstrap->sps_bytes > SIZE_MAX - bootstrap->pps_bytes) {
    return false;
  }
  size_t total = bootstrap->vps_bytes + bootstrap->sps_bytes + bootstrap->pps_bytes;
  if (total > SOURCE_CLOCK_MAX_VIDEO_PAYLOAD) return false;
  uint8_t *combined = NULL;
  if (total != 0) {
    combined = (uint8_t *)malloc(total);
    if (combined == NULL) return false;
    size_t used = 0;
    if (!append_bytes(combined, total, &used, bootstrap->vps, bootstrap->vps_bytes) ||
        !append_bytes(combined, total, &used, bootstrap->sps, bootstrap->sps_bytes) ||
        !append_bytes(combined, total, &used, bootstrap->pps, bootstrap->pps_bytes)) {
      free(combined);
      return false;
    }
  }
  free(bootstrap->configuration);
  bootstrap->configuration = combined;
  bootstrap->configuration_bytes = total;
  return true;
}

const char *source_clock_video_caps_string(uint8_t codec) {
  if (codec == SOURCE_CLOCK_CODEC_H264_ANNEXB_AU) {
    return "video/x-h264,stream-format=(string)byte-stream,alignment=(string)au";
  }
  if (codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU) {
    return "video/x-h265,stream-format=(string)byte-stream,alignment=(string)au";
  }
  return NULL;
}

void source_clock_video_bootstrap_init(SourceClockVideoBootstrap *bootstrap) {
  if (bootstrap != NULL) memset(bootstrap, 0, sizeof(*bootstrap));
}

void source_clock_video_bootstrap_clear(SourceClockVideoBootstrap *bootstrap) {
  if (bootstrap == NULL) return;
  free(bootstrap->configuration);
  free_slot(&bootstrap->vps, &bootstrap->vps_bytes);
  free_slot(&bootstrap->sps, &bootstrap->sps_bytes);
  free_slot(&bootstrap->pps, &bootstrap->pps_bytes);
  memset(bootstrap, 0, sizeof(*bootstrap));
}

static bool cache_configuration(SourceClockVideoBootstrap *bootstrap, uint8_t codec,
                                const uint8_t *payload, size_t payload_bytes) {
  uint8_t *next_vps = NULL, *next_sps = NULL, *next_pps = NULL;
  size_t next_vps_bytes = 0, next_sps_bytes = 0, next_pps_bytes = 0;
  bool found = false;
  size_t cursor = 0;
  while (cursor < payload_bytes) {
    size_t start = 0, code_bytes = 0;
    if (!find_next_start_code(payload, payload_bytes, cursor, &start, &code_bytes)) goto fail;
    size_t nal = start + code_bytes;
    if (nal >= payload_bytes) goto fail;
    size_t end = payload_bytes, next_start = 0, next_code = 0;
    if (find_next_start_code(payload, payload_bytes, nal, &next_start, &next_code)) end = next_start;
    if (end <= nal) goto fail;
    int kind = configuration_kind(codec, payload + nal, end - nal);
    if (kind != 0) {
      uint8_t **slot = kind == 1 ? &next_vps : kind == 2 ? &next_sps : &next_pps;
      size_t *slot_bytes = kind == 1 ? &next_vps_bytes : kind == 2 ? &next_sps_bytes : &next_pps_bytes;
      size_t bytes = end - start;
      uint8_t *copy = (uint8_t *)malloc(bytes);
      if (copy == NULL) goto fail;
      memcpy(copy, payload + start, bytes);
      free(*slot);
      *slot = copy;
      *slot_bytes = bytes;
      found = true;
    }
    cursor = end;
  }
  if (!found) goto fail;

  /* Commit only after every NAL was parsed and allocated successfully. */
  if (next_vps != NULL) { free_slot(&bootstrap->vps, &bootstrap->vps_bytes); bootstrap->vps = next_vps; bootstrap->vps_bytes = next_vps_bytes; next_vps = NULL; }
  if (next_sps != NULL) { free_slot(&bootstrap->sps, &bootstrap->sps_bytes); bootstrap->sps = next_sps; bootstrap->sps_bytes = next_sps_bytes; next_sps = NULL; }
  if (next_pps != NULL) { free_slot(&bootstrap->pps, &bootstrap->pps_bytes); bootstrap->pps = next_pps; bootstrap->pps_bytes = next_pps_bytes; next_pps = NULL; }
  return rebuild_configuration(bootstrap);

fail:
  free(next_vps);
  free(next_sps);
  free(next_pps);
  return false;
}

bool source_clock_video_bootstrap_prepare(SourceClockVideoBootstrap *bootstrap,
                                          uint8_t codec, uint16_t flags,
                                          const uint8_t *payload, size_t payload_bytes,
                                          const uint8_t **out_payload, size_t *out_payload_bytes,
                                          uint8_t **out_owned_payload) {
  if (bootstrap == NULL || payload == NULL || payload_bytes == 0 ||
      out_payload == NULL || out_payload_bytes == NULL || out_owned_payload == NULL ||
      source_clock_video_caps_string(codec) == NULL) return false;
  *out_payload = payload;
  *out_payload_bytes = payload_bytes;
  *out_owned_payload = NULL;

  if ((flags & SOURCE_CLOCK_FLAG_CONFIG) != 0) {
    if (bootstrap->codec != 0 && bootstrap->codec != codec) source_clock_video_bootstrap_clear(bootstrap);
    if (!cache_configuration(bootstrap, codec, payload, payload_bytes)) return false;
    bootstrap->codec = codec;
    /* CONFIG+IDR already carries the complete access unit. */
    if ((flags & SOURCE_CLOCK_FLAG_KEYFRAME) == 0) return true;
  }
  if ((flags & SOURCE_CLOCK_FLAG_KEYFRAME) != 0 &&
      (flags & SOURCE_CLOCK_FLAG_CONFIG) == 0 && bootstrap->configuration != NULL &&
      bootstrap->configuration_bytes != 0 && bootstrap->codec == codec) {
    if (bootstrap->configuration_bytes > SIZE_MAX - payload_bytes) return false;
    uint8_t *copy = (uint8_t *)malloc(bootstrap->configuration_bytes + payload_bytes);
    if (copy == NULL) return false;
    memcpy(copy, bootstrap->configuration, bootstrap->configuration_bytes);
    memcpy(copy + bootstrap->configuration_bytes, payload, payload_bytes);
    *out_payload = copy;
    *out_payload_bytes = bootstrap->configuration_bytes + payload_bytes;
    *out_owned_payload = copy;
  } else if (bootstrap->codec != 0 && bootstrap->codec != codec) {
    source_clock_video_bootstrap_clear(bootstrap);
  }
  return true;
}
