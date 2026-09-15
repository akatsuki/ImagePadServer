#ifndef IMAGEPAD_SOURCE_CLOCK_TIMELINE_H
#define IMAGEPAD_SOURCE_CLOCK_TIMELINE_H

#include "source_clock_protocol.h"

#include <stdbool.h>
#include <stdint.h>

#define SOURCE_CLOCK_WARMUP_NS 500000000ULL
#define SOURCE_CLOCK_OUTPUT_LEAD_NS 100000000ULL
#define SOURCE_CLOCK_LATE_DROP_NS 100000000ULL
#define SOURCE_CLOCK_MAX_CLOCK_MISMATCH_NS 1000000000ULL

typedef enum SourceClockSessionResult {
  SOURCE_CLOCK_SESSION_DUPLICATE = 0,
  SOURCE_CLOCK_SESSION_NEW = 1,
} SourceClockSessionResult;

typedef enum SourceClockMapResult {
  SOURCE_CLOCK_MAPPED = 0,
  SOURCE_CLOCK_DROP_LATE = 1,
  SOURCE_CLOCK_RESET_EPOCH = 2,
  SOURCE_CLOCK_NOT_READY = 3,
} SourceClockMapResult;

typedef struct SourceClockTimeline {
  uint64_t active_generation;
  bool has_generation;
  bool has_first_audio;
  bool has_first_video;
  bool ready;
  uint64_t first_audio_ntp_ns;
  uint64_t first_video_ntp_ns;
  uint64_t warmup_start_arrival_ns;
  uint64_t source_origin_ns;
  uint64_t output_origin_ns;
  uint64_t last_mapped_ns;
  bool has_last_mapped;
  uint64_t last_audio_mapped_ns;
  uint64_t last_video_mapped_ns;
  bool has_last_audio_mapped;
  bool has_last_video_mapped;
  bool has_last_audio;
  bool has_last_video;
  uint64_t last_audio_ntp_ns;
  uint64_t last_video_ntp_ns;
  uint64_t last_audio_arrival_ns;
  uint64_t last_video_arrival_ns;
} SourceClockTimeline;

void source_clock_timeline_init(SourceClockTimeline *timeline);
SourceClockSessionResult source_clock_timeline_session_start(SourceClockTimeline *timeline,
                                                             uint64_t generation);
void source_clock_timeline_offer_first(SourceClockTimeline *timeline, uint8_t stream_kind,
                                       uint64_t ntp_ns, uint64_t arrival_ns);
bool source_clock_timeline_ready(SourceClockTimeline *timeline, uint64_t now_running_ns);
/* Only video anomalies reset an established shared epoch. Dropped audio leaves
 * the origins and last accepted source/arrival/mapped state unchanged.
 * A new SESSION_START generation still permits warmup to establish a new epoch. */
SourceClockMapResult source_clock_timeline_map(SourceClockTimeline *timeline, uint8_t stream_kind,
                                               uint64_t ntp_ns, uint64_t arrival_ns,
                                               uint64_t now_running_ns, uint64_t *mapped_ns);
/* Project a warmup AU into the already established epoch without applying
 * live late-drop/floor mutation. The caller must keep the AU bounded and
 * mark it decode-only before pushing it to the decoder. */
SourceClockMapResult source_clock_timeline_project_history(const SourceClockTimeline *timeline,
                                                           uint8_t stream_kind,
                                                           uint64_t ntp_ns,
                                                           uint64_t *mapped_ns);

#endif
