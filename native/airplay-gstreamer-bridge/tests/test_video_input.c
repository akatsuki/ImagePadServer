#include "source_clock_protocol.h"
#include "video_input.h"
#include "source_clock_audio_bin.h"
#include "source_clock_fixed_pcm_audio.h"
#include "source_clock_events.h"
#include <gst/app/gstappsink.h>

#ifdef NDEBUG
#undef NDEBUG
#endif
#include <assert.h>
#include <stdlib.h>
#include <string.h>

/* Exercise the real static callback without exporting a production test API.
 * Rename only external definitions to avoid collisions with the core archive. */
static void test_pipeline_unlock(GMutex *mutex);
static SourceClockAudioBin *test_pipeline_audio_new(
    GstElement *parent, GstElement *mixer, GMainContext *context);
static gboolean test_pipeline_audio_free(SourceClockAudioBin *audio);
static SourceClockFixedPcmAudio *test_pipeline_fixed_audio_new(
    GstAppSrc *fixed_input, SourceClockFixedPcmAudioObserver observer,
    gpointer user_data, GError **error);
static gboolean test_pipeline_fixed_audio_configure(
    SourceClockFixedPcmAudio *audio, const SourceClockAudioFormat *format,
    GError **error);
static gboolean test_pipeline_fixed_audio_free(SourceClockFixedPcmAudio *audio);
static SourceClockFixedPcmAudioStopResult test_pipeline_fixed_audio_stop(
    SourceClockFixedPcmAudio *audio);
static GstFlowReturn test_pipeline_fixed_audio_push(
    SourceClockFixedPcmAudio *audio, GstBuffer *buffer);
static bool test_watermark_event_write(SourceClockEventWriter *writer,
                                       uint64_t generation, uint64_t sequence);
static bool test_candidate_event_write(SourceClockEventWriter *writer,
    SourceClockVideoProofEvent stage, uint32_t generation, uint32_t sequence,
    uint64_t running_time_ns);
#define source_clock_event_write_video_proof test_candidate_event_write
#define source_clock_event_write_video_watermark_final test_watermark_event_write
#define g_mutex_unlock test_pipeline_unlock
#define source_clock_audio_bin_new test_pipeline_audio_new
#define source_clock_audio_bin_free test_pipeline_audio_free
#define source_clock_fixed_pcm_audio_new test_pipeline_fixed_audio_new
#define source_clock_fixed_pcm_audio_configure test_pipeline_fixed_audio_configure
#define source_clock_fixed_pcm_audio_free test_pipeline_fixed_audio_free
#define source_clock_fixed_pcm_audio_stop test_pipeline_fixed_audio_stop
#define source_clock_fixed_pcm_audio_push test_pipeline_fixed_audio_push
#define source_clock_create_pipeline test_video_input_create_pipeline
#define airplay_source_clock_pipeline_run test_video_input_pipeline_run
#include "../source_clock_pipeline.c"
#undef airplay_source_clock_pipeline_run
#undef source_clock_create_pipeline
#undef source_clock_event_write_video_watermark_final
#undef source_clock_event_write_video_proof
#undef source_clock_fixed_pcm_audio_free
#undef source_clock_fixed_pcm_audio_configure
#undef source_clock_fixed_pcm_audio_new
#undef source_clock_fixed_pcm_audio_stop
#undef source_clock_fixed_pcm_audio_push
#undef source_clock_audio_bin_free
#undef source_clock_audio_bin_new
#undef g_mutex_unlock

/* Observe real calls, without substituting stop outcomes or fake bins. The
 * unlock interleave runs a second real control callback between map and push. */
static GMutex *interleave_mutex;
static void (*after_mapping_unlock)(void);
static gboolean track_audio_ownership;
static guint audio_new_calls, audio_free_calls;
static gboolean freed_after_complete;
static gboolean track_fixed_audio_ownership;
static guint fixed_audio_new_calls, fixed_audio_free_calls, fixed_audio_stop_calls;
static guint fixed_audio_configure_calls, fixed_audio_free_attempts;
static gint fixed_audio_stop_override = -1;
static gint fixed_audio_free_override = -1;
static guint fixed_audio_push_calls;
static gint fixed_audio_push_override = -1;
static gboolean fixed_audio_new_fail_once;
static guint fixed_audio_configure_fail_on_call;
static gboolean fixed_audio_push_fail_once;
static GstClockTime fixed_audio_last_pts, fixed_audio_last_duration;
static gboolean fixed_audio_last_discont;
static guint accept_thread_owned_contexts;

static void test_pipeline_unlock(GMutex *mutex) {
  void (*action)(void) = NULL;
  if (mutex == interleave_mutex && after_mapping_unlock != NULL) {
    action = after_mapping_unlock;
    after_mapping_unlock = NULL;
  }
  g_mutex_unlock(mutex);
  if (action != NULL) action();
}

static SourceClockAudioBin *test_pipeline_audio_new(
    GstElement *parent, GstElement *mixer, GMainContext *context) {
  if (track_audio_ownership) ++audio_new_calls;
  return source_clock_audio_bin_new(parent, mixer, context);
}

static gboolean test_pipeline_audio_free(SourceClockAudioBin *audio) {
  SourceClockAudioBinStats stats = {0};
  gboolean released;
  source_clock_audio_bin_get_stats(audio, &stats);
  released = source_clock_audio_bin_free(audio);
  if (track_audio_ownership && released) {
    ++audio_free_calls;
    freed_after_complete = stats.stop_complete;
  }
  return released;
}

static SourceClockFixedPcmAudio *test_pipeline_fixed_audio_new(
    GstAppSrc *fixed_input, SourceClockFixedPcmAudioObserver observer,
    gpointer user_data, GError **error) {
  if (track_fixed_audio_ownership) ++fixed_audio_new_calls;
  if (track_fixed_audio_ownership && fixed_audio_new_fail_once) {
    fixed_audio_new_fail_once = FALSE;
    if (error != NULL)
      *error = g_error_new_literal(g_quark_from_static_string("test-fixed-pcm"),
                                   1, "injected new failure");
    return NULL;
  }
  return source_clock_fixed_pcm_audio_new(fixed_input, observer, user_data, error);
}

static gboolean test_pipeline_fixed_audio_configure(
    SourceClockFixedPcmAudio *audio, const SourceClockAudioFormat *format,
    GError **error) {
  if (track_fixed_audio_ownership) ++fixed_audio_configure_calls;
  if (track_fixed_audio_ownership && fixed_audio_configure_fail_on_call != 0 &&
      fixed_audio_configure_calls == fixed_audio_configure_fail_on_call) {
    fixed_audio_configure_fail_on_call = 0;
    if (error != NULL)
      *error = g_error_new_literal(g_quark_from_static_string("test-fixed-pcm"),
                                   2, "injected configure failure");
    return FALSE;
  }
  return source_clock_fixed_pcm_audio_configure(audio, format, error);
}

static gboolean test_pipeline_fixed_audio_free(SourceClockFixedPcmAudio *audio) {
  if (track_fixed_audio_ownership) ++fixed_audio_free_attempts;
  if (fixed_audio_free_override >= 0)
    return (gboolean)fixed_audio_free_override;
  gboolean released = source_clock_fixed_pcm_audio_free(audio);
  if (track_fixed_audio_ownership && released) ++fixed_audio_free_calls;
  return released;
}

static SourceClockFixedPcmAudioStopResult test_pipeline_fixed_audio_stop(
    SourceClockFixedPcmAudio *audio) {
  if (track_fixed_audio_ownership) ++fixed_audio_stop_calls;
  if (fixed_audio_stop_override >= 0)
    return (SourceClockFixedPcmAudioStopResult)fixed_audio_stop_override;
  return source_clock_fixed_pcm_audio_stop(audio);
}

static GstFlowReturn test_pipeline_fixed_audio_push(
    SourceClockFixedPcmAudio *audio, GstBuffer *buffer) {
  if (track_fixed_audio_ownership) {
    ++fixed_audio_push_calls;
    fixed_audio_last_pts = GST_BUFFER_PTS(buffer);
    fixed_audio_last_duration = GST_BUFFER_DURATION(buffer);
    fixed_audio_last_discont = GST_BUFFER_FLAG_IS_SET(buffer, GST_BUFFER_FLAG_DISCONT);
  }
  if (track_fixed_audio_ownership && fixed_audio_push_fail_once) {
    fixed_audio_push_fail_once = FALSE;
    gst_buffer_unref(buffer);
    return GST_FLOW_ERROR;
  }
  if (fixed_audio_push_override >= 0) {
    gst_buffer_unref(buffer);
    return (GstFlowReturn)fixed_audio_push_override;
  }
  return source_clock_fixed_pcm_audio_push(audio, buffer);
}

