#include "source_clock_video_watermark.h"

#include <string.h>

typedef struct H264Classification {
  bool vcl;
  bool idr;
  bool sps;
  bool pps;
  bool parameter_sets_only;
} H264Classification;

static bool start_code(const uint8_t *data, size_t size, size_t from,
                       size_t *start, size_t *nal) {
  for (size_t i = from; i < size && size - i >= 3; ++i) {
    if (data[i] != 0 || data[i + 1] != 0) continue;
    if (data[i + 2] == 1) {
      *start = i;
      *nal = i + 3;
      return true;
    }
    if (size - i >= 4 && data[i + 2] == 0 && data[i + 3] == 1) {
      *start = i;
      *nal = i + 4;
      return true;
    }
  }
  return false;
}

static bool valid_nal(const uint8_t *data, size_t size) {
  unsigned zeros = 0;
  uint8_t type;
  if (size < 2 || (data[0] & 0x80) != 0) return false;
  type = data[0] & 0x1f;
  /* 0 and packetization-only types are not elementary Annex-B NAL units. */
  if (type == 0 || type >= 24) return false;
  for (size_t i = 1; i < size; ++i) {
    if (zeros == 2) {
      if (data[i] <= 2) return false;
      if (data[i] == 3) {
        if (i + 1 == size || data[i + 1] > 3) return false;
        zeros = 0;
        continue;
      }
    }
    zeros = data[i] == 0 ? zeros + 1 : 0;
  }
  return true;
}

static bool classify_h264(const uint8_t *data, size_t size,
                          H264Classification *result) {
  size_t start, nal;
  H264Classification found = {0};
  found.parameter_sets_only = true;
  if (data == NULL || !start_code(data, size, 0, &start, &nal)) return false;
  /* Annex-B permits leading_zero_8bits, never arbitrary skipped garbage. */
  for (size_t i = 0; i < start; ++i) {
    if (data[i] != 0) return false;
  }
  for (;;) {
    size_t next_start = size, next_nal = size;
    bool more = start_code(data, size, nal, &next_start, &next_nal);
    size_t end = next_start;
    uint8_t type;
    /* trailing_zero_8bits are outside the NAL (including before a delimiter). */
    while (end > nal && data[end - 1] == 0) --end;
    if (!valid_nal(data + nal, end - nal)) return false;
    type = data[nal] & 0x1f;
    if (type >= 1 && type <= 5) found.vcl = true;
    if (type == 5) found.idr = true;
    if (type == 7) found.sps = true;
    if (type == 8) found.pps = true;
    if (type != 7 && type != 8) found.parameter_sets_only = false;
    if (!more) break;
    nal = next_nal;
  }
  *result = found;
  return true;
}

void source_clock_video_watermark_init(SourceClockVideoWatermark *tracker) {
  if (tracker != NULL) memset(tracker, 0, sizeof(*tracker));
}

bool source_clock_video_h264_is_idr(const uint8_t *payload, size_t payload_bytes) {
  H264Classification nal;
  return classify_h264(payload, payload_bytes, &nal) && nal.idr;
}

void source_clock_video_watermark_connection_init(
    SourceClockVideoWatermarkConnection *connection, uint8_t stream_kind) {
  if (connection == NULL) return;
  memset(connection, 0, sizeof(*connection));
  connection->stream_kind = stream_kind;
  connection->connected = true;
}

void source_clock_video_watermark_disconnect(
    SourceClockVideoWatermarkConnection *connection) {
  if (connection != NULL) memset(connection, 0, sizeof(*connection));
}

