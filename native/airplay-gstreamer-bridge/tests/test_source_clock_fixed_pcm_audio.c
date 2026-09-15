#include "source_clock_fixed_pcm_audio.h"
#include "source_clock_protocol.h"
#include <gst/app/gstappsink.h>
#include <stdio.h>
#include <string.h>

#define MAX_SEEN 128u
#define REQUIRE(expr) do { if (!(expr)) { \
  g_printerr("E10-A failure line %d: %s\n", __LINE__, #expr); \
  ok = FALSE; goto cleanup; } } while (0)

typedef struct {
  GstClockTime pts, duration;
  gboolean discont, nonzero;
  gsize bytes;
  guint32 hash;
} Stamp;

typedef struct {
  GMutex mutex;
  GCond cond;
  guint count, nonzero, caps_errors;
  Stamp stamps[MAX_SEEN];
  gboolean stall, entered, release;
} Observation;

static void observe(Observation *o, GstBuffer *buffer) {
  Stamp s = {0};
  GstMapInfo map;
  gsize i;
  s.pts = GST_BUFFER_PTS(buffer);
  s.duration = GST_BUFFER_DURATION(buffer);
  s.discont = GST_BUFFER_FLAG_IS_SET(buffer, GST_BUFFER_FLAG_DISCONT);
  s.bytes = gst_buffer_get_size(buffer);
  s.hash = 2166136261u;
  if (gst_buffer_map(buffer, &map, GST_MAP_READ)) {
    for (i = 0; i < map.size; ++i) {
      s.nonzero |= map.data[i] != 0;
      s.hash = (s.hash ^ map.data[i]) * 16777619u;
    }
    gst_buffer_unmap(buffer, &map);
  }
  g_mutex_lock(&o->mutex);
  if (o->count < MAX_SEEN) o->stamps[o->count] = s;
  ++o->count;
  if (s.nonzero) ++o->nonzero;
  g_cond_broadcast(&o->cond);
  g_mutex_unlock(&o->mutex);
}

static GstPadProbeReturn observe_pad(GstPad *pad, GstPadProbeInfo *info, gpointer data) {
  (void)pad;
  if (GST_PAD_PROBE_INFO_BUFFER(info)) observe(data, GST_PAD_PROBE_INFO_BUFFER(info));
  return GST_PAD_PROBE_OK;
}

static void observe_decoded(GstSample *sample, gpointer data) {
  Observation *o = data;
  GstCaps *expected = gst_caps_from_string(
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved");
  GstCaps *caps = gst_sample_get_caps(sample);
  g_mutex_lock(&o->mutex);
  if (caps == NULL || !gst_caps_is_subset(caps, expected)) ++o->caps_errors;
  o->entered = TRUE;
  g_cond_broadcast(&o->cond);
  while (o->stall && !o->release) g_cond_wait(&o->cond, &o->mutex);
  g_mutex_unlock(&o->mutex);
  gst_caps_unref(expected);
  observe(o, gst_sample_get_buffer(sample));
}

/* Sync accounting avoids losing errors in GstPipeline's READY->NULL bus flush. */
static GstBusSyncReply watch_bus(GstBus *bus, GstMessage *message, gpointer data) {
  gint *errors = data;
  (void)bus;
  if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_ERROR) {
    GError *error = NULL;
    gchar *debug = NULL;
    gst_message_parse_error(message, &error, &debug);
    g_printerr("test bus error: %s\n", error->message);
    g_clear_error(&error);
    g_free(debug);
    g_atomic_int_inc(errors);
  }
  gst_message_unref(message);
  return GST_BUS_DROP;
}

static gboolean null_pipeline(GstElement *pipeline) {
  GstState current, pending;
  GstStateChangeReturn change = gst_element_set_state(pipeline, GST_STATE_NULL);
  return change != GST_STATE_CHANGE_FAILURE &&
      gst_element_get_state(pipeline, &current, &pending, GST_SECOND) != GST_STATE_CHANGE_FAILURE &&
      current == GST_STATE_NULL && pending == GST_STATE_VOID_PENDING;
}

static gboolean wait_count(Observation *o, guint count) {
  gint64 until = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  gboolean result;
  g_mutex_lock(&o->mutex);
  while (o->count < count && g_cond_wait_until(&o->cond, &o->mutex, until)) {}
  result = o->count >= count;
  g_mutex_unlock(&o->mutex);
  return result;
}

