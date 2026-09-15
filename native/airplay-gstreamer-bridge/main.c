#include "pipeline.h"
#include "source_clock_encoder_policy.h"

#include <errno.h>
#include <limits.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
#include <windows.h>
#endif

static void usage(void) {
  fprintf(stderr, "planned source-clock candidate only: --proof-source-generation N --proof-video-watermark N (both nonzero uint32)\n");
  fprintf(stderr, "usage: airplay-gstreamer-bridge --video-port PORT --audio-port PORT --stdout\n");
  fprintf(stderr, "   or: airplay-gstreamer-bridge --video-port PORT --audio-port PORT --publish-url URL --recording PATH --stop-file PATH --width N --height N --source-fps N --fps N --bitrate KBIT --maxrate KBIT --buffer-size KBIT --audio-bitrate BIT --gop N\n");
  fprintf(stderr, "   or: airplay-gstreamer-bridge --source-clock --video-listen-port PORT --audio-listen-port PORT --session-token HEX --session-id ID --publisher-generation N --ready-file PATH --media-ready-file PATH --event-log PATH --publish-url URL --recording PATH --stop-file PATH --width N --height N --source-fps N --fps N --bitrate KBIT --maxrate KBIT --buffer-size KBIT --audio-bitrate BIT --gop N --no-signal-seconds N\n");
  fprintf(stderr, "       comparison only: --source-clock-compare-tune TUNE [--source-clock-encoder-report PATH]\n");
}

static int parse_nonzero_uint64(const char *text, uint64_t *value) {
  char *end = NULL;
  const unsigned char *cursor;
  unsigned long long parsed;
  if (text == NULL || value == NULL || text[0] == '\0') {
    return 0;
  }
  for (cursor = (const unsigned char *)text; *cursor != '\0'; ++cursor) {
    if (*cursor < '0' || *cursor > '9') return 0;
  }
  errno = 0;
  parsed = strtoull(text, &end, 10);
  if (errno == ERANGE || end == text || *end != '\0' || parsed == 0) {
    return 0;
  }
  *value = (uint64_t)parsed;
  if ((unsigned long long)*value != parsed) {
    *value = 0;
    return 0;
  }
  return 1;
}

static int parse_nonnegative_int(const char *text, int *value) {
  char *end = NULL;
  const unsigned char *cursor;
  unsigned long parsed;
  if (text == NULL || value == NULL || text[0] == '\0') return 0;
  for (cursor = (const unsigned char *)text; *cursor != '\0'; ++cursor) {
    if (*cursor < '0' || *cursor > '9') return 0;
  }
  errno = 0;
  parsed = strtoul(text, &end, 10);
  if (errno == ERANGE || end == text || *end != '\0' || parsed > INT_MAX) return 0;
  *value = (int)parsed;
  return 1;
}