static GstAppSrc *make_fixed_pcm_source(void) {
  GstElement *element = gst_element_factory_make("appsrc", NULL);
  GstCaps *caps = gst_caps_from_string(
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved");
  assert(element != NULL && caps != NULL);
  g_object_set(element, "is-live", TRUE, "format", GST_FORMAT_TIME,
      "do-timestamp", FALSE, "block", FALSE, "emit-signals", FALSE,
      "max-buffers", (guint64)32, "max-bytes", (guint64)1048576,
      "max-time", (guint64)(500 * GST_MSECOND),
      "leaky-type", GST_APP_LEAKY_TYPE_UPSTREAM, NULL);
  gst_app_src_set_caps(GST_APP_SRC(element), caps);
  gst_caps_unref(caps);
  return GST_APP_SRC(element);
}

static GstBuffer *make_fixed_pcm_aac_material_frame(guint sample_rate) {
  GError *error = NULL;
  gchar *description = g_strdup_printf(
      "audiotestsrc num-buffers=1 samplesperbuffer=1024 wave=sine freq=600 ! "
      "audio/x-raw,rate=%u,channels=1 ! audioconvert ! avenc_aac ! "
      "aacparse ! audio/mpeg,mpegversion=4,stream-format=adts ! "
      "appsink name=material sync=false async=false", sample_rate);
  GstElement *pipeline = gst_parse_launch(description, &error);
  GstElement *sink;
  GstSample *sample;
  GstBuffer *frame;
  GstState current, pending;
  g_free(description);
  assert(error == NULL && pipeline != NULL);
  sink = gst_bin_get_by_name(GST_BIN(pipeline), "material");
  assert(sink != NULL);
  assert(gst_element_set_state(pipeline, GST_STATE_PLAYING) !=
         GST_STATE_CHANGE_FAILURE);
  sample = gst_app_sink_try_pull_sample(GST_APP_SINK(sink), 2 * GST_SECOND);
  assert(sample != NULL);
  frame = gst_buffer_copy_deep(gst_sample_get_buffer(sample));
  assert(frame != NULL);
  gst_sample_unref(sample);
  assert(gst_element_set_state(pipeline, GST_STATE_NULL) !=
         GST_STATE_CHANGE_FAILURE);
  assert(gst_element_get_state(pipeline, &current, &pending, GST_SECOND) !=
         GST_STATE_CHANGE_FAILURE);
  assert(current == GST_STATE_NULL && pending == GST_STATE_VOID_PENDING);
  gst_object_unref(sink);
  gst_object_unref(pipeline);
  return frame;
}

static void test_codec_caps_follow_source_header(void) {
  assert(strcmp(source_clock_video_caps_string(SOURCE_CLOCK_CODEC_H264_ANNEXB_AU),
                "video/x-h264,stream-format=(string)byte-stream,alignment=(string)au") == 0);
  assert(strcmp(source_clock_video_caps_string(SOURCE_CLOCK_CODEC_H265_ANNEXB_AU),
                "video/x-h265,stream-format=(string)byte-stream,alignment=(string)au") == 0);
  assert(source_clock_video_caps_string(99) == NULL);
}

static void test_accept_start_requires_ready_and_scheduler(void) {
  assert(!source_clock_accept_start_allowed(FALSE, FALSE));
  assert(!source_clock_accept_start_allowed(TRUE, FALSE));
  assert(!source_clock_accept_start_allowed(FALSE, TRUE));
  assert(source_clock_accept_start_allowed(TRUE, TRUE));
  puts("accept start gate requires publisher-ready and scheduler: PASS");
}

static GThread *accept_thread_start_success(const gchar *name,
                                            GThreadFunc function,
                                            gpointer data,
                                            GError **error) {
  static int sentinel;
  (void)name;
  (void)function;
  (void)error;
  g_free(data);
  ++accept_thread_owned_contexts;
  return (GThread *)&sentinel;
}

static GThread *accept_thread_start_failure(const gchar *name,
                                            GThreadFunc function,
                                            gpointer data,
                                            GError **error) {
  (void)name;
  (void)function;
  (void)data;
  g_set_error_literal(error, g_quark_from_static_string("accept-thread-test"),
                      1, "injected thread creation failure");
  return NULL;
}

static void test_accept_thread_start_transfers_context_only_on_success(void) {
  SourceClockAcceptContext *context = g_new0(SourceClockAcceptContext, 1);
  GThread *thread = NULL;
  GError *error = NULL;
  accept_thread_owned_contexts = 0;
  assert(source_clock_start_accept_thread(
      "accept-success", &context, &thread, accept_thread_start_success, &error));
  assert(context == NULL && thread != NULL && error == NULL);
  assert(accept_thread_owned_contexts == 1);

  context = g_new0(SourceClockAcceptContext, 1);
  thread = NULL;
  assert(!source_clock_start_accept_thread(
      "accept-failure", &context, &thread, accept_thread_start_failure, &error));
  assert(context != NULL && thread == NULL && error != NULL);
  g_clear_error(&error);
  g_free(context);
  puts("accept thread creation preserves context ownership on failure: PASS");
}

static void test_listener_ownership_is_taken_only_once(void) {
  SourceClockPipeline pipeline = {0};
  SourceClockListenerPair first;
  SourceClockListenerPair second;
  g_mutex_init(&pipeline.mutex);
  pipeline.video_listener = (SourceClockSocket)111;
  pipeline.audio_listener = (SourceClockSocket)222;

  first = source_clock_take_listeners(&pipeline);
  second = source_clock_take_listeners(&pipeline);
  assert(first.video == (SourceClockSocket)111);
  assert(first.audio == (SourceClockSocket)222);
  assert(second.video == SOURCE_CLOCK_INVALID_SOCKET);
  assert(second.audio == SOURCE_CLOCK_INVALID_SOCKET);
  assert(pipeline.video_listener == SOURCE_CLOCK_INVALID_SOCKET);
  assert(pipeline.audio_listener == SOURCE_CLOCK_INVALID_SOCKET);
  g_mutex_clear(&pipeline.mutex);
  puts("listener ownership can be taken only once: PASS");
}

static void test_stop_waits_for_accept_exit_before_listener_close(void) {
  SourceClockPipeline pipeline = {0};
  SourceClockAcceptContext *context;
  SourceClockListenerPair listeners;
  GError *error = NULL;
  unsigned port = 0;
  gint64 started;
#ifdef _WIN32
  WSADATA wsa;
  assert(WSAStartup(MAKEWORD(2, 2), &wsa) == 0);
#endif
  g_mutex_init(&pipeline.mutex);
  pipeline.loop = g_main_loop_new(NULL, FALSE);
  pipeline.video_listener = SOURCE_CLOCK_INVALID_SOCKET;
  pipeline.audio_listener = SOURCE_CLOCK_INVALID_SOCKET;
  assert(bind_listener(&pipeline.video_listener, 0, &port));
  assert(port != 0);
  context = g_new0(SourceClockAcceptContext, 1);
  context->pipeline = &pipeline;
  context->listener = pipeline.video_listener;
  context->stream_kind = SOURCE_CLOCK_STREAM_VIDEO;
  assert(source_clock_start_accept_thread(
      "accept-stop-order", &context, &pipeline.video_accept_thread,
      g_thread_try_new, &error));
  assert(error == NULL && context == NULL);

  pipeline.media_ready_emitted = true;
  pipeline.no_signal_seconds = 1;
  pipeline.last_signal_monotonic_us =
      g_get_monotonic_time() - 2 * G_USEC_PER_SEC;
  assert(source_clock_stop_watch(&pipeline) == G_SOURCE_REMOVE);
  assert(pipeline.video_listener != SOURCE_CLOCK_INVALID_SOCKET);
  started = g_get_monotonic_time();
  g_thread_join(pipeline.video_accept_thread);
  pipeline.video_accept_thread = NULL;
  assert(g_get_monotonic_time() - started < G_USEC_PER_SEC);
  listeners = source_clock_take_listeners(&pipeline);
  assert(listeners.video != SOURCE_CLOCK_INVALID_SOCKET);
  source_clock_socket_close(listeners.video);
  source_clock_socket_close(listeners.audio);
  g_main_loop_unref(pipeline.loop);
  g_mutex_clear(&pipeline.mutex);
#ifdef _WIN32
  WSACleanup();
#endif
  puts("stop joins accept worker before closing listener: PASS");
}

static void test_separate_configuration_is_prepended_to_first_keyframe(void) {
  const uint8_t configuration[] = {0, 0, 0, 1, 0x67, 0x42, 0, 0, 0, 1, 0x68, 0xce};
  const uint8_t keyframe[] = {0, 0, 0, 1, 0x65, 0x88, 0x84};
  uint8_t expected[sizeof(configuration) + sizeof(keyframe)];
  const uint8_t *output = NULL;
  size_t output_bytes = 0;
  uint8_t *owned = NULL;
  SourceClockVideoBootstrap bootstrap;
  memcpy(expected, configuration, sizeof(configuration));
  memcpy(expected + sizeof(configuration), keyframe, sizeof(keyframe));
  source_clock_video_bootstrap_init(&bootstrap);

  assert(source_clock_video_bootstrap_prepare(
      &bootstrap, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, SOURCE_CLOCK_FLAG_CONFIG,
      configuration, sizeof(configuration), &output, &output_bytes, &owned));
  assert(output == configuration);
  assert(output_bytes == sizeof(configuration));
  assert(owned == NULL);

  assert(source_clock_video_bootstrap_prepare(
      &bootstrap, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, SOURCE_CLOCK_FLAG_KEYFRAME,
      keyframe, sizeof(keyframe), &output, &output_bytes, &owned));
  assert(owned != NULL);
  assert(output == owned);
  assert(output_bytes == sizeof(expected));
  assert(memcmp(output, expected, sizeof(expected)) == 0);
  free(owned);
  source_clock_video_bootstrap_clear(&bootstrap);
}

static void test_individual_sps_and_pps_are_both_prepended_to_first_keyframe(void) {
  const uint8_t sps[] = {0, 0, 0, 1, 0x67, 0x64, 0x00, 0x1f};
  const uint8_t pps[] = {0, 0, 0, 1, 0x68, 0xee, 0x3c, 0xb0};
  const uint8_t keyframe[] = {0, 0, 0, 1, 0x65, 0x88, 0x84};
  const uint8_t expected[] = {
      0, 0, 0, 1, 0x67, 0x64, 0x00, 0x1f,
      0, 0, 0, 1, 0x68, 0xee, 0x3c, 0xb0,
      0, 0, 0, 1, 0x65, 0x88, 0x84,
  };
  const uint8_t *output = NULL;
  size_t output_bytes = 0;
  uint8_t *owned = NULL;
  SourceClockVideoBootstrap bootstrap;
  source_clock_video_bootstrap_init(&bootstrap);

  assert(source_clock_video_bootstrap_prepare(
      &bootstrap, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, SOURCE_CLOCK_FLAG_CONFIG,
      sps, sizeof(sps), &output, &output_bytes, &owned));
  assert(owned == NULL);
  assert(source_clock_video_bootstrap_prepare(
      &bootstrap, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, SOURCE_CLOCK_FLAG_CONFIG,
      pps, sizeof(pps), &output, &output_bytes, &owned));
  assert(owned == NULL);

  assert(source_clock_video_bootstrap_prepare(
      &bootstrap, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, SOURCE_CLOCK_FLAG_KEYFRAME,
      keyframe, sizeof(keyframe), &output, &output_bytes, &owned));
  assert(owned != NULL);
  assert(output == owned);
  assert(output_bytes == sizeof(expected));
  assert(memcmp(output, expected, sizeof(expected)) == 0);
  free(owned);
  source_clock_video_bootstrap_clear(&bootstrap);
}

static void test_configuration_is_not_reused_across_codec_change(void) {
  const uint8_t h264_configuration[] = {0, 0, 0, 1, 0x67};
  const uint8_t h265_keyframe[] = {0, 0, 0, 1, 0x26};
  const uint8_t *output = NULL;
  size_t output_bytes = 0;
  uint8_t *owned = NULL;
  SourceClockVideoBootstrap bootstrap;
  source_clock_video_bootstrap_init(&bootstrap);
  assert(source_clock_video_bootstrap_prepare(
      &bootstrap, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU, SOURCE_CLOCK_FLAG_CONFIG,
      h264_configuration, sizeof(h264_configuration), &output, &output_bytes, &owned));
  assert(source_clock_video_bootstrap_prepare(
      &bootstrap, SOURCE_CLOCK_CODEC_H265_ANNEXB_AU, SOURCE_CLOCK_FLAG_KEYFRAME,
      h265_keyframe, sizeof(h265_keyframe), &output, &output_bytes, &owned));
  assert(output == h265_keyframe);
  assert(output_bytes == sizeof(h265_keyframe));
  assert(owned == NULL);
  source_clock_video_bootstrap_clear(&bootstrap);
}

static void assert_header_fields(const SourceClockHeader *actual,
                                 const SourceClockHeader *input,
                                 uint16_t expected_flags) {
  assert(actual->flags == expected_flags);
  assert(actual->version == input->version);
  assert(actual->header_bytes == input->header_bytes);
  assert(actual->stream_kind == input->stream_kind);
  assert(actual->codec == input->codec);
  assert(actual->payload_bytes == input->payload_bytes);
  assert(actual->sequence == input->sequence);
  assert(actual->remote_ntp_ns == input->remote_ntp_ns);
  assert(actual->sample_count == input->sample_count);
  assert(actual->sample_rate == input->sample_rate);
  assert(actual->channels == input->channels);
  assert(actual->reserved == input->reserved);
}

static void test_format_change_header_survives_callback_and_does_not_leak_flags(void) {
  /* Annex-B gate fixtures, as in test_video_au_gate; no decoder is started. */
  const uint8_t format_a[] = {
      0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
      0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2,
      0, 0, 0, 1, 0x65, 0x88, 0x84,
  };
  const uint8_t format_b[] = {
      0, 0, 0, 1, 0x67, 0x4d, 0, 0x20,
      0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2,
      0, 0, 0, 1, 0x65, 0x88, 0x84,
  };
  const uint8_t pframe[] = {0, 0, 0, 1, 0x41, 0x9a, 0x20};
  const SourceClockHeader input_a = {
      1, 40, SOURCE_CLOCK_STREAM_VIDEO, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      0x0003, sizeof(format_a), 100, 1000000000ULL, 0, 0, 0, 0,
  };
  const SourceClockHeader input_b = {
      1, 40, SOURCE_CLOCK_STREAM_VIDEO, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      0x0003, sizeof(format_b), 101, 1033333333ULL, 0, 0, 0, 0,
  };
  const SourceClockHeader input_p = {
      1, 40, SOURCE_CLOCK_STREAM_VIDEO, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      0, sizeof(pframe), 102, 1066666666ULL, 0, 0, 0, 0,
  };
  SourceClockHeader caller_a = input_a;
  SourceClockHeader caller_b = input_b;
  SourceClockHeader caller_p = input_p;
  SourceClockPendingFrame saved_b;
  SourceClockPipeline context = {0};
  SourceClockVideoAUGate probe;
  g_mutex_init(&context.mutex);
  source_clock_timeline_init(&context.timeline);
  source_clock_video_au_gate_init(&context.video_au_gate);
  source_clock_video_bootstrap_init(&context.video_bootstrap);
  context.started_monotonic_us = g_get_monotonic_time();

  assert(on_source_frame(&context, &caller_a, format_a, sizeof(format_a)) == 0);
  assert(!context.timeline.ready);
  assert(context.video_au_gate.initialized);
  assert(context.pending_video.present);
  assert_header_fields(&context.pending_video.header, &input_a, 0x0003);
  assert_header_fields(&caller_a, &input_a, 0x0003);

  /* Check reachability with the real gate on a copy of the callback's A state.
   * Leave the callback's gate untouched so B must take the same transition. */
  probe = context.video_au_gate;
  assert(source_clock_video_au_gate_offer(
      &probe, input_b.codec, format_b, sizeof(format_b), input_b.flags) ==
      SOURCE_CLOCK_VIDEO_AU_FORMAT_CHANGE);
  assert(on_source_frame(&context, &caller_b, format_b, sizeof(format_b)) == 0);
  assert(!context.timeline.ready);
  assert(context.video_au_gate.sps_hash == probe.sps_hash);
  assert(context.pending_video.present);
  assert_header_fields(&context.pending_video.header, &input_b, 0x0007);
  assert_header_fields(&caller_b, &input_b, 0x0003);
  assert(context.pending_video.payload_bytes == sizeof(format_b));
  assert(memcmp(context.pending_video.payload, format_b, sizeof(format_b)) == 0);

  /* Retain the actual pending value after callback return. Detach ownership so
   * the existing keep-IDR policy cannot hide the following P-frame's header. */
  saved_b = context.pending_video;
  memset(&context.pending_video, 0, sizeof(context.pending_video));
  assert(on_source_frame(&context, &caller_p, pframe, sizeof(pframe)) == 0);
  assert(!context.timeline.ready);
  assert(context.pending_video.present);
  assert_header_fields(&context.pending_video.header, &input_p, 0);
  assert_header_fields(&caller_p, &input_p, 0);
  assert_header_fields(&caller_b, &input_b, 0x0003);
  assert_header_fields(&saved_b.header, &input_b, 0x0007);
  assert(context.pending_video.payload_bytes == sizeof(pframe));
  assert(memcmp(context.pending_video.payload, pframe, sizeof(pframe)) == 0);
  assert(memcmp(saved_b.payload, format_b, sizeof(format_b)) == 0);

  clear_pending_frame(&saved_b);
  clear_pending_frame(&context.pending_video);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.mutex);
  puts("format A -> FORMAT_CHANGE+IDR -> P-frame: callback header checks PASS");
}

/* Observe the real callback's appsrc output, after draining its first buffer
 * (appsrc itself marks that first buffer DISCONT). No decoder is started. */
