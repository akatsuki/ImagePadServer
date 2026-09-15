#include "source_clock_rtsp_termination.h"

#include <string.h>

#ifdef _WIN32
#include <process.h>
#else
#include <unistd.h>
#endif

bool source_clock_message_is_rtsp_eof(GstMessage *message,
                                      GstObject *rtsp_sink) {
  GError *error = NULL;
  gchar *debug = NULL;
  bool is_eof = false;
  if (message == NULL || rtsp_sink == NULL ||
      GST_MESSAGE_SRC(message) != rtsp_sink) {
    return false;
  }
  if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_WARNING) {
    gst_message_parse_warning(message, &error, &debug);
  } else if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_ERROR) {
    gst_message_parse_error(message, &error, &debug);
  } else {
    return false;
  }
  if (error != NULL && error->domain == GST_RESOURCE_ERROR &&
      error->code == GST_RESOURCE_ERROR_READ && debug != NULL) {
    is_eof = strstr(debug, "The server closed the connection.") != NULL ||
             strstr(debug, "End-of-file") != NULL;
  }
  g_clear_error(&error);
  g_free(debug);
  return is_eof;
}

int source_clock_current_process_id(void) {
#ifdef _WIN32
  return _getpid();
#else
  return (int)getpid();
#endif
}
