#include "pipeline.h"
#include "rtsp_h264_contract.h"
#include "scheduled_video_buffer.h"
#include "source_clock_pipeline_internal.h"
#include "source_clock_encoder_policy.h"
#include "source_clock_protocol.h"
#include "source_clock_reader.h"
#include "source_clock_video_watermark.h"
#include "source_clock_timeline.h"
#include "source_clock_audio_frame_clock.h"
#include "source_clock_audio_caps_state.h"
#include "source_clock_audio_bin.h"
#include "source_clock_fixed_pcm_audio.h"
#include "source_clock_events.h"
#include "source_clock_media_ready.h"
#include "source_clock_recording_finalize.h"
#include "source_clock_rtsp_termination.h"
#include "source_clock_stop_request.h"
#include "video_input.h"
#include "video_au_gate.h"
#include "video_scheduler.h"
#include "source_clock_video_pending_fifo.h"

#include <gst/app/gstappsrc.h>
#include <gst/app/gstappsink.h>
#include <gst/gst.h>
#include <gst/video/video.h>
#include <glib/gstdio.h>

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* Opt-in comparison build only; the public internal factory stays compatible. */
#ifndef IMAGEPAD_SOURCE_CLOCK_START_WITHOUT_REAL_AUDIO
#define IMAGEPAD_SOURCE_CLOCK_START_WITHOUT_REAL_AUDIO 0
#endif

#ifndef IMAGEPAD_SOURCE_CLOCK_DYNAMIC_AUDIO_BIN
#define IMAGEPAD_SOURCE_CLOCK_DYNAMIC_AUDIO_BIN 0
#endif

#ifndef IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO
#define IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO 0
#endif

#ifndef IMAGEPAD_SOURCE_CLOCK_RTSP_VIDEO_ONLY
#define IMAGEPAD_SOURCE_CLOCK_RTSP_VIDEO_ONLY 0
#endif

#ifndef IMAGEPAD_SOURCE_CLOCK_AUTO_AUDIO_PAYLOADER
#define IMAGEPAD_SOURCE_CLOCK_AUTO_AUDIO_PAYLOADER 0
#endif

#ifndef IMAGEPAD_SOURCE_CLOCK_DISABLE_RTSP_RTX
#define IMAGEPAD_SOURCE_CLOCK_DISABLE_RTSP_RTX 0
#endif

#ifndef IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP
#define IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP 0
#endif

#ifdef _WIN32
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
typedef SOCKET SourceClockSocket;
#define SOURCE_CLOCK_INVALID_SOCKET INVALID_SOCKET
#else
#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>
typedef int SourceClockSocket;
#define SOURCE_CLOCK_INVALID_SOCKET (-1)
#endif

static void source_clock_socket_close(SourceClockSocket socket_fd) {
  if (socket_fd == SOURCE_CLOCK_INVALID_SOCKET) return;
#ifdef _WIN32
  (void)shutdown(socket_fd, SD_BOTH);
  (void)closesocket(socket_fd);
#else
  (void)shutdown(socket_fd, SHUT_RDWR);
  (void)close(socket_fd);
#endif
}

static gboolean source_clock_socket_readable(SourceClockSocket socket_fd, int timeout_ms) {
  fd_set read_set;
  struct timeval timeout;
  int result;
  if (socket_fd == SOURCE_CLOCK_INVALID_SOCKET) return FALSE;
  FD_ZERO(&read_set);
  FD_SET(socket_fd, &read_set);
  timeout.tv_sec = timeout_ms / 1000;
  timeout.tv_usec = (timeout_ms % 1000) * 1000;
#ifdef _WIN32
  result = select(0, &read_set, NULL, NULL, &timeout);
#else
  result = select(socket_fd + 1, &read_set, NULL, NULL, &timeout);
#endif
  return result > 0 && FD_ISSET(socket_fd, &read_set);
}

typedef struct SourceClockPipeline SourceClockPipeline;

typedef struct {
  SourceClockPipeline *pipeline;
  SourceClockSocket listener;
  uint8_t stream_kind;
} SourceClockAcceptContext;

/* Lifetime is one accepted TCP connection, never shared with the audio reader. */
typedef struct {
  SourceClockPipeline *pipeline;
  SourceClockVideoWatermarkConnection watermark;
} SourceClockConnectionContext;

typedef enum {
  VIDEO_WATERMARK_OPEN = 0,
  VIDEO_WATERMARK_FINALIZING,
  VIDEO_WATERMARK_WRITTEN,
  VIDEO_WATERMARK_UNAVAILABLE,
  VIDEO_WATERMARK_FAILED
} SourceClockWatermarkFinalState;

typedef struct {
  SourceClockSocket video;
  SourceClockSocket audio;
} SourceClockListenerPair;

typedef GThread *(*SourceClockThreadTryNew)(const gchar *name,
                                            GThreadFunc function,
                                            gpointer data,
                                            GError **error);

typedef enum {
  SOURCE_CLOCK_VIDEO_OUTPUT_BLACK = 0,
  SOURCE_CLOCK_VIDEO_OUTPUT_FRAME = 1,
  SOURCE_CLOCK_VIDEO_OUTPUT_HOLD = 2,
} SourceClockVideoSchedulerOutputOrigin;

typedef struct {
  SourceClockPipeline *owner;
  GMutex mutex;
  gboolean stopping;
  SourceClockVideoScheduler policy;
  GstAppSink *decoded_sink;
  GstAppSrc *output_source;
  GstClock *clock;
  GstClockID periodic_id;
  GstClockTime base_time;
  guint64 next_tick_absolute;
  GThread *thread;
  guint64 offered_frames;
  guint64 dropped_frames;
  guint64 held_frames;
  guint64 tick_count;
  guint64 emitted_frames;
  gint64 report_monotonic_us;
  guint64 report_tick_count;
  guint64 report_emitted_frames;
  guint64 report_held_frames;
  guint64 report_dropped_frames;
  GstBuffer *black_buffer;
} SourceClockVideoSchedulerRuntime;

struct SourceClockPipeline {
  GMainLoop *loop;
  GstElement *pipeline;
	GstElement *rtsp_sink;
  GstAppSrc *video_source;
  GstAppSrc *audio_source;
  GstAppSrc *fixed_pcm_source;
  GstPad *video_encoded_src_pad;
  gulong video_encoded_probe_id;
  /* Sole owner; reader access and session/format replacement require mutex. */
  SourceClockAudioBin *audio_bin;
  /* Immutable after startup; unlike audio_bin, safe to read before locking. */
  gboolean dynamic_audio_enabled;
  gboolean dynamic_audio_disabled;
  SourceClockFixedPcmAudio *fixed_pcm_audio;
  gboolean fixed_pcm_audio_enabled;
  gboolean fixed_pcm_audio_disabled;
  guint64 audio_admission_generation;
  SourceClockSocket video_listener;
  SourceClockSocket audio_listener;
  GThread *video_accept_thread;
  GThread *audio_accept_thread;
  GMutex mutex;
  GMutex event_mutex;
  SourceClockTimeline timeline;
  SourceClockAudioFrameClock audio_frame_clock;
  SourceClockAudioCapsState audio_caps_state;
  SourceClockVideoAUGate video_au_gate;
  SourceClockVideoBootstrap video_bootstrap;
  SourceClockVideoWatermark video_watermark;
  gboolean video_watermark_invalid;
  SourceClockWatermarkFinalState video_watermark_final_state;
  gboolean candidate_proof_enabled; /* immutable before readers/scheduler start */
  SourceClockVideoProof candidate_proof;
  SourceClockPendingFrame candidate_pending;
  GstBuffer *candidate_decoded_buffer;
  uint64_t candidate_stage_time[4];
  SourceClockVideoSchedulerRuntime video_scheduler;
  uint8_t token[SOURCE_CLOCK_SESSION_TOKEN_BYTES];
  gint64 started_monotonic_us;
  gint64 last_signal_monotonic_us;
  gint no_signal_seconds;
  const char *stop_file;
  SourceClockEventWriter event_writer;
  bool media_ready_reserved;
  bool media_ready_emitted;
	bool video_input_idr_reserved;
	bool video_input_idr_emitted;
	bool video_encode_armed;
	bool video_encoded_reserved;
	bool video_encoded_emitted;
	bool rtsp_eof_emitted;
  gboolean stopping;
  gboolean fatal_error;
  gboolean protocol_error;
  gboolean no_signal;
  int exit_code;
  guint stop_watch_id;
  guint bus_watch_id;
  SourceClockPendingFrame pending_video;
  SourceClockPendingFrame pending_audio;
  SourceClockVideoPendingFifo video_pending_fifo;
  GMutex video_feed_mutex;
  gboolean video_feed_mutex_initialized;
  guint debug_video_frames;
  guint debug_audio_frames;
  uint8_t video_codec;
};

static gboolean source_clock_candidate_tick(SourceClockPipeline *p, uint64_t tick_ns);
static gboolean source_clock_candidate_decoded(SourceClockPipeline *p, GstBuffer *buffer);
static gboolean source_clock_candidate_encoded(SourceClockPipeline *p, GstBuffer *buffer);

static SourceClockListenerPair source_clock_take_listeners(
    SourceClockPipeline *pipeline) {
  SourceClockListenerPair listeners = {
      SOURCE_CLOCK_INVALID_SOCKET, SOURCE_CLOCK_INVALID_SOCKET};
  if (pipeline == NULL) return listeners;
  g_mutex_lock(&pipeline->mutex);
  listeners.video = pipeline->video_listener;
  listeners.audio = pipeline->audio_listener;
  pipeline->video_listener = SOURCE_CLOCK_INVALID_SOCKET;
  pipeline->audio_listener = SOURCE_CLOCK_INVALID_SOCKET;
  g_mutex_unlock(&pipeline->mutex);
  return listeners;
}

static void source_clock_close_listeners(SourceClockPipeline *pipeline) {
  SourceClockListenerPair listeners = source_clock_take_listeners(pipeline);
  source_clock_socket_close(listeners.video);
  source_clock_socket_close(listeners.audio);
}

static gboolean source_clock_debug_enabled(void) {
  const gchar *value = g_getenv("IMAGEPAD_SOURCE_CLOCK_DEBUG");
  return value != NULL && value[0] != '\0' && g_strcmp0(value, "0") != 0;
}

static void source_clock_log_video_shape(const char *stage,
                                         const SourceClockHeader *header,
                                         const uint8_t *payload,
                                         size_t payload_bytes,
                                         size_t cached_configuration_bytes) {
  GString *types;
  size_t i;
  unsigned found = 0;
  if (stage == NULL || header == NULL || payload == NULL || payload_bytes == 0) return;
  types = g_string_new(NULL);
  for (i = 0; i + 3 < payload_bytes && found < 16; ++i) {
    size_t nal = 0;
    if (payload[i] == 0 && payload[i + 1] == 0 && payload[i + 2] == 1) {
      nal = i + 3;
    } else if (i + 4 < payload_bytes && payload[i] == 0 && payload[i + 1] == 0 &&
               payload[i + 2] == 0 && payload[i + 3] == 1) {
      nal = i + 4;
    }
    if (nal == 0 || nal >= payload_bytes) continue;
    if (found != 0) g_string_append_c(types, ',');
    g_string_append_printf(types, "%u",
                           header->codec == SOURCE_CLOCK_CODEC_H265_ANNEXB_AU
                               ? (unsigned)((payload[nal] >> 1) & 0x3f)
                               : (unsigned)(payload[nal] & 0x1f));
    found++;
    i = nal;
  }
  g_printerr("AirPlay source-clock video shape stage=%s seq=%u flags=0x%04x "
             "bytes=%zu cached=%zu start-codes=%u nal-types=%s first=%02x%02x%02x%02x\n",
             stage, header->sequence, header->flags, payload_bytes,
             cached_configuration_bytes, found, types->str,
             payload_bytes > 0 ? payload[0] : 0,
             payload_bytes > 1 ? payload[1] : 0,
             payload_bytes > 2 ? payload[2] : 0,
             payload_bytes > 3 ? payload[3] : 0);
  g_string_free(types, TRUE);
}

static gint64 monotonic_ns(void) {
  return g_get_monotonic_time() * 1000;
}

static gboolean decode_token(const char *hex, uint8_t token[SOURCE_CLOCK_SESSION_TOKEN_BYTES]) {
  size_t i;
  if (hex == NULL || strlen(hex) != SOURCE_CLOCK_SESSION_TOKEN_BYTES * 2) return FALSE;
  for (i = 0; i < SOURCE_CLOCK_SESSION_TOKEN_BYTES; ++i) {
    int high;
    int low;
    char h = hex[i * 2];
    char l = hex[i * 2 + 1];
    high = h >= '0' && h <= '9' ? h - '0' : h >= 'a' && h <= 'f' ? h - 'a' + 10 : h >= 'A' && h <= 'F' ? h - 'A' + 10 : -1;
    low = l >= '0' && l <= '9' ? l - '0' : l >= 'a' && l <= 'f' ? l - 'a' + 10 : l >= 'A' && l <= 'F' ? l - 'A' + 10 : -1;
    if (high < 0 || low < 0) return FALSE;
    token[i] = (uint8_t)((high << 4) | low);
  }
  return TRUE;
}

static gboolean bind_listener(SourceClockSocket *out_socket, unsigned requested_port, unsigned *out_port) {
  SourceClockSocket socket_fd;
  struct sockaddr_in address;
  socklen_t address_length = sizeof(address);
  socket_fd = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
  if (socket_fd == SOURCE_CLOCK_INVALID_SOCKET) return FALSE;
  memset(&address, 0, sizeof(address));
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  address.sin_port = htons((uint16_t)requested_port);
  if (bind(socket_fd, (struct sockaddr *)&address, sizeof(address)) != 0 ||
      listen(socket_fd, 4) != 0 ||
      getsockname(socket_fd, (struct sockaddr *)&address, &address_length) != 0) {
    source_clock_socket_close(socket_fd);
    return FALSE;
  }
  *out_socket = socket_fd;
  *out_port = ntohs(address.sin_port);
  return TRUE;
}

static void signal_pipeline_error(SourceClockPipeline *pipeline, gboolean protocol) {
  g_mutex_lock(&pipeline->mutex);
  if (protocol) pipeline->protocol_error = TRUE;
  else pipeline->fatal_error = TRUE;
  pipeline->stopping = TRUE;
  pipeline->exit_code = protocol ? 21 : 22;
  g_mutex_unlock(&pipeline->mutex);
  if (pipeline->loop != NULL) g_main_loop_quit(pipeline->loop);
}

