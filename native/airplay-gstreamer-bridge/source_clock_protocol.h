#ifndef IMAGEPAD_SOURCE_CLOCK_PROTOCOL_H
#define IMAGEPAD_SOURCE_CLOCK_PROTOCOL_H

#include <stddef.h>
#include <stdint.h>

#define SOURCE_CLOCK_PROTOCOL_VERSION 1u
#define SOURCE_CLOCK_HEADER_SIZE 40u
#define SOURCE_CLOCK_MAX_VIDEO_PAYLOAD (64u * 1024u * 1024u)
#define SOURCE_CLOCK_MAX_AUDIO_PAYLOAD (1u * 1024u * 1024u)
#define SOURCE_CLOCK_SESSION_TOKEN_BYTES 16u

typedef enum SourceClockStatus {
  SOURCE_CLOCK_OK = 0,
  SOURCE_CLOCK_NEED_MORE = 1,
  SOURCE_CLOCK_BAD_MAGIC = 2,
  SOURCE_CLOCK_BAD_VERSION = 3,
  SOURCE_CLOCK_BAD_HEADER = 4,
  SOURCE_CLOCK_BAD_KIND = 5,
  SOURCE_CLOCK_PAYLOAD_TOO_LARGE = 6,
} SourceClockStatus;

typedef enum SourceClockStreamKind {
  SOURCE_CLOCK_STREAM_CONTROL = 0,
  SOURCE_CLOCK_STREAM_VIDEO = 1,
  SOURCE_CLOCK_STREAM_AUDIO = 2,
} SourceClockStreamKind;

typedef enum SourceClockCodec {
  SOURCE_CLOCK_CODEC_H264_ANNEXB_AU = 1,
  SOURCE_CLOCK_CODEC_H265_ANNEXB_AU = 2,
  SOURCE_CLOCK_CODEC_AAC_ELD_RAW = 16,
  SOURCE_CLOCK_CODEC_ALAC_RAW = 17,
  SOURCE_CLOCK_CODEC_AAC_LC_ADTS = 18,
} SourceClockCodec;

typedef enum SourceClockControlOpcode {
  SOURCE_CLOCK_CONTROL_HELLO = 1,
  SOURCE_CLOCK_CONTROL_SESSION_START = 2,
  SOURCE_CLOCK_CONTROL_STREAM_GAP = 3,
  SOURCE_CLOCK_CONTROL_SESSION_END = 4,
  SOURCE_CLOCK_CONTROL_HEARTBEAT = 5,
} SourceClockControlOpcode;

enum {
  SOURCE_CLOCK_FLAG_KEYFRAME = 1u << 0,
  SOURCE_CLOCK_FLAG_CONFIG = 1u << 1,
  SOURCE_CLOCK_FLAG_DISCONTINUITY = 1u << 2,
};

typedef struct SourceClockHeader {
  uint16_t version;
  uint16_t header_bytes;
  uint8_t stream_kind;
  uint8_t codec;
  uint16_t flags;
  uint32_t payload_bytes;
  uint32_t sequence;
  uint64_t remote_ntp_ns;
  uint32_t sample_count;
  uint32_t sample_rate;
  uint16_t channels;
  uint16_t reserved;
} SourceClockHeader;

SourceClockStatus source_clock_header_decode(const uint8_t *bytes, size_t length,
                                             SourceClockHeader *out);
SourceClockStatus source_clock_header_decode_for_stream(const uint8_t *bytes, size_t length,
                                                         uint8_t expected_stream_kind,
                                                         SourceClockHeader *out);
SourceClockStatus source_clock_header_encode(const SourceClockHeader *header, uint8_t *out,
                                             size_t length);

#endif
