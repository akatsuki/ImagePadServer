#include <gst/app/gstappsink.h>
#include <gst/app/gstappsrc.h>
#include <gst/gst.h>
#include <stdio.h>
#include <string.h>

#define MAX_OBSERVED 128u

typedef struct {
  GMutex mutex;
  guint buffers;
  guint nonzero;
  guint invalid_pts;
  guint regressions;
  GstClockTime pts[MAX_OBSERVED];
  GstClockTime duration[MAX_OBSERVED];
} Observation;

static gboolean buffer_nonzero(GstBuffer *buffer) {
  GstMapInfo map;
  gsize i;
  gboolean nonzero = FALSE;
  if (buffer == NULL || !gst_buffer_map(buffer, &map, GST_MAP_READ)) return FALSE;
  for (i = 0; i < map.size; ++i) {
    if (map.data[i] != 0) {
      nonzero = TRUE;
      break;
    }
  }
  gst_buffer_unmap(buffer, &map);
  return nonzero;
}

static GstPadProbeReturn observe_buffer(GstPad *pad, GstPadProbeInfo *info,
                                        gpointer data) {
  Observation *seen = data;
  GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  GstClockTime pts, duration;
  gboolean nonzero;
  (void)pad;
  if (buffer == NULL) return GST_PAD_PROBE_OK;
  pts = GST_BUFFER_PTS(buffer);
  duration = GST_BUFFER_DURATION(buffer);
  nonzero = buffer_nonzero(buffer);
  g_mutex_lock(&seen->mutex);
  if (!GST_CLOCK_TIME_IS_VALID(pts) || !GST_CLOCK_TIME_IS_VALID(duration) || duration == 0) {
    ++seen->invalid_pts;
  } else if (seen->buffers > 0 && seen->buffers <= MAX_OBSERVED &&
             pts < seen->pts[seen->buffers - 1]) {
    ++seen->regressions;
  }
  if (seen->buffers < MAX_OBSERVED) {
    seen->pts[seen->buffers] = pts;
    seen->duration[seen->buffers] = duration;
  }
  ++seen->buffers;
  if (nonzero) ++seen->nonzero;
  g_mutex_unlock(&seen->mutex);
  return GST_PAD_PROBE_OK;
}

static GPtrArray *make_aac_material(GstClockTime first_pts, gint sample_rate,
                                    gint channels, gint frequency) {
  GError *error = NULL;
  gchar *description = g_strdup_printf(
      "audiotestsrc num-buffers=12 samplesperbuffer=1024 wave=sine freq=%d ! "
      "audio/x-raw,rate=%d,channels=%d ! audioconvert ! avenc_aac ! "
      "aacparse ! audio/mpeg,mpegversion=4,stream-format=adts ! "
      "appsink name=material sync=false", frequency, sample_rate, channels);
  GstElement *pipeline = gst_parse_launch(description, &error);
  GstElement *sink = pipeline == NULL ? NULL :
      gst_bin_get_by_name(GST_BIN(pipeline), "material");
  GPtrArray *frames = g_ptr_array_new_with_free_func((GDestroyNotify)gst_buffer_unref);
  const GstClockTime frame_duration =
      gst_util_uint64_scale(1024, GST_SECOND, sample_rate);
  guint i;
  g_free(description);
  if (error != NULL || pipeline == NULL || sink == NULL) {
    g_printerr("E9 AAC material pipeline failed: %s\n",
        error == NULL ? "missing element" : error->message);
    g_clear_error(&error);
    if (sink != NULL) gst_object_unref(sink);
    if (pipeline != NULL) gst_object_unref(pipeline);
    return frames;
  }
  gst_element_set_state(pipeline, GST_STATE_PLAYING);
  for (i = 0; i < 12; ++i) {
    GstSample *sample = gst_app_sink_try_pull_sample(GST_APP_SINK(sink), 2 * GST_SECOND);
    GstBuffer *buffer;
    if (sample == NULL) break;
    buffer = gst_buffer_copy_deep(gst_sample_get_buffer(sample));
    GST_BUFFER_PTS(buffer) = first_pts + i * frame_duration;
    GST_BUFFER_DURATION(buffer) = frame_duration;
    g_ptr_array_add(frames, buffer);
    gst_sample_unref(sample);
  }
  gst_element_set_state(pipeline, GST_STATE_NULL);
  gst_object_unref(sink);
  gst_object_unref(pipeline);
  return frames;
}

