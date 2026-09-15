#ifndef IMAGEPAD_SOURCE_CLOCK_VIDEO_PROOF_H
#define IMAGEPAD_SOURCE_CLOCK_VIDEO_PROOF_H

#include <stdbool.h>
#include <stdint.h>

typedef enum SourceClockVideoProofEvent {
  SOURCE_CLOCK_PROOF_EVENT_NONE = 0,
  SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR,
  SOURCE_CLOCK_PROOF_EVENT_DECODED,
  SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR
} SourceClockVideoProofEvent;

/* Caller serializes access, including stop. No clocks, buffers, locks or I/O.
 * Fresh planned candidate only: normal publishers do not use this object.
 * The caller must validate the original AU and isolate exactly the selected
 * IDR through decode and encode; this state does NOT establish buffer identity.
 * No other input/BLACK/HOLD may enter those stages before complete().
 * input/output_result report the corresponding appsrc push result exactly once.
 * Callbacks may occur before those results. Event writes must be reserved via
 * take_event(), performed outside owner locks, then committed via finish_event.
 * Failed I/O may already have appended a line: failures are terminal, no retry.
 */
typedef struct SourceClockVideoProof {
  bool active;
  uint32_t source_generation, watermark, selected_sequence;
  bool input_done, decoded_seen, output_reserved, output_done, encoded_seen;
  SourceClockVideoProofEvent reserved_event, committed_event;
} SourceClockVideoProof;

bool source_clock_video_proof_init(SourceClockVideoProof *, uint32_t generation, uint32_t watermark);
bool source_clock_video_proof_select(SourceClockVideoProof *, uint32_t generation, uint32_t sequence, bool is_idr);
bool source_clock_video_proof_input_result(SourceClockVideoProof *, bool success);
bool source_clock_video_proof_decoded(SourceClockVideoProof *, bool valid);
bool source_clock_video_proof_take_output(SourceClockVideoProof *);
bool source_clock_video_proof_output_result(SourceClockVideoProof *, bool success);
bool source_clock_video_proof_encoded(SourceClockVideoProof *, bool valid_idr);
SourceClockVideoProofEvent source_clock_video_proof_take_event(SourceClockVideoProof *);
bool source_clock_video_proof_finish_event(SourceClockVideoProof *, SourceClockVideoProofEvent, bool written);
bool source_clock_video_proof_complete(const SourceClockVideoProof *);
void source_clock_video_proof_stop(SourceClockVideoProof *);

#endif
