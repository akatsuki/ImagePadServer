#include "source_clock_fixed_pcm_audio.h"
#include "source_clock_protocol.h"
#include <gst/app/gstappsink.h>
#include <string.h>

#define MAX_FRAMES 32u
#define MAX_BYTES (1024u * 1024u)
#define MAX_TIME (500 * GST_MSECOND)
#define DRAIN_US (500 * G_TIME_SPAN_MILLISECOND)
#define STOP_US (2 * G_TIME_SPAN_SECOND)

struct SourceClockFixedPcmAudio {
  GMutex mutex;
  GCond changed;
  GThread *worker;
  GQueue pending;                 /* borrowed from admitted, under mutex */
  GQueue admitted;                /* owns refs until decoder appsrc src probe */
  GstClockTime last_pts;
  guint configured_rate;
  guint configured_codec;
  GstAppSrc *output;
  SourceClockFixedPcmAudioObserver observer;
  gpointer observer_data;
  SourceClockFixedPcmAudioStats stats;
  gboolean worker_done, forwarding;
  gint64 drain_deadline;
  /* The worker exclusively owns these. On failed NULL they are quarantined. */
  GstElement *pipeline, *input, *sink;
  GstBus *bus;
  GstPad *input_pad;
  gulong input_probe;
};

GQuark source_clock_fixed_pcm_audio_error_quark(void) {
  return g_quark_from_static_string("source-clock-fixed-pcm-audio-error");
}

static void fail_locked(SourceClockFixedPcmAudio *audio, const gchar *message) {
  audio->stats.failed = TRUE;
  g_strlcpy(audio->stats.last_error, message, sizeof(audio->stats.last_error));
  g_cond_broadcast(&audio->changed);
}