/* Real encoded 44.1kHz mono sine, not fake ADTS headers or a copied decoder. */
static GPtrArray *make_material(void) {
  GError *error = NULL;
  gint errors = 0;
  GstElement *pipeline = gst_parse_launch(
      "audiotestsrc num-buffers=12 samplesperbuffer=1024 wave=sine freq=1000 ! "
      "audio/x-raw,rate=44100,channels=1 ! audioconvert ! avenc_aac ! "
      "aacparse ! audio/mpeg,mpegversion=4,stream-format=adts ! "
      "appsink name=material sync=false async=false", &error);
  GstElement *sink = NULL;
  GstBus *bus = NULL;
  GPtrArray *frames = g_ptr_array_new_with_free_func((GDestroyNotify)gst_buffer_unref);
  guint i;
  gboolean ok = TRUE;
  REQUIRE(error == NULL && pipeline != NULL);
  bus = gst_element_get_bus(pipeline);
  gst_bus_set_sync_handler(bus, watch_bus, &errors, NULL);
  sink = gst_bin_get_by_name(GST_BIN(pipeline), "material");
  REQUIRE(sink != NULL);
  REQUIRE(gst_element_set_state(pipeline, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);
  for (i = 0; i < 12; ++i) {
    GstSample *sample = gst_app_sink_try_pull_sample(GST_APP_SINK(sink), GST_SECOND);
    REQUIRE(sample != NULL);
    g_ptr_array_add(frames, gst_buffer_copy_deep(gst_sample_get_buffer(sample)));
    gst_sample_unref(sample);
  }
cleanup:
  if (error) { g_printerr("material: %s\n", error->message); g_clear_error(&error); }
  if (pipeline && !null_pipeline(pipeline)) ok = FALSE;
  if (bus) { gst_bus_set_sync_handler(bus, NULL, NULL, NULL); gst_object_unref(bus); }
  if (sink) gst_object_unref(sink);
  if (pipeline) gst_object_unref(pipeline);
  if (!ok || g_atomic_int_get(&errors)) { g_ptr_array_unref(frames); return NULL; }
  return frames;
}

static GPtrArray *make_material_at_rate(guint rate) {
  GError *error = NULL;
  gint errors = 0;
  gchar *description = g_strdup_printf(
      "audiotestsrc num-buffers=12 samplesperbuffer=1024 wave=sine freq=1000 ! "
      "audio/x-raw,rate=%u,channels=1 ! audioconvert ! avenc_aac ! "
      "aacparse ! audio/mpeg,mpegversion=4,stream-format=adts ! "
      "appsink name=material sync=false async=false", rate);
  GstElement *pipeline = gst_parse_launch(description, &error);
  GstElement *sink = NULL;
  GstBus *bus = NULL;
  GPtrArray *frames = g_ptr_array_new_with_free_func((GDestroyNotify)gst_buffer_unref);
  guint i;
  gboolean ok = TRUE;
  g_free(description);
  REQUIRE(error == NULL && pipeline != NULL);
  bus = gst_element_get_bus(pipeline);
  gst_bus_set_sync_handler(bus, watch_bus, &errors, NULL);
  sink = gst_bin_get_by_name(GST_BIN(pipeline), "material");
  REQUIRE(sink != NULL);
  REQUIRE(gst_element_set_state(pipeline, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);
  for (i = 0; i < 12; ++i) {
    GstSample *sample = gst_app_sink_try_pull_sample(GST_APP_SINK(sink), GST_SECOND);
    REQUIRE(sample != NULL);
    g_ptr_array_add(frames, gst_buffer_copy_deep(gst_sample_get_buffer(sample)));
    gst_sample_unref(sample);
  }
cleanup:
  if (error) { g_printerr("48k material: %s\n", error->message); g_clear_error(&error); }
  if (pipeline && !null_pipeline(pipeline)) ok = FALSE;
  if (bus) { gst_bus_set_sync_handler(bus, NULL, NULL, NULL); gst_object_unref(bus); }
  if (sink) gst_object_unref(sink);
  if (pipeline) gst_object_unref(pipeline);
  if (!ok || g_atomic_int_get(&errors)) { g_ptr_array_unref(frames); return NULL; }
  return frames;
}

static GstBuffer *frame_at(GPtrArray *frames, guint i, GstClockTime pts) {
  GstBuffer *b = gst_buffer_copy_deep(g_ptr_array_index(frames, i % frames->len));
  GST_BUFFER_PTS(b) = pts;
  GST_BUFFER_DTS(b) = GST_CLOCK_TIME_NONE;
  GST_BUFFER_DURATION(b) = gst_util_uint64_scale(1024, GST_SECOND, 44100);
  GST_BUFFER_FLAG_UNSET(b, GST_BUFFER_FLAG_DISCONT);
  if (i == 0 || i == 6) GST_BUFFER_FLAG_SET(b, GST_BUFFER_FLAG_DISCONT);
  return b;
}

static GstBuffer *frame_at_rate(GPtrArray *frames, guint i, GstClockTime pts, guint rate) {
  GstBuffer *b = gst_buffer_copy_deep(g_ptr_array_index(frames, i % frames->len));
  GST_BUFFER_PTS(b) = pts;
  GST_BUFFER_DTS(b) = GST_CLOCK_TIME_NONE;
  GST_BUFFER_DURATION(b) = gst_util_uint64_scale(1024, GST_SECOND, rate);
  GST_BUFFER_FLAG_UNSET(b, GST_BUFFER_FLAG_DISCONT);
  if (i == 0 || i == 6) GST_BUFFER_FLAG_SET(b, GST_BUFFER_FLAG_DISCONT);
  return b;
}

static GstElement *make_publisher(GError **error) {
  return gst_parse_launch(
      "audiomixer name=mix force-live=true ignore-inactive-pads=true "
      "latency=100000000 min-upstream-latency=100000000 ! "
      "fakesink sync=false async=false "
      "audiotestsrc is-live=true wave=silence ! "
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! mix. "
      "appsrc name=pcm is-live=true format=time do-timestamp=false block=false emit-signals=false "
      "max-buffers=32 max-bytes=1048576 max-time=500000000 leaky-type=upstream "
      "caps=audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! "
      "queue name=q max-size-buffers=32 max-size-bytes=1048576 "
      "max-size-time=500000000 leaky=upstream ! mix.", error);
}

static const SourceClockAudioFormat supported = {
  SOURCE_CLOCK_CODEC_AAC_LC_ADTS, 44100, 1, 1024, NULL
};

static GstElement *make_output(void) {
  GstElement *pcm = gst_element_factory_make("appsrc", NULL);
  GstCaps *caps = gst_caps_from_string(
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved");
  if (pcm) {
    gst_object_ref_sink(pcm);
    g_object_set(pcm, "caps", caps, "is-live", TRUE, "format", GST_FORMAT_TIME,
        "do-timestamp", FALSE, "block", FALSE, "emit-signals", FALSE,
        "max-buffers", (guint64)32, "max-bytes", (guint64)1048576,
        "max-time", (guint64)(500 * GST_MSECOND), "leaky-type", GST_APP_LEAKY_TYPE_UPSTREAM, NULL);
  }
  gst_caps_unref(caps);
  return pcm;
}

static gboolean test_48k_audio_decode(void) {
  GPtrArray *material = make_material_at_rate(48000);
  GstElement *pcm = make_output();
  SourceClockFixedPcmAudio *audio = NULL;
  SourceClockAudioFormat format = {
    SOURCE_CLOCK_CODEC_AAC_LC_ADTS, 48000, 1, 1024, NULL
  };
  SourceClockFixedPcmAudioStats stats = {0};
  Observation decoded = {0};
  GError *error = NULL;
  guint i;
  gboolean ok = TRUE;
  g_mutex_init(&decoded.mutex); g_cond_init(&decoded.cond);
  REQUIRE(material && material->len >= 12 && pcm);
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), observe_decoded, &decoded, &error);
  REQUIRE(audio && error == NULL);
  REQUIRE(source_clock_fixed_pcm_audio_configure(audio, &format, &error));
  for (i = 0; i < 12; ++i)
    REQUIRE(source_clock_fixed_pcm_audio_push(audio,
        frame_at_rate(material, i, GST_SECOND + i * gst_util_uint64_scale(1024, GST_SECOND, 48000), 48000)) == GST_FLOW_OK);
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.decoder_created && stats.accepted_frames == 12 && stats.decoded_frames > 0 &&
      stats.forwarded_frames == stats.decoded_frames && stats.output_flow_failures == 0 &&
      stats.bus_errors == 0 && !stats.failed && !stats.forced_null && stats.stopped &&
      stats.stop_complete && stats.eos_sent && stats.eos_observed && stats.decoder_null &&
      stats.callbacks_active == 0 && stats.pending_frames == 0 &&
      stats.pending_bytes == 0 && stats.pending_duration == 0);
  REQUIRE(wait_count(&decoded, (guint)stats.decoded_frames));
  g_mutex_lock(&decoded.mutex);
  ok = decoded.nonzero == decoded.count && decoded.caps_errors == 0;
  for (i = 0; ok && i < decoded.count; ++i) {
    ok = decoded.stamps[i].duration == gst_util_uint64_scale(1024, GST_SECOND, 48000) &&
        GST_CLOCK_TIME_IS_VALID(decoded.stamps[i].pts) &&
        (i ? decoded.stamps[i].pts == decoded.stamps[i - 1].pts +
                                  decoded.stamps[i - 1].duration
           : decoded.stamps[i].pts == GST_SECOND);
  }
  if (ok && decoded.count > 0)
    ok = decoded.stamps[decoded.count - 1].pts +
             decoded.stamps[decoded.count - 1].duration >=
         GST_SECOND + 12 * gst_util_uint64_scale(1024, GST_SECOND, 48000);
  g_mutex_unlock(&decoded.mutex);
  REQUIRE(ok);
cleanup:
  g_clear_error(&error);
  if (audio && !source_clock_fixed_pcm_audio_free(audio)) g_error("48k helper retained");
  if (pcm) gst_object_unref(pcm);
  if (material) g_ptr_array_unref(material);
  g_cond_clear(&decoded.cond); g_mutex_clear(&decoded.mutex);
  g_print("E10-C1 48k decode result=%s\n", ok ? "PASS" : "FAIL");
  return ok;
}

