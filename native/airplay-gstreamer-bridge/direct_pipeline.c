#include "pipeline.h"
#include "rtsp_h264_contract.h"
#include "last_frame.h"

#include <gst/app/gstappsink.h>
#include <gst/app/gstappsrc.h>
#include <gst/gst.h>

#include <stdio.h>
#include <string.h>

typedef struct {
  GMainLoop *loop;
  GstElement *pipeline;
  GstElement *video_source;
  GstElement *audio_source;
  GstElement *video_sink;
  GstElement *audio_sink;
  AirPlayLastFrame last_frame;
  GstBuffer *black_frame;
  GMutex timing_mutex;
  gboolean have_video_pts;
  gboolean have_audio_pts;
  GstClockTime last_video_pts;
  GstClockTime last_audio_pts;
  GstClockTime last_real_video_time;
  GstClockTime last_real_audio_time;
  GstClockTime video_period;
  GstClockTime audio_period;
  gint64 started_monotonic_us;
  const char *stop_file;
  gboolean stop_requested;
  gboolean fatal_error;
  gboolean diagnostics;
  gboolean stage_seen[10];
} AirPlayDirectPipeline;

typedef struct {
  AirPlayDirectPipeline *ctx;
  const char *label;
  guint index;
} AirPlayStageProbe;

static GstClockTime running_time(AirPlayDirectPipeline *ctx) {
  gint64 elapsed = g_get_monotonic_time() - ctx->started_monotonic_us;
  if (elapsed <= 0) {
    return 0;
  }
  return (GstClockTime)elapsed * 1000;
}

/*
 * AirPlay can leave a large discontinuity in the RTP-derived PTS when the
 * sender rotates, backgrounds an app, or resumes a video.  Passing that PTS
 * through makes MediaMTX wait for an impossibly distant keyframe and causes
 * LL-HLS parts/segments to grow without bound.  The direct publisher already
 * owns a monotonic process clock, so use that clock as the output timeline.
 * The input PTS is deliberately not consulted here.
 */
static GstClockTime reclock_pts(gboolean *have_last, GstClockTime *last,
                                GstClockTime now, GstClockTime step) {
  GstClockTime pts = now;
  if (*have_last && pts <= *last) {
    pts = *last + step;
  }
  *have_last = TRUE;
  *last = pts;
  return pts;
}

static GstBuffer *copy_with_timing(GstBuffer *source, GstClockTime pts,
                                   GstClockTime duration) {
  GstBuffer *copy = gst_buffer_copy(source);
  if (copy == NULL) {
    return NULL;
  }
  GST_BUFFER_PTS(copy) = pts;
  GST_BUFFER_DTS(copy) = GST_CLOCK_TIME_NONE;
  GST_BUFFER_DURATION(copy) = duration;
  return copy;
}

static GstBuffer *make_black_frame(guint width, guint height) {
  gsize y_size = (gsize)width * height;
  gsize size = y_size + y_size / 2;
  GstBuffer *buffer = gst_buffer_new_allocate(NULL, size, NULL);
  if (buffer == NULL) {
    return NULL;
  }
  GstMapInfo map;
  if (!gst_buffer_map(buffer, &map, GST_MAP_WRITE)) {
    gst_buffer_unref(buffer);
    return NULL;
  }
  memset(map.data, 0, y_size);
  memset(map.data + y_size, 128, size - y_size);
  gst_buffer_unmap(buffer, &map);
  return buffer;
}

static GstBuffer *make_silence(GstClockTime duration) {
  gsize frames = (gsize)(duration * 48000 / GST_SECOND);
  if (frames == 0) {
    frames = 480;
  }
  GstBuffer *buffer = gst_buffer_new_allocate(NULL, frames * 2 * sizeof(gint16), NULL);
  if (buffer == NULL) {
    return NULL;
  }
  GstMapInfo map;
  if (!gst_buffer_map(buffer, &map, GST_MAP_WRITE)) {
    gst_buffer_unref(buffer);
    return NULL;
  }
  memset(map.data, 0, map.size);
  gst_buffer_unmap(buffer, &map);
  return buffer;
}