static guint drain_bus_errors(GstElement *pipeline) {
  GstBus *bus = gst_element_get_bus(pipeline);
  GstMessage *message;
  guint errors = 0;
  while ((message = gst_bus_pop(bus)) != NULL) {
    if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_ERROR) ++errors;
    gst_message_unref(message);
  }
  gst_object_unref(bus);
  return errors;
}

int main(int argc, char **argv) {
  GError *error = NULL;
  GstElement *publisher = NULL, *mixer = NULL, *pcm_input = NULL, *pcm_queue = NULL;
  GstElement *decoder = NULL, *aac_input = NULL, *decoded_sink = NULL;
  GstPad *mixer_src = NULL, *queue_src = NULL, *fixed_mixer_pad = NULL;
  GstPad *queue_src_after = NULL, *fixed_mixer_pad_after = NULL;
  GstClock *clock_before = NULL, *clock_after = NULL;
  GstClockTime base_before = GST_CLOCK_TIME_NONE, base_after = GST_CLOCK_TIME_NONE;
  GstClockTime source_first_pts = GST_CLOCK_TIME_NONE, source_second_pts = GST_CLOCK_TIME_NONE;
  GPtrArray *aac_frames = NULL, *aac_frames_second = NULL;
  Observation fixed = {0}, output = {0};
  GstClockTime decoded_pts[MAX_OBSERVED], decoded_duration[MAX_OBSERVED];
  GstState current = GST_STATE_VOID_PENDING, pending = GST_STATE_VOID_PENDING;
  GstStateChangeReturn state_result = GST_STATE_CHANGE_FAILURE;
  guint decoded = 0, first_decoded = 0, second_decoded = 0;
  guint decoded_nonzero = 0, pushed = 0, bus_errors = 0;
  guint initial_output = 0, initial_nonzero = 0, fixed_buffers, fixed_nonzero;
  guint output_nonzero, pads_before = 0, pads_after = 0, i;
  gulong mixer_probe = 0, fixed_probe = 0;
  guint64 app_buffers = 0, app_bytes = 0, app_time = 0;
  guint queue_buffers = 0, queue_bytes = 0;
  guint64 queue_time = 0;
  gint app_leaky = 0, queue_leaky = 0;
  gboolean live = FALSE, block = TRUE, timestamp = TRUE;
  gint format = 0;
  gboolean pts_equal = TRUE, settings_ok = FALSE, states_ok = FALSE;
  gboolean pad_identity_ok = FALSE, first_decoder_teardown_ok = FALSE;
  gboolean steady_ok = FALSE, teardown_ok = TRUE, result = FALSE;
  gint64 deadline;

  gst_init(&argc, &argv);
  g_mutex_init(&fixed.mutex);
  g_mutex_init(&output.mutex);
  publisher = gst_parse_launch(
      "audiomixer name=mix force-live=true ignore-inactive-pads=true "
      "latency=100000000 min-upstream-latency=100000000 ! "
      "fakesink name=publisher_sink sync=false async=false "
      "audiotestsrc is-live=true wave=silence ! "
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! mix. "
      "appsrc name=pcm_input is-live=true format=time do-timestamp=false block=false "
      "max-buffers=32 max-bytes=1048576 max-time=500000000 leaky-type=upstream "
      "caps=audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! "
      "queue name=pcm_queue max-size-buffers=32 max-size-bytes=1048576 "
      "max-size-time=500000000 leaky=upstream ! mix.", &error);
  if (error != NULL || publisher == NULL) goto cleanup;
  mixer = gst_bin_get_by_name(GST_BIN(publisher), "mix");
  pcm_input = gst_bin_get_by_name(GST_BIN(publisher), "pcm_input");
  pcm_queue = gst_bin_get_by_name(GST_BIN(publisher), "pcm_queue");
  if (mixer == NULL || pcm_input == NULL || pcm_queue == NULL) goto cleanup;
  pads_before = GST_ELEMENT(mixer)->numsinkpads;
  mixer_src = gst_element_get_static_pad(mixer, "src");
  queue_src = gst_element_get_static_pad(pcm_queue, "src");
  fixed_mixer_pad = queue_src == NULL ? NULL : gst_pad_get_peer(queue_src);
  if (mixer_src == NULL || fixed_mixer_pad == NULL) goto cleanup;
  mixer_probe = gst_pad_add_probe(mixer_src, GST_PAD_PROBE_TYPE_BUFFER,
                                  observe_buffer, &output, NULL);
  fixed_probe = gst_pad_add_probe(fixed_mixer_pad, GST_PAD_PROBE_TYPE_BUFFER,
                                  observe_buffer, &fixed, NULL);
  if (mixer_probe == 0 || fixed_probe == 0) goto cleanup;
  if (gst_element_set_state(publisher, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE ||
      gst_element_get_state(publisher, &current, &pending, GST_SECOND) ==
          GST_STATE_CHANGE_FAILURE || current != GST_STATE_PLAYING ||
      pending != GST_STATE_VOID_PENDING) goto cleanup;
  clock_before = gst_element_get_clock(publisher);
  base_before = gst_element_get_base_time(publisher);
  deadline = g_get_monotonic_time() + 250 * G_TIME_SPAN_MILLISECOND;
  do {
    g_mutex_lock(&output.mutex);
    initial_output = output.buffers;
    initial_nonzero = output.nonzero;
    g_mutex_unlock(&output.mutex);
    if (initial_output >= 5) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  if (clock_before == NULL || initial_output < 5 || initial_nonzero != 0) goto cleanup;
  source_first_pts = gst_clock_get_time(clock_before) - base_before + 100 * GST_MSECOND;
  aac_frames = make_aac_material(source_first_pts, 44100, 1, 1000);
  if (aac_frames == NULL || aac_frames->len != 12) goto cleanup;

  decoder = gst_parse_launch(
      "appsrc name=aac_input is-live=true format=time do-timestamp=false block=false "
      "max-buffers=32 max-bytes=1048576 max-time=500000000 leaky-type=upstream "
      "caps=audio/mpeg,mpegversion=4,stream-format=adts,framed=true,rate=44100,channels=1 ! "
      "aacparse ! avdec_aac ! audioconvert ! audioresample ! "
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! "
      "appsink name=decoded_sink sync=false async=false max-buffers=32 drop=false", &error);
  if (error != NULL || decoder == NULL) goto cleanup;
  aac_input = gst_bin_get_by_name(GST_BIN(decoder), "aac_input");
  decoded_sink = gst_bin_get_by_name(GST_BIN(decoder), "decoded_sink");
  if (aac_input == NULL || decoded_sink == NULL ||
      gst_element_set_state(decoder, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE) goto cleanup;
  for (i = 0; i < aac_frames->len; ++i)
    if (gst_app_src_push_buffer(GST_APP_SRC(aac_input),
        gst_buffer_ref(g_ptr_array_index(aac_frames, i))) != GST_FLOW_OK) goto cleanup;
  if (gst_app_src_end_of_stream(GST_APP_SRC(aac_input)) != GST_FLOW_OK) goto cleanup;
  while (decoded < MAX_OBSERVED) {
    GstSample *sample = gst_app_sink_try_pull_sample(GST_APP_SINK(decoded_sink), 500 * GST_MSECOND);
    GstBuffer *input, *copy;
    if (sample == NULL) {
      if (gst_app_sink_is_eos(GST_APP_SINK(decoded_sink))) break;
      continue;
    }
    input = gst_sample_get_buffer(sample);
    decoded_pts[decoded] = GST_BUFFER_PTS(input);
    decoded_duration[decoded] = GST_BUFFER_DURATION(input);
    if (buffer_nonzero(input)) ++decoded_nonzero;
    copy = gst_buffer_copy_deep(input);
    gst_sample_unref(sample);
    if (gst_app_src_push_buffer(GST_APP_SRC(pcm_input), copy) != GST_FLOW_OK) goto cleanup;
    ++decoded;
    ++pushed;
  }
  first_decoded = decoded;
  state_result = gst_element_set_state(decoder, GST_STATE_NULL);
  first_decoder_teardown_ok = state_result != GST_STATE_CHANGE_FAILURE &&
      gst_element_get_state(decoder, &current, &pending, GST_SECOND) !=
          GST_STATE_CHANGE_FAILURE && current == GST_STATE_NULL &&
      pending == GST_STATE_VOID_PENDING;
  bus_errors += drain_bus_errors(decoder);
  gst_object_unref(decoded_sink); decoded_sink = NULL;
  gst_object_unref(aac_input); aac_input = NULL;
  gst_object_unref(decoder); decoder = NULL;

  source_second_pts = source_first_pts +
      12 * gst_util_uint64_scale(1024, GST_SECOND, 44100) + 100 * GST_MSECOND;
  aac_frames_second = make_aac_material(source_second_pts, 48000, 1, 600);
  if (aac_frames_second == NULL || aac_frames_second->len != 12) goto cleanup;
  {
    gchar *description = g_strdup_printf(
        "appsrc name=aac_input is-live=true format=time do-timestamp=false block=false "
        "max-buffers=32 max-bytes=1048576 max-time=500000000 leaky-type=upstream "
        "caps=audio/mpeg,mpegversion=4,stream-format=adts,framed=true,rate=%d,channels=1 ! "
        "aacparse ! avdec_aac ! audioconvert ! audioresample ! "
        "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! "
        "appsink name=decoded_sink sync=false async=false max-buffers=32 drop=false", 48000);
    decoder = gst_parse_launch(description, &error);
    g_free(description);
  }
  if (error != NULL || decoder == NULL) goto cleanup;
  aac_input = gst_bin_get_by_name(GST_BIN(decoder), "aac_input");
  decoded_sink = gst_bin_get_by_name(GST_BIN(decoder), "decoded_sink");
  if (aac_input == NULL || decoded_sink == NULL ||
      gst_element_set_state(decoder, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE) goto cleanup;
  for (i = 0; i < aac_frames_second->len; ++i)
    if (gst_app_src_push_buffer(GST_APP_SRC(aac_input),
        gst_buffer_ref(g_ptr_array_index(aac_frames_second, i))) != GST_FLOW_OK) goto cleanup;
  if (gst_app_src_end_of_stream(GST_APP_SRC(aac_input)) != GST_FLOW_OK) goto cleanup;
  while (decoded < MAX_OBSERVED) {
    GstSample *sample = gst_app_sink_try_pull_sample(GST_APP_SINK(decoded_sink), 500 * GST_MSECOND);
    GstBuffer *input, *copy;
    if (sample == NULL) {
      if (gst_app_sink_is_eos(GST_APP_SINK(decoded_sink))) break;
      continue;
    }
    input = gst_sample_get_buffer(sample);
    decoded_pts[decoded] = GST_BUFFER_PTS(input);
    decoded_duration[decoded] = GST_BUFFER_DURATION(input);
    if (buffer_nonzero(input)) ++decoded_nonzero;
    copy = gst_buffer_copy_deep(input);
    gst_sample_unref(sample);
    if (gst_app_src_push_buffer(GST_APP_SRC(pcm_input), copy) != GST_FLOW_OK) goto cleanup;
    ++decoded;
    ++pushed;
  }
  second_decoded = decoded - first_decoded;
  deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  do {
    g_mutex_lock(&fixed.mutex);
    fixed_buffers = fixed.buffers;
    fixed_nonzero = fixed.nonzero;
    g_mutex_unlock(&fixed.mutex);
    g_mutex_lock(&output.mutex);
    output_nonzero = output.nonzero;
    g_mutex_unlock(&output.mutex);
    if (fixed_buffers >= decoded && output_nonzero > 0) break;
    g_usleep(1000);
  } while (g_get_monotonic_time() < deadline);
  g_mutex_lock(&fixed.mutex);
  fixed_buffers = fixed.buffers;
  fixed_nonzero = fixed.nonzero;
  for (i = 0; i < MIN(decoded, MIN(fixed.buffers, MAX_OBSERVED)); ++i)
    if (fixed.pts[i] != decoded_pts[i] || fixed.duration[i] != decoded_duration[i])
      pts_equal = FALSE;
  g_mutex_unlock(&fixed.mutex);
  g_mutex_lock(&output.mutex);
  output_nonzero = output.nonzero;
  g_mutex_unlock(&output.mutex);
  pads_after = GST_ELEMENT(mixer)->numsinkpads;
  queue_src_after = gst_element_get_static_pad(pcm_queue, "src");
  fixed_mixer_pad_after = queue_src_after == NULL ? NULL :
      gst_pad_get_peer(queue_src_after);
  pad_identity_ok = fixed_mixer_pad_after == fixed_mixer_pad &&
      fixed_mixer_pad_after != NULL && gst_pad_is_linked(fixed_mixer_pad_after) &&
      GST_OBJECT_PARENT(fixed_mixer_pad_after) == GST_OBJECT(mixer);
  clock_after = gst_element_get_clock(publisher);
  base_after = gst_element_get_base_time(publisher);
  gst_element_get_state(publisher, &current, &pending, 0);
  g_object_get(pcm_input, "is-live", &live, "block", &block,
      "do-timestamp", &timestamp, "format", &format, "max-buffers", &app_buffers,
      "max-bytes", &app_bytes, "max-time", &app_time, "leaky-type", &app_leaky, NULL);
  g_object_get(pcm_queue, "max-size-buffers", &queue_buffers,
      "max-size-bytes", &queue_bytes, "max-size-time", &queue_time,
      "leaky", &queue_leaky, NULL);
  settings_ok = live && !block && !timestamp && format == GST_FORMAT_TIME &&
      app_buffers == 32 && app_bytes == 1048576 && app_time == 500 * GST_MSECOND &&
      app_leaky == GST_APP_LEAKY_TYPE_UPSTREAM && queue_buffers == 32 &&
      queue_bytes == 1048576 && queue_time == 500 * GST_MSECOND && queue_leaky == 1;
  states_ok = current == GST_STATE_PLAYING && pending == GST_STATE_VOID_PENDING &&
      clock_before == clock_after && base_before == base_after;
  steady_ok = first_decoded >= 12 && second_decoded >= 12 &&
      decoded_nonzero == decoded && pushed == decoded &&
      fixed_buffers == decoded && fixed_nonzero == decoded && output_nonzero > 0 &&
      fixed.invalid_pts == 0 && fixed.regressions == 0 && pts_equal &&
      pads_before == 2 && pads_after == pads_before && pad_identity_ok &&
      settings_ok && states_ok && first_decoder_teardown_ok;

cleanup:
  if (error != NULL) {
    g_printerr("E9 pipeline error: %s\n", error->message);
    g_clear_error(&error);
  }
  if (pcm_input != NULL) gst_app_src_end_of_stream(GST_APP_SRC(pcm_input));
  if (decoder != NULL) {
    state_result = gst_element_set_state(decoder, GST_STATE_NULL);
    teardown_ok = teardown_ok && state_result != GST_STATE_CHANGE_FAILURE &&
        gst_element_get_state(decoder, &current, &pending, GST_SECOND) !=
            GST_STATE_CHANGE_FAILURE && current == GST_STATE_NULL &&
        pending == GST_STATE_VOID_PENDING;
    bus_errors += drain_bus_errors(decoder);
  }
  if (publisher != NULL) {
    state_result = gst_element_set_state(publisher, GST_STATE_NULL);
    teardown_ok = teardown_ok && state_result != GST_STATE_CHANGE_FAILURE &&
        gst_element_get_state(publisher, &current, &pending, GST_SECOND) !=
            GST_STATE_CHANGE_FAILURE && current == GST_STATE_NULL &&
        pending == GST_STATE_VOID_PENDING;
    bus_errors += drain_bus_errors(publisher);
  }
  result = steady_ok && teardown_ok && bus_errors == 0;
  if (fixed_mixer_pad != NULL && fixed_probe != 0)
    gst_pad_remove_probe(fixed_mixer_pad, fixed_probe);
  if (mixer_src != NULL && mixer_probe != 0)
    gst_pad_remove_probe(mixer_src, mixer_probe);
  if (clock_after != NULL) gst_object_unref(clock_after);
  if (clock_before != NULL) gst_object_unref(clock_before);
  if (fixed_mixer_pad != NULL) gst_object_unref(fixed_mixer_pad);
  if (fixed_mixer_pad_after != NULL) gst_object_unref(fixed_mixer_pad_after);
  if (queue_src != NULL) gst_object_unref(queue_src);
  if (queue_src_after != NULL) gst_object_unref(queue_src_after);
  if (mixer_src != NULL) gst_object_unref(mixer_src);
  if (decoded_sink != NULL) gst_object_unref(decoded_sink);
  if (aac_input != NULL) gst_object_unref(aac_input);
  if (decoder != NULL) gst_object_unref(decoder);
  if (pcm_queue != NULL) gst_object_unref(pcm_queue);
  if (pcm_input != NULL) gst_object_unref(pcm_input);
  if (mixer != NULL) gst_object_unref(mixer);
  if (publisher != NULL) gst_object_unref(publisher);
  if (aac_frames != NULL) g_ptr_array_unref(aac_frames);
  if (aac_frames_second != NULL) g_ptr_array_unref(aac_frames_second);
  printf("E9-B audio-less=%u/%u decoded=%u+%u=%u nonzero=%u pushed=%u fixed=%u/%u "
      "output-nonzero=%u pads=%u/%u identity=%d pts-equal=%d settings=%d state=%d "
      "decoder1-null=%d teardown=%d bus-errors=%u result=%s\n",
      initial_output, initial_nonzero, first_decoded, second_decoded, decoded,
      decoded_nonzero, pushed,
      fixed_buffers, fixed_nonzero, output_nonzero, pads_before, pads_after,
      pad_identity_ok, pts_equal, settings_ok, states_ok, first_decoder_teardown_ok,
      teardown_ok, bus_errors, result ? "PASS" : "FAIL");
  g_mutex_clear(&fixed.mutex);
  g_mutex_clear(&output.mutex);
  gst_deinit();
  return result ? 0 : 1;
}
