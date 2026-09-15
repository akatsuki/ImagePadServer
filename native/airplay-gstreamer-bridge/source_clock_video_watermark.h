#ifndef IMAGEPAD_SOURCE_CLOCK_VIDEO_WATERMARK_H
#define IMAGEPAD_SOURCE_CLOCK_VIDEO_WATERMARK_H

#include "source_clock_protocol.h"

#include <stdbool.h>

typedef struct SourceClockVideoWatermarkSnapshot {
  bool has;
  uint32_t source_session_generation;
  uint32_t source_video_sequence;
} SourceClockVideoWatermarkSnapshot;

/* One tracker per receiver identity/publisher lifetime, NOT per TCP connection.
 * Initialize once. Generation changes and reconnects never reset sequence_high
 * or relabel/erase final. Callers serialize all access (including snapshot).
 * Fields are implementation state; consumers use snapshot, not direct writes. */
typedef struct SourceClockVideoWatermark {
  uint32_t generation;
  uint32_t sequence_high;
  bool session_ended;
  SourceClockVideoWatermarkSnapshot final;
} SourceClockVideoWatermark;

/* One state per authenticated TCP connection. Never copy a live state to a new
 * connection. stream_kind is the listener's role, not a frame's CONTROL kind.
 * Audio connections cannot establish or end a video generation. */
typedef struct SourceClockVideoWatermarkConnection {
  uint8_t stream_kind;
  bool connected;
  uint32_t generation;
  bool config_pending;
  uint32_t config_sequence;
  uint64_t config_ntp_ns;
  uint8_t config_codec;
} SourceClockVideoWatermarkConnection;

typedef enum SourceClockVideoWatermarkResult {
  SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED = 0,
  SOURCE_CLOCK_VIDEO_WATERMARK_IGNORED,
  SOURCE_CLOCK_VIDEO_WATERMARK_INCOMPLETE,
  SOURCE_CLOCK_VIDEO_WATERMARK_ACCEPTED,
  SOURCE_CLOCK_VIDEO_WATERMARK_UPDATED
} SourceClockVideoWatermarkResult;

void source_clock_video_watermark_init(SourceClockVideoWatermark *tracker);
void source_clock_video_watermark_connection_init(
    SourceClockVideoWatermarkConnection *connection, uint8_t stream_kind);
void source_clock_video_watermark_disconnect(
    SourceClockVideoWatermarkConnection *connection);

/* Supply a decoded header and the ORIGINAL contiguous payload after reader
 * authentication, before gate/decoder/appsrc/timeline/scheduler decisions.
 * No socket, allocation, clock, event writer, or finalization is involved.
 * Reader owns reassembly: received_bytes < payload_bytes returns INCOMPLETE
 * without mutation; do not call once per fragment with a fragment-only pointer.
 * All complete frames on this connection must be offered, including controls.
 * A complete intervening rejected/ignored frame revokes CONFIG adjacency but
 * rejection never advances generation, sequence_high, or final.
 * H264 validation covers Annex-B framing/NAL headers/byte escaping, not complete
 * SPS/PPS/slice decoding. Only NAL types 1..5 update final, regardless of flags.
 * CONFIG with SPS+PPS only arms one same-connection/generation/codec/NTP IDR
 * exception. CONFIG bits on the actual IDR are allowed. NTP is compared only
 * for this pair; it is never used for ordering or as a source sequence.
 * SESSION_START/END use CONTROL header.sequence as generation. END closes the
 * current source generation; a subsequent session must have a newer generation.
 */
SourceClockVideoWatermarkResult source_clock_video_watermark_offer(
    SourceClockVideoWatermark *tracker,
    SourceClockVideoWatermarkConnection *connection,
    const SourceClockHeader *header, const uint8_t *payload, size_t received_bytes);

/* Returns either has=false with zero fields, or has=true with a nonzero tuple.
 * Read-only and repeatable; this does not reserve or emit a final event. */
SourceClockVideoWatermarkSnapshot source_clock_video_watermark_snapshot(
    const SourceClockVideoWatermark *tracker);

/* Strict Annex-B structural validation plus actual type-5 NAL inspection.
 * Does not use source flags or GST_BUFFER delta flags as IDR evidence. */
bool source_clock_video_h264_is_idr(const uint8_t *payload, size_t payload_bytes);

#endif
