#include "last_frame.h"

void airplay_last_frame_init(AirPlayLastFrame *frame) {
  g_mutex_init(&frame->mutex);
  frame->buffer = NULL;
}

void airplay_last_frame_clear(AirPlayLastFrame *frame) {
  g_mutex_lock(&frame->mutex);
  GstBuffer *buffer = frame->buffer;
  frame->buffer = NULL;
  g_mutex_unlock(&frame->mutex);
  if (buffer != NULL) {
    gst_buffer_unref(buffer);
  }
  g_mutex_clear(&frame->mutex);
}

void airplay_last_frame_replace(AirPlayLastFrame *frame, GstBuffer *buffer) {
  if (buffer != NULL) {
    gst_buffer_ref(buffer);
  }
  g_mutex_lock(&frame->mutex);
  GstBuffer *previous = frame->buffer;
  frame->buffer = buffer;
  g_mutex_unlock(&frame->mutex);
  if (previous != NULL) {
    gst_buffer_unref(previous);
  }
}

GstBuffer *airplay_last_frame_ref(AirPlayLastFrame *frame) {
  g_mutex_lock(&frame->mutex);
  GstBuffer *buffer = frame->buffer;
  if (buffer != NULL) {
    gst_buffer_ref(buffer);
  }
  g_mutex_unlock(&frame->mutex);
  return buffer;
}

