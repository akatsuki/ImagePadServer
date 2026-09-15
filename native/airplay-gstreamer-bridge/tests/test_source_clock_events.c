#ifndef _WIN32
#define _POSIX_C_SOURCE 200809L
#endif

#include "source_clock_events.h"
#include "source_clock_media_ready.h"
#include "test_check.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <wchar.h>

#ifdef _WIN32
#include <windows.h>
#define TEST_PATH_SEPARATOR "\\"
#else
#include <sys/stat.h>
#include <unistd.h>
#define TEST_PATH_SEPARATOR "/"
#endif

#define TEST_PATH_BYTES 1024

typedef struct {
  SourceClockEventWriter writer;
  char ready_path[TEST_PATH_BYTES];
  char media_ready_path[TEST_PATH_BYTES];
  char event_log_path[TEST_PATH_BYTES];
  char recording_path[TEST_PATH_BYTES];
} WriterFixture;

static bool media_ready_test_write_result;
static unsigned media_ready_test_write_calls;

static bool media_ready_test_write(SourceClockEventWriter *writer,
                                   const uint64_t *running_time_ns,
                                   const uint64_t *source_ntp_ns) {
  (void) writer;
  TEST_CHECK(running_time_ns != NULL);
  TEST_CHECK(source_ntp_ns == NULL);
  media_ready_test_write_calls++;
  return media_ready_test_write_result;
}

#ifdef _WIN32
static char *test_utf8_from_wide(const wchar_t *value) {
  int bytes = WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS,
                                  value, -1, NULL, 0, NULL, NULL);
  char *utf8;
  if (bytes <= 0) {
    return NULL;
  }
  utf8 = (char *) malloc((size_t) bytes);
  if (utf8 == NULL) {
    return NULL;
  }
  if (WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS,
                          value, -1, utf8, bytes, NULL, NULL) != bytes) {
    free(utf8);
    return NULL;
  }
  return utf8;
}

