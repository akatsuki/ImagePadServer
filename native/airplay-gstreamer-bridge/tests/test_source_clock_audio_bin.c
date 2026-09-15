#include "source_clock_audio_bin.h"
#include "source_clock_protocol.h"
#include <gst/app/gstappsink.h>
#include <stdio.h>
#include <string.h>

/* Always evaluated, including Release/NDEBUG. */
#define CHECK(expr) do { if (!(expr)) { \
  fprintf(stderr, "%s:%d: FAIL: %s\n", __FILE__, __LINE__, #expr); \
  return FALSE; } } while (0)

static void released(gpointer data, GstMiniObject *object) {
  (void)object;
  ++*(guint *)data;
}

static GstBuffer *frame(gsize bytes, GstClockTime pts, GstClockTime duration,
                        guint *freed) {
  GstBuffer *buffer = gst_buffer_new_allocate(NULL, bytes, NULL);
  GST_BUFFER_PTS(buffer) = pts;
  GST_BUFFER_DURATION(buffer) = duration;
  if (freed != NULL) gst_mini_object_weak_ref(GST_MINI_OBJECT(buffer), released, freed);
  return buffer;
}

static SourceClockAudioFormat format(void) {
  SourceClockAudioFormat value = { SOURCE_CLOCK_CODEC_AAC_LC_ADTS, 48000, 2, 1024, NULL };
  return value;
}

static SourceClockAudioFormat eld_format(void) {
  SourceClockAudioFormat value = { SOURCE_CLOCK_CODEC_AAC_ELD_RAW, 44100, 2, 480, NULL };
  return value;
}

static gboolean lifecycle_snapshot(void) {
  GstElement *parent = gst_pipeline_new("b1-parent");
  GstElement *mixer = gst_bin_new("b1-placeholder");
  GMainContext *context = g_main_context_new();
  SourceClockAudioBin *audio;
  SourceClockAudioFormat input = format(), snapshot = {0};
  SourceClockAudioBinStats stats;
  GError *error = NULL;
  guint freed = 0;
  guint8 bytes[] = {0x11, 0x90}, actual[2] = {0};
  gboolean ok;
  GstFlowReturn flow;
  gst_bin_add(GST_BIN(parent), mixer);
  audio = source_clock_audio_bin_new(parent, mixer, context);
  CHECK(audio != NULL);
  source_clock_audio_bin_get_stats(audio, &stats);
  CHECK(!stats.configured && !stats.stopped && stats.pending_frames == 0);
  CHECK(GST_BIN_NUMCHILDREN(parent) == 1 && GST_BIN_NUMCHILDREN(mixer) == 0);
  input.codec_data = frame(2, 0, 0, &freed);
  gst_buffer_fill(input.codec_data, 0, bytes, 2);
  ok = source_clock_audio_bin_configure(audio, &input, &error);
  CHECK(ok && error == NULL);
  ok = source_clock_audio_bin_configure(audio, &input, &error);
  CHECK(ok && error == NULL); /* Same configure must not flush pending later. */
  bytes[0] = 0xff;
  gst_buffer_fill(input.codec_data, 0, bytes, 2);
  gst_buffer_unref(input.codec_data);
  input.codec_data = NULL;
  input.sample_rate = 8000;
  CHECK(freed == 1); /* Input codec_data was copied, not retained. */
  ok = source_clock_audio_bin_get_format(audio, &snapshot);
  CHECK(ok && snapshot.sample_rate == 48000 && snapshot.channels == 2);
  CHECK(snapshot.codec == SOURCE_CLOCK_CODEC_AAC_LC_ADTS && snapshot.samples_per_frame == 1024);
  gst_buffer_extract(snapshot.codec_data, 0, actual, 2);
  CHECK(actual[0] == 0x11 && actual[1] == 0x90);
  gst_buffer_fill(snapshot.codec_data, 0, bytes, 2);
  source_clock_audio_format_clear(&snapshot);
  source_clock_audio_format_clear(&snapshot);
  ok = source_clock_audio_bin_get_format(audio, &snapshot);
  CHECK(ok);
  gst_buffer_extract(snapshot.codec_data, 0, actual, 2);
  CHECK(actual[0] == 0x11); /* Returned snapshot also cannot mutate storage. */
  source_clock_audio_format_clear(&snapshot);
  flow = source_clock_audio_bin_push(audio, frame(10, 0, GST_MSECOND, &freed));
  CHECK(flow == GST_FLOW_OK && freed == 1);
  CHECK(GST_BIN_NUMCHILDREN(parent) == 1 && GST_BIN_NUMCHILDREN(mixer) == 0);
  CHECK(g_main_context_pending(context)); /* B2: deferred, not yet constructed. */
  source_clock_audio_bin_stop(audio);
  source_clock_audio_bin_stop(audio);
  CHECK(!g_main_context_pending(context));
  CHECK(freed == 2);
  flow = source_clock_audio_bin_push(audio, frame(10, 0, GST_MSECOND, &freed));
  CHECK(flow == GST_FLOW_FLUSHING && freed == 3);
  ok = source_clock_audio_bin_configure(audio, &input, &error);
  CHECK(!ok && error != NULL && error->code == SOURCE_CLOCK_AUDIO_BIN_STOPPED);
  g_clear_error(&error);
  source_clock_audio_bin_get_stats(audio, &stats);
  CHECK(stats.stopped && !stats.configured && stats.pending_frames == 0 && stats.pending_bytes == 0);
  source_clock_audio_bin_free(audio);
  audio = NULL;
  source_clock_audio_bin_free(audio); /* NULL is safe; stale pointer is not valid. */
  g_main_context_unref(context);
  gst_object_unref(parent);
  return TRUE;
}

static gboolean validation(void) {
  SourceClockAudioBin *audio = source_clock_audio_bin_new(NULL, NULL, NULL);
  SourceClockAudioFormat input = format(), bad, snapshot = {0};
  SourceClockAudioBinStats stats;
  GError *error = NULL;
  guint i, freed = 0;
  gboolean ok;
  GstFlowReturn flow;
  CHECK(audio != NULL);
  flow = source_clock_audio_bin_push(audio, frame(10, 0, GST_MSECOND, &freed));
  CHECK(flow == GST_FLOW_NOT_NEGOTIATED && freed == 1);
  for (i = 0; i < 10; ++i) {
    bad = input;
    if (i == 0) bad.codec = 0xff;
    if (i == 1) bad.codec = SOURCE_CLOCK_CODEC_ALAC_RAW;
    if (i == 2) bad.sample_rate = 0;
    if (i == 3) bad.sample_rate = 12345;
    if (i == 4) bad.channels = 0;
    if (i == 5) bad.samples_per_frame = 0;
    if (i == 6) bad.samples_per_frame = 960;
    if (i == 7) bad.channels = 3;
    if (i == 8) bad.codec_data = frame(0, 0, 0, NULL);
    if (i == 9) bad.codec_data = frame(1025, 0, 0, NULL);
    ok = source_clock_audio_bin_configure(audio, &bad, &error);
    CHECK(!ok && error != NULL);
    CHECK(error->code == (i < 2 ? SOURCE_CLOCK_AUDIO_BIN_UNSUPPORTED : SOURCE_CLOCK_AUDIO_BIN_INVALID_FORMAT));
    g_clear_error(&error);
    if (bad.codec_data != NULL) gst_buffer_unref(bad.codec_data);
  }
  ok = source_clock_audio_bin_configure(audio, NULL, &error);
  CHECK(!ok && error != NULL);
  g_clear_error(&error);
  ok = source_clock_audio_bin_configure(audio, &input, &error);
  CHECK(ok);
  flow = source_clock_audio_bin_push(audio, frame(10, 0, GST_MSECOND, &freed));
  CHECK(flow == GST_FLOW_OK);
  ok = source_clock_audio_bin_configure(audio, &input, &error);
  CHECK(ok);
  bad = input;
  bad.sample_rate = 44100;
  ok = source_clock_audio_bin_configure(audio, &bad, &error);
  CHECK(!ok && error != NULL && error->code == SOURCE_CLOCK_AUDIO_BIN_FORMAT_CHANGE);
  g_clear_error(&error);
  ok = source_clock_audio_bin_get_format(audio, &snapshot);
  CHECK(ok && snapshot.sample_rate == 48000);
  source_clock_audio_format_clear(&snapshot);
  source_clock_audio_bin_get_stats(audio, &stats);
  CHECK(stats.pending_frames == 1 && stats.pending_bytes == 10);
  source_clock_audio_bin_free(audio); /* free also stops and releases pending. */
  CHECK(freed == 2);
  return TRUE;
}

static gboolean aac_eld_validation(void) {
  SourceClockAudioBin *audio = source_clock_audio_bin_new(NULL, NULL, NULL);
  SourceClockAudioFormat input = eld_format(), snapshot = {0};
  SourceClockAudioBinStats stats = {0};
  GstClockTime duration = gst_util_uint64_scale(480, GST_SECOND, 44100);
  guint freed = 0;
  CHECK(audio != NULL);
  CHECK(source_clock_audio_bin_configure(audio, &input, NULL));
  CHECK(source_clock_audio_bin_get_format(audio, &snapshot));
  CHECK(snapshot.codec == SOURCE_CLOCK_CODEC_AAC_ELD_RAW);
  CHECK(snapshot.sample_rate == 44100 && snapshot.channels == 2);
  CHECK(snapshot.samples_per_frame == 480 && snapshot.codec_data == NULL);
  source_clock_audio_format_clear(&snapshot);
  CHECK(source_clock_audio_bin_push(audio, frame(32, GST_SECOND, duration, &freed)) == GST_FLOW_OK);
  source_clock_audio_bin_get_stats(audio, &stats);
  CHECK(stats.pending_frames == 1 && stats.pending_duration == duration);
  CHECK(source_clock_audio_bin_free(audio));
  CHECK(freed == 1);
  return TRUE;
}

/* Removing any one bound would accept the last buffer and retain its ownership. */
static gboolean bounds(void) {
  guint mode;
  for (mode = 0; mode < 4; ++mode) {
    SourceClockAudioBin *audio = source_clock_audio_bin_new(NULL, NULL, NULL);
    SourceClockAudioFormat input = format();
    SourceClockAudioBinStats stats;
    guint count = mode == 0 ? 32 : mode == 1 ? 2 : 1;
    gsize bytes = mode == 1 ? 524288 : 10;
    GstClockTime duration = mode == 2 ? 500 * GST_MSECOND : GST_MSECOND;
    guint i, freed = 0;
    gboolean ok = source_clock_audio_bin_configure(audio, &input, NULL);
    GstFlowReturn flow;
    CHECK(ok);
    for (i = 0; i < count; ++i) {
      flow = source_clock_audio_bin_push(audio, frame(bytes, i * duration, duration, &freed));
      CHECK(flow == GST_FLOW_OK);
    }
    flow = source_clock_audio_bin_push(audio,
        frame(bytes, mode == 3 ? GST_SECOND : count * duration, duration, &freed));
    CHECK(flow == GST_FLOW_CUSTOM_ERROR && freed == 1);
    source_clock_audio_bin_get_stats(audio, &stats);
    CHECK(stats.pending_frames == count && stats.pending_bytes == count * bytes);
    CHECK(stats.pending_duration == count * duration && stats.rejected_frames == 1);
    CHECK(stats.oldest_pts == 0);
    source_clock_audio_bin_free(audio);
    CHECK(freed == count + 1);
  }
  return TRUE;
}

static gboolean invalid_buffers(void) {
  SourceClockAudioBin *audio = source_clock_audio_bin_new(NULL, NULL, NULL);
  SourceClockAudioFormat input = format();
  SourceClockAudioBinStats stats;
  guint freed = 0, i;
  gboolean ok = source_clock_audio_bin_configure(audio, &input, NULL);
  GstFlowReturn flow;
  CHECK(ok);
  for (i = 0; i < 6; ++i) {
    GstBuffer *buffer = frame(i == 0 ? 0 : i == 1 ? 1048577 : 10,
        i == 2 ? GST_CLOCK_TIME_NONE : 0,
        i == 3 ? GST_CLOCK_TIME_NONE : i == 4 ? 0 : i == 5 ? 501 * GST_MSECOND : GST_MSECOND,
        &freed);
    flow = source_clock_audio_bin_push(audio, buffer);
    CHECK(flow != GST_FLOW_OK && freed == i + 1);
  }
  source_clock_audio_bin_get_stats(audio, &stats);
  CHECK(stats.pending_frames == 0 && stats.pending_bytes == 0 && stats.rejected_frames == 6);
  source_clock_audio_bin_free(audio);
  return TRUE;
}

static void object_released(gpointer data, GObject *object) {
  (void)object;
  ++*(guint *)data;
}

static gboolean references_and_timestamps(void) {
  GstElement *parent = gst_pipeline_new("owned-parent");
  GstElement *mixer = gst_bin_new("owned-placeholder");
  SourceClockAudioBin *audio;
  SourceClockAudioFormat input = format(), snapshot = {0};
  SourceClockAudioBinStats stats;
  GstBuffer *buffer;
  guint objects_freed = 0, buffers_freed = 0, snapshots_freed = 0;
  gboolean ok;
  GstFlowReturn flow;
  gst_object_ref_sink(parent);
  gst_object_ref_sink(mixer);
  g_object_weak_ref(G_OBJECT(parent), object_released, &objects_freed);
  g_object_weak_ref(G_OBJECT(mixer), object_released, &objects_freed);
  audio = source_clock_audio_bin_new(parent, mixer, NULL);
  gst_object_unref(parent);
  gst_object_unref(mixer);
  CHECK(objects_freed == 0);
  input.codec_data = frame(2, 0, 0, NULL);
  gst_buffer_memset(input.codec_data, 0, 0, 2);
  ok = source_clock_audio_bin_configure(audio, &input, NULL);
  CHECK(ok);
  gst_buffer_unref(input.codec_data);
  ok = source_clock_audio_bin_get_format(audio, &snapshot);
  CHECK(ok);
  gst_mini_object_weak_ref(GST_MINI_OBJECT(snapshot.codec_data), released, &snapshots_freed);
  buffer = frame(10, 10 * GST_MSECOND, GST_MSECOND, &buffers_freed);
  flow = source_clock_audio_bin_push(audio, gst_buffer_ref(buffer));
  CHECK(flow == GST_FLOW_OK);
  flow = source_clock_audio_bin_push(audio, buffer);
  CHECK(flow == GST_FLOW_OK); /* Each push consumes exactly one ref, not one object. */
  flow = source_clock_audio_bin_push(audio, frame(10, 0, GST_MSECOND, &buffers_freed));
  CHECK(flow == GST_FLOW_ERROR && buffers_freed == 1); /* Regressing PTS. */
  flow = source_clock_audio_bin_push(audio,
      frame(10, GST_CLOCK_TIME_NONE - 10, 11, &buffers_freed));
  CHECK(flow == GST_FLOW_ERROR && buffers_freed == 2); /* Addition overflow. */
  source_clock_audio_bin_get_stats(audio, &stats);
  CHECK(stats.pending_frames == 2 && stats.pending_bytes == 20 && stats.rejected_frames == 2);
  source_clock_audio_bin_free(audio);
  CHECK(objects_freed == 2 && buffers_freed == 3 && snapshots_freed == 0);
  source_clock_audio_format_clear(&snapshot); /* Snapshot outlives bin. */
  CHECK(snapshots_freed == 1);
  return TRUE;
}

typedef struct {
  GMainContext *context;
  gint pcm_buffers, nonzero_buffers, bad_caps, wrong_context, added, removed;
} PCMObservation;

static GstPadProbeReturn observe_pcm(GstPad *pad, GstPadProbeInfo *info, gpointer data) {
  PCMObservation *seen = data;
  GstCaps *caps = gst_pad_get_current_caps(pad);
  GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  GstMapInfo map;
  gint rate = 0, channels = 0;
  const GstStructure *s = caps == NULL ? NULL : gst_caps_get_structure(caps, 0);
  if (s == NULL || !gst_structure_has_name(s, "audio/x-raw") ||
      g_strcmp0(gst_structure_get_string(s, "format"), "S16LE") != 0 ||
      !gst_structure_get_int(s, "rate", &rate) || rate != 48000 ||
      !gst_structure_get_int(s, "channels", &channels) || channels != 2)
    g_atomic_int_inc(&seen->bad_caps);
  if (caps != NULL) gst_caps_unref(caps);
  if (buffer != NULL && gst_buffer_map(buffer, &map, GST_MAP_READ)) {
    gsize i;
    g_atomic_int_inc(&seen->pcm_buffers);
    for (i = 0; i < map.size; ++i) if (map.data[i] != 0) {
      g_atomic_int_inc(&seen->nonzero_buffers);
      break;
    }
    gst_buffer_unmap(buffer, &map);
  }
  return GST_PAD_PROBE_OK;
}

static void observe_real_pad(GstElement *mixer, GstPad *pad, gpointer data) {
  (void)mixer;
  if (GST_PAD_DIRECTION(pad) == GST_PAD_SINK)
    gst_pad_add_probe(pad, GST_PAD_PROBE_TYPE_BUFFER, observe_pcm, data, NULL);
}

static void observe_added(GstBin *parent, GstElement *child, gpointer data) {
  PCMObservation *seen = data;
  (void)parent; (void)child;
  g_atomic_int_inc(&seen->added);
  if (!g_main_context_is_owner(seen->context)) g_atomic_int_inc(&seen->wrong_context);
}

static void observe_removed(gpointer data, GObject *object) {
  PCMObservation *seen = data;
  (void)object;
  g_atomic_int_inc(&seen->removed);
}

static void observe_branch_lifetime(GstBin *parent, GstElement *child, gpointer data) {
  observe_added(parent, child, data);
  g_object_weak_ref(G_OBJECT(child), observe_removed, data);
}

