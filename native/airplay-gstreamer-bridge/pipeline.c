#include "pipeline.h"

#include "last_frame.h"

#include <gst/app/gstappsink.h>
#include <gst/app/gstappsrc.h>
#include <gst/gst.h>

#include <errno.h>
#include <stdio.h>
#include <string.h>

#ifdef G_OS_WIN32
#include <fcntl.h>
#include <io.h>
#else
#include <unistd.h>
#endif

typedef struct {
  GstElement *pipeline;
  GstElement *video_source;
  GstElement *audio_source;
  GstElement *video_sink;
  GstElement *audio_sink;
  GstElement *mux_sink;
  GMainLoop *loop;
  AirPlayLastFrame last_frame;
  GMutex timing_mutex;
  gboolean have_video_pts;
  gboolean have_audio_pts;
  GstClockTime last_video_pts;
  GstClockTime last_audio_pts;
  GstClockTime last_real_video_time;
  guint video_samples;
  guint audio_samples;
  guint mux_samples;
  gboolean fatal_error;
} AirPlayPipeline;

static const GstClockTime video_period = GST_SECOND / 60;

static GstClockTime running_time(AirPlayPipeline *ctx) {
  GstClock *clock = gst_element_get_clock(ctx->pipeline);
  if (clock == NULL) {
    return (GstClockTime)g_get_monotonic_time() * 1000;
  }
  GstClockTime now = gst_clock_get_time(clock);
  GstClockTime base = gst_element_get_base_time(ctx->pipeline);
  gst_object_unref(clock);
  if (now < base) {
    return 0;
  }
  return now - base;
}

static GstClockTime monotonic_pts(GstClockTime input, gboolean *have_last, GstClockTime *last, GstClockTime fallback) {
  if (input == GST_CLOCK_TIME_NONE) {
    input = fallback;
  }
  if (*have_last && input <= *last) {
    input = *last + video_period;
  }
  *have_last = TRUE;
  *last = input;
  return input;
}

static int write_all_stdout(const guint8 *data, gsize size) {
  gsize offset = 0;
  while (offset < size) {
#ifdef G_OS_WIN32
    int count = _write(_fileno(stdout), data + offset, (unsigned int)(size - offset));
#else
    ssize_t count = write(STDOUT_FILENO, data + offset, size - offset);
#endif
    if (count < 0) {
      if (errno == EINTR) {
        continue;
      }
      return -1;
    }
    if (count == 0) {
      return -1;
    }
    offset += (gsize)count;
  }
  return 0;
}

static GstFlowReturn handle_mux_sample(AirPlayPipeline *ctx, GstSample *sample) {
  GstBuffer *buffer = gst_sample_get_buffer(sample);
  ctx->mux_samples++;
  if (ctx->mux_samples == 1) {
    g_printerr("AirPlay GStreamer bridge: mux output started\n");
  }
  GstMapInfo map;
  int result = 0;
  if (buffer == NULL || !gst_buffer_map(buffer, &map, GST_MAP_READ)) {
    result = -1;
  } else {
    result = write_all_stdout(map.data, map.size);
    gst_buffer_unmap(buffer, &map);
  }
  if (result != 0) {
    g_printerr("AirPlay GStreamer bridge: stdout write failed\n");
    ctx->fatal_error = TRUE;
    g_main_loop_quit(ctx->loop);
    return GST_FLOW_ERROR;
  }
  return GST_FLOW_OK;
}

static GstFlowReturn on_mux_sample(GstAppSink *sink, gpointer user_data) {
  GstSample *sample = gst_app_sink_pull_sample(sink);
  if (sample == NULL) {
    return GST_FLOW_EOS;
  }
  GstFlowReturn result = handle_mux_sample((AirPlayPipeline *)user_data, sample);
  gst_sample_unref(sample);
  return result;
}

