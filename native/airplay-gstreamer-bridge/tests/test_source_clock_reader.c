#include "source_clock_reader.h"

#include <assert.h>
#include <stdint.h>
#include <string.h>

typedef struct ReaderTestState {
  int hello_count;
  int session_start_count;
  int media_count;
  uint32_t last_sequence;
} ReaderTestState;

static int on_frame(void *user, const SourceClockHeader *header, const uint8_t *payload,
                    size_t payload_bytes) {
  ReaderTestState *state = (ReaderTestState *)user;
  if (header->stream_kind == SOURCE_CLOCK_STREAM_CONTROL &&
      header->codec == SOURCE_CLOCK_CONTROL_HELLO) {
    state->hello_count++;
  } else if (header->stream_kind == SOURCE_CLOCK_STREAM_CONTROL &&
             header->codec == SOURCE_CLOCK_CONTROL_SESSION_START) {
    assert(payload_bytes == 0);
    state->session_start_count++;
  } else if (header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO) {
    assert(payload_bytes == 3);
    assert(memcmp(payload, "IDR", 3) == 0);
    state->media_count++;
    state->last_sequence = header->sequence;
  }
  return 0;
}

static size_t encode_frame(uint8_t *out, size_t out_size, uint8_t stream_kind, uint8_t codec,
                           uint32_t sequence, const uint8_t *payload, size_t payload_bytes) {
  SourceClockHeader header = {0};
  header.version = SOURCE_CLOCK_PROTOCOL_VERSION;
  header.header_bytes = SOURCE_CLOCK_HEADER_SIZE;
  header.stream_kind = stream_kind;
  header.codec = codec;
  header.payload_bytes = (uint32_t)payload_bytes;
  header.sequence = sequence;
  assert(out_size >= SOURCE_CLOCK_HEADER_SIZE + payload_bytes);
  assert(source_clock_header_encode(&header, out, out_size) == SOURCE_CLOCK_OK);
  if (payload_bytes > 0) memcpy(out + SOURCE_CLOCK_HEADER_SIZE, payload, payload_bytes);
  return SOURCE_CLOCK_HEADER_SIZE + payload_bytes;
}

int main(void) {
  const uint8_t token[SOURCE_CLOCK_SESSION_TOKEN_BYTES] = {
      0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15};
  uint8_t bytes[256] = {0};
  size_t length = encode_frame(bytes, sizeof(bytes), SOURCE_CLOCK_STREAM_CONTROL,
                                SOURCE_CLOCK_CONTROL_HELLO, 1, token, sizeof(token));
  length += encode_frame(bytes + length, sizeof(bytes) - length, SOURCE_CLOCK_STREAM_CONTROL,
                         SOURCE_CLOCK_CONTROL_SESSION_START, 2, NULL, 0);
  const uint8_t media[] = {'I', 'D', 'R'};
  length += encode_frame(bytes + length, sizeof(bytes) - length, SOURCE_CLOCK_STREAM_VIDEO,
                         SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, 7, media, sizeof(media));

  ReaderTestState state = {0};
  SourceClockReader reader;
  source_clock_reader_init(&reader, SOURCE_CLOCK_STREAM_VIDEO, token, on_frame, &state);
  for (size_t i = 0; i < length; ++i) {
    assert(source_clock_reader_feed(&reader, bytes + i, 1) == SOURCE_CLOCK_READER_OK);
  }
  assert(state.hello_count == 1);
  assert(state.session_start_count == 1);
  assert(state.media_count == 1);
  assert(state.last_sequence == 7);
  assert(source_clock_reader_authenticated(&reader));

  ReaderTestState bad_state = {0};
  SourceClockReader bad_reader;
  uint8_t wrong_token[SOURCE_CLOCK_SESSION_TOKEN_BYTES] = {0};
  wrong_token[0] = 99;
  source_clock_reader_init(&bad_reader, SOURCE_CLOCK_STREAM_VIDEO, wrong_token, on_frame, &bad_state);
  assert(source_clock_reader_feed(&bad_reader, bytes, length) == SOURCE_CLOCK_READER_BAD_TOKEN);
  assert(!source_clock_reader_authenticated(&bad_reader));
  return 0;
}