static gboolean set_direct_source_caps(AirPlayDirectPipeline *ctx, int width,
                                       int height, int fps) {
  gchar *video_description = g_strdup_printf(
      "video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1,pixel-aspect-ratio=1/1",
      width, height, fps > 0 ? fps : 60);
  GstCaps *video_caps = gst_caps_from_string(video_description);
  g_free(video_description);
  GstCaps *audio_caps = gst_caps_from_string(
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved");
  if (video_caps == NULL || audio_caps == NULL) {
    if (video_caps != NULL) gst_caps_unref(video_caps);
    if (audio_caps != NULL) gst_caps_unref(audio_caps);
    return FALSE;
  }
  gst_app_src_set_caps(GST_APP_SRC(ctx->video_source), video_caps);
  gst_app_src_set_caps(GST_APP_SRC(ctx->audio_source), audio_caps);
  gst_caps_unref(video_caps);
  gst_caps_unref(audio_caps);
  return TRUE;
}

static GstPadProbeReturn log_first_buffer(GstPad *pad, GstPadProbeInfo *info,
                                          gpointer user_data) {
  (void)pad;
  AirPlayStageProbe *probe = (AirPlayStageProbe *)user_data;
  if ((GST_PAD_PROBE_INFO_TYPE(info) & GST_PAD_PROBE_TYPE_BUFFER) != 0 &&
      !probe->ctx->stage_seen[probe->index]) {
    GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
    GstClockTime pts = buffer == NULL ? GST_CLOCK_TIME_NONE : GST_BUFFER_PTS(buffer);
    GstClockTime dts = buffer == NULL ? GST_CLOCK_TIME_NONE : GST_BUFFER_DTS(buffer);
    probe->ctx->stage_seen[probe->index] = TRUE;
    g_printerr("AirPlay direct GStreamer stage=%s first-buffer size=%" G_GSIZE_FORMAT
               " pts=%" GST_TIME_FORMAT " dts=%" GST_TIME_FORMAT "\n",
               probe->label, buffer == NULL ? 0 : gst_buffer_get_size(buffer),
               GST_TIME_ARGS(pts), GST_TIME_ARGS(dts));
  }
  return GST_PAD_PROBE_OK;
}

static void install_stage_probe(AirPlayDirectPipeline *ctx, const char *element_name,
                                const char *label, guint index) {
  if (!ctx->diagnostics) {
    return;
  }
  GstElement *element = gst_bin_get_by_name(GST_BIN(ctx->pipeline), element_name);
  if (element == NULL) {
    g_printerr("AirPlay direct GStreamer stage=%s element-not-found name=%s\n",
               label, element_name);
    return;
  }
  GstPad *src = gst_element_get_static_pad(element, "src");
  gst_object_unref(element);
  if (src == NULL) {
    g_printerr("AirPlay direct GStreamer stage=%s src-pad-not-found name=%s\n",
               label, element_name);
    return;
  }
  AirPlayStageProbe *probe = g_new0(AirPlayStageProbe, 1);
  probe->ctx = ctx;
  probe->label = label;
  probe->index = index;
  gst_pad_add_probe(src, GST_PAD_PROBE_TYPE_BUFFER, log_first_buffer, probe, g_free);
  gst_object_unref(src);
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

static gboolean configure_audio_rtsp_payloader(GstElement *pipeline,
                                                GError **error) {
  GstElement *rtsp_sink = gst_bin_get_by_name(GST_BIN(pipeline), "rtsp_sink");
  if (rtsp_sink == NULL) {
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_NEGOTIATION,
                "rtsp sink was not found");
    return FALSE;
  }
  GstPad *audio_pad = find_sink_pad(rtsp_sink, "sink_1");
  if (audio_pad == NULL) {
    gst_object_unref(rtsp_sink);
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_NEGOTIATION,
                "rtsp audio sink pad was not found");
    return FALSE;
  }
  GstElement *payloader = gst_element_factory_make("rtpmp4gpay", "audio_rtsp_payloader");
  if (payloader == NULL) {
    gst_object_unref(audio_pad);
    gst_object_unref(rtsp_sink);
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_MISSING_PLUGIN,
                "rtpmp4gpay is unavailable");
    return FALSE;
  }
  g_object_set(payloader, "pt", 96, NULL);
  g_object_set(audio_pad, "payloader", payloader, NULL);
  gst_object_unref(audio_pad);
  gst_object_unref(rtsp_sink);
  return TRUE;
}