static gboolean output_valid(GstAppSrc *input) {
  GstCaps *caps, *expected;
  guint64 buffers, bytes, time;
  gint format, leaky;
  gboolean live, block, timestamp, signals, valid;
  if (input == NULL || !GST_IS_APP_SRC(input)) return FALSE;
  caps = gst_app_src_get_caps(input);
  expected = gst_caps_from_string(
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved");
  valid = caps && gst_caps_is_fixed(caps) && gst_caps_is_subset(caps, expected);
  if (caps) gst_caps_unref(caps);
  gst_caps_unref(expected);
  g_object_get(input, "is-live", &live, "block", &block,
      "do-timestamp", &timestamp, "emit-signals", &signals, "format", &format,
      "max-buffers", &buffers, "max-bytes", &bytes, "max-time", &time,
      "leaky-type", &leaky, NULL);
  return valid && live && !block && !timestamp && !signals && format == GST_FORMAT_TIME &&
      buffers == MAX_FRAMES && bytes == MAX_BYTES && time == MAX_TIME &&
      leaky == GST_APP_LEAKY_TYPE_UPSTREAM;
}

SourceClockFixedPcmAudio *source_clock_fixed_pcm_audio_new(GstAppSrc *input,
    SourceClockFixedPcmAudioObserver observer, gpointer data, GError **error) {
  SourceClockFixedPcmAudio *audio;
  if (!output_valid(input)) {
    g_set_error_literal(error, SOURCE_CLOCK_FIXED_PCM_AUDIO_ERROR,
        SOURCE_CLOCK_FIXED_PCM_INVALID_OUTPUT, "fixed PCM appsrc contract not satisfied");
    return NULL;
  }
  audio = g_new0(SourceClockFixedPcmAudio, 1);
  g_mutex_init(&audio->mutex);
  g_cond_init(&audio->changed);
  g_queue_init(&audio->pending);
  g_queue_init(&audio->admitted);
  audio->last_pts = GST_CLOCK_TIME_NONE;
  audio->output = gst_object_ref(input);
  audio->observer = observer;
  audio->observer_data = data;
  return audio;
}

gboolean source_clock_fixed_pcm_audio_configure(SourceClockFixedPcmAudio *audio,
    const SourceClockAudioFormat *format, GError **error) {
  gboolean result = FALSE;
  gboolean supported;
  g_return_val_if_fail(audio != NULL, FALSE);
  g_mutex_lock(&audio->mutex);
  if (audio->stats.stopped || audio->stats.failed) {
    g_set_error_literal(error, SOURCE_CLOCK_FIXED_PCM_AUDIO_ERROR,
        SOURCE_CLOCK_FIXED_PCM_STOPPED, "fixed PCM helper is stopped or failed");
  } else {
    supported = format && format->codec_data == NULL &&
        ((format->codec == SOURCE_CLOCK_CODEC_AAC_LC_ADTS &&
          (format->sample_rate == 44100 || format->sample_rate == 48000) &&
          format->channels == 1 && format->samples_per_frame == 1024) ||
         (format->codec == SOURCE_CLOCK_CODEC_AAC_ELD_RAW &&
          format->sample_rate == 44100 && format->channels == 2 &&
          format->samples_per_frame == 480));
    if (!supported) {
      g_set_error_literal(error, SOURCE_CLOCK_FIXED_PCM_AUDIO_ERROR,
          SOURCE_CLOCK_FIXED_PCM_UNSUPPORTED, "Expected AAC-LC ADTS 44100/48000 mono 1024 or iOS AAC-ELD raw 44100 stereo 480, without codec_data");
    } else if (audio->stats.configured &&
        (audio->configured_rate != format->sample_rate ||
         audio->configured_codec != format->codec)) {
      g_set_error_literal(error, SOURCE_CLOCK_FIXED_PCM_AUDIO_ERROR,
          SOURCE_CLOCK_FIXED_PCM_FORMAT_CHANGE, "fixed PCM audio format changed within one helper generation");
    } else {
      audio->configured_rate = format->sample_rate;
      audio->configured_codec = format->codec;
      audio->stats.configured = TRUE;
      result = TRUE;
    }
  }
  g_mutex_unlock(&audio->mutex);
  return result;
}

static guint adts_sample_rate(guint index) {
  static const guint rates[] = {96000, 88200, 64000, 48000, 44100, 32000,
      24000, 22050, 16000, 12000, 11025, 8000, 7350};
  return index < G_N_ELEMENTS(rates) ? rates[index] : 0;
}

static gboolean frame_valid(SourceClockFixedPcmAudio *audio, GstBuffer *buffer) {
  guint8 h[7];
  gsize size = gst_buffer_get_size(buffer);
  guint length, channels, rate;
  GstClockTime pts = GST_BUFFER_PTS(buffer), duration = GST_BUFFER_DURATION(buffer);
  if (size == 0 || size > 8191 || !GST_CLOCK_TIME_IS_VALID(pts) ||
      !GST_CLOCK_TIME_IS_VALID(duration) || duration == 0 || duration > MAX_TIME ||
      pts >= GST_CLOCK_TIME_NONE - duration) return FALSE;
  /* Raw ELD has no ADTS header. Its complete-AU boundary comes from the
   * source-clock transport; codec parsing remains the decoder's responsibility. */
  if (audio->configured_codec == SOURCE_CLOCK_CODEC_AAC_ELD_RAW)
    return duration == gst_util_uint64_scale(480, GST_SECOND, 44100);
  if (size < 8 ||
      gst_buffer_extract(buffer, 0, h, sizeof(h)) != sizeof(h)) return FALSE;
  length = ((h[3] & 3u) << 11) | (h[4] << 3) | (h[5] >> 5);
  channels = ((h[2] & 1u) << 2) | (h[3] >> 6);
  rate = adts_sample_rate((h[2] >> 2) & 15);
  /* sync, MPEG-4, layer=0, AAC-LC profile=1, configured 44100/48000, mono, one block.
   * Reject embedded tuple changes before aacparse can renegotiate anything. */
  return h[0] == 0xff && (h[1] & 0xfe) == 0xf0 &&
      (h[2] >> 6) == 1 && rate == audio->configured_rate && channels == 1 &&
      (h[6] & 3) == 0 && length == size && size > ((h[1] & 1) ? 7u : 9u);
}

/* Observe synchronously: bus flushing at NULL cannot erase errors or EOS.
 * Drop every message so there is no unbounded, unconsumed message queue. */
static GstBusSyncReply decoder_bus(GstBus *bus, GstMessage *message, gpointer data) {
  SourceClockFixedPcmAudio *audio = data;
  (void)bus;
  g_mutex_lock(&audio->mutex);
  if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_ERROR) {
    GError *error = NULL;
    gchar *debug = NULL;
    gst_message_parse_error(message, &error, &debug);
    ++audio->stats.bus_errors;
    fail_locked(audio, error ? error->message : "decoder bus error");
    g_clear_error(&error);
    g_free(debug);
  } else if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_EOS) {
    audio->stats.eos_observed = TRUE;
    g_cond_broadcast(&audio->changed);
  }
  g_mutex_unlock(&audio->mutex);
  gst_message_unref(message);
  return GST_BUS_DROP;
}