static gboolean test_format_change_preserves_old_helper(GPtrArray *material) {
  GstElement *pcm = make_output();
  SourceClockFixedPcmAudio *audio = NULL;
  SourceClockAudioFormat changed = {
    SOURCE_CLOCK_CODEC_AAC_LC_ADTS, 48000, 1, 1024, NULL
  };
  SourceClockFixedPcmAudioStats stats = {0};
  Observation decoded = {0};
  GError *error = NULL;
  GstClockTime duration = gst_util_uint64_scale(1024, GST_SECOND, 44100);
  GstClockTime post_change_pts;
  guint i;
  gboolean saw_post_change_pts = FALSE;
  gboolean ok = TRUE;
  g_mutex_init(&decoded.mutex); g_cond_init(&decoded.cond);
  REQUIRE(material && material->len >= 4 && pcm);
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), observe_decoded, &decoded, &error);
  REQUIRE(audio && error == NULL);
  REQUIRE(source_clock_fixed_pcm_audio_configure(audio, &supported, &error));
  for (i = 0; i < 4; ++i)
    REQUIRE(source_clock_fixed_pcm_audio_push(audio, frame_at(material, i, GST_SECOND + i * duration)) == GST_FLOW_OK);
  REQUIRE(wait_count(&decoded, 1));
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.decoder_created && !stats.failed && stats.generation == 1);
  REQUIRE(!source_clock_fixed_pcm_audio_configure(audio, &changed, &error));
  REQUIRE(error && error->domain == SOURCE_CLOCK_FIXED_PCM_AUDIO_ERROR &&
      error->code == SOURCE_CLOCK_FIXED_PCM_FORMAT_CHANGE);
  g_clear_error(&error);
  post_change_pts = GST_SECOND + 4 * duration;
  REQUIRE(source_clock_fixed_pcm_audio_push(audio,
      frame_at(material, 4, post_change_pts)) == GST_FLOW_OK);
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.configured && stats.accepted_frames == 5);
  REQUIRE(!stats.failed && stats.bus_errors == 0 && stats.decoder_created && stats.decoder_null &&
      stats.forwarded_frames == stats.decoded_frames && stats.decoded_frames >= 1);
  g_mutex_lock(&decoded.mutex);
  for (i = 0; i < decoded.count && i < MAX_SEEN; ++i)
    if (decoded.stamps[i].nonzero && decoded.stamps[i].pts >= post_change_pts)
      saw_post_change_pts = TRUE;
  g_mutex_unlock(&decoded.mutex);
  REQUIRE(saw_post_change_pts);
cleanup:
  g_clear_error(&error);
  if (audio && !source_clock_fixed_pcm_audio_free(audio)) g_error("format-change helper retained");
  if (pcm) gst_object_unref(pcm);
  g_cond_clear(&decoded.cond); g_mutex_clear(&decoded.mutex);
  g_print("E10-C1 format-change result=%s\n", ok ? "PASS" : "FAIL");
  return ok;
}

static gboolean test_adts_tuple_mismatch_is_local(GPtrArray *material44,
                                                   GPtrArray *material48) {
  GstElement *pcm = make_output();
  SourceClockFixedPcmAudio *audio = NULL;
  SourceClockAudioFormat format48 = {
    SOURCE_CLOCK_CODEC_AAC_LC_ADTS, 48000, 1, 1024, NULL
  };
  SourceClockFixedPcmAudioStats stats = {0};
  GError *error = NULL;
  gboolean ok = TRUE;
  REQUIRE(material44 && material44->len >= 1 && material48 && material48->len >= 1 && pcm);
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), NULL, NULL, &error);
  REQUIRE(audio && source_clock_fixed_pcm_audio_configure(audio, &supported, &error));
  REQUIRE(source_clock_fixed_pcm_audio_push(audio,
      frame_at_rate(material48, 0, GST_SECOND, 48000)) == GST_FLOW_ERROR);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.rejected_frames == 1 && !stats.decoder_created && !stats.failed);
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  REQUIRE(source_clock_fixed_pcm_audio_free(audio));
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), NULL, NULL, &error);
  REQUIRE(audio && source_clock_fixed_pcm_audio_configure(audio, &format48, &error));
  REQUIRE(source_clock_fixed_pcm_audio_push(audio,
      frame_at(material44, 0, GST_SECOND)) == GST_FLOW_ERROR);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.rejected_frames == 1 && !stats.decoder_created && !stats.failed);
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
cleanup:
  g_clear_error(&error);
  if (audio && !source_clock_fixed_pcm_audio_free(audio)) g_error("tuple-mismatch helper retained");
  if (pcm) gst_object_unref(pcm);
  g_print("E10-C1 ADTS tuple mismatch result=%s\n", ok ? "PASS" : "FAIL");
  return ok;
}