static GstElement *create_direct_pipeline(int video_port, int audio_port,
                                          const char *publish_url,
                                          const char *recording,
                                          int width, int height, int fps,
                                          int bitrate_kbps, int maxrate_kbps,
                                          int buffer_size_kbps, int audio_bitrate_bps,
                                          int gop,
                                          gboolean use_nvenc, GError **error) {
  const char *video_reorder_element =
      "rtpjitterbuffer name=video_jitter mode=none latency=200 drop-on-latency=false do-lost=false";
  const char *audio_reorder_element =
      "rtpjitterbuffer name=audio_jitter mode=none latency=200 drop-on-latency=false do-lost=false";
  const int encoder_bitrate_kbps = use_nvenc ? bitrate_kbps : maxrate_kbps;
  gchar *escaped_url = g_strescape(publish_url == NULL ? "" : publish_url, NULL);
  gchar *escaped_recording = g_strescape(recording == NULL ? "" : recording, NULL);
  gchar *decoder_description = g_strdup(
      use_nvenc ? "nvh264dec name=video_decoder" : "avdec_h264 name=video_decoder");
  gchar *encoder_description = g_strdup_printf(
      use_nvenc
          ? "videoconvert ! video/x-raw,format=NV12 ! nvh264enc name=video_encoder bitrate=%d gop-size=%d bframes=0 zerolatency=true repeat-sequence-header=true preset=low-latency-hq"
          : "x264enc name=video_encoder bitrate=%d pass=qual quantizer=23 key-int-max=%d bframes=0 tune=zerolatency speed-preset=superfast vbv-buf-capacity=1000 byte-stream=true option-string=\"vbv-maxrate=%d:vbv-bufsize=%d:aud=1:repeat-headers=1:" IMAGEPAD_RTSP_X264_SINGLE_SLICE_OPTIONS "\"",
      encoder_bitrate_kbps, gop, maxrate_kbps, buffer_size_kbps);
  gchar *description = g_strdup_printf(
      "rtspclientsink name=rtsp_sink location=\"%s\" protocols=tcp latency=2000 "
      "mp4mux name=record_mux faststart=true ! filesink location=\"%s\" "
      "appsrc name=video_source is-live=true format=time do-timestamp=false block=false ! "
      "queue name=video_encode_queue max-size-buffers=30 max-size-bytes=0 max-size-time=500000000 leaky=downstream ! "
      "%s ! tee name=video_tee "
      // The RTSP branches are paced by GStreamer's pipeline clock. Keeping
      // clocksync before the payloader prevents queued buffers from becoming
      // visible catch-up playback after a sender pause.
      "video_tee. ! queue ! clocksync name=video_clock sync=true sync-to-first=true ! h264parse config-interval=1 ! rtsp_sink.sink_0 "
      "video_tee. ! queue ! h264parse config-interval=1 ! video/x-h264,stream-format=(string)avc,alignment=(string)au ! record_mux.video_0 "
      "appsrc name=audio_source is-live=true format=time do-timestamp=false block=false ! "
      "queue name=audio_encode_queue max-size-buffers=32 max-size-bytes=0 max-size-time=500000000 leaky=downstream ! "
      "audioconvert ! audioresample ! audio/x-raw,format=F32LE,rate=48000,channels=2,layout=interleaved ! "
      "avenc_aac name=audio_encoder bitrate=%d ! tee name=audio_tee "
      "audio_tee. ! queue ! clocksync name=audio_clock sync=true sync-to-first=true ! aacparse ! audio/mpeg,mpegversion=(int)4,stream-format=(string)raw,channels=(int)2,rate=(int)48000 ! rtsp_sink.sink_1 "
      "audio_tee. ! queue ! aacparse ! audio/mpeg,mpegversion=(int)4,stream-format=(string)raw,channels=(int)2,rate=(int)48000 ! record_mux.audio_0",
      escaped_url, escaped_recording, encoder_description, audio_bitrate_bps);
  g_free(escaped_url);
  g_free(escaped_recording);
  g_free(encoder_description);

  GstElement *pipeline = gst_parse_launch(description, error);
  g_free(description);
  if (pipeline == NULL) {
    return NULL;
  }

  gchar *video_input = g_strdup_printf(
      "udpsrc name=video_src buffer-size=67108864 port=%d caps=application/x-rtp,media=(string)video,clock-rate=(int)90000,encoding-name=(string)H264,payload=(int)96,packetization-mode=(int)1 ! "
      "%s ! rtph264depay name=video_depay request-keyframe=false wait-for-keyframe=false ! "
      "h264parse config-interval=-1 ! %s ! videoconvert ! videoscale add-borders=true ! videorate name=video_rate drop-only=true ! "
      "video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1,pixel-aspect-ratio=1/1 ! "
      "queue name=video_input_queue max-size-buffers=8 leaky=downstream ! "
      "appsink name=video_sink emit-signals=false sync=false max-buffers=8 drop=true",
      video_port, video_reorder_element, decoder_description, width, height, fps);
  gchar *audio_input = g_strdup_printf(
      "udpsrc name=audio_src buffer-size=67108864 port=%d caps=application/x-rtp,media=(string)audio,clock-rate=(int)44100,encoding-name=(string)L16,payload=(int)96,channels=(int)2 ! "
      "%s ! rtpL16depay name=audio_depay ! audioconvert ! audiorate name=audio_rate skip-to-first=true tolerance=5000000 ! audioresample ! "
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! "
      "queue name=audio_input_queue max-size-buffers=32 ! appsink name=audio_sink emit-signals=false sync=false max-buffers=32 drop=false",
      audio_port, audio_reorder_element);
  GstElement *video_bin = gst_parse_bin_from_description(video_input, TRUE, error);
  g_free(video_input);
  g_free(decoder_description);
  if (video_bin == NULL) {
    gst_object_unref(pipeline);
    g_free(audio_input);
    return NULL;
  }
  GstElement *audio_bin = gst_parse_bin_from_description(audio_input, TRUE, error);
  g_free(audio_input);
  if (audio_bin == NULL) {
    gst_object_unref(video_bin);
    gst_object_unref(pipeline);
    return NULL;
  }
  gst_bin_add_many(GST_BIN(pipeline), video_bin, audio_bin, NULL);
  if (!configure_audio_rtsp_payloader(pipeline, error)) {
    gst_object_unref(pipeline);
    return NULL;
  }
  return pipeline;
}