static void test_config_only_format_change_retains_discont(gboolean drop_recovery_idr) {
  const uint8_t config_a[] = {0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
                              0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2};
  const uint8_t config_b[] = {0, 0, 0, 1, 0x67, 0x4d, 0, 0x20,
                              0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2};
  const uint8_t idr[] = {0, 0, 0, 1, 0x65, 0x88, 0x84};
  const uint8_t pframe[] = {0, 0, 0, 1, 0x41, 0x9a, 0x20};
  uint8_t expected[sizeof(config_b) + sizeof(idr)];
  SourceClockPipeline context = {0};
  SourceClockHeader header = {
      1, 40, SOURCE_CLOCK_STREAM_VIDEO, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      SOURCE_CLOCK_FLAG_CONFIG, sizeof(config_a), 1, 500 * GST_MSECOND, 0, 0, 0, 0};
  SourceClockHeader caller;
  GError *error = NULL;
  GstElement *transport = gst_parse_launch(
      "appsrc name=src is-live=true format=time do-timestamp=false ! "
      "appsink name=sink sync=false async=false", &error);
  GstAppSink *sink;
  GstSample *sample;
  GstBuffer *buffer;
  GstMapInfo map;
  assert(error == NULL && transport != NULL);
  context.video_source = GST_APP_SRC(gst_bin_get_by_name(GST_BIN(transport), "src"));
  sink = GST_APP_SINK(gst_bin_get_by_name(GST_BIN(transport), "sink"));
  assert(context.video_source != NULL && sink != NULL);
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.event_mutex);
  source_clock_timeline_init(&context.timeline);
  source_clock_video_au_gate_init(&context.video_au_gate);
  source_clock_video_bootstrap_init(&context.video_bootstrap);
  context.timeline.ready = true;
  context.timeline.has_first_video = true;
  context.timeline.output_origin_ns = 5 * GST_SECOND;
  context.started_monotonic_us = g_get_monotonic_time();
  assert(gst_element_set_state(transport, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);

  assert(on_source_frame(&context, &header, config_a, sizeof(config_a)) == 0);
  sample = gst_app_sink_try_pull_sample(sink, GST_SECOND);
  assert(sample != NULL);
  gst_sample_unref(sample);
  header.flags = SOURCE_CLOCK_FLAG_KEYFRAME;
  header.payload_bytes = sizeof(idr);
  assert(on_source_frame(&context, &header, idr, sizeof(idr)) == 0);
  sample = gst_app_sink_try_pull_sample(sink, GST_SECOND);
  assert(sample != NULL);
  assert(!GST_BUFFER_FLAG_IS_SET(gst_sample_get_buffer(sample), GST_BUFFER_FLAG_DISCONT));
  gst_sample_unref(sample);

  header.flags = SOURCE_CLOCK_FLAG_CONFIG;
  header.payload_bytes = sizeof(config_b);
  caller = header;
  assert(on_source_frame(&context, &caller, config_b, sizeof(config_b)) == 0);
  assert_header_fields(&caller, &header, SOURCE_CLOCK_FLAG_CONFIG);
  assert(gst_app_sink_try_pull_sample(sink, 0) == NULL);
  if (drop_recovery_idr) {
    /* Older than the last source point by >100ms: real timeline DROP_LATE. */
    header.remote_ntp_ns = 0;
    header.flags = SOURCE_CLOCK_FLAG_KEYFRAME;
    header.payload_bytes = sizeof(idr);
    caller = header;
    assert(on_source_frame(&context, &caller, idr, sizeof(idr)) == 0);
    assert_header_fields(&caller, &header, SOURCE_CLOCK_FLAG_KEYFRAME);
    assert(context.video_au_gate.waiting_for_idr);
    assert(gst_app_sink_try_pull_sample(sink, 0) == NULL);
    header.flags = 0;
    header.payload_bytes = sizeof(pframe);
    header.remote_ntp_ns = 520 * GST_MSECOND;
    assert(on_source_frame(&context, &header, pframe, sizeof(pframe)) == 0);
    assert(gst_app_sink_try_pull_sample(sink, 0) == NULL);
  }

  header.flags = SOURCE_CLOCK_FLAG_KEYFRAME;
  header.payload_bytes = sizeof(idr);
  header.remote_ntp_ns = 533 * GST_MSECOND;
  caller = header;
  assert(on_source_frame(&context, &caller, idr, sizeof(idr)) == 0);
  assert_header_fields(&caller, &header, SOURCE_CLOCK_FLAG_KEYFRAME);
  sample = gst_app_sink_try_pull_sample(sink, GST_SECOND);
  assert(sample != NULL);
  buffer = gst_sample_get_buffer(sample);
  assert(GST_BUFFER_FLAG_IS_SET(buffer, GST_BUFFER_FLAG_DISCONT));
  assert(GST_BUFFER_PTS(buffer) == 5533 * GST_MSECOND);
  memcpy(expected, config_b, sizeof(config_b));
  memcpy(expected + sizeof(config_b), idr, sizeof(idr));
  assert(gst_buffer_map(buffer, &map, GST_MAP_READ));
  if (map.size != sizeof(expected) || memcmp(map.data, expected, sizeof(expected)) != 0) {
    fprintf(stderr, "unexpected size=%zu want=%zu\\n", map.size, sizeof(expected));
    for (gsize i = 0; i < map.size; ++i) fprintf(stderr, "%02x", map.data[i]);
    fprintf(stderr, "\\n");
    assert(0);
  }
  gst_buffer_unmap(buffer, &map);
  gst_sample_unref(sample);

  header.flags = 0;
  header.payload_bytes = sizeof(pframe);
  header.remote_ntp_ns = 566 * GST_MSECOND;
  assert(on_source_frame(&context, &header, pframe, sizeof(pframe)) == 0);
  sample = gst_app_sink_try_pull_sample(sink, GST_SECOND);
  assert(sample != NULL);
  assert(!GST_BUFFER_FLAG_IS_SET(gst_sample_get_buffer(sample), GST_BUFFER_FLAG_DISCONT));
  assert(GST_BUFFER_PTS(gst_sample_get_buffer(sample)) == 5566 * GST_MSECOND);
  gst_sample_unref(sample);
  assert(gst_app_sink_try_pull_sample(sink, 0) == NULL);
  assert(gst_element_set_state(transport, GST_STATE_NULL) != GST_STATE_CHANGE_FAILURE);
  gst_object_unref(context.video_source);
  gst_object_unref(sink);
  gst_object_unref(transport);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.event_mutex);
  g_mutex_clear(&context.mutex);
  puts(drop_recovery_idr ? "CONFIG-only -> late IDR -> recovery IDR DISCONT -> P: PASS"
                         : "CONFIG-only -> IDR DISCONT -> P: PASS");
}

/* A bridge-local late drop removes a compressed reference frame.  Following
 * P-frames must not reach the decoder until a clean IDR repairs the chain. */
static void test_late_video_drop_waits_for_recovery_idr(void) {
  const uint8_t idr[] = {
      0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
      0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2,
      0, 0, 0, 1, 0x65, 0x88, 0x84,
  };
  const uint8_t pframe[] = {0, 0, 0, 1, 0x41, 0x9a, 0x20};
  SourceClockHeader header = {
      1, 40, SOURCE_CLOCK_STREAM_VIDEO, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      0, sizeof(pframe), 102, 0, 0, 0, 0, 0,
  };
  SourceClockPipeline context = {0};
  g_mutex_init(&context.mutex);
  source_clock_timeline_init(&context.timeline);
  source_clock_video_au_gate_init(&context.video_au_gate);
  source_clock_video_bootstrap_init(&context.video_bootstrap);
  assert(source_clock_video_au_gate_offer(
      &context.video_au_gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      idr, sizeof(idr), SOURCE_CLOCK_FLAG_KEYFRAME) == SOURCE_CLOCK_VIDEO_AU_ACCEPT);
  context.timeline.ready = true;
  context.timeline.has_first_video = true;
  context.timeline.source_origin_ns = 0;
  context.timeline.output_origin_ns = 0;
  context.started_monotonic_us = g_get_monotonic_time() - 500000;

  assert(on_source_frame(&context, &header, pframe, sizeof(pframe)) == 0);
  assert(context.video_au_gate.waiting_for_idr);
  assert(source_clock_video_au_gate_offer(
      &context.video_au_gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      pframe, sizeof(pframe), 0) == SOURCE_CLOCK_VIDEO_AU_DROP);
  assert(source_clock_video_au_gate_offer(
      &context.video_au_gate, SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      idr, sizeof(idr), SOURCE_CLOCK_FLAG_KEYFRAME) == SOURCE_CLOCK_VIDEO_AU_ACCEPT);

  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.mutex);
  puts("late compressed video drop -> wait for recovery IDR: PASS");
}

/* A corrupt decoder output must not replace the last known-good frame held by
 * the fixed-cadence scheduler. */