/* Catches accepting header/metadata changes before decoder creation, and new
 * silently overriding the caller's unsafe output configuration. */
static gboolean test_rejection_and_lazy_free(GPtrArray *material) {
  GstElement *pcm = make_output();
  SourceClockFixedPcmAudio *audio = NULL;
  SourceClockFixedPcmAudioStats stats;
  GError *error = NULL;
  gboolean ok = TRUE;
  guint i;
  REQUIRE(pcm);
  g_object_set(pcm, "block", TRUE, NULL);
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), NULL, NULL, &error);
  REQUIRE(!audio && error && error->code == SOURCE_CLOCK_FIXED_PCM_INVALID_OUTPUT);
  g_clear_error(&error);
  g_object_set(pcm, "block", FALSE, NULL);
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), NULL, NULL, &error);
  REQUIRE(audio && source_clock_fixed_pcm_audio_configure(audio, &supported, &error));
  for (i = 0; i < 12; ++i) {
    GstBuffer *b = frame_at(material, 0, GST_SECOND);
    GstMapInfo map;
    REQUIRE(gst_buffer_map(b, &map, GST_MAP_WRITE));
    switch (i) {
      case 0: map.data[0] = 0; break;                         /* sync */
      case 1: map.data[1] |= 8; break;                        /* MPEG-2 */
      case 2: map.data[1] |= 2; break;                        /* layer */
      case 3: map.data[2] &= 0x3f; break;                     /* AAC Main */
      case 4: map.data[2] = (map.data[2] & 0xc3) | 12; break; /* 48kHz */
      case 5: map.data[3] = (map.data[3] & 0x3f) | 0x80; break; /* stereo */
      case 6: map.data[6] |= 1; break;                        /* >1024 */
      case 7: map.data[4] ^= 1; break;                        /* length */
      case 8: GST_BUFFER_PTS(b) = GST_CLOCK_TIME_NONE; break;
      case 9: GST_BUFFER_DURATION(b) = 0; break;
      case 10: GST_BUFFER_DURATION(b) = GST_CLOCK_TIME_NONE; break;
      case 11: GST_BUFFER_PTS(b) = GST_CLOCK_TIME_NONE - 1; break;
    }
    gst_buffer_unmap(b, &map);
    REQUIRE(source_clock_fixed_pcm_audio_push(audio, b) == GST_FLOW_ERROR);
  }
  REQUIRE(source_clock_fixed_pcm_audio_push(audio, gst_buffer_new()) == GST_FLOW_ERROR);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(!stats.decoder_created && stats.generation == 0 && stats.rejected_frames == 13);
  REQUIRE(stats.pending_frames == 0 && !stats.failed && !stats.stopped);
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.stop_complete && stats.decoder_null && !stats.decoder_created && stats.bus_errors == 0);
cleanup:
  g_clear_error(&error);
  if (audio && !source_clock_fixed_pcm_audio_free(audio)) g_error("lazy helper retained");
  if (pcm) gst_object_unref(pcm);
  g_print("E10-A rejection/lazy-free=%s\n", ok ? "PASS" : "FAIL");
  return ok;
}

/* A deliberately stalled diagnostic emulates an uncooperative streaming
 * dependency. It must not make stop/free hang or release live callback data.
 * Also catches unbounded admission while decoder consumption is stalled. */
static gboolean test_bounded_stop_and_input(GPtrArray *material) {
  GstElement *pcm = make_output();
  SourceClockFixedPcmAudio *audio = NULL;
  SourceClockFixedPcmAudioStats stats = {0};
  Observation decoded = {0};
  GError *error = NULL;
  guint i, rejected = 0;
  gint64 start, until;
  gboolean ok = TRUE, entered;
  g_mutex_init(&decoded.mutex); g_cond_init(&decoded.cond);
  decoded.stall = TRUE;
  REQUIRE(pcm);
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), observe_decoded, &decoded, &error);
  REQUIRE(audio && source_clock_fixed_pcm_audio_configure(audio, &supported, &error));
  for (i = 0; i < 4; ++i)
    REQUIRE(source_clock_fixed_pcm_audio_push(audio, frame_at(material, i,
        GST_SECOND + i * 24 * GST_MSECOND)) == GST_FLOW_OK);
  until = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  g_mutex_lock(&decoded.mutex);
  while (!decoded.entered && g_cond_wait_until(&decoded.cond, &decoded.mutex, until)) {}
  entered = decoded.entered;
  g_mutex_unlock(&decoded.mutex);
  REQUIRE(entered);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.pending_frames > 0);
  /* Incoming span overflow must not discard the already healthy window. */
  REQUIRE(source_clock_fixed_pcm_audio_push(audio, frame_at(material, 0, 2 * GST_SECOND)) == GST_FLOW_CUSTOM_ERROR);
  {
    GstBuffer *b = frame_at(material, 0, GST_SECOND + 100 * GST_MSECOND);
    GST_BUFFER_DURATION(b) = 500 * GST_MSECOND;
    REQUIRE(source_clock_fixed_pcm_audio_push(audio, b) == GST_FLOW_CUSTOM_ERROR);
    b = frame_at(material, 0, GST_SECOND + 100 * GST_MSECOND);
    GST_BUFFER_DURATION(b) = 501 * GST_MSECOND;
    REQUIRE(source_clock_fixed_pcm_audio_push(audio, b) == GST_FLOW_ERROR);
  }
  for (i = 0; i < 100; ++i) {
    GstBuffer *b = frame_at(material, i, GST_SECOND + 100 * GST_MSECOND + i * GST_MSECOND);
    GstFlowReturn flow;
    GST_BUFFER_DURATION(b) = GST_MSECOND;
    flow = source_clock_fixed_pcm_audio_push(audio, b);
    REQUIRE(flow == GST_FLOW_OK || flow == GST_FLOW_CUSTOM_ERROR);
    if (flow == GST_FLOW_CUSTOM_ERROR) ++rejected;
    source_clock_fixed_pcm_audio_get_stats(audio, &stats);
    REQUIRE(stats.pending_frames <= 32 && stats.pending_bytes <= 1048576 &&
        stats.pending_duration <= 500 * GST_MSECOND);
  }
  REQUIRE(rejected > 0 && stats.pending_frames == 32);
  start = g_get_monotonic_time();
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_TIMEOUT);
  REQUIRE(g_get_monotonic_time() - start < 2500 * G_TIME_SPAN_MILLISECOND);
  start = g_get_monotonic_time();
  REQUIRE(!source_clock_fixed_pcm_audio_free(audio)); /* SAME pointer still alive */
  REQUIRE(g_get_monotonic_time() - start < 2500 * G_TIME_SPAN_MILLISECOND);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.stopped && !stats.stop_complete && stats.callbacks_active == 1 && stats.forced_null);
