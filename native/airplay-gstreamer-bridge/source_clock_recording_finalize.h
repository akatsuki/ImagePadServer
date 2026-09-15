#ifndef IMAGEPAD_SOURCE_CLOCK_RECORDING_FINALIZE_H
#define IMAGEPAD_SOURCE_CLOCK_RECORDING_FINALIZE_H

#include <stdbool.h>

#include <gst/gst.h>

#include "source_clock_events.h"

typedef struct {
  bool eos_complete;
  bool pipeline_closed;
  bool event_written;
} SourceClockRecordingFinalizeResult;

bool source_clock_recording_message_is_sink_eos(
    GstMessage *message, GstElement *record_sink);

bool source_clock_recording_wait_for_sink_eos(
    GstBus *bus, GstElement *record_sink, GstClockTime timeout);

bool source_clock_recording_send_eos_and_wait(
    GstElement *pipeline, GstClockTime timeout);

bool source_clock_recording_close_pipeline(GstElement *pipeline);

SourceClockRecordingFinalizeResult source_clock_recording_finalize(
    GstElement *pipeline, SourceClockEventWriter *writer,
    const char *recording_path, GstClockTime eos_timeout);

#endif
