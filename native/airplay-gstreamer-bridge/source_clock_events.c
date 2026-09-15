#ifndef _WIN32
#define _POSIX_C_SOURCE 200809L
#endif

#include "source_clock_events.h"

#include <inttypes.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#ifdef _WIN32
#include <windows.h>
#endif

typedef struct {
  char *data;
  size_t length;
  size_t capacity;
} JsonBuffer;

static bool nonempty(const char *value) {
  return value != NULL && value[0] != '\0';
}

static bool writer_is_valid(const SourceClockEventWriter *writer) {
  return writer != NULL && nonempty(writer->session_id) &&
         writer->publisher_generation != 0 && nonempty(writer->ready_path) &&
         nonempty(writer->media_ready_path) && nonempty(writer->event_log_path);
}

#ifdef _WIN32
static wchar_t *utf8_to_wide(const char *value) {
  int characters;
  wchar_t *wide;
  if (value == NULL) {
    return NULL;
  }
  characters = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS,
                                   value, -1, NULL, 0);
  if (characters <= 0 ||
      (size_t) characters > SIZE_MAX / sizeof(wchar_t)) {
    return NULL;
  }
  wide = (wchar_t *) malloc((size_t) characters * sizeof(wchar_t));
  if (wide == NULL) {
    return NULL;
  }
  if (MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS,
                          value, -1, wide, characters) != characters) {
    free(wide);
    return NULL;
  }
  return wide;
}

static FILE *open_utf8_file(const char *path, bool append) {
  wchar_t *wide_path = utf8_to_wide(path);
  FILE *file;
  if (wide_path == NULL) {
    return NULL;
  }
  file = _wfopen(wide_path, append ? L"ab" : L"wb");
  free(wide_path);
  return file;
}

static void remove_utf8_file(const char *path) {
  wchar_t *wide_path = utf8_to_wide(path);
  if (wide_path != NULL) {
    _wremove(wide_path);
    free(wide_path);
  }
}
#else
static FILE *open_utf8_file(const char *path, bool append) {
  return fopen(path, append ? "ab" : "wb");
}

static void remove_utf8_file(const char *path) {
  remove(path);
}
#endif

static void json_buffer_clear(JsonBuffer *buffer) {
  free(buffer->data);
  buffer->data = NULL;
  buffer->length = 0;
  buffer->capacity = 0;
}

static bool json_buffer_reserve(JsonBuffer *buffer, size_t additional) {
  size_t required;
  size_t capacity;
  char *next;
  if (additional > SIZE_MAX - buffer->length - 1) {
    return false;
  }
  required = buffer->length + additional + 1;
  if (required <= buffer->capacity) {
    return true;
  }
  capacity = buffer->capacity == 0 ? 256 : buffer->capacity;
  while (capacity < required) {
    if (capacity > SIZE_MAX / 2) {
      capacity = required;
      break;
    }
    capacity *= 2;
  }
  next = (char *) realloc(buffer->data, capacity);
  if (next == NULL) {
    return false;
  }
  buffer->data = next;
  buffer->capacity = capacity;
  return true;
}

static bool json_buffer_append_bytes(JsonBuffer *buffer,
                                     const char *value, size_t bytes) {
  if (!json_buffer_reserve(buffer, bytes)) {
    return false;
  }
  memcpy(buffer->data + buffer->length, value, bytes);
  buffer->length += bytes;
  buffer->data[buffer->length] = '\0';
  return true;
}

static bool json_buffer_append_literal(JsonBuffer *buffer, const char *value) {
  return json_buffer_append_bytes(buffer, value, strlen(value));
}

static bool json_buffer_append_char(JsonBuffer *buffer, char value) {
  return json_buffer_append_bytes(buffer, &value, 1);
}

static bool json_buffer_append_uint64(JsonBuffer *buffer, uint64_t value) {
  char number[32];
  int bytes = snprintf(number, sizeof(number), "%" PRIu64, value);
  return bytes > 0 && (size_t) bytes < sizeof(number) &&
         json_buffer_append_bytes(buffer, number, (size_t) bytes);
}

static bool json_buffer_append_int(JsonBuffer *buffer, int value) {
  char number[32];
  int bytes = snprintf(number, sizeof(number), "%d", value);
  return bytes > 0 && (size_t) bytes < sizeof(number) &&
         json_buffer_append_bytes(buffer, number, (size_t) bytes);
}