static void test_corrupt_decoded_video_is_not_scheduled(void) {
  GError *error = NULL;
  GstElement *pipeline = gst_parse_launch(
      "appsrc name=src is-live=true format=time do-timestamp=false "
      "caps=video/x-raw,format=I420,width=2,height=2,framerate=30/1 ! "
      "appsink name=sink sync=false",
      &error);
  GstAppSrc *src;
  GstAppSink *sink;
  GstBuffer *buffer;
  SourceClockVideoSchedulerRuntime runtime = {0};
  assert(error == NULL && pipeline != NULL);
  src = GST_APP_SRC(gst_bin_get_by_name(GST_BIN(pipeline), "src"));
  sink = GST_APP_SINK(gst_bin_get_by_name(GST_BIN(pipeline), "sink"));
  assert(src != NULL && sink != NULL);
  g_mutex_init(&runtime.mutex);
  source_clock_video_scheduler_init(&runtime.policy, GST_SECOND / 30);
  assert(gst_element_set_state(pipeline, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);

  buffer = gst_buffer_new_allocate(NULL, 6, NULL);
  assert(buffer != NULL);
  GST_BUFFER_PTS(buffer) = 0;
  GST_BUFFER_DURATION(buffer) = GST_SECOND / 30;
  GST_BUFFER_FLAG_SET(buffer, GST_BUFFER_FLAG_CORRUPTED);
  assert(gst_app_src_push_buffer(src, buffer) == GST_FLOW_OK);
  assert(on_decoded_video_sample(sink, &runtime) == GST_FLOW_OK);
  assert(runtime.policy.queue_size == 0);
  assert(runtime.offered_frames == 0);

  gst_element_set_state(pipeline, GST_STATE_NULL);
  gst_object_unref(src);
  gst_object_unref(sink);
  gst_object_unref(pipeline);
  g_mutex_clear(&runtime.mutex);
  puts("corrupt decoded video does not replace held frame: PASS");
}

/* BLACK must be a scheduler output only, never a fake policy frame.  It must
 * be an independent I420 buffer, while real FRAME/HOLD output remains the
 * only path that can arm the first-real-video event. */
static void test_black_scheduler_output_is_not_real_video(void) {
  GstElement *source = gst_element_factory_make("appsrc", NULL);
  GstCaps *caps = gst_caps_from_string(
      "video/x-raw,format=I420,width=3,height=3,framerate=60/1,"
      "pixel-aspect-ratio=1/1");
  GstBuffer *black;
  GstBuffer *output;
  GstBuffer *real;
  GstVideoInfo info;
  GstVideoFrame mapped;
  SourceClockVideoSchedulerRuntime runtime = {0};
  SourceClockVideoSchedulerOutputOrigin origin;
  GError *error = NULL;
  guint plane;

  assert(source != NULL && caps != NULL);
  gst_app_src_set_caps(GST_APP_SRC(source), caps);
  black = create_black_video_buffer(GST_APP_SRC(source), &error);
  assert(error == NULL && black != NULL);
  assert(gst_video_info_from_caps(&info, caps));
  assert(gst_video_frame_map(&mapped, &info, black, GST_MAP_READ));
  for (plane = 0; plane < GST_VIDEO_INFO_N_PLANES(&info); ++plane) {
    const guint8 value = plane == 0 ? 16 : 128;
    const gint stride = GST_VIDEO_FRAME_PLANE_STRIDE(&mapped, plane);
    const guint width = GST_VIDEO_FRAME_COMP_WIDTH(&mapped, plane);
    const guint height = GST_VIDEO_FRAME_COMP_HEIGHT(&mapped, plane);
    const guint pixel_stride = GST_VIDEO_FRAME_COMP_PSTRIDE(&mapped, plane);
    guint row;
    assert(stride > 0 && width > 0 && height > 0 && pixel_stride > 0);
    for (row = 0; row < height; ++row) {
      const guint8 *data = (const guint8 *)GST_VIDEO_FRAME_PLANE_DATA(&mapped, plane) +
                           (gsize)row * (gsize)stride;
      guint column;
      for (column = 0; column < width * pixel_stride; ++column)
        assert(data[column] == value);
      for (; column < (guint)stride; ++column)
        assert(data[column] == 0);
    }
  }
  gst_video_frame_unmap(&mapped);

  g_mutex_init(&runtime.mutex);
  runtime.black_buffer = gst_buffer_ref(black);
  source_clock_video_scheduler_init(&runtime.policy, GST_SECOND / 60);
  output = take_scheduled_video_buffer(&runtime, 0, &origin);
  assert(output != NULL && origin == SOURCE_CLOCK_VIDEO_OUTPUT_BLACK);
  assert(!runtime.policy.has_last);
  gst_buffer_unref(output);
  output = take_scheduled_video_buffer(&runtime, GST_SECOND / 60, &origin);
  assert(output != NULL && origin == SOURCE_CLOCK_VIDEO_OUTPUT_BLACK);
  assert(!runtime.policy.has_last);
  gst_buffer_unref(output);

  real = gst_buffer_new_allocate(NULL, 6, NULL);
  assert(real != NULL);
  assert(source_clock_video_scheduler_offer(
      &runtime.policy, (SourceClockVideoFrame){1, 0, real}));
  output = take_scheduled_video_buffer(&runtime, 0, &origin);
  assert(output != NULL && origin == SOURCE_CLOCK_VIDEO_OUTPUT_FRAME);
  assert(runtime.policy.has_last && runtime.policy.last.id == 1);
  gst_buffer_unref(output);
  output = take_scheduled_video_buffer(&runtime, GST_SECOND / 60, &origin);
  assert(output != NULL && origin == SOURCE_CLOCK_VIDEO_OUTPUT_HOLD);
  gst_buffer_unref(output);

  clear_video_scheduler_runtime(&runtime);
  gst_buffer_unref(black);
  gst_caps_unref(caps);
  gst_object_unref(source);
  g_mutex_clear(&runtime.mutex);
  puts("black scheduler output is isolated from real-video state: PASS");
}

static SourceClockHeader audio_header(uint8_t codec) {
  SourceClockHeader header = {0};
  header.stream_kind = SOURCE_CLOCK_STREAM_AUDIO;
  header.codec = codec;
  header.remote_ntp_ns = 100 * GST_SECOND;
  header.sample_rate = 48000;
  header.channels = 1;
  header.sample_count = 1024;
  return header;
}

static void init_audio_adapter_test(SourceClockPipeline *context) {
  memset(context, 0, sizeof(*context));
  g_mutex_init(&context->mutex);
  context->dynamic_audio_enabled = TRUE;
  context->audio_bin = source_clock_audio_bin_new(NULL, NULL, NULL);
  assert(context->audio_bin != NULL);
}

static void clear_audio_adapter_test(SourceClockPipeline *context) {
  source_clock_audio_bin_free(context->audio_bin);
  context->audio_bin = NULL;
  g_mutex_clear(&context->mutex);
}

static void assert_audio_not_fatal(const SourceClockPipeline *context) {
  assert(!context->fatal_error);
  assert(!context->protocol_error);
  assert(!context->stopping);
  assert(context->exit_code == 0);
}

static void test_dynamic_audio_header_pts_and_duration(void) {
  const uint8_t payload[] = {0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4};
  SourceClockPipeline context;
  SourceClockHeader header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockAudioFormat format = {0};
  SourceClockAudioBinStats stats = {0};
  init_audio_adapter_test(&context);
  assert(push_source_buffer(&context, &header, payload, sizeof(payload), 7 * GST_SECOND, 0));
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(stats.configured && stats.pending_frames == 1);
  assert(stats.pending_bytes == sizeof(payload));
  assert(stats.oldest_pts == 7 * GST_SECOND);
  assert(stats.pending_duration == 21333333);
  assert(source_clock_audio_bin_get_format(context.audio_bin, &format));
  assert(format.codec == SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  assert(format.sample_rate == 48000 && format.channels == 1);
  assert(format.samples_per_frame == 1024 && format.codec_data == NULL);
  source_clock_audio_format_clear(&format);
  /* Remote NTP is unchanged: only mapped PTS controls bin acceptance. */
  assert(push_source_buffer(&context, &header, payload, sizeof(payload),
                            7 * GST_SECOND + 21333333, 0));
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(stats.pending_frames == 2);
  assert(stats.pending_duration == 42666666);
  assert_audio_not_fatal(&context);
  clear_audio_adapter_test(&context);
}

static void test_dynamic_aac_eld_header_pts_and_duration(void) {
  const uint8_t payload[] = {0x01, 0x02, 0x03, 0x04};
  SourceClockPipeline context;
  SourceClockHeader header = audio_header(SOURCE_CLOCK_CODEC_AAC_ELD_RAW);
  SourceClockAudioFormat format = {0};
  SourceClockAudioBinStats stats = {0};
  const GstClockTime duration = gst_util_uint64_scale(480, GST_SECOND, 44100);
  header.sample_rate = 44100;
  header.channels = 2;
  header.sample_count = 480;
  init_audio_adapter_test(&context);
  assert(push_source_buffer(&context, &header, payload, sizeof(payload), 7 * GST_SECOND, 0));
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(stats.configured && stats.pending_frames == 1);
  assert(stats.pending_bytes == sizeof(payload));
  assert(stats.oldest_pts == 7 * GST_SECOND && stats.pending_duration == duration);
  assert(source_clock_audio_bin_get_format(context.audio_bin, &format));
  assert(format.codec == SOURCE_CLOCK_CODEC_AAC_ELD_RAW);
  assert(format.sample_rate == 44100 && format.channels == 2);
  assert(format.samples_per_frame == 480 && format.codec_data == NULL);
  source_clock_audio_format_clear(&format);
  assert_audio_not_fatal(&context);
  clear_audio_adapter_test(&context);
}

static void test_dynamic_audio_failures_are_terminal_audio_only(void) {
  const uint8_t payload[] = {1, 2, 3, 4};
  SourceClockPipeline context;
  SourceClockHeader header;
  SourceClockAudioBinStats before, after;
  unsigned scenario;
  for (scenario = 0; scenario < 4; ++scenario) {
    init_audio_adapter_test(&context);
    header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
    if (scenario == 0) header.codec = SOURCE_CLOCK_CODEC_ALAC_RAW;
    if (scenario == 1) header.sample_rate = 0;
    if (scenario == 2) source_clock_audio_bin_stop(context.audio_bin);
    if (scenario == 3) {
      assert(push_source_buffer(&context, &header, payload, sizeof(payload), 7 * GST_SECOND, 0));
    }
    audio_new_calls = audio_free_calls = 0;
    track_audio_ownership = TRUE;
    assert(push_source_buffer(&context, &header, payload, sizeof(payload), 6 * GST_SECOND, 0));
    track_audio_ownership = FALSE;
    assert(context.dynamic_audio_disabled);
    assert(context.audio_admission_generation == 0);
    assert(audio_new_calls == 0 && audio_free_calls == 0);
    assert_audio_not_fatal(&context);
    source_clock_audio_bin_get_stats(context.audio_bin, &before);
    /* A later valid frame cannot create a retry loop or fall into legacy audio. */
    header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
    assert(push_source_buffer(&context, &header, payload, sizeof(payload), 8 * GST_SECOND, 0));
    source_clock_audio_bin_get_stats(context.audio_bin, &after);
    assert(after.pending_frames == before.pending_frames);
    assert(after.rejected_frames == before.rejected_frames);
    assert(after.configure_operations == before.configure_operations);
    assert_audio_not_fatal(&context);
    clear_audio_adapter_test(&context);
  }
}

static void test_legacy_audio_without_bin_keeps_appsrc_path(void) {
  const uint8_t payload[] = {1, 2, 3, 4};
  SourceClockPipeline context = {0};
  SourceClockHeader header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  GstCaps *caps;
  gint rate, channels;
  guint64 queued = 0;
  context.audio_source = GST_APP_SRC(gst_element_factory_make("appsrc", NULL));
  assert(context.audio_source != NULL);
  assert(set_audio_input_caps(&context, &header));
  caps = gst_app_src_get_caps(context.audio_source);
  assert(gst_structure_get_int(gst_caps_get_structure(caps, 0), "rate", &rate));
  assert(gst_structure_get_int(gst_caps_get_structure(caps, 0), "channels", &channels));
  /* Existing fixed caps deliberately remain unchanged in this task. */
  assert(rate == 44100 && channels == 2);
  gst_caps_unref(caps);
  assert(push_source_buffer(&context, &header, payload, sizeof(payload), 7 * GST_SECOND, 0));
  g_object_get(context.audio_source, "current-level-buffers", &queued, NULL);
  assert(queued == 1 && context.audio_bin == NULL);
  gst_object_unref(context.audio_source);
}

typedef struct {
  GMutex mutex;
  GCond cond;
  gboolean entered, release;
} PipelineEOSHold;

static GstPadProbeReturn hold_audio_bin_eos(
    GstPad *pad, GstPadProbeInfo *info, gpointer data) {
  PipelineEOSHold *hold = data;
  (void)pad;
  if (GST_EVENT_TYPE(GST_PAD_PROBE_INFO_EVENT(info)) != GST_EVENT_EOS)
    return GST_PAD_PROBE_OK;
  g_mutex_lock(&hold->mutex);
  hold->entered = TRUE;
  g_cond_broadcast(&hold->cond);
  while (!hold->release) g_cond_wait(&hold->cond, &hold->mutex);
  g_mutex_unlock(&hold->mutex);
  return GST_PAD_PROBE_OK;
}

static GstElement *find_audio_bin_appsrc(GstElement *parent) {
  GstIterator *iterator = gst_bin_iterate_recurse(GST_BIN(parent));
  GValue item = G_VALUE_INIT;
  GstElement *found = NULL;
  while (found == NULL && gst_iterator_next(iterator, &item) == GST_ITERATOR_OK) {
    GstElement *element = g_value_get_object(&item);
    GstElementFactory *factory = gst_element_get_factory(element);
    if (factory != NULL &&
        strcmp(gst_plugin_feature_get_name(GST_PLUGIN_FEATURE(factory)), "appsrc") == 0)
      found = gst_object_ref(element);
    g_value_reset(&item);
  }
  g_value_unset(&item);
  gst_iterator_free(iterator);
  return found;
}

/* Catches a production caller that discards the sole owner pointer after
 * PENDING/free(FALSE). The real audio-bin worker is held at appsrc EOS while
 * this thread owns its configured main context. */
static void test_audio_bin_caller_retains_pending_owner(gboolean new_session) {
  GstElement *parent = gst_parse_launch(
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=audio_mixer ignore-inactive-pads=true ! fakesink sync=false async=false", NULL);
  GstElement *mixer = gst_bin_get_by_name(GST_BIN(parent), "audio_mixer");
  GMainContext *main_context = g_main_context_new();
  SourceClockPipeline context = {0};
  SourceClockAudioFormat format = {
      SOURCE_CLOCK_CODEC_AAC_LC_ADTS, 48000, 1, 1024, NULL};
  SourceClockAudioBinStats before = {0}, after = {0};
  SourceClockAudioBin *owner_alias;
  SourceClockAudioStopResult first, second = SOURCE_CLOCK_AUDIO_STOP_PENDING;
  SourceClockHeader header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockHeader start = {0};
  const uint8_t payload[] = {0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4};
  GstElement *appsrc;
  GstPad *src;
  PipelineEOSHold hold = {0};
  gboolean pointer_retained, push_blocked, completed_and_released;
  gint64 deadline;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  g_mutex_init(&hold.mutex);
  context.dynamic_audio_enabled = TRUE;
  context.pipeline = parent;
  context.loop = g_main_loop_new(main_context, FALSE);
  source_clock_timeline_init(&context.timeline);
  context.started_monotonic_us = g_get_monotonic_time();
  g_cond_init(&hold.cond);
  gst_element_set_state(parent, GST_STATE_PLAYING);
  context.audio_bin = source_clock_audio_bin_new(parent, mixer, main_context);
  assert(context.audio_bin != NULL);
  owner_alias = context.audio_bin;
  assert(source_clock_audio_bin_configure(context.audio_bin, &format, NULL));
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  do {
    while (g_main_context_iteration(main_context, FALSE)) {}
    source_clock_audio_bin_get_stats(context.audio_bin, &before);
    if (before.ready) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  assert(before.ready);
  appsrc = find_audio_bin_appsrc(parent);
  assert(appsrc != NULL);
  src = gst_element_get_static_pad(appsrc, "src");
  assert(src != NULL);
  gst_pad_add_probe(src, GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM,
                    hold_audio_bin_eos, &hold, NULL);

  assert(g_main_context_acquire(main_context));
  if (new_session) {
    start.stream_kind = SOURCE_CLOCK_STREAM_CONTROL;
    start.codec = SOURCE_CLOCK_CONTROL_SESSION_START;
    start.sequence = 9;
    audio_new_calls = audio_free_calls = 0;
    track_audio_ownership = TRUE;
    assert(on_source_frame(&context, &start, NULL, 0) == 0);
    assert(context.audio_bin == owner_alias);
    assert(context.audio_admission_generation == 1);
    assert(on_source_frame(&context, &start, NULL, 0) == 0);
    assert(context.audio_admission_generation == 1);
    assert(audio_new_calls == 0 && audio_free_calls == 0);
    track_audio_ownership = FALSE;
    first = source_clock_audio_bin_stop(context.audio_bin);
  } else {
    first = source_clock_pipeline_release_audio_bin(
        &context.audio_bin, &context.dynamic_audio_disabled);
  }
  g_main_context_release(main_context);
  pointer_retained = first == SOURCE_CLOCK_AUDIO_STOP_PENDING &&
      context.audio_bin == owner_alias && context.dynamic_audio_disabled;
  assert(GST_STATE(parent) == GST_STATE_PLAYING);
  if (context.audio_bin != NULL) {
    source_clock_audio_bin_get_stats(context.audio_bin, &before);
    assert(push_source_buffer(&context, &header, payload, sizeof(payload), GST_SECOND,
                              context.audio_admission_generation));
    source_clock_audio_bin_get_stats(context.audio_bin, &after);
    push_blocked = before.configure_operations == after.configure_operations &&
        before.pending_frames == after.pending_frames &&
        before.rejected_frames == after.rejected_frames;
  } else {
    push_blocked = FALSE;
  }

  g_mutex_lock(&hold.mutex);
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  while (!hold.entered && g_cond_wait_until(&hold.cond, &hold.mutex, deadline)) {}
  hold.release = TRUE;
  g_cond_broadcast(&hold.cond);
  g_mutex_unlock(&hold.mutex);
  if (context.audio_bin != NULL) {
    second = source_clock_pipeline_release_audio_bin(
        &context.audio_bin, &context.dynamic_audio_disabled);
    completed_and_released = second == SOURCE_CLOCK_AUDIO_STOP_COMPLETE &&
        context.audio_bin == NULL;
  } else {
    /* RED cleanup for the old caller behavior: its FALSE free did not consume
     * owner_alias even though it discarded the production field. */
    second = source_clock_audio_bin_stop(owner_alias);
    completed_and_released = source_clock_audio_bin_free(owner_alias);
  }
  gst_object_unref(src);
  gst_object_unref(appsrc);
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_object_unref(mixer);
  gst_object_unref(parent);
  g_main_loop_unref(context.loop);
  g_main_context_unref(main_context);
  g_mutex_clear(&context.mutex);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&hold.mutex);
  g_cond_clear(&hold.cond);
  assert(hold.entered);
  assert(pointer_retained);
  assert(push_blocked);
  assert(completed_and_released);
  assert_audio_not_fatal(&context);
}

static SourceClockPipeline *interleaved_pipeline;
static void start_session_after_mapping(void) {
  SourceClockHeader start = {0};
  /* Proves the real callback mapped audio before yielding its lock. */
  assert(interleaved_pipeline->timeline.has_last_audio_mapped);
  start.stream_kind = SOURCE_CLOCK_STREAM_CONTROL;
  start.codec = SOURCE_CLOCK_CONTROL_SESSION_START;
  start.sequence = 73;
  assert(on_source_frame(interleaved_pipeline, &start, NULL, 0) == 0);
}

/* A mapped frame from the old session must not configure or enter the new
 * fixed decoder helper. Pending old-session audio is cleared, while a frame
 * carrying the new admission generation is accepted. The publisher-owned PCM
 * appsrc remains the same; only helper ownership is replaced, and duplicate
 * SESSION_START is inert. */
static void test_fixed_pcm_session_replaces_helper_and_rejects_old_mapping(void) {
  const uint8_t payload[] = {0xff, 0xf1, 0x50, 0x40, 0x01, 0x1f, 0xfc, 0x00};
  SourceClockPipeline context = {0};
  SourceClockHeader audio = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockHeader duplicate = {0};
  SourceClockFixedPcmAudioStats stats = {0};
  GstAppSrc *source;
  GError *error = NULL;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  source = make_fixed_pcm_source();
  context.fixed_pcm_source = source;
  context.fixed_pcm_audio_enabled = TRUE;
  context.fixed_pcm_audio = source_clock_fixed_pcm_audio_new(source, NULL, NULL, &error);
  assert(context.fixed_pcm_audio != NULL && error == NULL);
  source_clock_timeline_init(&context.timeline);
  audio.sample_rate = 44100;
  context.started_monotonic_us = g_get_monotonic_time();
  source_clock_timeline_offer_first(&context.timeline, SOURCE_CLOCK_STREAM_AUDIO,
                                    100 * GST_SECOND, 0);
  source_clock_timeline_offer_first(&context.timeline, SOURCE_CLOCK_STREAM_VIDEO,
                                    100 * GST_SECOND, 0);
  assert(source_clock_timeline_ready(&context.timeline, 0));
  audio.remote_ntp_ns = 100 * GST_SECOND;
  assert(save_pending_frame(&context.pending_audio, &audio,
                            payload, sizeof(payload), 0));
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  track_fixed_audio_ownership = TRUE;
  interleaved_pipeline = &context;
  interleave_mutex = &context.mutex;
  after_mapping_unlock = start_session_after_mapping;
  assert(on_source_frame(&context, &audio, payload, sizeof(payload)) == 0);
  after_mapping_unlock = NULL;
  interleave_mutex = NULL;
  interleaved_pipeline = NULL;
  track_fixed_audio_ownership = FALSE;
  assert(context.audio_admission_generation == 1);
  assert(context.fixed_pcm_audio != NULL);
  assert(!context.fixed_pcm_audio_disabled);
  assert(!context.pending_audio.present);
  assert(fixed_audio_stop_calls == 1 && fixed_audio_free_calls == 1 &&
         fixed_audio_new_calls == 1);
  source_clock_fixed_pcm_audio_get_stats(context.fixed_pcm_audio, &stats);
  assert(!stats.configured && !stats.decoder_created && stats.accepted_frames == 0 &&
         stats.rejected_frames == 0);
  duplicate.stream_kind = SOURCE_CLOCK_STREAM_CONTROL;
  duplicate.codec = SOURCE_CLOCK_CONTROL_SESSION_START;
  duplicate.sequence = 73;
  fixed_audio_new_calls = fixed_audio_free_calls = 0;
  track_fixed_audio_ownership = TRUE;
  assert(on_source_frame(&context, &duplicate, NULL, 0) == 0);
  track_fixed_audio_ownership = FALSE;
  assert(fixed_audio_new_calls == 0 && fixed_audio_free_calls == 0 &&
         context.audio_admission_generation == 1 && !context.fixed_pcm_audio_disabled);
  assert(push_source_buffer(&context, &audio, payload, sizeof(payload),
                            GST_SECOND, context.audio_admission_generation));
  source_clock_fixed_pcm_audio_get_stats(context.fixed_pcm_audio, &stats);
  assert(stats.configured && stats.accepted_frames == 1 && stats.rejected_frames == 0);
  assert(source_clock_fixed_pcm_audio_stop(context.fixed_pcm_audio) ==
         SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  assert(source_clock_fixed_pcm_audio_free(context.fixed_pcm_audio));
  context.fixed_pcm_audio = NULL;
  gst_object_unref(source);
  clear_pending_frame(&context.pending_audio);
  clear_pending_frame(&context.pending_video);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.mutex);
}

/* If helper shutdown is incomplete, production must retain the exact owner and
 * silence the real-audio route. Free/recreate would race callbacks still owned
 * by the old generation. */
static void test_fixed_pcm_session_stop_timeout_retains_owner(void) {
  SourceClockPipeline context = {0};
  SourceClockHeader start = {0};
  SourceClockFixedPcmAudio *owner;
  GstAppSrc *source;
  GError *error = NULL;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  source = make_fixed_pcm_source();
  context.fixed_pcm_source = source;
  context.fixed_pcm_audio_enabled = TRUE;
  context.fixed_pcm_audio = source_clock_fixed_pcm_audio_new(source, NULL, NULL, &error);
  assert(context.fixed_pcm_audio != NULL && error == NULL);
  owner = context.fixed_pcm_audio;
  source_clock_timeline_init(&context.timeline);
  context.started_monotonic_us = g_get_monotonic_time();
  start.stream_kind = SOURCE_CLOCK_STREAM_CONTROL;
  start.codec = SOURCE_CLOCK_CONTROL_SESSION_START;
  start.sequence = 91;
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  fixed_audio_stop_override = SOURCE_CLOCK_FIXED_PCM_STOP_TIMEOUT;
  track_fixed_audio_ownership = TRUE;
  assert(on_source_frame(&context, &start, NULL, 0) == 0);
  track_fixed_audio_ownership = FALSE;
  fixed_audio_stop_override = -1;
  assert(context.audio_admission_generation == 1);
  assert(context.fixed_pcm_audio == owner);
  assert(context.fixed_pcm_audio_disabled);
  assert(fixed_audio_stop_calls == 1 && fixed_audio_free_calls == 0 &&
         fixed_audio_new_calls == 0);
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  track_fixed_audio_ownership = TRUE;
  assert(on_source_frame(&context, &start, NULL, 0) == 0);
  track_fixed_audio_ownership = FALSE;
  assert(context.fixed_pcm_audio == owner && context.audio_admission_generation == 1);
  assert(fixed_audio_stop_calls == 0 && fixed_audio_free_calls == 0 &&
         fixed_audio_new_calls == 0);
  assert(source_clock_fixed_pcm_audio_stop(context.fixed_pcm_audio) ==
         SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  assert(source_clock_fixed_pcm_audio_free(context.fixed_pcm_audio));
  context.fixed_pcm_audio = NULL;
  gst_object_unref(source);
  clear_pending_frame(&context.pending_audio);
  clear_pending_frame(&context.pending_video);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.mutex);
}

typedef struct {
  const char *name;
  SourceClockFixedPcmAudioStopResult stop_result;
  gint free_result;
} FixedPcmRetirementFailure;

/* Every failed retirement must quarantine the exact old owner. The format
 * change is detected once, but no replacement configure or push may happen
 * after stop/free ownership is uncertain. */
static void test_fixed_pcm_format_change_retains_owner_on_failure(
    const FixedPcmRetirementFailure *failure) {
  const uint8_t payload[] = {0xff, 0xf1, 0x50, 0x40, 1, 2, 3, 4};
  SourceClockPipeline context = {0};
  SourceClockAudioFormat initial = {
      SOURCE_CLOCK_CODEC_AAC_LC_ADTS, 48000, 1, 1024, NULL};
  SourceClockHeader changed = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockHeader pending = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockFixedPcmAudio *owner;
  GstAppSrc *source;
  SourceClockTimeline timeline_before;
  GError *error = NULL;

  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  source = make_fixed_pcm_source();
  context.fixed_pcm_source = source;
  context.fixed_pcm_audio_enabled = TRUE;
  context.fixed_pcm_audio = source_clock_fixed_pcm_audio_new(
      source, NULL, NULL, &error);
  assert(context.fixed_pcm_audio != NULL && error == NULL);
  assert(source_clock_fixed_pcm_audio_configure(
      context.fixed_pcm_audio, &initial, &error));
  owner = context.fixed_pcm_audio;
  changed.sample_rate = 44100;
  source_clock_timeline_init(&context.timeline);
  source_clock_timeline_offer_first(&context.timeline,
                                    SOURCE_CLOCK_STREAM_AUDIO,
                                    100 * GST_SECOND, 0);
  source_clock_timeline_offer_first(&context.timeline,
                                    SOURCE_CLOCK_STREAM_VIDEO,
                                    100 * GST_SECOND, 0);
  timeline_before = context.timeline;
  pending.sample_rate = 48000;
  assert(save_pending_frame(&context.pending_audio, &pending,
                            payload, sizeof(payload), 0));

  fixed_audio_new_calls = fixed_audio_configure_calls = 0;
  fixed_audio_free_calls = fixed_audio_free_attempts = 0;
  fixed_audio_stop_calls = fixed_audio_push_calls = 0;
  fixed_audio_stop_override = failure->stop_result;
  fixed_audio_free_override = failure->free_result;
  fixed_audio_push_override = GST_FLOW_OK;
  track_fixed_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed, payload, sizeof(payload),
                            0, context.audio_admission_generation));
  track_fixed_audio_ownership = FALSE;
  fixed_audio_stop_override = -1;
  fixed_audio_free_override = -1;
  fixed_audio_push_override = -1;

  assert(context.audio_admission_generation == 1);
  assert(context.fixed_pcm_audio == owner);
  assert(context.fixed_pcm_source == source);
  assert(context.fixed_pcm_audio_disabled);
  assert(!context.pending_audio.present);
  assert(memcmp(&timeline_before, &context.timeline,
                sizeof(timeline_before)) == 0);
  assert(fixed_audio_stop_calls == 1);
  assert(fixed_audio_new_calls == 0);
  assert(fixed_audio_configure_calls == 1);
  assert(fixed_audio_push_calls == 0);
  if (failure->stop_result != SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE) {
    assert(fixed_audio_free_attempts == 0);
  } else {
    assert(fixed_audio_free_attempts == 1);
    assert(fixed_audio_free_calls == 0);
  }

  /* A quarantined owner is not admitted again with either the stale or the
   * current generation. */
  fixed_audio_push_override = GST_FLOW_OK;
  track_fixed_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed, payload, sizeof(payload),
                            0, context.audio_admission_generation - 1));
  assert(push_source_buffer(&context, &changed, payload, sizeof(payload),
                            0, context.audio_admission_generation));
  track_fixed_audio_ownership = FALSE;
  fixed_audio_push_override = -1;
  assert(context.fixed_pcm_audio == owner);
  assert(fixed_audio_new_calls == 0 && fixed_audio_configure_calls == 1 &&
         fixed_audio_push_calls == 0);

  /* The injected wrapper never owned the object. Cleanup must use the real
   * stop/free only after all overrides have been removed. */
  assert(source_clock_fixed_pcm_audio_stop(context.fixed_pcm_audio) ==
         SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  assert(source_clock_fixed_pcm_audio_free(context.fixed_pcm_audio));
  context.fixed_pcm_audio = NULL;
  gst_object_unref(source);
  clear_pending_frame(&context.pending_audio);
  clear_pending_frame(&context.pending_video);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.mutex);
  printf("fixed_pcm_retirement_failure=%s: PASS\n", failure->name);
}