static void clear_pending_frame(SourceClockPendingFrame *pending) {
  source_clock_video_pending_frame_clear(pending);
}

static void release_scheduled_video_buffer(void *opaque) {
  if (opaque != NULL) gst_buffer_unref(GST_BUFFER(opaque));
}

static GstBuffer *create_black_video_buffer(GstAppSrc *output_source,
                                            GError **error) {
  GstCaps *caps = NULL;
  GstVideoInfo info;
  GstVideoFrame frame;
  GstBuffer *buffer = NULL;
  gboolean mapped = FALSE;
  guint plane;
  gsize size;
  if (output_source == NULL) {
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_NEGOTIATION,
                "source-clock black video output source is missing");
    return NULL;
  }
  g_object_get(output_source, "caps", &caps, NULL);
  if (caps == NULL || gst_caps_is_empty(caps) ||
      !gst_video_info_from_caps(&info, caps) ||
      GST_VIDEO_INFO_FORMAT(&info) != GST_VIDEO_FORMAT_I420) {
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_NEGOTIATION,
                "source-clock black video requires fixed I420 caps");
    if (caps != NULL) gst_caps_unref(caps);
    return NULL;
  }
  size = GST_VIDEO_INFO_SIZE(&info);
  if (size == 0 || size > G_MAXSIZE) {
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_NEGOTIATION,
                "source-clock black video I420 size is invalid");
    gst_caps_unref(caps);
    return NULL;
  }
  buffer = gst_buffer_new_allocate(NULL, size, NULL);
  if (buffer == NULL || !gst_buffer_memset(buffer, 0, 0, size)) {
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_FAILED,
                "source-clock black video buffer allocation failed");
    if (buffer != NULL) gst_buffer_unref(buffer);
    gst_caps_unref(caps);
    return NULL;
  }
  if (!gst_video_frame_map(&frame, &info, buffer, GST_MAP_WRITE)) {
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_FAILED,
                "source-clock black video buffer map failed");
    gst_buffer_unref(buffer);
    gst_caps_unref(caps);
    return NULL;
  }
  mapped = TRUE;
  for (plane = 0; plane < GST_VIDEO_INFO_N_PLANES(&info); ++plane) {
    gint stride = GST_VIDEO_FRAME_PLANE_STRIDE(&frame, plane);
    guint width = GST_VIDEO_FRAME_COMP_WIDTH(&frame, plane);
    guint height = GST_VIDEO_FRAME_COMP_HEIGHT(&frame, plane);
    guint pixel_stride = GST_VIDEO_FRAME_COMP_PSTRIDE(&frame, plane);
    gsize row_bytes;
    guint row;
    guint8 value = plane == 0 ? 16 : 128;
    if (stride <= 0 || width == 0 || height == 0 || pixel_stride == 0 ||
        (gsize)width > G_MAXSIZE / (gsize)pixel_stride) {
      g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_NEGOTIATION,
                  "source-clock black video I420 plane layout is invalid");
      goto failed;
    }
    row_bytes = (gsize)width * (gsize)pixel_stride;
    if (row_bytes > (gsize)stride ||
        (gsize)height > G_MAXSIZE / (gsize)stride) {
      g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_NEGOTIATION,
                  "source-clock black video I420 plane size overflows");
      goto failed;
    }
    for (row = 0; row < height; ++row) {
      guint8 *data = (guint8 *)GST_VIDEO_FRAME_PLANE_DATA(&frame, plane) +
                     (gsize)row * (gsize)stride;
      memset(data, value, row_bytes);
      /* The allocation was zeroed before mapping; leave stride padding
       * initialized to zero instead of exposing uninitialized bytes. */
    }
  }
  gst_video_frame_unmap(&frame);
  gst_caps_unref(caps);
  return buffer;
failed:
  if (mapped) gst_video_frame_unmap(&frame);
  gst_buffer_unref(buffer);
  gst_caps_unref(caps);
  return NULL;
}

static void clear_video_scheduler_runtime(SourceClockVideoSchedulerRuntime *runtime) {
  size_t i;
  if (runtime == NULL) return;
  for (i = 0; i < runtime->policy.queue_size; ++i) {
    release_scheduled_video_buffer(runtime->policy.queue[i].opaque);
    runtime->policy.queue[i].opaque = NULL;
  }
  if (runtime->policy.has_last) {
    release_scheduled_video_buffer(runtime->policy.last.opaque);
    runtime->policy.last.opaque = NULL;
  }
  if (runtime->black_buffer != NULL) {
    gst_buffer_unref(runtime->black_buffer);
    runtime->black_buffer = NULL;
  }
  source_clock_video_scheduler_init(&runtime->policy, runtime->policy.cadence_ns);
}

static void clear_video_scheduler_pending(SourceClockVideoSchedulerRuntime *runtime) {
  size_t i;
  if (runtime == NULL) return;
  for (i = 0; i < runtime->policy.queue_size; ++i) {
    release_scheduled_video_buffer(runtime->policy.queue[i].opaque);
    runtime->policy.queue[i].opaque = NULL;
  }
  runtime->policy.queue_size = 0;
}

static GstBuffer *take_scheduled_video_buffer(
    SourceClockVideoSchedulerRuntime *runtime, uint64_t tick_ns,
    SourceClockVideoSchedulerOutputOrigin *origin) {
  SourceClockVideoTick tick;
  GstBuffer *old_last = NULL;
  GstBuffer *selected_buffer = NULL;
  size_t selected = SIZE_MAX;
  size_t i;
  if (origin != NULL) *origin = SOURCE_CLOCK_VIDEO_OUTPUT_BLACK;
  if (runtime == NULL) return NULL;
  for (i = 0; i < runtime->policy.queue_size; ++i) {
    if (runtime->policy.queue[i].pts_ns <= tick_ns) selected = i;
  }
  /* The policy object intentionally keeps only metadata.  Release decoded
   * buffers that the policy discards before asking it to advance. */
  if (selected != SIZE_MAX) {
    for (i = 0; i < selected; ++i) {
      release_scheduled_video_buffer(runtime->policy.queue[i].opaque);
      runtime->policy.queue[i].opaque = NULL;
    }
  }
  if (runtime->policy.has_last) old_last = GST_BUFFER(runtime->policy.last.opaque);
  tick = source_clock_video_scheduler_tick(&runtime->policy, tick_ns);
  if (tick.kind == SOURCE_CLOCK_VIDEO_TICK_FRAME ||
      tick.kind == SOURCE_CLOCK_VIDEO_TICK_HOLD) {
    if (tick.frame.opaque != NULL) {
      selected_buffer = source_clock_clone_scheduled_video_buffer(
          GST_BUFFER(tick.frame.opaque));
      if (tick.kind == SOURCE_CLOCK_VIDEO_TICK_HOLD) {
        runtime->held_frames++;
        if (origin != NULL) *origin = SOURCE_CLOCK_VIDEO_OUTPUT_HOLD;
      } else if (origin != NULL) {
        *origin = SOURCE_CLOCK_VIDEO_OUTPUT_FRAME;
      }
    }
  }
  if (tick.kind == SOURCE_CLOCK_VIDEO_TICK_FRAME && old_last != NULL &&
      old_last != GST_BUFFER(tick.frame.opaque)) {
    release_scheduled_video_buffer(old_last);
  }
  if (selected_buffer == NULL && tick.kind == SOURCE_CLOCK_VIDEO_TICK_BLACK &&
      runtime->black_buffer != NULL) {
    selected_buffer = source_clock_clone_scheduled_video_buffer(
        runtime->black_buffer);
  }
  return selected_buffer;
}

static GstFlowReturn on_decoded_video_sample(GstAppSink *sink, gpointer user_data) {
  SourceClockVideoSchedulerRuntime *runtime =
      (SourceClockVideoSchedulerRuntime *)user_data;
  SourceClockPipeline *owner;
  GstSample *sample;
  GstBuffer *buffer;
  GstClockTime pts;
  SourceClockVideoFrame frame;
  gboolean offered;
  gboolean emit_media_ready = FALSE;
  SourceClockMediaReadyWriteResult media_ready_write_result;
  uint64_t running_time_ns = 0;
  if (runtime == NULL || sink == NULL) return GST_FLOW_ERROR;
  sample = gst_app_sink_pull_sample(sink);
  if (sample == NULL) return GST_FLOW_EOS;
  buffer = gst_sample_get_buffer(sample);
  pts = buffer == NULL ? GST_CLOCK_TIME_NONE : GST_BUFFER_PTS(buffer);
  if (source_clock_candidate_decoded(runtime->owner, buffer)) {
    gst_sample_unref(sample);
    return GST_FLOW_OK;
  }
  if (buffer == NULL || pts == GST_CLOCK_TIME_NONE) {
    gst_sample_unref(sample);
    return GST_FLOW_OK;
  }
  if (GST_BUFFER_FLAG_IS_SET(buffer, GST_BUFFER_FLAG_CORRUPTED)) {
    g_mutex_lock(&runtime->mutex);
    runtime->dropped_frames++;
    g_mutex_unlock(&runtime->mutex);
    gst_sample_unref(sample);
    return GST_FLOW_OK;
  }
  /* Warmup history is fed to the decoder only to establish reference state.
   * A decode-only output must never enter the fixed-cadence display scheduler. */
  if (GST_BUFFER_FLAG_IS_SET(buffer, GST_BUFFER_FLAG_DECODE_ONLY)) {
    gst_sample_unref(sample);
    return GST_FLOW_OK;
  }
  frame.id = ++runtime->offered_frames;
  frame.pts_ns = (uint64_t)pts;
  if (source_clock_debug_enabled() && frame.id <= 3) {
    g_printerr("AirPlay source-clock decoded video frame=%" G_GUINT64_FORMAT
               " pts=%" G_GUINT64_FORMAT "\n", frame.id, frame.pts_ns);
  }
  frame.opaque = gst_buffer_ref(buffer);
  g_mutex_lock(&runtime->mutex);
  if (runtime->stopping) {
    g_mutex_unlock(&runtime->mutex);
    release_scheduled_video_buffer(frame.opaque);
    gst_sample_unref(sample);
    return GST_FLOW_FLUSHING;
  }
  owner = runtime->owner;
  if (runtime->policy.queue_size == SOURCE_CLOCK_VIDEO_SCHEDULER_CAPACITY) {
    release_scheduled_video_buffer(runtime->policy.queue[0].opaque);
    runtime->policy.queue[0].opaque = NULL;
    runtime->dropped_frames++;
  }
  offered = source_clock_video_scheduler_offer(&runtime->policy, frame);
  if (!offered) {
    runtime->dropped_frames++;
    release_scheduled_video_buffer(frame.opaque);
  }
  g_mutex_unlock(&runtime->mutex);
  if (owner != NULL) {
    g_mutex_lock(&owner->mutex);
    emit_media_ready = source_clock_media_ready_reserve_after_offer(
        owner->stopping, offered, &owner->media_ready_reserved,
        (uint64_t)pts, &running_time_ns);
    g_mutex_unlock(&owner->mutex);
  }
  if (emit_media_ready) {
    g_mutex_lock(&owner->event_mutex);
    media_ready_write_result = source_clock_media_ready_write_reserved(
        &owner->event_writer, running_time_ns,
        source_clock_event_write_video_decoded);
    g_mutex_unlock(&owner->event_mutex);
    if (media_ready_write_result == SOURCE_CLOCK_MEDIA_READY_WRITTEN) {
      g_mutex_lock(&owner->mutex);
      owner->last_signal_monotonic_us = g_get_monotonic_time();
      source_clock_media_ready_commit_write(
          media_ready_write_result, &owner->media_ready_emitted);
      g_mutex_unlock(&owner->mutex);
      g_printerr("AirPlay source-clock first decoded video accepted pts=%" G_GUINT64_FORMAT "\n",
                 (guint64)running_time_ns);
    } else {
      g_printerr("AirPlay source-clock could not write decoded-video event\n");
      if (g_remove(owner->event_writer.media_ready_path) != 0) {
        g_printerr("AirPlay source-clock could not remove failed decoded-video snapshot\n");
      }
      signal_pipeline_error(owner, FALSE);
    }
  }
  gst_sample_unref(sample);
  return GST_FLOW_OK;
}

static GstPadProbeReturn on_encoded_video_buffer(GstPad *pad,
                                                 GstPadProbeInfo *info,
                                                 gpointer user_data) {
  SourceClockPipeline *pipeline = (SourceClockPipeline *)user_data;
  GstBuffer *buffer;
  GstClockTime pts;
  gint64 now_monotonic_us;
  gboolean emit = FALSE;
  gboolean written;
  uint64_t running_time_ns;
  (void)pad;
  if (pipeline == NULL || info == NULL ||
      (GST_PAD_PROBE_INFO_TYPE(info) & GST_PAD_PROBE_TYPE_BUFFER) == 0) {
    return GST_PAD_PROBE_OK;
  }
  buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  if (source_clock_candidate_encoded(pipeline, buffer)) return GST_PAD_PROBE_OK;
  pts = buffer == NULL ? GST_CLOCK_TIME_NONE : GST_BUFFER_PTS(buffer);
  if (pts == GST_CLOCK_TIME_NONE) return GST_PAD_PROBE_OK;
  now_monotonic_us = g_get_monotonic_time();
  running_time_ns = now_monotonic_us >= pipeline->started_monotonic_us
                        ? (uint64_t)(now_monotonic_us - pipeline->started_monotonic_us) * 1000
                        : 0;
  g_mutex_lock(&pipeline->mutex);
  if (pipeline->video_encode_armed && !pipeline->video_encoded_reserved &&
      !pipeline->stopping) {
    pipeline->video_encoded_reserved = true;
    emit = TRUE;
  }
  g_mutex_unlock(&pipeline->mutex);
  if (!emit) return GST_PAD_PROBE_OK;
  g_mutex_lock(&pipeline->event_mutex);
  written = source_clock_event_write_video_encoded(
      &pipeline->event_writer, &running_time_ns);
  g_mutex_unlock(&pipeline->event_mutex);
  g_mutex_lock(&pipeline->mutex);
  pipeline->video_encoded_emitted = written;
  if (!written) pipeline->video_encoded_reserved = false;
  g_mutex_unlock(&pipeline->mutex);
  if (written) {
    g_printerr("AirPlay source-clock first real video encoded pts=%" G_GUINT64_FORMAT "\n",
               (guint64)running_time_ns);
  } else {
    g_printerr("AirPlay source-clock could not write encoded-video event\n");
  }
  return GST_PAD_PROBE_OK;
}

