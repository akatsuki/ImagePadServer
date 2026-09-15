#include "source_clock_protocol.h"

#include <string.h>

static uint16_t read_u16(const uint8_t *p) {
  return (uint16_t)(((uint16_t)p[0] << 8) | p[1]);
}

static uint32_t read_u32(const uint8_t *p) {
  return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) |
         ((uint32_t)p[2] << 8) | (uint32_t)p[3];
}

static uint64_t read_u64(const uint8_t *p) {
  uint64_t hi = read_u32(p);
  uint64_t lo = read_u32(p + 4);
  return (hi << 32) | lo;
}

static void write_u16(uint8_t *p, uint16_t value) {
  p[0] = (uint8_t)(value >> 8);
  p[1] = (uint8_t)value;
}

static void write_u32(uint8_t *p, uint32_t value) {
  p[0] = (uint8_t)(value >> 24);
  p[1] = (uint8_t)(value >> 16);
  p[2] = (uint8_t)(value >> 8);
  p[3] = (uint8_t)value;
}

static void write_u64(uint8_t *p, uint64_t value) {
  write_u32(p, (uint32_t)(value >> 32));
  write_u32(p + 4, (uint32_t)value);
}

SourceClockStatus source_clock_header_decode(const uint8_t *bytes, size_t length,
                                             SourceClockHeader *out) {
  if (bytes == NULL || out == NULL || length < SOURCE_CLOCK_HEADER_SIZE) {
    return SOURCE_CLOCK_NEED_MORE;
  }
  if (memcmp(bytes, "IPAF", 4) != 0) {
    return SOURCE_CLOCK_BAD_MAGIC;
  }
  SourceClockHeader h;
  h.version = read_u16(bytes + 4);
  h.header_bytes = read_u16(bytes + 6);
  h.stream_kind = bytes[8];
  h.codec = bytes[9];
  h.flags = read_u16(bytes + 10);
  h.payload_bytes = read_u32(bytes + 12);
  h.sequence = read_u32(bytes + 16);
  h.remote_ntp_ns = read_u64(bytes + 20);
  h.sample_count = read_u32(bytes + 28);
  h.sample_rate = read_u32(bytes + 32);
  h.channels = read_u16(bytes + 36);
  h.reserved = read_u16(bytes + 38);
  if (h.version != SOURCE_CLOCK_PROTOCOL_VERSION) {
    return SOURCE_CLOCK_BAD_VERSION;
  }
  if (h.header_bytes != SOURCE_CLOCK_HEADER_SIZE || h.reserved != 0) {
    return SOURCE_CLOCK_BAD_HEADER;
  }
  if (h.stream_kind > SOURCE_CLOCK_STREAM_AUDIO) {
    return SOURCE_CLOCK_BAD_KIND;
  }
  if ((h.stream_kind == SOURCE_CLOCK_STREAM_VIDEO &&
       h.payload_bytes > SOURCE_CLOCK_MAX_VIDEO_PAYLOAD) ||
      (h.stream_kind == SOURCE_CLOCK_STREAM_AUDIO &&
       h.payload_bytes > SOURCE_CLOCK_MAX_AUDIO_PAYLOAD)) {
    return SOURCE_CLOCK_PAYLOAD_TOO_LARGE;
  }
  *out = h;
  return SOURCE_CLOCK_OK;
}

SourceClockStatus source_clock_header_decode_for_stream(const uint8_t *bytes, size_t length,
                                                         uint8_t expected_stream_kind,
                                                         SourceClockHeader *out) {
  SourceClockStatus status = source_clock_header_decode(bytes, length, out);
  if (status != SOURCE_CLOCK_OK) {
    return status;
  }
  if (out->stream_kind != expected_stream_kind) {
    return SOURCE_CLOCK_BAD_KIND;
  }
  return SOURCE_CLOCK_OK;
}

SourceClockStatus source_clock_header_encode(const SourceClockHeader *header, uint8_t *out,
                                             size_t length) {
  if (header == NULL || out == NULL || length < SOURCE_CLOCK_HEADER_SIZE) {
    return SOURCE_CLOCK_NEED_MORE;
  }
  if (header->version != SOURCE_CLOCK_PROTOCOL_VERSION ||
      header->header_bytes != SOURCE_CLOCK_HEADER_SIZE ||
      header->reserved != 0 || header->stream_kind > SOURCE_CLOCK_STREAM_AUDIO) {
    return SOURCE_CLOCK_BAD_HEADER;
  }
  if ((header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO &&
       header->payload_bytes > SOURCE_CLOCK_MAX_VIDEO_PAYLOAD) ||
      (header->stream_kind == SOURCE_CLOCK_STREAM_AUDIO &&
       header->payload_bytes > SOURCE_CLOCK_MAX_AUDIO_PAYLOAD)) {
    return SOURCE_CLOCK_PAYLOAD_TOO_LARGE;
  }
  memset(out, 0, SOURCE_CLOCK_HEADER_SIZE);
  memcpy(out, "IPAF", 4);
  write_u16(out + 4, header->version);
  write_u16(out + 6, header->header_bytes);
  out[8] = header->stream_kind;
  out[9] = header->codec;
  write_u16(out + 10, header->flags);
  write_u32(out + 12, header->payload_bytes);
  write_u32(out + 16, header->sequence);
  write_u64(out + 20, header->remote_ntp_ns);
  write_u32(out + 28, header->sample_count);
  write_u32(out + 32, header->sample_rate);
  write_u16(out + 36, header->channels);
  write_u16(out + 38, header->reserved);
  return SOURCE_CLOCK_OK;
}
