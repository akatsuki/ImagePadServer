#ifndef IMAGEPAD_AIRPLAY_LAST_FRAME_H
#define IMAGEPAD_AIRPLAY_LAST_FRAME_H

#include <gst/gst.h>

typedef struct {
  GMutex mutex;
  GstBuffer *buffer;
} AirPlayLastFrame;

void airplay_last_frame_init(AirPlayLastFrame *frame);
void airplay_last_frame_clear(AirPlayLastFrame *frame);
void airplay_last_frame_replace(AirPlayLastFrame *frame, GstBuffer *buffer);
GstBuffer *airplay_last_frame_ref(AirPlayLastFrame *frame);

#endif