static GstElement *create_with_encoder_fallback(int video_port, int audio_port,
                                                 const char *publish_url,
                                                 const char *recording,
                                                 int width, int height, int fps,
                                                 int bitrate_kbps, int maxrate_kbps,
                                                 int buffer_size_kbps, int audio_bitrate_bps,
                                                 int gop,
                                                 gboolean *used_nvenc,
                                                 GError **error) {
  const char *force_cpu = g_getenv("IMAGEPAD_AIRPLAY_GSTREAMER_CPU");
  if (force_cpu != NULL &&
      (strcmp(force_cpu, "1") == 0 || strcmp(force_cpu, "true") == 0 ||
       strcmp(force_cpu, "yes") == 0 || strcmp(force_cpu, "on") == 0)) {
    g_printerr("AirPlay direct GStreamer pipeline: forcing x264enc by environment\n");
    *used_nvenc = FALSE;
    return create_direct_pipeline(video_port, audio_port, publish_url, recording,
                                  width, height, fps, bitrate_kbps, maxrate_kbps,
                                  buffer_size_kbps, audio_bitrate_bps, gop, FALSE, error);
  }
  GstElementFactory *nvenc_factory = gst_element_factory_find("nvh264enc");
  GstElementFactory *nvdec_factory = gst_element_factory_find("nvh264dec");
  gboolean has_nvenc = nvenc_factory != NULL;
  gboolean has_nvdec = nvdec_factory != NULL;
  if (nvenc_factory != NULL) gst_object_unref(nvenc_factory);
  if (nvdec_factory != NULL) gst_object_unref(nvdec_factory);
  if (has_nvenc && has_nvdec) {
    GstElement *pipeline = create_direct_pipeline(video_port, audio_port, publish_url, recording,
                                                   width, height, fps, bitrate_kbps, maxrate_kbps,
                                                   buffer_size_kbps, audio_bitrate_bps, gop, TRUE, error);
    if (pipeline != NULL) {
      *used_nvenc = TRUE;
      return pipeline;
    }
    g_clear_error(error);
  }
  *used_nvenc = FALSE;
  return create_direct_pipeline(video_port, audio_port, publish_url, recording,
                                width, height, fps, bitrate_kbps, maxrate_kbps,
                                buffer_size_kbps, audio_bitrate_bps, gop, FALSE, error);
}