static gpointer source_clock_video_scheduler_thread(gpointer user_data) {
  SourceClockVideoSchedulerRuntime *runtime =
      (SourceClockVideoSchedulerRuntime *)user_data;
  for (;;) {
    GstClockReturn wait_result;
    GstBuffer *buffer;
    SourceClockVideoSchedulerOutputOrigin origin;
    guint64 tick_absolute;
    guint64 tick_ns;
    wait_result = gst_clock_id_wait(runtime->periodic_id, NULL);
    if (wait_result == GST_CLOCK_UNSCHEDULED) break;
    g_mutex_lock(&runtime->mutex);
    if (runtime->stopping) {
      g_mutex_unlock(&runtime->mutex);
      break;
    }
    tick_absolute = runtime->next_tick_absolute;
    runtime->next_tick_absolute += runtime->policy.cadence_ns;
    tick_ns = tick_absolute >= runtime->base_time
                   ? tick_absolute - runtime->base_time
                   : 0;
    runtime->tick_count++;
    if (runtime->owner != NULL && runtime->owner->candidate_proof_enabled) {
      g_mutex_unlock(&runtime->mutex);
      if (source_clock_candidate_tick(runtime->owner, tick_ns)) continue;
      g_mutex_lock(&runtime->mutex);
      if (runtime->stopping) {
        g_mutex_unlock(&runtime->mutex);
        break;
      }
    }
    buffer = take_scheduled_video_buffer(runtime, tick_ns, &origin);
    if (buffer != NULL) runtime->emitted_frames++;
    if (source_clock_debug_enabled() &&
        g_get_monotonic_time() - runtime->report_monotonic_us >= G_USEC_PER_SEC) {
      gint64 now_us = g_get_monotonic_time();
      guint64 ticks = runtime->tick_count - runtime->report_tick_count;
      guint64 emitted = runtime->emitted_frames - runtime->report_emitted_frames;
      guint64 held = runtime->held_frames - runtime->report_held_frames;
      guint64 dropped = runtime->dropped_frames - runtime->report_dropped_frames;
      g_printerr("AirPlay source-clock video scheduler ticks=%" G_GUINT64_FORMAT
                 " emitted=%" G_GUINT64_FORMAT " held=%" G_GUINT64_FORMAT
                 " dropped=%" G_GUINT64_FORMAT " depth=%zu tick_ns=%" G_GUINT64_FORMAT
                 " head_pts=%" G_GUINT64_FORMAT "\n",
                 ticks, emitted, held, dropped, runtime->policy.queue_size,
                 tick_ns, runtime->policy.queue_size == 0 ? 0 : runtime->policy.queue[0].pts_ns);
      runtime->report_monotonic_us = now_us;
      runtime->report_tick_count = runtime->tick_count;
      runtime->report_emitted_frames = runtime->emitted_frames;
      runtime->report_held_frames = runtime->held_frames;
      runtime->report_dropped_frames = runtime->dropped_frames;
    }
    g_mutex_unlock(&runtime->mutex);
    if (buffer != NULL) {
      GST_BUFFER_PTS(buffer) = (GstClockTime)tick_ns;
      GST_BUFFER_DTS(buffer) = GST_CLOCK_TIME_NONE;
      GST_BUFFER_DURATION(buffer) = (GstClockTime)runtime->policy.cadence_ns;
      if (gst_app_src_push_buffer(runtime->output_source, buffer) != GST_FLOW_OK) break;
      if (origin != SOURCE_CLOCK_VIDEO_OUTPUT_BLACK &&
          runtime->owner != NULL) {
        g_mutex_lock(&runtime->owner->mutex);
        runtime->owner->video_encode_armed = true;
        g_mutex_unlock(&runtime->owner->mutex);
      }
    }
  }
  return NULL;
}

static gboolean start_video_scheduler(SourceClockVideoSchedulerRuntime *runtime,
                                       SourceClockPipeline *owner,
                                       GstElement *pipeline, GstAppSink *decoded_sink,
                                       GstAppSrc *output_source, int fps) {
  GstClockTime cadence;
  if (runtime == NULL || owner == NULL || pipeline == NULL || decoded_sink == NULL ||
      output_source == NULL || fps < 1) return FALSE;
  runtime->owner = owner;
  cadence = (GstClockTime)(GST_SECOND / (guint)fps);
  if (cadence == 0) return FALSE;
  runtime->decoded_sink = decoded_sink;
  runtime->output_source = output_source;
  {
    GError *black_error = NULL;
    runtime->black_buffer = create_black_video_buffer(output_source, &black_error);
    if (runtime->black_buffer == NULL) {
      g_printerr("AirPlay source-clock black video buffer could not be created: %s\n",
                 black_error == NULL ? "unknown" : black_error->message);
      g_clear_error(&black_error);
      return FALSE;
    }
  }
  runtime->clock = gst_element_get_clock(pipeline);
  runtime->base_time = gst_element_get_base_time(pipeline);
  if (runtime->clock == NULL) return FALSE;
  source_clock_video_scheduler_init(&runtime->policy, (uint64_t)cadence);
  runtime->next_tick_absolute = runtime->base_time + cadence;
  runtime->report_monotonic_us = g_get_monotonic_time();
  runtime->periodic_id = gst_clock_new_periodic_id(
      runtime->clock, runtime->next_tick_absolute, cadence);
  if (runtime->periodic_id == NULL) return FALSE;
  gst_app_sink_set_emit_signals(decoded_sink, TRUE);
  g_signal_connect(decoded_sink, "new-sample", G_CALLBACK(on_decoded_video_sample), runtime);
  {
    GError *thread_error = NULL;
    runtime->thread = g_thread_try_new("source-clock-video-scheduler",
                                       source_clock_video_scheduler_thread,
                                       runtime, &thread_error);
    if (runtime->thread == NULL) {
      g_printerr("AirPlay source-clock scheduler thread creation failed: %s\n",
                 thread_error == NULL ? "unknown" : thread_error->message);
      g_clear_error(&thread_error);
    }
  }
  return runtime->thread != NULL;
}

static gboolean source_clock_accept_start_allowed(gboolean publisher_ready,
                                                  gboolean scheduler_started) {
  return publisher_ready && scheduler_started;
}

static gpointer source_clock_accept_thread(gpointer user_data);

static gboolean source_clock_start_accept_thread(
    const gchar *name, SourceClockAcceptContext **accept_context,
    GThread **thread, SourceClockThreadTryNew create_thread, GError **error) {
  GThread *started;
  if (name == NULL || accept_context == NULL || *accept_context == NULL ||
      thread == NULL || *thread != NULL || create_thread == NULL) {
    return FALSE;
  }
  started = create_thread(name, source_clock_accept_thread, *accept_context, error);
  if (started == NULL) return FALSE;
  *thread = started;
  *accept_context = NULL;
  return TRUE;
}

static void stop_video_scheduler(SourceClockVideoSchedulerRuntime *runtime) {
  if (runtime == NULL) return;
  g_mutex_lock(&runtime->mutex);
  runtime->stopping = TRUE;
  if (runtime->periodic_id != NULL) gst_clock_id_unschedule(runtime->periodic_id);
  g_mutex_unlock(&runtime->mutex);
  if (runtime->thread != NULL) {
    g_thread_join(runtime->thread);
    runtime->thread = NULL;
  }
  if (runtime->periodic_id != NULL) {
    gst_clock_id_unref(runtime->periodic_id);
    runtime->periodic_id = NULL;
  }
  if (runtime->clock != NULL) {
    gst_object_unref(runtime->clock);
    runtime->clock = NULL;
  }
  g_mutex_lock(&runtime->mutex);
  clear_video_scheduler_runtime(runtime);
  g_mutex_unlock(&runtime->mutex);
}

static gboolean save_pending_frame(SourceClockPendingFrame *pending,
                                   const SourceClockHeader *header,
                                   const uint8_t *payload, size_t payload_bytes,
                                   uint64_t arrival_ns) {
  uint8_t *copy;
  if (pending == NULL || header == NULL || payload == NULL || payload_bytes == 0) return FALSE;
  /* Keep an already captured IDR rather than replacing it with a P-frame. */
  if (pending->present &&
      (pending->header.flags & SOURCE_CLOCK_FLAG_KEYFRAME) != 0 &&
      (header->flags & SOURCE_CLOCK_FLAG_KEYFRAME) == 0) {
    return TRUE;
  }
  copy = (uint8_t *)g_malloc(payload_bytes);
  if (copy == NULL) return FALSE;
  memcpy(copy, payload, payload_bytes);
  clear_pending_frame(pending);
  pending->present = TRUE;
  pending->header = *header;
  pending->arrival_ns = arrival_ns;
  pending->payload = copy;
  pending->payload_bytes = payload_bytes;
  return TRUE;
}

/* Called outside pipeline->mutex, including pending-frame flush from either
 * reader. Serialize adapter state; the audio-bin owns all ready/pending data.
 * Terminal audio failure deliberately returns success to the media reader. */
static gboolean push_dynamic_audio_buffer(SourceClockPipeline *pipeline,
                                          const SourceClockHeader *header,
                                          const uint8_t *payload, size_t payload_bytes,
                                          uint64_t mapped_ns,
                                          guint64 audio_admission_generation) {
  SourceClockAudioFormat format = {0};
  GstBuffer *buffer = NULL;
  GError *error = NULL;
  GstFlowReturn flow = GST_FLOW_OK;
  const char *failure = NULL;
  g_mutex_lock(&pipeline->mutex);
  if (audio_admission_generation != pipeline->audio_admission_generation ||
      pipeline->dynamic_audio_disabled || pipeline->audio_bin == NULL ||
      pipeline->stopping) goto done;
  if (header->codec != SOURCE_CLOCK_CODEC_AAC_LC_ADTS &&
      header->codec != SOURCE_CLOCK_CODEC_AAC_ELD_RAW) {
    failure = "unsupported codec";
    goto disabled;
  }
  format.codec = header->codec;
  format.sample_rate = header->sample_rate;
  format.channels = header->channels;
  format.samples_per_frame = header->sample_count;
  if (!source_clock_audio_bin_configure(pipeline->audio_bin, &format, &error)) {
    if (error != NULL && error->domain == SOURCE_CLOCK_AUDIO_BIN_ERROR &&
        error->code == SOURCE_CLOCK_AUDIO_BIN_FORMAT_CHANGE) {
      g_clear_error(&error);
      ++pipeline->audio_admission_generation;
      clear_pending_frame(&pipeline->pending_audio);
      pipeline->dynamic_audio_disabled = TRUE;
      if (source_clock_pipeline_release_audio_bin(
              &pipeline->audio_bin, &pipeline->dynamic_audio_disabled) !=
              SOURCE_CLOCK_AUDIO_STOP_COMPLETE || pipeline->audio_bin != NULL) {
        failure = "format replacement stop incomplete";
        goto disabled;
      }
      if (pipeline->pipeline != NULL && pipeline->loop != NULL) {
        GstElement *mixer = gst_bin_get_by_name(
            GST_BIN(pipeline->pipeline), "audio_mixer");
        if (mixer != NULL) {
          pipeline->audio_bin = source_clock_audio_bin_new(
              pipeline->pipeline, mixer,
              g_main_loop_get_context(pipeline->loop));
          gst_object_unref(mixer); /* new holds an independent reference. */
        }
      }
      if (pipeline->audio_bin == NULL) {
        failure = "format replacement unavailable";
        goto disabled;
      }
      if (!source_clock_audio_bin_configure(pipeline->audio_bin, &format, &error)) {
        failure = error != NULL ? error->message : "replacement configure failed";
        goto disabled;
      }
      pipeline->dynamic_audio_disabled = FALSE;
      /* This one frame announced the new tuple; all previously mapped frames
       * retain the old generation and are rejected at admission above. */
      audio_admission_generation = pipeline->audio_admission_generation;
    } else {
      failure = error != NULL ? error->message : "configure failed";
      goto disabled;
    }
  }
  buffer = gst_buffer_new_allocate(NULL, payload_bytes, NULL);
  if (buffer == NULL || gst_buffer_fill(buffer, 0, payload, payload_bytes) != payload_bytes) {
    if (buffer != NULL) gst_buffer_unref(buffer);
    failure = "buffer allocation/copy failed";
    goto disabled;
  }
  GST_BUFFER_PTS(buffer) = (GstClockTime)mapped_ns;
  GST_BUFFER_DTS(buffer) = GST_CLOCK_TIME_NONE;
  /* configure has validated the codec-specific nonzero rate/sample count. */
  GST_BUFFER_DURATION(buffer) = gst_util_uint64_scale(
      header->sample_count, GST_SECOND, header->sample_rate);
  if ((header->flags & SOURCE_CLOCK_FLAG_DISCONTINUITY) != 0)
    GST_BUFFER_FLAG_SET(buffer, GST_BUFFER_FLAG_DISCONT);
  /* push consumes the reference on EVERY return, even flow failure. */
  flow = source_clock_audio_bin_push(pipeline->audio_bin, buffer);
  if (flow == GST_FLOW_OK) goto done;
  failure = gst_flow_get_name(flow);
disabled:
  pipeline->dynamic_audio_disabled = TRUE;
  g_printerr("AirPlay source-clock dynamic audio disabled codec=%u: %s; silence retained\n",
             header->codec, failure);
done:
  g_clear_error(&error);
  g_mutex_unlock(&pipeline->mutex);
  return TRUE;
}

/* Caller holds pipeline->mutex. This helper changes decoder ownership only;
 * the caller owns admission-generation changes and pending-frame policy. */
static gboolean replace_fixed_pcm_audio_helper_locked(
    SourceClockPipeline *pipeline, const char *reason);

/* Unsupported input is isolated to this frame. A supported tuple change
 * replaces only the decoder helper; the fixed publisher branch, silence and
 * video remain alive. */