static GstFlowReturn handle_video_sample(AirPlayPipeline *ctx, GstSample *sample) {
  GstBuffer *input = gst_sample_get_buffer(sample);
  ctx->video_samples++;
  if (ctx->video_samples == 1) {
    g_printerr("AirPlay GStreamer bridge: first decoded video sample\n");
  }
  GstBuffer *output = input == NULL ? NULL : gst_buffer_copy_deep(input);
  if (output == NULL) {
    return GST_FLOW_ERROR;
  }

  GstClockTime now = running_time(ctx);
  g_mutex_lock(&ctx->timing_mutex);
  GstClockTime pts = monotonic_pts(GST_BUFFER_PTS(input), &ctx->have_video_pts, &ctx->last_video_pts, now);
  ctx->last_real_video_time = now;
  GST_BUFFER_PTS(output) = pts;
  GST_BUFFER_DTS(output) = GST_CLOCK_TIME_NONE;
  GST_BUFFER_DURATION(output) = video_period;
  airplay_last_frame_replace(&ctx->last_frame, output);
  g_mutex_unlock(&ctx->timing_mutex);

  GstFlowReturn result = gst_app_src_push_buffer(GST_APP_SRC(ctx->video_source), output);
  if (result != GST_FLOW_OK && result != GST_FLOW_FLUSHING) {
    g_printerr("AirPlay GStreamer bridge: video appsrc push failed: %s\n", gst_flow_get_name(result));
  }
  return result;
}

static GstFlowReturn on_video_sample(GstAppSink *sink, gpointer user_data) {
  GstSample *sample = gst_app_sink_pull_sample(sink);
  if (sample == NULL) {
    return GST_FLOW_EOS;
  }
  GstFlowReturn result = handle_video_sample((AirPlayPipeline *)user_data, sample);
  gst_sample_unref(sample);
  return result;
}

static GstFlowReturn handle_audio_sample(AirPlayPipeline *ctx, GstSample *sample) {
  GstBuffer *input = gst_sample_get_buffer(sample);
  ctx->audio_samples++;
  if (ctx->audio_samples == 1) {
    g_printerr("AirPlay GStreamer bridge: first decoded audio sample\n");
  }
  GstBuffer *output = input == NULL ? NULL : gst_buffer_copy_deep(input);
  if (output == NULL) {
    return GST_FLOW_ERROR;
  }
  GstClockTime now = running_time(ctx);
  g_mutex_lock(&ctx->timing_mutex);
  GST_BUFFER_PTS(output) = monotonic_pts(GST_BUFFER_PTS(input), &ctx->have_audio_pts, &ctx->last_audio_pts, now);
  GST_BUFFER_DTS(output) = GST_CLOCK_TIME_NONE;
  g_mutex_unlock(&ctx->timing_mutex);
  GstFlowReturn result = gst_app_src_push_buffer(GST_APP_SRC(ctx->audio_source), output);
  if (result != GST_FLOW_OK && result != GST_FLOW_FLUSHING) {
    g_printerr("AirPlay GStreamer bridge: audio appsrc push failed: %s\n", gst_flow_get_name(result));
  }
  return result;
}

static GstFlowReturn on_audio_sample(GstAppSink *sink, gpointer user_data) {
  GstSample *sample = gst_app_sink_pull_sample(sink);
  if (sample == NULL) {
    return GST_FLOW_EOS;
  }
  GstFlowReturn result = handle_audio_sample((AirPlayPipeline *)user_data, sample);
  gst_sample_unref(sample);
  return result;
}

static gboolean poll_samples(gpointer user_data) {
  AirPlayPipeline *ctx = (AirPlayPipeline *)user_data;
  GstSample *sample;
  sample = gst_app_sink_try_pull_preroll(GST_APP_SINK(ctx->video_sink), 0);
  if (sample != NULL) {
    handle_video_sample(ctx, sample);
    gst_sample_unref(sample);
  }
  sample = gst_app_sink_try_pull_preroll(GST_APP_SINK(ctx->audio_sink), 0);
  if (sample != NULL) {
    handle_audio_sample(ctx, sample);
    gst_sample_unref(sample);
  }
  sample = gst_app_sink_try_pull_preroll(GST_APP_SINK(ctx->mux_sink), 0);
  if (sample != NULL) {
    handle_mux_sample(ctx, sample);
    gst_sample_unref(sample);
  }
  while ((sample = gst_app_sink_try_pull_sample(GST_APP_SINK(ctx->video_sink), 0)) != NULL) {
    handle_video_sample(ctx, sample);
    gst_sample_unref(sample);
  }
  while ((sample = gst_app_sink_try_pull_sample(GST_APP_SINK(ctx->audio_sink), 0)) != NULL) {
    handle_audio_sample(ctx, sample);
    gst_sample_unref(sample);
  }
  while ((sample = gst_app_sink_try_pull_sample(GST_APP_SINK(ctx->mux_sink), 0)) != NULL) {
    handle_mux_sample(ctx, sample);
    gst_sample_unref(sample);
  }
  return G_SOURCE_CONTINUE;
}