static wchar_t *test_wide_from_utf8(const char *value) {
  int characters = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS,
                                        value, -1, NULL, 0);
  wchar_t *wide;
  if (characters <= 0) {
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
#endif

static FILE *test_open_utf8_read(const char *path) {
#ifdef _WIN32
  wchar_t *wide_path = test_wide_from_utf8(path);
  FILE *file;
  if (wide_path == NULL) {
    return NULL;
  }
  file = _wfopen(wide_path, L"rb");
  free(wide_path);
  return file;
#else
  return fopen(path, "rb");
#endif
}

static FILE *test_open_utf8_write(const char *path) {
#ifdef _WIN32
  wchar_t *wide_path = test_wide_from_utf8(path);
  FILE *file;
  if (wide_path == NULL) {
    return NULL;
  }
  file = _wfopen(wide_path, L"wb");
  free(wide_path);
  return file;
#else
  return fopen(path, "wb");
#endif
}

static void test_delete_utf8_file(const char *path) {
#ifdef _WIN32
  wchar_t *wide_path = test_wide_from_utf8(path);
  if (wide_path != NULL) {
    _wremove(wide_path);
    free(wide_path);
  }
#else
  remove(path);
#endif
}

static void test_remove_utf8_directory(const char *path) {
#ifdef _WIN32
  wchar_t *wide_path = test_wide_from_utf8(path);
  if (wide_path != NULL) {
    RemoveDirectoryW(wide_path);
    free(wide_path);
  }
#else
  rmdir(path);
#endif
}

static bool join_path(char *output, size_t output_bytes,
                      const char *directory, const char *name) {
  int written = snprintf(output, output_bytes, "%s%s%s",
                         directory, TEST_PATH_SEPARATOR, name);
  return written > 0 && (size_t) written < output_bytes;
}

static bool create_test_directory(char *output, size_t output_bytes) {
#ifdef _WIN32
  wchar_t temporary_root[MAX_PATH];
  wchar_t temporary_file[MAX_PATH];
  DWORD root_characters = GetTempPathW(MAX_PATH, temporary_root);
  char *utf8_directory;
  int written;
  if (root_characters == 0 || root_characters >= MAX_PATH ||
      GetTempFileNameW(temporary_root, L"sce", 0, temporary_file) == 0) {
    return false;
  }
  if (!DeleteFileW(temporary_file) ||
      !CreateDirectoryW(temporary_file, NULL)) {
    return false;
  }
  utf8_directory = test_utf8_from_wide(temporary_file);
  if (utf8_directory == NULL) {
    RemoveDirectoryW(temporary_file);
    return false;
  }
  written = snprintf(output, output_bytes, "%s", utf8_directory);
  free(utf8_directory);
  if (written <= 0 || (size_t) written >= output_bytes) {
    RemoveDirectoryW(temporary_file);
    return false;
  }
  return true;
#else
  char pattern[] = "/tmp/imagepad-source-clock-events-XXXXXX";
  char *directory = mkdtemp(pattern);
  if (directory == NULL) {
    return false;
  }
  return snprintf(output, output_bytes, "%s", directory) > 0 &&
         strlen(directory) < output_bytes;
#endif
}

static void remove_test_directory(const char *directory) {
  test_remove_utf8_directory(directory);
}

static bool file_exists(const char *path) {
  FILE *file = test_open_utf8_read(path);
  if (file == NULL) {
    return false;
  }
  fclose(file);
  return true;
}

static char *read_file(const char *path) {
  FILE *file = test_open_utf8_read(path);
  long length;
  char *contents;
  size_t bytes_read;
  if (file == NULL || fseek(file, 0, SEEK_END) != 0) {
    if (file != NULL) {
      fclose(file);
    }
    return NULL;
  }
  length = ftell(file);
  if (length < 0 || fseek(file, 0, SEEK_SET) != 0) {
    fclose(file);
    return NULL;
  }
  contents = (char *) malloc((size_t) length + 1);
  if (contents == NULL) {
    fclose(file);
    return NULL;
  }
  bytes_read = fread(contents, 1, (size_t) length, file);
  fclose(file);
  if (bytes_read != (size_t) length) {
    free(contents);
    return NULL;
  }
  contents[length] = '\0';
  return contents;
}

static void write_file(const char *path, const char *contents) {
  FILE *file = test_open_utf8_write(path);
  size_t length = strlen(contents);
  TEST_CHECK(file != NULL);
  TEST_CHECK(fwrite(contents, 1, length, file) == length);
  TEST_CHECK(fclose(file) == 0);
}

static void check_file_contents(const char *path, const char *expected) {
  char *actual = read_file(path);
  TEST_CHECK(actual != NULL);
  TEST_CHECK(strcmp(actual, expected) == 0);
  free(actual);
}

static size_t count_lines(const char *contents) {
  size_t lines = 0;
  const char *cursor;
  for (cursor = contents; *cursor != '\0'; cursor++) {
    if (*cursor == '\n') {
      lines++;
    }
  }
  return lines;
}

static void initialize_fixture(WriterFixture *fixture, const char *directory,
                               const char *session_id, uint64_t generation) {
  TEST_CHECK(join_path(fixture->ready_path, sizeof(fixture->ready_path),
                       directory, "publisher-ready.json"));
  TEST_CHECK(join_path(fixture->media_ready_path, sizeof(fixture->media_ready_path),
                       directory, "video-decoded.json"));
  TEST_CHECK(join_path(fixture->event_log_path, sizeof(fixture->event_log_path),
                       directory, "events.jsonl"));
  TEST_CHECK(join_path(fixture->recording_path, sizeof(fixture->recording_path),
                       directory, "publisher-0007.mp4"));
  TEST_CHECK(source_clock_event_writer_init(
      &fixture->writer, session_id, generation,
      fixture->ready_path, fixture->media_ready_path, fixture->event_log_path));
}

static void cleanup_fixture(const WriterFixture *fixture, const char *directory) {
  char temporary_path[TEST_PATH_BYTES];
  test_delete_utf8_file(fixture->ready_path);
  test_delete_utf8_file(fixture->media_ready_path);
  test_delete_utf8_file(fixture->event_log_path);
  TEST_CHECK(snprintf(temporary_path, sizeof(temporary_path), "%s.tmp",
                      fixture->ready_path) > 0);
  test_delete_utf8_file(temporary_path);
  TEST_CHECK(snprintf(temporary_path, sizeof(temporary_path), "%s.tmp",
                      fixture->media_ready_path) > 0);
  test_delete_utf8_file(temporary_path);
  remove_test_directory(directory);
}

static void test_rejects_invalid_identity_and_paths(void) {
  SourceClockEventWriter writer;
  TEST_CHECK(!source_clock_event_writer_init(
      NULL, "session", 1, "ready", "media", "events"));
  TEST_CHECK(!source_clock_event_writer_init(
      &writer, "", 1, "ready", "media", "events"));
  TEST_CHECK(!source_clock_event_writer_init(
      &writer, "session", 0, "ready", "media", "events"));
  TEST_CHECK(!source_clock_event_writer_init(
      &writer, "session", 1, "", "media", "events"));
  TEST_CHECK(!source_clock_event_writer_init(
      &writer, "session", 1, "ready", "", "events"));
  TEST_CHECK(!source_clock_event_writer_init(
      &writer, "session", 1, "ready", "media", ""));
}

static void test_rejects_invalid_event_inputs(void) {
  char directory[TEST_PATH_BYTES];
  WriterFixture fixture;
  TEST_CHECK(create_test_directory(directory, sizeof(directory)));
  initialize_fixture(&fixture, directory, "validation-session", 9);

  TEST_CHECK(!source_clock_event_write_publisher_ready(&fixture.writer, 0, 41002));
  TEST_CHECK(!source_clock_event_write_publisher_ready(&fixture.writer, 65535, 41002));
  TEST_CHECK(!source_clock_event_write_publisher_ready(&fixture.writer, 41001, 0));
  TEST_CHECK(!source_clock_event_write_publisher_ready(&fixture.writer, 41001, 65535));
  TEST_CHECK(!source_clock_event_write_video_decoded(&fixture.writer, NULL, NULL));
  TEST_CHECK(!source_clock_event_write_recording_finalized(&fixture.writer, NULL));
  TEST_CHECK(!source_clock_event_write_recording_finalized(&fixture.writer, ""));
  TEST_CHECK(!source_clock_event_write_rtsp_eof(&fixture.writer, 0));
  TEST_CHECK(!source_clock_event_write_rtsp_eof(&fixture.writer, -1));
  TEST_CHECK(!file_exists(fixture.ready_path));
  TEST_CHECK(!file_exists(fixture.media_ready_path));
  TEST_CHECK(!file_exists(fixture.event_log_path));

  cleanup_fixture(&fixture, directory);
}

static void test_writes_rtsp_eof_as_log_only_event(void) {
  char directory[TEST_PATH_BYTES];
  WriterFixture fixture;
  char *events;
  TEST_CHECK(create_test_directory(directory, sizeof(directory)));
  initialize_fixture(&fixture, directory, "rtsp-eof-session", 11);

  TEST_CHECK(source_clock_event_write_rtsp_eof(&fixture.writer, 4321));
  TEST_CHECK(!file_exists(fixture.ready_path));
  TEST_CHECK(!file_exists(fixture.media_ready_path));
  events = read_file(fixture.event_log_path);
  TEST_CHECK(events != NULL);
  TEST_CHECK(count_lines(events) == 1);
  TEST_CHECK(strstr(events, "\"event\":\"rtsp-eof\"") != NULL);
  TEST_CHECK(strstr(events, "\"processId\":4321") != NULL);
  free(events);

  cleanup_fixture(&fixture, directory);
}

static void test_writes_video_stage_events_as_log_only_events(void) {
  char directory[TEST_PATH_BYTES];
  WriterFixture fixture;
  uint64_t input_running_time_ns = 100;
  uint64_t input_source_ntp_ns = 200;
  uint64_t encoded_running_time_ns = 300;
  char *events;
  const char *input;
  const char *encoded;
  TEST_CHECK(create_test_directory(directory, sizeof(directory)));
  initialize_fixture(&fixture, directory, "video-stage-session", 12);

  TEST_CHECK(source_clock_event_write_video_input_idr(
      &fixture.writer, &input_running_time_ns, &input_source_ntp_ns));
  TEST_CHECK(source_clock_event_write_video_encoded(
      &fixture.writer, &encoded_running_time_ns));
  TEST_CHECK(!file_exists(fixture.ready_path));
  TEST_CHECK(!file_exists(fixture.media_ready_path));
  events = read_file(fixture.event_log_path);
  TEST_CHECK(events != NULL);
  TEST_CHECK(count_lines(events) == 2);
  input = strstr(events, "\"event\":\"video-input-idr\"");
  encoded = strstr(events, "\"event\":\"video-encoded\"");
  TEST_CHECK(input != NULL && encoded != NULL && input < encoded);
  TEST_CHECK(strstr(input, "\"runningTimeNs\":100") != NULL);
  TEST_CHECK(strstr(input, "\"sourceNtpNs\":200") != NULL);
  TEST_CHECK(strstr(encoded, "\"runningTimeNs\":300") != NULL);
  free(events);

  cleanup_fixture(&fixture, directory);
}

static void test_writes_video_watermark_final_log_only_event(void) {
  static const char ready_sentinel[] = "ready-sentinel\n";
  static const char media_sentinel[] = "media-sentinel\n";
  char directory[TEST_PATH_BYTES];
  char invalid_event_log_path[TEST_PATH_BYTES];
  WriterFixture fixture;
  SourceClockEventWriter invalid_writer;
  char *events;
  int written;

  TEST_CHECK(create_test_directory(directory, sizeof(directory)));
  initialize_fixture(&fixture, directory, "watermark-session", 13);
  write_file(fixture.ready_path, ready_sentinel);
  write_file(fixture.media_ready_path, media_sentinel);

  TEST_CHECK(!source_clock_event_write_video_watermark_final(NULL, 17, 29));
  TEST_CHECK(!source_clock_event_write_video_watermark_final(
      &fixture.writer, 0, 29));
  TEST_CHECK(!source_clock_event_write_video_watermark_final(
      &fixture.writer, 17, 0));
  check_file_contents(fixture.ready_path, ready_sentinel);
  check_file_contents(fixture.media_ready_path, media_sentinel);
  TEST_CHECK(!file_exists(fixture.event_log_path));

  {
    SourceClockEventWriter invalid_writers[9];
    size_t index;
    for (index = 0; index < 9; ++index) invalid_writers[index] = fixture.writer;
    invalid_writers[0].session_id = NULL;
    invalid_writers[1].session_id = "";
    invalid_writers[2].publisher_generation = 0;
    invalid_writers[3].ready_path = NULL;
    invalid_writers[4].ready_path = "";
    invalid_writers[5].media_ready_path = NULL;
    invalid_writers[6].media_ready_path = "";
    invalid_writers[7].event_log_path = NULL;
    invalid_writers[8].event_log_path = "";
    for (index = 0; index < 9; ++index) {
      TEST_CHECK(!source_clock_event_write_video_watermark_final(
          &invalid_writers[index], 17, 29));
      check_file_contents(fixture.ready_path, ready_sentinel);
      check_file_contents(fixture.media_ready_path, media_sentinel);
      TEST_CHECK(!file_exists(fixture.event_log_path));
    }
  }

  written = snprintf(invalid_event_log_path, sizeof(invalid_event_log_path),
                     "%s%smissing%swatermark.jsonl", directory,
                     TEST_PATH_SEPARATOR, TEST_PATH_SEPARATOR);
  TEST_CHECK(written > 0 && (size_t) written < sizeof(invalid_event_log_path));
  invalid_writer = fixture.writer;
  invalid_writer.event_log_path = invalid_event_log_path;
  TEST_CHECK(!source_clock_event_write_video_watermark_final(
      &invalid_writer, 17, 29));
  TEST_CHECK(!file_exists(invalid_event_log_path));
  check_file_contents(fixture.ready_path, ready_sentinel);
  check_file_contents(fixture.media_ready_path, media_sentinel);

  TEST_CHECK(source_clock_event_write_video_watermark_final(
      &fixture.writer, 17, 29));
  check_file_contents(fixture.ready_path, ready_sentinel);
  check_file_contents(fixture.media_ready_path, media_sentinel);
  events = read_file(fixture.event_log_path);
  TEST_CHECK(events != NULL);
  TEST_CHECK(count_lines(events) == 1);
  TEST_CHECK(strstr(
      events,
      "{\"schema\":2,\"sessionId\":\"watermark-session\","
      "\"publisherGeneration\":13,\"event\":\"video-watermark-final\","
      "\"at\":\"") == events);
  TEST_CHECK(strstr(events,
                    "\",\"sourceSessionGeneration\":17,"
                    "\"sourceVideoSequence\":29}\n") != NULL);
  TEST_CHECK(strstr(events, "\"runningTimeNs\"") == NULL);
  TEST_CHECK(strstr(events, "\"sourceNtpNs\"") == NULL);
  TEST_CHECK(strstr(events, "\"recordingPath\"") == NULL);
  free(events);

  cleanup_fixture(&fixture, directory);
}

static void test_writes_replaces_and_appends_schema_v2_events(void) {
  char directory[TEST_PATH_BYTES];
  char temporary_path[TEST_PATH_BYTES];
  WriterFixture fixture;
  uint64_t running_time_zero = 0;
  uint64_t running_time_next = 99;
  uint64_t source_ntp = 123456789;
  char *ready;
  char *media;
  char *events;
  const char *first;
  const char *second;
  const char *third;
  const char *fourth;

  TEST_CHECK(create_test_directory(directory, sizeof(directory)));
  initialize_fixture(&fixture, directory, "fixture-\"slash\\line\n", 7);

  TEST_CHECK(source_clock_event_write_publisher_ready(&fixture.writer, 41001, 41002));
  TEST_CHECK(source_clock_event_write_video_decoded(
      &fixture.writer, &running_time_zero, NULL));
  TEST_CHECK(source_clock_event_write_recording_finalized(
      &fixture.writer, "recording-\"slash\\line\n.mp4"));

  ready = read_file(fixture.ready_path);
  media = read_file(fixture.media_ready_path);
  events = read_file(fixture.event_log_path);
  TEST_CHECK(ready != NULL && media != NULL && events != NULL);
  TEST_CHECK(strstr(ready, "\"schema\":2") != NULL);
  TEST_CHECK(strstr(ready, "\"sessionId\":\"fixture-\\\"slash\\\\line\\n\"") != NULL);
  TEST_CHECK(strstr(ready, "\"publisherGeneration\":7") != NULL);
  TEST_CHECK(strstr(ready, "\"event\":\"publisher-ready\"") != NULL);
  TEST_CHECK(strstr(ready, "\"protocolVersion\":1") != NULL);
  TEST_CHECK(strstr(ready, "\"videoListenPort\":41001") != NULL);
  TEST_CHECK(strstr(ready, "\"audioListenPort\":41002") != NULL);
  TEST_CHECK(strstr(ready, "\"pipelineStartAccepted\":true") != NULL);
  TEST_CHECK(strstr(ready, "\"at\":\"") != NULL);
  TEST_CHECK(strstr(media, "\"event\":\"video-decoded\"") != NULL);
  TEST_CHECK(strstr(media, "\"videoDecoded\":true") != NULL);
  TEST_CHECK(strstr(media, "\"runningTimeNs\":0") != NULL);
  TEST_CHECK(strstr(media, "sourceNtpNs") == NULL);
  TEST_CHECK(count_lines(events) == 3);
  first = strstr(events, "\"event\":\"publisher-ready\"");
  second = strstr(events, "\"event\":\"video-decoded\"");
  third = strstr(events, "\"event\":\"recording-finalized\"");
  TEST_CHECK(first != NULL && second != NULL && third != NULL);
  TEST_CHECK(first < second && second < third);
  TEST_CHECK(strstr(third,
                    "\"recordingPath\":\"recording-\\\"slash\\\\line\\n.mp4\"") != NULL);
  TEST_CHECK(strstr(third, "\"recordingClosed\":true") != NULL);
  free(ready);
  free(media);
  free(events);

  TEST_CHECK(source_clock_event_write_video_decoded(
      &fixture.writer, &running_time_next, &source_ntp));
  media = read_file(fixture.media_ready_path);
  TEST_CHECK(media != NULL);
  TEST_CHECK(strstr(media, "\"runningTimeNs\":99") != NULL);
  TEST_CHECK(strstr(media, "\"sourceNtpNs\":123456789") != NULL);
  TEST_CHECK(strstr(media, "\"runningTimeNs\":0") == NULL);
  free(media);

  TEST_CHECK(source_clock_event_write_publisher_ready(&fixture.writer, 42001, 42002));
  ready = read_file(fixture.ready_path);
  events = read_file(fixture.event_log_path);
  TEST_CHECK(ready != NULL && events != NULL);
  TEST_CHECK(strstr(ready, "\"videoListenPort\":42001") != NULL);
  TEST_CHECK(strstr(ready, "\"videoListenPort\":41001") == NULL);
  TEST_CHECK(count_lines(events) == 5);
  first = strstr(events, "\"event\":\"publisher-ready\"");
  second = first == NULL ? NULL : strstr(first + 1, "\"event\":\"video-decoded\"");
  third = second == NULL ? NULL : strstr(second + 1, "\"event\":\"recording-finalized\"");
  fourth = third == NULL ? NULL : strstr(third + 1, "\"event\":\"video-decoded\"");
  {
    const char *fifth = fourth == NULL ? NULL :
        strstr(fourth + 1, "\"event\":\"publisher-ready\"");
    TEST_CHECK(first != NULL && second != NULL && third != NULL &&
               fourth != NULL && fifth != NULL);
    TEST_CHECK(first < second && second < third && third < fourth &&
               fourth < fifth);
  }
  free(ready);
  free(events);

  TEST_CHECK(snprintf(temporary_path, sizeof(temporary_path), "%s.tmp",
                      fixture.ready_path) > 0);
  TEST_CHECK(!file_exists(temporary_path));
  TEST_CHECK(snprintf(temporary_path, sizeof(temporary_path), "%s.tmp",
                      fixture.media_ready_path) > 0);
  TEST_CHECK(!file_exists(temporary_path));

  cleanup_fixture(&fixture, directory);
}

static void test_decoded_callback_media_ready_decision_path(void) {
  SourceClockEventWriter writer;
  bool reserved = false;
  bool emitted = false;
  uint64_t reserved_running_time_ns = 0;
  SourceClockMediaReadyWriteResult write_result;

  memset(&writer, 0, sizeof(writer));
  media_ready_test_write_result = true;
  media_ready_test_write_calls = 0;

  TEST_CHECK(!source_clock_media_ready_reserve_after_offer(
      true, true, &reserved, 11, &reserved_running_time_ns));
  TEST_CHECK(!reserved);
  TEST_CHECK(!source_clock_media_ready_reserve_after_offer(
      false, false, &reserved, 22, &reserved_running_time_ns));
  TEST_CHECK(!reserved);

  TEST_CHECK(source_clock_media_ready_reserve_after_offer(
      false, true, &reserved, 33, &reserved_running_time_ns));
  TEST_CHECK(reserved);
  TEST_CHECK(reserved_running_time_ns == 33);
  TEST_CHECK(!source_clock_media_ready_reserve_after_offer(
      false, true, &reserved, 44, &reserved_running_time_ns));
  TEST_CHECK(reserved_running_time_ns == 33);

  write_result = source_clock_media_ready_write_reserved(
      &writer, reserved_running_time_ns, media_ready_test_write);
  TEST_CHECK(write_result == SOURCE_CLOCK_MEDIA_READY_WRITTEN);
  TEST_CHECK(media_ready_test_write_calls == 1);
  source_clock_media_ready_commit_write(write_result, &emitted);
  TEST_CHECK(emitted);

  reserved = false;
  emitted = false;
  reserved_running_time_ns = 0;
  media_ready_test_write_result = false;
  TEST_CHECK(source_clock_media_ready_reserve_after_offer(
      false, true, &reserved, 55, &reserved_running_time_ns));
  write_result = source_clock_media_ready_write_reserved(
      &writer, reserved_running_time_ns, media_ready_test_write);
  TEST_CHECK(write_result == SOURCE_CLOCK_MEDIA_READY_WRITE_FAILED);
  TEST_CHECK(media_ready_test_write_calls == 2);
  source_clock_media_ready_commit_write(write_result, &emitted);
  TEST_CHECK(!emitted);
}

static void test_candidate_proof_events(void) {
  char directory[TEST_PATH_BYTES];
  WriterFixture f;
  char *events, *media;
  TEST_CHECK(create_test_directory(directory, sizeof(directory)));
  initialize_fixture(&f, directory, "proof-session", 8);
  write_file(f.ready_path, "ready-sentinel\n");
  write_file(f.media_ready_path, "media-sentinel\n");
  TEST_CHECK(!source_clock_event_write_video_proof(NULL, SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR, 7, 101, 0));
  TEST_CHECK(!source_clock_event_write_video_proof(&f.writer, SOURCE_CLOCK_PROOF_EVENT_NONE, 7, 101, 0));
  TEST_CHECK(!source_clock_event_write_video_proof(&f.writer, (SourceClockVideoProofEvent)99, 7, 101, 0));
  TEST_CHECK(!source_clock_event_write_video_proof(&f.writer, SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR, 0, 101, 0));
  TEST_CHECK(!source_clock_event_write_video_proof(&f.writer, SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR, 7, 0, 0));
  TEST_CHECK(!file_exists(f.event_log_path));
  check_file_contents(f.ready_path, "ready-sentinel\n");
  check_file_contents(f.media_ready_path, "media-sentinel\n");
  TEST_CHECK(source_clock_event_write_video_proof(&f.writer, SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR, 7, 101, 0));
  check_file_contents(f.media_ready_path, "media-sentinel\n");
  TEST_CHECK(source_clock_event_write_video_proof(&f.writer, SOURCE_CLOCK_PROOF_EVENT_DECODED, 7, 101, 50));
  media = read_file(f.media_ready_path);
  TEST_CHECK(media != NULL);
  TEST_CHECK(strstr(media, "\"event\":\"video-decoded\"") != NULL);
  TEST_CHECK(strstr(media, "\"sourceSessionGeneration\":7,\"sourceVideoSequence\":101") != NULL);
  TEST_CHECK(strstr(media, "\"runningTimeNs\":50,\"videoDecoded\":true") != NULL);
  TEST_CHECK(source_clock_event_write_video_proof(&f.writer, SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR, 7, 101, 75));
  check_file_contents(f.media_ready_path, media);
  check_file_contents(f.ready_path, "ready-sentinel\n");
  events = read_file(f.event_log_path);
  TEST_CHECK(events != NULL && count_lines(events) == 3);
  TEST_CHECK(strstr(events, "\"event\":\"video-input-idr\"") != NULL);
  TEST_CHECK(strstr(events, "\"event\":\"video-encoded-idr\"") != NULL);
  free(events); free(media);
  {
    SourceClockEventWriter bad = f.writer;
    bad.event_log_path = directory; /* a directory cannot be opened as an append file */
    TEST_CHECK(!source_clock_event_write_video_proof(&bad, SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR, 7, 101, 0));
  }
  cleanup_fixture(&f, directory);
}

static int emit_boundary_files(const char *directory) {
  WriterFixture fixture;
  uint64_t running_time_zero = 0;
  initialize_fixture(&fixture, directory, "fixture-session-001", 7);
  if (!source_clock_event_write_publisher_ready(&fixture.writer, 41001, 41002) ||
      !source_clock_event_write_video_decoded(
          &fixture.writer, &running_time_zero, NULL) ||
      !source_clock_event_write_rtsp_eof(&fixture.writer, 4321) ||
      !source_clock_event_write_recording_finalized(
          &fixture.writer, fixture.recording_path) ||
      !source_clock_event_write_video_watermark_final(&fixture.writer, 9, 100) ||
      !source_clock_event_write_video_proof(&fixture.writer,
          SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR, 9, 101, 0) ||
      !source_clock_event_write_video_proof(&fixture.writer,
          SOURCE_CLOCK_PROOF_EVENT_DECODED, 9, 101, 0) ||
      !source_clock_event_write_video_proof(&fixture.writer,
          SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR, 9, 101, 0)) {
    return 2;
  }
  return 0;
}

#ifdef _WIN32
int wmain(int argc, wchar_t **argv) {
  if (argc == 3 && wcscmp(argv[1], L"--emit-dir") == 0) {
    char *directory = test_utf8_from_wide(argv[2]);
    int result;
    if (directory == NULL) {
      return 2;
    }
    result = emit_boundary_files(directory);
    free(directory);
    return result;
  }
  TEST_CHECK(argc == 1);
  test_rejects_invalid_identity_and_paths();
  test_rejects_invalid_event_inputs();
  test_writes_replaces_and_appends_schema_v2_events();
  test_writes_video_stage_events_as_log_only_events();
  test_writes_video_watermark_final_log_only_event();
  test_candidate_proof_events();
  test_writes_rtsp_eof_as_log_only_event();
  test_decoded_callback_media_ready_decision_path();
  return 0;
}
#else
int main(int argc, char **argv) {
  if (argc == 3 && strcmp(argv[1], "--emit-dir") == 0) {
    return emit_boundary_files(argv[2]);
  }
  TEST_CHECK(argc == 1);
  test_rejects_invalid_identity_and_paths();
  test_rejects_invalid_event_inputs();
  test_writes_replaces_and_appends_schema_v2_events();
  test_writes_video_stage_events_as_log_only_events();
  test_writes_video_watermark_final_log_only_event();
  test_candidate_proof_events();
  test_writes_rtsp_eof_as_log_only_event();
  test_decoded_callback_media_ready_decision_path();
  return 0;
}
#endif