static gboolean push_fixed_pcm_audio_buffer(SourceClockPipeline *pipeline,
                                            const SourceClockHeader *header,
                                            const uint8_t *payload,
                                            size_t payload_bytes,
                                            uint64_t mapped_ns,
                                            guint64 audio_admission_generation) {
  SourceClockAudioFormat format = {0};
  GstBuffer *buffer = NULL;
  GError *error = NULL;
  GstFlowReturn flow;
  gboolean format_replaced = FALSE;
  g_mutex_lock(&pipeline->mutex);
  if (audio_admission_generation != pipeline->audio_admission_generation ||
      pipeline->fixed_pcm_audio_disabled || pipeline->fixed_pcm_audio == NULL ||
      pipeline->stopping) goto done;
  format.codec = header->codec;
  format.sample_rate = header->sample_rate;
  format.channels = header->channels;
  format.samples_per_frame = header->sample_count;
  if (!source_clock_fixed_pcm_audio_configure(
          pipeline->fixed_pcm_audio, &format, &error)) {
    if (error != NULL && error->domain == SOURCE_CLOCK_FIXED_PCM_AUDIO_ERROR &&
        error->code == SOURCE_CLOCK_FIXED_PCM_FORMAT_CHANGE) {
      g_clear_error(&error);
      ++pipeline->audio_admission_generation;
      clear_pending_frame(&pipeline->pending_audio);
      if (!replace_fixed_pcm_audio_helper_locked(pipeline, "format replacement"))
        goto done;
      if (!source_clock_fixed_pcm_audio_configure(
              pipeline->fixed_pcm_audio, &format, &error)) {
        pipeline->fixed_pcm_audio_disabled = TRUE;
        g_printerr("AirPlay source-clock fixed PCM replacement configure failed: %s; silence retained\n",
                   error == NULL ? "unknown error" : error->message);
        goto done;
      }
      format_replaced = TRUE;
    } else {
      if (source_clock_debug_enabled()) {
        g_printerr("AirPlay source-clock fixed PCM frame rejected codec=%u rate=%u channels=%u samples=%u: %s\n",
                   header->codec, header->sample_rate, header->channels,
                   header->sample_count,
                   error == NULL ? "unsupported format" : error->message);
      }
      goto done;
    }
  }
  buffer = gst_buffer_new_allocate(NULL, payload_bytes, NULL);
  if (buffer == NULL ||
      gst_buffer_fill(buffer, 0, payload, payload_bytes) != payload_bytes) {
    if (buffer != NULL) gst_buffer_unref(buffer);
    pipeline->fixed_pcm_audio_disabled = TRUE;
    g_printerr("AirPlay source-clock fixed PCM audio disabled: buffer allocation failed; silence retained\n");
    goto done;
  }
  GST_BUFFER_PTS(buffer) = (GstClockTime)mapped_ns;
  GST_BUFFER_DTS(buffer) = GST_CLOCK_TIME_NONE;
  GST_BUFFER_DURATION(buffer) = gst_util_uint64_scale(
      header->sample_count, GST_SECOND, header->sample_rate);
  if (format_replaced ||
      (header->flags & SOURCE_CLOCK_FLAG_DISCONTINUITY) != 0)
    GST_BUFFER_FLAG_SET(buffer, GST_BUFFER_FLAG_DISCONT);
  /* push consumes the reference on every return. */
  flow = source_clock_fixed_pcm_audio_push(pipeline->fixed_pcm_audio, buffer);
  if (flow != GST_FLOW_OK && source_clock_debug_enabled()) {
    g_printerr("AirPlay source-clock fixed PCM push rejected flow=%s pts=%" G_GUINT64_FORMAT " size=%zu\n",
               gst_flow_get_name(flow), mapped_ns, payload_bytes);
  }
done:
  g_clear_error(&error);
  g_mutex_unlock(&pipeline->mutex);
  return TRUE;
}

static gboolean push_source_buffer_ex(SourceClockPipeline *pipeline,
                                       const SourceClockHeader *header,
                                       const uint8_t *payload, size_t payload_bytes,
                                       uint64_t mapped_ns,
                                       guint64 audio_admission_generation,
                                       gboolean decode_only) {
  GstAppSrc *source = NULL;
  GstClockTime duration = GST_CLOCK_TIME_NONE;
  GstBuffer *buffer;
  GstMapInfo map;
  if (pipeline == NULL || header == NULL || payload == NULL || payload_bytes == 0) return TRUE;
  if (header->stream_kind == SOURCE_CLOCK_STREAM_AUDIO &&
      pipeline->fixed_pcm_audio_enabled)
    return push_fixed_pcm_audio_buffer(pipeline, header, payload, payload_bytes,
                                       mapped_ns, audio_admission_generation);
  if (header->stream_kind == SOURCE_CLOCK_STREAM_AUDIO && pipeline->dynamic_audio_enabled)
    return push_dynamic_audio_buffer(pipeline, header, payload, payload_bytes,
                                     mapped_ns, audio_admission_generation);
  if (header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO) source = pipeline->video_source;
  if (header->stream_kind == SOURCE_CLOCK_STREAM_AUDIO) source = pipeline->audio_source;
  if (source == NULL) return TRUE;
  if (header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO) {
    /* appsrc is non-blocking: reject before allocation when the live ingress
     * queue is already at its bounded AU/byte/time budget. Dropping a
     * compressed reference AU enters the IDR recovery gate at the caller. */
    GObjectClass *klass = G_OBJECT_GET_CLASS(source);
    GParamSpec *buffers_property = g_object_class_find_property(
        klass, "current-level-buffers");
    GParamSpec *bytes_property = g_object_class_find_property(
        klass, "current-level-bytes");
    GParamSpec *time_property = g_object_class_find_property(
        klass, "current-level-time");
    guint64 current_buffers = 0;
    guint64 current_bytes = 0;
    guint64 current_time = 0;
    if (buffers_property != NULL) g_object_get(source, "current-level-buffers", &current_buffers, NULL);
    if (bytes_property != NULL) g_object_get(source, "current-level-bytes", &current_bytes, NULL);
    if (time_property != NULL) g_object_get(source, "current-level-time", &current_time, NULL);
    if ((buffers_property != NULL && current_buffers >= SOURCE_CLOCK_VIDEO_PENDING_CAPACITY) ||
        (bytes_property != NULL &&
         ((guint64)current_bytes + payload_bytes > SOURCE_CLOCK_VIDEO_PENDING_MAX_BYTES)) ||
        (time_property != NULL && current_time > SOURCE_CLOCK_VIDEO_PENDING_MAX_AGE_NS)) {
      if (source_clock_debug_enabled())
        g_printerr("AirPlay source-clock video appsrc budget reached buffers=%" G_GUINT64_FORMAT " bytes=%" G_GUINT64_FORMAT " time=%" G_GUINT64_FORMAT "\n",
                   current_buffers, current_bytes, current_time);
      return FALSE;
    }
  }
  if (header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO &&
      pipeline->video_codec != header->codec) {
    const char *caps_string = source_clock_video_caps_string(header->codec);
    GstCaps *caps;
    if (caps_string == NULL) return FALSE;
    caps = gst_caps_from_string(caps_string);
    if (caps == NULL) return FALSE;
    gst_app_src_set_caps(source, caps);
    gst_caps_unref(caps);
    pipeline->video_codec = header->codec;
    g_printerr("AirPlay source-clock video caps selected codec=%u caps=%s\n",
               header->codec, caps_string);
  }
  if (header->stream_kind == SOURCE_CLOCK_STREAM_AUDIO && header->sample_rate != 0) {
    duration = (GstClockTime)source_clock_audio_frame_duration_ns(
        &pipeline->audio_frame_clock, header->sample_count, header->sample_rate);
  }
  buffer = gst_buffer_new_allocate(NULL, payload_bytes, NULL);
  if (buffer == NULL) return FALSE;
  if (!gst_buffer_map(buffer, &map, GST_MAP_WRITE)) {
    gst_buffer_unref(buffer);
    return FALSE;
  }
  memcpy(map.data, payload, payload_bytes);
  gst_buffer_unmap(buffer, &map);
  GST_BUFFER_PTS(buffer) = (GstClockTime)mapped_ns;
  GST_BUFFER_DTS(buffer) = GST_CLOCK_TIME_NONE;
  GST_BUFFER_DURATION(buffer) = duration;
  if ((header->flags & SOURCE_CLOCK_FLAG_DISCONTINUITY) != 0) {
    GST_BUFFER_FLAG_SET(buffer, GST_BUFFER_FLAG_DISCONT);
  }
  if (decode_only) GST_BUFFER_FLAG_SET(buffer, GST_BUFFER_FLAG_DECODE_ONLY);
  GstFlowReturn flow = gst_app_src_push_buffer(source, buffer);
  if (flow != GST_FLOW_OK && source_clock_debug_enabled()) {
    g_printerr("AirPlay source-clock appsrc push failed stream=%u flow=%s pts=%" G_GUINT64_FORMAT " size=%zu\n",
               header->stream_kind, gst_flow_get_name(flow), mapped_ns, payload_bytes);
  }
  return flow == GST_FLOW_OK;
}

static gboolean push_source_buffer(SourceClockPipeline *pipeline,
                                    const SourceClockHeader *header,
                                    const uint8_t *payload, size_t payload_bytes,
                                    uint64_t mapped_ns,
                                    guint64 audio_admission_generation) {
  return push_source_buffer_ex(pipeline, header, payload, payload_bytes, mapped_ns,
                               audio_admission_generation, FALSE);
}

static void mark_video_push_loss(SourceClockPipeline *pipeline) {
  if (pipeline == NULL) return;
  g_mutex_lock(&pipeline->mutex);
  source_clock_video_au_gate_mark_loss(&pipeline->video_au_gate);
  g_mutex_unlock(&pipeline->mutex);
}

SourceClockAudioStopResult source_clock_pipeline_release_audio_bin(
    SourceClockAudioBin **audio_bin, gboolean *dynamic_audio_disabled) {
  SourceClockAudioStopResult result;
  if (audio_bin == NULL || *audio_bin == NULL)
    return SOURCE_CLOCK_AUDIO_STOP_COMPLETE;
  result = source_clock_audio_bin_stop(*audio_bin);
  if (result == SOURCE_CLOCK_AUDIO_STOP_COMPLETE) {
    if (source_clock_audio_bin_free(*audio_bin)) {
      *audio_bin = NULL;
      return SOURCE_CLOCK_AUDIO_STOP_COMPLETE;
    }
    /* COMPLETE followed by free(FALSE) violates the expected C1 transition.
     * Quarantine the still-owned object instead of guessing that it is safe. */
    result = SOURCE_CLOCK_AUDIO_STOP_FAILED_RETAINED;
  }
  if (dynamic_audio_disabled != NULL)
    *dynamic_audio_disabled = TRUE;
  return result;
}

/* Called only for NEW SESSION_START, with pipeline->mutex held. Both readers'
 * push admission is closed, and mapped-but-not-pushed audio is invalidated.
 * C1's worker never takes pipeline->mutex; stop is bounded, not a retry loop. */
static void replace_session_audio_bin(SourceClockPipeline *pipeline) {
  GstElement *mixer;
  if (!pipeline->dynamic_audio_enabled) return;
  ++pipeline->audio_admission_generation;
  pipeline->dynamic_audio_disabled = TRUE;
  if (source_clock_pipeline_release_audio_bin(
          &pipeline->audio_bin, &pipeline->dynamic_audio_disabled) !=
          SOURCE_CLOCK_AUDIO_STOP_COMPLETE || pipeline->audio_bin != NULL)
    return;
  if (pipeline->pipeline == NULL || pipeline->loop == NULL) return;
  mixer = gst_bin_get_by_name(GST_BIN(pipeline->pipeline), "audio_mixer");
  if (mixer == NULL) return;
  pipeline->audio_bin = source_clock_audio_bin_new(
      pipeline->pipeline, mixer, g_main_loop_get_context(pipeline->loop));
  gst_object_unref(mixer); /* new holds an independent reference. */
  if (pipeline->audio_bin != NULL) pipeline->dynamic_audio_disabled = FALSE;
}

/* A new source session retires only the helper-owned decoder lifetime.  The
 * publisher-owned fixed PCM appsrc/queue/mixer pad remains linked and keeps
 * the publisher clock, base-time and RTSP/recording branches untouched. */
static gboolean replace_fixed_pcm_audio_helper_locked(
    SourceClockPipeline *pipeline, const char *reason) {
  SourceClockFixedPcmAudioStopResult stop_result;
  GError *error = NULL;
  pipeline->fixed_pcm_audio_disabled = TRUE;
  if (pipeline->fixed_pcm_audio != NULL) {
    stop_result = source_clock_fixed_pcm_audio_stop(pipeline->fixed_pcm_audio);
    if (stop_result != SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE ||
        !source_clock_fixed_pcm_audio_free(pipeline->fixed_pcm_audio)) {
      g_printerr("AirPlay source-clock fixed PCM helper retained during %s: stop=%d\n",
                 reason, (int)stop_result);
      return FALSE;
    }
    pipeline->fixed_pcm_audio = NULL;
  }
  if (pipeline->fixed_pcm_source == NULL) return FALSE;
  pipeline->fixed_pcm_audio = source_clock_fixed_pcm_audio_new(
      pipeline->fixed_pcm_source, NULL, NULL, &error);
  if (pipeline->fixed_pcm_audio == NULL) {
    g_printerr("AirPlay source-clock fixed PCM helper %s failed: %s; silence retained\n",
               reason,
               error == NULL ? "unknown error" : error->message);
  } else {
    pipeline->fixed_pcm_audio_disabled = FALSE;
  }
  g_clear_error(&error);
  return pipeline->fixed_pcm_audio != NULL;
}

static void replace_session_fixed_pcm_audio(SourceClockPipeline *pipeline) {
  if (!pipeline->fixed_pcm_audio_enabled) return;
  ++pipeline->audio_admission_generation;
  (void)replace_fixed_pcm_audio_helper_locked(pipeline, "session replacement");
}

static gboolean set_audio_input_caps(SourceClockPipeline *pipeline,
                                     const SourceClockHeader *header) {
  GstCaps *caps = NULL;
  if (pipeline == NULL || header == NULL || pipeline->audio_source == NULL) return FALSE;
  if (!source_clock_audio_caps_state_should_update(&pipeline->audio_caps_state,
                                                   header->codec,
                                                   header->sample_rate,
                                                   header->channels)) {
    return TRUE;
  }
  if (header->codec == SOURCE_CLOCK_CODEC_ALAC_RAW) {
    caps = gst_caps_from_string(
        "audio/x-alac,mpegversion=(int)4,channels=(int)2,rate=(int)44100,"
        "stream-format=(string)raw,codec_data=(buffer)00000024616c616300000000000001600010280a0e0200ff00000000000000000000ac44");
  } else if (header->codec == SOURCE_CLOCK_CODEC_AAC_LC_ADTS) {
    caps = gst_caps_from_string(
        "audio/mpeg,mpegversion=(int)4,channels=(int)2,rate=(int)44100,"
        "stream-format=(string)adts");
  } else if (header->codec == SOURCE_CLOCK_CODEC_AAC_ELD_RAW) {
    caps = gst_caps_from_string(
        "audio/mpeg,mpegversion=(int)4,channels=(int)2,rate=(int)44100,"
        "stream-format=(string)raw,codec_data=(buffer)f8e85000");
  }
  if (caps == NULL) return FALSE;
  gst_app_src_set_caps(pipeline->audio_source, caps);
  gst_caps_unref(caps);
  return TRUE;
}