static void test_fixed_pcm_format_replaces_helper(guint from_rate, guint to_rate) {
  uint8_t payload[] = {0xff, 0xf1, 0x50, 0x40, 0x01, 0x1f, 0xfc, 0x00};
  SourceClockPipeline context = {0};
  SourceClockAudioFormat initial = {
    SOURCE_CLOCK_CODEC_AAC_LC_ADTS, from_rate, 1, 1024, NULL
  };
  SourceClockHeader changed = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockHeader pending = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockFixedPcmAudioStats stats = {0};
  GstAppSrc *source;
  GstClockTime duration = gst_util_uint64_scale(1024, GST_SECOND, to_rate);
  GError *error = NULL;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  source = make_fixed_pcm_source();
  context.fixed_pcm_source = source;
  context.fixed_pcm_audio_enabled = TRUE;
  context.fixed_pcm_audio = source_clock_fixed_pcm_audio_new(source, NULL, NULL, &error);
  assert(context.fixed_pcm_audio != NULL && error == NULL);
  assert(source_clock_fixed_pcm_audio_configure(context.fixed_pcm_audio, &initial, &error));
  source_clock_timeline_init(&context.timeline);
  context.started_monotonic_us = g_get_monotonic_time();
  source_clock_timeline_offer_first(&context.timeline, SOURCE_CLOCK_STREAM_AUDIO,
                                    100 * GST_SECOND, 0);
  source_clock_timeline_offer_first(&context.timeline, SOURCE_CLOCK_STREAM_VIDEO,
                                    100 * GST_SECOND, 0);
  assert(source_clock_timeline_ready(&context.timeline, 0));
  changed.sample_rate = to_rate;
  changed.remote_ntp_ns = 100 * GST_SECOND;
  pending.sample_rate = from_rate;
  assert(save_pending_frame(&context.pending_audio, &pending,
                            payload, sizeof(payload), 0));
  payload[2] = to_rate == 48000 ? 0x4c : 0x50;
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  fixed_audio_push_calls = 0;
  fixed_audio_push_override = GST_FLOW_OK;
  track_fixed_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed, payload, sizeof(payload), 0,
                            context.audio_admission_generation));
  track_fixed_audio_ownership = FALSE;
  fixed_audio_push_override = -1;
  assert(context.audio_admission_generation == 1);
  assert(context.fixed_pcm_source == source && context.fixed_pcm_audio != NULL);
  assert(!context.fixed_pcm_audio_disabled && !context.pending_audio.present);
  assert(fixed_audio_stop_calls == 1);
  assert(fixed_audio_free_calls == 1);
  assert(fixed_audio_new_calls == 1);
  assert(fixed_audio_push_calls == 1);
  assert(fixed_audio_last_pts == 0 && fixed_audio_last_duration == duration &&
         fixed_audio_last_discont);
  source_clock_fixed_pcm_audio_get_stats(context.fixed_pcm_audio, &stats);
  assert(stats.configured && !stats.decoder_created && stats.accepted_frames == 0);
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  fixed_audio_push_override = GST_FLOW_OK;
  track_fixed_audio_ownership = TRUE;
  changed.remote_ntp_ns += duration;
  assert(push_source_buffer(&context, &changed, payload, sizeof(payload), duration,
                            context.audio_admission_generation));
  track_fixed_audio_ownership = FALSE;
  fixed_audio_push_override = -1;
  assert(context.audio_admission_generation == 1);
  assert(fixed_audio_stop_calls == 0 && fixed_audio_free_calls == 0 &&
         fixed_audio_new_calls == 0 && fixed_audio_push_calls == 2);
  assert(fixed_audio_last_pts == duration && fixed_audio_last_duration == duration &&
         !fixed_audio_last_discont);
  assert(source_clock_fixed_pcm_audio_stop(context.fixed_pcm_audio) ==
         SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  assert(source_clock_fixed_pcm_audio_free(context.fixed_pcm_audio));
  context.fixed_pcm_audio = NULL;
  gst_object_unref(source);
  clear_pending_frame(&context.pending_audio);
  clear_pending_frame(&context.pending_video);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.mutex);
}

