#ifndef IMAGEPAD_SOURCE_CLOCK_PIPELINE_INTERNAL_H
#define IMAGEPAD_SOURCE_CLOCK_PIPELINE_INTERNAL_H

#include <gst/gst.h>
#include "source_clock_audio_bin.h"

/* Runtime-only late-audio candidate: both START_WITHOUT_REAL_AUDIO and
 * DYNAMIC_AUDIO_BIN compile options must be enabled (C fallbacks are zero).
 * It does not change these compatibility factory contracts. Only NEW source
 * SESSION_START replaces the audio bin, under reader admission lock, with an
 * audio generation check after timeline mapping. Duplicate control messages
 * are idempotent. Within one session, only AAC-LC tuple FORMAT_CHANGE replaces
 * a fully released bin and advances the same admission generation; the frame
 * announcing the new tuple is admitted after replacement. Other codecs and
 * reconnect behavior remain deferred. Default is OFF. */

/* Compatibility factory: always includes real audio ingress. */
GstElement *source_clock_create_pipeline(const char *publish_url,
                                         const char *recording,
                                         int width, int height,
                                         int source_fps, int output_fps,
                                         int bitrate_kbps, int maxrate_kbps,
                                         int buffer_size_kbps, int audio_bitrate_bps,
                                         int gop,
                                         GError **error);

/* FALSE omits only audio_input/audio_decoder, retaining silence and outputs.
 * The caller owns the returned pipeline. This does not add late audio. */
GstElement *source_clock_create_pipeline_with_audio_ingress(
    const char *publish_url, const char *recording,
    int width, int height, int source_fps, int output_fps,
    int bitrate_kbps, int maxrate_kbps,
    int buffer_size_kbps, int audio_bitrate_bps, int gop,
    gboolean include_real_audio_ingress, GError **error);

/* Independent diagnostic switches. FALSE for include_rtsp_audio omits only
 * the RTSP audio queue/track and its custom payloader, not recording audio.
 * With RTSP audio present, FALSE for include_custom_audio_payloader leaves
 * sink_1's payloader NULL for rtspclientsink to select automatically.
 * TRUE for disable_rtsp_rtx sets only rtspclientsink's rtx-time to zero.
 * Both compatibility factories above keep RTSP audio and the custom payloader. */
GstElement *source_clock_create_pipeline_with_audio_options(
    const char *publish_url, const char *recording,
    int width, int height, int source_fps, int output_fps,
    int bitrate_kbps, int maxrate_kbps,
    int buffer_size_kbps, int audio_bitrate_bps, int gop,
    gboolean include_real_audio_ingress, gboolean include_rtsp_audio,
    gboolean include_custom_audio_payloader,
    gboolean disable_rtsp_rtx,
    GError **error);

/* E10-A internal factory: fixed_pcm_audio adds only one startup-created,
 * bounded raw-PCM appsrc/queue linked to the existing audio mixer. AAC decode
 * remains outside the publisher pipeline. Compatibility factories above
 * always pass FALSE, so their topology stays unchanged. */
GstElement *source_clock_create_pipeline_with_audio_routing(
    const char *publish_url, const char *recording,
    int width, int height, int source_fps, int output_fps,
    int bitrate_kbps, int maxrate_kbps,
    int buffer_size_kbps, int audio_bitrate_bps, int gop,
    gboolean include_real_audio_ingress, gboolean include_fixed_pcm_audio,
    gboolean include_rtsp_audio, gboolean include_custom_audio_payloader,
    gboolean disable_rtsp_rtx, GError **error);

/* Production caller boundary for the C1 terminal stop/free contract.
 * COMPLETE + free(TRUE) consumes the owner ref and NULLs *audio_bin.
 * PENDING/TIMEOUT/FAILED_RETAINED, or an unexpected free(FALSE), preserves
 * the identical non-NULL pointer and sets *dynamic_audio_disabled=TRUE so the
 * same publisher cannot create/push another real-audio branch. A later call
 * observes the same C1 worker. FAILED_RETAINED identifies quarantined caller
 * ownership; it does not restart the receiver, publisher, or MediaMTX.
 * In-session callers must hold the pipeline admission mutex across this call
 * and replacement; final cleanup instead joins readers first. Session policy
 * and format replacement are deliberately outside this helper. */
SourceClockAudioStopResult source_clock_pipeline_release_audio_bin(
    SourceClockAudioBin **audio_bin, gboolean *dynamic_audio_disabled);

#endif