static void audio_decoder_pad_added(GstElement *decoder, GstPad *pad, gpointer user_data) {
  SourceClockPipeline *pipeline = (SourceClockPipeline *)user_data;
  GstElement *mixer;
  GstElement *convert;
  GstElement *resample;
  GstElement *caps_filter;
  GstElement *queue;
  GstCaps *target_caps;
  GstPad *convert_sink;
  GstPad *filter_src;
  GstPad *sink;
  GstPadLinkReturn link_result;
  (void)decoder;
  if (gst_pad_is_linked(pad)) return;
  mixer = gst_bin_get_by_name(GST_BIN(pipeline->pipeline), "audio_mixer");
  if (mixer != NULL) {
    convert = gst_element_factory_make("audioconvert", NULL);
    resample = gst_element_factory_make("audioresample", NULL);
    caps_filter = gst_element_factory_make("capsfilter", NULL);
    queue = gst_element_factory_make("queue", NULL);
    if (convert == NULL || resample == NULL || caps_filter == NULL || queue == NULL) {
      g_printerr("AirPlay source-clock audio converter elements are unavailable\n");
      if (convert != NULL) gst_object_unref(convert);
      if (resample != NULL) gst_object_unref(resample);
      if (caps_filter != NULL) gst_object_unref(caps_filter);
      if (queue != NULL) gst_object_unref(queue);
      gst_object_unref(mixer);
      return;
    }
    target_caps = gst_caps_from_string(
        "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved");
    g_object_set(caps_filter, "caps", target_caps, NULL);
    gst_caps_unref(target_caps);
    g_object_set(queue, "max-size-time", (guint64)500000000, "leaky", 2, NULL);
    gst_bin_add_many(GST_BIN(pipeline->pipeline), convert, resample, caps_filter, queue, NULL);
    if (!gst_element_link_many(convert, resample, caps_filter, queue, NULL)) {
      g_printerr("AirPlay source-clock audio converter chain link failed\n");
      gst_object_unref(mixer);
      return;
    }
    convert_sink = gst_element_get_static_pad(convert, "sink");
    filter_src = gst_element_get_static_pad(queue, "src");
    sink = gst_element_get_request_pad(mixer, "sink_%u");
    if (convert_sink != NULL && filter_src != NULL && sink != NULL) {
      link_result = gst_pad_link(pad, convert_sink);
      if (link_result == GST_PAD_LINK_OK) link_result = gst_pad_link(filter_src, sink);
      if (link_result != GST_PAD_LINK_OK) {
        g_printerr("AirPlay source-clock audio decoder link failed: %s\n",
                   gst_pad_link_get_name(link_result));
      }
    }
    if (convert_sink != NULL) gst_object_unref(convert_sink);
    if (filter_src != NULL) gst_object_unref(filter_src);
    if (sink != NULL) gst_object_unref(sink);
    gst_element_sync_state_with_parent(convert);
    gst_element_sync_state_with_parent(resample);
    gst_element_sync_state_with_parent(caps_filter);
    gst_element_sync_state_with_parent(queue);
    gst_object_unref(mixer);
  }
}

static int on_source_frame(void *user, const SourceClockHeader *header,
                           const uint8_t *payload, size_t payload_bytes) {
  SourceClockPipeline *pipeline = (SourceClockPipeline *)user;
  SourceClockPendingFrame flush_video;
  SourceClockPendingFrame flush_audio;
  SourceClockPendingFrame flush_video_history[SOURCE_CLOCK_VIDEO_PENDING_CAPACITY];
  size_t flush_video_history_count = 0;
  uint64_t flush_video_history_generation = 0;
  SourceClockHeader normalized_header;
  uint64_t flush_video_pts = 0;
  uint64_t flush_audio_pts = 0;
  uint64_t mapped_ns = 0;
  guint64 audio_admission_generation = 0;
  uint64_t arrival_ns = (uint64_t)monotonic_ns();
  uint64_t running_ns;
  SourceClockMapResult map_result;
  gboolean became_ready;
  gboolean emit_input_idr = FALSE;
  gboolean video_feed_locked = FALSE;
  gboolean input_idr_written;
  uint64_t input_idr_running_ns = 0;
  uint64_t input_idr_source_ntp_ns = 0;
  SourceClockVideoAUResult au_result;
  const uint8_t *prepared_payload = payload;
  size_t prepared_payload_bytes = payload_bytes;
  uint8_t *owned_payload = NULL;
  memset(&flush_video, 0, sizeof(flush_video));
  memset(&flush_audio, 0, sizeof(flush_audio));
  memset(flush_video_history, 0, sizeof(flush_video_history));
  if (header == NULL) return 0;
  /* Control messages such as SESSION_START and SESSION_END intentionally have
   * an empty payload.  Media frames, on the other hand, must carry bytes. */
  if (header->stream_kind != SOURCE_CLOCK_STREAM_CONTROL &&
      (payload == NULL || payload_bytes == 0)) {
    return 0;
  }
  /* Serialize compressed video pushes, including the ready-transition
   * history drain, so a live AU cannot overtake its decoder reference AU. */
  if (header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO && pipeline->video_source != NULL &&
      pipeline->video_feed_mutex_initialized) {
    g_mutex_lock(&pipeline->video_feed_mutex);
    video_feed_locked = TRUE;
  }
  running_ns = arrival_ns >= (uint64_t)(pipeline->started_monotonic_us * 1000)
                   ? arrival_ns - (uint64_t)(pipeline->started_monotonic_us * 1000)
                   : 0;
  g_mutex_lock(&pipeline->mutex);
  if (source_clock_debug_enabled()) {
    guint *counter = header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO
                         ? &pipeline->debug_video_frames
                         : header->stream_kind == SOURCE_CLOCK_STREAM_AUDIO
                               ? &pipeline->debug_audio_frames
                               : NULL;
    if (counter != NULL && *counter < 12) {
      g_printerr("AirPlay source-clock input stream=%u codec=%u seq=%u flags=0x%04x ntp=%" G_GUINT64_FORMAT " size=%zu\n",
                 header->stream_kind, header->codec, header->sequence,
                 header->flags, header->remote_ntp_ns, payload_bytes);
      (*counter)++;
    }
  }
  /* The source clock is established from the first received AU metadata even
   * when the AU gate must wait for a clean IDR after a reset.  Otherwise an
   * audio-only warmup can make the eventual IDR appear far in the future and
   * the fixed-cadence scheduler would remain black until the session ends. */
  if (header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO &&
      !pipeline->timeline.has_first_video) {
    source_clock_timeline_offer_first(&pipeline->timeline, header->stream_kind,
                                      header->remote_ntp_ns, running_ns);
  }
  if (header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO) {
    if (source_clock_debug_enabled() && pipeline->debug_video_frames <= 12) {
      source_clock_log_video_shape("raw", header, payload, payload_bytes,
                                   pipeline->video_bootstrap.configuration_bytes);
    }
    if (!source_clock_video_bootstrap_prepare(&pipeline->video_bootstrap,
                                               header->codec, header->flags,
                                               payload, payload_bytes,
                                               &prepared_payload, &prepared_payload_bytes,
                                               &owned_payload)) {
      g_mutex_unlock(&pipeline->mutex);
      if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
      return -1;
    }
    payload = prepared_payload;
    payload_bytes = prepared_payload_bytes;
    if (source_clock_debug_enabled() && pipeline->debug_video_frames <= 12) {
      source_clock_log_video_shape("prepared", header, payload, payload_bytes,
                                   pipeline->video_bootstrap.configuration_bytes);
    }
    au_result = source_clock_video_au_gate_offer(&pipeline->video_au_gate,
                                                  header->codec, payload,
                                                  payload_bytes, header->flags);
    if (au_result == SOURCE_CLOCK_VIDEO_AU_DROP) {
      g_mutex_unlock(&pipeline->mutex);
      free(owned_payload);
      if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
      return 0;
    }
    if (au_result == SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE) {
      if ((header->flags & SOURCE_CLOCK_FLAG_KEYFRAME) == 0) {
        g_mutex_unlock(&pipeline->mutex);
        free(owned_payload);
        if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
        return 0;
      }
      normalized_header = *header;
      normalized_header.flags |= SOURCE_CLOCK_FLAG_DISCONTINUITY;
      header = &normalized_header;
    }
    if ((header->flags & SOURCE_CLOCK_FLAG_KEYFRAME) != 0 &&
        !pipeline->video_input_idr_reserved) {
      pipeline->video_input_idr_reserved = true;
      emit_input_idr = TRUE;
      input_idr_running_ns = running_ns;
      input_idr_source_ntp_ns = header->remote_ntp_ns;
    }
  }
  if (header->stream_kind == SOURCE_CLOCK_STREAM_CONTROL) {
    if (header->codec == SOURCE_CLOCK_CONTROL_SESSION_START) {
      SourceClockSessionResult session_result = source_clock_timeline_session_start(
          &pipeline->timeline, header->sequence);
      if (session_result == SOURCE_CLOCK_SESSION_NEW) {
        replace_session_audio_bin(pipeline);
        replace_session_fixed_pcm_audio(pipeline);
        clear_pending_frame(&pipeline->pending_video);
        source_clock_video_pending_fifo_clear(&pipeline->video_pending_fifo);
        clear_pending_frame(&pipeline->pending_audio);
        source_clock_audio_frame_clock_init(&pipeline->audio_frame_clock);
        source_clock_video_au_gate_init(&pipeline->video_au_gate);
        source_clock_video_bootstrap_clear(&pipeline->video_bootstrap);
        pipeline->video_codec = 0;
        g_mutex_lock(&pipeline->video_scheduler.mutex);
        clear_video_scheduler_pending(&pipeline->video_scheduler);
        g_mutex_unlock(&pipeline->video_scheduler.mutex);
      }
    }
    g_mutex_unlock(&pipeline->mutex);
    if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
    return 0;
  }
  if (header->stream_kind == SOURCE_CLOCK_STREAM_AUDIO && pipeline->audio_source != NULL &&
      !set_audio_input_caps(pipeline, header)) {
    g_mutex_unlock(&pipeline->mutex);
    if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
    return -1;
  }
  pipeline->last_signal_monotonic_us = g_get_monotonic_time();
  source_clock_timeline_offer_first(&pipeline->timeline, header->stream_kind,
                                    header->remote_ntp_ns, running_ns);
  became_ready = source_clock_timeline_ready(&pipeline->timeline, running_ns);
  if (!pipeline->timeline.ready && !became_ready) {
    gboolean saved;
    if (header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO && pipeline->video_source != NULL) {
      SourceClockVideoPendingResult pending_result =
          source_clock_video_pending_fifo_enqueue(&pipeline->video_pending_fifo,
                                                  header, payload, payload_bytes, running_ns);
      saved = pending_result == SOURCE_CLOCK_VIDEO_PENDING_ACCEPTED;
      if (pending_result != SOURCE_CLOCK_VIDEO_PENDING_ACCEPTED)
        source_clock_video_au_gate_mark_loss(&pipeline->video_au_gate);
    } else {
      SourceClockPendingFrame *pending = header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO
                                             ? &pipeline->pending_video
                                             : &pipeline->pending_audio;
      saved = save_pending_frame(pending, header, payload, payload_bytes, running_ns);
    }
    g_mutex_unlock(&pipeline->mutex);
    if (emit_input_idr) {
      g_mutex_lock(&pipeline->event_mutex);
      input_idr_written = source_clock_event_write_video_input_idr(
          &pipeline->event_writer, &input_idr_running_ns,
          &input_idr_source_ntp_ns);
      g_mutex_unlock(&pipeline->event_mutex);
      g_mutex_lock(&pipeline->mutex);
      pipeline->video_input_idr_emitted = input_idr_written;
      if (!input_idr_written) pipeline->video_input_idr_reserved = false;
      g_mutex_unlock(&pipeline->mutex);
    }
    free(owned_payload);
    if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
    return saved ? 0 : -1;
  }
  if (became_ready) {
    flush_video_history_generation = pipeline->timeline.active_generation;
    while (pipeline->video_source != NULL &&
           flush_video_history_count < SOURCE_CLOCK_VIDEO_PENDING_CAPACITY &&
           source_clock_video_pending_fifo_pop(&pipeline->video_pending_fifo,
                                               &flush_video_history[flush_video_history_count])) {
      flush_video_history_count++;
    }
    if (pipeline->video_source == NULL && pipeline->pending_video.present) {
      uint64_t pending_mapped = 0;
      SourceClockMapResult pending_result = source_clock_timeline_map(
          &pipeline->timeline, SOURCE_CLOCK_STREAM_VIDEO,
          pipeline->pending_video.header.remote_ntp_ns, pipeline->pending_video.arrival_ns,
          running_ns, &pending_mapped);
      if (pending_result == SOURCE_CLOCK_MAPPED || pending_result == SOURCE_CLOCK_RESET_EPOCH) {
        flush_video = pipeline->pending_video;
        memset(&pipeline->pending_video, 0, sizeof(pipeline->pending_video));
        flush_video_pts = pending_mapped;
      }
    }
    if (pipeline->pending_audio.present) {
      uint64_t pending_mapped = 0;
      SourceClockMapResult pending_result = source_clock_timeline_map(
          &pipeline->timeline, SOURCE_CLOCK_STREAM_AUDIO,
          pipeline->pending_audio.header.remote_ntp_ns, pipeline->pending_audio.arrival_ns,
          running_ns, &pending_mapped);
      if (pending_result == SOURCE_CLOCK_MAPPED || pending_result == SOURCE_CLOCK_RESET_EPOCH) {
        flush_audio = pipeline->pending_audio;
        memset(&pipeline->pending_audio, 0, sizeof(pipeline->pending_audio));
        flush_audio_pts = pending_mapped;
      }
    }
  }
  map_result = source_clock_timeline_map(&pipeline->timeline, header->stream_kind,
                                         header->remote_ntp_ns, running_ns, running_ns,
                                          &mapped_ns);
  if (map_result == SOURCE_CLOCK_DROP_LATE &&
      header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO) {
    /* A dropped compressed AU can be a reference for every following P-frame.
     * Hold the last decoded frame and reject dependent input until an IDR
     * repairs the decoder reference chain. */
    pipeline->video_au_gate.waiting_for_idr = true;
    /* The gate handed this notification to an IDR that never reached appsrc.
     * Re-arm it so the next recovery IDR still resets the decoder. */
    if (au_result == SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE)
      pipeline->video_au_gate.format_change_pending = true;
  }
  /* The same mapping lock covers detached pending_audio and this frame. */
  audio_admission_generation = pipeline->audio_admission_generation;
  g_mutex_unlock(&pipeline->mutex);
  if (emit_input_idr) {
    g_mutex_lock(&pipeline->event_mutex);
    input_idr_written = source_clock_event_write_video_input_idr(
        &pipeline->event_writer, &input_idr_running_ns,
        &input_idr_source_ntp_ns);
    g_mutex_unlock(&pipeline->event_mutex);
    g_mutex_lock(&pipeline->mutex);
    pipeline->video_input_idr_emitted = input_idr_written;
    if (!input_idr_written) pipeline->video_input_idr_reserved = false;
    g_mutex_unlock(&pipeline->mutex);
  }
  for (size_t history_index = 0; history_index < flush_video_history_count; ++history_index) {
    uint64_t history_pts = 0;
    SourceClockTimeline history_timeline;
    g_mutex_lock(&pipeline->mutex);
    if (pipeline->timeline.active_generation != flush_video_history_generation) {
      g_mutex_unlock(&pipeline->mutex);
      for (size_t cleanup_index = history_index; cleanup_index < flush_video_history_count; ++cleanup_index)
        clear_pending_frame(&flush_video_history[cleanup_index]);
      free(owned_payload);
      if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
      return 0;
    }
    history_timeline = pipeline->timeline;
    g_mutex_unlock(&pipeline->mutex);
    SourceClockMapResult history_result = source_clock_timeline_project_history(
        &history_timeline, SOURCE_CLOCK_STREAM_VIDEO,
        flush_video_history[history_index].header.remote_ntp_ns, &history_pts);
    if (history_result == SOURCE_CLOCK_MAPPED &&
        !push_source_buffer_ex(pipeline, &flush_video_history[history_index].header,
                               flush_video_history[history_index].payload,
                               flush_video_history[history_index].payload_bytes,
                               history_pts, audio_admission_generation, TRUE)) {
      mark_video_push_loss(pipeline);
      for (size_t cleanup_index = history_index; cleanup_index < flush_video_history_count; ++cleanup_index)
        clear_pending_frame(&flush_video_history[cleanup_index]);
      clear_pending_frame(&flush_video);
      clear_pending_frame(&flush_audio);
      free(owned_payload);
      if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
      return -1;
    }
    clear_pending_frame(&flush_video_history[history_index]);
  }
  if (flush_video.present) {
    if (!push_source_buffer(pipeline, &flush_video.header, flush_video.payload,
                            flush_video.payload_bytes, flush_video_pts,
                            audio_admission_generation)) {
      mark_video_push_loss(pipeline);
      clear_pending_frame(&flush_video);
      clear_pending_frame(&flush_audio);
      free(owned_payload);
      if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
      return -1;
    }
    clear_pending_frame(&flush_video);
  }
  if (flush_audio.present) {
    if (!push_source_buffer(pipeline, &flush_audio.header, flush_audio.payload,
                            flush_audio.payload_bytes, flush_audio_pts,
                            audio_admission_generation)) {
      clear_pending_frame(&flush_audio);
      free(owned_payload);
      if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
      return -1;
    }
    clear_pending_frame(&flush_audio);
  }
  if (map_result == SOURCE_CLOCK_DROP_LATE || map_result == SOURCE_CLOCK_NOT_READY) {
    free(owned_payload);
    if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
    return 0;
  }
  {
    gboolean pushed = push_source_buffer(pipeline, header, payload, payload_bytes,
                                         mapped_ns, audio_admission_generation);
    if (!pushed && header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO)
      mark_video_push_loss(pipeline);
    int result = pushed ? 0 : -1;
    free(owned_payload);
    if (video_feed_locked) g_mutex_unlock(&pipeline->video_feed_mutex);
    return result;
  }
}