static gboolean repeat_last_video(gpointer user_data) {
  AirPlayPipeline *ctx = (AirPlayPipeline *)user_data;
  GstBuffer *held = airplay_last_frame_ref(&ctx->last_frame);
  if (held == NULL) {
    return G_SOURCE_CONTINUE;
  }
  GstClockTime now = running_time(ctx);
  g_mutex_lock(&ctx->timing_mutex);
  gboolean gap = !ctx->have_video_pts || now > ctx->last_real_video_time + video_period;
  GstClockTime pts = ctx->last_video_pts;
  if (gap) {
    GstClockTime next = pts == GST_CLOCK_TIME_NONE ? now : pts + video_period;
    if (next < now) {
      next = now;
    }
    GST_BUFFER_PTS(held) = next;
    GST_BUFFER_DTS(held) = GST_CLOCK_TIME_NONE;
    GST_BUFFER_DURATION(held) = video_period;
    ctx->last_video_pts = next;
  }
  g_mutex_unlock(&ctx->timing_mutex);

  if (gap) {
    GstFlowReturn result = gst_app_src_push_buffer(GST_APP_SRC(ctx->video_source), held);
    if (result != GST_FLOW_OK && result != GST_FLOW_FLUSHING) {
      g_printerr("AirPlay GStreamer bridge: held video push failed: %s\n", gst_flow_get_name(result));
    }
  } else {
    gst_buffer_unref(held);
  }
  return G_SOURCE_CONTINUE;
}

static gboolean on_bus_message(GstBus *bus, GstMessage *message, gpointer user_data) {
  (void)bus;
  AirPlayPipeline *ctx = (AirPlayPipeline *)user_data;
  switch (GST_MESSAGE_TYPE(message)) {
    case GST_MESSAGE_ERROR: {
      GError *error = NULL;
      gchar *debug = NULL;
      gst_message_parse_error(message, &error, &debug);
      g_printerr("AirPlay GStreamer bridge: pipeline error: %s%s%s\n",
                 error == NULL ? "unknown" : error->message,
                 debug == NULL ? "" : " (",
                 debug == NULL ? "" : debug);
      if (debug != NULL) {
        g_printerr(")\n");
      }
      g_clear_error(&error);
      g_free(debug);
      ctx->fatal_error = TRUE;
      g_main_loop_quit(ctx->loop);
      break;
    }
    case GST_MESSAGE_EOS:
      g_printerr("AirPlay GStreamer bridge: unexpected EOS\n");
      ctx->fatal_error = TRUE;
      g_main_loop_quit(ctx->loop);
      break;
    default:
      break;
  }
  return G_SOURCE_CONTINUE;
}