static bool json_buffer_append_string(JsonBuffer *buffer, const char *value) {
  static const char hex[] = "0123456789abcdef";
  const unsigned char *cursor = (const unsigned char *) value;
  if (!json_buffer_append_char(buffer, '"')) {
    return false;
  }
  while (*cursor != '\0') {
    unsigned char current = *cursor++;
    switch (current) {
      case '"':
        if (!json_buffer_append_literal(buffer, "\\\"")) return false;
        break;
      case '\\':
        if (!json_buffer_append_literal(buffer, "\\\\")) return false;
        break;
      case '\b':
        if (!json_buffer_append_literal(buffer, "\\b")) return false;
        break;
      case '\f':
        if (!json_buffer_append_literal(buffer, "\\f")) return false;
        break;
      case '\n':
        if (!json_buffer_append_literal(buffer, "\\n")) return false;
        break;
      case '\r':
        if (!json_buffer_append_literal(buffer, "\\r")) return false;
        break;
      case '\t':
        if (!json_buffer_append_literal(buffer, "\\t")) return false;
        break;
      default:
        if (current < 0x20) {
          char escaped[] = {'\\', 'u', '0', '0', hex[current >> 4],
                            hex[current & 0x0f]};
          if (!json_buffer_append_bytes(buffer, escaped, sizeof(escaped))) {
            return false;
          }
        } else if (!json_buffer_append_char(buffer, (char) current)) {
          return false;
        }
        break;
    }
  }
  return json_buffer_append_char(buffer, '"');
}

static bool utc_timestamp(char *output, size_t output_bytes) {
  struct timespec now;
  struct tm utc;
  int written;
  if (timespec_get(&now, TIME_UTC) != TIME_UTC || now.tv_sec <= 0) {
    return false;
  }
#ifdef _WIN32
  if (gmtime_s(&utc, &now.tv_sec) != 0) {
    return false;
  }
#else
  if (gmtime_r(&now.tv_sec, &utc) == NULL) {
    return false;
  }
#endif
  written = snprintf(output, output_bytes,
                     "%04d-%02d-%02dT%02d:%02d:%02d.%09ldZ",
                     utc.tm_year + 1900, utc.tm_mon + 1, utc.tm_mday,
                     utc.tm_hour, utc.tm_min, utc.tm_sec, now.tv_nsec);
  return written > 0 && (size_t) written < output_bytes;
}

static bool append_common_event(JsonBuffer *buffer,
                                const SourceClockEventWriter *writer,
                                const char *event_name,
                                const char *timestamp) {
  return json_buffer_append_literal(buffer, "{\"schema\":2,\"sessionId\":") &&
         json_buffer_append_string(buffer, writer->session_id) &&
         json_buffer_append_literal(buffer, ",\"publisherGeneration\":") &&
         json_buffer_append_uint64(buffer, writer->publisher_generation) &&
         json_buffer_append_literal(buffer, ",\"event\":") &&
         json_buffer_append_string(buffer, event_name) &&
         json_buffer_append_literal(buffer, ",\"at\":") &&
         json_buffer_append_string(buffer, timestamp);
}

static bool replace_file_atomically(const char *temporary_path,
                                    const char *snapshot_path) {
#ifdef _WIN32
  wchar_t *wide_temporary_path = utf8_to_wide(temporary_path);
  wchar_t *wide_snapshot_path = utf8_to_wide(snapshot_path);
  bool replaced = wide_temporary_path != NULL && wide_snapshot_path != NULL &&
                  MoveFileExW(wide_temporary_path, wide_snapshot_path,
                              MOVEFILE_REPLACE_EXISTING |
                                  MOVEFILE_WRITE_THROUGH) != 0;
  free(wide_temporary_path);
  free(wide_snapshot_path);
  return replaced;
#else
  return rename(temporary_path, snapshot_path) == 0;
#endif
}

static bool write_snapshot(const char *snapshot_path, const JsonBuffer *event) {
  size_t path_bytes = strlen(snapshot_path);
  char *temporary_path;
  FILE *file;
  bool written;
  if (path_bytes > SIZE_MAX - 5) {
    return false;
  }
  temporary_path = (char *) malloc(path_bytes + 5);
  if (temporary_path == NULL) {
    return false;
  }
  memcpy(temporary_path, snapshot_path, path_bytes);
  memcpy(temporary_path + path_bytes, ".tmp", 5);
  remove_utf8_file(temporary_path);
  file = open_utf8_file(temporary_path, false);
  if (file == NULL) {
    free(temporary_path);
    return false;
  }
  written = fwrite(event->data, 1, event->length, file) == event->length &&
            fputc('\n', file) != EOF && fflush(file) == 0;
  if (fclose(file) != 0) {
    written = false;
  }
  if (!written || !replace_file_atomically(temporary_path, snapshot_path)) {
    remove_utf8_file(temporary_path);
    free(temporary_path);
    return false;
  }
  free(temporary_path);
  return true;
}