/* Called by the authenticated reader only after a complete frame. Keep proof
 * bookkeeping independent of the later gate/map/push decision. A bad proof
 * input invalidates this publisher's evidence, not the AirPlay connection. */
#include "source_clock_candidate.inc"

static int on_received_source_frame(void *user, const SourceClockHeader *header,
                                    const uint8_t *payload, size_t payload_bytes) {
  SourceClockConnectionContext *connection = user;
  SourceClockPipeline *pipeline = connection->pipeline;
  gboolean invalid = FALSE;
  gboolean candidate = FALSE;
  gboolean candidate_failed = FALSE;
  g_mutex_lock(&pipeline->mutex);
  if (!pipeline->video_watermark_invalid &&
      pipeline->video_watermark_final_state == VIDEO_WATERMARK_OPEN) {
    SourceClockVideoWatermarkResult result = source_clock_video_watermark_offer(
        &pipeline->video_watermark, &connection->watermark, header, payload, payload_bytes);
    if (result == SOURCE_CLOCK_VIDEO_WATERMARK_REJECTED ||
        result == SOURCE_CLOCK_VIDEO_WATERMARK_INCOMPLETE) {
      pipeline->video_watermark_invalid = TRUE;
      invalid = TRUE;
    }
  }
  candidate = pipeline->candidate_proof_enabled &&
              !source_clock_video_proof_complete(&pipeline->candidate_proof);
  if (candidate) {
    if (pipeline->stopping || pipeline->video_watermark_invalid ||
        (header->stream_kind == SOURCE_CLOCK_STREAM_CONTROL &&
         ((header->codec == SOURCE_CLOCK_CONTROL_SESSION_START &&
           header->sequence != pipeline->candidate_proof.source_generation) ||
          (connection->watermark.stream_kind == SOURCE_CLOCK_STREAM_VIDEO &&
           header->codec == SOURCE_CLOCK_CONTROL_SESSION_END)))) {
      source_clock_video_proof_stop(&pipeline->candidate_proof);
      candidate_failed = TRUE;
    }
  }
  g_mutex_unlock(&pipeline->mutex);
  if (invalid)
    g_printerr("AirPlay source-clock video watermark unavailable: invalid source identity/frame\n");
  if (candidate_failed) {
    source_clock_candidate_fail(pipeline);
    return 0;
  }
  if (candidate && header->stream_kind == SOURCE_CLOCK_STREAM_VIDEO)
    return source_clock_candidate_video(pipeline, header, payload, payload_bytes);
  return on_source_frame(pipeline, header, payload, payload_bytes);
}

/* Teardown-owner call after both readers and scheduler join, probe removal,
 * and a confirmed pipeline NULL transition. The latter drains streaming
 * callbacks which may already have reserved another event. No owner lock is
 * held during file I/O. A failed append may have written bytes: never retry. */
static gboolean source_clock_finalize_video_watermark(SourceClockPipeline *pipeline,
                                                       gboolean pipeline_closed) {
  SourceClockVideoWatermarkSnapshot final;
  gboolean written;
  g_mutex_lock(&pipeline->mutex);
  if (pipeline->video_watermark_final_state != VIDEO_WATERMARK_OPEN) {
    written = pipeline->video_watermark_final_state == VIDEO_WATERMARK_WRITTEN;
    g_mutex_unlock(&pipeline->mutex);
    return written;
  }
  if (!pipeline_closed || !pipeline->stopping ||
      pipeline->video_accept_thread != NULL || pipeline->audio_accept_thread != NULL ||
      pipeline->video_scheduler.thread != NULL || !pipeline->video_scheduler.stopping ||
      pipeline->video_encoded_probe_id != 0) {
    g_mutex_unlock(&pipeline->mutex);
    return FALSE;
  }
  final = source_clock_video_watermark_snapshot(&pipeline->video_watermark);
  if (!final.has || pipeline->video_watermark_invalid || pipeline->protocol_error) {
    pipeline->video_watermark_final_state = VIDEO_WATERMARK_UNAVAILABLE;
    g_mutex_unlock(&pipeline->mutex);
    return FALSE;
  }
  pipeline->video_watermark_final_state = VIDEO_WATERMARK_FINALIZING;
  g_mutex_unlock(&pipeline->mutex);
  g_mutex_lock(&pipeline->event_mutex);
  written = source_clock_event_write_video_watermark_final(
      &pipeline->event_writer, final.source_session_generation, final.source_video_sequence);
  g_mutex_unlock(&pipeline->event_mutex);
  g_mutex_lock(&pipeline->mutex);
  pipeline->video_watermark_final_state = written ? VIDEO_WATERMARK_WRITTEN : VIDEO_WATERMARK_FAILED;
  /* Preserve explicit no-signal (20) and earlier error reasons. */
  if (!written && pipeline->exit_code == 0) pipeline->exit_code = 22;
  g_mutex_unlock(&pipeline->mutex);
  if (!written) g_printerr("AirPlay source-clock final video watermark write failed; no retry\n");
  return written;
}

static gpointer source_clock_accept_thread(gpointer user_data) {
  SourceClockAcceptContext *accept_context = (SourceClockAcceptContext *)user_data;
  SourceClockPipeline *pipeline = accept_context->pipeline;
  uint8_t buffer[64 * 1024];
  for (;;) {
    gboolean stopping;
    g_mutex_lock(&pipeline->mutex);
    stopping = pipeline->stopping;
    g_mutex_unlock(&pipeline->mutex);
    if (stopping) break;
    if (!source_clock_socket_readable(accept_context->listener, 250)) continue;
    SourceClockSocket client = accept(accept_context->listener, NULL, NULL);
    SourceClockReader reader;
    SourceClockConnectionContext connection = {0};
    if (client == SOURCE_CLOCK_INVALID_SOCKET) break;
    connection.pipeline = pipeline;
    source_clock_video_watermark_connection_init(&connection.watermark, accept_context->stream_kind);
    source_clock_reader_init(&reader, accept_context->stream_kind, pipeline->token,
                             on_received_source_frame, &connection);
    for (;;) {
      g_mutex_lock(&pipeline->mutex);
      stopping = pipeline->stopping;
      g_mutex_unlock(&pipeline->mutex);
      if (stopping) break;
      if (!source_clock_socket_readable(client, 250)) continue;
      int received = recv(client, (char *)buffer, sizeof(buffer), 0);
      if (received <= 0) break;
      if (source_clock_reader_feed(&reader, buffer, (size_t)received) != SOURCE_CLOCK_READER_OK) {
        signal_pipeline_error(pipeline, TRUE);
        break;
      }
    }
    source_clock_reader_destroy(&reader);
    source_clock_video_watermark_disconnect(&connection.watermark);
    source_clock_socket_close(client);
    g_mutex_lock(&pipeline->mutex);
    stopping = pipeline->stopping;
    g_mutex_unlock(&pipeline->mutex);
    if (stopping) break;
  }
  g_free(accept_context);
  return NULL;
}

static GstPad *find_sink_pad(GstElement *element, const char *name) {
  GstIterator *iterator = gst_element_iterate_sink_pads(element);
  GValue item = G_VALUE_INIT;
  GstIteratorResult result;
  GstPad *found = NULL;
  while ((result = gst_iterator_next(iterator, &item)) == GST_ITERATOR_OK) {
    GstPad *candidate = GST_PAD(g_value_get_object(&item));
    if (candidate != NULL && g_strcmp0(GST_PAD_NAME(candidate), name) == 0) {
      found = GST_PAD(gst_object_ref(candidate));
      g_value_unset(&item);
      break;
    }
    g_value_unset(&item);
  }
  gst_iterator_free(iterator);
  return found;
}

static gboolean configure_audio_payloader(GstElement *pipeline, GError **error) {
  GstElement *sink = gst_bin_get_by_name(GST_BIN(pipeline), "rtsp_sink");
  GstPad *pad;
  GstElement *payloader;
  if (sink == NULL) return FALSE;
  pad = find_sink_pad(sink, "sink_1");
  if (pad == NULL) {
    gst_object_unref(sink);
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_NEGOTIATION,
                "source-clock RTSP audio sink pad was not found");
    return FALSE;
  }
  payloader = gst_element_factory_make("rtpmp4gpay", "source_clock_audio_payloader");
  if (payloader == NULL) {
    gst_object_unref(pad);
    gst_object_unref(sink);
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_MISSING_PLUGIN,
                "rtpmp4gpay is unavailable");
    return FALSE;
  }
  g_object_set(payloader, "pt", 96, NULL);
  /* Element factories return a floating reference. Make the factory-side
   * ownership explicit before the pad property takes its own reference. */
  gst_object_ref_sink(payloader);
  g_object_set(pad, "payloader", payloader, NULL);
  gst_object_unref(payloader);
  gst_object_unref(pad);
  gst_object_unref(sink);
  return TRUE;
}

GstElement *source_clock_create_pipeline(const char *publish_url, const char *recording,
                                         int width, int height,
                                         int source_fps, int output_fps,
                                         int bitrate_kbps, int maxrate_kbps,
                                         int buffer_size_kbps, int audio_bitrate_bps,
                                         int gop,
                                         GError **error) {
  return source_clock_create_pipeline_with_audio_ingress(
      publish_url, recording, width, height, source_fps, output_fps,
      bitrate_kbps, maxrate_kbps, buffer_size_kbps, audio_bitrate_bps,
      gop, TRUE, error);
}

GstElement *source_clock_create_pipeline_with_audio_ingress(
    const char *publish_url, const char *recording,
    int width, int height, int source_fps, int output_fps,
    int bitrate_kbps, int maxrate_kbps,
    int buffer_size_kbps, int audio_bitrate_bps, int gop,
    gboolean include_real_audio_ingress, GError **error) {
  return source_clock_create_pipeline_with_audio_options(
      publish_url, recording, width, height, source_fps, output_fps,
      bitrate_kbps, maxrate_kbps, buffer_size_kbps, audio_bitrate_bps,
      gop, include_real_audio_ingress, TRUE, TRUE, FALSE, error);
}

