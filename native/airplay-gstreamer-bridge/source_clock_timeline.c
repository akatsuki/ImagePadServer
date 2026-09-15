#include "source_clock_timeline.h"

#include <string.h>

static bool add_overflow_u64(uint64_t left, uint64_t right, uint64_t *out) {
  if (UINT64_MAX - left < right) {
    return true;
  }
  *out = left + right;
  return false;
}

static uint64_t abs_delta_u64(uint64_t left, uint64_t right) {
  return left >= right ? left - right : right - left;
}

static bool valid_media_kind(uint8_t stream_kind) {
  return stream_kind == SOURCE_CLOCK_STREAM_AUDIO || stream_kind == SOURCE_CLOCK_STREAM_VIDEO;
}

void source_clock_timeline_init(SourceClockTimeline *timeline) {
  if (timeline != NULL) {
    memset(timeline, 0, sizeof(*timeline));
  }
}

SourceClockSessionResult source_clock_timeline_session_start(SourceClockTimeline *timeline,
                                                             uint64_t generation) {
  if (timeline == NULL) {
    return SOURCE_CLOCK_SESSION_NEW;
  }
  if (timeline->has_generation && timeline->active_generation == generation) {
    return SOURCE_CLOCK_SESSION_DUPLICATE;
  }
  timeline->active_generation = generation;
  timeline->has_generation = true;
  timeline->has_first_audio = false;
  timeline->has_first_video = false;
  timeline->ready = false;
  timeline->has_last_audio = false;
  timeline->has_last_video = false;
  timeline->has_last_audio_mapped = false;
  timeline->has_last_video_mapped = false;
  return SOURCE_CLOCK_SESSION_NEW;
}

void source_clock_timeline_offer_first(SourceClockTimeline *timeline, uint8_t stream_kind,
                                       uint64_t ntp_ns, uint64_t arrival_ns) {
  if (timeline == NULL || !valid_media_kind(stream_kind)) {
    return;
  }
  if (!timeline->has_first_audio && stream_kind == SOURCE_CLOCK_STREAM_AUDIO) {
    timeline->has_first_audio = true;
    timeline->first_audio_ntp_ns = ntp_ns;
    timeline->warmup_start_arrival_ns = arrival_ns;
  }
  if (!timeline->has_first_video && stream_kind == SOURCE_CLOCK_STREAM_VIDEO) {
    timeline->has_first_video = true;
    timeline->first_video_ntp_ns = ntp_ns;
    if (timeline->warmup_start_arrival_ns == 0 || arrival_ns < timeline->warmup_start_arrival_ns) {
      timeline->warmup_start_arrival_ns = arrival_ns;
    }
  }
}

bool source_clock_timeline_ready(SourceClockTimeline *timeline, uint64_t now_running_ns) {
  if (timeline == NULL || (!timeline->has_first_audio && !timeline->has_first_video)) {
    return false;
  }
  if (timeline->ready) {
    return true;
  }
  uint64_t elapsed = now_running_ns >= timeline->warmup_start_arrival_ns
                         ? now_running_ns - timeline->warmup_start_arrival_ns
                         : 0;
  if (!timeline->has_first_audio || !timeline->has_first_video) {
    if (elapsed < SOURCE_CLOCK_WARMUP_NS) {
      return false;
    }
  }
  if (timeline->has_first_audio && timeline->has_first_video) {
    timeline->source_origin_ns = timeline->first_audio_ntp_ns < timeline->first_video_ntp_ns
                                     ? timeline->first_audio_ntp_ns
                                     : timeline->first_video_ntp_ns;
  } else if (timeline->has_first_audio) {
    timeline->source_origin_ns = timeline->first_audio_ntp_ns;
  } else {
    timeline->source_origin_ns = timeline->first_video_ntp_ns;
  }
  uint64_t output_origin = 0;
  if (add_overflow_u64(now_running_ns, SOURCE_CLOCK_OUTPUT_LEAD_NS, &output_origin)) {
    output_origin = UINT64_MAX;
  }
  if (timeline->has_last_mapped && output_origin <= timeline->last_mapped_ns) {
    output_origin = timeline->last_mapped_ns == UINT64_MAX ? UINT64_MAX : timeline->last_mapped_ns + 1;
  }
  timeline->output_origin_ns = output_origin;
  timeline->ready = true;
  return true;
}

static SourceClockMapResult reset_epoch(SourceClockTimeline *timeline, uint64_t ntp_ns,
                                         uint64_t now_running_ns, uint64_t *mapped_ns) {
  uint64_t output_origin = 0;
  if (add_overflow_u64(now_running_ns, SOURCE_CLOCK_OUTPUT_LEAD_NS, &output_origin)) {
    output_origin = UINT64_MAX;
  }
  if (timeline->has_last_mapped && output_origin <= timeline->last_mapped_ns) {
    output_origin = timeline->last_mapped_ns == UINT64_MAX ? UINT64_MAX : timeline->last_mapped_ns + 1;
  }
  timeline->source_origin_ns = ntp_ns;
  timeline->output_origin_ns = output_origin;
  timeline->has_last_mapped = true;
  timeline->last_mapped_ns = output_origin;
  if (mapped_ns != NULL) {
    *mapped_ns = output_origin;
  }
  return SOURCE_CLOCK_RESET_EPOCH;
}

