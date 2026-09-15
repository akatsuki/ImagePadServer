#ifndef IMAGEPAD_SOURCE_CLOCK_RTSP_TERMINATION_H
#define IMAGEPAD_SOURCE_CLOCK_RTSP_TERMINATION_H

#include <gst/gst.h>

#include <stdbool.h>

bool source_clock_message_is_rtsp_eof(GstMessage *message,
                                      GstObject *rtsp_sink);
int source_clock_current_process_id(void);

#endif
