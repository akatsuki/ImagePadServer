#include "last_frame.h"

#include <stdio.h>

int main(void) {
  gst_init(NULL, NULL);
  AirPlayLastFrame frame;
  airplay_last_frame_init(&frame);
  GstBuffer *a = gst_buffer_new_allocate(NULL, 1, NULL);
  GstBuffer *b = gst_buffer_new_allocate(NULL, 1, NULL);
  if (a == NULL || b == NULL) {
    return 2;
  }
  airplay_last_frame_replace(&frame, a);
  GstBuffer *held = airplay_last_frame_ref(&frame);
  if (held == NULL || held != a) {
    if (held != NULL) gst_buffer_unref(held);
    gst_buffer_unref(a);
    gst_buffer_unref(b);
    airplay_last_frame_clear(&frame);
    return 3;
  }
  gst_buffer_unref(held);
  airplay_last_frame_replace(&frame, b);
  held = airplay_last_frame_ref(&frame);
  int ok = held != NULL && held == b;
  if (held != NULL) gst_buffer_unref(held);
  gst_buffer_unref(a);
  gst_buffer_unref(b);
  airplay_last_frame_clear(&frame);
  return ok ? 0 : 4;
}

