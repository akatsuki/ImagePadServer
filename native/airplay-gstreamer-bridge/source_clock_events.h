#ifndef IMAGEPAD_SOURCE_CLOCK_EVENTS_H
#define IMAGEPAD_SOURCE_CLOCK_EVENTS_H

#include <stdbool.h>
#include <stdint.h>
#include "source_clock_video_proof.h"

typedef struct {
  /* session_id and all file paths use UTF-8. */
  const char *session_id;
  uint64_t publisher_generation;
  const char *ready_path;
  const char *media_ready_path;
  const char *event_log_path;
} SourceClockEventWriter;

/* Emission performs file I/O and must be called after releasing owner locks. */

bool source_clock_event_writer_init(SourceClockEventWriter *writer,
                                    const char *session_id,
                                    uint64_t publisher_generation,
                                    const char *ready_path,
                                    const char *media_ready_path,
                                    const char *event_log_path);

bool source_clock_event_write_publisher_ready(SourceClockEventWriter *writer,
                                              int video_listen_port,
                                              int audio_listen_port);

bool source_clock_event_write_video_input_idr(SourceClockEventWriter *writer,
                                              const uint64_t *running_time_ns,
                                              const uint64_t *source_ntp_ns);

bool source_clock_event_write_video_decoded(SourceClockEventWriter *writer,
                                            const uint64_t *running_time_ns,
                                            const uint64_t *source_ntp_ns);

bool source_clock_event_write_video_encoded(SourceClockEventWriter *writer,
                                            const uint64_t *running_time_ns);

bool source_clock_event_write_video_watermark_final(
    SourceClockEventWriter *writer,
    uint64_t source_session_generation,
    uint64_t source_video_sequence);

/* Candidate-only proof stage. Decoded writes the existing media-ready snapshot;
 * other stages are log-only. Caller owns stage validation and one-shot I/O. */
bool source_clock_event_write_video_proof(SourceClockEventWriter *writer,
    SourceClockVideoProofEvent stage, uint32_t source_generation,
    uint32_t source_sequence, uint64_t running_time_ns);

bool source_clock_event_write_recording_finalized(SourceClockEventWriter *writer,
                                                  const char *recording_path);

bool source_clock_event_write_rtsp_eof(SourceClockEventWriter *writer,
                                       int process_id);

#endif
