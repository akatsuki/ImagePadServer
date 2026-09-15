#ifndef IMAGEPAD_SCHEDULED_VIDEO_BUFFER_H
#define IMAGEPAD_SCHEDULED_VIDEO_BUFFER_H

#include <gst/gst.h>

GstBuffer *source_clock_clone_scheduled_video_buffer(GstBuffer *source);

#endif