static void init_fixed_pcm_failure_test(SourceClockPipeline *context,
                                        GstAppSrc **source_out) {
  SourceClockAudioFormat initial = {
      SOURCE_CLOCK_CODEC_AAC_LC_ADTS, 48000, 1, 1024, NULL};
  GstAppSrc *source;
  GError *error = NULL;
  memset(context, 0, sizeof(*context));
  g_mutex_init(&context->mutex);
  g_mutex_init(&context->video_scheduler.mutex);
  source = make_fixed_pcm_source();
  context->fixed_pcm_source = source;
  context->fixed_pcm_audio_enabled = TRUE;
  context->fixed_pcm_audio = source_clock_fixed_pcm_audio_new(source, NULL, NULL, &error);
  assert(context->fixed_pcm_audio != NULL && error == NULL);
  assert(source_clock_fixed_pcm_audio_configure(
      context->fixed_pcm_audio, &initial, &error));
  source_clock_timeline_init(&context->timeline);
  assert(save_pending_frame(&context->pending_audio,
                            &(SourceClockHeader){
                                .stream_kind = SOURCE_CLOCK_STREAM_AUDIO,
                                .codec = SOURCE_CLOCK_CODEC_AAC_LC_ADTS,
                                .remote_ntp_ns = 100 * GST_SECOND,
                                .sample_rate = 48000,
                                .channels = 1,
                                .sample_count = 1024,
                            },
                            (const uint8_t[]){0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4},
                            8, 0));
  *source_out = source;
}

static void clear_fixed_pcm_failure_test(SourceClockPipeline *context,
                                         GstAppSrc *source) {
  fixed_audio_new_fail_once = FALSE;
  fixed_audio_configure_fail_on_call = 0;
  fixed_audio_push_fail_once = FALSE;
  fixed_audio_push_override = -1;
  fixed_audio_stop_override = -1;
  fixed_audio_free_override = -1;
  if (context->fixed_pcm_audio != NULL) {
    assert(source_clock_fixed_pcm_audio_stop(context->fixed_pcm_audio) ==
           SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
    assert(source_clock_fixed_pcm_audio_free(context->fixed_pcm_audio));
    context->fixed_pcm_audio = NULL;
  }
  gst_object_unref(source);
  clear_pending_frame(&context->pending_audio);
  clear_pending_frame(&context->pending_video);
  source_clock_video_bootstrap_clear(&context->video_bootstrap);
  g_mutex_clear(&context->video_scheduler.mutex);
  g_mutex_clear(&context->mutex);
}

static SourceClockHeader fixed_pcm_changed_header(void) {
  SourceClockHeader header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  header.sample_rate = 44100;
  return header;
}

/* After old helper retirement, a failed replacement NEW must leave no stale
 * owner pointer that cleanup or a later frame could reuse. */
static void test_fixed_pcm_replacement_new_failure(void) {
  const uint8_t payload[] = {0xff, 0xf1, 0x50, 0x40, 1, 2, 3, 4};
  SourceClockPipeline context;
  SourceClockHeader changed = fixed_pcm_changed_header();
  GstAppSrc *source;
  SourceClockTimeline timeline_before;
  GError *error = NULL;
  init_fixed_pcm_failure_test(&context, &source);
  timeline_before = context.timeline;
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  fixed_audio_configure_calls = fixed_audio_push_calls = 0;
  fixed_audio_new_fail_once = TRUE;
  fixed_audio_push_override = GST_FLOW_OK;
  track_fixed_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed, payload, sizeof(payload),
                            0, context.audio_admission_generation));
  track_fixed_audio_ownership = FALSE;
  fixed_audio_push_override = -1;
  assert(context.audio_admission_generation == 1);
  assert(context.fixed_pcm_audio == NULL);
  assert(context.fixed_pcm_audio_disabled);
  assert(context.fixed_pcm_source == source && !context.pending_audio.present);
  assert(memcmp(&timeline_before, &context.timeline, sizeof(timeline_before)) == 0);
  assert(fixed_audio_stop_calls == 1 && fixed_audio_free_calls == 1);
  assert(fixed_audio_new_calls == 1 && fixed_audio_configure_calls == 1);
  assert(fixed_audio_push_calls == 0);
  (void)error;
  clear_fixed_pcm_failure_test(&context, source);
  puts("fixed PCM replacement NEW failure ownership: PASS");
}

/* A replacement CONFIGURE failure retains the new owner in a disabled
 * quarantine; cleanup must be able to stop/free that exact owner later. */
static void test_fixed_pcm_replacement_configure_failure(void) {
  const uint8_t payload[] = {0xff, 0xf1, 0x50, 0x40, 1, 2, 3, 4};
  SourceClockPipeline context;
  SourceClockFixedPcmAudio *new_owner;
  SourceClockHeader changed = fixed_pcm_changed_header();
  SourceClockFixedPcmAudioStats stats = {0};
  GstAppSrc *source;
  init_fixed_pcm_failure_test(&context, &source);
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  fixed_audio_configure_calls = fixed_audio_push_calls = 0;
  fixed_audio_configure_fail_on_call = 2;
  fixed_audio_push_override = GST_FLOW_OK;
  track_fixed_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed, payload, sizeof(payload),
                            0, context.audio_admission_generation));
  track_fixed_audio_ownership = FALSE;
  fixed_audio_push_override = -1;
  new_owner = context.fixed_pcm_audio;
  assert(new_owner != NULL && new_owner == context.fixed_pcm_audio);
  assert(context.audio_admission_generation == 1);
  assert(context.fixed_pcm_audio_disabled);
  assert(!context.pending_audio.present && context.fixed_pcm_source == source);
  assert(fixed_audio_stop_calls == 1 && fixed_audio_free_calls == 1);
  assert(fixed_audio_new_calls == 1 && fixed_audio_configure_calls == 2);
  assert(fixed_audio_push_calls == 0);
  source_clock_fixed_pcm_audio_get_stats(new_owner, &stats);
  assert(!stats.configured && !stats.decoder_created && !stats.stopped);
  fixed_audio_configure_fail_on_call = 0;
  fixed_audio_configure_calls = fixed_audio_push_calls = 0;
  track_fixed_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed, payload, sizeof(payload),
                            0, context.audio_admission_generation));
  track_fixed_audio_ownership = FALSE;
  assert(context.fixed_pcm_audio == new_owner && context.fixed_pcm_audio_disabled);
  assert(fixed_audio_configure_calls == 0 && fixed_audio_push_calls == 0);
  clear_fixed_pcm_failure_test(&context, source);
  puts("fixed PCM replacement CONFIGURE failure ownership: PASS");
}

/* A changed-frame push error consumes only that buffer. The replacement helper
 * stays configured/enabled and admits the next same-tuple frame once. */
static void test_fixed_pcm_changed_frame_push_failure(void) {
  const uint8_t payload[] = {0xff, 0xf1, 0x50, 0x40, 1, 2, 3, 4};
  SourceClockPipeline context;
  SourceClockFixedPcmAudio *new_owner;
  SourceClockHeader changed = fixed_pcm_changed_header();
  SourceClockFixedPcmAudioStats stats = {0};
  GstBuffer *material;
  GstMapInfo map;
  GstAppSrc *source;
  init_fixed_pcm_failure_test(&context, &source);
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  fixed_audio_configure_calls = fixed_audio_push_calls = 0;
  fixed_audio_push_fail_once = TRUE;
  fixed_audio_push_override = GST_FLOW_OK;
  track_fixed_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed, payload, sizeof(payload),
                            0, context.audio_admission_generation));
  track_fixed_audio_ownership = FALSE;
  new_owner = context.fixed_pcm_audio;
  assert(new_owner != NULL && new_owner == context.fixed_pcm_audio);
  assert(context.audio_admission_generation == 1);
  assert(!context.fixed_pcm_audio_disabled);
  assert(fixed_audio_stop_calls == 1 && fixed_audio_free_calls == 1);
  assert(fixed_audio_new_calls == 1 && fixed_audio_configure_calls == 2);
  assert(fixed_audio_push_calls == 1);
  source_clock_fixed_pcm_audio_get_stats(new_owner, &stats);
  assert(stats.configured && !stats.decoder_created && stats.accepted_frames == 0);
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  fixed_audio_configure_calls = 0;
  fixed_audio_push_override = -1;
  material = make_fixed_pcm_aac_material_frame(44100);
  assert(gst_buffer_map(material, &map, GST_MAP_READ));
  track_fixed_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed, map.data, map.size,
                            gst_util_uint64_scale(1024, GST_SECOND, 44100),
                            context.audio_admission_generation));
  track_fixed_audio_ownership = FALSE;
  gst_buffer_unmap(material, &map);
  gst_buffer_unref(material);
  assert(context.fixed_pcm_audio == new_owner && !context.fixed_pcm_audio_disabled);
  assert(context.audio_admission_generation == 1);
  assert(fixed_audio_new_calls == 0 && fixed_audio_free_calls == 0 &&
         fixed_audio_stop_calls == 0 && fixed_audio_configure_calls == 1 &&
         fixed_audio_push_calls == 2);
  assert(fixed_audio_last_pts ==
         gst_util_uint64_scale(1024, GST_SECOND, 44100));
  assert(fixed_audio_last_duration ==
         gst_util_uint64_scale(1024, GST_SECOND, 44100));
  assert(!fixed_audio_last_discont);
  source_clock_fixed_pcm_audio_get_stats(new_owner, &stats);
  assert(stats.configured && stats.accepted_frames == 1 &&
         stats.rejected_frames == 0);
  clear_fixed_pcm_failure_test(&context, source);
  puts("fixed PCM changed-frame PUSH failure recovery: PASS");
}

/* Declared here because the fixed-PCM stale-frame tests sit immediately after
 * C2-A, while the existing format-interleave callback is defined later. */
static SourceClockPipeline *format_interleaved_pipeline;
static SourceClockHeader format_interleaved_header;
static const uint8_t *format_interleaved_payload;
static size_t format_interleaved_payload_bytes;
static void change_audio_format_after_mapping(void);

static void init_fixed_pcm_interleave_test(SourceClockPipeline *context,
                                           GstAppSrc **source_out,
                                           guint sample_rate) {
  GstAppSrc *source;
  GError *error = NULL;
  memset(context, 0, sizeof(*context));
  g_mutex_init(&context->mutex);
  g_mutex_init(&context->video_scheduler.mutex);
  source = make_fixed_pcm_source();
  context->fixed_pcm_source = source;
  context->fixed_pcm_audio_enabled = TRUE;
  context->fixed_pcm_audio = source_clock_fixed_pcm_audio_new(
      source, NULL, NULL, &error);
  assert(context->fixed_pcm_audio != NULL && error == NULL);
  {
    SourceClockAudioFormat format = {
        SOURCE_CLOCK_CODEC_AAC_LC_ADTS, sample_rate, 1, 1024, NULL};
    assert(source_clock_fixed_pcm_audio_configure(
        context->fixed_pcm_audio, &format, &error));
  }
  source_clock_timeline_init(&context->timeline);
  source_clock_video_au_gate_init(&context->video_au_gate);
  source_clock_video_bootstrap_init(&context->video_bootstrap);
  context->started_monotonic_us = g_get_monotonic_time();
  *source_out = source;
}

