#include "source_clock_video_proof.h"

#include <string.h>

void source_clock_video_proof_stop(SourceClockVideoProof *p) {
  if (p != NULL) p->active = false;
}

static bool reject(SourceClockVideoProof *p) {
  source_clock_video_proof_stop(p);
  return false;
}

bool source_clock_video_proof_init(SourceClockVideoProof *p, uint32_t generation,
                                   uint32_t watermark) {
  if (p == NULL) return false;
  memset(p, 0, sizeof(*p));
  if (generation == 0 || watermark == 0) return false;
  p->source_generation = generation;
  p->watermark = watermark;
  p->active = true;
  return true;
}

bool source_clock_video_proof_select(SourceClockVideoProof *p, uint32_t generation,
                                     uint32_t sequence, bool is_idr) {
  if (p == NULL || !p->active) return false;
  if (generation != p->source_generation) return reject(p);
  if (p->selected_sequence != 0 || !is_idr || sequence <= p->watermark) return false;
  p->selected_sequence = sequence;
  return true;
}

bool source_clock_video_proof_input_result(SourceClockVideoProof *p, bool success) {
  if (p == NULL || !p->active) return false;
  if (!success || p->selected_sequence == 0 || p->input_done) return reject(p);
  p->input_done = true;
  return true;
}

bool source_clock_video_proof_decoded(SourceClockVideoProof *p, bool valid) {
  if (p == NULL || !p->active) return false;
  if (!valid || p->selected_sequence == 0 || p->decoded_seen) return reject(p);
  p->decoded_seen = true;
  return true;
}

bool source_clock_video_proof_take_output(SourceClockVideoProof *p) {
  if (p == NULL || !p->active || p->output_reserved ||
      p->committed_event != SOURCE_CLOCK_PROOF_EVENT_DECODED) return false;
  p->output_reserved = true;
  return true;
}

bool source_clock_video_proof_output_result(SourceClockVideoProof *p, bool success) {
  if (p == NULL || !p->active) return false;
  if (!success || !p->output_reserved || p->output_done) return reject(p);
  p->output_done = true;
  return true;
}

bool source_clock_video_proof_encoded(SourceClockVideoProof *p, bool valid_idr) {
  if (p == NULL || !p->active) return false;
  if (!valid_idr || !p->output_reserved || p->encoded_seen) return reject(p);
  p->encoded_seen = true;
  return true;
}

SourceClockVideoProofEvent source_clock_video_proof_take_event(SourceClockVideoProof *p) {
  if (p == NULL || !p->active || p->reserved_event != SOURCE_CLOCK_PROOF_EVENT_NONE)
    return SOURCE_CLOCK_PROOF_EVENT_NONE;
  switch (p->committed_event) {
    case SOURCE_CLOCK_PROOF_EVENT_NONE:
      if (p->input_done) p->reserved_event = SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR;
      break;
    case SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR:
      if (p->decoded_seen) p->reserved_event = SOURCE_CLOCK_PROOF_EVENT_DECODED;
      break;
    case SOURCE_CLOCK_PROOF_EVENT_DECODED:
      if (p->output_done && p->encoded_seen)
        p->reserved_event = SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR;
      break;
    default: break;
  }
  return p->reserved_event;
}

bool source_clock_video_proof_finish_event(SourceClockVideoProof *p,
                                          SourceClockVideoProofEvent event,
                                          bool written) {
  if (p == NULL || !p->active) return false;
  if (!written || event == SOURCE_CLOCK_PROOF_EVENT_NONE ||
      event != p->reserved_event) return reject(p);
  p->committed_event = event;
  p->reserved_event = SOURCE_CLOCK_PROOF_EVENT_NONE;
  return true;
}

bool source_clock_video_proof_complete(const SourceClockVideoProof *p) {
  return p != NULL && p->active &&
         p->committed_event == SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR;
}
