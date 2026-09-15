#include "source_clock_recording_finalize.h"
#include "test_check.h"

#include <glib/gstdio.h>
#include <gst/gst.h>
#include <string.h>

static GstMessage *forwarded_message(GstElement *pipeline, GstMessage *original) {
  GstMessage *forwarded = gst_message_new_element(
      GST_OBJECT(pipeline),
      gst_structure_new("GstBinForwarded", "message", GST_TYPE_MESSAGE,
                        original, NULL));
  gst_message_unref(original);
  return forwarded;
}

int main(int argc, char **argv) {
  GstElement *pipeline;
  GstElement *record_sink;
  GstElement *rtsp_sink;
  GstBus *bus;
  GstMessage *message;
  GstClockTime wait_started;
  GstClockTime wait_elapsed;
  GError *error = NULL;
  gchar *directory;
  gchar *ready_path;
  gchar *media_ready_path;
  gchar *event_log_path;
  gchar *event_log_contents = NULL;
  gsize event_log_bytes = 0;
  SourceClockEventWriter writer;
  SourceClockRecordingFinalizeResult finalize_result;

  gst_init(&argc, &argv);
  pipeline = gst_pipeline_new("recording-finalize-test");
  record_sink = gst_element_factory_make("fakesink", "record_sink");
  rtsp_sink = gst_element_factory_make("fakesink", "rtsp_sink");
  TEST_CHECK(pipeline != NULL && record_sink != NULL && rtsp_sink != NULL);
  TEST_CHECK(gst_bin_add(GST_BIN(pipeline), record_sink));
  TEST_CHECK(gst_bin_add(GST_BIN(pipeline), rtsp_sink));

  TEST_CHECK(!source_clock_recording_message_is_sink_eos(NULL, record_sink));
  message = gst_message_new_eos(GST_OBJECT(record_sink));
  TEST_CHECK(!source_clock_recording_message_is_sink_eos(message, record_sink));
  gst_message_unref(message);

  message = gst_message_new_element(
      GST_OBJECT(pipeline), gst_structure_new_empty("not-forwarded"));
  TEST_CHECK(!source_clock_recording_message_is_sink_eos(message, record_sink));
  gst_message_unref(message);

  message = gst_message_new_element(
      GST_OBJECT(pipeline), gst_structure_new_empty("GstBinForwarded"));
  TEST_CHECK(!source_clock_recording_message_is_sink_eos(message, record_sink));
  gst_message_unref(message);

  message = gst_message_new_element(
      GST_OBJECT(pipeline),
      gst_structure_new("GstBinForwarded", "message", G_TYPE_STRING,
                        "not-a-message", NULL));
  TEST_CHECK(!source_clock_recording_message_is_sink_eos(message, record_sink));
  gst_message_unref(message);

  message = forwarded_message(
      pipeline, gst_message_new_eos(GST_OBJECT(rtsp_sink)));
  TEST_CHECK(!source_clock_recording_message_is_sink_eos(message, record_sink));
  gst_message_unref(message);

  message = forwarded_message(
      pipeline,
      gst_message_new_state_changed(GST_OBJECT(record_sink), GST_STATE_READY,
                                    GST_STATE_PAUSED, GST_STATE_VOID_PENDING));
  TEST_CHECK(!source_clock_recording_message_is_sink_eos(message, record_sink));
  gst_message_unref(message);

  message = forwarded_message(
      pipeline, gst_message_new_eos(GST_OBJECT(record_sink)));
  TEST_CHECK(source_clock_recording_message_is_sink_eos(message, record_sink));
  TEST_CHECK(!source_clock_recording_message_is_sink_eos(message, NULL));
  gst_message_unref(message);

  bus = gst_bus_new();
  TEST_CHECK(bus != NULL);
  TEST_CHECK(!source_clock_recording_wait_for_sink_eos(
      NULL, record_sink, 20 * GST_MSECOND));
  TEST_CHECK(!source_clock_recording_wait_for_sink_eos(
      bus, NULL, 20 * GST_MSECOND));
  TEST_CHECK(!source_clock_recording_wait_for_sink_eos(
      bus, record_sink, 0));
  TEST_CHECK(!source_clock_recording_wait_for_sink_eos(
      bus, record_sink, GST_CLOCK_TIME_NONE));
  TEST_CHECK(!source_clock_recording_wait_for_sink_eos(
      bus, record_sink, GST_CLOCK_TIME_NONE - 1));
  wait_started = gst_util_get_timestamp();
  TEST_CHECK(!source_clock_recording_wait_for_sink_eos(
      bus, record_sink, 20 * GST_MSECOND));
  wait_elapsed = gst_util_get_timestamp() - wait_started;
  TEST_CHECK(wait_elapsed >= 10 * GST_MSECOND);
  TEST_CHECK(wait_elapsed < GST_SECOND);

  TEST_CHECK(gst_bus_post(
      bus, forwarded_message(
               pipeline, gst_message_new_eos(GST_OBJECT(rtsp_sink)))));
  TEST_CHECK(gst_bus_post(
      bus, forwarded_message(
               pipeline, gst_message_new_eos(GST_OBJECT(record_sink)))));
  TEST_CHECK(source_clock_recording_wait_for_sink_eos(
      bus, record_sink, 200 * GST_MSECOND));

  TEST_CHECK(gst_bus_post(
      bus, gst_message_new_custom(
               GST_MESSAGE_ERROR, GST_OBJECT(record_sink),
               gst_structure_new_empty("recording-finalize-test-error"))));
  TEST_CHECK(gst_bus_post(
      bus, forwarded_message(
               pipeline, gst_message_new_eos(GST_OBJECT(record_sink)))));
  TEST_CHECK(!source_clock_recording_wait_for_sink_eos(
      bus, record_sink, 200 * GST_MSECOND));
  TEST_CHECK(source_clock_recording_wait_for_sink_eos(
      bus, record_sink, 200 * GST_MSECOND));
  gst_object_unref(bus);

  TEST_CHECK(!source_clock_recording_close_pipeline(NULL));

  gst_object_unref(pipeline);

  directory = g_dir_make_tmp("imagepad-recording-finalize-XXXXXX", &error);
  TEST_CHECK(error == NULL && directory != NULL);
  ready_path = g_build_filename(directory, "ready.json", NULL);
  media_ready_path = g_build_filename(directory, "media-ready.json", NULL);
  event_log_path = g_build_filename(directory, "events.jsonl", NULL);
  TEST_CHECK(source_clock_event_writer_init(
      &writer, "fixture-session", 7, ready_path, media_ready_path,
      event_log_path));

  pipeline = gst_parse_launch(
      "appsrc name=video_record_queue is-live=true ! "
      "fakesink name=record_sink async=false sync=false "
      "appsrc name=audio_record_queue is-live=true ! "
      "fakesink name=audio_record_sink async=false sync=false",
      &error);
  TEST_CHECK(error == NULL && pipeline != NULL);
  g_object_set(pipeline, "message-forward", TRUE, NULL);
  TEST_CHECK(gst_element_set_state(pipeline, GST_STATE_PLAYING) !=
             GST_STATE_CHANGE_FAILURE);
  finalize_result = source_clock_recording_finalize(
      pipeline, &writer, "publisher-0007.mp4", 2 * GST_SECOND);
  TEST_CHECK(finalize_result.eos_complete);
  TEST_CHECK(finalize_result.pipeline_closed);
  TEST_CHECK(finalize_result.event_written);
  {
    GstState state = GST_STATE_VOID_PENDING;
    TEST_CHECK(gst_element_get_state(pipeline, &state, NULL, 0) ==
               GST_STATE_CHANGE_SUCCESS);
    TEST_CHECK(state == GST_STATE_NULL);
  }
  TEST_CHECK(g_file_get_contents(
      event_log_path, &event_log_contents, &event_log_bytes, &error));
  TEST_CHECK(error == NULL && event_log_bytes > 0);
  TEST_CHECK(strstr(event_log_contents,
                    "\"event\":\"recording-finalized\"") != NULL);
  TEST_CHECK(strstr(event_log_contents,
                    "\"recordingPath\":\"publisher-0007.mp4\"") != NULL);
  TEST_CHECK(strstr(event_log_contents,
                    "\"recordingClosed\":true") != NULL);
  g_free(event_log_contents);
  event_log_contents = NULL;
  gst_object_unref(pipeline);

  TEST_CHECK(g_remove(event_log_path) == 0);

  pipeline = gst_parse_launch(
      "appsrc name=video_record_queue is-live=true ! "
      "fakesink name=record_sink async=false sync=false",
      &error);
  TEST_CHECK(error == NULL && pipeline != NULL);
  g_object_set(pipeline, "message-forward", TRUE, NULL);
  TEST_CHECK(gst_element_set_state(pipeline, GST_STATE_PLAYING) !=
             GST_STATE_CHANGE_FAILURE);
  finalize_result = source_clock_recording_finalize(
      pipeline, &writer, "publisher-0007.mp4", 20 * GST_MSECOND);
  TEST_CHECK(!finalize_result.eos_complete);
  TEST_CHECK(finalize_result.pipeline_closed);
  TEST_CHECK(!finalize_result.event_written);
  TEST_CHECK(!g_file_test(event_log_path, G_FILE_TEST_EXISTS));
  gst_object_unref(pipeline);

  g_free(event_log_path);
  g_free(media_ready_path);
  g_free(ready_path);
  TEST_CHECK(g_rmdir(directory) == 0);
  g_free(directory);
  return 0;
}