static GstPadProbeReturn input_taken(GstPad *pad, GstPadProbeInfo *info, gpointer data) {
  SourceClockFixedPcmAudio *audio = data;
  GstBuffer *reserved;
  (void)pad; (void)info;
  g_mutex_lock(&audio->mutex);
  reserved = g_queue_pop_head(&audio->admitted);
  if (reserved) {
    --audio->stats.pending_frames;
    audio->stats.pending_bytes -= gst_buffer_get_size(reserved);
    audio->stats.pending_duration -= GST_BUFFER_DURATION(reserved);
  }
  g_cond_broadcast(&audio->changed);
  g_mutex_unlock(&audio->mutex);
  if (reserved) gst_buffer_unref(reserved);
  return GST_PAD_PROBE_OK;
}

static GstFlowReturn forward_sample(GstAppSink *sink, gpointer data) {
  SourceClockFixedPcmAudio *audio = data;
  GstSample *sample;
  GstFlowReturn flow = GST_FLOW_OK;
  gboolean forward;
  g_mutex_lock(&audio->mutex);
  ++audio->stats.callbacks_active;
  forward = audio->forwarding;
  g_mutex_unlock(&audio->mutex);
  sample = gst_app_sink_pull_sample(sink); /* new_sample means already available */
  if (sample != NULL) {
    GstBuffer *buffer = gst_sample_get_buffer(sample);
    g_mutex_lock(&audio->mutex);
    ++audio->stats.decoded_frames;
    g_mutex_unlock(&audio->mutex);
    if (forward && buffer) {
      if (audio->observer) audio->observer(sample, audio->observer_data);
      /* A stalled observer may return after the drain deadline/forced NULL.
       * Do not forward using the stale admission snapshot from callback entry. */
      g_mutex_lock(&audio->mutex);
      forward = audio->forwarding;
      g_mutex_unlock(&audio->mutex);
      /* Transfer a ref, not push_sample (which would change the fixed caps).
       * No copy/retimestamp: preserve all normalized metadata and buffer flags. */
      if (forward) {
        flow = gst_app_src_push_buffer(audio->output, gst_buffer_ref(buffer));
        g_mutex_lock(&audio->mutex);
        if (flow == GST_FLOW_OK) ++audio->stats.forwarded_frames;
        else {
          ++audio->stats.output_flow_failures;
          fail_locked(audio, "fixed PCM output rejected a buffer");
        }
        g_mutex_unlock(&audio->mutex);
      }
    }
    gst_sample_unref(sample);
  }
  g_mutex_lock(&audio->mutex);
  --audio->stats.callbacks_active;
  g_cond_broadcast(&audio->changed);
  g_mutex_unlock(&audio->mutex);
  /* Output failures are isolated: worker observes failed and tears down locally. */
  return GST_FLOW_OK;
}