static SourceClockVideoWatermarkResult offer_control(
    SourceClockVideoWatermark *tracker,
    SourceClockVideoWatermarkConnection *connection, const SourceClockHeader *h) {
  if (h->codec != SOURCE_CLOCK_CONTROL_SESSION_START &&
      h->codec != SOURCE_CLOCK_CONTROL_SESSION_END)
    return SOURCE_CLOCK_VIDEO_WATERMARK_IGNORED;
  if (h->payload_bytes != 0 || h->sequence == 0 ||
      h->sequence < tracker->generation)
    return SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED;
  if (h->codec == SOURCE_CLOCK_CONTROL_SESSION_START) {
    if (h->sequence == tracker->generation && tracker->session_ended)
      return SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED;
    tracker->generation = h->sequence;
    tracker->session_ended = false;
    connection->generation = h->sequence;
  } else {
    if (connection->generation != h->sequence || tracker->generation != h->sequence)
      return SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED;
    tracker->session_ended = true;
    connection->generation = 0;
  }
  return SOURCE_CLOCK_VIDEO_WATERMARK_ACCEPTED;
}

SourceClockVideoWatermarkResult source_clock_video_watermark_offer(
    SourceClockVideoWatermark *tracker,
    SourceClockVideoWatermarkConnection *connection,
    const SourceClockHeader *h, const uint8_t *payload, size_t received_bytes) {
  H264Classification nal;
  bool pending;
  if (tracker == NULL || connection == NULL || h == NULL)
    return SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED;
  if (!connection->connected) return SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED;
  if (connection->stream_kind == SOURCE_CLOCK_STREAM_AUDIO)
    return SOURCE_CLOCK_VIDEO_WATERMARK_IGNORED;
  pending = connection->config_pending;
  if (connection->stream_kind != SOURCE_CLOCK_STREAM_VIDEO ||
      h->version != SOURCE_CLOCK_PROTOCOL_VERSION ||
      h->header_bytes != SOURCE_CLOCK_HEADER_SIZE || h->reserved != 0 ||
      (h->stream_kind != SOURCE_CLOCK_STREAM_CONTROL &&
       h->stream_kind != SOURCE_CLOCK_STREAM_VIDEO) ||
      h->payload_bytes > SOURCE_CLOCK_MAX_VIDEO_PAYLOAD ||
      received_bytes > h->payload_bytes || (received_bytes != 0 && payload == NULL)) {
    connection->config_pending = false;
    return SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED;
  }
  if (received_bytes < h->payload_bytes) return SOURCE_CLOCK_VIDEO_WATERMARK_INCOMPLETE;
  /* Consume adjacency on every complete frame, even if later rejected/ignored.
   * No accepted high water or tuple is committed until all checks succeed. */
  connection->config_pending = false;
  if (h->stream_kind == SOURCE_CLOCK_STREAM_CONTROL)
    return offer_control(tracker, connection, h);
  if (h->codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU)
    return SOURCE_CLOCK_VIDEO_WATERMARK_IGNORED;
  if (h->codec != SOURCE_CLOCK_CODEC_H264_ANNEXB_AU ||
      connection->generation == 0 || connection->generation != tracker->generation ||
      tracker->session_ended || h->sequence == 0 ||
      !classify_h264(payload, received_bytes, &nal))
    return SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED;
  if (h->sequence <= tracker->sequence_high) {
    if (h->sequence != tracker->sequence_high || !pending || !nal.idr ||
        connection->config_sequence != h->sequence ||
        connection->config_codec != h->codec ||
        connection->config_ntp_ns != h->remote_ntp_ns)
      return SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED;
  }
  tracker->sequence_high = h->sequence;
  if (nal.vcl) {
    tracker->final.has = true;
    tracker->final.source_session_generation = connection->generation;
    tracker->final.source_video_sequence = h->sequence;
    return SOURCE_CLOCK_VIDEO_WATERMARK_UPDATED;
  }
  if ((h->flags & SOURCE_CLOCK_FLAG_CONFIG) != 0 &&
      nal.parameter_sets_only && nal.sps && nal.pps) {
    connection->config_pending = true;
    connection->config_sequence = h->sequence;
    connection->config_ntp_ns = h->remote_ntp_ns;
    connection->config_codec = h->codec;
  }
  return SOURCE_CLOCK_VIDEO_WATERMARK_ACCEPTED;
}

SourceClockVideoWatermarkSnapshot source_clock_video_watermark_snapshot(
    const SourceClockVideoWatermark *tracker) {
  SourceClockVideoWatermarkSnapshot empty = {0};
  return tracker == NULL ? empty : tracker->final;
}
