#include "source_clock_protocol.h"

#include <assert.h>
#include <stdint.h>
#include <string.h>

int main(void) {
  static const uint8_t golden[40] = {
      0x49, 0x50, 0x41, 0x46, 0x00, 0x01, 0x00, 0x28,
      0x01, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x04,
      0x00, 0x00, 0x00, 0x07, 0x00, 0x00, 0x00, 0x00,
      0x3b, 0x9a, 0xca, 0x00, 0x00, 0x00, 0x00, 0x00,
      0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
  };
  SourceClockHeader header = {0};
  header.version = 1;
  header.header_bytes = 40;
  header.stream_kind = SOURCE_CLOCK_STREAM_VIDEO;
  header.codec = SOURCE_CLOCK_CODEC_H264_ANNEXB_AU;
  header.flags = SOURCE_CLOCK_FLAG_KEYFRAME;
  header.payload_bytes = 4;
  header.sequence = 7;
  header.remote_ntp_ns = 1000000000ULL;
  uint8_t encoded[40] = {0};
  assert(source_clock_header_encode(&header, encoded, sizeof(encoded)) == SOURCE_CLOCK_OK);
  assert(memcmp(encoded, golden, sizeof(golden)) == 0);

  SourceClockHeader decoded = {0};
  assert(source_clock_header_decode(encoded, sizeof(encoded) - 1, &decoded) == SOURCE_CLOCK_NEED_MORE);
  uint8_t concatenated[80] = {0};
  memcpy(concatenated, encoded, sizeof(encoded));
  memcpy(concatenated + sizeof(encoded), encoded, sizeof(encoded));
  assert(source_clock_header_decode(encoded, sizeof(encoded), &decoded) == SOURCE_CLOCK_OK);
  assert(decoded.remote_ntp_ns == header.remote_ntp_ns);
  assert(decoded.payload_bytes == header.payload_bytes);
  assert(source_clock_header_decode(concatenated + sizeof(encoded), sizeof(encoded), &decoded) ==
         SOURCE_CLOCK_OK);
  assert(source_clock_header_decode_for_stream(encoded, sizeof(encoded),
                                               SOURCE_CLOCK_STREAM_AUDIO, &decoded) ==
         SOURCE_CLOCK_BAD_KIND);
  encoded[38] = 1;
  assert(source_clock_header_decode(encoded, sizeof(encoded), &decoded) == SOURCE_CLOCK_BAD_HEADER);
  return 0;
}