static void clear_fixed_pcm_interleave_test(SourceClockPipeline *context,
                                            GstAppSrc *source) {
  assert(source_clock_fixed_pcm_audio_stop(context->fixed_pcm_audio) ==
         SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  assert(source_clock_fixed_pcm_audio_free(context->fixed_pcm_audio));
  context->fixed_pcm_audio = NULL;
  gst_object_unref(source);
  clear_pending_frame(&context->pending_audio);
  clear_pending_frame(&context->pending_video);
  source_clock_video_bootstrap_clear(&context->video_bootstrap);
  g_mutex_clear(&context->video_scheduler.mutex);
  g_mutex_clear(&context->mutex);
}

/* A format replacement after the old audio frame has been mapped must reject
 * that old frame while admitting the replacement frame exactly once. */
static void test_fixed_pcm_rejects_old_mapped_audio(void) {
  const uint8_t old_payload[] = {0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4};
  const uint8_t changed_payload[] = {0xff, 0xf1, 0x50, 0x40, 1, 2, 3, 4};
  SourceClockPipeline context;
  SourceClockHeader old_header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockHeader changed_header = old_header;
  GstAppSrc *source;

  init_fixed_pcm_interleave_test(&context, &source, 48000);
  changed_header.sample_rate = 44100;
  changed_header.remote_ntp_ns = 100 * GST_SECOND;
  source_clock_timeline_offer_first(&context.timeline,
                                    SOURCE_CLOCK_STREAM_AUDIO,
                                    100 * GST_SECOND, 0);
  source_clock_timeline_offer_first(&context.timeline,
                                    SOURCE_CLOCK_STREAM_VIDEO,
                                    100 * GST_SECOND, 0);
  assert(source_clock_timeline_ready(&context.timeline, 0));

  format_interleaved_pipeline = &context;
  format_interleaved_header = changed_header;
  format_interleaved_payload = changed_payload;
  format_interleaved_payload_bytes = sizeof(changed_payload);
  interleave_mutex = &context.mutex;
  after_mapping_unlock = change_audio_format_after_mapping;
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  fixed_audio_push_calls = 0;
  fixed_audio_push_override = GST_FLOW_OK;
  track_fixed_audio_ownership = TRUE;
  assert(on_source_frame(&context, &old_header, old_payload,
                         sizeof(old_payload)) == 0);
  track_fixed_audio_ownership = FALSE;
  fixed_audio_push_override = -1;
  after_mapping_unlock = NULL;
  interleave_mutex = NULL;
  format_interleaved_pipeline = NULL;

  assert(context.audio_admission_generation == 1);
  assert(context.fixed_pcm_source == source);
  assert(context.fixed_pcm_audio != NULL &&
         !context.fixed_pcm_audio_disabled);
  assert(!context.pending_audio.present);
  assert(fixed_audio_stop_calls == 1 && fixed_audio_free_calls == 1 &&
         fixed_audio_new_calls == 1);
  assert(fixed_audio_push_calls == 1);
  assert(fixed_audio_last_pts == 3 * GST_SECOND);
  assert(fixed_audio_last_duration ==
         gst_util_uint64_scale(1024, GST_SECOND, 44100));
  assert(fixed_audio_last_discont);
  clear_fixed_pcm_interleave_test(&context, source);
}

/* When timeline readiness detaches an old pending audio frame into the local
 * flush_audio value, a concurrent new-tuple frame must still invalidate it by
 * generation. Only that new frame may reach the replacement helper. */
static void test_fixed_pcm_rejects_old_detached_pending_audio(void) {
  const uint8_t old_payload[] = {0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4};
  const uint8_t changed_payload[] = {0xff, 0xf1, 0x50, 0x40, 1, 2, 3, 4};
  const uint8_t video_payload[] = {
      0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
      0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2,
      0, 0, 0, 1, 0x65, 0x88, 0x84};
  SourceClockPipeline context;
  SourceClockHeader old_header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockHeader changed_header = old_header;
  SourceClockHeader video = {0};
  GstAppSrc *source;

  init_fixed_pcm_interleave_test(&context, &source, 48000);
  changed_header.sample_rate = 44100;
  changed_header.remote_ntp_ns = 100 * GST_SECOND;
  old_header.remote_ntp_ns = 100 * GST_SECOND;
  assert(on_source_frame(&context, &old_header, old_payload,
                         sizeof(old_payload)) == 0);
  assert(!context.timeline.ready && context.pending_audio.present);

  video.stream_kind = SOURCE_CLOCK_STREAM_VIDEO;
  video.codec = SOURCE_CLOCK_CODEC_H264_ANNEXB_AU;
  video.flags = SOURCE_CLOCK_FLAG_CONFIG | SOURCE_CLOCK_FLAG_KEYFRAME;
  video.remote_ntp_ns = 100 * GST_SECOND;
  format_interleaved_pipeline = &context;
  format_interleaved_header = changed_header;
  format_interleaved_payload = changed_payload;
  format_interleaved_payload_bytes = sizeof(changed_payload);
  interleave_mutex = &context.mutex;
  after_mapping_unlock = change_audio_format_after_mapping;
  fixed_audio_new_calls = fixed_audio_free_calls = fixed_audio_stop_calls = 0;
  fixed_audio_push_calls = 0;
  fixed_audio_push_override = GST_FLOW_OK;
  track_fixed_audio_ownership = TRUE;
  assert(on_source_frame(&context, &video, video_payload,
                         sizeof(video_payload)) == 0);
  track_fixed_audio_ownership = FALSE;
  fixed_audio_push_override = -1;
  after_mapping_unlock = NULL;
  interleave_mutex = NULL;
  format_interleaved_pipeline = NULL;

  assert(context.audio_admission_generation == 1);
  assert(context.fixed_pcm_source == source);
  assert(context.fixed_pcm_audio != NULL &&
         !context.fixed_pcm_audio_disabled);
  assert(!context.pending_audio.present);
  assert(fixed_audio_stop_calls == 1 && fixed_audio_free_calls == 1 &&
         fixed_audio_new_calls == 1);
  assert(fixed_audio_push_calls == 1);
  assert(fixed_audio_last_pts == 3 * GST_SECOND);
  assert(fixed_audio_last_duration ==
         gst_util_uint64_scale(1024, GST_SECOND, 44100));
  assert(fixed_audio_last_discont);
  clear_fixed_pcm_interleave_test(&context, source);
}

/* Removing the captured-generation check admits either a current audio frame
 * or audio detached from pending by the video reader into the next session. */
static void test_session_rejects_mapped_audio(gboolean flush_pending_audio) {
  const uint8_t payload[] = {0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4};
  const uint8_t video_payload[] = {
      0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
      0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2,
      0, 0, 0, 1, 0x65, 0x88, 0x84};
  SourceClockPipeline context = {0};
  SourceClockHeader audio = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockHeader video = {0};
  SourceClockAudioBinStats stats = {0};
  GstElement *mixer;
  guint64 queued = 0;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  context.dynamic_audio_enabled = TRUE;
  context.pipeline = gst_pipeline_new(NULL);
  mixer = gst_element_factory_make("audiomixer", "audio_mixer");
  assert(mixer != NULL && gst_bin_add(GST_BIN(context.pipeline), mixer));
  context.loop = g_main_loop_new(NULL, FALSE);
  context.audio_bin = source_clock_audio_bin_new(context.pipeline, mixer,
                                                g_main_loop_get_context(context.loop));
  context.video_source = GST_APP_SRC(gst_element_factory_make("appsrc", NULL));
  assert(context.audio_bin != NULL && context.video_source != NULL);
  source_clock_timeline_init(&context.timeline);
  source_clock_video_au_gate_init(&context.video_au_gate);
  source_clock_video_bootstrap_init(&context.video_bootstrap);
  context.started_monotonic_us = g_get_monotonic_time();
  source_clock_timeline_offer_first(&context.timeline, SOURCE_CLOCK_STREAM_AUDIO,
                                    100 * GST_SECOND, 0);
  source_clock_timeline_offer_first(&context.timeline, SOURCE_CLOCK_STREAM_VIDEO,
                                    100 * GST_SECOND, 0);
  assert(source_clock_timeline_ready(&context.timeline, 0));
  video.stream_kind = SOURCE_CLOCK_STREAM_VIDEO;
  video.codec = SOURCE_CLOCK_CODEC_H264_ANNEXB_AU;
  video.flags = SOURCE_CLOCK_FLAG_CONFIG | SOURCE_CLOCK_FLAG_KEYFRAME;
  video.remote_ntp_ns = 100 * GST_SECOND;
  if (flush_pending_audio)
    assert(save_pending_frame(&context.pending_audio, &audio, payload, sizeof(payload), 0));
  interleaved_pipeline = &context;
  interleave_mutex = &context.mutex;
  after_mapping_unlock = start_session_after_mapping;
  if (flush_pending_audio) {
    assert(on_source_frame(&context, &video, video_payload, sizeof(video_payload)) == 0);
  } else {
    assert(on_source_frame(&context, &audio, payload, sizeof(payload)) == 0);
    /* A stale audio generation is not a reason to discard video. */
    assert(push_source_buffer(&context, &video, video_payload, sizeof(video_payload),
                              GST_SECOND, 0));
  }
  assert(after_mapping_unlock == NULL);
  interleave_mutex = NULL;
  interleaved_pipeline = NULL;
  assert(context.audio_admission_generation == 1);
  assert(!context.dynamic_audio_disabled);
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(!stats.configured && stats.pending_frames == 0 && stats.rejected_frames == 0);
  g_object_get(context.video_source, "current-level-buffers", &queued, NULL);
  assert(queued == 1);
  assert_audio_not_fatal(&context);
  assert(source_clock_pipeline_release_audio_bin(
      &context.audio_bin, &context.dynamic_audio_disabled) == SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
  gst_object_unref(context.video_source);
  gst_object_unref(context.pipeline);
  g_main_loop_unref(context.loop);
  clear_pending_frame(&context.pending_audio);
  clear_pending_frame(&context.pending_video);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.mutex);
}

static void test_session_without_mixer_retains_silence_route(void) {
  SourceClockPipeline context = {0};
  SourceClockHeader start = {0};
  SourceClockHeader audio = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  const uint8_t payload[] = {1, 2, 3, 4};
  guint64 queued = 0;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  context.dynamic_audio_enabled = TRUE;
  context.pipeline = gst_pipeline_new(NULL);
  context.loop = g_main_loop_new(NULL, FALSE);
  context.audio_source = GST_APP_SRC(gst_element_factory_make("appsrc", NULL));
  assert(context.audio_source != NULL);
  context.audio_bin = source_clock_audio_bin_new(NULL, NULL, NULL);
  assert(context.audio_bin != NULL);
  start.stream_kind = SOURCE_CLOCK_STREAM_CONTROL;
  start.codec = SOURCE_CLOCK_CONTROL_SESSION_START;
  start.sequence = 1;
  assert(on_source_frame(&context, &start, NULL, 0) == 0);
  assert(context.audio_bin == NULL && context.dynamic_audio_disabled);
  assert(context.audio_admission_generation == 1);
  /* No fall-through into a legacy input when replacement cannot be made. */
  assert(push_source_buffer(&context, &audio, payload, sizeof(payload), GST_SECOND, 1));
  g_object_get(context.audio_source, "current-level-buffers", &queued, NULL);
  assert(queued == 0);
  assert(on_source_frame(&context, &start, NULL, 0) == 0);
  assert(context.audio_bin == NULL && context.dynamic_audio_disabled);
  assert(context.audio_admission_generation == 1);
  assert_audio_not_fatal(&context);
  gst_object_unref(context.audio_source);
  gst_object_unref(context.pipeline);
  g_main_loop_unref(context.loop);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.mutex);
}

/* A new control session must retire the configured branch, not reuse its
 * terminal/configured state. No publisher or network element is constructed. */
static void test_new_session_replaces_audio_bin(void) {
  SourceClockPipeline context = {0};
  SourceClockHeader start = {0};
  SourceClockHeader audio = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  const uint8_t payload[] = {0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4};
  SourceClockAudioBinStats stats = {0};
  GstElement *mixer;
  SourceClockAudioBin *new_owner;
  SourceClockTimeline timeline_before;
  guint64 admission_generation;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  source_clock_timeline_init(&context.timeline);
  context.started_monotonic_us = g_get_monotonic_time();
  context.dynamic_audio_enabled = TRUE;
  context.pipeline = gst_pipeline_new(NULL);
  mixer = gst_element_factory_make("audiomixer", "audio_mixer");
  assert(mixer != NULL && gst_bin_add(GST_BIN(context.pipeline), mixer));
  context.loop = g_main_loop_new(NULL, FALSE);
  context.audio_bin = source_clock_audio_bin_new(context.pipeline, mixer,
                                                g_main_loop_get_context(context.loop));
  assert(context.audio_bin != NULL);
  assert(push_source_buffer(&context, &audio, payload, sizeof(payload), GST_SECOND, 0));
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(stats.configured && stats.pending_frames == 1);
  new_owner = context.audio_bin;
  start.stream_kind = SOURCE_CLOCK_STREAM_CONTROL;
  start.codec = SOURCE_CLOCK_CONTROL_SESSION_START;
  start.sequence = 41;
  /* The disabled-by-default runtime route must not touch bin ownership. */
  context.dynamic_audio_enabled = FALSE;
  assert(on_source_frame(&context, &start, NULL, 0) == 0);
  assert(context.audio_bin == new_owner);
  assert(context.audio_admission_generation == 0 && !context.dynamic_audio_disabled);
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(stats.configured && stats.pending_frames == 1);
  context.dynamic_audio_enabled = TRUE;
  start.sequence = 42;
  audio_new_calls = audio_free_calls = 0;
  freed_after_complete = FALSE;
  track_audio_ownership = TRUE;
  assert(on_source_frame(&context, &start, NULL, 0) == 0);
  track_audio_ownership = FALSE;
  assert(audio_free_calls == 1 && freed_after_complete && audio_new_calls == 1);
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(!stats.configured && !stats.stopped && stats.pending_frames == 0);
  assert(!context.dynamic_audio_disabled);
  new_owner = context.audio_bin;
  admission_generation = context.audio_admission_generation;
  assert(admission_generation == 1);
  /* Both enabled and disabled duplicates preserve owner/admission generation. */
  assert(on_source_frame(&context, &start, NULL, 0) == 0);
  assert(context.audio_bin == new_owner &&
         context.audio_admission_generation == admission_generation);
  assert(!context.dynamic_audio_disabled);
  context.dynamic_audio_disabled = TRUE;
  assert(on_source_frame(&context, &start, NULL, 0) == 0);
  assert(context.audio_bin == new_owner &&
         context.audio_admission_generation == admission_generation);
  assert(context.dynamic_audio_disabled);
  context.dynamic_audio_disabled = FALSE;
  timeline_before = context.timeline;
  assert(push_source_buffer(&context, &audio, payload, sizeof(payload),
                            2 * GST_SECOND, admission_generation));
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(stats.configured && !stats.stopped && stats.pending_frames == 1);
  assert(stats.oldest_pts == 2 * GST_SECOND);
  assert(memcmp(&timeline_before, &context.timeline, sizeof(timeline_before)) == 0);
  assert(source_clock_pipeline_release_audio_bin(
      &context.audio_bin, &context.dynamic_audio_disabled) == SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
  gst_object_unref(context.pipeline);
  g_main_loop_unref(context.loop);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.mutex);
}

static void init_format_change_test(SourceClockPipeline *context) {
  GstElement *mixer;
  memset(context, 0, sizeof(*context));
  g_mutex_init(&context->mutex);
  g_mutex_init(&context->video_scheduler.mutex);
  source_clock_timeline_init(&context->timeline);
  source_clock_video_au_gate_init(&context->video_au_gate);
  source_clock_video_bootstrap_init(&context->video_bootstrap);
  context->started_monotonic_us = g_get_monotonic_time();
  context->dynamic_audio_enabled = TRUE;
  context->pipeline = gst_pipeline_new(NULL);
  mixer = gst_element_factory_make("audiomixer", "audio_mixer");
  assert(mixer != NULL && gst_bin_add(GST_BIN(context->pipeline), mixer));
  context->loop = g_main_loop_new(NULL, FALSE);
  context->audio_bin = source_clock_audio_bin_new(
      context->pipeline, mixer, g_main_loop_get_context(context->loop));
  assert(context->audio_bin != NULL);
}

