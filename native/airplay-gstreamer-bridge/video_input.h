#ifndef IMAGEPAD_VIDEO_INPUT_H
#define IMAGEPAD_VIDEO_INPUT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

typedef struct SourceClockVideoBootstrap {
  uint8_t *configuration;
  size_t configuration_bytes;
  /* Keep each codec parameter-set independently.  A CONFIG AU may contain
   * only SPS (or only PPS); replacing one set must not discard the others. */
  uint8_t *vps;
  size_t vps_bytes;
  uint8_t *sps;
  size_t sps_bytes;
  uint8_t *pps;
  size_t pps_bytes;
  uint8_t codec;
} SourceClockVideoBootstrap;

const char *source_clock_video_caps_string(uint8_t codec);

void source_clock_video_bootstrap_init(SourceClockVideoBootstrap *bootstrap);
void source_clock_video_bootstrap_clear(SourceClockVideoBootstrap *bootstrap);
bool source_clock_video_bootstrap_prepare(SourceClockVideoBootstrap *bootstrap,
                                          uint8_t codec, uint16_t flags,
                                          const uint8_t *payload, size_t payload_bytes,
                                          const uint8_t **out_payload, size_t *out_payload_bytes,
                                          uint8_t **out_owned_payload);

#endif
