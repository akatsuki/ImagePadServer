#ifndef IMAGEPAD_VIDEO_AU_GATE_H
#define IMAGEPAD_VIDEO_AU_GATE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

typedef enum SourceClockVideoAUResult {
  SOURCE_CLOCK_VIDEO_AU_DROP = 0,
  SOURCE_CLOCK_VIDEO_AU_ACCEPT = 1,
  SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE = 2,
} SourceClockVideoAUResult;

typedef struct SourceClockVideoAUGate {
  bool waiting_for_idr;
  bool format_change_pending;
  bool initialized;
  bool has_vps;
  bool has_sps;
  bool has_pps;
  uint8_t codec;
  uint32_t vps_hash;
  uint32_t sps_hash;
  uint32_t pps_hash;
} SourceClockVideoAUGate;

void source_clock_video_au_gate_init(SourceClockVideoAUGate *gate);
/* Mark a loss that happened after AU parsing (for example an appsrc push
 * failure). The next video access unit must be a clean IDR. */
void source_clock_video_au_gate_mark_loss(SourceClockVideoAUGate *gate);
SourceClockVideoAUResult source_clock_video_au_gate_offer(SourceClockVideoAUGate *gate,
                                                          uint8_t codec,
                                                          const uint8_t *payload,
                                                          size_t payload_bytes,
                                                          uint16_t flags);

#endif