static bool append_event_log(const char *event_log_path, const JsonBuffer *event) {
  FILE *file = open_utf8_file(event_log_path, true);
  bool written;
  if (file == NULL) {
    return false;
  }
  written = fwrite(event->data, 1, event->length, file) == event->length &&
            fputc('\n', file) != EOF && fflush(file) == 0;
  if (fclose(file) != 0) {
    written = false;
  }
  return written;
}

static bool emit_event(SourceClockEventWriter *writer,
                       const char *snapshot_path, JsonBuffer *event) {
  bool success = write_snapshot(snapshot_path, event) &&
                 append_event_log(writer->event_log_path, event);
  json_buffer_clear(event);
  return success;
}

static bool emit_log_event(SourceClockEventWriter *writer, JsonBuffer *event) {
  bool success = append_event_log(writer->event_log_path, event);
  json_buffer_clear(event);
  return success;
}

bool source_clock_event_writer_init(SourceClockEventWriter *writer,
                                    const char *session_id,
                                    uint64_t publisher_generation,
                                    const char *ready_path,
                                    const char *media_ready_path,
                                    const char *event_log_path) {
  if (writer == NULL || !nonempty(session_id) || publisher_generation == 0 ||
      !nonempty(ready_path) || !nonempty(media_ready_path) ||
      !nonempty(event_log_path)) {
    return false;
  }
  writer->session_id = session_id;
  writer->publisher_generation = publisher_generation;
  writer->ready_path = ready_path;
  writer->media_ready_path = media_ready_path;
  writer->event_log_path = event_log_path;
  return true;
}

bool source_clock_event_write_publisher_ready(SourceClockEventWriter *writer,
                                              int video_listen_port,
                                              int audio_listen_port) {
  char timestamp[64];
  JsonBuffer event = {0};
  if (!writer_is_valid(writer) || video_listen_port <= 0 ||
      video_listen_port > 65534 || audio_listen_port <= 0 ||
      audio_listen_port > 65534 || !utc_timestamp(timestamp, sizeof(timestamp))) {
    return false;
  }
  if (!append_common_event(&event, writer, "publisher-ready", timestamp) ||
      !json_buffer_append_literal(&event, ",\"protocolVersion\":1") ||
      !json_buffer_append_literal(&event, ",\"videoListenPort\":") ||
      !json_buffer_append_int(&event, video_listen_port) ||
      !json_buffer_append_literal(&event, ",\"audioListenPort\":") ||
      !json_buffer_append_int(&event, audio_listen_port) ||
      !json_buffer_append_literal(&event,
                                  ",\"pipelineStartAccepted\":true}")) {
    json_buffer_clear(&event);
    return false;
  }
  return emit_event(writer, writer->ready_path, &event);
}

static bool write_video_stage_log_event(SourceClockEventWriter *writer,
                                        const char *event_name,
                                        const uint64_t *running_time_ns,
                                        const uint64_t *source_ntp_ns) {
  char timestamp[64];
  JsonBuffer event = {0};
  if (!writer_is_valid(writer) || !nonempty(event_name) ||
      running_time_ns == NULL || !utc_timestamp(timestamp, sizeof(timestamp))) {
    return false;
  }
  if (!append_common_event(&event, writer, event_name, timestamp) ||
      !json_buffer_append_literal(&event, ",\"runningTimeNs\":") ||
      !json_buffer_append_uint64(&event, *running_time_ns)) {
    json_buffer_clear(&event);
    return false;
  }
  if (source_ntp_ns != NULL &&
      (!json_buffer_append_literal(&event, ",\"sourceNtpNs\":") ||
       !json_buffer_append_uint64(&event, *source_ntp_ns))) {
    json_buffer_clear(&event);
    return false;
  }
  if (!json_buffer_append_char(&event, '}')) {
    json_buffer_clear(&event);
    return false;
  }
  return emit_log_event(writer, &event);
}

bool source_clock_event_write_video_input_idr(SourceClockEventWriter *writer,
                                              const uint64_t *running_time_ns,
                                              const uint64_t *source_ntp_ns) {
  return write_video_stage_log_event(writer, "video-input-idr",
                                     running_time_ns, source_ntp_ns);
}

bool source_clock_event_write_video_decoded(SourceClockEventWriter *writer,
                                            const uint64_t *running_time_ns,
                                            const uint64_t *source_ntp_ns) {
  char timestamp[64];
  JsonBuffer event = {0};
  if (!writer_is_valid(writer) || running_time_ns == NULL ||
      !utc_timestamp(timestamp, sizeof(timestamp))) {
    return false;
  }
  if (!append_common_event(&event, writer, "video-decoded", timestamp) ||
      !json_buffer_append_literal(&event, ",\"videoDecoded\":true") ||
      !json_buffer_append_literal(&event, ",\"runningTimeNs\":") ||
      !json_buffer_append_uint64(&event, *running_time_ns)) {
    json_buffer_clear(&event);
    return false;
  }
  if (source_ntp_ns != NULL &&
      (!json_buffer_append_literal(&event, ",\"sourceNtpNs\":") ||
       !json_buffer_append_uint64(&event, *source_ntp_ns))) {
    json_buffer_clear(&event);
    return false;
  }
  if (!json_buffer_append_char(&event, '}')) {
    json_buffer_clear(&event);
    return false;
  }
  return emit_event(writer, writer->media_ready_path, &event);
}