static void clear_format_change_test(SourceClockPipeline *context) {
  assert(source_clock_pipeline_release_audio_bin(
      &context->audio_bin, &context->dynamic_audio_disabled) ==
      SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
  gst_object_unref(context->pipeline);
  g_main_loop_unref(context->loop);
  clear_pending_frame(&context->pending_audio);
  clear_pending_frame(&context->pending_video);
  source_clock_video_bootstrap_clear(&context->video_bootstrap);
  g_mutex_clear(&context->video_scheduler.mutex);
  g_mutex_clear(&context->mutex);
}

/* Catches treating FORMAT_CHANGE as terminal audio failure. Real bin ownership
 * instrumentation proves old COMPLETE/free and exactly one replacement. */
static void test_same_session_audio_format_change_replaces_and_admits_frame(void) {
  const uint8_t payload[] = {0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4};
  SourceClockPipeline context;
  SourceClockHeader old_header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockHeader changed_header = old_header;
  SourceClockAudioBinStats stats = {0};
  SourceClockAudioFormat format = {0};
  SourceClockTimeline timeline_before;
  SourceClockVideoAUGate gate_before;
  SourceClockVideoBootstrap bootstrap_before;
  guint64 admission_generation;
  init_format_change_test(&context);
  context.video_codec = SOURCE_CLOCK_CODEC_H264_ANNEXB_AU;
  changed_header.sample_rate = 44100;
  assert(push_source_buffer(&context, &old_header, payload, sizeof(payload),
                            GST_SECOND, 0));
  timeline_before = context.timeline;
  gate_before = context.video_au_gate;
  bootstrap_before = context.video_bootstrap;
  audio_new_calls = audio_free_calls = 0;
  freed_after_complete = FALSE;
  track_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed_header, payload, sizeof(payload),
                            2 * GST_SECOND, 0));
  track_audio_ownership = FALSE;
  assert(audio_free_calls == 1 && freed_after_complete && audio_new_calls == 1);
  assert(!context.dynamic_audio_disabled);
  admission_generation = context.audio_admission_generation;
  assert(admission_generation == 1);
  assert(source_clock_audio_bin_get_format(context.audio_bin, &format));
  assert(format.codec == SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  assert(format.sample_rate == 44100 && format.channels == 1);
  assert(format.samples_per_frame == 1024 && format.codec_data == NULL);
  source_clock_audio_format_clear(&format);
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(stats.configured && !stats.stopped && stats.pending_frames == 1);
  assert(stats.oldest_pts == 2 * GST_SECOND);
  assert(stats.pending_duration == 23219954);
  assert(memcmp(&timeline_before, &context.timeline, sizeof(timeline_before)) == 0);
  assert(memcmp(&gate_before, &context.video_au_gate, sizeof(gate_before)) == 0);
  assert(memcmp(&bootstrap_before, &context.video_bootstrap,
                sizeof(bootstrap_before)) == 0);
  assert(context.video_codec == SOURCE_CLOCK_CODEC_H264_ANNEXB_AU);

  audio_new_calls = audio_free_calls = 0;
  track_audio_ownership = TRUE;
  assert(push_source_buffer(&context, &changed_header, payload, sizeof(payload),
                            2 * GST_SECOND + 23219954, admission_generation));
  track_audio_ownership = FALSE;
  assert(audio_new_calls == 0 && audio_free_calls == 0);
  assert(context.audio_admission_generation == admission_generation);
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(stats.pending_frames == 2 && stats.pending_duration == 46439908);
  assert_audio_not_fatal(&context);
  clear_format_change_test(&context);
}

static SourceClockPipeline *format_interleaved_pipeline;
static SourceClockHeader format_interleaved_header;
static const uint8_t *format_interleaved_payload;
static size_t format_interleaved_payload_bytes;
static SourceClockTimeline format_timeline_before;
static void change_audio_format_after_mapping(void) {
  format_timeline_before = format_interleaved_pipeline->timeline;
  assert(push_source_buffer(format_interleaved_pipeline,
                            &format_interleaved_header,
                            format_interleaved_payload,
                            format_interleaved_payload_bytes,
                            3 * GST_SECOND,
                            format_interleaved_pipeline->audio_admission_generation));
  assert(memcmp(&format_timeline_before, &format_interleaved_pipeline->timeline,
                sizeof(format_timeline_before)) == 0);
}

/* A format replacement between mapping and push must admit only the frame that
 * announced the new tuple; detached old-format audio remains stale. */
static void test_format_change_rejects_old_mapped_audio(gboolean flush_pending_audio) {
  const uint8_t payload[] = {0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4};
  const uint8_t video_payload[] = {
      0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
      0, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2,
      0, 0, 0, 1, 0x65, 0x88, 0x84};
  SourceClockPipeline context;
  SourceClockHeader old_header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockHeader changed_header = old_header;
  SourceClockHeader video = {0};
  SourceClockAudioBinStats stats = {0};
  init_format_change_test(&context);
  changed_header.sample_rate = 44100;
  assert(push_source_buffer(&context, &old_header, payload, sizeof(payload),
                            GST_SECOND, 0));
  source_clock_timeline_offer_first(&context.timeline, SOURCE_CLOCK_STREAM_AUDIO,
                                    100 * GST_SECOND, 0);
  source_clock_timeline_offer_first(&context.timeline, SOURCE_CLOCK_STREAM_VIDEO,
                                    100 * GST_SECOND, 0);
  assert(source_clock_timeline_ready(&context.timeline, 0));
  old_header.remote_ntp_ns += 20 * GST_MSECOND;
  video.stream_kind = SOURCE_CLOCK_STREAM_VIDEO;
  video.codec = SOURCE_CLOCK_CODEC_H264_ANNEXB_AU;
  video.flags = SOURCE_CLOCK_FLAG_CONFIG | SOURCE_CLOCK_FLAG_KEYFRAME;
  video.remote_ntp_ns = 100 * GST_SECOND;
  if (flush_pending_audio)
    assert(save_pending_frame(&context.pending_audio, &old_header,
                              payload, sizeof(payload), 0));
  format_interleaved_pipeline = &context;
  format_interleaved_header = changed_header;
  format_interleaved_payload = payload;
  format_interleaved_payload_bytes = sizeof(payload);
  interleave_mutex = &context.mutex;
  after_mapping_unlock = change_audio_format_after_mapping;
  if (flush_pending_audio)
    assert(on_source_frame(&context, &video, video_payload, sizeof(video_payload)) == 0);
  else
    assert(on_source_frame(&context, &old_header, payload, sizeof(payload)) == 0);
  assert(after_mapping_unlock == NULL);
  interleave_mutex = NULL;
  format_interleaved_pipeline = NULL;
  source_clock_audio_bin_get_stats(context.audio_bin, &stats);
  assert(context.audio_admission_generation == 1 && !context.dynamic_audio_disabled);
  assert(stats.configured && stats.pending_frames == 1);
  assert(stats.oldest_pts == 3 * GST_SECOND);
  assert(stats.pending_duration == 23219954);
  assert_audio_not_fatal(&context);
  clear_format_change_test(&context);
}

static void test_format_change_retains_pending_owner(void) {
  GstElement *parent = gst_parse_launch(
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=audio_mixer ignore-inactive-pads=true ! "
      "fakesink sync=false async=false", NULL);
  GstElement *mixer = gst_bin_get_by_name(GST_BIN(parent), "audio_mixer");
  GMainContext *main_context = g_main_context_new();
  SourceClockPipeline context = {0};
  SourceClockAudioFormat old_format = {
      SOURCE_CLOCK_CODEC_AAC_LC_ADTS, 48000, 1, 1024, NULL};
  SourceClockHeader changed_header = audio_header(SOURCE_CLOCK_CODEC_AAC_LC_ADTS);
  SourceClockAudioBin *owner_alias;
  SourceClockAudioBinStats stats = {0};
  const uint8_t payload[] = {0xff, 0xf1, 0x4c, 0x40, 1, 2, 3, 4};
  GstElement *appsrc;
  GstPad *src;
  PipelineEOSHold hold = {0};
  gint64 deadline;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  g_mutex_init(&hold.mutex);
  g_cond_init(&hold.cond);
  context.dynamic_audio_enabled = TRUE;
  context.pipeline = parent;
  context.loop = g_main_loop_new(main_context, FALSE);
  context.started_monotonic_us = g_get_monotonic_time();
  gst_element_set_state(parent, GST_STATE_PLAYING);
  context.audio_bin = source_clock_audio_bin_new(parent, mixer, main_context);
  assert(context.audio_bin != NULL);
  owner_alias = context.audio_bin;
  assert(source_clock_audio_bin_configure(context.audio_bin, &old_format, NULL));
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  do {
    while (g_main_context_iteration(main_context, FALSE)) {}
    source_clock_audio_bin_get_stats(context.audio_bin, &stats);
    if (stats.ready) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  assert(stats.ready);
  appsrc = find_audio_bin_appsrc(parent);
  assert(appsrc != NULL);
  src = gst_element_get_static_pad(appsrc, "src");
  assert(src != NULL);
  gst_pad_add_probe(src, GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM,
                    hold_audio_bin_eos, &hold, NULL);
  changed_header.sample_rate = 44100;
  audio_new_calls = audio_free_calls = 0;
  track_audio_ownership = TRUE;
  assert(g_main_context_acquire(main_context));
  assert(push_source_buffer(&context, &changed_header, payload, sizeof(payload),
                            2 * GST_SECOND, 0));
  g_main_context_release(main_context);
  track_audio_ownership = FALSE;
  assert(context.audio_bin == owner_alias && context.dynamic_audio_disabled);
  assert(context.audio_admission_generation == 1);
  assert(audio_new_calls == 0 && audio_free_calls == 0);
  assert(GST_STATE(parent) == GST_STATE_PLAYING);
  assert_audio_not_fatal(&context);

  g_mutex_lock(&hold.mutex);
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  while (!hold.entered && g_cond_wait_until(&hold.cond, &hold.mutex, deadline)) {}
  hold.release = TRUE;
  g_cond_broadcast(&hold.cond);
  g_mutex_unlock(&hold.mutex);
  assert(hold.entered);
  assert(source_clock_pipeline_release_audio_bin(
      &context.audio_bin, &context.dynamic_audio_disabled) ==
      SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
  assert(context.audio_bin == NULL);
  gst_object_unref(src);
  gst_object_unref(appsrc);
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_object_unref(mixer);
  gst_object_unref(parent);
  g_main_loop_unref(context.loop);
  g_main_context_unref(main_context);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.mutex);
  g_mutex_clear(&hold.mutex);
  g_cond_clear(&hold.cond);
}

#include "test_video_watermark_pipeline.inc"
#include "test_candidate_pipeline.inc"
#include "test_candidate_interleavings.inc"

int main(int argc, char **argv) {
  if (argc == 2 && strcmp(argv[1], "--candidate-interleavings") == 0) {
    gst_init(NULL, NULL);
    for (unsigned stage = 1; stage <= 3; ++stage) {
      test_candidate_write_interleave((SourceClockVideoProofEvent)stage, FALSE);
      test_candidate_write_interleave((SourceClockVideoProofEvent)stage, TRUE);
    }
    puts("candidate event-write failure/stop at all three stages: PASS");
    return 0;
  }
  if (argc == 2 && strcmp(argv[1], "--candidate-audio") == 0) {
    gst_init(NULL, NULL);
    test_candidate_real_late_audio();
    return 0;
  }
  if (argc == 2 && strcmp(argv[1], "--candidate-codec") == 0) {
    gst_init(NULL, NULL);
    test_candidate_real_codec_single_idr();
    return 0;
  }
  if (argc == 2 && strcmp(argv[1], "--candidate") == 0) {
    gst_init(NULL, NULL);
    test_candidate_no_audio_one_idr_proof();
    test_candidate_terminal_and_audio_independence();
    test_candidate_idr_must_be_in_payload();
    test_candidate_respects_presentation_deadline();
    test_candidate_split_config_same_sequence_and_late_audio_start();
    puts("candidate pipeline tests: PASS");
    return 0;
  }
  if (argc != 1 && !(argc == 2 &&
      (strcmp(argv[1], "--watermark") == 0 ||
       strcmp(argv[1], "--format-change-late-idr") == 0))) {
    fprintf(stderr, "unknown test scenario\n");
    return 2;
  }
  gst_init(NULL, NULL);
  if (argc == 2 && strcmp(argv[1], "--watermark") == 0) {
    test_watermark_pipeline();
    test_watermark_final_failure_and_no_media();
    test_watermark_authenticated_reader_boundary();
    puts("watermark pipeline tests: PASS");
    return 0;
  }
  test_watermark_pipeline();
  test_watermark_final_failure_and_no_media();
  test_watermark_authenticated_reader_boundary();
  if (argc == 2 && strcmp(argv[1], "--format-change-late-idr") == 0) {
    test_config_only_format_change_retains_discont(TRUE);
    return 0;
  }
  test_config_only_format_change_retains_discont(FALSE);
  test_config_only_format_change_retains_discont(TRUE);
  test_codec_caps_follow_source_header();
  test_accept_start_requires_ready_and_scheduler();
  test_accept_thread_start_transfers_context_only_on_success();
  test_listener_ownership_is_taken_only_once();
  test_stop_waits_for_accept_exit_before_listener_close();
  test_separate_configuration_is_prepended_to_first_keyframe();
  test_individual_sps_and_pps_are_both_prepended_to_first_keyframe();
  test_configuration_is_not_reused_across_codec_change();
  test_format_change_header_survives_callback_and_does_not_leak_flags();
  test_late_video_drop_waits_for_recovery_idr();
  test_corrupt_decoded_video_is_not_scheduled();
  test_black_scheduler_output_is_not_real_video();
  test_dynamic_audio_header_pts_and_duration();
  test_dynamic_aac_eld_header_pts_and_duration();
  test_dynamic_audio_failures_are_terminal_audio_only();
  test_legacy_audio_without_bin_keeps_appsrc_path();
  test_audio_bin_caller_retains_pending_owner(FALSE);
  test_audio_bin_caller_retains_pending_owner(TRUE);
  test_fixed_pcm_session_replaces_helper_and_rejects_old_mapping();
  test_fixed_pcm_session_stop_timeout_retains_owner();
  {
    const FixedPcmRetirementFailure failures[] = {
        {"stop-timeout", SOURCE_CLOCK_FIXED_PCM_STOP_TIMEOUT, -1},
        {"stop-failed-retained", SOURCE_CLOCK_FIXED_PCM_STOP_FAILED_RETAINED, -1},
        {"free-false", SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE, FALSE},
    };
    for (guint i = 0; i < G_N_ELEMENTS(failures); ++i)
      test_fixed_pcm_format_change_retains_owner_on_failure(&failures[i]);
  }
  test_fixed_pcm_format_replaces_helper(44100, 48000);
  test_fixed_pcm_format_replaces_helper(48000, 44100);
  test_fixed_pcm_replacement_new_failure();
  test_fixed_pcm_replacement_configure_failure();
  test_fixed_pcm_changed_frame_push_failure();
  test_fixed_pcm_rejects_old_mapped_audio();
  test_fixed_pcm_rejects_old_detached_pending_audio();
  test_session_rejects_mapped_audio(TRUE);
  test_session_rejects_mapped_audio(FALSE);
  test_session_without_mixer_retains_silence_route();
  test_new_session_replaces_audio_bin();
  test_same_session_audio_format_change_replaces_and_admits_frame();
  test_format_change_rejects_old_mapped_audio(FALSE);
  test_format_change_rejects_old_mapped_audio(TRUE);
  test_format_change_retains_pending_owner();
  puts("video 5 + dynamic audio adapter 3 + caller lifecycle 1 + session lifecycle 5 + format lifecycle 4 + fixed stale interleave 2 groups: PASS");
  return 0;
}
