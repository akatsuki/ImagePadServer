#include "source_clock_rtsp_termination.h"
#include "test_check.h"

#include <gst/gst.h>

static GstMessage *new_resource_message(GstObject *source, GstMessageType type,
                                        GstResourceError code,
                                        const char *message,
                                        const char *debug) {
  GError *error = g_error_new_literal(GST_RESOURCE_ERROR, code, message);
  if (type == GST_MESSAGE_WARNING) {
    return gst_message_new_warning(source, error, g_strdup(debug));
  }
  return gst_message_new_error(source, error, g_strdup(debug));
}

int main(void) {
  GstElement *rtsp_sink;
  GstElement *other;
  GstMessage *message;
  gst_init(NULL, NULL);
  rtsp_sink = gst_element_factory_make("fakesink", "rtsp_sink");
  other = gst_element_factory_make("fakesink", "video_decoder");
  TEST_CHECK(rtsp_sink != NULL && other != NULL);

  message = new_resource_message(GST_OBJECT(rtsp_sink), GST_MESSAGE_WARNING,
                                 GST_RESOURCE_ERROR_READ,
                                 "Could not read from resource.",
                                 "The server closed the connection.");
  TEST_CHECK(source_clock_message_is_rtsp_eof(message, GST_OBJECT(rtsp_sink)));
  gst_message_unref(message);

  message = new_resource_message(GST_OBJECT(rtsp_sink), GST_MESSAGE_ERROR,
                                 GST_RESOURCE_ERROR_READ,
                                 "Could not read from resource.",
                                 "Could not receive message. (End-of-file)");
  TEST_CHECK(source_clock_message_is_rtsp_eof(message, GST_OBJECT(rtsp_sink)));
  gst_message_unref(message);

  message = new_resource_message(GST_OBJECT(rtsp_sink), GST_MESSAGE_ERROR,
                                 GST_RESOURCE_ERROR_OPEN_READ_WRITE,
                                 "Could not open resource for reading and writing.",
                                 "Failed to connect. (Generic error)");
  TEST_CHECK(!source_clock_message_is_rtsp_eof(message, GST_OBJECT(rtsp_sink)));
  gst_message_unref(message);

  message = new_resource_message(GST_OBJECT(other), GST_MESSAGE_ERROR,
                                 GST_RESOURCE_ERROR_READ,
                                 "Could not read from resource.",
                                 "Could not receive message. (End-of-file)");
  TEST_CHECK(!source_clock_message_is_rtsp_eof(message, GST_OBJECT(rtsp_sink)));
  gst_message_unref(message);

  {
    GError *wrong_domain = g_error_new_literal(
        GST_STREAM_ERROR, GST_RESOURCE_ERROR_READ,
        "Could not read from resource.");
    message = gst_message_new_error(
        GST_OBJECT(rtsp_sink), wrong_domain,
        g_strdup("Could not receive message. (End-of-file)"));
    TEST_CHECK(!source_clock_message_is_rtsp_eof(message,
                                                  GST_OBJECT(rtsp_sink)));
    gst_message_unref(message);
  }

  message = new_resource_message(GST_OBJECT(rtsp_sink), GST_MESSAGE_ERROR,
                                 GST_RESOURCE_ERROR_WRITE,
                                 "Could not write to resource.",
                                 "Could not send message. (Generic error)");
  TEST_CHECK(!source_clock_message_is_rtsp_eof(message, GST_OBJECT(rtsp_sink)));
  gst_message_unref(message);

  gst_object_unref(other);
  gst_object_unref(rtsp_sink);
  return 0;
}