GstElement *source_clock_create_pipeline_with_audio_options(
    const char *publish_url, const char *recording,
    int width, int height, int source_fps, int output_fps,
    int bitrate_kbps, int maxrate_kbps,
    int buffer_size_kbps, int audio_bitrate_bps, int gop,
    gboolean include_real_audio_ingress, gboolean include_rtsp_audio,
    gboolean include_custom_audio_payloader,
    gboolean disable_rtsp_rtx,
    GError **error) {
  return source_clock_create_pipeline_with_audio_routing(
      publish_url, recording, width, height, source_fps, output_fps,
      bitrate_kbps, maxrate_kbps, buffer_size_kbps, audio_bitrate_bps,
      gop, include_real_audio_ingress, FALSE, include_rtsp_audio,
      include_custom_audio_payloader, disable_rtsp_rtx, error);
}

GstElement *source_clock_create_pipeline_with_audio_routing(
    const char *publish_url, const char *recording,
    int width, int height, int source_fps, int output_fps,
    int bitrate_kbps, int maxrate_kbps,
    int buffer_size_kbps, int audio_bitrate_bps, int gop,
    gboolean include_real_audio_ingress, gboolean include_fixed_pcm_audio,
    gboolean include_rtsp_audio, gboolean include_custom_audio_payloader,
    gboolean disable_rtsp_rtx, GError **error) {
  /* TCP remains the default. The opt-in UDP diagnostic keeps RTP off the RTSP
   * control socket so Windows GSocket read/write wait failures can be isolated
   * without changing downstream MediaMTX outputs. */
  const char *rtsp_protocol =
      IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP ? "udp" : "tcp";
  /* Keep one complete H.264 slice per frame for the PC RTSP decoder path.
   * zerolatency enables sliced threading; override it explicitly after the
   * tune while retaining the preset, quality, VBV and keyframe settings. */
  gchar *description = g_strdup_printf(
      "rtspclientsink name=rtsp_sink protocols=%s latency=200 timeout=0 tcp-timeout=0 do-rtsp-keep-alive=false "
      "mp4mux name=record_mux faststart=true ! filesink name=record_sink "
      "appsrc name=video_scheduled_input is-live=true format=time do-timestamp=false block=false "
      "max-buffers=4 max-bytes=0 max-time=0 leaky-type=downstream "
      "caps=video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1,pixel-aspect-ratio=1/1 ! "
      "queue max-size-buffers=4 leaky=downstream ! videoconvert ! "
      "videorate name=video_output_rate drop-only=true ! "
      "capsfilter name=video_output_caps caps=\"video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1,pixel-aspect-ratio=1/1\" ! "
      "x264enc name=source_clock_video_encoder bitrate=%d pass=qual quantizer=23 key-int-max=%d bframes=0 tune=zerolatency speed-preset=superfast vbv-buf-capacity=1000 byte-stream=true "
      "option-string=\"vbv-maxrate=%d:vbv-bufsize=%d:aud=1:repeat-headers=1:" IMAGEPAD_RTSP_X264_SINGLE_SLICE_OPTIONS "\" ! "
      "h264parse config-interval=1 ! tee name=video_tee "
      "video_tee. ! queue name=video_rtsp_queue ! rtsp_sink.sink_0 "
      "video_tee. ! queue name=video_record_queue max-size-buffers=0 "
      "max-size-bytes=16777216 max-size-time=4000000000 ! h264parse config-interval=1 ! "
      "video/x-h264,stream-format=(string)avc,alignment=(string)au ! record_mux.video_0 "
      "appsrc name=video_input is-live=true format=time do-timestamp=false block=false max-buffers=64 max-bytes=67108864 max-time=600000000 "
      "caps=video/x-h264,stream-format=(string)byte-stream,alignment=(string)au ! "
      "decodebin name=video_decoder force-sw-decoders=true ! "
      "videoconvert ! videoscale add-borders=true ! "
      "video/x-raw,format=I420,width=%d,height=%d,pixel-aspect-ratio=1/1 ! "
      "queue max-size-buffers=3 leaky=downstream ! "
      "appsink name=video_decoded_sink emit-signals=true sync=false async=false max-buffers=3 drop=true "
      "audiotestsrc name=audio_silence is-live=true wave=silence ! "
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! "
      "queue max-size-time=500000000 leaky=downstream ! "
      "audiomixer name=audio_mixer force-live=true ignore-inactive-pads=true "
      "latency=100000000 min-upstream-latency=200000000 ! audioconvert ! audioresample ! "
      "audio/x-raw,format=F32LE,rate=48000,channels=2,layout=interleaved ! "
      "avenc_aac name=source_clock_audio_encoder bitrate=%d ! aacparse ! tee name=audio_tee "
      "%s"
      "audio_tee. ! queue name=audio_record_queue max-size-buffers=0 "
      "max-size-bytes=16777216 max-size-time=4000000000 ! "
      "audio/mpeg,mpegversion=(int)4,stream-format=(string)raw,channels=(int)2,rate=(int)48000 ! record_mux.audio_0 "
      "%s"
      "%s",
      rtsp_protocol,
      /* Schedule directly at the delivery cadence. A 60fps scheduled source
       * plus drop-only videorate can catch up at 60fps after a late first
       * buffer or gap; that exceeds a 30fps encoder/GOP contract. Receiving
       * and decoding the iPhone's source cadence remain independent. */
      width, height, output_fps,
      width, height, output_fps,
      maxrate_kbps, gop, maxrate_kbps, buffer_size_kbps,
      width, height,
      audio_bitrate_bps,
      include_rtsp_audio
          ? "audio_tee. ! queue name=audio_rtsp_queue ! rtsp_sink.sink_1 "
          : "",
      include_real_audio_ingress
          ? "appsrc name=audio_input is-live=true format=time do-timestamp=false block=false max-buffers=32 "
            "! decodebin name=audio_decoder"
          : "",
      include_fixed_pcm_audio
          ? "appsrc name=audio_fixed_pcm_input is-live=true format=time do-timestamp=false block=false emit-signals=false "
            "max-buffers=32 max-bytes=1048576 max-time=500000000 leaky-type=upstream "
            "caps=audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! "
            "queue name=audio_fixed_pcm_queue max-size-buffers=192 max-size-bytes=1048576 "
            "max-size-time=4000000000 leaky=downstream ! audio_mixer. "
          : "");
  GstElement *pipeline = gst_parse_launch(description, error);
  g_free(description);
  if (pipeline == NULL) return NULL;
  g_object_set(pipeline, "message-forward", TRUE, NULL);
  {
    GstElement *rtsp_sink = gst_bin_get_by_name(GST_BIN(pipeline), "rtsp_sink");
    GstElement *record_sink = gst_bin_get_by_name(GST_BIN(pipeline), "record_sink");
    if (rtsp_sink == NULL || record_sink == NULL) {
      if (rtsp_sink != NULL) gst_object_unref(rtsp_sink);
      if (record_sink != NULL) gst_object_unref(record_sink);
      g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_MISSING_PLUGIN,
                  "source-clock output sinks are unavailable");
      gst_object_unref(pipeline);
      return NULL;
    }
    g_object_set(rtsp_sink, "location", publish_url, NULL);
    if (disable_rtsp_rtx) g_object_set(rtsp_sink, "rtx-time", 0u, NULL);
    g_object_set(record_sink, "location", recording == NULL ? "" : recording, NULL);
    gst_object_unref(record_sink);
    gst_object_unref(rtsp_sink);
  }
  if (include_rtsp_audio && include_custom_audio_payloader &&
      !configure_audio_payloader(pipeline, error)) {
    gst_object_unref(pipeline);
    return NULL;
  }
  return pipeline;
}

static gboolean source_clock_stop_watch(gpointer user_data) {
  SourceClockPipeline *pipeline = (SourceClockPipeline *)user_data;
  gboolean should_stop = FALSE;
  gboolean media_started;
  gint no_signal_seconds;
  gint64 last_signal_monotonic_us;
  gint64 now_monotonic_us;
  if (pipeline->stop_file != NULL && g_file_test(pipeline->stop_file, G_FILE_TEST_EXISTS)) {
    pipeline->no_signal = source_clock_stop_request_is_no_signal(pipeline->stop_file);
    pipeline->exit_code = pipeline->no_signal ? 20 : 0;
    should_stop = TRUE;
  }
  now_monotonic_us = g_get_monotonic_time();
  g_mutex_lock(&pipeline->mutex);
  media_started = pipeline->media_ready_emitted;
  no_signal_seconds = pipeline->no_signal_seconds;
  last_signal_monotonic_us = pipeline->last_signal_monotonic_us;
  g_mutex_unlock(&pipeline->mutex);
  if (!should_stop && media_started && no_signal_seconds > 0 &&
      now_monotonic_us - last_signal_monotonic_us >=
          (gint64)no_signal_seconds * G_USEC_PER_SEC) {
    g_printerr("AirPlay source-clock no-signal timeout elapsed=%" G_GINT64_FORMAT "us\n",
               now_monotonic_us - last_signal_monotonic_us);
    pipeline->no_signal = TRUE;
    pipeline->exit_code = 20;
    should_stop = TRUE;
  }
  if (should_stop) {
    g_mutex_lock(&pipeline->mutex);
    pipeline->stopping = TRUE;
    g_mutex_unlock(&pipeline->mutex);
    pipeline->stop_watch_id = 0;
    g_printerr("AirPlay source-clock stop requested; leaving main loop\n");
    g_main_loop_quit(pipeline->loop);
    return G_SOURCE_REMOVE;
  }
  return G_SOURCE_CONTINUE;
}

static gboolean source_clock_bus_watch(GstBus *bus, GstMessage *message, gpointer user_data) {
  SourceClockPipeline *pipeline = (SourceClockPipeline *)user_data;
  (void)bus;
  if (!pipeline->rtsp_eof_emitted &&
      source_clock_message_is_rtsp_eof(message, GST_OBJECT(pipeline->rtsp_sink))) {
    int process_id = source_clock_current_process_id();
    gboolean written;
    g_mutex_lock(&pipeline->event_mutex);
    written = source_clock_event_write_rtsp_eof(&pipeline->event_writer, process_id);
    g_mutex_unlock(&pipeline->event_mutex);
    if (written) {
      pipeline->rtsp_eof_emitted = true;
      g_printerr("AirPlay source-clock RTSP EOF event emitted process=%d\n",
                 process_id);
    } else {
      g_printerr("AirPlay source-clock could not write RTSP EOF event\n");
    }
  }
  if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_ERROR) {
    GError *error = NULL;
    gchar *debug = NULL;
    gst_message_parse_error(message, &error, &debug);
    g_printerr("AirPlay source-clock GStreamer pipeline error: %s%s%s\n",
               error == NULL ? "unknown" : error->message,
               debug == NULL ? "" : " (", debug == NULL ? "" : debug);
    if (debug != NULL) g_printerr(")\n");
    g_clear_error(&error);
    g_free(debug);
    signal_pipeline_error(pipeline, FALSE);
  } else if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_EOS) {
    pipeline->exit_code = 0;
    g_main_loop_quit(pipeline->loop);
  }
  return G_SOURCE_CONTINUE;
}