static int run_main(int argc, char **argv) {
  int video_port = 0;
  int audio_port = 0;
  int stdout_mode = 0;
  const char *publish_url = NULL;
  const char *recording = NULL;
  const char *stop_file = NULL;
  int width = 1920;
  int height = 1080;
  int source_fps = 60;
  int fps = 30;
  int bitrate_kbps = 9000;
  int maxrate_kbps = 10400;
  int buffer_size_kbps = 18000;
  int audio_bitrate_bps = 160000;
  int gop = 30;
  int source_clock_mode = 0;
  int video_listen_port = 0;
  int audio_listen_port = 0;
  const char *session_token = NULL;
  const char *session_id = NULL;
  uint64_t publisher_generation = 0;
  uint64_t proof_source_generation = 0, proof_video_watermark = 0;
  const char *ready_file = NULL;
  const char *media_ready_file = NULL;
  const char *event_log = NULL;
  int no_signal_seconds = 180;
  const char *compare_tune = NULL;
  const char *encoder_report = NULL;
  for (int i = 1; i < argc; ++i) {
    if (strcmp(argv[i], "--source-clock") == 0) {
      source_clock_mode = 1;
    } else if (strcmp(argv[i], "--video-listen-port") == 0 && i + 1 < argc) {
      video_listen_port = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--audio-listen-port") == 0 && i + 1 < argc) {
      audio_listen_port = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--session-token") == 0 && i + 1 < argc) {
      session_token = argv[++i];
    } else if (strcmp(argv[i], "--session-id") == 0 && i + 1 < argc) {
      session_id = argv[++i];
    } else if (strcmp(argv[i], "--publisher-generation") == 0 && i + 1 < argc) {
      if (!parse_nonzero_uint64(argv[++i], &publisher_generation)) {
        usage();
        return 2;
      }
    } else if (strcmp(argv[i], "--proof-source-generation") == 0 && i + 1 < argc) {
      if (proof_source_generation != 0 ||
          !parse_nonzero_uint64(argv[++i], &proof_source_generation) || proof_source_generation > UINT32_MAX) {
        usage(); return 2;
      }
    } else if (strcmp(argv[i], "--proof-video-watermark") == 0 && i + 1 < argc) {
      if (proof_video_watermark != 0 ||
          !parse_nonzero_uint64(argv[++i], &proof_video_watermark) || proof_video_watermark > UINT32_MAX) {
        usage(); return 2;
      }
    } else if (strcmp(argv[i], "--ready-file") == 0 && i + 1 < argc) {
      ready_file = argv[++i];
    } else if (strcmp(argv[i], "--media-ready-file") == 0 && i + 1 < argc) {
      media_ready_file = argv[++i];
    } else if (strcmp(argv[i], "--event-log") == 0 && i + 1 < argc) {
      event_log = argv[++i];
    } else if (strcmp(argv[i], "--no-signal-seconds") == 0 && i + 1 < argc) {
      if (!parse_nonnegative_int(argv[++i], &no_signal_seconds)) {
        usage();
        return 2;
      }
    } else if (strcmp(argv[i], "--source-clock-compare-tune") == 0 && i + 1 < argc) {
      compare_tune = argv[++i];
    } else if (strcmp(argv[i], "--source-clock-encoder-report") == 0 && i + 1 < argc) {
      encoder_report = argv[++i];
    } else if (strcmp(argv[i], "--video-port") == 0 && i + 1 < argc) {
      video_port = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--audio-port") == 0 && i + 1 < argc) {
      audio_port = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--stdout") == 0) {
      stdout_mode = 1;
    } else if (strcmp(argv[i], "--publish-url") == 0 && i + 1 < argc) {
      publish_url = argv[++i];
    } else if (strcmp(argv[i], "--recording") == 0 && i + 1 < argc) {
      recording = argv[++i];
    } else if (strcmp(argv[i], "--stop-file") == 0 && i + 1 < argc) {
      stop_file = argv[++i];
    } else if (strcmp(argv[i], "--width") == 0 && i + 1 < argc) {
      width = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--height") == 0 && i + 1 < argc) {
      height = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--source-fps") == 0 && i + 1 < argc) {
      source_fps = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--fps") == 0 && i + 1 < argc) {
      fps = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--bitrate") == 0 && i + 1 < argc) {
      bitrate_kbps = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--maxrate") == 0 && i + 1 < argc) {
      maxrate_kbps = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--buffer-size") == 0 && i + 1 < argc) {
      buffer_size_kbps = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--audio-bitrate") == 0 && i + 1 < argc) {
      audio_bitrate_bps = atoi(argv[++i]);
    } else if (strcmp(argv[i], "--gop") == 0 && i + 1 < argc) {
      gop = atoi(argv[++i]);
    } else {
      usage();
      return 2;
    }
  }
  if (source_clock_mode) {
    if ((proof_source_generation == 0) != (proof_video_watermark == 0) ||
        video_listen_port < 0 || video_listen_port > 65534 ||
        audio_listen_port < 0 || audio_listen_port > 65534 ||
        ((video_listen_port == 0) != (audio_listen_port == 0)) ||
        session_token == NULL || session_token[0] == '\0' ||
        session_id == NULL || session_id[0] == '\0' || publisher_generation == 0 ||
        ready_file == NULL || ready_file[0] == '\0' ||
        media_ready_file == NULL || media_ready_file[0] == '\0' ||
        event_log == NULL || event_log[0] == '\0' || publish_url == NULL ||
        recording == NULL || stop_file == NULL ||
        width < 2 || height < 2 || source_fps < 1 || fps < 1 ||
        bitrate_kbps < 1 || maxrate_kbps < 1 || buffer_size_kbps < 1 ||
        audio_bitrate_bps < 1 || gop < 1 || no_signal_seconds < 0) {
      usage();
      return 2;
    }
    {
      GError *policy_error = NULL;
      if (!source_clock_encoder_policy_configure(compare_tune, encoder_report,
                                                 &policy_error)) {
        fprintf(stderr, "encoder comparison policy: %s\n",
                policy_error == NULL ? "configuration failed" : policy_error->message);
        g_clear_error(&policy_error);
        return 2;
      }
    }
    return airplay_source_clock_pipeline_run(publish_url, recording, stop_file, session_token,
                                             session_id, publisher_generation,
                                             ready_file, media_ready_file, event_log,
                                             video_listen_port, audio_listen_port,
                                             width, height, source_fps, fps,
                                             bitrate_kbps, maxrate_kbps,
                                             buffer_size_kbps, audio_bitrate_bps,
                                             gop, no_signal_seconds,
                                             (uint32_t)proof_source_generation, (uint32_t)proof_video_watermark);
  }
  if (proof_source_generation != 0 || proof_video_watermark != 0) { usage(); return 2; }
  if (compare_tune != NULL || encoder_report != NULL) { usage(); return 2; }
  if (video_port < 1 || video_port > 65534 || audio_port < 1 || audio_port > 65534) {
    usage();
    return 2;
  }
  if (stdout_mode && publish_url == NULL && recording == NULL) {
    return airplay_pipeline_run(video_port, audio_port);
  }
  if (stdout_mode || publish_url == NULL || recording == NULL || stop_file == NULL ||
      width < 2 || height < 2 || fps < 1 || bitrate_kbps < 1 ||
      maxrate_kbps < 1 || buffer_size_kbps < 1 || audio_bitrate_bps < 1 || gop < 1) {
    usage();
    return 2;
  }
  return airplay_direct_pipeline_run(video_port, audio_port, publish_url, recording, stop_file,
                                     width, height, fps, bitrate_kbps, maxrate_kbps,
                                     buffer_size_kbps, audio_bitrate_bps, gop);
}