cleanup:
  g_clear_error(&error);
  g_mutex_lock(&decoded.mutex);
  decoded.release = TRUE;
  g_cond_broadcast(&decoded.cond);
  g_mutex_unlock(&decoded.mutex);
  if (audio) {
    if (source_clock_fixed_pcm_audio_stop(audio) != SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE)
      g_error("stalled helper failed to finish after release");
    source_clock_fixed_pcm_audio_get_stats(audio, &stats);
    if (stats.callbacks_active || !stats.decoder_null || stats.bus_errors || stats.forwarded_frames != 0) ok = FALSE;
    if (!source_clock_fixed_pcm_audio_free(audio)) g_error("stalled helper retained");
  }
  if (pcm) gst_object_unref(pcm);
  g_cond_clear(&decoded.cond); g_mutex_clear(&decoded.mutex);
  g_print("E10-A bounded-stop/input rejected=%u bus-errors=%u result=%s\n",
      rejected, stats.bus_errors, ok ? "PASS" : "FAIL");
  return ok;
}

/* Valid ADTS header with an invalid raw AAC payload exercises decoder ERROR,
 * not the helper's format rejection. The error must survive teardown to NULL. */
static gboolean test_decoder_error_accounting(GPtrArray *material) {
  GstElement *pcm = make_output();
  SourceClockFixedPcmAudio *audio = NULL;
  SourceClockFixedPcmAudioStats stats = {0};
  GError *error = NULL;
  GstBuffer *b;
  GstMapInfo map;
  gboolean ok = TRUE;
  REQUIRE(pcm);
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), NULL, NULL, &error);
  REQUIRE(audio && source_clock_fixed_pcm_audio_configure(audio, &supported, &error));
  b = frame_at(material, 0, GST_SECOND);
  REQUIRE(gst_buffer_map(b, &map, GST_MAP_WRITE));
  memset(map.data + 7, 0, map.size - 7);
  gst_buffer_unmap(b, &map);
  REQUIRE(source_clock_fixed_pcm_audio_push(audio, b) == GST_FLOW_OK);
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.failed && stats.bus_errors > 0 && stats.last_error[0] != '\0');
  REQUIRE(stats.decoder_null && stats.callbacks_active == 0 && stats.stop_complete);
cleanup:
  g_clear_error(&error);
  if (audio && !source_clock_fixed_pcm_audio_free(audio)) g_error("errored helper retained");
  if (pcm) gst_object_unref(pcm);
  g_print("E10-A decoder-error bus-errors=%u result=%s\n", stats.bus_errors, ok ? "PASS" : "FAIL");
  return ok;
}

/* Catches eager decoder construction, silent drops, timestamp/flag rewriting,
 * generation replacement on rejection, publisher mutation and unsafe teardown.
 * Also catches helper-local EOS/last-PTS leaking into a replacement lifetime:
 * the next helper must deliver lower-PTS PCM to the SAME live publisher pad. */