static GstFlowReturn push_buffer(GstAppSrc *source, GstBuffer *buffer,
                                 const char *label) {
  GstFlowReturn result = gst_app_src_push_buffer(source, buffer);
  if (result != GST_FLOW_OK && result != GST_FLOW_FLUSHING) {
    g_printerr("AirPlay direct GStreamer: %s push failed: %s\n",
               label, gst_flow_get_name(result));
  }
  return result;
}

static GstFlowReturn handle_video_sample(AirPlayDirectPipeline *ctx, GstSample *sample) {
  GstBuffer *input = gst_sample_get_buffer(sample);
  if (input == NULL) return GST_FLOW_ERROR;
  GstBuffer *output = gst_buffer_copy_deep(input);
  if (output == NULL) return GST_FLOW_ERROR;
  GstClockTime now = running_time(ctx);
  g_mutex_lock(&ctx->timing_mutex);
  GstClockTime pts = reclock_pts(&ctx->have_video_pts, &ctx->last_video_pts,
                                 now, ctx->video_period);
  GST_BUFFER_PTS(output) = pts;
  GST_BUFFER_DTS(output) = GST_CLOCK_TIME_NONE;
  GST_BUFFER_DURATION(output) = ctx->video_period;
  ctx->last_real_video_time = now;
  airplay_last_frame_replace(&ctx->last_frame, output);
  g_mutex_unlock(&ctx->timing_mutex);
  return push_buffer(GST_APP_SRC(ctx->video_source), output, "video");
}

static GstFlowReturn handle_audio_sample(AirPlayDirectPipeline *ctx, GstSample *sample) {
  GstBuffer *input = gst_sample_get_buffer(sample);
  if (input == NULL) return GST_FLOW_ERROR;
  GstBuffer *output = gst_buffer_copy_deep(input);
  if (output == NULL) return GST_FLOW_ERROR;
  GstClockTime now = running_time(ctx);
  g_mutex_lock(&ctx->timing_mutex);
  GstClockTime pts = reclock_pts(&ctx->have_audio_pts, &ctx->last_audio_pts,
                                 now, ctx->audio_period);
  GST_BUFFER_PTS(output) = pts;
  GST_BUFFER_DTS(output) = GST_CLOCK_TIME_NONE;
  if (GST_BUFFER_DURATION(output) == GST_CLOCK_TIME_NONE) {
    GST_BUFFER_DURATION(output) = ctx->audio_period;
  }
  ctx->last_real_audio_time = now;
  g_mutex_unlock(&ctx->timing_mutex);
  return push_buffer(GST_APP_SRC(ctx->audio_source), output, "audio");
}

static gboolean poll_direct_samples(gpointer user_data) {
  AirPlayDirectPipeline *ctx = (AirPlayDirectPipeline *)user_data;
  // Drain the bounded appsink queues before the next clock tick. clocksync on
  // the RTSP branches paces the resulting buffers, while leaving stale input
  // queued would turn a short sender pause into accumulating latency.
  GstSample *sample;
  while ((sample = gst_app_sink_try_pull_sample(GST_APP_SINK(ctx->video_sink), 0)) != NULL) {
    GstFlowReturn result = handle_video_sample(ctx, sample);
    gst_sample_unref(sample);
    if (result != GST_FLOW_OK) return G_SOURCE_CONTINUE;
  }
  while ((sample = gst_app_sink_try_pull_sample(GST_APP_SINK(ctx->audio_sink), 0)) != NULL) {
    GstFlowReturn result = handle_audio_sample(ctx, sample);
    gst_sample_unref(sample);
    if (result != GST_FLOW_OK) return G_SOURCE_CONTINUE;
  }
  return G_SOURCE_CONTINUE;
}

static gboolean repeat_last_video(gpointer user_data) {
  AirPlayDirectPipeline *ctx = (AirPlayDirectPipeline *)user_data;
  GstBuffer *held = airplay_last_frame_ref(&ctx->last_frame);
  if (held == NULL && ctx->black_frame != NULL) {
    held = gst_buffer_ref(ctx->black_frame);
  }
  if (held == NULL) return G_SOURCE_CONTINUE;

  GstClockTime now = running_time(ctx);
  g_mutex_lock(&ctx->timing_mutex);
  gboolean gap = !ctx->have_video_pts ||
                 ctx->last_real_video_time == GST_CLOCK_TIME_NONE ||
                 now >= ctx->last_real_video_time + ctx->video_period;
  GstClockTime pts = ctx->have_video_pts ? ctx->last_video_pts + ctx->video_period : now;
  if (pts < now) pts = now;
  g_mutex_unlock(&ctx->timing_mutex);

  if (!gap) {
    gst_buffer_unref(held);
    return G_SOURCE_CONTINUE;
  }
  GstBuffer *output = copy_with_timing(held, pts, ctx->video_period);
  gst_buffer_unref(held);
  if (output == NULL) return G_SOURCE_CONTINUE;
  g_mutex_lock(&ctx->timing_mutex);
  ctx->have_video_pts = TRUE;
  ctx->last_video_pts = pts;
  g_mutex_unlock(&ctx->timing_mutex);
  push_buffer(GST_APP_SRC(ctx->video_source), output, "held video");
  return G_SOURCE_CONTINUE;
}