SourceClockMapResult source_clock_timeline_map(SourceClockTimeline *timeline, uint8_t stream_kind,
                                               uint64_t ntp_ns, uint64_t arrival_ns,
                                               uint64_t now_running_ns, uint64_t *mapped_ns) {
  if (timeline == NULL || !timeline->ready || !valid_media_kind(stream_kind)) {
    return SOURCE_CLOCK_NOT_READY;
  }
  bool is_audio = stream_kind == SOURCE_CLOCK_STREAM_AUDIO;
  bool *has_last = stream_kind == SOURCE_CLOCK_STREAM_AUDIO ? &timeline->has_last_audio : &timeline->has_last_video;
  uint64_t *last_ntp = stream_kind == SOURCE_CLOCK_STREAM_AUDIO ? &timeline->last_audio_ntp_ns : &timeline->last_video_ntp_ns;
  uint64_t *last_arrival = stream_kind == SOURCE_CLOCK_STREAM_AUDIO ? &timeline->last_audio_arrival_ns : &timeline->last_video_arrival_ns;
  bool *has_last_mapped = stream_kind == SOURCE_CLOCK_STREAM_AUDIO
                              ? &timeline->has_last_audio_mapped
                              : &timeline->has_last_video_mapped;
  uint64_t *last_mapped = stream_kind == SOURCE_CLOCK_STREAM_AUDIO
                              ? &timeline->last_audio_mapped_ns
                              : &timeline->last_video_mapped_ns;
  if (*has_last) {
    /* A delayed packet is not a source-clock reset.  Keep the last accepted
     * source point so a late frame cannot move the shared epoch backwards or
     * cause the other stream to be re-clocked. */
    if (ntp_ns < *last_ntp && *last_ntp - ntp_ns > SOURCE_CLOCK_LATE_DROP_NS) {
      return SOURCE_CLOCK_DROP_LATE;
    }
    uint64_t source_delta = abs_delta_u64(ntp_ns, *last_ntp);
    uint64_t arrival_delta = abs_delta_u64(arrival_ns, *last_arrival);
    if (abs_delta_u64(source_delta, arrival_delta) > SOURCE_CLOCK_MAX_CLOCK_MISMATCH_NS) {
      if (is_audio) {
        return SOURCE_CLOCK_DROP_LATE;
      }
      *last_ntp = ntp_ns;
      *last_arrival = arrival_ns;
      *has_last = true;
      return reset_epoch(timeline, ntp_ns, now_running_ns, mapped_ns);
    }
  }
  /* Preserve the video reset/recovery path. Audio must pass every drop check
   * before committing its last accepted source point or mapped floor. */
  if (!is_audio) {
    *last_ntp = ntp_ns;
    *last_arrival = arrival_ns;
    *has_last = true;
  }
  if (ntp_ns < timeline->source_origin_ns) {
    if (is_audio) {
      return SOURCE_CLOCK_DROP_LATE;
    }
    return reset_epoch(timeline, ntp_ns, now_running_ns, mapped_ns);
  }
  uint64_t delta = ntp_ns - timeline->source_origin_ns;
  uint64_t mapped = 0;
  if (add_overflow_u64(timeline->output_origin_ns, delta, &mapped)) {
    if (is_audio) {
      return SOURCE_CLOCK_DROP_LATE;
    }
    return reset_epoch(timeline, ntp_ns, now_running_ns, mapped_ns);
  }
  if (mapped_ns != NULL) {
    *mapped_ns = mapped;
  }
  if (*has_last_mapped && mapped < *last_mapped) {
    return SOURCE_CLOCK_DROP_LATE;
  }
  if (is_audio && now_running_ns > mapped && now_running_ns - mapped > SOURCE_CLOCK_LATE_DROP_NS) {
    return SOURCE_CLOCK_DROP_LATE;
  }
  *has_last_mapped = true;
  *last_mapped = mapped;
  timeline->has_last_mapped = true;
  if (mapped > timeline->last_mapped_ns) {
    timeline->last_mapped_ns = mapped;
  }
  if (now_running_ns > mapped && now_running_ns - mapped > SOURCE_CLOCK_LATE_DROP_NS) {
    return SOURCE_CLOCK_DROP_LATE;
  }
  if (is_audio) {
    *last_ntp = ntp_ns;
    *last_arrival = arrival_ns;
    *has_last = true;
  }
  return SOURCE_CLOCK_MAPPED;
}

SourceClockMapResult source_clock_timeline_project_history(const SourceClockTimeline *timeline,
                                                           uint8_t stream_kind,
                                                           uint64_t ntp_ns,
                                                           uint64_t *mapped_ns) {
  uint64_t delta;
  uint64_t mapped;
  if (timeline == NULL || !timeline->ready || !valid_media_kind(stream_kind))
    return SOURCE_CLOCK_NOT_READY;
  if (ntp_ns < timeline->source_origin_ns) return SOURCE_CLOCK_DROP_LATE;
  delta = ntp_ns - timeline->source_origin_ns;
  if (add_overflow_u64(timeline->output_origin_ns, delta, &mapped))
    return SOURCE_CLOCK_DROP_LATE;
  if (mapped_ns != NULL) *mapped_ns = mapped;
  return SOURCE_CLOCK_MAPPED;
}
