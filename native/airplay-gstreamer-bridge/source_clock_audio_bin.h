#ifndef IMAGEPAD_SOURCE_CLOCK_AUDIO_BIN_H
#define IMAGEPAD_SOURCE_CLOCK_AUDIO_BIN_H

#include <gst/gst.h>
#include <stdint.h>

G_BEGIN_DECLS
typedef struct SourceClockAudioBin SourceClockAudioBin;
typedef struct {
  uint8_t codec;
  uint32_t sample_rate;
  uint16_t channels;
  uint32_t samples_per_frame;
  GstBuffer *codec_data;
} SourceClockAudioFormat;

typedef enum {
  SOURCE_CLOCK_AUDIO_BIN_UNSUPPORTED,
  SOURCE_CLOCK_AUDIO_BIN_INVALID_FORMAT,
  SOURCE_CLOCK_AUDIO_BIN_FORMAT_CHANGE,
  SOURCE_CLOCK_AUDIO_BIN_STOPPED
} SourceClockAudioBinError;
#define SOURCE_CLOCK_AUDIO_BIN_ERROR (source_clock_audio_bin_error_quark())
GQuark source_clock_audio_bin_error_quark(void);

typedef struct {
  gboolean configured, stopped;
  guint pending_frames;
  gsize pending_bytes;
  GstClockTime pending_duration, oldest_pts;
  guint64 rejected_frames;
  guint64 startup_dropped_frames;
  gboolean waiting_for_parent_playing;
  guint configure_operations;
  gboolean ready, creation_failed;
  guint64 operation_generation;
  gboolean stop_complete, stop_failed, eos_sent, eos_observed, forced_null;
} SourceClockAudioBinStats;

typedef enum {
  SOURCE_CLOCK_AUDIO_STOP_COMPLETE,
  SOURCE_CLOCK_AUDIO_STOP_PENDING,
  SOURCE_CLOCK_AUDIO_STOP_TIMEOUT,
  SOURCE_CLOCK_AUDIO_STOP_FAILED_RETAINED
} SourceClockAudioStopResult;

/* new creates no elements, pads or state changes.
 * parent/mixer/context may be NULL; non-NULL arguments are borrowed during new
 * and held by independent references until free (floating refs are not sunk).
 * Configure/push/stop/getters serialize on an internal mutex. Caller must keep
 * audio alive throughout every call; free requires exclusive ownership with all
 * callers joined. free(NULL) is safe. Only a TRUE free result consumes caller
 * ownership; on FALSE keep the SAME pointer alive and do not replace the bin.
 * With parent/mixer/context supplied, configure attaches one cancellable source to
 * that context (never inline invoke). Without all three, retain B1 queue-only
 * behavior. The context must be iterated to construct the branch. Construction
 * waits for stable parent PLAYING, retrying every 20ms without changing its state.
 * stop is terminal and idempotent; free implies stop. Context owners never
 * wait for the cleanup worker. Do not call stop/free from streaming callbacks.
 * No session/format replacement or production adapter integration here.
 * Caller must not mutate input format/codec_data during configure. */
SourceClockAudioBin *source_clock_audio_bin_new(
    GstElement *parent, GstElement *mixer, GMainContext *context);

/* Accept SOURCE_CLOCK_CODEC_AAC_LC_ADTS at ADTS indexed sample rates,
 * mono/stereo and 1024 samples/frame, plus the observed iOS AAC-ELD raw tuple
 * (44100 Hz, stereo, 480 samples/frame, implicit f8e85000 config). AAC-LC
 * codec_data is opaque metadata, nonempty and at most 1KiB. Deep-copy it,
 * never borrow its memory. Identical configure is idempotent; a different valid format is
 * rejected until B2+ provides generation switching. Failure preserves state.
 * Standard GError rule applies: error == NULL or *error == NULL on entry. */
gboolean source_clock_audio_bin_configure(
    SourceClockAudioBin *audio, const SourceClockAudioFormat *format, GError **error);

/* Consume one buffer reference on EVERY return, including rejection. Caller
 * must not mutate retained aliases. Require nonempty complete frames, valid
 * mapped PTS and positive duration. Pending is limited to 32 frames, 1MiB,
 * 500ms duration sum AND PTS span (including duration). PTS must not regress.
 * While awaiting stable parent PLAYING, evict oldest complete frames to retain
 * the newest bounded window; count startup_dropped_frames and return OK. Source
 * PTS remains unchanged; the first retained frame drained is marked DISCONT.
 * Individually oversized frames are rejected without eviction. Otherwise,
 * reject the incoming frame without evicting existing frames on overflow:
 * GST_FLOW_CUSTOM_ERROR; malformed input: GST_FLOW_ERROR; no configure:
 * GST_FLOW_NOT_NEGOTIATED; stopped: GST_FLOW_FLUSHING. Each rejected non-NULL
 * frame increments rejected_frames. GST_FLOW_OK means accepted, NOT decoded.
 * Once ready, a nonblocking bounded appsrc owns frames; upstream-leaky mode
 * rejects overload without accumulation. Failed construction rejects further
 * frames with GST_FLOW_ERROR; no automatic retry. */
GstFlowReturn source_clock_audio_bin_push(SourceClockAudioBin *audio, GstBuffer *buffer);
/* Close admission/invalidate the creation generation first. A single worker
 * serializes behind in-flight creation/push, drains branch-local EOS (500ms),
 * unlinks/releases its request pad, NULLs/removes the branch, then drops refs.
 * EOS is intercepted at the branch output, never forwarded to the mixer.
 * If EOS cannot drain, forced_null records the fallback: confirm branch NULL
 * BEFORE unlinking. Failure to confirm NULL retains topology and ownership.
 * Never alters parent state/clock/base-time or any video element.
 * Context owner: immediate PENDING unless already complete. Control worker:
 * wait at most 2s per call. TIMEOUT does NOT cancel/join/free the cleanup worker
 * or release the caller's object. Observe stats / call stop again later; this
 * only waits for the SAME worker, never retries creation or teardown.
 * FAILED_RETAINED is terminal quarantine, not permission to release/replace.
 * COMPLETE means safe detachment, including completion of stale callbacks. */
SourceClockAudioStopResult source_clock_audio_bin_stop(SourceClockAudioBin *audio);
/* Exclusive caller only. FALSE preserves caller ownership on pending/timeout/
 * failure; TRUE joins the finished worker and consumes exactly one owner ref.
 * Never set the caller pointer to NULL on FALSE. Existing void-style callers
 * must be adapted before enabling this implementation in production. */
gboolean source_clock_audio_bin_free(SourceClockAudioBin *audio);

/* Coherent diagnostic snapshots. get_format requires an empty output struct;
 * success returns an independent deep copy, released using format_clear.
 * These outputs belong to the caller and are not internally synchronized. */
gboolean source_clock_audio_bin_get_format(SourceClockAudioBin *audio, SourceClockAudioFormat *out);
void source_clock_audio_format_clear(SourceClockAudioFormat *format);
void source_clock_audio_bin_get_stats(SourceClockAudioBin *audio, SourceClockAudioBinStats *out);
G_END_DECLS
#endif