static gboolean repeat_silent_audio(gpointer user_data) {
  AirPlayDirectPipeline *ctx = (AirPlayDirectPipeline *)user_data;
  GstClockTime now = running_time(ctx);
  g_mutex_lock(&ctx->timing_mutex);
  gboolean gap = !ctx->have_audio_pts ||
                 ctx->last_real_audio_time == GST_CLOCK_TIME_NONE ||
                 now >= ctx->last_real_audio_time + ctx->audio_period;
  GstClockTime pts = ctx->have_audio_pts ? ctx->last_audio_pts + ctx->audio_period : now;
  if (pts < now) pts = now;
  g_mutex_unlock(&ctx->timing_mutex);
  if (!gap) return G_SOURCE_CONTINUE;

  GstBuffer *silence = make_silence(ctx->audio_period);
  if (silence == NULL) return G_SOURCE_CONTINUE;
  GST_BUFFER_PTS(silence) = pts;
  GST_BUFFER_DTS(silence) = GST_CLOCK_TIME_NONE;
  GST_BUFFER_DURATION(silence) = ctx->audio_period;
  g_mutex_lock(&ctx->timing_mutex);
  ctx->have_audio_pts = TRUE;
  ctx->last_audio_pts = pts;
  g_mutex_unlock(&ctx->timing_mutex);
  push_buffer(GST_APP_SRC(ctx->audio_source), silence, "silence");
  return G_SOURCE_CONTINUE;
}

static gboolean request_eos_if_stopping(gpointer user_data) {
  AirPlayDirectPipeline *ctx = (AirPlayDirectPipeline *)user_data;
  if (ctx->stop_file != NULL && g_file_test(ctx->stop_file, G_FILE_TEST_EXISTS)) {
    ctx->stop_requested = TRUE;
    g_printerr("AirPlay direct GStreamer pipeline: graceful stop requested\n");
    gst_element_send_event(ctx->pipeline, gst_event_new_eos());
    return G_SOURCE_REMOVE;
  }
  return G_SOURCE_CONTINUE;
}

static gboolean on_bus_message(GstBus *bus, GstMessage *message, gpointer user_data) {
  (void)bus;
  AirPlayDirectPipeline *ctx = (AirPlayDirectPipeline *)user_data;
  switch (GST_MESSAGE_TYPE(message)) {
    case GST_MESSAGE_ERROR: {
      GError *error = NULL;
      gchar *debug = NULL;
      gst_message_parse_error(message, &error, &debug);
      g_printerr("AirPlay direct GStreamer pipeline error: %s%s%s\n",
                 error == NULL ? "unknown" : error->message,
                 debug == NULL ? "" : " (",
                 debug == NULL ? "" : debug);
      if (debug != NULL) g_printerr(")\n");
      g_clear_error(&error);
      g_free(debug);
      ctx->fatal_error = TRUE;
      g_main_loop_quit(ctx->loop);
      break;
    }
    case GST_MESSAGE_EOS:
      g_printerr("AirPlay direct GStreamer pipeline reached EOS\n");
      g_main_loop_quit(ctx->loop);
      break;
    default:
      break;
  }
  return G_SOURCE_CONTINUE;
}

