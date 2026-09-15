#ifndef IMAGEPAD_SOURCE_CLOCK_READER_H
#define IMAGEPAD_SOURCE_CLOCK_READER_H

#include "source_clock_protocol.h"

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

typedef enum SourceClockReaderStatus {
  SOURCE_CLOCK_READER_OK = 0,
  SOURCE_CLOCK_READER_NEED_MORE = 1,
  SOURCE_CLOCK_READER_BAD_TOKEN = 2,
  SOURCE_CLOCK_READER_BAD_PROTOCOL = 3,
  SOURCE_CLOCK_READER_CALLBACK_ERROR = 4,
} SourceClockReaderStatus;

typedef int (*SourceClockReaderCallback)(void *user, const SourceClockHeader *header,
                                         const uint8_t *payload, size_t payload_bytes);

typedef struct SourceClockReader {
  uint8_t expected_stream_kind;
  uint8_t token[SOURCE_CLOCK_SESSION_TOKEN_BYTES];
  SourceClockReaderCallback callback;
  void *user;
  uint8_t header_bytes[SOURCE_CLOCK_HEADER_SIZE];
  size_t header_length;
  SourceClockHeader header;
  uint8_t *payload;
  size_t payload_length;
  bool hello_seen;
  bool authenticated;
  SourceClockReaderStatus error;
} SourceClockReader;

void source_clock_reader_init(SourceClockReader *reader, uint8_t expected_stream_kind,
                              const uint8_t token[SOURCE_CLOCK_SESSION_TOKEN_BYTES],
                              SourceClockReaderCallback callback, void *user);
void source_clock_reader_destroy(SourceClockReader *reader);
SourceClockReaderStatus source_clock_reader_feed(SourceClockReader *reader, const uint8_t *bytes,
                                                  size_t length);
bool source_clock_reader_authenticated(const SourceClockReader *reader);

#endif
