#include "source_clock_reader.h"

#include <stdlib.h>
#include <string.h>

static SourceClockReaderStatus reader_fail(SourceClockReader *reader,
                                           SourceClockReaderStatus status) {
  reader->error = status;
  return status;
}

static bool valid_control(const SourceClockHeader *header) {
  return header->stream_kind == SOURCE_CLOCK_STREAM_CONTROL &&
         header->codec >= SOURCE_CLOCK_CONTROL_HELLO &&
         header->codec <= SOURCE_CLOCK_CONTROL_HEARTBEAT;
}

static bool valid_media(const SourceClockReader *reader, const SourceClockHeader *header) {
  return header->stream_kind == reader->expected_stream_kind;
}

void source_clock_reader_init(SourceClockReader *reader, uint8_t expected_stream_kind,
                              const uint8_t token[SOURCE_CLOCK_SESSION_TOKEN_BYTES],
                              SourceClockReaderCallback callback, void *user) {
  memset(reader, 0, sizeof(*reader));
  reader->expected_stream_kind = expected_stream_kind;
  if (token != NULL) {
    memcpy(reader->token, token, SOURCE_CLOCK_SESSION_TOKEN_BYTES);
  }
  reader->callback = callback;
  reader->user = user;
}

void source_clock_reader_destroy(SourceClockReader *reader) {
  if (reader != NULL) {
    free(reader->payload);
    reader->payload = NULL;
    reader->payload_length = 0;
  }
}

static SourceClockReaderStatus start_frame(SourceClockReader *reader) {
  SourceClockStatus status = source_clock_header_decode(reader->header_bytes,
                                                        sizeof(reader->header_bytes),
                                                        &reader->header);
  if (status != SOURCE_CLOCK_OK) {
    return reader_fail(reader, SOURCE_CLOCK_READER_BAD_PROTOCOL);
  }
  if (!valid_control(&reader->header) && !valid_media(reader, &reader->header)) {
    return reader_fail(reader, SOURCE_CLOCK_READER_BAD_PROTOCOL);
  }
  if (!reader->hello_seen &&
      (reader->header.stream_kind != SOURCE_CLOCK_STREAM_CONTROL ||
       reader->header.codec != SOURCE_CLOCK_CONTROL_HELLO ||
       reader->header.payload_bytes != SOURCE_CLOCK_SESSION_TOKEN_BYTES)) {
    return reader_fail(reader, SOURCE_CLOCK_READER_BAD_PROTOCOL);
  }
  if (reader->header.payload_bytes > 0) {
    uint8_t *payload = (uint8_t *)realloc(reader->payload, reader->header.payload_bytes);
    if (payload == NULL) {
      return reader_fail(reader, SOURCE_CLOCK_READER_BAD_PROTOCOL);
    }
    reader->payload = payload;
  }
  reader->payload_length = 0;
  return SOURCE_CLOCK_READER_OK;
}

static SourceClockReaderStatus finish_frame(SourceClockReader *reader) {
  if (reader->header.stream_kind == SOURCE_CLOCK_STREAM_CONTROL &&
      reader->header.codec == SOURCE_CLOCK_CONTROL_HELLO) {
    if (memcmp(reader->payload, reader->token, SOURCE_CLOCK_SESSION_TOKEN_BYTES) != 0) {
      return reader_fail(reader, SOURCE_CLOCK_READER_BAD_TOKEN);
    }
    reader->hello_seen = true;
    reader->authenticated = true;
  }
  if (reader->callback != NULL &&
      reader->callback(reader->user, &reader->header, reader->payload, reader->payload_length) != 0) {
    return reader_fail(reader, SOURCE_CLOCK_READER_CALLBACK_ERROR);
  }
  reader->header_length = 0;
  reader->payload_length = 0;
  return SOURCE_CLOCK_READER_OK;
}

SourceClockReaderStatus source_clock_reader_feed(SourceClockReader *reader, const uint8_t *bytes,
                                                  size_t length) {
  if (reader == NULL || (bytes == NULL && length != 0)) {
    return SOURCE_CLOCK_READER_BAD_PROTOCOL;
  }
  if (reader->error != SOURCE_CLOCK_READER_OK) {
    return reader->error;
  }
  size_t offset = 0;
  while (offset < length) {
    if (reader->header_length < SOURCE_CLOCK_HEADER_SIZE) {
      size_t remaining = SOURCE_CLOCK_HEADER_SIZE - reader->header_length;
      size_t take = length - offset < remaining ? length - offset : remaining;
      memcpy(reader->header_bytes + reader->header_length, bytes + offset, take);
      reader->header_length += take;
      offset += take;
      if (reader->header_length < SOURCE_CLOCK_HEADER_SIZE) {
        continue;
      }
      SourceClockReaderStatus status = start_frame(reader);
      if (status != SOURCE_CLOCK_READER_OK) {
        return status;
      }
    }
    size_t remaining_payload = reader->header.payload_bytes - reader->payload_length;
    size_t take = length - offset < remaining_payload ? length - offset : remaining_payload;
    if (take > 0) {
      memcpy(reader->payload + reader->payload_length, bytes + offset, take);
      reader->payload_length += take;
      offset += take;
    }
    if (reader->payload_length == reader->header.payload_bytes) {
      SourceClockReaderStatus status = finish_frame(reader);
      if (status != SOURCE_CLOCK_READER_OK) {
        return status;
      }
    }
  }
  return SOURCE_CLOCK_READER_OK;
}

bool source_clock_reader_authenticated(const SourceClockReader *reader) {
  return reader != NULL && reader->authenticated;
}
