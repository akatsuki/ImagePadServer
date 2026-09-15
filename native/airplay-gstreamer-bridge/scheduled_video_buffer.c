#include "scheduled_video_buffer.h"

GstBuffer *source_clock_clone_scheduled_video_buffer(GstBuffer *source) {
  if (source == NULL) return NULL;
  return gst_buffer_copy(source);
}