bool source_clock_event_write_video_encoded(SourceClockEventWriter *writer,
                                            const uint64_t *running_time_ns) {
  return write_video_stage_log_event(writer, "video-encoded",
                                     running_time_ns, NULL);
}

bool source_clock_event_write_video_watermark_final(
    SourceClockEventWriter *writer,
    uint64_t source_session_generation,
    uint64_t source_video_sequence) {
  char timestamp[64];
  JsonBuffer event = {0};
  if (!writer_is_valid(writer) || source_session_generation == 0 ||
      source_video_sequence == 0 || !utc_timestamp(timestamp, sizeof(timestamp))) {
    return false;
  }
  if (!append_common_event(&event, writer, "video-watermark-final", timestamp) ||
      !json_buffer_append_literal(&event, ",\"sourceSessionGeneration\":") ||
      !json_buffer_append_uint64(&event, source_session_generation) ||
      !json_buffer_append_literal(&event, ",\"sourceVideoSequence\":") ||
      !json_buffer_append_uint64(&event, source_video_sequence) ||
      !json_buffer_append_char(&event, '}')) {
    json_buffer_clear(&event);
    return false;
  }
  return emit_log_event(writer, &event);
}

bool source_clock_event_write_video_proof(SourceClockEventWriter *writer,
    SourceClockVideoProofEvent stage, uint32_t generation, uint32_t sequence,
    uint64_t running_time_ns) {
  char timestamp[64];
  JsonBuffer event = {0};
  const char *name;
  switch (stage) {
    case SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR: name = "video-input-idr"; break;
    case SOURCE_CLOCK_PROOF_EVENT_DECODED: name = "video-decoded"; break;
    case SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR: name = "video-encoded-idr"; break;
    default: return false;
  }
  if (!writer_is_valid(writer) || generation == 0 || sequence == 0 ||
      !utc_timestamp(timestamp, sizeof(timestamp))) return false;
  if (!append_common_event(&event, writer, name, timestamp) ||
      !json_buffer_append_literal(&event, ",\"sourceSessionGeneration\":") ||
      !json_buffer_append_uint64(&event, generation) ||
      !json_buffer_append_literal(&event, ",\"sourceVideoSequence\":") ||
      !json_buffer_append_uint64(&event, sequence) ||
      !json_buffer_append_literal(&event, ",\"runningTimeNs\":") ||
      !json_buffer_append_uint64(&event, running_time_ns) ||
      (stage == SOURCE_CLOCK_PROOF_EVENT_DECODED &&
       !json_buffer_append_literal(&event, ",\"videoDecoded\":true")) ||
      !json_buffer_append_char(&event, '}')) {
    json_buffer_clear(&event);
    return false;
  }
  return stage == SOURCE_CLOCK_PROOF_EVENT_DECODED
             ? emit_event(writer, writer->media_ready_path, &event)
             : emit_log_event(writer, &event);
}

bool source_clock_event_write_recording_finalized(SourceClockEventWriter *writer,
                                                  const char *recording_path) {
  char timestamp[64];
  JsonBuffer event = {0};
  if (!writer_is_valid(writer) || !nonempty(recording_path) ||
      !utc_timestamp(timestamp, sizeof(timestamp))) {
    return false;
  }
  if (!append_common_event(&event, writer, "recording-finalized", timestamp) ||
      !json_buffer_append_literal(&event, ",\"recordingPath\":") ||
      !json_buffer_append_string(&event, recording_path) ||
      !json_buffer_append_literal(&event, ",\"recordingClosed\":true}")) {
    json_buffer_clear(&event);
    return false;
  }
  return emit_log_event(writer, &event);
}

bool source_clock_event_write_rtsp_eof(SourceClockEventWriter *writer,
                                       int process_id) {
  char timestamp[64];
  JsonBuffer event = {0};
  if (!writer_is_valid(writer) || process_id <= 0 ||
      !utc_timestamp(timestamp, sizeof(timestamp))) {
    return false;
  }
  if (!append_common_event(&event, writer, "rtsp-eof", timestamp) ||
      !json_buffer_append_literal(&event, ",\"processId\":") ||
      !json_buffer_append_int(&event, process_id) ||
      !json_buffer_append_char(&event, '}')) {
    json_buffer_clear(&event);
    return false;
  }
  return emit_log_event(writer, &event);
}