int airplay_direct_pipeline_run(int video_port, int audio_port,
                                const char *publish_url, const char *recording,
                                const char *stop_file,
                                int width, int height, int fps,
                                int bitrate_kbps, int maxrate_kbps,
                                int buffer_size_kbps, int audio_bitrate_bps,
                                int gop) {
  gst_init(NULL, NULL);
  GError *error = NULL;
  gboolean used_nvenc = FALSE;
  g_printerr("AirPlay direct GStreamer pipeline: constructing held-frame publisher\n");
  GstElement *pipeline = create_with_encoder_fallback(video_port, audio_port, publish_url, recording,
                                                      width, height, fps, bitrate_kbps, maxrate_kbps,
                                                      buffer_size_kbps, audio_bitrate_bps, gop,
                                                      &used_nvenc, &error);
  if (pipeline == NULL) {
    g_printerr("AirPlay direct GStreamer pipeline construction failed: %s\n",
               error == NULL ? "unknown" : error->message);
    g_clear_error(&error);
    return 1;
  }

  AirPlayDirectPipeline ctx = {0};
  ctx.loop = g_main_loop_new(NULL, FALSE);
  ctx.pipeline = pipeline;
  ctx.stop_file = stop_file;
  ctx.video_period = GST_SECOND / (fps > 0 ? fps : 60);
  ctx.audio_period = GST_SECOND / 100;
  ctx.started_monotonic_us = g_get_monotonic_time();
  ctx.last_video_pts = GST_CLOCK_TIME_NONE;
  ctx.last_audio_pts = GST_CLOCK_TIME_NONE;
  ctx.last_real_video_time = GST_CLOCK_TIME_NONE;
  ctx.last_real_audio_time = GST_CLOCK_TIME_NONE;
  ctx.diagnostics = g_getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG") != NULL &&
                    strcmp(g_getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG"), "1") == 0;
  ctx.video_source = gst_bin_get_by_name(GST_BIN(pipeline), "video_source");
  ctx.audio_source = gst_bin_get_by_name(GST_BIN(pipeline), "audio_source");
  ctx.video_sink = gst_bin_get_by_name(GST_BIN(pipeline), "video_sink");
  ctx.audio_sink = gst_bin_get_by_name(GST_BIN(pipeline), "audio_sink");
  if (ctx.video_source == NULL || ctx.audio_source == NULL ||
      ctx.video_sink == NULL || ctx.audio_sink == NULL) {
    g_printerr("AirPlay direct GStreamer pipeline element lookup failed\n");
    if (ctx.video_source != NULL) gst_object_unref(ctx.video_source);
    if (ctx.audio_source != NULL) gst_object_unref(ctx.audio_source);
    if (ctx.video_sink != NULL) gst_object_unref(ctx.video_sink);
    if (ctx.audio_sink != NULL) gst_object_unref(ctx.audio_sink);
    g_main_loop_unref(ctx.loop);
    gst_object_unref(pipeline);
    return 1;
  }

  if (!set_direct_source_caps(&ctx, width, height, fps)) {
    g_printerr("AirPlay direct GStreamer pipeline caps construction failed\n");
    gst_object_unref(ctx.video_source);
    gst_object_unref(ctx.audio_source);
    gst_object_unref(ctx.video_sink);
    gst_object_unref(ctx.audio_sink);
    g_main_loop_unref(ctx.loop);
    gst_object_unref(pipeline);
    return 1;
  }
  ctx.black_frame = make_black_frame((guint)width, (guint)height);
  if (ctx.black_frame == NULL) {
    g_printerr("AirPlay direct GStreamer pipeline black frame allocation failed\n");
    gst_object_unref(ctx.video_source);
    gst_object_unref(ctx.audio_source);
    gst_object_unref(ctx.video_sink);
    gst_object_unref(ctx.audio_sink);
    g_main_loop_unref(ctx.loop);
    gst_object_unref(pipeline);
    return 1;
  }
  airplay_last_frame_init(&ctx.last_frame);
  g_mutex_init(&ctx.timing_mutex);

  GstBus *bus = gst_element_get_bus(pipeline);
  guint watch_id = gst_bus_add_watch(bus, on_bus_message, &ctx);
  gst_object_unref(bus);
  install_stage_probe(&ctx, "video_src", "video-udp", 0);
  install_stage_probe(&ctx, "video_jitter", "video-jitter", 1);
  install_stage_probe(&ctx, "video_depay", "video-depay", 2);
  install_stage_probe(&ctx, "video_decoder", "video-decoder", 3);
  install_stage_probe(&ctx, "video_encoder", "video-encoder", 4);
  install_stage_probe(&ctx, "audio_src", "audio-udp", 5);
  install_stage_probe(&ctx, "audio_jitter", "audio-jitter", 6);
  install_stage_probe(&ctx, "audio_depay", "audio-depay", 7);

  GstStateChangeReturn state = gst_element_set_state(pipeline, GST_STATE_PLAYING);
  if (state == GST_STATE_CHANGE_FAILURE && used_nvenc) {
    g_printerr("AirPlay direct GStreamer pipeline: NVENC failed; retrying with x264\n");
    if (watch_id != 0) g_source_remove(watch_id);
    gst_element_set_state(pipeline, GST_STATE_NULL);
    gst_object_unref(pipeline);
    g_clear_error(&error);
    pipeline = create_direct_pipeline(video_port, audio_port, publish_url, recording,
                                      width, height, fps, bitrate_kbps, maxrate_kbps,
                                      buffer_size_kbps, audio_bitrate_bps, gop, FALSE, &error);
    if (pipeline != NULL) {
      ctx.pipeline = pipeline;
      ctx.video_source = gst_bin_get_by_name(GST_BIN(pipeline), "video_source");
      ctx.audio_source = gst_bin_get_by_name(GST_BIN(pipeline), "audio_source");
      ctx.video_sink = gst_bin_get_by_name(GST_BIN(pipeline), "video_sink");
      ctx.audio_sink = gst_bin_get_by_name(GST_BIN(pipeline), "audio_sink");
      if (!set_direct_source_caps(&ctx, width, height, fps)) {
        g_printerr("AirPlay direct GStreamer pipeline fallback caps construction failed\n");
        gst_object_unref(pipeline);
        pipeline = NULL;
      }
      if (pipeline != NULL) {
      state = gst_element_set_state(pipeline, GST_STATE_PLAYING);
      if (state != GST_STATE_CHANGE_FAILURE) {
        GstBus *fallback_bus = gst_element_get_bus(pipeline);
        watch_id = gst_bus_add_watch(fallback_bus, on_bus_message, &ctx);
        gst_object_unref(fallback_bus);
      }
      }
    }
  }
  if (state == GST_STATE_CHANGE_FAILURE || pipeline == NULL) {
    g_printerr("AirPlay direct GStreamer pipeline: failed to enter PLAYING state: %s\n",
               error == NULL ? "unknown" : error->message);
    g_clear_error(&error);
    if (pipeline != NULL) {
      gst_element_set_state(pipeline, GST_STATE_NULL);
      gst_object_unref(pipeline);
    }
    if (watch_id != 0) g_source_remove(watch_id);
    airplay_last_frame_clear(&ctx.last_frame);
    g_mutex_clear(&ctx.timing_mutex);
    gst_buffer_unref(ctx.black_frame);
    gst_object_unref(ctx.video_source);
    gst_object_unref(ctx.audio_source);
    gst_object_unref(ctx.video_sink);
    gst_object_unref(ctx.audio_sink);
    g_main_loop_unref(ctx.loop);
    return 1;
  }
  g_printerr("AirPlay direct GStreamer pipeline: RTSP/TCP publisher is running (%s, held-frame)\n",
             used_nvenc ? "nvh264enc" : "x264enc");
  guint poll_id = g_timeout_add(1, poll_direct_samples, &ctx);
  guint video_hold_id = g_timeout_add(16, repeat_last_video, &ctx);
  guint audio_hold_id = g_timeout_add(10, repeat_silent_audio, &ctx);
  guint stop_watch_id = g_timeout_add(100, request_eos_if_stopping, &ctx);
  g_main_loop_run(ctx.loop);

  gst_element_set_state(pipeline, GST_STATE_NULL);
  if (poll_id != 0) g_source_remove(poll_id);
  if (video_hold_id != 0) g_source_remove(video_hold_id);
  if (audio_hold_id != 0) g_source_remove(audio_hold_id);
  if (stop_watch_id != 0 && !ctx.stop_requested) g_source_remove(stop_watch_id);
  if (watch_id != 0) g_source_remove(watch_id);
  airplay_last_frame_clear(&ctx.last_frame);
  g_mutex_clear(&ctx.timing_mutex);
  gst_buffer_unref(ctx.black_frame);
  gst_object_unref(ctx.video_source);
  gst_object_unref(ctx.audio_source);
  gst_object_unref(ctx.video_sink);
  gst_object_unref(ctx.audio_sink);
  g_main_loop_unref(ctx.loop);
  gst_object_unref(pipeline);
  return ctx.fatal_error ? 1 : 0;
}
