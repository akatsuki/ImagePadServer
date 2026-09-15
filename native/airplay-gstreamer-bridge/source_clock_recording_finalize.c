#include "source_clock_recording_finalize.h"

#include <glib-object.h>

bool source_clock_recording_message_is_sink_eos(
    GstMessage *message, GstElement *record_sink) {
  const GstStructure *structure;
  const GValue *value;
  GstMessage *original;
  if (message == NULL || record_sink == NULL ||
      GST_MESSAGE_TYPE(message) != GST_MESSAGE_ELEMENT) {
    return false;
  }
  structure = gst_message_get_structure(message);
  if (structure == NULL ||
      !gst_structure_has_name(structure, "GstBinForwarded")) {
    return false;
  }
  value = gst_structure_get_value(structure, "message");
  if (value == NULL || !G_VALUE_HOLDS(value, GST_TYPE_MESSAGE)) {
    return false;
  }
  original = (GstMessage *)g_value_get_boxed(value);
  return original != NULL && GST_IS_MESSAGE(original) &&
         GST_MESSAGE_TYPE(original) == GST_MESSAGE_EOS &&
         GST_MESSAGE_SRC(original) == GST_OBJECT(record_sink);
}

bool source_clock_recording_wait_for_sink_eos(
    GstBus *bus, GstElement *record_sink, GstClockTime timeout) {
  GstClockTime started;
  GstClockTime deadline;
  if (bus == NULL || record_sink == NULL || timeout == 0 ||
      timeout == GST_CLOCK_TIME_NONE) {
    return false;
  }
  started = gst_util_get_timestamp();
  if (!GST_CLOCK_TIME_IS_VALID(started) || timeout > G_MAXUINT64 - started) {
    return false;
  }
  deadline = started + timeout;
  for (;;) {
    GstClockTime now = gst_util_get_timestamp();
    GstMessage *message;
    bool complete;
    if (!GST_CLOCK_TIME_IS_VALID(now) || now >= deadline) {
      return false;
    }
    message = gst_bus_timed_pop_filtered(
        bus, deadline - now, GST_MESSAGE_ELEMENT | GST_MESSAGE_ERROR);
    if (message == NULL) {
      return false;
    }
    if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_ERROR) {
      gst_message_unref(message);
      return false;
    }
    complete = source_clock_recording_message_is_sink_eos(
        message, record_sink);
    gst_message_unref(message);
    if (complete) {
      return true;
    }
  }
}

bool source_clock_recording_send_eos_and_wait(
    GstElement *pipeline, GstClockTime timeout) {
  GstElement *video_queue;
  GstElement *audio_queue;
  GstElement *record_sink;
  GstBus *bus;
  bool video_dispatched;
  bool audio_dispatched;
  bool dispatched;
  bool complete = false;
  if (pipeline == NULL || timeout == 0 || timeout == GST_CLOCK_TIME_NONE) {
    return false;
  }
  video_queue = gst_bin_get_by_name(
      GST_BIN(pipeline), "video_record_queue");
  audio_queue = gst_bin_get_by_name(
      GST_BIN(pipeline), "audio_record_queue");
  record_sink = gst_bin_get_by_name(GST_BIN(pipeline), "record_sink");
  bus = gst_element_get_bus(pipeline);
  if (video_queue == NULL || audio_queue == NULL || record_sink == NULL ||
      bus == NULL) {
    if (bus != NULL) gst_object_unref(bus);
    if (record_sink != NULL) gst_object_unref(record_sink);
    if (audio_queue != NULL) gst_object_unref(audio_queue);
    if (video_queue != NULL) gst_object_unref(video_queue);
    return false;
  }
  video_dispatched = gst_element_send_event(
      video_queue, gst_event_new_eos());
  audio_dispatched = gst_element_send_event(
      audio_queue, gst_event_new_eos());
  dispatched = video_dispatched && audio_dispatched;
  if (dispatched) {
    complete = source_clock_recording_wait_for_sink_eos(
        bus, record_sink, timeout);
  }
  gst_object_unref(bus);
  gst_object_unref(record_sink);
  gst_object_unref(audio_queue);
  gst_object_unref(video_queue);
  return complete;
}

bool source_clock_recording_close_pipeline(GstElement *pipeline) {
  GstState state = GST_STATE_VOID_PENDING;
  GstState pending = GST_STATE_VOID_PENDING;
  GstStateChangeReturn changed;
  if (pipeline == NULL) {
    return false;
  }
  changed = gst_element_set_state(pipeline, GST_STATE_NULL);
  if (changed == GST_STATE_CHANGE_FAILURE) {
    return false;
  }
  changed = gst_element_get_state(pipeline, &state, &pending, 0);
  return changed == GST_STATE_CHANGE_SUCCESS && state == GST_STATE_NULL &&
         pending == GST_STATE_VOID_PENDING;
}

SourceClockRecordingFinalizeResult source_clock_recording_finalize(
    GstElement *pipeline, SourceClockEventWriter *writer,
    const char *recording_path, GstClockTime eos_timeout) {
  SourceClockRecordingFinalizeResult result = {false, false, false};
  if (pipeline == NULL) {
    return result;
  }
  result.eos_complete = source_clock_recording_send_eos_and_wait(
      pipeline, eos_timeout);
  result.pipeline_closed = source_clock_recording_close_pipeline(pipeline);
  if (result.eos_complete && result.pipeline_closed) {
    result.event_written = source_clock_event_write_recording_finalized(
        writer, recording_path);
  }
  return result;
}
