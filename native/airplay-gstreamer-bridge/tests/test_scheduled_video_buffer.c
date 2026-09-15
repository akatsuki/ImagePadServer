#include "scheduled_video_buffer.h"

#include <assert.h>

int main(int argc, char **argv) {
  GstBuffer *source;
  GstBuffer *first;
  GstBuffer *second;
  GstMemory *source_memory;

  gst_init(&argc, &argv);
  source = gst_buffer_new_allocate(NULL, 16, NULL);
  assert(source != NULL);
  GST_BUFFER_PTS(source) = 100;
  GST_BUFFER_DTS(source) = 90;
  GST_BUFFER_DURATION(source) = 10;
  source_memory = gst_buffer_peek_memory(source, 0);
  assert(source_memory != NULL);

  first = source_clock_clone_scheduled_video_buffer(source);
  second = source_clock_clone_scheduled_video_buffer(source);
  assert(first != NULL);
  assert(second != NULL);
  assert(first != source);
  assert(second != source);
  assert(first != second);
  assert(gst_buffer_peek_memory(first, 0) == source_memory);
  assert(gst_buffer_peek_memory(second, 0) == source_memory);

  GST_BUFFER_PTS(first) = 1000;
  GST_BUFFER_DTS(first) = GST_CLOCK_TIME_NONE;
  GST_BUFFER_DURATION(first) = 20;
  GST_BUFFER_PTS(second) = 2000;
  GST_BUFFER_DURATION(second) = 30;

  assert(GST_BUFFER_PTS(source) == 100);
  assert(GST_BUFFER_DTS(source) == 90);
  assert(GST_BUFFER_DURATION(source) == 10);
  assert(GST_BUFFER_PTS(first) == 1000);
  assert(GST_BUFFER_DURATION(first) == 20);
  assert(GST_BUFFER_PTS(second) == 2000);
  assert(GST_BUFFER_DURATION(second) == 30);

  gst_buffer_unref(second);
  gst_buffer_unref(first);
  gst_buffer_unref(source);
  return 0;
}