int airplay_pipeline_run(int video_port, int audio_port) {
#ifdef G_OS_WIN32
  _setmode(_fileno(stdout), _O_BINARY);
#endif
  gst_init(NULL, NULL);
  GError *error = NULL;
  g_printerr("AirPlay GStreamer bridge: constructing pipeline\n");
  GstElement *pipeline = gst_pipeline_new("airplay-pipeline");
  GstElement *mux = gst_element_factory_make("matroskamux", "mux");
  GstElement *mux_output_sink = gst_element_factory_make("appsink", "mux_sink");
  GstElement *video_source = gst_element_factory_make("appsrc", "video_source");
  GstElement *audio_source = gst_element_factory_make("appsrc", "audio_source");
  GstElement *video_queue = gst_element_factory_make("queue", "video_output_queue");
  GstElement *audio_queue = gst_element_factory_make("queue", "audio_output_queue");
  if (pipeline == NULL || mux == NULL || mux_output_sink == NULL || video_source == NULL || audio_source == NULL ||
      video_queue == NULL || audio_queue == NULL) {
    g_printerr("AirPlay GStreamer bridge: failed to create output elements\n");
    if (pipeline != NULL) gst_object_unref(pipeline);
    return 1;
  }
  g_object_set(mux, "streamable", TRUE, NULL);
  g_object_set(mux_output_sink, "emit-signals", TRUE, "sync", FALSE, NULL);
  g_object_set(video_source, "is-live", TRUE, "format", GST_FORMAT_TIME, "do-timestamp", FALSE, "block", FALSE, NULL);
  g_object_set(audio_source, "is-live", TRUE, "format", GST_FORMAT_TIME, "do-timestamp", FALSE, "block", FALSE, NULL);
  GstCaps *video_caps = gst_caps_from_string("video/x-raw,format=I420,width=1920,height=1080,framerate=60/1");
  GstCaps *audio_caps = gst_caps_from_string("audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved");
  gst_app_src_set_caps(GST_APP_SRC(video_source), video_caps);
  gst_app_src_set_caps(GST_APP_SRC(audio_source), audio_caps);
  gst_caps_unref(video_caps);
  gst_caps_unref(audio_caps);
  g_object_set(video_queue, "max-size-buffers", 8, NULL);
  g_object_set(audio_queue, "max-size-buffers", 32, NULL);
  gst_bin_add_many(GST_BIN(pipeline), mux, mux_output_sink, video_source, video_queue, audio_source, audio_queue, NULL);
  if (!gst_element_link(mux, mux_output_sink) || !gst_element_link(video_source, video_queue) || !gst_element_link(audio_source, audio_queue)) {
    g_printerr("AirPlay GStreamer bridge: failed to link output elements\n");
    gst_object_unref(pipeline);
    return 1;
  }
  GstPad *video_pad = gst_element_get_request_pad(mux, "video_%u");
  GstPad *audio_pad = gst_element_get_request_pad(mux, "audio_%u");
  GstPad *video_src_pad = gst_element_get_static_pad(video_queue, "src");
  GstPad *audio_src_pad = gst_element_get_static_pad(audio_queue, "src");
  gboolean linked = video_pad != NULL && audio_pad != NULL && video_src_pad != NULL && audio_src_pad != NULL &&
                    gst_pad_link(video_src_pad, video_pad) == GST_PAD_LINK_OK &&
                    gst_pad_link(audio_src_pad, audio_pad) == GST_PAD_LINK_OK;
  if (video_src_pad != NULL) gst_object_unref(video_src_pad);
  if (audio_src_pad != NULL) gst_object_unref(audio_src_pad);
  if (video_pad != NULL) gst_object_unref(video_pad);
  if (audio_pad != NULL) gst_object_unref(audio_pad);
  if (!linked) {
    g_printerr("AirPlay GStreamer bridge: failed to link output pads\n");
    gst_object_unref(pipeline);
    return 1;
  }

  gchar *video_description = g_strdup_printf(
    "udpsrc port=%d caps=application/x-rtp,media=(string)video,clock-rate=(int)90000,encoding-name=(string)H264,payload=(int)96,packetization-mode=(int)1 ! "
    "rtpjitterbuffer latency=20 drop-on-latency=true do-lost=false ! rtph264depay ! h264parse config-interval=-1 ! "
    "avdec_h264 output-corrupt=false ! videoconvert ! videoscale ! videoconvert ! video/x-raw,format=I420,width=1920,height=1080 ! "
    "queue max-size-buffers=8 leaky=downstream ! appsink name=video_sink emit-signals=true sync=false max-buffers=8 drop=true",
    video_port);
  gchar *audio_description = g_strdup_printf(
    "udpsrc port=%d caps=application/x-rtp,media=(string)audio,clock-rate=(int)44100,encoding-name=(string)L16,payload=(int)96,channels=(int)2 ! "
    "rtpjitterbuffer latency=20 drop-on-latency=true do-lost=false ! rtpL16depay ! audioconvert ! audioresample ! "
    "audio/x-raw,format=S16LE,rate=48000,channels=2 ! queue max-size-buffers=32 ! appsink name=audio_sink emit-signals=true sync=false max-buffers=32 drop=false",
    audio_port);
  GstElement *video_bin = gst_parse_bin_from_description(video_description, TRUE, &error);
  g_free(video_description);
  if (video_bin == NULL || error != NULL) {
    g_printerr("AirPlay GStreamer bridge: video pipeline construction failed: %s\n", error == NULL ? "unknown" : error->message);
    g_clear_error(&error);
    gst_object_unref(pipeline);
    g_free(audio_description);
    return 1;
  }
  GstElement *audio_bin = gst_parse_bin_from_description(audio_description, TRUE, &error);
  g_free(audio_description);
  if (audio_bin == NULL || error != NULL) {
    g_printerr("AirPlay GStreamer bridge: audio pipeline construction failed: %s\n", error == NULL ? "unknown" : error->message);
    g_clear_error(&error);
    gst_object_unref(video_bin);
    gst_object_unref(pipeline);
    return 1;
  }
  gst_bin_add_many(GST_BIN(pipeline), video_bin, audio_bin, NULL);
  g_printerr("AirPlay GStreamer bridge: pipeline construction returned\n");

  AirPlayPipeline ctx = {0};
  ctx.pipeline = pipeline;
  ctx.loop = g_main_loop_new(NULL, FALSE);
  ctx.video_source = gst_bin_get_by_name(GST_BIN(pipeline), "video_source");
  ctx.audio_source = gst_bin_get_by_name(GST_BIN(pipeline), "audio_source");
  GstElement *video_sink = gst_bin_get_by_name(GST_BIN(pipeline), "video_sink");
  GstElement *audio_sink = gst_bin_get_by_name(GST_BIN(pipeline), "audio_sink");
  GstElement *mux_sink = gst_bin_get_by_name(GST_BIN(pipeline), "mux_sink");
  if (ctx.video_source == NULL || ctx.audio_source == NULL || video_sink == NULL || audio_sink == NULL || mux_sink == NULL) {
    g_printerr("AirPlay GStreamer bridge: pipeline element lookup failed\n");
    if (ctx.video_source != NULL) gst_object_unref(ctx.video_source);
    if (ctx.audio_source != NULL) gst_object_unref(ctx.audio_source);
    if (video_sink != NULL) gst_object_unref(video_sink);
    if (audio_sink != NULL) gst_object_unref(audio_sink);
    if (mux_sink != NULL) gst_object_unref(mux_sink);
    g_main_loop_unref(ctx.loop);
    gst_object_unref(pipeline);
    return 1;
  }
  ctx.video_sink = video_sink;
  ctx.audio_sink = audio_sink;
  ctx.mux_sink = mux_sink;

  airplay_last_frame_init(&ctx.last_frame);
  g_mutex_init(&ctx.timing_mutex);
  ctx.last_video_pts = GST_CLOCK_TIME_NONE;
  ctx.last_audio_pts = GST_CLOCK_TIME_NONE;
  gst_app_sink_set_emit_signals(GST_APP_SINK(video_sink), FALSE);
  gst_app_sink_set_emit_signals(GST_APP_SINK(audio_sink), FALSE);
  gst_app_sink_set_emit_signals(GST_APP_SINK(mux_sink), FALSE);
  GstBus *bus = gst_element_get_bus(pipeline);
  gst_bus_add_signal_watch(bus);
  g_signal_connect(bus, "message", G_CALLBACK(on_bus_message), &ctx);
  gst_object_unref(bus);

  GstStateChangeReturn state = gst_element_set_state(pipeline, GST_STATE_PLAYING);
  if (state == GST_STATE_CHANGE_FAILURE) {
    g_printerr("AirPlay GStreamer bridge: failed to enter PLAYING state\n");
    ctx.fatal_error = TRUE;
  } else {
    g_printerr("AirPlay GStreamer bridge: pipeline is running\n");
    g_timeout_add(1, poll_samples, &ctx);
    g_timeout_add(16, repeat_last_video, &ctx);
    g_main_loop_run(ctx.loop);
  }

  gst_element_set_state(pipeline, GST_STATE_NULL);
  airplay_last_frame_clear(&ctx.last_frame);
  g_mutex_clear(&ctx.timing_mutex);
  gst_object_unref(video_sink);
  gst_object_unref(audio_sink);
  gst_object_unref(mux_sink);
  gst_object_unref(ctx.video_source);
  gst_object_unref(ctx.audio_source);
  g_main_loop_unref(ctx.loop);
  gst_object_unref(pipeline);
  return ctx.fatal_error ? 1 : 0;
}
