#ifndef IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO_H
#define IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO_H

#include <gst/app/gstappsrc.h>
#include "source_clock_audio_bin.h"

G_BEGIN_DECLS

typedef struct SourceClockFixedPcmAudio SourceClockFixedPcmAudio;
typedef enum {
  SOURCE_CLOCK_FIXED_PCM_INVALID_OUTPUT,
  SOURCE_CLOCK_FIXED_PCM_UNSUPPORTED,
  SOURCE_CLOCK_FIXED_PCM_STOPPED,
  SOURCE_CLOCK_FIXED_PCM_FORMAT_CHANGE
} SourceClockFixedPcmAudioError;
#define SOURCE_CLOCK_FIXED_PCM_AUDIO_ERROR (source_clock_fixed_pcm_audio_error_quark())
GQuark source_clock_fixed_pcm_audio_error_quark(void);

typedef enum {
  SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE,
  SOURCE_CLOCK_FIXED_PCM_STOP_TIMEOUT,
  SOURCE_CLOCK_FIXED_PCM_STOP_FAILED_RETAINED
} SourceClockFixedPcmAudioStopResult;

typedef struct {
  gboolean configured, decoder_created, failed, stopped, stop_complete;
  gboolean eos_sent, eos_observed, forced_null, decoder_null;
  guint generation, callbacks_active, bus_errors;
  guint pending_frames;
  gsize pending_bytes;
  GstClockTime pending_duration;
  guint64 accepted_frames, rejected_frames, decoded_frames, forwarded_frames;
  guint64 output_flow_failures;
  gchar last_error[256];
} SourceClockFixedPcmAudioStats;

/* Optional streaming diagnostic, called on the normalized appsink sample BEFORE
 * forwarding. Borrowed sample: do not mutate, block, or call stop/free here.
 * user_data must survive until successful free. The callback may get_stats.
 * NULL observer is the normal production mode. */
typedef void (*SourceClockFixedPcmAudioObserver)(GstSample *sample, gpointer user_data);

/* No decoder, worker, pad, state transition or output mutation in new/configure.
 * Holds an independent ref to fixed_input (does not sink floating references).
 * Caller supplies S16LE/48000/2/interleaved fixed caps, is-live=true, format=time,
 * do-timestamp=false, block=false, emit-signals=false, max-buffers=32,
 * max-bytes=1048576, max-time=500ms, leaky-type=upstream; invalid output fails.
 * Caller owns output topology and keeps these settings stable through free.
 * No EOS, flush, caps, state, clock or base-time changes are sent to publisher.
 * Control calls are thread-safe; free requires exclusive caller ownership.
 * Do not call stop/free from any streaming callback. GError must be empty. */
SourceClockFixedPcmAudio *source_clock_fixed_pcm_audio_new(
    GstAppSrc *fixed_input, SourceClockFixedPcmAudioObserver observer,
    gpointer user_data, GError **error);

/* Accept AAC_LC_ADTS / 44100 or 48000 / mono / 1024, and the observed iOS
 * AAC_ELD_RAW / 44100 / stereo / 480 tuple (implicit f8e85000 config).
 * Both require codec_data=NULL. Same tuple is idempotent; a different supported tuple
 * returns FORMAT_CHANGE without replacing the configured format or decoder. */
gboolean source_clock_fixed_pcm_audio_configure(SourceClockFixedPcmAudio *audio,
    const SourceClockAudioFormat *format, GError **error);

/* Consumes one ref on EVERY return. Require one transport-framed raw iOS ELD
 * AU with 480/44100-second duration, or exactly one complete MPEG-4 ADTS
 * AAC-LC frame matching the configured 44100/48000 mono tuple, one raw block,
 * valid nonregressing PTS and positive
 * duration <=500ms. No retained alias may be mutated by the caller.
 * First valid accepted frame starts one worker/decoder generation, never a
 * publisher child. Pending input (including worker/appsrc handoff) is bounded
 * by 32 frames / 1MiB / 500ms duration sum and PTS span. Overflow drops incoming
 * frame: GST_FLOW_CUSTOM_ERROR. Invalid frame: ERROR; no configure:
 * NOT_NEGOTIATED; stopped: FLUSHING. OK means admitted, NOT decoded/delivered.
 * Output appsrc is upstream-leaky: decoded/forwarded stats mean attempts, not
 * downstream delivery; observe the caller's pad to establish lossless delivery.
 * Normalized PCM buffer PTS/duration/DISCONT are forwarded without modification.
 * A frame whose ADTS rate/channels do not match the configured tuple is rejected
 * locally; no decoder renegotiation or same-session format switching is done. */
GstFlowReturn source_clock_fixed_pcm_audio_push(SourceClockFixedPcmAudio *audio,
    GstBuffer *buffer);

/* Terminal, idempotent. Close admission, let the SAME worker drain accepted
 * input + local EOS for at most 500ms, then force NULL if necessary. Bus sync
 * accounting remains installed through NULL (no auto-flush loss). Only after
 * NULL and callback quiescence are all decoder objects released.
 * Each stop/free call waits at most 2s; a blocked plugin never causes an
 * unbounded caller join. TIMEOUT retains all ownership; retry the same object.
 * FAILED_RETAINED quarantines an unconfirmed NULL; never release/replace it.
 * COMPLETE guarantees quiescence, not successful decode: inspect failed,
 * bus_errors, forced_null, and output_flow_failures separately. */
SourceClockFixedPcmAudioStopResult source_clock_fixed_pcm_audio_stop(
    SourceClockFixedPcmAudio *audio);
/* TRUE consumes ownership; FALSE retains the SAME pointer and observer data.
 * NULL is safe. Never clear your pointer on FALSE. */
gboolean source_clock_fixed_pcm_audio_free(SourceClockFixedPcmAudio *audio);
void source_clock_fixed_pcm_audio_get_stats(SourceClockFixedPcmAudio *audio,
    SourceClockFixedPcmAudioStats *out);

G_END_DECLS
#endif