#ifdef _WIN32
static char *utf8_from_wide(const wchar_t *value) {
  int bytes;
  char *utf8;
  if (value == NULL) return NULL;
  bytes = WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS,
                              value, -1, NULL, 0, NULL, NULL);
  if (bytes <= 0) return NULL;
  utf8 = (char *)malloc((size_t)bytes);
  if (utf8 == NULL) return NULL;
  if (WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS,
                          value, -1, utf8, bytes, NULL, NULL) != bytes) {
    free(utf8);
    return NULL;
  }
  return utf8;
}

int wmain(int argc, wchar_t **wide_argv) {
  char **argv;
  int i;
  int result;
  if (argc < 0 || wide_argv == NULL) return 2;
  argv = (char **)calloc((size_t)argc + 1, sizeof(char *));
  if (argv == NULL) return 2;
  for (i = 0; i < argc; ++i) {
    argv[i] = utf8_from_wide(wide_argv[i]);
    if (argv[i] == NULL) {
      while (i-- > 0) free(argv[i]);
      free(argv);
      return 2;
    }
  }
  result = run_main(argc, argv);
  for (i = 0; i < argc; ++i) free(argv[i]);
  free(argv);
  return result;
}
#else
int main(int argc, char **argv) {
  return run_main(argc, argv);
}
#endif