static gboolean check_branch_settings(GstElement *parent, GstClockTime base) {
  GstIterator *it = gst_bin_iterate_recurse(GST_BIN(parent));
  GValue item = G_VALUE_INIT;
  gboolean ok = TRUE;
  guint inputs = 0;
  while (gst_iterator_next(it, &item) == GST_ITERATOR_OK) {
    GstElement *child = g_value_get_object(&item);
    GstElementFactory *factory = gst_element_get_factory(child);
    if (factory != NULL && g_strcmp0(gst_plugin_feature_get_name(GST_PLUGIN_FEATURE(factory)), "appsrc") == 0) {
      gboolean live = FALSE, timestamp = TRUE, block = TRUE;
      gint stream_format = 0, leaky = 0, rate = 0, channels = 0;
      guint64 buffers = 0, bytes = 0, time = 0;
      GstCaps *caps = NULL;
      GstState state;
      const GstStructure *s;
      ++inputs;
      g_object_get(child, "is-live", &live, "do-timestamp", &timestamp, "block", &block,
          "format", &stream_format, "caps", &caps, "max-buffers", &buffers,
          "max-bytes", &bytes, "max-time", &time, "leaky-type", &leaky, NULL);
      s = caps == NULL ? NULL : gst_caps_get_structure(caps, 0);
      gst_element_get_state(child, &state, NULL, 0);
      ok = ok && live && !timestamp && !block && stream_format == GST_FORMAT_TIME &&
          buffers == 32 && bytes == 1048576 && time == 500 * GST_MSECOND && leaky == 1 &&
          state == GST_STATE_PLAYING && gst_element_get_base_time(child) == base &&
          s != NULL && g_strcmp0(gst_structure_get_string(s, "stream-format"), "adts") == 0 &&
          gst_structure_get_int(s, "rate", &rate) && rate == 44100 &&
          gst_structure_get_int(s, "channels", &channels) && channels == 1;
      if (caps != NULL) gst_caps_unref(caps);
    }
    g_value_reset(&item);
  }
  g_value_unset(&item);
  gst_iterator_free(it);
  return ok && inputs == 1;
}

/* State-change messages retain their source object. Drain the test pipeline's
 * unobserved bus before asserting final object release. */
static void drain_bus(GstElement *pipeline) {
  GstBus *bus = gst_element_get_bus(pipeline);
  GstMessage *message;
  while ((message = gst_bus_pop(bus)) != NULL) gst_message_unref(message);
  gst_object_unref(bus);
}

static GstElement *find_appsrc(GstElement *parent) {
  GstIterator *it = gst_bin_iterate_recurse(GST_BIN(parent));
  GValue item = G_VALUE_INIT;
  GstElement *found = NULL;
  while (found == NULL && gst_iterator_next(it, &item) == GST_ITERATOR_OK) {
    GstElement *child = g_value_get_object(&item);
    GstElementFactory *factory = gst_element_get_factory(child);
    if (factory != NULL &&
        g_strcmp0(gst_plugin_feature_get_name(GST_PLUGIN_FEATURE(factory)), "appsrc") == 0)
      found = gst_object_ref(child);
    g_value_reset(&item);
  }
  g_value_unset(&item);
  gst_iterator_free(it);
  return found;
}