static gboolean test_native_gates(GPtrArray *material) {
  GError *error = NULL;
  GstElement *publisher = NULL, *pcm = NULL, *queue = NULL, *mixer = NULL;
  GstPad *fixed_pad = NULL, *queue_src = NULL, *mixer_src = NULL, *peer = NULL;
  GstClock *clock = NULL, *after_clock = NULL;
  GstBus *bus = NULL;
  GstState state, pending;
  GstClockTime base = 0, first = 0, duration = gst_util_uint64_scale(1024, GST_SECOND, 44100);
  SourceClockFixedPcmAudio *audio = NULL;
  SourceClockFixedPcmAudioStats stats = {0};
  SourceClockAudioFormat bad;
  SourceClockFixedPcmAudioStats next_stats = {0};
  Observation decoded = {0}, next_decoded = {0}, fixed = {0}, output = {0};
  GstClockTime next_first = 0;
  gulong fixed_probe = 0, output_probe = 0;
  guint i, discontinuities = 0, generations_observed = 0, first_count = 0;
  gint errors = 0;
  gboolean ok = TRUE;
  g_mutex_init(&decoded.mutex); g_cond_init(&decoded.cond);
  g_mutex_init(&next_decoded.mutex); g_cond_init(&next_decoded.cond);
  g_mutex_init(&fixed.mutex); g_cond_init(&fixed.cond);
  g_mutex_init(&output.mutex); g_cond_init(&output.cond);
  publisher = make_publisher(&error);
  REQUIRE(publisher != NULL && error == NULL);
  bus = gst_element_get_bus(publisher);
  gst_bus_set_sync_handler(bus, watch_bus, &errors, NULL);
  pcm = gst_bin_get_by_name(GST_BIN(publisher), "pcm");
  queue = gst_bin_get_by_name(GST_BIN(publisher), "q");
  mixer = gst_bin_get_by_name(GST_BIN(publisher), "mix");
  REQUIRE(pcm && queue && mixer);
  queue_src = gst_element_get_static_pad(queue, "src");
  fixed_pad = gst_pad_get_peer(queue_src);
  mixer_src = gst_element_get_static_pad(mixer, "src");
  REQUIRE(fixed_pad && mixer_src && GST_ELEMENT(mixer)->numsinkpads == 2);
  fixed_probe = gst_pad_add_probe(fixed_pad, GST_PAD_PROBE_TYPE_BUFFER, observe_pad, &fixed, NULL);
  output_probe = gst_pad_add_probe(mixer_src, GST_PAD_PROBE_TYPE_BUFFER, observe_pad, &output, NULL);
  REQUIRE(fixed_probe && output_probe);
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), observe_decoded, &decoded, &error);
  REQUIRE(audio && error == NULL);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(!stats.configured && !stats.decoder_created && stats.generation == 0);
  REQUIRE(source_clock_fixed_pcm_audio_push(audio, frame_at(material, 0, 0)) == GST_FLOW_NOT_NEGOTIATED);
  bad = supported; bad.codec = SOURCE_CLOCK_CODEC_AAC_ELD_RAW;
  REQUIRE(!source_clock_fixed_pcm_audio_configure(audio, &bad, &error));
  REQUIRE(error && error->domain == SOURCE_CLOCK_FIXED_PCM_AUDIO_ERROR &&
      error->code == SOURCE_CLOCK_FIXED_PCM_UNSUPPORTED);
  g_clear_error(&error);
  REQUIRE(source_clock_fixed_pcm_audio_configure(audio, &supported, &error));
  REQUIRE(source_clock_fixed_pcm_audio_configure(audio, &supported, &error));
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.configured && !stats.decoder_created && stats.generation == 0);
  REQUIRE(gst_element_set_state(publisher, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);
  gst_element_get_state(publisher, &state, &pending, GST_SECOND);
  REQUIRE(state == GST_STATE_PLAYING && pending == GST_STATE_VOID_PENDING);
  clock = gst_element_get_clock(publisher); base = gst_element_get_base_time(publisher);
  REQUIRE(clock && wait_count(&output, 5));
  g_mutex_lock(&output.mutex);
  i = output.nonzero;
  g_mutex_unlock(&output.mutex);
  REQUIRE(i == 0);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(!stats.decoder_created);
  first = gst_clock_get_time(clock) - base + 100 * GST_MSECOND;
  for (i = 0; i < 12; ++i) {
    if (i == 6) {
      REQUIRE(wait_count(&decoded, 4));
      source_clock_fixed_pcm_audio_get_stats(audio, &stats);
      REQUIRE(stats.decoder_created && stats.generation == 1 && !stats.failed);
      REQUIRE(source_clock_fixed_pcm_audio_configure(audio, &supported, &error));
      bad = supported; bad.sample_rate = 48000;
      REQUIRE(!source_clock_fixed_pcm_audio_configure(audio, &bad, &error));
      REQUIRE(error && error->code == SOURCE_CLOCK_FIXED_PCM_FORMAT_CHANGE); g_clear_error(&error);
      bad = supported; bad.sample_rate = 32000;
      REQUIRE(!source_clock_fixed_pcm_audio_configure(audio, &bad, &error));
      REQUIRE(error && error->code == SOURCE_CLOCK_FIXED_PCM_UNSUPPORTED); g_clear_error(&error);
      bad = supported; bad.channels = 2;
      REQUIRE(!source_clock_fixed_pcm_audio_configure(audio, &bad, &error));
      REQUIRE(error && error->code == SOURCE_CLOCK_FIXED_PCM_UNSUPPORTED); g_clear_error(&error);
      bad = supported; bad.samples_per_frame = 960;
      REQUIRE(!source_clock_fixed_pcm_audio_configure(audio, &bad, &error));
      REQUIRE(error && error->code == SOURCE_CLOCK_FIXED_PCM_UNSUPPORTED); g_clear_error(&error);
      bad = supported; bad.codec = SOURCE_CLOCK_CODEC_ALAC_RAW;
      REQUIRE(!source_clock_fixed_pcm_audio_configure(audio, &bad, &error));
      REQUIRE(error && error->code == SOURCE_CLOCK_FIXED_PCM_UNSUPPORTED); g_clear_error(&error);
      bad = supported; bad.codec_data = gst_buffer_new_allocate(NULL, 2, NULL);
      {
        gboolean rejected = !source_clock_fixed_pcm_audio_configure(audio, &bad, &error);
        gboolean unsupported = error && error->code == SOURCE_CLOCK_FIXED_PCM_UNSUPPORTED;
        gst_buffer_unref(bad.codec_data); g_clear_error(&error);
        REQUIRE(rejected && unsupported);
      }
      source_clock_fixed_pcm_audio_get_stats(audio, &stats);
      REQUIRE(stats.generation == 1 && !stats.failed && !stats.stopped);
      {
        GstBuffer *invalid = frame_at(material, i, first + i * duration);
        GstMapInfo map;
        REQUIRE(gst_buffer_map(invalid, &map, GST_MAP_WRITE));
        map.data[2] = (map.data[2] & 0xc3) | 12; /* header silently switches to 48k */
        gst_buffer_unmap(invalid, &map);
        REQUIRE(source_clock_fixed_pcm_audio_push(audio, invalid) == GST_FLOW_ERROR);
        REQUIRE(source_clock_fixed_pcm_audio_push(audio, frame_at(material, i, first)) == GST_FLOW_ERROR);
      }
    }
    REQUIRE(source_clock_fixed_pcm_audio_push(audio, frame_at(material, i,
        first + i * duration + (i >= 6 ? 30 * GST_MSECOND : 0))) == GST_FLOW_OK);
  }
  /* Establish generation 1 at the mixer before stopping/freeing its helper. */
  {
    gint64 deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
    guint nonzero;
    g_mutex_lock(&output.mutex);
    while (output.nonzero == 0 && g_cond_wait_until(&output.cond, &output.mutex, deadline)) {}
    nonzero = output.nonzero;
    g_mutex_unlock(&output.mutex);
    REQUIRE(nonzero > 0);
  }
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  source_clock_fixed_pcm_audio_get_stats(audio, &stats);
  REQUIRE(stats.stop_complete && stats.decoder_null && stats.eos_sent && stats.eos_observed);
  REQUIRE(!stats.forced_null && !stats.failed && stats.callbacks_active == 0 && stats.bus_errors == 0);
  REQUIRE(stats.accepted_frames == 12 && stats.decoded_frames >= 12);
  REQUIRE(stats.decoded_frames == stats.forwarded_frames && stats.output_flow_failures == 0);
  REQUIRE(wait_count(&fixed, (guint)stats.decoded_frames));
  g_mutex_lock(&decoded.mutex); g_mutex_lock(&fixed.mutex);
  ok = decoded.count == fixed.count && decoded.count == stats.decoded_frames &&
      decoded.count <= MAX_SEEN && decoded.caps_errors == 0 &&
      decoded.nonzero == decoded.count && fixed.nonzero == fixed.count;
  for (i = 0; ok && i < decoded.count; ++i) {
    Stamp *a = &decoded.stamps[i], *b = &fixed.stamps[i];
    ok = a->pts == b->pts && a->duration == b->duration && a->discont == b->discont &&
        a->bytes == b->bytes && a->hash == b->hash &&
        GST_CLOCK_TIME_IS_VALID(a->pts) && GST_CLOCK_TIME_IS_VALID(a->duration) && a->duration > 0;
    if (i && a->pts < decoded.stamps[i - 1].pts) ok = FALSE;
    discontinuities += a->discont;
  }
  g_mutex_unlock(&fixed.mutex); g_mutex_unlock(&decoded.mutex);
  REQUIRE(ok && discontinuities >= 2);
  first_count = (guint)stats.decoded_frames;
  REQUIRE(source_clock_fixed_pcm_audio_push(audio, frame_at(material, 0, first)) == GST_FLOW_FLUSHING);
  REQUIRE(!source_clock_fixed_pcm_audio_configure(audio, &supported, &error)); g_clear_error(&error);
  REQUIRE(source_clock_fixed_pcm_audio_free(audio)); audio = NULL;
  peer = gst_pad_get_peer(queue_src);
  after_clock = gst_element_get_clock(publisher);
  gst_element_get_state(publisher, &state, &pending, 0);
  REQUIRE(peer == fixed_pad && gst_pad_is_linked(peer) && GST_OBJECT_PARENT(peer) == GST_OBJECT(mixer));
  REQUIRE(GST_ELEMENT(mixer)->numsinkpads == 2 && after_clock == clock &&
      gst_element_get_base_time(publisher) == base && state == GST_STATE_PLAYING && pending == GST_STATE_VOID_PENDING);
  {
    GstElement *same = gst_bin_get_by_name(GST_BIN(publisher), "pcm");
    gboolean identity = same == pcm && GST_OBJECT_PARENT(same) == GST_OBJECT(publisher);
    if (same) gst_object_unref(same);
    REQUIRE(identity);
  }
  {
    gint64 deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
    guint nonzero;
    g_mutex_lock(&output.mutex);
    while (output.nonzero == 0 && g_cond_wait_until(&output.cond, &output.mutex, deadline)) {}
    nonzero = output.nonzero;
    g_mutex_unlock(&output.mutex);
    REQUIRE(nonzero > 0);
  }
  ++generations_observed;
  {
    guint64 buffers, bytes, time;
    guint qbuffers, qbytes;
    gint leaky, qleaky;
    gboolean live, block, timestamp;
    gint format;
    g_object_get(pcm, "max-buffers", &buffers, "max-bytes", &bytes, "max-time", &time,
        "leaky-type", &leaky, "is-live", &live, "block", &block,
        "do-timestamp", &timestamp, "format", &format, NULL);
    REQUIRE(buffers == 32 && bytes == 1048576 && time == 500 * GST_MSECOND &&
        leaky == GST_APP_LEAKY_TYPE_UPSTREAM && live && !block && !timestamp && format == GST_FORMAT_TIME);
    g_object_get(queue, "max-size-buffers", &qbuffers, "max-size-bytes", &qbytes,
        "max-size-time", &time, "leaky", &qleaky, NULL);
    REQUIRE(qbuffers == 32 && qbytes == 1048576 && time == 500 * GST_MSECOND && qleaky == 1);
  }
  /* A new helper object is a new test generation, even if the allocator reuses
   * its address. The production stats.generation is local to EACH helper. Keep
   * the fixed-pad probe and its cumulative observations across both lifetimes. */
  audio = source_clock_fixed_pcm_audio_new(GST_APP_SRC(pcm), observe_decoded, &next_decoded, &error);
  REQUIRE(audio && error == NULL);
  source_clock_fixed_pcm_audio_get_stats(audio, &next_stats);
  REQUIRE(!next_stats.configured && !next_stats.decoder_created && next_stats.generation == 0 &&
      next_stats.accepted_frames == 0 && next_stats.decoded_frames == 0);
  REQUIRE(source_clock_fixed_pcm_audio_configure(audio, &supported, &error));
  next_first = first / 2;
  REQUIRE(next_first < first);
  for (i = 0; i < 12; ++i) {
    GstBuffer *b = frame_at(material, i, next_first + i * duration);
    /* Only the first input frame marks this new generation's discontinuity. */
    if (i) GST_BUFFER_FLAG_UNSET(b, GST_BUFFER_FLAG_DISCONT);
    REQUIRE(source_clock_fixed_pcm_audio_push(audio, b) == GST_FLOW_OK);
  }
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  REQUIRE(source_clock_fixed_pcm_audio_stop(audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
  source_clock_fixed_pcm_audio_get_stats(audio, &next_stats);
  REQUIRE(next_stats.decoder_created && next_stats.generation == 1 && next_stats.stopped &&
      next_stats.stop_complete && next_stats.decoder_null && next_stats.eos_sent && next_stats.eos_observed);
  REQUIRE(!next_stats.forced_null && !next_stats.failed && next_stats.callbacks_active == 0 &&
      next_stats.bus_errors == 0 && next_stats.pending_frames == 0 &&
      next_stats.pending_bytes == 0 && next_stats.pending_duration == 0);
  /* AAC/resampling can split output buffers: no encoded/PCM count equality. */
  REQUIRE(next_stats.accepted_frames == 12 && next_stats.decoded_frames > 0 &&
      next_stats.decoded_frames <= MAX_SEEN - first_count);
  REQUIRE(next_stats.decoded_frames == next_stats.forwarded_frames && next_stats.output_flow_failures == 0);
  REQUIRE(wait_count(&fixed, first_count + (guint)next_stats.decoded_frames));
  g_mutex_lock(&decoded.mutex); g_mutex_lock(&next_decoded.mutex); g_mutex_lock(&fixed.mutex);
  ok = decoded.count == first_count && next_decoded.count == next_stats.decoded_frames &&
      fixed.count == first_count + next_decoded.count && next_decoded.caps_errors == 0 &&
      next_decoded.nonzero == next_decoded.count && fixed.nonzero == fixed.count &&
      next_decoded.stamps[0].pts == next_first && next_decoded.stamps[0].discont &&
      fixed.stamps[first_count].pts < fixed.stamps[first_count - 1].pts;
  for (i = 0; ok && i < next_decoded.count; ++i) {
    Stamp *a = &next_decoded.stamps[i], *b = &fixed.stamps[first_count + i];
    /* Compare normalized PCM before forwarding against actual fixed-pad
     * delivery, not appsrc admission/forward-attempt counters. */
    ok = a->pts == b->pts && a->duration == b->duration && a->discont == b->discont &&
        a->bytes == b->bytes && a->hash == b->hash && a->nonzero && b->nonzero &&
        GST_CLOCK_TIME_IS_VALID(a->pts) && GST_CLOCK_TIME_IS_VALID(a->duration) && a->duration > 0;
    if (i && a->pts < next_decoded.stamps[i - 1].pts) ok = FALSE;
  }
  g_mutex_unlock(&fixed.mutex); g_mutex_unlock(&next_decoded.mutex); g_mutex_unlock(&decoded.mutex);
  REQUIRE(ok);
  REQUIRE(source_clock_fixed_pcm_audio_push(audio, frame_at(material, 0, next_first)) == GST_FLOW_FLUSHING);
  REQUIRE(source_clock_fixed_pcm_audio_free(audio)); audio = NULL;
  gst_object_unref(peer); peer = gst_pad_get_peer(queue_src);
  gst_object_unref(after_clock); after_clock = gst_element_get_clock(publisher);
  gst_element_get_state(publisher, &state, &pending, 0);
  REQUIRE(peer == fixed_pad && gst_pad_is_linked(peer) && GST_OBJECT_PARENT(peer) == GST_OBJECT(mixer));
  REQUIRE(GST_ELEMENT(mixer)->numsinkpads == 2 && after_clock == clock &&
      gst_element_get_base_time(publisher) == base && state == GST_STATE_PLAYING && pending == GST_STATE_VOID_PENDING);
  {
    GstElement *same_pcm = gst_bin_get_by_name(GST_BIN(publisher), "pcm");
    GstElement *same_queue = gst_bin_get_by_name(GST_BIN(publisher), "q");
    GstPad *pcm_src = gst_element_get_static_pad(pcm, "src");
    GstPad *queue_sink = gst_element_get_static_pad(queue, "sink");
    GstPad *pcm_peer = pcm_src ? gst_pad_get_peer(pcm_src) : NULL;
    gboolean identity = same_pcm == pcm && same_queue == queue &&
        GST_OBJECT_PARENT(pcm) == GST_OBJECT(publisher) &&
        GST_OBJECT_PARENT(queue) == GST_OBJECT(publisher) &&
        queue_sink && pcm_peer == queue_sink;
    if (pcm_peer) gst_object_unref(pcm_peer);
    if (queue_sink) gst_object_unref(queue_sink);
    if (pcm_src) gst_object_unref(pcm_src);
    if (same_queue) gst_object_unref(same_queue);
    if (same_pcm) gst_object_unref(same_pcm);
    REQUIRE(identity);
  }
  ++generations_observed;
  g_print("E10-B2-A helper generations observed=%u required=2\n", generations_observed);
  REQUIRE(generations_observed == 2);
cleanup:
  if (error) { g_printerr("native gates: %s\n", error->message); g_clear_error(&error); }
  if (audio && !source_clock_fixed_pcm_audio_free(audio)) {
    /* Do not clear observer mutexes while a retained helper may still use them. */
    g_error("helper retained during cleanup");
  }
  if (publisher && !null_pipeline(publisher)) g_error("publisher failed to quiesce");
  if (fixed_probe) gst_pad_remove_probe(fixed_pad, fixed_probe);
  if (output_probe) gst_pad_remove_probe(mixer_src, output_probe);
  if (bus) { gst_bus_set_sync_handler(bus, NULL, NULL, NULL); gst_object_unref(bus); }
  if (g_atomic_int_get(&errors)) ok = FALSE;
  if (after_clock) gst_object_unref(after_clock);
  if (clock) gst_object_unref(clock);
  if (peer) gst_object_unref(peer);
  if (fixed_pad) gst_object_unref(fixed_pad);
  if (queue_src) gst_object_unref(queue_src);
  if (mixer_src) gst_object_unref(mixer_src);
  if (pcm) gst_object_unref(pcm);
  if (queue) gst_object_unref(queue);
  if (mixer) gst_object_unref(mixer);
  if (publisher) gst_object_unref(publisher);
  g_cond_clear(&decoded.cond); g_mutex_clear(&decoded.mutex);
  g_cond_clear(&next_decoded.cond); g_mutex_clear(&next_decoded.mutex);
  g_cond_clear(&fixed.cond); g_mutex_clear(&fixed.mutex);
  g_cond_clear(&output.cond); g_mutex_clear(&output.mutex);
  g_print("E10-A native-gates decoded=%" G_GUINT64_FORMAT " forwarded=%" G_GUINT64_FORMAT
      " discont=%u bus-errors=%u/%d result=%s\n", stats.decoded_frames,
      stats.forwarded_frames, discontinuities, stats.bus_errors, errors, ok ? "PASS" : "FAIL");
  g_print("E10-B2-A generation-swap first-pts=%" G_GUINT64_FORMAT " next-pts=%" G_GUINT64_FORMAT
      " next-decoded=%" G_GUINT64_FORMAT " next-forwarded=%" G_GUINT64_FORMAT
      " next-bus-errors=%u callbacks=%u decoder-null=%d result=%s\n", first, next_first,
      next_stats.decoded_frames, next_stats.forwarded_frames, next_stats.bus_errors,
      next_stats.callbacks_active, next_stats.decoder_null, ok ? "PASS" : "FAIL");
  return ok;
}

int main(int argc, char **argv) {
  GPtrArray *material;
  gboolean ok = TRUE;
  gst_init(&argc, &argv);
  material = make_material();
  if (!material) return 1;
  ok = test_48k_audio_decode() && ok;
  ok = test_format_change_preserves_old_helper(material) && ok;
  {
    GPtrArray *material48 = make_material_at_rate(48000);
    ok = test_adts_tuple_mismatch_is_local(material, material48) && ok;
    if (material48) g_ptr_array_unref(material48);
  }
  ok = test_rejection_and_lazy_free(material) && ok;
  ok = test_native_gates(material) && ok;
  ok = test_bounded_stop_and_input(material) && ok;
  ok = test_decoder_error_accounting(material) && ok;
  g_ptr_array_unref(material);
  gst_deinit();
  return ok ? 0 : 1;
}