int airplay_source_clock_pipeline_run(const char *publish_url, const char *recording,
                                      const char *stop_file, const char *session_token,
                                      const char *session_id, uint64_t publisher_generation,
                                      const char *ready_file, const char *media_ready_file,
                                      const char *event_log,
                                      int video_listen_port, int audio_listen_port,
                                      int width, int height,
                                      int source_fps, int output_fps,
                                      int bitrate_kbps, int maxrate_kbps,
                                      int buffer_size_kbps, int audio_bitrate_bps,
                                      int gop, int no_signal_seconds,
                                      uint32_t proof_source_generation, uint32_t proof_video_watermark) {
  SourceClockPipeline context;
  SourceClockAcceptContext *video_accept;
  SourceClockAcceptContext *audio_accept;
  GError *error = NULL;
  unsigned video_port = 0;
  unsigned audio_port = 0;
  GstBus *bus;
  GstStateChangeReturn state;
  gboolean scheduler_started = FALSE;
  SourceClockRecordingFinalizeResult recording_finalize = {false, false, false};
  const gboolean fixed_pcm_audio_enabled =
      IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO;
  const gboolean include_real_audio_ingress =
      !fixed_pcm_audio_enabled && !IMAGEPAD_SOURCE_CLOCK_START_WITHOUT_REAL_AUDIO;
  const gboolean include_rtsp_audio =
      !IMAGEPAD_SOURCE_CLOCK_RTSP_VIDEO_ONLY;
  const gboolean include_custom_audio_payloader =
      !IMAGEPAD_SOURCE_CLOCK_AUTO_AUDIO_PAYLOADER;
  const gboolean disable_rtsp_rtx =
      IMAGEPAD_SOURCE_CLOCK_DISABLE_RTSP_RTX;
  if (publish_url == NULL || session_token == NULL || session_id == NULL ||
      ((proof_source_generation == 0) != (proof_video_watermark == 0)) ||
      publisher_generation == 0 || ready_file == NULL ||
      media_ready_file == NULL || event_log == NULL) return 2;
#ifdef _WIN32
  WSADATA wsa;
  if (WSAStartup(MAKEWORD(2, 2), &wsa) != 0) return 2;
#endif
  memset(&context, 0, sizeof(context));
  if (proof_source_generation != 0) {
    if (!source_clock_video_proof_init(&context.candidate_proof,
          proof_source_generation, proof_video_watermark)) return 2;
    context.candidate_proof_enabled = TRUE;
  }
  if (!source_clock_event_writer_init(&context.event_writer, session_id,
                                      publisher_generation, ready_file,
                                      media_ready_file, event_log) ||
      !decode_token(session_token, context.token) ||
      !bind_listener(&context.video_listener, (unsigned)video_listen_port, &video_port) ||
      !bind_listener(&context.audio_listener, (unsigned)audio_listen_port, &audio_port)) {
    if (context.video_listener != SOURCE_CLOCK_INVALID_SOCKET) source_clock_socket_close(context.video_listener);
    if (context.audio_listener != SOURCE_CLOCK_INVALID_SOCKET) source_clock_socket_close(context.audio_listener);
    return 2;
  }
  gst_init(NULL, NULL);
  context.pipeline = source_clock_create_pipeline_with_audio_routing(
      publish_url, recording, width, height,
      source_fps, output_fps,
      bitrate_kbps, maxrate_kbps, buffer_size_kbps, audio_bitrate_bps,
      gop, include_real_audio_ingress, fixed_pcm_audio_enabled,
      include_rtsp_audio,
      include_custom_audio_payloader, disable_rtsp_rtx, &error);
  if (context.pipeline == NULL) {
    g_printerr("AirPlay source-clock pipeline construction failed: %s\n",
               error == NULL ? "unknown" : error->message);
    g_clear_error(&error);
    source_clock_socket_close(context.video_listener);
    source_clock_socket_close(context.audio_listener);
    return 22;
  }
  if (!source_clock_encoder_policy_apply(context.pipeline, &error)) {
    g_printerr("AirPlay source-clock encoder comparison failed: %s\n",
               error == NULL ? "unknown error" : error->message);
    g_clear_error(&error);
    gst_object_unref(context.pipeline);
    source_clock_socket_close(context.video_listener);
    source_clock_socket_close(context.audio_listener);
    return 22;
  }
  if (context.candidate_proof_enabled &&
      !source_clock_candidate_configure_encoder(context.pipeline)) {
    g_printerr("AirPlay candidate encoded proof caps could not be configured\n");
    gst_object_unref(context.pipeline);
    source_clock_socket_close(context.video_listener);
    source_clock_socket_close(context.audio_listener);
    return 22;
  }
  context.rtsp_sink = gst_bin_get_by_name(GST_BIN(context.pipeline), "rtsp_sink");
  context.loop = g_main_loop_new(NULL, FALSE);
  context.video_source = GST_APP_SRC(gst_bin_get_by_name(GST_BIN(context.pipeline), "video_input"));
  context.audio_source = GST_APP_SRC(gst_bin_get_by_name(GST_BIN(context.pipeline), "audio_input"));
  context.fixed_pcm_source = GST_APP_SRC(
      gst_bin_get_by_name(GST_BIN(context.pipeline), "audio_fixed_pcm_input"));
  {
    GstElement *video_encoder = gst_bin_get_by_name(
        GST_BIN(context.pipeline), "source_clock_video_encoder");
    if (video_encoder != NULL) {
      context.video_encoded_src_pad = gst_element_get_static_pad(video_encoder, "src");
      gst_object_unref(video_encoder);
    }
    if (context.video_encoded_src_pad != NULL) {
      context.video_encoded_probe_id = gst_pad_add_probe(
          context.video_encoded_src_pad, GST_PAD_PROBE_TYPE_BUFFER,
          on_encoded_video_buffer, &context, NULL);
    }
  }
  context.video_scheduler.decoded_sink = GST_APP_SINK(
      gst_bin_get_by_name(GST_BIN(context.pipeline), "video_decoded_sink"));
  context.video_scheduler.output_source = GST_APP_SRC(
      gst_bin_get_by_name(GST_BIN(context.pipeline), "video_scheduled_input"));
  if (include_real_audio_ingress) {
    GstElement *audio_decoder = gst_bin_get_by_name(GST_BIN(context.pipeline), "audio_decoder");
    if (audio_decoder != NULL) {
      g_signal_connect(audio_decoder, "pad-added", G_CALLBACK(audio_decoder_pad_added), &context);
      gst_object_unref(audio_decoder);
    }
  }
  /* Runtime candidate only. Immutable routing flag is set before readers start;
   * compatibility factories and the default legacy ingress remain unchanged. */
  if (!fixed_pcm_audio_enabled && IMAGEPAD_SOURCE_CLOCK_START_WITHOUT_REAL_AUDIO &&
      IMAGEPAD_SOURCE_CLOCK_DYNAMIC_AUDIO_BIN) {
    context.dynamic_audio_enabled = TRUE;
    GstElement *mixer = gst_bin_get_by_name(GST_BIN(context.pipeline), "audio_mixer");
    if (mixer != NULL) {
      context.audio_bin = source_clock_audio_bin_new(
          context.pipeline, mixer, g_main_loop_get_context(context.loop));
      gst_object_unref(mixer); /* new retained its own reference */
    }
    if (context.audio_bin == NULL) {
      context.dynamic_audio_disabled = TRUE;
      g_printerr("AirPlay source-clock dynamic audio unavailable; silence retained\n");
    }
  }
  if (fixed_pcm_audio_enabled) {
    context.fixed_pcm_audio_enabled = TRUE;
    context.fixed_pcm_audio = source_clock_fixed_pcm_audio_new(
        context.fixed_pcm_source, NULL, NULL, &error);
    if (context.fixed_pcm_audio == NULL) {
      context.fixed_pcm_audio_disabled = TRUE;
      g_printerr("AirPlay source-clock fixed PCM audio unavailable: %s; silence retained\n",
                 error == NULL ? "unknown error" : error->message);
      g_clear_error(&error);
    }
  }
  context.started_monotonic_us = g_get_monotonic_time();
  context.last_signal_monotonic_us = 0;
  context.no_signal_seconds = no_signal_seconds;
  context.stop_file = stop_file;
  context.exit_code = 0;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.event_mutex);
  g_mutex_init(&context.video_feed_mutex);
  context.video_feed_mutex_initialized = TRUE;
  g_mutex_init(&context.video_scheduler.mutex);
  context.video_scheduler.owner = &context;
  if (context.rtsp_sink == NULL || context.video_source == NULL ||
      (include_real_audio_ingress && context.audio_source == NULL) ||
      (fixed_pcm_audio_enabled && context.fixed_pcm_source == NULL) ||
      context.video_scheduler.decoded_sink == NULL ||
      context.video_scheduler.output_source == NULL ||
      context.video_encoded_src_pad == NULL || context.video_encoded_probe_id == 0) {
    g_printerr("AirPlay source-clock pipeline elements are incomplete\n");
    signal_pipeline_error(&context, FALSE);
  }
  source_clock_timeline_init(&context.timeline);
  source_clock_audio_frame_clock_init(&context.audio_frame_clock);
  source_clock_audio_caps_state_init(&context.audio_caps_state);
  source_clock_video_au_gate_init(&context.video_au_gate);
  source_clock_video_bootstrap_init(&context.video_bootstrap);
  source_clock_video_pending_fifo_init(&context.video_pending_fifo);
  bus = gst_element_get_bus(context.pipeline);
  context.bus_watch_id = gst_bus_add_watch(bus, source_clock_bus_watch, &context);
  gst_object_unref(bus);
  video_accept = g_new0(SourceClockAcceptContext, 1);
  video_accept->pipeline = &context;
  video_accept->listener = context.video_listener;
  video_accept->stream_kind = SOURCE_CLOCK_STREAM_VIDEO;
  audio_accept = g_new0(SourceClockAcceptContext, 1);
  audio_accept->pipeline = &context;
  audio_accept->listener = context.audio_listener;
  audio_accept->stream_kind = SOURCE_CLOCK_STREAM_AUDIO;
  state = gst_element_set_state(context.pipeline, GST_STATE_PLAYING);
  if (state != GST_STATE_CHANGE_FAILURE) {
    const char *state_name = state == GST_STATE_CHANGE_ASYNC
                                 ? "ASYNC"
                                 : state == GST_STATE_CHANGE_NO_PREROLL
                                       ? "NO_PREROLL"
                                       : "SUCCESS";
    g_printerr("AirPlay source-clock PLAYING request accepted state=%s video=%u audio=%u\n",
               state_name, video_port, audio_port);
  }
  if (state == GST_STATE_CHANGE_FAILURE) {
    signal_pipeline_error(&context, FALSE);
  } else {
    gboolean ready_written;
    g_mutex_lock(&context.event_mutex);
    ready_written = source_clock_event_write_publisher_ready(
        &context.event_writer, (int)video_port, (int)audio_port);
    g_mutex_unlock(&context.event_mutex);
    if (!ready_written) {
    g_printerr("AirPlay source-clock could not write publisher-ready event\n");
    signal_pipeline_error(&context, FALSE);
    } else if (!start_video_scheduler(&context.video_scheduler, &context, context.pipeline,
                                      context.video_scheduler.decoded_sink,
                                      context.video_scheduler.output_source, output_fps)) {
      g_printerr("AirPlay source-clock video scheduler could not start\n");
      signal_pipeline_error(&context, FALSE);
    } else {
      scheduler_started = TRUE;
      g_printerr("AirPlay source-clock publisher-ready event emitted and scheduler started\n");
      if (source_clock_accept_start_allowed(ready_written, scheduler_started)) {
        GError *thread_error = NULL;
        if (!source_clock_start_accept_thread(
                "source-clock-video-accept", &video_accept,
                &context.video_accept_thread, g_thread_try_new, &thread_error) ||
            !source_clock_start_accept_thread(
                "source-clock-audio-accept", &audio_accept,
                &context.audio_accept_thread, g_thread_try_new, &thread_error)) {
          g_printerr("AirPlay source-clock accept thread could not start: %s\n",
                     thread_error == NULL ? "unknown" : thread_error->message);
          g_clear_error(&thread_error);
          signal_pipeline_error(&context, FALSE);
        }
      }
    }
  }
  context.stop_watch_id = g_timeout_add(100, source_clock_stop_watch, &context);
  if (!context.stopping) g_main_loop_run(context.loop);
  g_printerr("AirPlay source-clock cleanup begin\n");
  g_mutex_lock(&context.mutex);
  context.stopping = TRUE;
  if (context.candidate_proof_enabled) source_clock_video_proof_stop(&context.candidate_proof);
  g_mutex_unlock(&context.mutex);
  if (context.video_accept_thread != NULL) {
    g_thread_join(context.video_accept_thread);
    context.video_accept_thread = NULL;
  }
  g_printerr("AirPlay source-clock video reader stopped\n");
  if (context.audio_accept_thread != NULL) {
    g_thread_join(context.audio_accept_thread);
    context.audio_accept_thread = NULL;
  }
  g_printerr("AirPlay source-clock audio reader stopped\n");
  source_clock_close_listeners(&context);
  if (video_accept != NULL) {
    g_free(video_accept);
    video_accept = NULL;
  }
  if (audio_accept != NULL) {
    g_free(audio_accept);
    audio_accept = NULL;
  }
  stop_video_scheduler(&context.video_scheduler);
  g_printerr("AirPlay source-clock scheduler stopped\n");
  if (context.video_encoded_src_pad != NULL && context.video_encoded_probe_id != 0) {
    gst_pad_remove_probe(context.video_encoded_src_pad, context.video_encoded_probe_id);
    context.video_encoded_probe_id = 0;
  }
  if (context.fixed_pcm_audio != NULL) {
    SourceClockFixedPcmAudioStopResult fixed_stop =
        source_clock_fixed_pcm_audio_stop(context.fixed_pcm_audio);
    if (fixed_stop == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE &&
        source_clock_fixed_pcm_audio_free(context.fixed_pcm_audio)) {
      context.fixed_pcm_audio = NULL;
    } else {
      context.fixed_pcm_audio_disabled = TRUE;
      g_printerr("AirPlay source-clock fixed PCM audio retained during final cleanup: stop=%d\n",
                 (int)fixed_stop);
    }
  }
  if (context.stop_watch_id != 0) g_source_remove(context.stop_watch_id);
  if (context.bus_watch_id != 0) g_source_remove(context.bus_watch_id);
  if (!context.no_signal) {
    g_printerr("AirPlay source-clock finalizing pipeline\n");
    recording_finalize = source_clock_recording_finalize(
        context.pipeline, &context.event_writer, recording, 8 * GST_SECOND);
    g_printerr(
        "AirPlay source-clock recording finalize eos=%d closed=%d event=%d\n",
        recording_finalize.eos_complete ? 1 : 0,
        recording_finalize.pipeline_closed ? 1 : 0,
        recording_finalize.event_written ? 1 : 0);
  } else {
    recording_finalize.pipeline_closed =
        source_clock_recording_close_pipeline(context.pipeline);
  }
  if (!recording_finalize.pipeline_closed) {
    g_printerr("AirPlay source-clock pipeline NULL transition was not confirmed\n");
  }
  source_clock_finalize_video_watermark(&context, recording_finalize.pipeline_closed);
  /* Readers/scheduler are joined above. Parent and loop remain alive while
   * stop cancels creation and free releases the bin's independent references. */
  if (context.audio_bin != NULL) {
    SourceClockAudioStopResult audio_stop = source_clock_pipeline_release_audio_bin(
        &context.audio_bin, &context.dynamic_audio_disabled);
    if (audio_stop != SOURCE_CLOCK_AUDIO_STOP_COMPLETE) {
      g_printerr("AirPlay source-clock audio bin retained during final cleanup: stop=%d\n",
                 (int)audio_stop);
      /* C1 owns independent parent/context refs. Keep the non-NULL pointer and
       * its owner ref; later unrefs below cannot invalidate the retained bin. */
    }
  }
  g_mutex_lock(&context.video_scheduler.mutex);
  context.video_scheduler.owner = NULL;
  g_mutex_unlock(&context.video_scheduler.mutex);
  g_printerr("AirPlay source-clock pipeline stopped\n");
  if (context.video_source != NULL) gst_object_unref(context.video_source);
  if (context.audio_source != NULL) gst_object_unref(context.audio_source);
  if (context.fixed_pcm_source != NULL) gst_object_unref(context.fixed_pcm_source);
  if (context.video_scheduler.decoded_sink != NULL) {
    gst_object_unref(context.video_scheduler.decoded_sink);
    context.video_scheduler.decoded_sink = NULL;
  }
  if (context.video_scheduler.output_source != NULL) {
    gst_object_unref(context.video_scheduler.output_source);
    context.video_scheduler.output_source = NULL;
  }
  if (context.rtsp_sink != NULL) gst_object_unref(context.rtsp_sink);
  if (context.video_encoded_src_pad != NULL) {
    gst_object_unref(context.video_encoded_src_pad);
    context.video_encoded_src_pad = NULL;
  }
  gst_object_unref(context.pipeline);
  clear_pending_frame(&context.pending_video);
  source_clock_video_pending_fifo_clear(&context.video_pending_fifo);
  clear_pending_frame(&context.candidate_pending);
  if (context.candidate_decoded_buffer != NULL) gst_buffer_unref(context.candidate_decoded_buffer);
  clear_pending_frame(&context.pending_audio);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_main_loop_unref(context.loop);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.event_mutex);
  if (context.video_feed_mutex_initialized) g_mutex_clear(&context.video_feed_mutex);
  g_mutex_clear(&context.mutex);
  return context.exit_code;
}