static gboolean create_decoder(SourceClockFixedPcmAudio *audio) {
  GError *error = NULL;
  GstAppSinkCallbacks callbacks = {0};
  gchar *description;
  guint sample_rate, codec;
  gchar *input_caps;
  g_mutex_lock(&audio->mutex);
  sample_rate = audio->configured_rate;
  codec = audio->configured_codec;
  g_mutex_unlock(&audio->mutex);
  /* Use the same implicit iOS ELD config as the existing audio-bin path.
   * Only the private decoder changes; the publisher's fixed PCM caps stay put. */
  input_caps = codec == SOURCE_CLOCK_CODEC_AAC_ELD_RAW
      ? g_strdup("audio/mpeg,mpegversion=4,stream-format=raw,framed=true,"
                 "rate=44100,channels=2,codec_data=(buffer)f8e85000")
      : g_strdup_printf("audio/mpeg,mpegversion=4,stream-format=adts,framed=true,"
                        "rate=%u,channels=1", sample_rate);
  description = g_strdup_printf(
      "appsrc name=aac_input is-live=true format=time do-timestamp=false block=false emit-signals=false "
      "max-buffers=1 max-bytes=1048576 max-time=500000000 leaky-type=upstream "
      "caps=%s ! "
      "aacparse ! avdec_aac ! audioconvert ! audioresample ! "
      "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved ! "
      "appsink name=decoded_sink sync=false async=false max-buffers=1 drop=false wait-on-eos=false",
      input_caps);
  g_free(input_caps);
  audio->pipeline = gst_parse_launch(description, &error);
  g_free(description);
  if (audio->pipeline) {
    audio->bus = gst_element_get_bus(audio->pipeline);
    gst_bus_set_sync_handler(audio->bus, decoder_bus, audio, NULL);
  }
  if (error || !audio->pipeline) {
    g_mutex_lock(&audio->mutex);
    fail_locked(audio, error ? error->message : "decoder construction failed");
    g_mutex_unlock(&audio->mutex);
    g_clear_error(&error);
    return FALSE;
  }
  audio->input = gst_bin_get_by_name(GST_BIN(audio->pipeline), "aac_input");
  audio->sink = gst_bin_get_by_name(GST_BIN(audio->pipeline), "decoded_sink");
  if (!audio->input || !audio->sink) return FALSE;
  audio->input_pad = gst_element_get_static_pad(audio->input, "src");
  audio->input_probe = gst_pad_add_probe(audio->input_pad, GST_PAD_PROBE_TYPE_BUFFER,
      input_taken, audio, NULL);
  callbacks.new_sample = forward_sample;
  gst_app_sink_set_callbacks(GST_APP_SINK(audio->sink), &callbacks, audio, NULL);
  g_mutex_lock(&audio->mutex);
  audio->forwarding = TRUE;
  audio->stats.decoder_created = TRUE;
  audio->stats.generation = 1;
  g_mutex_unlock(&audio->mutex);
  return gst_element_set_state(audio->pipeline, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE;
}

static void clear_admitted_locked(SourceClockFixedPcmAudio *audio) {
  GstBuffer *buffer;
  g_queue_clear(&audio->pending);
  while ((buffer = g_queue_pop_head(&audio->admitted)) != NULL) gst_buffer_unref(buffer);
  audio->stats.pending_frames = 0;
  audio->stats.pending_bytes = 0;
  audio->stats.pending_duration = 0;
}

static gpointer decoder_worker(gpointer data) {
  SourceClockFixedPcmAudio *audio = data;
  gboolean created = create_decoder(audio), null_ok = TRUE;
  if (!created) {
    g_mutex_lock(&audio->mutex);
    if (!audio->stats.failed) fail_locked(audio, "decoder failed to enter PLAYING");
    g_mutex_unlock(&audio->mutex);
  }
  while (created) {
    GstBuffer *buffer;
    GstFlowReturn flow;
    g_mutex_lock(&audio->mutex);
    while (g_queue_is_empty(&audio->pending) && !audio->stats.stopped && !audio->stats.failed)
      g_cond_wait(&audio->changed, &audio->mutex);
    if (audio->stats.failed || (audio->stats.stopped &&
        (g_queue_is_empty(&audio->pending) || g_get_monotonic_time() >= audio->drain_deadline))) {
      g_mutex_unlock(&audio->mutex);
      break;
    }
    g_mutex_unlock(&audio->mutex);
    /* One producer, one queued appsrc buffer at most. Waiting here never blocks
     * admission or stop. Reservation includes this queue and the handoff. */
    if (gst_app_src_get_current_level_buffers(GST_APP_SRC(audio->input)) != 0) {
      g_mutex_lock(&audio->mutex);
      g_cond_wait_until(&audio->changed, &audio->mutex, g_get_monotonic_time() + 1000);
      g_mutex_unlock(&audio->mutex);
      continue;
    }
    g_mutex_lock(&audio->mutex);
    buffer = g_queue_pop_head(&audio->pending);
    if (buffer) gst_buffer_ref(buffer);
    g_mutex_unlock(&audio->mutex);
    if (!buffer) continue;
    flow = gst_app_src_push_buffer(GST_APP_SRC(audio->input), buffer);
    if (flow != GST_FLOW_OK) {
      g_mutex_lock(&audio->mutex);
      fail_locked(audio, "decoder appsrc rejected a frame");
      g_mutex_unlock(&audio->mutex);
      break;
    }
  }
  g_mutex_lock(&audio->mutex);
  if (!audio->stats.stopped) {
    audio->stats.stopped = TRUE;
    audio->drain_deadline = g_get_monotonic_time() + DRAIN_US;
  }
  g_mutex_unlock(&audio->mutex);
  if (created) {
    GstFlowReturn flow = gst_app_src_end_of_stream(GST_APP_SRC(audio->input));
    g_mutex_lock(&audio->mutex);
    audio->stats.eos_sent = flow == GST_FLOW_OK;
    while (!audio->stats.eos_observed && !audio->stats.failed &&
        g_cond_wait_until(&audio->changed, &audio->mutex, audio->drain_deadline)) {}
    audio->stats.forced_null = !audio->stats.eos_observed || !g_queue_is_empty(&audio->pending);
    g_mutex_unlock(&audio->mutex);
  }
  g_mutex_lock(&audio->mutex);
  audio->forwarding = FALSE;
  g_mutex_unlock(&audio->mutex);
  /* set_state itself may block in a third-party element. This is why a single
   * worker owns cleanup and stop/free never join it before worker_done. */
  if (audio->pipeline) {
    GstState state, pending;
    GstStateChangeReturn change = gst_element_set_state(audio->pipeline, GST_STATE_NULL);
    null_ok = change != GST_STATE_CHANGE_FAILURE &&
        gst_element_get_state(audio->pipeline, &state, &pending, GST_SECOND) != GST_STATE_CHANGE_FAILURE &&
        state == GST_STATE_NULL && pending == GST_STATE_VOID_PENDING;
  }
  g_mutex_lock(&audio->mutex);
  null_ok = null_ok && audio->stats.callbacks_active == 0;
  g_mutex_unlock(&audio->mutex);
  if (null_ok) {
    GstAppSinkCallbacks empty = {0};
    if (audio->sink) gst_app_sink_set_callbacks(GST_APP_SINK(audio->sink), &empty, NULL, NULL);
    if (audio->input_probe) gst_pad_remove_probe(audio->input_pad, audio->input_probe);
    /* All streaming callbacks are quiesced by confirmed NULL; account messages
     * until this point, before removing the synchronous bus observer. */
    if (audio->bus) gst_bus_set_sync_handler(audio->bus, NULL, NULL, NULL);
    if (audio->input_pad) gst_object_unref(audio->input_pad);
    if (audio->sink) gst_object_unref(audio->sink);
    if (audio->input) gst_object_unref(audio->input);
    if (audio->bus) gst_object_unref(audio->bus);
    if (audio->pipeline) gst_object_unref(audio->pipeline);
    audio->input_pad = NULL; audio->sink = NULL; audio->input = NULL;
    audio->bus = NULL; audio->pipeline = NULL;
  }
  g_mutex_lock(&audio->mutex);
  if (null_ok && audio->stats.callbacks_active == 0) {
    clear_admitted_locked(audio);
    audio->stats.decoder_null = TRUE;
    audio->stats.stop_complete = TRUE;
  } else {
    fail_locked(audio, "decoder NULL/callback quiescence unconfirmed; ownership retained");
  }
  audio->worker_done = TRUE;
  g_cond_broadcast(&audio->changed);
  g_mutex_unlock(&audio->mutex);
  return NULL;
}

GstFlowReturn source_clock_fixed_pcm_audio_push(SourceClockFixedPcmAudio *audio, GstBuffer *buffer) {
  GstFlowReturn flow = GST_FLOW_OK;
  GstBuffer *oldest;
  gsize size;
  if (!audio) {
    if (buffer) gst_buffer_unref(buffer);
    return GST_FLOW_ERROR;
  }
  if (!buffer) return GST_FLOW_ERROR;
  size = gst_buffer_get_size(buffer);
  g_mutex_lock(&audio->mutex);
  oldest = g_queue_peek_head(&audio->admitted);
  if (audio->stats.stopped) flow = GST_FLOW_FLUSHING;
  else if (audio->stats.failed) flow = GST_FLOW_ERROR;
  else if (!audio->stats.configured) flow = GST_FLOW_NOT_NEGOTIATED;
  else if (!frame_valid(audio, buffer) || (GST_CLOCK_TIME_IS_VALID(audio->last_pts) &&
      GST_BUFFER_PTS(buffer) < audio->last_pts)) flow = GST_FLOW_ERROR;
  else if (audio->stats.pending_frames >= MAX_FRAMES || size > MAX_BYTES - audio->stats.pending_bytes ||
      GST_BUFFER_DURATION(buffer) > MAX_TIME - audio->stats.pending_duration ||
      (oldest && GST_BUFFER_PTS(buffer) + GST_BUFFER_DURATION(buffer) - GST_BUFFER_PTS(oldest) > MAX_TIME))
    flow = GST_FLOW_CUSTOM_ERROR;
  if (flow == GST_FLOW_OK) {
    g_queue_push_tail(&audio->admitted, buffer);
    g_queue_push_tail(&audio->pending, buffer);
    ++audio->stats.pending_frames;
    audio->stats.pending_bytes += size;
    audio->stats.pending_duration += GST_BUFFER_DURATION(buffer);
    ++audio->stats.accepted_frames;
    audio->last_pts = GST_BUFFER_PTS(buffer);
    if (!audio->worker) audio->worker = g_thread_new("fixed-pcm-audio", decoder_worker, audio);
    g_cond_broadcast(&audio->changed);
  } else {
    ++audio->stats.rejected_frames;
  }
  g_mutex_unlock(&audio->mutex);
  if (flow != GST_FLOW_OK) gst_buffer_unref(buffer);
  return flow;
}

SourceClockFixedPcmAudioStopResult source_clock_fixed_pcm_audio_stop(SourceClockFixedPcmAudio *audio) {
  gint64 until = g_get_monotonic_time() + STOP_US;
  SourceClockFixedPcmAudioStopResult result;
  if (!audio) return SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE;
  g_mutex_lock(&audio->mutex);
  if (!audio->stats.stopped) {
    audio->stats.stopped = TRUE;
    audio->drain_deadline = g_get_monotonic_time() + DRAIN_US;
    g_cond_broadcast(&audio->changed);
  }
  if (!audio->worker) {
    audio->stats.decoder_null = TRUE;
    audio->stats.stop_complete = TRUE;
    audio->worker_done = TRUE;
  }
  while (!audio->worker_done && g_cond_wait_until(&audio->changed, &audio->mutex, until)) {}
  result = !audio->worker_done ? SOURCE_CLOCK_FIXED_PCM_STOP_TIMEOUT :
      audio->stats.stop_complete ? SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE :
      SOURCE_CLOCK_FIXED_PCM_STOP_FAILED_RETAINED;
  g_mutex_unlock(&audio->mutex);
  return result;
}

gboolean source_clock_fixed_pcm_audio_free(SourceClockFixedPcmAudio *audio) {
  if (!audio) return TRUE;
  if (source_clock_fixed_pcm_audio_stop(audio) != SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE) return FALSE;
  if (audio->worker) g_thread_join(audio->worker);
  gst_object_unref(audio->output);
  g_cond_clear(&audio->changed);
  g_mutex_clear(&audio->mutex);
  g_free(audio);
  return TRUE;
}

void source_clock_fixed_pcm_audio_get_stats(SourceClockFixedPcmAudio *audio,
    SourceClockFixedPcmAudioStats *out) {
  g_return_if_fail(audio && out);
  g_mutex_lock(&audio->mutex);
  *out = audio->stats;
  g_mutex_unlock(&audio->mutex);
}