static gboolean aac_eld_branch_caps(void) {
  GstElement *parent = gst_parse_launch(
      "audiotestsrc is-live=true wave=silence ! "
      "audio/x-raw,format=S16LE,rate=48000,channels=2 ! "
      "audiomixer name=mix force-live=true ignore-inactive-pads=true ! "
      "fakesink sync=false async=false", NULL);
  GstElement *mixer = gst_bin_get_by_name(GST_BIN(parent), "mix");
  GMainContext *context = g_main_context_new();
  SourceClockAudioBin *audio = source_clock_audio_bin_new(parent, mixer, context);
  SourceClockAudioFormat input = eld_format();
  SourceClockAudioBinStats stats = {0};
  GstElement *appsrc;
  GstCaps *caps = NULL;
  const GstStructure *structure;
  const GValue *codec_data_value;
  GstBuffer *codec_data;
  guint8 actual[4] = {0};
  gint rate = 0, channels = 0;
  gint64 deadline;
  CHECK(parent != NULL && mixer != NULL && audio != NULL);
  CHECK(gst_element_set_state(parent, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);
  CHECK(source_clock_audio_bin_configure(audio, &input, NULL));
  deadline = g_get_monotonic_time() + G_TIME_SPAN_SECOND;
  do {
    while (g_main_context_iteration(context, FALSE)) {}
    source_clock_audio_bin_get_stats(audio, &stats);
    if (stats.ready || stats.creation_failed) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  CHECK(stats.ready && !stats.creation_failed);
  appsrc = find_appsrc(parent);
  CHECK(appsrc != NULL);
  caps = gst_app_src_get_caps(GST_APP_SRC(appsrc));
  CHECK(caps != NULL);
  structure = gst_caps_get_structure(caps, 0);
  CHECK(g_strcmp0(gst_structure_get_string(structure, "stream-format"), "raw") == 0);
  CHECK(gst_structure_get_int(structure, "rate", &rate) && rate == 44100);
  CHECK(gst_structure_get_int(structure, "channels", &channels) && channels == 2);
  codec_data_value = gst_structure_get_value(structure, "codec_data");
  CHECK(codec_data_value != NULL && GST_VALUE_HOLDS_BUFFER(codec_data_value));
  codec_data = gst_value_get_buffer(codec_data_value);
  CHECK(codec_data != NULL && gst_buffer_get_size(codec_data) == 4);
  CHECK(gst_buffer_extract(codec_data, 0, actual, sizeof(actual)) == sizeof(actual));
  CHECK(actual[0] == 0xf8 && actual[1] == 0xe8 && actual[2] == 0x50 && actual[3] == 0x00);
  gst_caps_unref(caps);
  gst_object_unref(appsrc);
  CHECK(source_clock_audio_bin_free(audio));
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_object_unref(mixer);
  gst_object_unref(parent);
  g_main_context_unref(context);
  return TRUE;
}

/* Material is encoded in a separate pipeline. Only the decoder branch's PCM
 * at the newly requested mixer sink is evidence, never encoder output/silence. */
static GPtrArray *aac_material(void) {
  GError *error = NULL;
  GstElement *producer = gst_parse_launch(
      "audiotestsrc num-buffers=12 samplesperbuffer=1024 wave=sine ! "
      "audio/x-raw,rate=44100,channels=1 ! audioconvert ! avenc_aac ! "
      "aacparse ! audio/mpeg,mpegversion=4,stream-format=adts ! "
      "appsink name=material sync=false", &error);
  GstElement *sink;
  GPtrArray *frames = g_ptr_array_new_with_free_func((GDestroyNotify)gst_buffer_unref);
  guint i;
  if (error != NULL || producer == NULL) {
    g_printerr("AAC fixture: %s\n", error == NULL ? "no pipeline" : error->message);
    g_clear_error(&error);
    if (producer != NULL) gst_object_unref(producer);
    return frames;
  }
  sink = gst_bin_get_by_name(GST_BIN(producer), "material");
  gst_element_set_state(producer, GST_STATE_PLAYING);
  for (i = 0; i < 12; ++i) {
    GstSample *sample = gst_app_sink_try_pull_sample(GST_APP_SINK(sink), 2 * GST_SECOND);
    GstBuffer *buffer;
    if (sample == NULL) break;
    buffer = gst_buffer_copy_deep(gst_sample_get_buffer(sample));
    GST_BUFFER_PTS(buffer) = gst_util_uint64_scale(i * 1024, GST_SECOND, 44100);
    GST_BUFFER_DURATION(buffer) = gst_util_uint64_scale(1024, GST_SECOND, 44100);
    g_ptr_array_add(frames, buffer);
    gst_sample_unref(sample);
  }
  gst_element_set_state(producer, GST_STATE_NULL);
  gst_object_unref(sink);
  gst_object_unref(producer);
  return frames;
}

typedef struct {
  const char *name;
  gboolean expect_pcm;
  GstClockTime late_cutoff;
  GMutex mutex;
  guint buffers, nonzero_buffers, late_buffers, late_nonzero_buffers;
  guint bad_caps, invalid_pts, regressing_pts, discontinuities;
  gboolean first_discont;
  gboolean have_pts, have_late_nonzero;
  GstClockTime first_pts, last_pts, last_end;
  GstClockTime first_late_nonzero_pts, last_late_nonzero_end;
} ContinuityObservation;

typedef struct {
  ContinuityObservation appsrc, mixer_sink, mixer_src;
  guint appsrc_probes, mixer_sink_probes;
  guint unstable_additions;
  GMutex tail_mutex;
  GCond tail_cond;
  GstClockTime tail_cutoff;
  gboolean hold_tail, tail_entered, tail_released;
} StartupContinuityHarness;

/* Reproduce an appsrc worker still delivering the final queued buffers after
 * the test thread has already observed enough continuous downstream audio. */
static GstPadProbeReturn hold_continuity_tail(GstPad *pad, GstPadProbeInfo *info,
                                             gpointer data) {
  StartupContinuityHarness *harness = data;
  GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  (void)pad;
  if (buffer == NULL || GST_BUFFER_PTS(buffer) < harness->tail_cutoff)
    return GST_PAD_PROBE_OK;
  g_mutex_lock(&harness->tail_mutex);
  harness->tail_entered = TRUE;
  while (!harness->tail_released)
    g_cond_wait(&harness->tail_cond, &harness->tail_mutex);
  g_mutex_unlock(&harness->tail_mutex);
  return GST_PAD_PROBE_OK;
}

static gboolean release_continuity_tail(gpointer data) {
  StartupContinuityHarness *harness = data;
  g_mutex_lock(&harness->tail_mutex);
  harness->tail_released = TRUE;
  g_cond_broadcast(&harness->tail_cond);
  g_mutex_unlock(&harness->tail_mutex);
  return G_SOURCE_REMOVE;
}

static void continuity_observation_init(ContinuityObservation *seen,
                                         const char *name,
                                         gboolean expect_pcm,
                                         GstClockTime late_cutoff) {
  memset(seen, 0, sizeof(*seen));
  seen->name = name;
  seen->expect_pcm = expect_pcm;
  seen->late_cutoff = late_cutoff;
  seen->first_pts = GST_CLOCK_TIME_NONE;
  seen->last_pts = GST_CLOCK_TIME_NONE;
  seen->last_end = GST_CLOCK_TIME_NONE;
  seen->first_late_nonzero_pts = GST_CLOCK_TIME_NONE;
  seen->last_late_nonzero_end = GST_CLOCK_TIME_NONE;
  g_mutex_init(&seen->mutex);
}

static void continuity_observation_clear(ContinuityObservation *seen) {
  g_mutex_clear(&seen->mutex);
}

static GstPadProbeReturn observe_continuity(GstPad *pad, GstPadProbeInfo *info,
                                             gpointer data) {
  ContinuityObservation *seen = data;
  GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  GstCaps *caps;
  GstMapInfo map;
  GstClockTime pts, duration, end;
  gboolean nonzero = FALSE, bad_caps = FALSE;
  const GstStructure *s;
  gint rate = 0, channels = 0;
  gsize i;
  if (buffer == NULL) return GST_PAD_PROBE_OK;
  caps = gst_pad_get_current_caps(pad);
  s = caps == NULL ? NULL : gst_caps_get_structure(caps, 0);
  if (seen->expect_pcm) {
    bad_caps = s == NULL || !gst_structure_has_name(s, "audio/x-raw") ||
        g_strcmp0(gst_structure_get_string(s, "format"), "S16LE") != 0 ||
        !gst_structure_get_int(s, "rate", &rate) || rate != 48000 ||
        !gst_structure_get_int(s, "channels", &channels) || channels != 2;
  } else {
    bad_caps = s == NULL || !gst_structure_has_name(s, "audio/mpeg") ||
        g_strcmp0(gst_structure_get_string(s, "stream-format"), "adts") != 0;
  }
  if (caps != NULL) gst_caps_unref(caps);
  if (gst_buffer_map(buffer, &map, GST_MAP_READ)) {
    for (i = 0; i < map.size; ++i) {
      if (map.data[i] != 0) {
        nonzero = TRUE;
        break;
      }
    }
    gst_buffer_unmap(buffer, &map);
  }
  pts = GST_BUFFER_PTS(buffer);
  duration = GST_BUFFER_DURATION(buffer);
  end = GST_CLOCK_TIME_IS_VALID(pts) && GST_CLOCK_TIME_IS_VALID(duration) &&
      duration < GST_CLOCK_TIME_NONE - pts ? pts + duration : GST_CLOCK_TIME_NONE;
  g_mutex_lock(&seen->mutex);
  if (seen->buffers == 0) seen->first_discont = GST_BUFFER_FLAG_IS_SET(buffer, GST_BUFFER_FLAG_DISCONT);
  if (GST_BUFFER_FLAG_IS_SET(buffer, GST_BUFFER_FLAG_DISCONT)) ++seen->discontinuities;
  ++seen->buffers;
  if (nonzero) ++seen->nonzero_buffers;
  if (bad_caps) ++seen->bad_caps;
  if (!GST_CLOCK_TIME_IS_VALID(pts) || !GST_CLOCK_TIME_IS_VALID(end)) {
    ++seen->invalid_pts;
  } else {
    if (seen->have_pts && pts < seen->last_pts) ++seen->regressing_pts;
    if (!seen->have_pts) seen->first_pts = pts;
    seen->have_pts = TRUE;
    seen->last_pts = pts;
    seen->last_end = end;
    if (pts >= seen->late_cutoff) {
      ++seen->late_buffers;
      if (nonzero) {
        ++seen->late_nonzero_buffers;
        if (!seen->have_late_nonzero) seen->first_late_nonzero_pts = pts;
        seen->have_late_nonzero = TRUE;
        seen->last_late_nonzero_end = end;
      }
    }
  }
  g_mutex_unlock(&seen->mutex);
  return GST_PAD_PROBE_OK;
}

static gboolean continuity_is_late(const ContinuityObservation *value) {
  ContinuityObservation *seen = (ContinuityObservation *)value;
  gboolean ok;
  g_mutex_lock(&seen->mutex);
  ok = seen->late_nonzero_buffers >= 20 && seen->have_late_nonzero &&
      seen->last_late_nonzero_end > seen->first_late_nonzero_pts &&
      seen->last_late_nonzero_end - seen->first_late_nonzero_pts >= 1500 * GST_MSECOND &&
      seen->bad_caps == 0 && seen->invalid_pts == 0 && seen->regressing_pts == 0;
  g_mutex_unlock(&seen->mutex);
  return ok;
}

static gboolean continuity_input_drained(ContinuityObservation *seen,
                                         guint64 startup_dropped, guint total) {
  gboolean drained;
  g_mutex_lock(&seen->mutex);
  drained = seen->buffers + startup_dropped == total;
  g_mutex_unlock(&seen->mutex);
  return drained;
}

static void print_continuity(const ContinuityObservation *value) {
  ContinuityObservation *seen = (ContinuityObservation *)value;
  g_mutex_lock(&seen->mutex);
  printf("E1 boundary=%s buffers=%u nonzero=%u late=%u late_nonzero=%u "
      "first_pts=%" G_GUINT64_FORMAT " last_pts=%" G_GUINT64_FORMAT
      " last_end=%" G_GUINT64_FORMAT " late_first=%" G_GUINT64_FORMAT
      " late_end=%" G_GUINT64_FORMAT " bad_caps=%u invalid_pts=%u regressions=%u\n",
      seen->name, seen->buffers, seen->nonzero_buffers, seen->late_buffers,
      seen->late_nonzero_buffers, seen->first_pts, seen->last_pts, seen->last_end,
      seen->first_late_nonzero_pts, seen->last_late_nonzero_end, seen->bad_caps,
      seen->invalid_pts, seen->regressing_pts);
  g_mutex_unlock(&seen->mutex);
}

static void observe_startup_branch(GstBin *parent, GstElement *child, gpointer data) {
  StartupContinuityHarness *harness = data;
  GstElement *input;
  GstPad *src;
  GstState current = GST_STATE_VOID_PENDING, pending = GST_STATE_VOID_PENDING;
  if (!GST_IS_BIN(child)) return;
  input = find_appsrc(child);
  if (input == NULL) return;
  gst_element_get_state(GST_ELEMENT(parent), &current, &pending, 0);
  if (current != GST_STATE_PLAYING || pending != GST_STATE_VOID_PENDING) ++harness->unstable_additions;
  src = gst_element_get_static_pad(input, "src");
  if (src != NULL) {
    if (harness->hold_tail)
      gst_pad_add_probe(src, GST_PAD_PROBE_TYPE_BUFFER, hold_continuity_tail,
          harness, NULL);
    gst_pad_add_probe(src, GST_PAD_PROBE_TYPE_BUFFER, observe_continuity,
        &harness->appsrc, NULL);
    ++harness->appsrc_probes;
    gst_object_unref(src);
  }
  gst_object_unref(input);
}

static void observe_startup_mixer_pad(GstElement *mixer, GstPad *pad, gpointer data) {
  StartupContinuityHarness *harness = data;
  (void)mixer;
  if (GST_PAD_DIRECTION(pad) != GST_PAD_SINK) return;
  gst_pad_add_probe(pad, GST_PAD_PROBE_TYPE_BUFFER, observe_continuity,
      &harness->mixer_sink, NULL);
  ++harness->mixer_sink_probes;
}

/* Removing the stable-PLAYING gate loses audio at mixer output. A non-live
 * preroll branch so the parent is deterministically PAUSED -> PLAYING pending
 * while dynamic AAC arrives. The control runs the same stream fully PLAYING. */
static gboolean audio_continuity_case(guint hold_ms, gboolean hold_tail) {
  gboolean hold_preroll = hold_ms != 0;
  const char *description = hold_preroll ?
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=mix ignore-inactive-pads=true latency=100000000 "
      "min-upstream-latency=200000000 ! fakesink sync=false async=false "
      "appsrc name=preroll_hold is-live=false format=time ! "
      "audio/x-raw,format=S16LE,rate=8000,channels=1,layout=interleaved ! "
      "fakesink async=true sync=false" :
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=mix ignore-inactive-pads=true latency=100000000 "
      "min-upstream-latency=200000000 ! fakesink sync=false async=false";
  GstElement *parent = gst_parse_launch(description, NULL);
  GstElement *mixer = gst_bin_get_by_name(GST_BIN(parent), "mix");
  GstElement *preroll_hold = hold_preroll ?
      gst_bin_get_by_name(GST_BIN(parent), "preroll_hold") : NULL;
  GstPad *mixer_src = gst_element_get_static_pad(mixer, "src");
  GMainContext *context = g_main_context_new();
  SourceClockAudioBin *audio = source_clock_audio_bin_new(parent, mixer, context);
  SourceClockAudioFormat input = format();
  SourceClockAudioBinStats stats;
  StartupContinuityHarness harness = {0};
  GPtrArray *frames = aac_material();
  const GstClockTime frame_duration = gst_util_uint64_scale(1024, GST_SECOND, 44100);
  const guint total_frames = (guint)gst_util_uint64_scale_ceil((hold_ms > 1000 ? 4400 : 3200) * GST_MSECOND,
      44100, 1024 * GST_SECOND);
  const GstClockTime total_duration = total_frames * frame_duration;
  const GstClockTime late_cutoff = total_duration - 2 * GST_SECOND;
  GstState startup_current = GST_STATE_VOID_PENDING, startup_pending = GST_STATE_VOID_PENDING;
  GstState held_current = GST_STATE_VOID_PENDING, held_pending = GST_STATE_VOID_PENDING;
  GstState final_current = GST_STATE_VOID_PENDING, final_pending = GST_STATE_VOID_PENDING;
  GstStateChangeReturn request_result, startup_result, final_result;
  GstFlowReturn last_flow = GST_FLOW_OK, preroll_flow = GST_FLOW_OK;
  guint mixer_src_probe, i, pushed = 0, flow_failures = 0;
  guint held_audio_frames = 0;
  guint baseline_children = GST_BIN_NUMCHILDREN(parent), baseline_pads = GST_ELEMENT(mixer)->numsinkpads;
  guint max_pending = 0, preplaying_violations = 0;
  GstClockTime max_pending_duration = 0;
  gboolean bounds_ok = TRUE;
  GstClockTime held_audio_duration = 0;
  gboolean preroll_released = FALSE, startup_state_ok, held_state_ok = TRUE;
  gint64 start_us, deadline;
  gboolean appsrc_ok, mixer_sink_ok, mixer_src_ok, stats_ok, result;
  gboolean input_discont_ok;
  guint input_discontinuities;
  gboolean input_first_discont;
  const char *first_losing_boundary;
  GSource *tail_release = NULL;
  CHECK(parent != NULL && mixer != NULL && mixer_src != NULL && audio != NULL &&
      (!hold_preroll || preroll_hold != NULL));
  CHECK(frames->len == 12 && total_duration >= 3 * GST_SECOND);
  g_mutex_init(&harness.tail_mutex);
  g_cond_init(&harness.tail_cond);
  harness.hold_tail = hold_tail;
  harness.tail_cutoff = (total_frames - 3) * frame_duration;
  input.sample_rate = 44100;
  input.channels = 1;
  continuity_observation_init(&harness.appsrc, "appsrc-src", FALSE, late_cutoff);
  continuity_observation_init(&harness.mixer_sink, "dynamic-mixer-sink", TRUE, late_cutoff);
  continuity_observation_init(&harness.mixer_src, "mixer-src", TRUE, late_cutoff);
  g_signal_connect(parent, "element-added", G_CALLBACK(observe_startup_branch), &harness);
  g_signal_connect(mixer, "pad-added", G_CALLBACK(observe_startup_mixer_pad), &harness);
  mixer_src_probe = gst_pad_add_probe(mixer_src, GST_PAD_PROBE_TYPE_BUFFER,
      observe_continuity, &harness.mixer_src, NULL);
  request_result = gst_element_set_state(parent, GST_STATE_PLAYING);
  startup_result = gst_element_get_state(parent, &startup_current, &startup_pending,
      hold_preroll ? 200 * GST_MSECOND : 0);
  startup_state_ok = hold_preroll ?
      request_result == GST_STATE_CHANGE_ASYNC && startup_result == GST_STATE_CHANGE_ASYNC &&
          startup_current != GST_STATE_PLAYING && startup_pending == GST_STATE_PLAYING :
      request_result != GST_STATE_CHANGE_FAILURE && startup_current == GST_STATE_PLAYING &&
          startup_pending == GST_STATE_VOID_PENDING;
  CHECK(startup_state_ok);
  CHECK(source_clock_audio_bin_configure(audio, &input, NULL));
  start_us = g_get_monotonic_time();
  for (i = 0; i < total_frames; ++i) {
    GstBuffer *buffer = gst_buffer_copy_deep(g_ptr_array_index(frames, i % frames->len));
    gint64 target_us = start_us + (gint64)gst_util_uint64_scale(i + 1,
        1024 * G_USEC_PER_SEC, 44100);
    GST_BUFFER_PTS(buffer) = i * frame_duration;
    GST_BUFFER_DURATION(buffer) = frame_duration;
    GST_BUFFER_FLAG_UNSET(buffer, GST_BUFFER_FLAG_DISCONT);
    last_flow = source_clock_audio_bin_push(audio, buffer);
    if (last_flow == GST_FLOW_OK) ++pushed;
    else ++flow_failures;
    if (hold_preroll && !preroll_released) {
      ++held_audio_frames;
      held_audio_duration = (i + 1) * frame_duration;
    }
    while (g_main_context_iteration(context, FALSE)) {}
    source_clock_audio_bin_get_stats(audio, &stats);
    max_pending = MAX(max_pending, stats.pending_frames);
    max_pending_duration = MAX(max_pending_duration, stats.pending_duration);
    bounds_ok = bounds_ok && stats.pending_frames <= 32 && stats.pending_bytes <= 1048576 &&
        stats.pending_duration <= 500 * GST_MSECOND &&
        (stats.pending_frames == 0 || (i + 1) * frame_duration - stats.oldest_pts <= 500 * GST_MSECOND);
    if (hold_preroll && !preroll_released &&
        (stats.ready || GST_BIN_NUMCHILDREN(parent) != baseline_children ||
         GST_ELEMENT(mixer)->numsinkpads != baseline_pads || harness.appsrc_probes != 0))
      ++preplaying_violations;
    while (g_get_monotonic_time() < target_us) {
      gint64 remaining = target_us - g_get_monotonic_time();
      while (g_main_context_iteration(context, FALSE)) {}
      if (remaining > 0) g_usleep((gulong)MIN(remaining, 1000));
    }
    if (hold_preroll && !preroll_released && held_audio_duration >= hold_ms * GST_MSECOND) {
      GstBuffer *preroll = gst_buffer_new_allocate(NULL, 320, NULL);
      gst_element_get_state(parent, &held_current, &held_pending, 0);
      held_state_ok = held_current != GST_STATE_PLAYING && held_pending == GST_STATE_PLAYING;
      gst_buffer_memset(preroll, 0, 0, 320);
      GST_BUFFER_PTS(preroll) = 0;
      GST_BUFFER_DURATION(preroll) = 20 * GST_MSECOND;
      preroll_flow = gst_app_src_push_buffer(GST_APP_SRC(preroll_hold), preroll);
      if (preroll_flow == GST_FLOW_OK)
        preroll_flow = gst_app_src_end_of_stream(GST_APP_SRC(preroll_hold));
      preroll_released = TRUE;
    }
  }
  if (hold_tail) {
    tail_release = g_timeout_source_new(100);
    g_source_set_callback(tail_release, release_continuity_tail, &harness, NULL);
    g_source_attach(tail_release, context);
  }
  deadline = g_get_monotonic_time() + G_TIME_SPAN_SECOND;
  do {
    while (g_main_context_iteration(context, FALSE)) {}
    source_clock_audio_bin_get_stats(audio, &stats);
    /* Continuous PCM does not imply that the asynchronous appsrc worker has
     * consumed the final buffers. Keep the full-count assertion, but wait for
     * its observable boundary within the same bounded drain deadline. */
    if (continuity_input_drained(&harness.appsrc, stats.startup_dropped_frames, total_frames) &&
        continuity_is_late(&harness.appsrc) &&
        continuity_is_late(&harness.mixer_sink) &&
        continuity_is_late(&harness.mixer_src)) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  source_clock_audio_bin_get_stats(audio, &stats);
  final_result = gst_element_get_state(parent, &final_current, &final_pending,
      hold_preroll ? GST_SECOND : 0);
  appsrc_ok = continuity_is_late(&harness.appsrc);
  mixer_sink_ok = continuity_is_late(&harness.mixer_sink);
  mixer_src_ok = continuity_is_late(&harness.mixer_src);
  stats_ok = stats.configured && stats.ready && !stats.creation_failed &&
      stats.pending_frames == 0 && stats.rejected_frames == 0 && !stats.waiting_for_parent_playing;
  g_mutex_lock(&harness.appsrc.mutex);
  stats_ok = stats_ok && harness.appsrc.buffers + stats.startup_dropped_frames == total_frames;
  if (hold_preroll) stats_ok = stats_ok && stats.startup_dropped_frames > 0 &&
      harness.appsrc.buffers + stats.startup_dropped_frames == total_frames &&
      harness.appsrc.first_pts == stats.startup_dropped_frames * frame_duration;
  input_discont_ok = harness.appsrc.first_discont && harness.appsrc.discontinuities == 1;
  input_discontinuities = harness.appsrc.discontinuities;
  input_first_discont = harness.appsrc.first_discont;
  g_mutex_unlock(&harness.appsrc.mutex);
  first_losing_boundary = !appsrc_ok ? "appsrc-src" :
      !mixer_sink_ok ? "dynamic-mixer-sink" : !mixer_src_ok ? "mixer-src" : "NONE";
  print_continuity(&harness.appsrc);
  print_continuity(&harness.mixer_sink);
  print_continuity(&harness.mixer_src);
  printf("E2 hold_ms=%u preplaying_violations=%u max_pending=%u max_duration=%" G_GUINT64_FORMAT
      " bounded=%d startup_dropped=%" G_GUINT64_FORMAT " unstable_additions=%u input_discont=%u first_discont=%d\n",
      hold_ms, preplaying_violations, max_pending, max_pending_duration, bounds_ok,
      stats.startup_dropped_frames, harness.unstable_additions, input_discontinuities, input_first_discont);
  printf("E2 drain complete=%d stats_ok=%d input_discont_ok=%d\n",
      continuity_input_drained(&harness.appsrc, stats.startup_dropped_frames, total_frames),
      stats_ok, input_discont_ok);
  printf("E1 case=%s request=%s startup_result=%s initial=%s/%s "
      "held=%s/%s held_frames=%u held_duration=%" G_GUINT64_FORMAT
      " preroll_released=%d preroll_flow=%s final_result=%s final=%s/%s pushed=%u/%u "
      "flow_failures=%u last_flow=%s configured=%d ready=%d creation_failed=%d "
      "pending=%u rejected=%" G_GUINT64_FORMAT " probes=%u/%u first_losing_boundary=%s\n",
      hold_preroll ? "async-held" : "playing-control",
      gst_element_state_change_return_get_name(request_result),
      gst_element_state_change_return_get_name(startup_result),
      gst_element_state_get_name(startup_current), gst_element_state_get_name(startup_pending),
      gst_element_state_get_name(held_current), gst_element_state_get_name(held_pending),
      held_audio_frames, held_audio_duration, preroll_released, gst_flow_get_name(preroll_flow),
      gst_element_state_change_return_get_name(final_result),
      gst_element_state_get_name(final_current), gst_element_state_get_name(final_pending),
      pushed, total_frames, flow_failures, gst_flow_get_name(last_flow), stats.configured,
      stats.ready, stats.creation_failed, stats.pending_frames, stats.rejected_frames,
      harness.appsrc_probes, harness.mixer_sink_probes, first_losing_boundary);
  result = startup_state_ok && pushed == total_frames && flow_failures == 0 && bounds_ok &&
      preplaying_violations == 0 && harness.unstable_additions == 0 &&
      harness.appsrc_probes == 1 && harness.mixer_sink_probes == 1 && stats_ok &&
      appsrc_ok && mixer_sink_ok && mixer_src_ok &&
      (!hold_preroll || (held_state_ok && preroll_released && preroll_flow == GST_FLOW_OK &&
          held_audio_duration >= hold_ms * GST_MSECOND &&
          held_audio_duration < hold_ms * GST_MSECOND + frame_duration &&
          input_discont_ok &&
          final_result != GST_STATE_CHANGE_FAILURE && final_current == GST_STATE_PLAYING &&
           final_pending == GST_STATE_VOID_PENDING));
  g_mutex_lock(&harness.tail_mutex);
  if (hold_tail) result = result && harness.tail_entered;
  g_mutex_unlock(&harness.tail_mutex);
  if (tail_release != NULL) {
    g_source_destroy(tail_release);
    g_source_unref(tail_release);
  }
  release_continuity_tail(&harness);
  source_clock_audio_bin_stop(audio);
  source_clock_audio_bin_free(audio);
  g_signal_handlers_disconnect_by_data(parent, &harness);
  g_signal_handlers_disconnect_by_data(mixer, &harness);
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_pad_remove_probe(mixer_src, mixer_src_probe);
  gst_object_unref(mixer_src);
  if (preroll_hold != NULL) gst_object_unref(preroll_hold);
  gst_object_unref(mixer);
  gst_object_unref(parent);
  g_main_context_unref(context);
  g_ptr_array_unref(frames);
  continuity_observation_clear(&harness.appsrc);
  continuity_observation_clear(&harness.mixer_sink);
  continuity_observation_clear(&harness.mixer_src);
  g_cond_clear(&harness.tail_cond);
  g_mutex_clear(&harness.tail_mutex);
  CHECK(result);
  return TRUE;
}

static gboolean startup_audio_continuity_control(void) {
  return audio_continuity_case(0, FALSE);
}

static gboolean startup_audio_continuity_async(void) {
  return audio_continuity_case(850, FALSE);
}

static gboolean startup_audio_continuity_long_async(void) {
  return audio_continuity_case(2000, FALSE);
}

static gboolean startup_audio_continuity_queued_tail(void) {
  return audio_continuity_case(850, TRUE);
}

typedef struct {
  const char *name;
  gboolean expect_pcm;
  GMutex mutex;
  GstSegment segment;
  gboolean saw_stream_start, saw_caps, saw_segment;
  guint order_violations;
  char event_order[32];
  guint event_order_len;
  guint buffers, nonzero_buffers;
  gboolean have_first, have_last;
  GstClockTime first_pts, last_pts, first_running, last_running;
  GstClockTime output_cursor_pts, output_cursor_end;
  guint output_cursor_buffers;
  gboolean have_output_cursor;
  GstState parent_current, parent_pending, branch_current, branch_pending;
  gboolean pad_active, bases_equal, clocks_equal;
  gint64 pad_offset;
  GstClockTime parent_base_time, branch_base_time;
} AxisObservation;

typedef struct {
  GstElement *parent;
  AxisObservation mixer_sink;
  AxisObservation mixer_src;
  gboolean paced;
  guint mixer_sink_probes;
} AxisHarness;

static void axis_observation_init(AxisObservation *seen, const char *name,
                                  gboolean expect_pcm) {
  memset(seen, 0, sizeof(*seen));
  seen->name = name;
  seen->expect_pcm = expect_pcm;
  seen->first_pts = GST_CLOCK_TIME_NONE;
  seen->last_pts = GST_CLOCK_TIME_NONE;
  seen->first_running = GST_CLOCK_TIME_NONE;
  seen->last_running = GST_CLOCK_TIME_NONE;
  seen->output_cursor_pts = GST_CLOCK_TIME_NONE;
  seen->output_cursor_end = GST_CLOCK_TIME_NONE;
  seen->parent_current = GST_STATE_VOID_PENDING;
  seen->parent_pending = GST_STATE_VOID_PENDING;
  seen->branch_current = GST_STATE_VOID_PENDING;
  seen->branch_pending = GST_STATE_VOID_PENDING;
  gst_segment_init(&seen->segment, GST_FORMAT_TIME);
  g_mutex_init(&seen->mutex);
}

static void axis_observation_clear(AxisObservation *seen) {
  g_mutex_clear(&seen->mutex);
}

static void axis_record_event(AxisObservation *seen, char code) {
  if (seen->event_order_len + 1 < sizeof(seen->event_order)) {
    seen->event_order[seen->event_order_len++] = code;
    seen->event_order[seen->event_order_len] = '\0';
  }
}

static gboolean axis_buffer_nonzero(GstBuffer *buffer) {
  GstMapInfo map;
  gsize i;
  gboolean nonzero = FALSE;
  if (!gst_buffer_map(buffer, &map, GST_MAP_READ)) return FALSE;
  for (i = 0; i < map.size; ++i) {
    if (map.data[i] != 0) {
      nonzero = TRUE;
      break;
    }
  }
  gst_buffer_unmap(buffer, &map);
  return nonzero;
}

static GstPadProbeReturn observe_axis_mixer_src(GstPad *pad,
                                                 GstPadProbeInfo *info,
                                                 gpointer data) {
  AxisObservation *seen = data;
  GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  GstClockTime pts, duration, end;
  (void)pad;
  if (buffer == NULL) return GST_PAD_PROBE_OK;
  pts = GST_BUFFER_PTS(buffer);
  duration = GST_BUFFER_DURATION(buffer);
  end = GST_CLOCK_TIME_IS_VALID(pts) && GST_CLOCK_TIME_IS_VALID(duration) &&
      duration < GST_CLOCK_TIME_NONE - pts ? pts + duration : GST_CLOCK_TIME_NONE;
  g_mutex_lock(&seen->mutex);
  ++seen->buffers;
  if (axis_buffer_nonzero(buffer)) ++seen->nonzero_buffers;
  if (GST_CLOCK_TIME_IS_VALID(pts)) {
    seen->last_pts = pts;
    if (GST_CLOCK_TIME_IS_VALID(end)) seen->last_running = end;
    seen->have_last = TRUE;
  }
  g_mutex_unlock(&seen->mutex);
  return GST_PAD_PROBE_OK;
}

static GstPadProbeReturn observe_axis_mixer_sink(GstPad *pad,
                                                   GstPadProbeInfo *info,
                                                   gpointer data) {
  AxisHarness *harness = data;
  AxisObservation *seen = &harness->mixer_sink;
  GstEvent *event = GST_PAD_PROBE_INFO_EVENT(info);
  GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  gboolean nonzero;
  GstClockTime pts, running;
  GstSegment segment;
  GstPad *peer = NULL;
  GstElement *branch = NULL;
  GstState parent_current = GST_STATE_VOID_PENDING, parent_pending = GST_STATE_VOID_PENDING;
  GstState branch_current = GST_STATE_VOID_PENDING, branch_pending = GST_STATE_VOID_PENDING;
  GstClock *parent_clock, *branch_clock = NULL;
  GstClockTime parent_base, branch_base = GST_CLOCK_TIME_NONE;
  gint64 pad_offset;

  if ((GST_PAD_PROBE_INFO_TYPE(info) & GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM) != 0) {
    GstEventType type = GST_EVENT_TYPE(event);
    g_mutex_lock(&seen->mutex);
    if (type == GST_EVENT_STREAM_START) {
      if (seen->saw_stream_start) ++seen->order_violations;
      seen->saw_stream_start = TRUE;
      axis_record_event(seen, 'S');
    } else if (type == GST_EVENT_CAPS) {
      if (!seen->saw_stream_start || seen->saw_caps) ++seen->order_violations;
      seen->saw_caps = TRUE;
      axis_record_event(seen, 'C');
    } else if (type == GST_EVENT_SEGMENT) {
      const GstSegment *incoming = NULL;
      if (!seen->saw_caps || seen->saw_segment) ++seen->order_violations;
      gst_event_parse_segment(event, &incoming);
      if (incoming != NULL) seen->segment = *incoming;
      seen->saw_segment = incoming != NULL;
      axis_record_event(seen, 'G');
    }
    g_mutex_unlock(&seen->mutex);
    return GST_PAD_PROBE_OK;
  }
  if (buffer == NULL) return GST_PAD_PROBE_OK;

  nonzero = axis_buffer_nonzero(buffer);
  pts = GST_BUFFER_PTS(buffer);
  g_mutex_lock(&seen->mutex);
  segment = seen->segment;
  if (!seen->saw_segment || !GST_CLOCK_TIME_IS_VALID(pts)) {
    ++seen->order_violations;
    running = GST_CLOCK_TIME_NONE;
  } else {
    running = gst_segment_to_running_time(&segment, GST_FORMAT_TIME, pts);
  }
  if (!seen->have_first) {
    seen->first_pts = pts;
    seen->first_running = running;
    seen->have_first = TRUE;
    axis_record_event(seen, 'B');
    g_mutex_lock(&harness->mixer_src.mutex);
    if (harness->mixer_src.have_last) {
      seen->have_output_cursor = TRUE;
      seen->output_cursor_pts = harness->mixer_src.last_pts;
      seen->output_cursor_end = harness->mixer_src.last_running;
      seen->output_cursor_buffers = harness->mixer_src.buffers;
    }
    g_mutex_unlock(&harness->mixer_src.mutex);
    peer = gst_pad_get_peer(pad);
    if (peer != NULL) {
      GstObject *owner = gst_object_get_parent(GST_OBJECT(peer));
      if (owner != NULL && GST_IS_ELEMENT(owner)) branch = GST_ELEMENT(owner);
      else if (owner != NULL) gst_object_unref(owner);
      gst_object_unref(peer);
    }
    gst_element_get_state(harness->parent, &parent_current, &parent_pending, 0);
    parent_clock = gst_element_get_clock(harness->parent);
    parent_base = gst_element_get_base_time(harness->parent);
    if (branch != NULL) {
      gst_element_get_state(branch, &branch_current, &branch_pending, 0);
      branch_clock = gst_element_get_clock(branch);
      branch_base = gst_element_get_base_time(branch);
      gst_object_unref(branch);
    }
    pad_offset = gst_pad_get_offset(pad);
    seen->parent_current = parent_current;
    seen->parent_pending = parent_pending;
    seen->branch_current = branch_current;
    seen->branch_pending = branch_pending;
    seen->pad_active = gst_pad_is_active(pad);
    seen->pad_offset = pad_offset;
    seen->parent_base_time = parent_base;
    seen->branch_base_time = branch_base;
    seen->bases_equal = branch != NULL && parent_base == branch_base;
    seen->clocks_equal = branch != NULL && parent_clock == branch_clock;
  } else if (GST_CLOCK_TIME_IS_VALID(pts) &&
             GST_CLOCK_TIME_IS_VALID(seen->last_pts) && pts < seen->last_pts) {
    ++seen->order_violations;
  }
  ++seen->buffers;
  if (nonzero) ++seen->nonzero_buffers;
  if (GST_CLOCK_TIME_IS_VALID(pts)) {
    seen->last_pts = pts;
    seen->last_running = running;
    seen->have_last = TRUE;
  }
  g_mutex_unlock(&seen->mutex);
  return GST_PAD_PROBE_OK;
}

static void observe_axis_mixer_pad(GstElement *mixer, GstPad *pad, gpointer data) {
  AxisHarness *harness = data;
  (void)mixer;
  if (GST_PAD_DIRECTION(pad) == GST_PAD_SINK) {
    gst_pad_add_probe(pad, GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM | GST_PAD_PROBE_TYPE_BUFFER,
        observe_axis_mixer_sink, harness, NULL);
    g_atomic_int_inc((gint *)&harness->mixer_sink_probes);
  }
}

static const char *axis_classification(const AxisObservation *value) {
  AxisObservation *seen = (AxisObservation *)value;
  gboolean segment_ok, state_ok, pad_ok, cursor_ahead;
  g_mutex_lock(&seen->mutex);
  segment_ok = seen->saw_stream_start && seen->saw_caps && seen->saw_segment &&
      seen->order_violations == 0 && seen->have_first && seen->have_last &&
      GST_CLOCK_TIME_IS_VALID(seen->first_running) &&
      GST_CLOCK_TIME_IS_VALID(seen->last_running) &&
      seen->segment.format == GST_FORMAT_TIME && seen->segment.rate > 0.0;
  state_ok = seen->parent_current == GST_STATE_PLAYING &&
      seen->parent_pending == GST_STATE_VOID_PENDING &&
      seen->branch_current == GST_STATE_PLAYING &&
      seen->branch_pending == GST_STATE_VOID_PENDING;
  pad_ok = seen->pad_active;
  cursor_ahead = seen->have_output_cursor &&
      GST_CLOCK_TIME_IS_VALID(seen->output_cursor_end) &&
      GST_CLOCK_TIME_IS_VALID(seen->first_running) &&
      seen->output_cursor_end > seen->first_running + 100 * GST_MSECOND;
  g_mutex_unlock(&seen->mutex);
  if (!segment_ok) return "SEGMENT_INVALID";
  if (!state_ok) return "BRANCH_STATE_INCOMPLETE";
  if (!pad_ok) return "PAD_INACTIVE";
  if (cursor_ahead) return "OUTPUT_CURSOR_AHEAD";
  return "UNRESOLVED";
}

static void print_axis_observation(const AxisObservation *value) {
  AxisObservation *seen = (AxisObservation *)value;
  g_mutex_lock(&seen->mutex);
  printf("E3 boundary=%s events=%s buffers=%u nonzero=%u order_violations=%u "
      "segment=%d start=%" G_GUINT64_FORMAT " time=%" G_GUINT64_FORMAT
      " base=%" G_GUINT64_FORMAT " offset=%" G_GUINT64_FORMAT " rate=%g "
      "first_pts=%" G_GUINT64_FORMAT " first_running=%" G_GUINT64_FORMAT
      "last_pts=%" G_GUINT64_FORMAT " last_running=%" G_GUINT64_FORMAT "\n",
      seen->name, seen->event_order, seen->buffers, seen->nonzero_buffers,
      seen->order_violations, seen->saw_segment, seen->segment.start,
      seen->segment.time, seen->segment.base, seen->segment.offset,
      seen->segment.rate, seen->first_pts, seen->first_running, seen->last_pts,
      seen->last_running);
  printf("E3 state boundary=%s parent=%s/%s branch=%s/%s pad_active=%d pad_offset=%" G_GINT64_FORMAT
      " base_equal=%d clock_equal=%d parent_base=%" G_GUINT64_FORMAT
      " branch_base=%" G_GUINT64_FORMAT " output_cursor=%d/%u pts=%" G_GUINT64_FORMAT
      " end=%" G_GUINT64_FORMAT "\n", seen->name,
      gst_element_state_get_name(seen->parent_current),
      gst_element_state_get_name(seen->parent_pending),
      gst_element_state_get_name(seen->branch_current),
      gst_element_state_get_name(seen->branch_pending), seen->pad_active,
      seen->pad_offset, seen->bases_equal, seen->clocks_equal,
      seen->parent_base_time, seen->branch_base_time, seen->have_output_cursor,
      seen->output_cursor_buffers, seen->output_cursor_pts,
      seen->output_cursor_end);
  g_mutex_unlock(&seen->mutex);
}

static gboolean axis_diagnostic_case(guint hold_ms, gboolean paced) {
  const char *sink = paced ? "fakesink sync=true async=false" :
      "fakesink sync=false async=false";
  char *description = g_strdup_printf(
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=mix ignore-inactive-pads=true ! %s "
      "appsrc name=preroll_hold is-live=false format=time ! "
      "audio/x-raw,format=S16LE,rate=8000,channels=1,layout=interleaved ! "
      "fakesink async=true sync=false", sink);
  GstElement *parent = gst_parse_launch(description, NULL);
  GstElement *mixer = parent == NULL ? NULL : gst_bin_get_by_name(GST_BIN(parent), "mix");
  GstElement *preroll_hold = parent == NULL ? NULL :
      gst_bin_get_by_name(GST_BIN(parent), "preroll_hold");
  GstPad *mixer_src = mixer == NULL ? NULL : gst_element_get_static_pad(mixer, "src");
  GMainContext *context = g_main_context_new();
  SourceClockAudioBin *audio = NULL;
  SourceClockAudioFormat input = format();
  GPtrArray *frames = aac_material();
  AxisHarness harness = {0};
  SourceClockAudioBinStats stats;
  GstState current = GST_STATE_VOID_PENDING, pending = GST_STATE_VOID_PENDING;
  GstStateChangeReturn result;
  guint total_frames, i, pushed = 0;
  gint64 start_us, deadline;
  gboolean released = FALSE, ok;
  GstClockTime frame_duration = gst_util_uint64_scale(1024, GST_SECOND, 44100);
  GstFlowReturn flow = GST_FLOW_ERROR;

  g_free(description);
  CHECK(parent != NULL && mixer != NULL && preroll_hold != NULL && mixer_src != NULL);
  audio = source_clock_audio_bin_new(parent, mixer, context);
  CHECK(audio != NULL && frames->len == 12);
  harness.parent = parent;
  harness.paced = paced;
  axis_observation_init(&harness.mixer_sink, "dynamic-mixer-sink", TRUE);
  axis_observation_init(&harness.mixer_src, "mixer-src", TRUE);
  gst_pad_add_probe(mixer_src, GST_PAD_PROBE_TYPE_BUFFER,
      observe_axis_mixer_src, &harness.mixer_src, NULL);
  g_signal_connect(mixer, "pad-added", G_CALLBACK(observe_axis_mixer_pad), &harness);
  result = gst_element_set_state(parent, GST_STATE_PLAYING);
  CHECK(result == GST_STATE_CHANGE_ASYNC);
  CHECK(gst_element_get_state(parent, &current, &pending, 200 * GST_MSECOND) ==
      GST_STATE_CHANGE_ASYNC && current != GST_STATE_PLAYING &&
      pending == GST_STATE_PLAYING);
  input.sample_rate = 44100;
  input.channels = 1;
  CHECK(source_clock_audio_bin_configure(audio, &input, NULL));
  total_frames = (guint)gst_util_uint64_scale_ceil(
      (hold_ms > 1000 ? 4400 : 3200) * GST_MSECOND, 44100,
      1024 * GST_SECOND);
  start_us = g_get_monotonic_time();
  for (i = 0; i < total_frames; ++i) {
    GstBuffer *buffer = gst_buffer_copy_deep(g_ptr_array_index(frames, i % frames->len));
    gint64 target_us = start_us + (gint64)gst_util_uint64_scale(i + 1,
        1024 * G_USEC_PER_SEC, 44100);
    GST_BUFFER_PTS(buffer) = i * frame_duration;
    GST_BUFFER_DURATION(buffer) = frame_duration;
    flow = source_clock_audio_bin_push(audio, buffer);
    if (flow == GST_FLOW_OK) ++pushed;
    while (g_main_context_iteration(context, FALSE)) {}
    while (g_get_monotonic_time() < target_us) {
      gint64 remaining = target_us - g_get_monotonic_time();
      while (g_main_context_iteration(context, FALSE)) {}
      if (remaining > 0) g_usleep((gulong)MIN(remaining, 1000));
    }
    if (!released && (i + 1) * frame_duration >= hold_ms * GST_MSECOND) {
      GstBuffer *preroll = gst_buffer_new_allocate(NULL, 320, NULL);
      gst_element_get_state(parent, &current, &pending, 0);
      gst_buffer_memset(preroll, 0, 0, 320);
      GST_BUFFER_PTS(preroll) = 0;
      GST_BUFFER_DURATION(preroll) = 20 * GST_MSECOND;
      flow = gst_app_src_push_buffer(GST_APP_SRC(preroll_hold), preroll);
      if (flow == GST_FLOW_OK) flow = gst_app_src_end_of_stream(GST_APP_SRC(preroll_hold));
      released = TRUE;
    }
  }
  deadline = g_get_monotonic_time() + G_TIME_SPAN_SECOND;
  while (g_get_monotonic_time() < deadline) {
    while (g_main_context_iteration(context, FALSE)) {}
    g_usleep(1000);
  }
  source_clock_audio_bin_get_stats(audio, &stats);
  ok = pushed == total_frames && released && flow == GST_FLOW_OK &&
      harness.mixer_sink_probes == 1 && stats.ready && !stats.creation_failed;
  printf("E3 case=hold-%u paced=%d classification=%s pushed=%u/%u "
      "mixer_output_buffers=%u mixer_output_nonzero=%u startup_dropped=%" G_GUINT64_FORMAT "\n",
      hold_ms, paced, axis_classification(&harness.mixer_sink), pushed,
      total_frames, harness.mixer_src.buffers, harness.mixer_src.nonzero_buffers,
      stats.startup_dropped_frames);
  print_axis_observation(&harness.mixer_sink);
  print_axis_observation(&harness.mixer_src);
  source_clock_audio_bin_stop(audio);
  source_clock_audio_bin_free(audio);
  g_signal_handlers_disconnect_by_data(mixer, &harness);
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_object_unref(mixer_src);
  gst_object_unref(preroll_hold);
  gst_object_unref(mixer);
  gst_object_unref(parent);
  g_main_context_unref(context);
  g_ptr_array_unref(frames);
  axis_observation_clear(&harness.mixer_sink);
  axis_observation_clear(&harness.mixer_src);
  CHECK(ok);
  return TRUE;
}

static gboolean startup_audio_time_axis_diagnostic(void) {
  CHECK(axis_diagnostic_case(850, FALSE));
  CHECK(axis_diagnostic_case(850, TRUE));
  CHECK(axis_diagnostic_case(2000, FALSE));
  CHECK(axis_diagnostic_case(2000, TRUE));
  return TRUE;
}

/* E4 characterizes recovery mechanisms, not a production fix. Each invocation
 * creates a clean copy of the E3 850ms/unpaced pipeline; E1/E3 stay untouched. */
typedef enum { E4_RECALCULATE, E4_MIXER_LATENCY, E4_SOURCE_OFFSET } E4Control;
typedef struct {
  GstSegment segment;
  guint streams, caps, segments, buffers, violations;
  GstClockTime pts[512], running[512];
  char order[32];
  guint order_length;
} E4Boundary;
typedef struct {
  GMutex mutex;
  GstElement *parent;
  E4Control control;
  E4Boundary source, sink;
  GstSegment initial_source_segment;
  GstPad *source_pad, *sink_pad;
  gulong source_probe, sink_probe;
  guint links, offset_calls, shifted_buffers;
  GstClockTime clock_running, cursor, cursor_at_arrival;
  gint64 offset;
  guint output_buffers, nonzero;
  GstClockTime first_nonzero, last_nonzero_end, run_start, run_end, longest_run;
} E4Harness;

static void e4_event(E4Boundary *seen, GstEvent *event) {
  char code = 0;
  switch (GST_EVENT_TYPE(event)) {
    case GST_EVENT_STREAM_START:
      ++seen->streams; code = 'S'; break;
    case GST_EVENT_CAPS:
      if (!seen->streams) ++seen->violations;
      ++seen->caps; code = 'C'; break;
    case GST_EVENT_SEGMENT: {
      const GstSegment *segment;
      if (!seen->caps) ++seen->violations;
      gst_event_parse_segment(event, &segment);
      if (segment->format != GST_FORMAT_TIME || segment->rate != 1.0 ||
          segment->applied_rate != 1.0) ++seen->violations;
      seen->segment = *segment;
      ++seen->segments; code = 'G'; break;
    }
    default: break;
  }
  /* Sticky event replay is legal after a source offset, but must still precede
   * data and retain a valid TIME segment. Validate every following buffer. */
  if (code && seen->order_length + 1 < sizeof(seen->order)) {
    seen->order[seen->order_length++] = code;
    seen->order[seen->order_length] = 0;
  }
}

static GstClockTime e4_buffer(E4Boundary *seen, GstBuffer *buffer) {
  GstClockTime pts = GST_BUFFER_PTS(buffer), running = GST_CLOCK_TIME_NONE;
  if (!seen->streams || !seen->caps || !seen->segments ||
      !GST_CLOCK_TIME_IS_VALID(pts)) ++seen->violations;
  else running = gst_segment_to_running_time(&seen->segment, GST_FORMAT_TIME, pts);
  if (!GST_CLOCK_TIME_IS_VALID(running)) ++seen->violations;
  if (seen->buffers < G_N_ELEMENTS(seen->pts)) {
    if (seen->buffers && pts < seen->pts[seen->buffers - 1]) ++seen->violations;
    seen->pts[seen->buffers] = pts;
    seen->running[seen->buffers] = running;
  } else ++seen->violations;
  if (!seen->buffers && seen->order_length + 1 < sizeof(seen->order)) {
    seen->order[seen->order_length++] = 'B';
    seen->order[seen->order_length] = 0;
  }
  ++seen->buffers;
  return running;
}

static GstPadProbeReturn e4_source_probe(GstPad *pad, GstPadProbeInfo *info,
                                        gpointer data) {
  E4Harness *h = data;
  gboolean apply = FALSE;
  gint64 offset = 0;
  g_mutex_lock(&h->mutex);
  if (GST_PAD_PROBE_INFO_TYPE(info) & GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM) {
    e4_event(&h->source, GST_PAD_PROBE_INFO_EVENT(info));
  } else if (GST_PAD_PROBE_INFO_TYPE(info) & GST_PAD_PROBE_TYPE_BUFFER) {
    GstClockTime running = e4_buffer(&h->source, GST_PAD_PROBE_INFO_BUFFER(info));
    if (h->source.buffers == 1) {
      GstClock *clock = gst_element_get_clock(h->parent);
      GstClockTime base = gst_element_get_base_time(h->parent);
      GstClockTime now = clock ? gst_clock_get_time(clock) : GST_CLOCK_TIME_NONE;
      h->initial_source_segment = h->source.segment;
      if (clock) gst_object_unref(clock);
      if (GST_CLOCK_TIME_IS_VALID(now) && GST_CLOCK_TIME_IS_VALID(base) && now >= base)
        h->clock_running = now - base;
      if (h->control == E4_SOURCE_OFFSET && GST_PAD_DIRECTION(pad) == GST_PAD_SRC &&
          GST_CLOCK_TIME_IS_VALID(h->clock_running) && GST_CLOCK_TIME_IS_VALID(running) &&
          h->clock_running > running && h->clock_running - running <= 2 * GST_SECOND) {
        /* Measured once, no fixed shift, no PTS/DTS mutation, no sink-pad offset. */
        h->offset = (gint64)(h->clock_running - running);
        offset = h->offset;
        ++h->offset_calls;
        apply = TRUE;
      }
    }
  }
  g_mutex_unlock(&h->mutex);
  if (apply) gst_pad_set_offset(pad, offset);
  return GST_PAD_PROBE_OK;
}

static GstPadProbeReturn e4_sink_probe(GstPad *pad, GstPadProbeInfo *info,
                                      gpointer data) {
  E4Harness *h = data;
  (void)pad;
  g_mutex_lock(&h->mutex);
  if (GST_PAD_PROBE_INFO_TYPE(info) & GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM) {
    e4_event(&h->sink, GST_PAD_PROBE_INFO_EVENT(info));
  } else if (GST_PAD_PROBE_INFO_TYPE(info) & GST_PAD_PROBE_TYPE_BUFFER) {
    e4_buffer(&h->sink, GST_PAD_PROBE_INFO_BUFFER(info));
    if (h->sink.buffers == 1) h->cursor_at_arrival = h->cursor;
  }
  g_mutex_unlock(&h->mutex);
  return GST_PAD_PROBE_OK;
}

static void e4_linked(GstPad *sink, GstPad *source, gpointer data) {
  E4Harness *h = data;
  if (GST_PAD_DIRECTION(source) != GST_PAD_SRC || GST_PAD_DIRECTION(sink) != GST_PAD_SINK)
    return;
  ++h->links;
  if (h->links != 1) return;
  /* The linked peer is the real dynamic ghost source, before state sync/drain. */
  h->source_pad = gst_object_ref(source);
  h->source_probe = gst_pad_add_probe(source,
      GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM | GST_PAD_PROBE_TYPE_BUFFER,
      e4_source_probe, h, NULL);
}

static void e4_pad_added(GstElement *mixer, GstPad *pad, gpointer data) {
  E4Harness *h = data;
  (void)mixer;
  if (GST_PAD_DIRECTION(pad) != GST_PAD_SINK || h->sink_pad) return;
  h->sink_pad = gst_object_ref(pad);
  h->sink_probe = gst_pad_add_probe(pad,
      GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM | GST_PAD_PROBE_TYPE_BUFFER,
      e4_sink_probe, h, NULL);
  g_signal_connect(pad, "linked", G_CALLBACK(e4_linked), h);
}

static GstPadProbeReturn e4_output_probe(GstPad *pad, GstPadProbeInfo *info,
                                        gpointer data) {
  E4Harness *h = data;
  GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  GstClockTime pts = GST_BUFFER_PTS(buffer), duration = GST_BUFFER_DURATION(buffer);
  gboolean nonzero = axis_buffer_nonzero(buffer);
  (void)pad;
  g_mutex_lock(&h->mutex);
  ++h->output_buffers;
  if (GST_CLOCK_TIME_IS_VALID(pts) && GST_CLOCK_TIME_IS_VALID(duration) &&
      duration < GST_CLOCK_TIME_NONE - pts) {
    h->cursor = pts + duration;
    if (nonzero) {
      if (!h->nonzero) h->first_nonzero = pts;
      ++h->nonzero;
      h->last_nonzero_end = pts + duration;
      if (!GST_CLOCK_TIME_IS_VALID(h->run_start) || pts != h->run_end) h->run_start = pts;
      h->run_end = pts + duration;
      h->longest_run = MAX(h->longest_run, h->run_end - h->run_start);
    } else h->run_start = GST_CLOCK_TIME_NONE;
  }
  g_mutex_unlock(&h->mutex);
  return GST_PAD_PROBE_OK;
}

static gboolean e4_case(E4Control control, gboolean *recovered) {
  static const char *names[] = { "recalculate", "bounded-mixer-latency", "measured-source-offset" };
  GstElement *parent = gst_parse_launch(
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=mix ignore-inactive-pads=true ! fakesink sync=false async=false "
      "appsrc name=preroll_hold is-live=false format=time ! "
      "audio/x-raw,format=S16LE,rate=8000,channels=1,layout=interleaved ! "
      "fakesink async=true sync=false", NULL);
  GstElement *mixer = parent ? gst_bin_get_by_name(GST_BIN(parent), "mix") : NULL;
  GstElement *hold = parent ? gst_bin_get_by_name(GST_BIN(parent), "preroll_hold") : NULL;
  GstPad *out = mixer ? gst_element_get_static_pad(mixer, "src") : NULL;
  GMainContext *context = g_main_context_new();
  GPtrArray *frames = aac_material();
  SourceClockAudioBin *audio;
  SourceClockAudioBinStats stats;
  SourceClockAudioFormat input = format();
  E4Harness h = {0};
  GstState current, pending;
  GstClockTime frame_duration = gst_util_uint64_scale(1024, GST_SECOND, 44100);
  guint i, pushed = 0, errors = 0, recalc_calls = 0;
  guint total = (guint)gst_util_uint64_scale_ceil(3200 * GST_MSECOND, 44100, 1024 * GST_SECOND);
  guint64 latency = 0, upstream = 0;
  gint64 start, deadline;
  gulong out_probe;
  gboolean released = FALSE, recalc_ok = FALSE, ok, pts_equal = TRUE, shift_ok = TRUE;
  GstBus *bus;
  GstMessage *message;
  CHECK(parent && mixer && hold && out && frames->len == 12);
  *recovered = FALSE;
  h.parent = parent; h.control = control;
  h.clock_running = h.cursor = h.cursor_at_arrival = GST_CLOCK_TIME_NONE;
  h.first_nonzero = h.last_nonzero_end = h.run_start = h.run_end = GST_CLOCK_TIME_NONE;
  g_mutex_init(&h.mutex);
  gst_segment_init(&h.source.segment, GST_FORMAT_TIME);
  gst_segment_init(&h.sink.segment, GST_FORMAT_TIME);
  if (control == E4_MIXER_LATENCY)
    /* 900ms combined budget covers E3's ~700ms lead; never tune per result. */
    g_object_set(mixer, "latency", (guint64)(100 * GST_MSECOND),
        "min-upstream-latency", (guint64)(800 * GST_MSECOND), NULL);
  g_object_get(mixer, "latency", &latency, "min-upstream-latency", &upstream, NULL);
  out_probe = gst_pad_add_probe(out, GST_PAD_PROBE_TYPE_BUFFER, e4_output_probe, &h, NULL);
  g_signal_connect(mixer, "pad-added", G_CALLBACK(e4_pad_added), &h);
  audio = source_clock_audio_bin_new(parent, mixer, context);
  CHECK(audio && gst_element_set_state(parent, GST_STATE_PLAYING) == GST_STATE_CHANGE_ASYNC);
  CHECK(gst_element_get_state(parent, &current, &pending, 200 * GST_MSECOND) ==
      GST_STATE_CHANGE_ASYNC && current != GST_STATE_PLAYING && pending == GST_STATE_PLAYING);
  input.sample_rate = 44100; input.channels = 1;
  CHECK(source_clock_audio_bin_configure(audio, &input, NULL));
  start = g_get_monotonic_time();
  for (i = 0; i < total; ++i) {
    GstBuffer *buffer = gst_buffer_copy_deep(g_ptr_array_index(frames, i % frames->len));
    gint64 target = start + (gint64)gst_util_uint64_scale(i + 1, 1024 * G_USEC_PER_SEC, 44100);
    /* Fixture timestamps exactly match E3; controls never rewrite them. */
    GST_BUFFER_PTS(buffer) = i * frame_duration;
    GST_BUFFER_DURATION(buffer) = frame_duration;
    if (source_clock_audio_bin_push(audio, buffer) == GST_FLOW_OK) ++pushed;
    do {
      while (g_main_context_iteration(context, FALSE)) {}
      source_clock_audio_bin_get_stats(audio, &stats);
      if (control == E4_RECALCULATE && stats.ready && !recalc_calls) {
        ++recalc_calls;
        recalc_ok = gst_bin_recalculate_latency(GST_BIN(parent));
      }
      if (g_get_monotonic_time() >= target) break;
      g_usleep(1000);
    } while (TRUE);
    if (!released && (i + 1) * frame_duration >= 850 * GST_MSECOND) {
      GstBuffer *preroll = gst_buffer_new_allocate(NULL, 320, NULL);
      gst_buffer_memset(preroll, 0, 0, 320);
      GST_BUFFER_PTS(preroll) = 0;
      GST_BUFFER_DURATION(preroll) = 20 * GST_MSECOND;
      released = gst_app_src_push_buffer(GST_APP_SRC(hold), preroll) == GST_FLOW_OK &&
          gst_app_src_end_of_stream(GST_APP_SRC(hold)) == GST_FLOW_OK;
    }
  }
  /* Fixed observation window accommodates the explicit latency, no EOS flush
   * and no early exit upon first nonzero buffer to manufacture recovery. */
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  while (g_get_monotonic_time() < deadline) {
    while (g_main_context_iteration(context, FALSE)) {}
    g_usleep(1000);
  }
  source_clock_audio_bin_get_stats(audio, &stats);
  gst_element_get_state(parent, &current, &pending, 0);
  g_mutex_lock(&h.mutex);
  /* Snapshot before teardown/EOS: a shutdown flush is not live recovery. */
  for (i = 0; i < MIN(h.sink.buffers, G_N_ELEMENTS(h.sink.pts)); ++i) {
    if (i >= h.source.buffers || h.sink.pts[i] != h.source.pts[i]) pts_equal = FALSE;
    if (i > 0 && i < h.source.buffers) {
      /* Source probes also see the adjusted sticky SEGMENT replay. Compare
       * against the original segment, not an already shifted running-time. */
      GstClockTime original = gst_segment_to_running_time(
          &h.initial_source_segment, GST_FORMAT_TIME, h.source.pts[i]);
      if (GST_CLOCK_TIME_IS_VALID(original) && h.sink.running[i] >= original &&
          h.sink.running[i] - original == (guint64)h.offset) ++h.shifted_buffers;
      else shift_ok = FALSE;
    }
  }
  ok = pushed == total && released && stats.ready && !stats.creation_failed &&
      !stats.rejected_frames && current == GST_STATE_PLAYING && pending == GST_STATE_VOID_PENDING &&
      h.links == 1 && h.source.buffers > 20 && h.sink.buffers > 20 &&
      h.source.buffers < G_N_ELEMENTS(h.source.pts) && h.sink.buffers < G_N_ELEMENTS(h.sink.pts) &&
      !h.source.violations && !h.sink.violations && pts_equal && shift_ok &&
      GST_CLOCK_TIME_IS_VALID(h.cursor_at_arrival) &&
      h.source_pad && gst_pad_get_offset(h.source_pad) == h.offset &&
      h.sink_pad && gst_pad_get_offset(h.sink_pad) == 0 &&
      (control != E4_RECALCULATE || (recalc_calls == 1 && recalc_ok)) &&
      (control != E4_SOURCE_OFFSET || (h.offset_calls == 1 && h.offset > 0 && h.shifted_buffers > 20));
  *recovered = ok && h.nonzero >= 20 && h.longest_run >= 200 * GST_MSECOND;
  printf("E4 control=%s output_buffers=%u nonzero=%u first_nonzero=%" G_GUINT64_FORMAT
      " last_nonzero_end=%" G_GUINT64_FORMAT " longest_nonzero_span=%" G_GUINT64_FORMAT
      " first_source_running=%" G_GUINT64_FORMAT " first_sink_running=%" G_GUINT64_FORMAT
      " cursor_at_arrival=%" G_GUINT64_FORMAT " clock_running=%" G_GUINT64_FORMAT
      " latency=%" G_GUINT64_FORMAT " min_upstream_latency=%" G_GUINT64_FORMAT
      " offset=%" G_GINT64_FORMAT " offset_calls=%u recalc=%u/%d classification=%s\n",
      names[control], h.output_buffers, h.nonzero, h.first_nonzero, h.last_nonzero_end,
      h.longest_run, h.source.running[0], h.sink.running[0], h.cursor_at_arrival,
      h.clock_running, latency, upstream, h.offset, h.offset_calls, recalc_calls, recalc_ok,
      !ok ? "INVALID_CONTROL" : *recovered ? "RECOVERED" : "NO_RECOVERY");
  printf("E4 integrity source_events=%s sink_events=%s segments=%u/%u violations=%u/%u "
      "buffers=%u/%u pts_equal=%d following_shift_ok=%d following_checked=%u "
      "source_segment_base=%" G_GUINT64_FORMAT " sink_segment_base=%" G_GUINT64_FORMAT
      " pushed=%u/%u dropped=%" G_GUINT64_FORMAT "\n", h.source.order, h.sink.order,
      h.source.segments, h.sink.segments, h.source.violations, h.sink.violations,
      h.source.buffers, h.sink.buffers, pts_equal, shift_ok, h.shifted_buffers,
      h.source.segment.base, h.sink.segment.base, pushed, total, stats.startup_dropped_frames);
  g_mutex_unlock(&h.mutex);
  bus = gst_element_get_bus(parent);
  while ((message = gst_bus_pop_filtered(bus, GST_MESSAGE_ERROR)) != NULL) {
    GError *error = NULL;
    gst_message_parse_error(message, &error, NULL);
    fprintf(stderr, "E4 bus error: %s\n", error ? error->message : "unknown");
    g_clear_error(&error); gst_message_unref(message); ++errors;
  }
  gst_object_unref(bus);
  source_clock_audio_bin_stop(audio);
  source_clock_audio_bin_free(audio);
  gst_element_set_state(parent, GST_STATE_NULL);
  g_signal_handlers_disconnect_by_data(mixer, &h);
  if (h.sink_pad) {
    g_signal_handlers_disconnect_by_data(h.sink_pad, &h);
    gst_pad_remove_probe(h.sink_pad, h.sink_probe); gst_object_unref(h.sink_pad);
  }
  if (h.source_pad) {
    gst_pad_remove_probe(h.source_pad, h.source_probe); gst_object_unref(h.source_pad);
  }
  gst_pad_remove_probe(out, out_probe);
  gst_object_unref(out); gst_object_unref(hold); gst_object_unref(mixer); gst_object_unref(parent);
  g_main_context_unref(context); g_ptr_array_unref(frames); g_mutex_clear(&h.mutex);
  if (errors) *recovered = FALSE;
  CHECK(ok && errors == 0);
  return TRUE;
}

static gboolean startup_audio_recovery_controls(void) {
  gboolean recovered[3] = {FALSE, FALSE, FALSE}, valid = TRUE;
  guint i;
  for (i = 0; i < 3; ++i) {
    gboolean result = e4_case((E4Control)i, &recovered[i]);
    valid = result && valid; /* Always run all three independent controls. */
  }
  printf("E4 ranking timebase-preserving: 1=recalculate(%d) 2=bounded-mixer-latency(%d) "
      "3=measured-source-offset(%d; audio-only offset is NOT AV qualification) result=%s\n",
      recovered[0], recovered[1], recovered[2], !valid ? "INVALID_CONTROL" :
      (recovered[0] || recovered[1] || recovered[2]) ? "RECOVERY_OBSERVED" : "NO_RECOVERY");
  CHECK(valid);
  return TRUE;
}

typedef struct {
  GstElement *parent;
  GstElement *mixer;
  GstElement *hold;
  GstPad *output;
  GMainContext *context;
  SourceClockAudioBin *audio;
  GPtrArray *frames;
  E4Harness harness;
  GstClockTime frame_duration;
  guint hold_ms;
  guint min_upstream_latency_ms;
  guint total_frames;
  guint pushed;
  guint flow_failures;
  guint bus_errors;
  gboolean released;
  gboolean release_ok;
  gboolean state_ok;
  gboolean stats_ok;
  gboolean integrity_ok;
  GstStateChangeReturn request_result;
  GstStateChangeReturn startup_result;
  GstStateChangeReturn final_result;
  GstState startup_current;
  GstState startup_pending;
  GstState final_current;
  GstState final_pending;
  GstClockTime configured_latency;
  GstClockTime configured_min_upstream_latency;
  gboolean mutex_initialized;
  gulong output_probe;
} E5LatencyCase;

static gboolean e5_boundary_ok(const E4Boundary *boundary) {
  return boundary->streams == 1 && boundary->caps == 1 && boundary->segments == 1 &&
      boundary->violations == 0 && boundary->order_length == 4 &&
      strcmp(boundary->order, "SCGB") == 0 && boundary->buffers > 0;
}

static gboolean e5_case(guint hold_ms, guint min_upstream_latency_ms,
                        gboolean *integrity_ok) {
  E5LatencyCase value = {0};
  SourceClockAudioFormat input = format();
  SourceClockAudioBinStats stats = {0};
  GstState current = GST_STATE_VOID_PENDING, pending = GST_STATE_VOID_PENDING;
  GstBus *bus = NULL;
  GstMessage *message;
  gint64 start, deadline;
  guint i;
  gboolean pts_equal = TRUE;
  gboolean pass = FALSE;
  const char *description =
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=mix ignore-inactive-pads=true ! fakesink sync=false async=false "
      "appsrc name=preroll_hold is-live=false format=time ! "
      "audio/x-raw,format=S16LE,rate=8000,channels=1,layout=interleaved ! "
      "fakesink async=true sync=false";

  value.hold_ms = hold_ms;
  value.min_upstream_latency_ms = min_upstream_latency_ms;
  value.parent = gst_parse_launch(description, NULL);
  value.mixer = value.parent == NULL ? NULL :
      gst_bin_get_by_name(GST_BIN(value.parent), "mix");
  value.hold = value.parent == NULL ? NULL :
      gst_bin_get_by_name(GST_BIN(value.parent), "preroll_hold");
  value.output = value.mixer == NULL ? NULL :
      gst_element_get_static_pad(value.mixer, "src");
  value.context = g_main_context_new();
  value.frames = aac_material();
  value.frame_duration = gst_util_uint64_scale(1024, GST_SECOND, 44100);
  value.total_frames = (guint)gst_util_uint64_scale_ceil(
      (hold_ms > 1000 ? 4400 : 3200) * GST_MSECOND, 44100,
      1024 * GST_SECOND);
  value.startup_current = value.final_current = GST_STATE_VOID_PENDING;
  value.startup_pending = value.final_pending = GST_STATE_VOID_PENDING;
  if (value.parent == NULL || value.mixer == NULL || value.hold == NULL ||
      value.output == NULL || value.frames == NULL || value.frames->len != 12)
    goto cleanup;

  value.harness.parent = value.parent;
  value.harness.control = E4_RECALCULATE;
  value.harness.clock_running = value.harness.cursor = GST_CLOCK_TIME_NONE;
  value.harness.cursor_at_arrival = GST_CLOCK_TIME_NONE;
  value.harness.first_nonzero = value.harness.last_nonzero_end =
      value.harness.run_start = value.harness.run_end = GST_CLOCK_TIME_NONE;
  g_mutex_init(&value.harness.mutex);
  value.mutex_initialized = TRUE;
  gst_segment_init(&value.harness.source.segment, GST_FORMAT_TIME);
  gst_segment_init(&value.harness.sink.segment, GST_FORMAT_TIME);
  g_object_set(value.mixer, "latency", (guint64)(100 * GST_MSECOND),
      "min-upstream-latency", (guint64)(min_upstream_latency_ms * GST_MSECOND), NULL);
  g_object_get(value.mixer, "latency", &value.configured_latency,
      "min-upstream-latency", &value.configured_min_upstream_latency, NULL);
  value.output_probe = gst_pad_add_probe(value.output, GST_PAD_PROBE_TYPE_BUFFER,
      e4_output_probe, &value.harness, NULL);
  g_signal_connect(value.mixer, "pad-added", G_CALLBACK(e4_pad_added), &value.harness);
  value.audio = source_clock_audio_bin_new(value.parent, value.mixer, value.context);
  if (value.audio == NULL) goto cleanup;

  value.request_result = gst_element_set_state(value.parent, GST_STATE_PLAYING);
  value.startup_result = gst_element_get_state(value.parent, &value.startup_current,
      &value.startup_pending, 200 * GST_MSECOND);
  if (value.request_result != GST_STATE_CHANGE_ASYNC ||
      value.startup_result != GST_STATE_CHANGE_ASYNC ||
      value.startup_current == GST_STATE_PLAYING ||
      value.startup_pending != GST_STATE_PLAYING)
    goto cleanup;
  input.sample_rate = 44100;
  input.channels = 1;
  if (!source_clock_audio_bin_configure(value.audio, &input, NULL)) goto cleanup;

  start = g_get_monotonic_time();
  for (i = 0; i < value.total_frames; ++i) {
    GstBuffer *buffer = gst_buffer_copy_deep(g_ptr_array_index(value.frames, i % value.frames->len));
    gint64 target = start + (gint64)gst_util_uint64_scale(i + 1,
        1024 * G_USEC_PER_SEC, 44100);
    GST_BUFFER_PTS(buffer) = i * value.frame_duration;
    GST_BUFFER_DURATION(buffer) = value.frame_duration;
    if (source_clock_audio_bin_push(value.audio, buffer) == GST_FLOW_OK)
      ++value.pushed;
    else
      ++value.flow_failures;
    while (g_main_context_iteration(value.context, FALSE)) {}
    while (g_get_monotonic_time() < target) {
      gint64 remaining = target - g_get_monotonic_time();
      while (g_main_context_iteration(value.context, FALSE)) {}
      if (remaining > 0) g_usleep((gulong)MIN(remaining, 1000));
    }
    if (!value.released && (i + 1) * value.frame_duration >= hold_ms * GST_MSECOND) {
      GstBuffer *preroll = gst_buffer_new_allocate(NULL, 320, NULL);
      gst_buffer_memset(preroll, 0, 0, 320);
      GST_BUFFER_PTS(preroll) = 0;
      GST_BUFFER_DURATION(preroll) = 20 * GST_MSECOND;
      value.release_ok = gst_app_src_push_buffer(GST_APP_SRC(value.hold), preroll) == GST_FLOW_OK &&
          gst_app_src_end_of_stream(GST_APP_SRC(value.hold)) == GST_FLOW_OK;
      value.released = TRUE;
    }
  }
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  while (g_get_monotonic_time() < deadline) {
    while (g_main_context_iteration(value.context, FALSE)) {}
    g_usleep(1000);
  }
  source_clock_audio_bin_get_stats(value.audio, &stats);
  value.final_result = gst_element_get_state(value.parent, &value.final_current,
      &value.final_pending, 0);
  value.state_ok = value.final_result != GST_STATE_CHANGE_FAILURE &&
      value.final_current == GST_STATE_PLAYING &&
      value.final_pending == GST_STATE_VOID_PENDING;
  value.stats_ok = stats.configured && stats.ready && !stats.creation_failed &&
      !stats.waiting_for_parent_playing && stats.pending_frames == 0 &&
      stats.rejected_frames == 0;
  for (i = 0; i < MIN(value.harness.source.buffers, value.harness.sink.buffers); ++i)
    if (value.harness.source.pts[i] != value.harness.sink.pts[i]) pts_equal = FALSE;
  value.integrity_ok = value.startup_current != GST_STATE_PLAYING &&
      value.startup_pending == GST_STATE_PLAYING && value.pushed == value.total_frames &&
      value.flow_failures == 0 && value.released && value.release_ok && value.state_ok &&
      value.stats_ok && value.harness.links == 1 && value.harness.source.buffers > 20 &&
      value.harness.sink.buffers > 20 && e5_boundary_ok(&value.harness.source) &&
      e5_boundary_ok(&value.harness.sink) && pts_equal && value.harness.source_pad != NULL &&
      value.harness.sink_pad != NULL && gst_pad_get_offset(value.harness.source_pad) == 0 &&
      gst_pad_get_offset(value.harness.sink_pad) == 0;
  pass = value.integrity_ok && value.harness.longest_run >= 1500 * GST_MSECOND;
  bus = gst_element_get_bus(value.parent);
  while ((message = gst_bus_pop_filtered(bus, GST_MESSAGE_ERROR)) != NULL) {
    GError *error = NULL;
    gst_message_parse_error(message, &error, NULL);
    fprintf(stderr, "E5 bus error hold=%u min=%u: %s\n", hold_ms,
        min_upstream_latency_ms, error == NULL ? "unknown" : error->message);
    g_clear_error(&error);
    gst_message_unref(message);
    ++value.bus_errors;
  }
  gst_object_unref(bus);
  bus = NULL;
  value.integrity_ok = value.integrity_ok && value.bus_errors == 0;
  pass = value.integrity_ok && value.harness.longest_run >= 1500 * GST_MSECOND;

cleanup:
  if (integrity_ok != NULL) *integrity_ok = value.integrity_ok && value.bus_errors == 0;
  printf("E5 hold_ms=%u min_upstream_latency_ms=%u configured_latency=%" G_GUINT64_FORMAT
      " configured_min=%" G_GUINT64_FORMAT " first_input_running=%" G_GUINT64_FORMAT
      " cursor_at_first_input=%" G_GUINT64_FORMAT " output_buffers=%u nonzero=%u"
      " longest_nonzero_span=%" G_GUINT64_FORMAT " source_events=%s sink_events=%s"
      " source_violations=%u sink_violations=%u pts_equal=%d pad_offset=%" G_GINT64_FORMAT "/%" G_GINT64_FORMAT
      " pushed=%u/%u flow_failures=%u state_ok=%d integrity=%d result=%s\n",
      hold_ms, min_upstream_latency_ms, value.configured_latency,
      value.configured_min_upstream_latency, value.harness.source.running[0],
      value.harness.cursor_at_arrival, value.harness.output_buffers,
      value.harness.nonzero, value.harness.longest_run, value.harness.source.order,
      value.harness.sink.order, value.harness.source.violations,
      value.harness.sink.violations, pts_equal,
      value.harness.source_pad == NULL ? 0 : gst_pad_get_offset(value.harness.source_pad),
      value.harness.sink_pad == NULL ? 0 : gst_pad_get_offset(value.harness.sink_pad),
      value.pushed, value.total_frames, value.flow_failures, value.state_ok,
      value.integrity_ok && value.bus_errors == 0, pass ? "PASS" : "FAIL");
  if (bus != NULL) gst_object_unref(bus);
  if (value.audio != NULL) {
    source_clock_audio_bin_stop(value.audio);
    source_clock_audio_bin_free(value.audio);
  }
  if (value.parent != NULL) gst_element_set_state(value.parent, GST_STATE_NULL);
  if (value.mixer != NULL) g_signal_handlers_disconnect_by_data(value.mixer, &value.harness);
  if (value.harness.sink_pad != NULL) {
    g_signal_handlers_disconnect_by_data(value.harness.sink_pad, &value.harness);
    if (value.harness.sink_probe != 0)
      gst_pad_remove_probe(value.harness.sink_pad, value.harness.sink_probe);
    gst_object_unref(value.harness.sink_pad);
  }
  if (value.harness.source_pad != NULL) {
    if (value.harness.source_probe != 0)
      gst_pad_remove_probe(value.harness.source_pad, value.harness.source_probe);
    gst_object_unref(value.harness.source_pad);
  }
  if (value.output != NULL && value.output_probe != 0)
    gst_pad_remove_probe(value.output, value.output_probe);
  if (value.mutex_initialized) g_mutex_clear(&value.harness.mutex);
  if (value.audio == NULL && value.parent != NULL)
    g_signal_handlers_disconnect_by_data(value.parent, &value.harness);
  if (value.output != NULL) gst_object_unref(value.output);
  if (value.hold != NULL) gst_object_unref(value.hold);
  if (value.mixer != NULL) gst_object_unref(value.mixer);
  if (value.parent != NULL) gst_object_unref(value.parent);
  if (value.context != NULL) g_main_context_unref(value.context);
  if (value.frames != NULL) g_ptr_array_unref(value.frames);
  return pass;
}

static gboolean startup_audio_latency_sweep(void) {
  /* Keep the regression sweep around the measured boundary. Wider exploratory
   * sweeps belong in a targeted diagnostic run, not the default CTest gate. */
  static const guint values[] = {100, 200, 300};
  gboolean pass_850[G_N_ELEMENTS(values)] = {FALSE};
  gboolean pass_2000[G_N_ELEMENTS(values)] = {FALSE};
  gboolean integrity_850, integrity_2000;
  gint smallest = -1, next_higher = -1;
  guint i;
  for (i = 0; i < G_N_ELEMENTS(values); ++i) {
    pass_850[i] = e5_case(850, values[i], &integrity_850);
    pass_2000[i] = e5_case(2000, values[i], &integrity_2000);
    if (!integrity_850 || !integrity_2000) {
      fprintf(stderr, "E5 integrity failure at min_upstream_latency_ms=%u\n", values[i]);
    }
    printf("E5 sweep min_upstream_latency_ms=%u hold_850=%s hold_2000=%s\n",
        values[i], pass_850[i] ? "PASS" : "FAIL", pass_2000[i] ? "PASS" : "FAIL");
  }
  for (i = 0; i < G_N_ELEMENTS(values); ++i) {
    if (pass_850[i] && pass_2000[i]) {
      if (smallest < 0) smallest = (gint)values[i];
      else if (next_higher < 0) next_higher = (gint)values[i];
    }
  }
  printf("E5 decision smallest_passing_both_ms=%d next_higher_passing_ms=%d %s\n",
      smallest, next_higher, smallest < 0 ? "NO_BOUNDED_THRESHOLD" : "diagnostic_only");
  return TRUE;
}

typedef struct { SourceClockAudioBin *audio; SourceClockAudioFormat format; gboolean ok; } ConfigureCall;
static gpointer configure_twice(gpointer data) {
  ConfigureCall *call = data;
  call->ok = source_clock_audio_bin_configure(call->audio, &call->format, NULL);
  call->ok = source_clock_audio_bin_configure(call->audio, &call->format, NULL) && call->ok;
  return NULL;
}

/* Catches missing decode/drain, wrong input caps, parent clock reset, duplicate
 * reservations, wrong-context creation, and leaking the requested mixer pad. */
static gboolean pcm_dynamic_case(gboolean other_thread) {
  GstElement *parent = gst_parse_launch(
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=mix ignore-inactive-pads=true ! fakesink sync=false async=false", NULL);
  GstElement *mixer = gst_bin_get_by_name(GST_BIN(parent), "mix");
  GMainContext *context = g_main_context_new();
  PCMObservation seen = {0};
  SourceClockAudioBin *audio;
  SourceClockAudioBinStats stats;
  ConfigureCall call;
  GPtrArray *frames = aac_material();
  GstClock *before_clock, *after_clock;
  GstClockTime before_base;
  guint before_children = GST_BIN_NUMCHILDREN(parent), before_pads = GST_ELEMENT(mixer)->numsinkpads, i;
  gint64 deadline;
  gboolean result, settings_ok, stats_ok, pcm_ok, context_ok, clock_ok, cleanup_ok;
  gboolean pts_ok, appsrc_failure_ok;
  GstClockTime last_fixture_pts = gst_util_uint64_scale(11 * 1024, GST_SECOND, 44100);
  GstClockTime frame_duration = gst_util_uint64_scale(1024, GST_SECOND, 44100);
  GstFlowReturn backward_flow, same_flow, forward_flow, appsrc_failure_flow;
  GstElement *input;
  guint pts_freed = 0, appsrc_failure_freed = 0;
  guint64 rejected_before;
  CHECK(frames->len == 12);
  seen.context = context;
  gst_element_set_state(parent, GST_STATE_PLAYING);
  gst_element_get_state(parent, NULL, NULL, 2 * GST_SECOND);
  before_clock = gst_element_get_clock(parent);
  before_base = gst_element_get_base_time(parent);
  g_signal_connect(mixer, "pad-added", G_CALLBACK(observe_real_pad), &seen);
  g_signal_connect(parent, "element-added", G_CALLBACK(observe_branch_lifetime), &seen);
  audio = source_clock_audio_bin_new(parent, mixer, context);
  CHECK(GST_BIN_NUMCHILDREN(parent) == before_children);
  call.audio = audio; call.format = format(); call.format.sample_rate = 44100; call.format.channels = 1;
  if (other_thread) {
    GThread *thread = g_thread_new("configure-only", configure_twice, &call);
    g_thread_join(thread);
  } else {
    /* invoke() could run inline here. No topology mutation may escape dispatch. */
    g_main_context_push_thread_default(context);
    configure_twice(&call);
    g_main_context_pop_thread_default(context);
  }
  CHECK(call.ok);
  CHECK(GST_BIN_NUMCHILDREN(parent) == before_children);
  source_clock_audio_bin_get_stats(audio, &stats);
  CHECK(stats.configure_operations == 1 && !stats.ready);
  for (i = 0; i < frames->len; ++i) {
    GstFlowReturn flow = source_clock_audio_bin_push(audio, gst_buffer_ref(g_ptr_array_index(frames, i)));
    CHECK(flow == GST_FLOW_OK);
  }
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  do {
    while (g_main_context_iteration(context, FALSE)) {}
    if (g_atomic_int_get(&seen.pcm_buffers) >= 8) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  after_clock = gst_element_get_clock(parent);
  source_clock_audio_bin_get_stats(audio, &stats);
  settings_ok = check_branch_settings(parent, before_base);
  stats_ok = stats.configure_operations == 1 &&
      stats.ready && !stats.creation_failed && stats.pending_frames == 0;
  pcm_ok = g_atomic_int_get(&seen.pcm_buffers) > 0 &&
      g_atomic_int_get(&seen.nonzero_buffers) > 0 && g_atomic_int_get(&seen.bad_caps) == 0;
  context_ok = seen.wrong_context == 0 && seen.added == 1;
  clock_ok = before_clock == after_clock && before_base == gst_element_get_base_time(parent);
  rejected_before = stats.rejected_frames;
  backward_flow = source_clock_audio_bin_push(audio,
      frame(10, 0, frame_duration, &pts_freed));
  source_clock_audio_bin_get_stats(audio, &stats);
  pts_ok = backward_flow == GST_FLOW_ERROR && pts_freed == 1 &&
      stats.rejected_frames == rejected_before + 1;
  same_flow = source_clock_audio_bin_push(audio,
      frame(10, last_fixture_pts, frame_duration, NULL));
  forward_flow = source_clock_audio_bin_push(audio,
      frame(10, last_fixture_pts + frame_duration, frame_duration, NULL));
  pts_ok = pts_ok && same_flow == GST_FLOW_OK && forward_flow == GST_FLOW_OK;
  input = find_appsrc(parent);
  CHECK(input != NULL);
  CHECK(gst_app_src_end_of_stream(GST_APP_SRC(input)) == GST_FLOW_OK);
  gst_object_unref(input);
  source_clock_audio_bin_get_stats(audio, &stats);
  rejected_before = stats.rejected_frames;
  appsrc_failure_flow = source_clock_audio_bin_push(audio,
      frame(10, last_fixture_pts + 2 * frame_duration, frame_duration,
            &appsrc_failure_freed));
  source_clock_audio_bin_get_stats(audio, &stats);
  appsrc_failure_ok = appsrc_failure_flow != GST_FLOW_OK &&
      appsrc_failure_freed == 1 && stats.rejected_frames == rejected_before + 1;
  source_clock_audio_bin_stop(audio);
  drain_bus(parent);
  cleanup_ok = GST_BIN_NUMCHILDREN(parent) == before_children &&
      GST_ELEMENT(mixer)->numsinkpads == before_pads && seen.removed == 1;
  result = settings_ok && stats_ok && pcm_ok && context_ok && clock_ok &&
      pts_ok && appsrc_failure_ok && cleanup_ok;
  printf("PCM %s: buffers=%d nonzero=%d bad_caps=%d additions=%d wrong_context=%d "
      "settings=%d stats=%d pcm=%d context=%d clock=%d pts=%d appsrc_failure=%d "
      "cleanup=%d removed=%d children=%u/%u pads=%u/%u\n",
      other_thread ? "worker" : "owner", seen.pcm_buffers, seen.nonzero_buffers,
      seen.bad_caps, seen.added, seen.wrong_context, settings_ok, stats_ok, pcm_ok,
      context_ok, clock_ok, pts_ok, appsrc_failure_ok, cleanup_ok, seen.removed,
      GST_BIN_NUMCHILDREN(parent),
      before_children, GST_ELEMENT(mixer)->numsinkpads, before_pads);
  source_clock_audio_bin_free(audio);
  gst_object_unref(before_clock); gst_object_unref(after_clock);
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_object_unref(mixer); gst_object_unref(parent);
  g_main_context_unref(context); g_ptr_array_unref(frames);
  CHECK(result);
  return TRUE;
}
static gboolean pcm_owner(void) { return pcm_dynamic_case(FALSE); }
static gboolean pcm_worker(void) { return pcm_dynamic_case(TRUE); }

static GstPadProbeReturn count_silence(GstPad *pad, GstPadProbeInfo *info, gpointer data) {
  (void)pad; (void)info;
  g_atomic_int_inc((gint *)data);
  return GST_PAD_PROBE_OK;
}

/* A real video mixer accepts a request pad but rejects PCM linking. This tests
 * rollback after child creation/addition and pad acquisition, not a mock flag. */
static gboolean partial_link_failure(void) {
  GstElement *parent = gst_parse_launch(
      "audiotestsrc is-live=true wave=silence ! fakesink name=silence sync=false async=false", NULL);
  GstElement *wrong_mixer = gst_element_factory_make("compositor", NULL);
  GstElement *sink = gst_bin_get_by_name(GST_BIN(parent), "silence");
  GstPad *sinkpad = gst_element_get_static_pad(sink, "sink");
  GMainContext *context = g_main_context_new();
  PCMObservation seen = {0};
  SourceClockAudioBin *audio;
  SourceClockAudioBinStats stats;
  SourceClockAudioFormat input = format();
  guint children, pads, freed = 0;
  gint silent_buffers = 0, before;
  gint64 deadline;
  CHECK(wrong_mixer != NULL);
  /* Keep the deliberately disconnected video mixer from holding parent state. */
  gst_element_set_locked_state(wrong_mixer, TRUE);
  gst_bin_add(GST_BIN(parent), wrong_mixer);
  gst_pad_add_probe(sinkpad, GST_PAD_PROBE_TYPE_BUFFER, count_silence, &silent_buffers, NULL);
  gst_element_set_state(parent, GST_STATE_PLAYING);
  children = GST_BIN_NUMCHILDREN(parent); pads = GST_ELEMENT(wrong_mixer)->numsinkpads;
  seen.context = context;
  g_signal_connect(parent, "element-added", G_CALLBACK(observe_branch_lifetime), &seen);
  audio = source_clock_audio_bin_new(parent, wrong_mixer, context);
  CHECK(source_clock_audio_bin_configure(audio, &input, NULL));
  CHECK(source_clock_audio_bin_push(audio, frame(10, 0, GST_MSECOND, &freed)) == GST_FLOW_OK);
  while (g_main_context_iteration(context, FALSE)) {}
  source_clock_audio_bin_get_stats(audio, &stats);
  CHECK(stats.creation_failed && !stats.ready && stats.pending_frames == 0 && freed == 1);
  CHECK(stats.configure_operations == 1 && seen.added == 1 && seen.removed == 1);
  CHECK(GST_BIN_NUMCHILDREN(parent) == children && GST_ELEMENT(wrong_mixer)->numsinkpads == pads);
  CHECK(source_clock_audio_bin_push(audio, frame(10, 0, GST_MSECOND, &freed)) == GST_FLOW_ERROR && freed == 2);
  CHECK(source_clock_audio_bin_configure(audio, &input, NULL));
  CHECK(!g_main_context_pending(context)); /* Same format does not retry. */
  before = g_atomic_int_get(&silent_buffers);
  deadline = g_get_monotonic_time() + G_TIME_SPAN_SECOND;
  while (g_atomic_int_get(&silent_buffers) == before && g_get_monotonic_time() < deadline) g_usleep(1000);
  CHECK(g_atomic_int_get(&silent_buffers) > before);
  source_clock_audio_bin_free(audio);
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_object_unref(sinkpad); gst_object_unref(sink); gst_object_unref(parent);
  g_main_context_unref(context);
  return TRUE;
}

static gboolean cancel_before_dispatch(void) {
  GstElement *parent = gst_pipeline_new(NULL);
  GstElement *mixer = gst_element_factory_make("audiomixer", NULL);
  GMainContext *context = g_main_context_new();
  SourceClockAudioBin *audio;
  SourceClockAudioFormat input = format();
  PCMObservation seen = {0};
  guint freed = 0;
  gst_bin_add(GST_BIN(parent), mixer);
  seen.context = context;
  g_signal_connect(parent, "element-added", G_CALLBACK(observe_branch_lifetime), &seen);
  audio = source_clock_audio_bin_new(parent, mixer, context);
  CHECK(source_clock_audio_bin_configure(audio, &input, NULL));
  CHECK(source_clock_audio_bin_push(audio, frame(10, 0, GST_MSECOND, &freed)) == GST_FLOW_OK);
  CHECK(source_clock_audio_bin_stop(audio) == SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
  CHECK(source_clock_audio_bin_free(audio));
  while (g_main_context_iteration(context, FALSE)) {}
  CHECK(freed == 1 && seen.added == 0 && GST_BIN_NUMCHILDREN(parent) == 1);
  gst_object_unref(parent); g_main_context_unref(context);
  return TRUE;
}

/* Removing cancellation must not construct a branch after the parent starts.
 * Removing oldest-first eviction must either reject input or exceed a bound. */
static gboolean waiting_bounds_and_stop(void) {
  guint mode;
  for (mode = 0; mode < 4; ++mode) {
    GstElement *parent = gst_parse_launch(
        "audiotestsrc is-live=true wave=silence ! audiomixer name=mix ! fakesink async=false sync=false", NULL);
    GstElement *mixer = gst_bin_get_by_name(GST_BIN(parent), "mix");
    GMainContext *context = g_main_context_new();
    SourceClockAudioBin *audio = source_clock_audio_bin_new(parent, mixer, context);
    SourceClockAudioFormat input = format();
    SourceClockAudioBinStats stats;
    guint i, freed = 0, children = GST_BIN_NUMCHILDREN(parent), pads = GST_ELEMENT(mixer)->numsinkpads;
    guint retained = mode == 0 ? 32 : mode == 1 ? 2 : 1;
    gsize bytes = mode == 1 ? 524288 : 10;
    GstClockTime duration = mode == 2 ? 500 * GST_MSECOND : GST_MSECOND;
    GstClockTime step = mode == 3 ? GST_SECOND : duration;
    gint64 deadline;
    CHECK(source_clock_audio_bin_configure(audio, &input, NULL));
    while (g_main_context_iteration(context, FALSE)) {}
    CHECK(!g_main_context_pending(context)); /* Retry source is not a busy idle. */
    for (i = 0; i < 40; ++i)
      CHECK(source_clock_audio_bin_push(audio, frame(bytes, i * step, duration, &freed)) == GST_FLOW_OK);
    source_clock_audio_bin_get_stats(audio, &stats);
    CHECK(!stats.ready && !stats.creation_failed && stats.pending_frames == retained &&
        stats.pending_bytes == retained * bytes && stats.pending_duration == retained * duration &&
        stats.oldest_pts == (40 - retained) * step && stats.rejected_frames == 0 && freed == 40 - retained &&
        stats.waiting_for_parent_playing && stats.startup_dropped_frames == 40 - retained);
    /* Invalid individual frames must not evict valid queued input. */
    CHECK(source_clock_audio_bin_push(audio, frame(1048577, 40 * step, duration, &freed)) == GST_FLOW_CUSTOM_ERROR);
    CHECK(source_clock_audio_bin_push(audio, frame(10, 40 * step, 501 * GST_MSECOND, &freed)) == GST_FLOW_CUSTOM_ERROR);
    CHECK(source_clock_audio_bin_push(audio, frame(10, GST_CLOCK_TIME_NONE, duration, &freed)) == GST_FLOW_ERROR);
    source_clock_audio_bin_get_stats(audio, &stats);
    CHECK(stats.pending_frames == retained && stats.pending_bytes == retained * bytes &&
        stats.oldest_pts == (40 - retained) * step && stats.startup_dropped_frames == 40 - retained &&
        stats.rejected_frames == 3 && freed == 43 - retained);
    CHECK(GST_BIN_NUMCHILDREN(parent) == children && GST_ELEMENT(mixer)->numsinkpads == pads);
    CHECK(source_clock_audio_bin_stop(audio) == SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
    CHECK(source_clock_audio_bin_free(audio));
    CHECK(freed == 43);
    gst_element_set_state(parent, GST_STATE_PLAYING);
    deadline = g_get_monotonic_time() + 100 * G_TIME_SPAN_MILLISECOND;
    while (g_get_monotonic_time() < deadline) {
      while (g_main_context_iteration(context, FALSE)) {}
      g_usleep(1000);
    }
    CHECK(!g_main_context_pending(context) && GST_BIN_NUMCHILDREN(parent) == children &&
        GST_ELEMENT(mixer)->numsinkpads == pads);
    printf("E2 waiting mode=%u retained=%u evicted=%u stopped_freed=%u late_branch=0\n",
        mode, retained, 40 - retained, freed);
    gst_element_set_state(parent, GST_STATE_NULL);
    gst_object_unref(mixer); gst_object_unref(parent); g_main_context_unref(context);
  }
  return TRUE;
}

typedef struct {
  GMutex mutex;
  GCond cond;
  gboolean entered, release, stop_done;
  SourceClockAudioBin *audio;
  GMainContext *context;
  gint64 elapsed;
  SourceClockAudioStopResult stop_result;
} CreationRace;

/* Pause the real construction at GstBin::element-added, outside audio's
 * status mutex. No production test hook and no artificial decoder. */
static void hold_creation(GstBin *parent, GstElement *child, gpointer data) {
  CreationRace *race = data;
  (void)parent; (void)child;
  g_mutex_lock(&race->mutex);
  race->entered = TRUE;
  g_cond_broadcast(&race->cond);
  while (!race->release) g_cond_wait(&race->cond, &race->mutex);
  g_mutex_unlock(&race->mutex);
}

static gpointer dispatch_creation(gpointer data) {
  CreationRace *race = data;
  g_main_context_iteration(race->context, FALSE);
  return NULL;
}

static gpointer stop_during_creation(gpointer data) {
  CreationRace *race = data;
  gint64 start = g_get_monotonic_time();
  race->stop_result = source_clock_audio_bin_stop(race->audio);
  g_mutex_lock(&race->mutex);
  race->elapsed = g_get_monotonic_time() - start;
  race->stop_done = TRUE;
  g_cond_broadcast(&race->cond);
  g_mutex_unlock(&race->mutex);
  return NULL;
}

/* RED: the old stop waits on operation before closing admission and cannot
 * return while construction is held. Always release/join before asserting. */
static gboolean construction_stop_bounded(void) {
  GstElement *parent = gst_pipeline_new(NULL);
  GstElement *mixer = gst_element_factory_make("audiomixer", NULL);
  CreationRace race = {0};
  SourceClockAudioFormat input = format();
  SourceClockAudioBinStats stats;
  GThread *dispatch, *stopper;
  guint baseline_children, baseline_pads, rejected_freed = 0;
  guint64 stopped_generation;
  gboolean bounded, closed;
  GError *error = NULL;
  gint64 deadline;
  g_mutex_init(&race.mutex); g_cond_init(&race.cond);
  race.context = g_main_context_new();
  /* Construction now requires stable PLAYING; keep the unused mixer from
   * participating in parent preroll, then hold the actual element-added call. */
  gst_element_set_locked_state(mixer, TRUE);
  gst_bin_add(GST_BIN(parent), mixer);
  CHECK(gst_element_set_state(parent, GST_STATE_PLAYING) == GST_STATE_CHANGE_SUCCESS);
  baseline_children = GST_BIN_NUMCHILDREN(parent);
  baseline_pads = GST_ELEMENT(mixer)->numsinkpads;
  race.audio = source_clock_audio_bin_new(parent, mixer, race.context);
  g_signal_connect(parent, "element-added", G_CALLBACK(hold_creation), &race);
  CHECK(source_clock_audio_bin_configure(race.audio, &input, NULL));
  dispatch = g_thread_new("creation-dispatch", dispatch_creation, &race);
  g_mutex_lock(&race.mutex);
  deadline = g_get_monotonic_time() + 5 * G_TIME_SPAN_SECOND;
  while (!race.entered && g_cond_wait_until(&race.cond, &race.mutex, deadline)) {}
  g_mutex_unlock(&race.mutex);
  CHECK(race.entered);
  stopper = g_thread_new("bounded-stop", stop_during_creation, &race);
  g_mutex_lock(&race.mutex);
  deadline = g_get_monotonic_time() + 2300 * G_TIME_SPAN_MILLISECOND;
  while (!race.stop_done && g_cond_wait_until(&race.cond, &race.mutex, deadline)) {}
  bounded = race.stop_done;
  g_mutex_unlock(&race.mutex);
  source_clock_audio_bin_get_stats(race.audio, &stats);
  stopped_generation = stats.operation_generation;
  closed = stats.stopped && !stats.ready && !stats.creation_failed;
  CHECK(bounded && race.stop_result == SOURCE_CLOCK_AUDIO_STOP_TIMEOUT && closed);
  CHECK(!source_clock_audio_bin_free(race.audio));
  source_clock_audio_bin_get_stats(race.audio, &stats);
  CHECK(stats.stopped && !stats.ready && !stats.creation_failed &&
      stats.operation_generation == stopped_generation);
  CHECK(!source_clock_audio_bin_configure(race.audio, &input, &error));
  CHECK(error != NULL && error->code == SOURCE_CLOCK_AUDIO_BIN_STOPPED);
  g_clear_error(&error);
  CHECK(source_clock_audio_bin_push(race.audio,
      frame(10, 0, GST_MSECOND, &rejected_freed)) == GST_FLOW_FLUSHING);
  CHECK(rejected_freed == 1);
  g_mutex_lock(&race.mutex);
  race.release = TRUE;
  g_cond_broadcast(&race.cond);
  g_mutex_unlock(&race.mutex);
  g_thread_join(dispatch); g_thread_join(stopper);
  deadline = g_get_monotonic_time() + 2500 * G_TIME_SPAN_MILLISECOND;
  do {
    source_clock_audio_bin_get_stats(race.audio, &stats);
    if (stats.stop_complete || stats.stop_failed) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  CHECK(stats.stop_complete && !stats.stop_failed && stats.stopped &&
      !stats.ready && !stats.creation_failed &&
      stats.operation_generation == stopped_generation);
  CHECK(GST_BIN_NUMCHILDREN(parent) == baseline_children &&
      GST_ELEMENT(mixer)->numsinkpads == baseline_pads);
  CHECK(source_clock_audio_bin_stop(race.audio) == SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
  CHECK(source_clock_audio_bin_free(race.audio));
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_object_unref(parent); g_main_context_unref(race.context);
  g_mutex_clear(&race.mutex); g_cond_clear(&race.cond);
  printf("construction-stop: returned-before-release=%d admission-closed=%d elapsed-us=%" G_GINT64_FORMAT "\n",
      bounded, closed, race.elapsed);
  return TRUE;
}

typedef struct { gint eos, removed_before_eos; } EOSObservation;
static GstPadProbeReturn observe_eos(GstPad *pad, GstPadProbeInfo *info, gpointer data) {
  EOSObservation *seen = data;
  (void)pad;
  if (GST_EVENT_TYPE(GST_PAD_PROBE_INFO_EVENT(info)) == GST_EVENT_EOS)
    g_atomic_int_inc(&seen->eos);
  return GST_PAD_PROBE_OK;
}
static void watch_branch_eos(GstBin *parent, GstElement *child, gpointer data) {
  GstPad *src = gst_element_get_static_pad(child, "src");
  (void)parent;
  gst_pad_add_probe(src, GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM, observe_eos, data, NULL);
  gst_object_unref(src);
}
static void watch_branch_remove(GstBin *parent, GstElement *child, gpointer data) {
  EOSObservation *seen = data;
  (void)parent; (void)child;
  if (!g_atomic_int_get(&seen->eos)) g_atomic_int_inc(&seen->removed_before_eos);
}

/* RED: NULL/removal alone is not evidence that the old branch drained EOS. */
static gboolean normal_eos_stop(void) {
  GstElement *parent = gst_parse_launch(
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=mix ignore-inactive-pads=true ! fakesink sync=false async=false", NULL);
  GstElement *mixer = gst_bin_get_by_name(GST_BIN(parent), "mix");
  GMainContext *context = g_main_context_new();
  SourceClockAudioBin *audio = source_clock_audio_bin_new(parent, mixer, context);
  SourceClockAudioFormat input = format();
  GPtrArray *frames = aac_material();
  EOSObservation eos = {0};
  PCMObservation pcm = {0};
  guint i, children = GST_BIN_NUMCHILDREN(parent), pads = GST_ELEMENT(mixer)->numsinkpads;
  gint64 deadline;
  gboolean result;
  input.sample_rate = 44100; input.channels = 1;
  pcm.context = context;
  g_signal_connect(mixer, "pad-added", G_CALLBACK(observe_real_pad), &pcm);
  g_signal_connect(parent, "element-added", G_CALLBACK(watch_branch_eos), &eos);
  g_signal_connect(parent, "element-removed", G_CALLBACK(watch_branch_remove), &eos);
  gst_element_set_state(parent, GST_STATE_PLAYING);
  CHECK(frames->len == 12 && source_clock_audio_bin_configure(audio, &input, NULL));
  for (i = 0; i < frames->len; ++i)
    CHECK(source_clock_audio_bin_push(audio, gst_buffer_ref(g_ptr_array_index(frames, i))) == GST_FLOW_OK);
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  do {
    while (g_main_context_iteration(context, FALSE)) {}
    if (g_atomic_int_get(&pcm.pcm_buffers) >= 8) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  CHECK(source_clock_audio_bin_stop(audio) == SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
  result = g_atomic_int_get(&pcm.nonzero_buffers) > 0 &&
      g_atomic_int_get(&eos.eos) == 1 && !g_atomic_int_get(&eos.removed_before_eos) &&
      GST_BIN_NUMCHILDREN(parent) == children && GST_ELEMENT(mixer)->numsinkpads == pads;
  printf("normal-stop: eos=%d removed-before-eos=%d\n", eos.eos, eos.removed_before_eos);
  CHECK(source_clock_audio_bin_free(audio));
  g_signal_handlers_disconnect_by_data(parent, &eos);
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_object_unref(mixer); gst_object_unref(parent);
  g_main_context_unref(context); g_ptr_array_unref(frames);
  CHECK(result);
  return TRUE;
}

typedef struct {
  GMutex mutex;
  GCond cond;
  gboolean entered, release;
} EOSHold;

static GstPadProbeReturn hold_eos(GstPad *pad, GstPadProbeInfo *info, gpointer data) {
  EOSHold *hold = data;
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

/* A context owner must never wait for its own lifecycle machinery. The real
 * branch's EOS is held on a streaming thread so PENDING is deterministic. */
static gboolean owner_stop_pending(void) {
  GstElement *parent = gst_parse_launch(
      "audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=mix ignore-inactive-pads=true ! fakesink sync=false async=false", NULL);
  GstElement *mixer = gst_bin_get_by_name(GST_BIN(parent), "mix");
  GMainContext *context = g_main_context_new();
  SourceClockAudioBin *audio = source_clock_audio_bin_new(parent, mixer, context);
  SourceClockAudioFormat input = format();
  SourceClockAudioBinStats stats;
  GPtrArray *frames = aac_material();
  GstElement *appsrc;
  GstObject *branch;
  GstPad *src;
  EOSHold hold = {0};
  gint64 deadline;
  guint i;
  input.sample_rate = 44100; input.channels = 1;
  g_mutex_init(&hold.mutex); g_cond_init(&hold.cond);
  gst_element_set_state(parent, GST_STATE_PLAYING);
  CHECK(frames->len == 12 && source_clock_audio_bin_configure(audio, &input, NULL));
  for (i = 0; i < frames->len; ++i)
    CHECK(source_clock_audio_bin_push(audio,
        gst_buffer_ref(g_ptr_array_index(frames, i))) == GST_FLOW_OK);
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  do {
    while (g_main_context_iteration(context, FALSE)) {}
    source_clock_audio_bin_get_stats(audio, &stats);
    if (stats.ready) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  CHECK(stats.ready);
  appsrc = find_appsrc(parent);
  CHECK(appsrc != NULL);
  branch = gst_object_get_parent(GST_OBJECT(appsrc));
  src = gst_element_get_static_pad(GST_ELEMENT(branch), "src");
  CHECK(src != NULL);
  gst_pad_add_probe(src, GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM, hold_eos, &hold, NULL);
  CHECK(g_main_context_acquire(context));
  CHECK(source_clock_audio_bin_stop(audio) == SOURCE_CLOCK_AUDIO_STOP_PENDING);
  g_main_context_release(context);
  g_mutex_lock(&hold.mutex);
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  while (!hold.entered && g_cond_wait_until(&hold.cond, &hold.mutex, deadline)) {}
  CHECK(hold.entered);
  hold.release = TRUE;
  g_cond_broadcast(&hold.cond);
  g_mutex_unlock(&hold.mutex);
  CHECK(source_clock_audio_bin_stop(audio) == SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
  CHECK(source_clock_audio_bin_free(audio));
  gst_object_unref(src); gst_object_unref(branch); gst_object_unref(appsrc);
  gst_element_set_state(parent, GST_STATE_NULL);
  gst_object_unref(mixer); gst_object_unref(parent);
  g_main_context_unref(context); g_ptr_array_unref(frames);
  g_mutex_clear(&hold.mutex); g_cond_clear(&hold.cond);
  return TRUE;
}

typedef struct {
  GMainContext *context;
  GMutex mutex;
  GCond cond;
  gboolean context_stop;
  gboolean allocation_entered;
  gboolean allocation_release;
  gboolean probe_failed;
  guint allocation_queries;
  GstPad *pad;
  gulong allocation_probe;
  gulong buffer_probe;
  guint added;
  guint removed;
  gint delivered;
  gint nonzero_delivered;
} E8AllocationHarness;

static GstPadProbeReturn e8_count_old_pcm(GstPad *pad, GstPadProbeInfo *info,
                                           gpointer data) {
  E8AllocationHarness *harness = data;
  GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  GstMapInfo map;
  gboolean nonzero = FALSE;
  gsize i;
  (void)pad;
  if (buffer == NULL) return GST_PAD_PROBE_OK;
  if (gst_buffer_map(buffer, &map, GST_MAP_READ)) {
    for (i = 0; i < map.size; ++i) {
      if (map.data[i] != 0) {
        nonzero = TRUE;
        break;
      }
    }
    gst_buffer_unmap(buffer, &map);
  }
  g_atomic_int_inc(&harness->delivered);
  if (nonzero) g_atomic_int_inc(&harness->nonzero_delivered);
  return GST_PAD_PROBE_OK;
}

static GstPadProbeReturn e8_hold_allocation(GstPad *pad, GstPadProbeInfo *info,
                                              gpointer data) {
  E8AllocationHarness *harness = data;
  GstQuery *query = GST_PAD_PROBE_INFO_QUERY(info);
  (void)pad;
  if (query == NULL || GST_QUERY_TYPE(query) != GST_QUERY_ALLOCATION)
    return GST_PAD_PROBE_OK;
  g_mutex_lock(&harness->mutex);
  ++harness->allocation_queries;
  if (!harness->allocation_entered) {
    harness->allocation_entered = TRUE;
    g_cond_broadcast(&harness->cond);
    while (!harness->allocation_release)
      g_cond_wait(&harness->cond, &harness->mutex);
  }
  g_mutex_unlock(&harness->mutex);
  return GST_PAD_PROBE_OK;
}

static void e8_pad_added(GstElement *mixer, GstPad *pad, gpointer data) {
  E8AllocationHarness *harness = data;
  (void)mixer;
  if (GST_PAD_DIRECTION(pad) != GST_PAD_SINK) return;
  g_mutex_lock(&harness->mutex);
  if (harness->pad == NULL) {
    harness->pad = gst_object_ref(pad);
    harness->allocation_probe = gst_pad_add_probe(pad,
        GST_PAD_PROBE_TYPE_QUERY_DOWNSTREAM, e8_hold_allocation, harness, NULL);
    harness->buffer_probe = gst_pad_add_probe(pad,
        GST_PAD_PROBE_TYPE_BUFFER, e8_count_old_pcm, harness, NULL);
    if (harness->allocation_probe == 0 || harness->buffer_probe == 0)
      harness->probe_failed = TRUE;
    ++harness->added;
    g_cond_broadcast(&harness->cond);
  }
  g_mutex_unlock(&harness->mutex);
}

static void e8_pad_removed(GstElement *mixer, GstPad *pad, gpointer data) {
  E8AllocationHarness *harness = data;
  (void)mixer;
  g_mutex_lock(&harness->mutex);
  if (pad == harness->pad) {
    ++harness->removed;
    g_cond_broadcast(&harness->cond);
  }
  g_mutex_unlock(&harness->mutex);
}

static gpointer e8_context_worker(gpointer data) {
  E8AllocationHarness *harness = data;
  for (;;) {
    gboolean stop;
    g_mutex_lock(&harness->mutex);
    stop = harness->context_stop;
    g_mutex_unlock(&harness->mutex);
    if (stop) break;
    g_main_context_iteration(harness->context, TRUE);
  }
  return NULL;
}

typedef struct {
  SourceClockAudioBin *audio;
  E8AllocationHarness *harness;
  GMutex mutex;
  GCond cond;
  gboolean started;
  SourceClockAudioStopResult result;
} E8StopCall;

static gpointer e8_stop_worker(gpointer data) {
  E8StopCall *call = data;
  g_mutex_lock(&call->mutex);
  call->started = TRUE;
  g_cond_broadcast(&call->cond);
  g_mutex_unlock(&call->mutex);
  call->result = source_clock_audio_bin_stop(call->audio);
  return NULL;
}

/* E8-A diagnoses whether replacement loses accepted AAC before mixer input.
 * AAC and decoded PCM buffer counts need not be one-to-one; coverage requires
 * every decoded buffer to be non-silent and at least one per accepted frame. */
static gboolean allocation_hold_format_replacement_diagnostic(void) {
  GstElement *parent = NULL, *mixer = NULL;
  GMainContext *context = NULL;
  SourceClockAudioBin *audio = NULL, *replacement = NULL;
  SourceClockAudioFormat input = format(), changed = format(), snapshot = {0};
  GPtrArray *frames = NULL;
  E8AllocationHarness harness = {0};
  E8StopCall stop_call = {0};
  GThread *context_thread = NULL, *stop_thread = NULL;
  GError *error = NULL;
  GstBus *bus = NULL;
  GstMessage *message;
  GstState current = GST_STATE_VOID_PENDING, pending = GST_STATE_VOID_PENDING;
  GstStateChangeReturn state_result;
  SourceClockAudioBinStats stats = {0};
  guint accepted = 0, bus_errors = 0, baseline_children = 0, baseline_pads = 0;
  gint delivered_while_held = 0;
  guint64 generation_before_stop = 0;
  gint64 deadline;
  gboolean allocation_held = FALSE, stopped = FALSE, control_ok = FALSE;
  gboolean format_change_seen = FALSE, replacement_ok = FALSE;
  gboolean coverage = FALSE, result = FALSE;

#define E8_REQUIRE(expr) do { if (!(expr)) { \
  fprintf(stderr, "%s:%d: E8 harness FAIL: %s\n", __FILE__, __LINE__, #expr); \
  goto e8_cleanup; } } while (0)

  g_mutex_init(&harness.mutex);
  g_cond_init(&harness.cond);
  g_mutex_init(&stop_call.mutex);
  g_cond_init(&stop_call.cond);

  parent = gst_parse_launch(
      "audiotestsrc is-live=true wave=silence ! "
      "audio/x-raw,rate=48000,channels=2 ! "
      "audiomixer name=mix ignore-inactive-pads=true ! "
      "fakesink sync=false async=false", &error);
  E8_REQUIRE(error == NULL && parent != NULL);
  mixer = gst_bin_get_by_name(GST_BIN(parent), "mix");
  context = g_main_context_new();
  harness.context = context;
  E8_REQUIRE(mixer != NULL && context != NULL);
  baseline_children = GST_BIN_NUMCHILDREN(parent);
  baseline_pads = GST_ELEMENT(mixer)->numsinkpads;

  state_result = gst_element_set_state(parent, GST_STATE_PLAYING);
  E8_REQUIRE(state_result != GST_STATE_CHANGE_FAILURE &&
      gst_element_get_state(parent, &current, &pending, 2 * GST_SECOND) !=
          GST_STATE_CHANGE_FAILURE && current == GST_STATE_PLAYING &&
      pending == GST_STATE_VOID_PENDING);

  g_signal_connect(mixer, "pad-added", G_CALLBACK(e8_pad_added), &harness);
  g_signal_connect(mixer, "pad-removed", G_CALLBACK(e8_pad_removed), &harness);
  audio = source_clock_audio_bin_new(parent, mixer, context);
  frames = aac_material();
  input.sample_rate = 44100;
  input.channels = 1;
  changed.sample_rate = 48000;
  changed.channels = 1;
  E8_REQUIRE(audio != NULL && frames != NULL && frames->len == 12);
  E8_REQUIRE(source_clock_audio_bin_configure(audio, &input, &error));
  E8_REQUIRE(error == NULL);
  source_clock_audio_bin_get_stats(audio, &stats);
  generation_before_stop = stats.operation_generation;
  for (guint i = 0; i < frames->len; ++i) {
    if (source_clock_audio_bin_push(audio,
        gst_buffer_copy_deep(g_ptr_array_index(frames, i))) == GST_FLOW_OK)
      ++accepted;
  }
  source_clock_audio_bin_get_stats(audio, &stats);
  E8_REQUIRE(accepted == 12 && stats.pending_frames == 12 &&
      stats.rejected_frames == 0 && g_atomic_int_get(&harness.delivered) == 0);

  context_thread = g_thread_new("e8-context", e8_context_worker, &harness);
  E8_REQUIRE(context_thread != NULL);
  g_mutex_lock(&harness.mutex);
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  while (!harness.allocation_entered && !harness.probe_failed &&
         g_get_monotonic_time() < deadline)
    g_cond_wait_until(&harness.cond, &harness.mutex, deadline);
  allocation_held = harness.allocation_entered && !harness.probe_failed &&
      harness.allocation_queries == 1;
  delivered_while_held = g_atomic_int_get(&harness.delivered);
  g_mutex_unlock(&harness.mutex);
  E8_REQUIRE(allocation_held);
  E8_REQUIRE(delivered_while_held == 0);

  format_change_seen = !source_clock_audio_bin_configure(audio, &changed, &error) &&
      error != NULL && error->domain == SOURCE_CLOCK_AUDIO_BIN_ERROR &&
      error->code == SOURCE_CLOCK_AUDIO_BIN_FORMAT_CHANGE;
  g_clear_error(&error);
  E8_REQUIRE(format_change_seen);

  stop_call.audio = audio;
  stop_call.harness = &harness;
  stop_thread = g_thread_new("e8-stop", e8_stop_worker, &stop_call);
  E8_REQUIRE(stop_thread != NULL);
  g_mutex_lock(&stop_call.mutex);
  deadline = g_get_monotonic_time() + G_TIME_SPAN_SECOND;
  while (!stop_call.started && g_get_monotonic_time() < deadline)
    g_cond_wait_until(&stop_call.cond, &stop_call.mutex, deadline);
  if (!stop_call.started) {
    g_mutex_unlock(&stop_call.mutex);
    fprintf(stderr, "%s:%d: E8 harness FAIL: stop_call.started\n",
        __FILE__, __LINE__);
    goto e8_cleanup;
  }
  g_mutex_unlock(&stop_call.mutex);
  deadline = g_get_monotonic_time() + G_TIME_SPAN_SECOND;
  do {
    source_clock_audio_bin_get_stats(audio, &stats);
    stopped = stats.stopped && stats.operation_generation > generation_before_stop;
    if (!stopped) g_usleep(1000);
  } while (!stopped && g_get_monotonic_time() < deadline);
  E8_REQUIRE(stopped);
  g_mutex_lock(&harness.mutex);
  harness.allocation_release = TRUE;
  g_cond_broadcast(&harness.cond);
  g_mutex_unlock(&harness.mutex);
  g_thread_join(stop_thread);
  stop_thread = NULL;
  E8_REQUIRE(stop_call.result == SOURCE_CLOCK_AUDIO_STOP_COMPLETE);
  E8_REQUIRE(source_clock_audio_bin_free(audio));
  audio = NULL;
  E8_REQUIRE(harness.added == 1 && harness.removed == 1 &&
      GST_BIN_NUMCHILDREN(parent) == baseline_children &&
      GST_ELEMENT(mixer)->numsinkpads == baseline_pads);

  replacement = source_clock_audio_bin_new(parent, mixer, context);
  replacement_ok = replacement != NULL &&
      source_clock_audio_bin_configure(replacement, &changed, &error) &&
      error == NULL && source_clock_audio_bin_get_format(replacement, &snapshot) &&
      snapshot.sample_rate == 48000 && snapshot.channels == 1;
  E8_REQUIRE(replacement_ok);

  state_result = gst_element_get_state(parent, &current, &pending, 0);
  E8_REQUIRE(state_result != GST_STATE_CHANGE_FAILURE &&
      current == GST_STATE_PLAYING && pending == GST_STATE_VOID_PENDING);
  bus = gst_element_get_bus(parent);
  while ((message = gst_bus_pop(bus)) != NULL) {
    if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_ERROR) ++bus_errors;
    gst_message_unref(message);
  }
  gst_object_unref(bus);
  bus = NULL;
  control_ok = allocation_held && accepted == 12 && stopped &&
      stop_call.result == SOURCE_CLOCK_AUDIO_STOP_COMPLETE && bus_errors == 0 &&
      harness.added == 1 && harness.removed == 1 &&
      delivered_while_held == 0 && format_change_seen && replacement_ok &&
      current == GST_STATE_PLAYING && pending == GST_STATE_VOID_PENDING;
  coverage = g_atomic_int_get(&harness.delivered) >= (gint)accepted &&
      g_atomic_int_get(&harness.nonzero_delivered) ==
          g_atomic_int_get(&harness.delivered);
  result = control_ok && coverage;

e8_cleanup:
  if (error != NULL) g_clear_error(&error);
  source_clock_audio_format_clear(&snapshot);
  g_mutex_lock(&harness.mutex);
  harness.allocation_release = TRUE;
  g_cond_broadcast(&harness.cond);
  g_mutex_unlock(&harness.mutex);
  if (stop_thread != NULL) {
    g_thread_join(stop_thread);
    stop_thread = NULL;
  }
  if (audio != NULL) {
    source_clock_audio_bin_stop(audio);
    if (source_clock_audio_bin_free(audio)) audio = NULL;
  }
  if (replacement != NULL) {
    source_clock_audio_bin_stop(replacement);
    source_clock_audio_bin_free(replacement);
    replacement = NULL;
  }
  if (mixer != NULL) g_signal_handlers_disconnect_by_data(mixer, &harness);
  if (harness.pad != NULL) {
    if (harness.allocation_probe != 0)
      gst_pad_remove_probe(harness.pad, harness.allocation_probe);
    if (harness.buffer_probe != 0)
      gst_pad_remove_probe(harness.pad, harness.buffer_probe);
    gst_object_unref(harness.pad);
    harness.pad = NULL;
  }
  if (context_thread != NULL) {
    g_mutex_lock(&harness.mutex);
    harness.context_stop = TRUE;
    g_mutex_unlock(&harness.mutex);
    g_main_context_wakeup(context);
    g_thread_join(context_thread);
    context_thread = NULL;
  }
  if (bus != NULL) gst_object_unref(bus);
  if (parent != NULL) {
    gst_element_set_state(parent, GST_STATE_NULL);
    gst_object_unref(parent);
  }
  if (mixer != NULL) gst_object_unref(mixer);
  if (context != NULL) g_main_context_unref(context);
  if (frames != NULL) g_ptr_array_unref(frames);
  g_mutex_clear(&stop_call.mutex);
  g_cond_clear(&stop_call.cond);
  g_mutex_clear(&harness.mutex);
  g_cond_clear(&harness.cond);
  printf("E8-A allocation-held=%d queries=%u accepted=%u held-delivered=%d delivered=%d "
      "nonzero_delivered=%d stopped=%d generation=%" G_GUINT64_FORMAT
      " format-change=%d replacement=%d added=%u removed=%u bus_errors=%u control=%d coverage=%d\n",
      allocation_held, harness.allocation_queries, accepted, delivered_while_held,
      g_atomic_int_get(&harness.delivered),
      g_atomic_int_get(&harness.nonzero_delivered), stopped, stats.operation_generation,
      format_change_seen, replacement_ok, harness.added, harness.removed,
      bus_errors, control_ok, coverage);
  return result;
#undef E8_REQUIRE
}

int main(int argc, char **argv) {
  static const struct { const char *name; gboolean (*run)(void); } tests[] = {
    {"lifecycle-snapshot", lifecycle_snapshot}, {"validation", validation},
    {"aac-eld-validation", aac_eld_validation},
    {"aac-eld-branch-caps", aac_eld_branch_caps},
    {"bounds", bounds}, {"invalid-buffers", invalid_buffers},
    {"references-timestamps", references_and_timestamps},
    {"pcm-owner-context", pcm_owner}, {"pcm-worker-context", pcm_worker},
    {"startup-audio-continuity-control", startup_audio_continuity_control},
    {"startup-audio-continuity-async", startup_audio_continuity_async},
    {"startup-audio-continuity-long-async", startup_audio_continuity_long_async},
    {"startup-audio-continuity-queued-tail", startup_audio_continuity_queued_tail},
    {"startup-audio-time-axis-diagnostic", startup_audio_time_axis_diagnostic},
    {"startup-audio-recovery-controls", startup_audio_recovery_controls},
    {"startup-audio-latency-sweep", startup_audio_latency_sweep},
    {"waiting-bounds-and-stop", waiting_bounds_and_stop},
    {"partial-link-failure", partial_link_failure}, {"cancel-before-dispatch", cancel_before_dispatch},
    {"construction-stop-bounded", construction_stop_bounded}, {"normal-eos-stop", normal_eos_stop},
    {"owner-stop-pending", owner_stop_pending},
    {"allocation-hold-format-replacement-diagnostic", allocation_hold_format_replacement_diagnostic}
  };
  guint i, selected = 0;
  int failures = 0;
  gst_init(&argc, &argv);
  for (i = 0; i < G_N_ELEMENTS(tests); ++i) {
    if (argc > 1 && strcmp(argv[1], tests[i].name) != 0) continue;
    ++selected;
    gboolean ok = tests[i].run();
    printf("%s: %s\n", tests[i].name, ok ? "PASS" : "FAIL");
    if (!ok) ++failures;
  }
  gst_deinit();
  if (argc > 1 && selected == 0) return 2;
  return failures != 0;
}
