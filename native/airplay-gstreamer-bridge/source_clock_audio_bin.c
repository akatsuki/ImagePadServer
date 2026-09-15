#include "source_clock_audio_bin.h"
#include "source_clock_protocol.h"
#include <gst/app/gstappsrc.h>
#include <string.h>

#define PENDING_FRAMES 32u
#define PENDING_BYTES ((gsize)1048576)
#define PENDING_TIME (500 * GST_MSECOND)

struct SourceClockAudioBin {
  GMutex mutex;
  GMutex operation;
  GCond stopped_cond;
  GThread *stop_worker;
  guint creation_refs;
  gint refs;
  GSource *creation;
  GstElement *branch, *input;
  GstPad *mixer_pad;
  GstElement *parent, *mixer;
  GMainContext *context;
  SourceClockAudioFormat format;
  SourceClockAudioBinStats stats;
  GstClockTime last_pts, latest_end;
  gboolean have_last_pts;
  gboolean startup_discont;
  GQueue pending;
};

typedef struct {
  SourceClockAudioBin *audio;
  guint64 generation;
} CreationOperation;

static gboolean create_branch(gpointer data);
static void audio_unref(gpointer data);

static void creation_unref(gpointer data) {
  CreationOperation *op = data;
  g_mutex_lock(&op->audio->mutex);
  --op->audio->creation_refs;
  g_cond_broadcast(&op->audio->stopped_cond);
  g_mutex_unlock(&op->audio->mutex);
  audio_unref(op->audio);
  g_free(op);
}

static gboolean current_operation(SourceClockAudioBin *audio, guint64 generation) {
  gboolean current;
  g_mutex_lock(&audio->mutex);
  current = !audio->stats.stopped && audio->stats.operation_generation == generation;
  g_mutex_unlock(&audio->mutex);
  return current;
}

/* Called with mutex held; disposing buffers is always the caller's job. */
static GQueue take_pending(SourceClockAudioBin *audio) {
  GQueue queue = audio->pending;
  g_queue_init(&audio->pending);
  audio->stats.pending_frames = 0;
  audio->stats.pending_bytes = 0;
  audio->stats.pending_duration = 0;
  audio->stats.oldest_pts = GST_CLOCK_TIME_NONE;
  audio->latest_end = 0;
  return queue;
}

static void discard_pending(GQueue *queue) {
  while (!g_queue_is_empty(queue)) gst_buffer_unref(g_queue_pop_head(queue));
}

/* operation lock owns branch topology. Never change the parent's state/clock. */
static void remove_branch(SourceClockAudioBin *audio) {
  if (audio->branch != NULL) gst_element_set_state(audio->branch, GST_STATE_NULL);
  if (audio->mixer_pad != NULL) {
    GstPad *peer = gst_pad_get_peer(audio->mixer_pad);
    if (peer != NULL) {
      gst_pad_unlink(peer, audio->mixer_pad);
      gst_object_unref(peer);
    }
    gst_element_release_request_pad(audio->mixer, audio->mixer_pad);
    gst_object_unref(audio->mixer_pad);
    audio->mixer_pad = NULL;
  }
  if (audio->branch != NULL) {
    if (GST_OBJECT_PARENT(audio->branch) == GST_OBJECT(audio->parent))
      gst_bin_remove(GST_BIN(audio->parent), audio->branch);
    gst_object_unref(audio->branch);
    audio->branch = audio->input = NULL;
  }
}

static gboolean create_branch(gpointer data) {
  CreationOperation *op = data;
  SourceClockAudioBin *audio = op->audio;
  const char *factories[] = { "appsrc", "aacparse", "avdec_aac", "audioconvert", "audioresample", "capsfilter", "queue" };
  GstElement *elements[G_N_ELEMENTS(factories)] = {0};
  GstCaps *caps;
  GstPad *src = NULL, *ghost;
  GstPadTemplate *templ;
  GSource *source;
  GQueue pending;
  gboolean ok = FALSE, mark_discont, parent_playing = FALSE;
  GstState state = GST_STATE_VOID_PENDING, next = GST_STATE_VOID_PENDING;
  GstStateChangeReturn parent_result = GST_STATE_CHANGE_FAILURE;
  guint i;
  /* Never stall the context behind a push/teardown operation. */
  if (!g_mutex_trylock(&audio->operation)) return G_SOURCE_CONTINUE;
  /* Leave topology absent during preroll. The timeout source owns callback
   * lifetime and is cancelled by stop, including after a dispatched retry. */
  if (current_operation(audio, op->generation) && GST_IS_BIN(audio->parent)) {
    parent_result = gst_element_get_state(audio->parent, &state, &next, 0);
    parent_playing = parent_result != GST_STATE_CHANGE_FAILURE &&
        state == GST_STATE_PLAYING && next == GST_STATE_VOID_PENDING;
  }
  if (parent_result != GST_STATE_CHANGE_FAILURE &&
      (state != GST_STATE_PLAYING || next != GST_STATE_VOID_PENDING)) {
    g_mutex_lock(&audio->mutex);
    if (!audio->stats.stopped) audio->stats.waiting_for_parent_playing = TRUE;
    g_mutex_unlock(&audio->mutex);
    g_mutex_unlock(&audio->operation);
    return G_SOURCE_CONTINUE;
  }
  g_mutex_lock(&audio->mutex);
  source = audio->creation;
  audio->creation = NULL;
  audio->stats.waiting_for_parent_playing = FALSE;
  if (audio->stats.stopped || audio->stats.operation_generation != op->generation) {
    g_mutex_unlock(&audio->mutex);
    g_mutex_unlock(&audio->operation);
    if (source != NULL) g_source_unref(source);
    return G_SOURCE_REMOVE;
  }
  g_mutex_unlock(&audio->mutex);
  if (!parent_playing || !g_main_context_is_owner(audio->context) || !GST_IS_BIN(audio->parent) ||
      GST_OBJECT_PARENT(audio->mixer) != GST_OBJECT(audio->parent)) goto done;
  audio->branch = gst_bin_new(NULL);
  gst_object_ref_sink(audio->branch);
  for (i = 0; i < G_N_ELEMENTS(factories); ++i) {
    elements[i] = gst_element_factory_make(factories[i], NULL);
    if (elements[i] == NULL) goto done;
    if (!gst_bin_add(GST_BIN(audio->branch), elements[i])) {
      gst_object_unref(elements[i]);
      goto done;
    }
  }
  audio->input = elements[0]; /* Borrowed from our independently held branch. */
  if (audio->format.codec == SOURCE_CLOCK_CODEC_AAC_ELD_RAW) {
    /* UxPlay's AAC-ELD callback does not carry AudioSpecificConfig.  Use the
     * same fixed config that the proven legacy decodebin path consumes for
     * iOS 18's 44.1 kHz stereo, 480-sample frames. */
    caps = gst_caps_from_string(
        "audio/mpeg,mpegversion=(int)4,channels=(int)2,rate=(int)44100,"
        "stream-format=(string)raw,framed=(boolean)true,"
        "codec_data=(buffer)f8e85000");
  } else {
    caps = gst_caps_new_simple("audio/mpeg", "mpegversion", G_TYPE_INT, 4,
        "stream-format", G_TYPE_STRING, "adts", "framed", G_TYPE_BOOLEAN, TRUE,
        "rate", G_TYPE_INT, (gint)audio->format.sample_rate,
        "channels", G_TYPE_INT, (gint)audio->format.channels, NULL);
  }
  if (caps == NULL) goto done;
  g_object_set(audio->input, "caps", caps, "is-live", TRUE, "format", GST_FORMAT_TIME,
      "do-timestamp", FALSE, "block", FALSE, "max-buffers", (guint64)PENDING_FRAMES,
      "max-bytes", (guint64)PENDING_BYTES, "max-time", (guint64)PENDING_TIME,
      "leaky-type", GST_APP_LEAKY_TYPE_UPSTREAM, NULL);
  gst_caps_unref(caps);
  caps = gst_caps_from_string("audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved");
  g_object_set(elements[5], "caps", caps, NULL);
  gst_caps_unref(caps);
  g_object_set(elements[6], "max-size-buffers", PENDING_FRAMES, "max-size-bytes", (guint)PENDING_BYTES,
      "max-size-time", (guint64)PENDING_TIME, "leaky", 1, NULL);
  for (i = 1; i < G_N_ELEMENTS(factories); ++i)
    if (!gst_element_link(elements[i - 1], elements[i])) goto done;
  src = gst_element_get_static_pad(elements[6], "src");
  ghost = gst_ghost_pad_new("src", src);
  gst_object_unref(src); src = NULL;
  if (ghost == NULL) goto done;
  if (!gst_element_add_pad(audio->branch, ghost)) {
    gst_object_unref(ghost);
    goto done;
  }
  if (!gst_bin_add(GST_BIN(audio->parent), audio->branch)) goto done;
  if (!current_operation(audio, op->generation)) goto done;
  templ = gst_element_class_get_pad_template(GST_ELEMENT_GET_CLASS(audio->mixer), "sink_%u");
  if (templ == NULL || GST_PAD_TEMPLATE_PRESENCE(templ) != GST_PAD_REQUEST) goto done;
  audio->mixer_pad = gst_element_request_pad(audio->mixer, templ, NULL, NULL);
  src = gst_element_get_static_pad(audio->branch, "src");
  if (audio->mixer_pad == NULL || gst_pad_link(src, audio->mixer_pad) != GST_PAD_LINK_OK) goto done;
  if (!current_operation(audio, op->generation)) goto done;
  if (!gst_element_sync_state_with_parent(audio->branch)) goto done;
  ok = TRUE;
done:
  if (src != NULL) gst_object_unref(src);
  if (!ok && current_operation(audio, op->generation)) remove_branch(audio);
  g_mutex_lock(&audio->mutex);
  if (!audio->stats.stopped && audio->stats.operation_generation == op->generation) {
    audio->stats.ready = ok;
    audio->stats.creation_failed = !ok;
  } else ok = FALSE;
  pending = take_pending(audio);
  mark_discont = audio->startup_discont;
  audio->startup_discont = FALSE;
  g_mutex_unlock(&audio->mutex);
  /* All static links, caps, request pad and state sync completed before drain.
   * Explicit AAC decoder avoids decodebin's pad-added/first-buffer cycle. */
  while (ok && !g_queue_is_empty(&pending) && current_operation(audio, op->generation)) {
    GstBuffer *buffer = g_queue_pop_head(&pending);
    GstFlowReturn flow;
    if (mark_discont) {
      buffer = gst_buffer_make_writable(buffer);
      GST_BUFFER_FLAG_SET(buffer, GST_BUFFER_FLAG_DISCONT);
      mark_discont = FALSE;
    }
    flow = gst_app_src_push_buffer(GST_APP_SRC(audio->input), buffer);
    if (flow != GST_FLOW_OK) ok = FALSE;
  }
  if (!ok && current_operation(audio, op->generation)) {
    remove_branch(audio);
    g_mutex_lock(&audio->mutex);
    if (!audio->stats.stopped && audio->stats.operation_generation == op->generation) {
      audio->stats.ready = FALSE;
      audio->stats.creation_failed = TRUE;
    }
    g_mutex_unlock(&audio->mutex);
  }
  g_mutex_unlock(&audio->operation);
  discard_pending(&pending);
  if (source != NULL) g_source_unref(source);
  return G_SOURCE_REMOVE;
}

GQuark source_clock_audio_bin_error_quark(void) {
  return g_quark_from_static_string("source-clock-audio-bin-error");
}

void source_clock_audio_format_clear(SourceClockAudioFormat *format) {
  if (format == NULL) return;
  if (format->codec_data != NULL) gst_buffer_unref(format->codec_data);
  memset(format, 0, sizeof(*format));
}

static gboolean copy_format(const SourceClockAudioFormat *input, SourceClockAudioFormat *out) {
  *out = *input;
  out->codec_data = input->codec_data == NULL ? NULL : gst_buffer_copy_deep(input->codec_data);
  if (input->codec_data != NULL && out->codec_data == NULL) {
    memset(out, 0, sizeof(*out));
    return FALSE;
  }
  return TRUE;
}

static gboolean valid_format(const SourceClockAudioFormat *format, GError **error) {
  static const uint32_t rates[] = {
    96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350
  };
  gboolean rate_ok = FALSE;
  guint i;
  if (format != NULL && format->codec != SOURCE_CLOCK_CODEC_AAC_LC_ADTS &&
      format->codec != SOURCE_CLOCK_CODEC_AAC_ELD_RAW) {
    g_set_error_literal(error, SOURCE_CLOCK_AUDIO_BIN_ERROR, SOURCE_CLOCK_AUDIO_BIN_UNSUPPORTED,
                        "Audio bin accepts only AAC-LC ADTS or iOS AAC-ELD raw");
    return FALSE;
  }
  if (format != NULL && format->codec == SOURCE_CLOCK_CODEC_AAC_ELD_RAW) {
    if (format->sample_rate != 44100 || format->channels != 2 ||
        format->samples_per_frame != 480 || format->codec_data != NULL) {
      g_set_error_literal(error, SOURCE_CLOCK_AUDIO_BIN_ERROR,
                          SOURCE_CLOCK_AUDIO_BIN_INVALID_FORMAT,
                          "Invalid iOS AAC-ELD format (44100 Hz, stereo, 480 samples, implicit config required)");
      return FALSE;
    }
    return TRUE;
  }
  if (format != NULL) {
    for (i = 0; i < G_N_ELEMENTS(rates); ++i) {
      if (rates[i] == format->sample_rate) rate_ok = TRUE;
    }
  }
  if (format == NULL || !rate_ok || format->channels < 1 || format->channels > 2 ||
      format->samples_per_frame != 1024 ||
      (format->codec_data != NULL && (gst_buffer_get_size(format->codec_data) == 0 ||
                                     gst_buffer_get_size(format->codec_data) > 1024))) {
    g_set_error_literal(error, SOURCE_CLOCK_AUDIO_BIN_ERROR, SOURCE_CLOCK_AUDIO_BIN_INVALID_FORMAT,
                        "Invalid AAC-LC format (rate, mono/stereo, 1024 samples, optional 1..1024-byte metadata)");
    return FALSE;
  }
  return TRUE;
}

static gboolean same_format(const SourceClockAudioFormat *a, const SourceClockAudioFormat *b) {
  guint8 a_bytes[1024], b_bytes[1024];
  gsize size;
  if (a->codec != b->codec || a->sample_rate != b->sample_rate ||
      a->channels != b->channels || a->samples_per_frame != b->samples_per_frame) return FALSE;
  if (a->codec_data == NULL || b->codec_data == NULL) return a->codec_data == b->codec_data;
  size = gst_buffer_get_size(a->codec_data);
  if (size != gst_buffer_get_size(b->codec_data)) return FALSE;
  return gst_buffer_extract(a->codec_data, 0, a_bytes, size) == size &&
         gst_buffer_extract(b->codec_data, 0, b_bytes, size) == size &&
         memcmp(a_bytes, b_bytes, size) == 0;
}

SourceClockAudioBin *source_clock_audio_bin_new(
    GstElement *parent, GstElement *mixer, GMainContext *context) {
  SourceClockAudioBin *audio = g_new0(SourceClockAudioBin, 1);
  g_mutex_init(&audio->mutex);
  g_mutex_init(&audio->operation);
  g_cond_init(&audio->stopped_cond);
  audio->refs = 1;
  g_queue_init(&audio->pending);
  audio->parent = parent == NULL ? NULL : gst_object_ref(parent);
  audio->mixer = mixer == NULL ? NULL : gst_object_ref(mixer);
  audio->context = context == NULL ? NULL : g_main_context_ref(context);
  audio->stats.oldest_pts = GST_CLOCK_TIME_NONE;
  return audio;
}

gboolean source_clock_audio_bin_configure(
    SourceClockAudioBin *audio, const SourceClockAudioFormat *format, GError **error) {
  gboolean ok = FALSE;
  if (audio == NULL) {
    g_set_error_literal(error, SOURCE_CLOCK_AUDIO_BIN_ERROR, SOURCE_CLOCK_AUDIO_BIN_INVALID_FORMAT,
                        "audio is NULL");
    return FALSE;
  }
  g_mutex_lock(&audio->mutex);
  if (audio->stats.stopped) {
    g_set_error_literal(error, SOURCE_CLOCK_AUDIO_BIN_ERROR, SOURCE_CLOCK_AUDIO_BIN_STOPPED,
                        "Audio bin is stopped");
  } else if (valid_format(format, error)) {
    if (audio->stats.configured) {
      ok = same_format(&audio->format, format);
      if (!ok) g_set_error_literal(error, SOURCE_CLOCK_AUDIO_BIN_ERROR, SOURCE_CLOCK_AUDIO_BIN_FORMAT_CHANGE,
                                   "B1 does not switch configured formats");
    } else {
      ok = copy_format(format, &audio->format);
      if (ok) {
        audio->stats.configured = TRUE;
        if (audio->parent != NULL && audio->mixer != NULL && audio->context != NULL) {
          CreationOperation *op = g_new0(CreationOperation, 1);
          /* Attach under lock, never invoke under lock: invoke may run inline.
           * Source holds lifetime even if free races a dispatched callback. */
          GstState state = GST_STATE_VOID_PENDING, next = GST_STATE_VOID_PENDING;
          gst_element_get_state(audio->parent, &state, &next, 0);
          audio->stats.waiting_for_parent_playing =
              state != GST_STATE_PLAYING || next != GST_STATE_VOID_PENDING;
          audio->creation = g_timeout_source_new(20);
          /* First dispatch stays immediate; subsequent retries are bounded. */
          g_source_set_ready_time(audio->creation, 0);
          g_atomic_int_inc(&audio->refs);
          ++audio->creation_refs;
          op->audio = audio;
          op->generation = ++audio->stats.operation_generation;
          g_source_set_callback(audio->creation, create_branch, op, creation_unref);
          ++audio->stats.configure_operations;
          g_source_attach(audio->creation, audio->context);
        }
      }
      else g_set_error_literal(error, SOURCE_CLOCK_AUDIO_BIN_ERROR, SOURCE_CLOCK_AUDIO_BIN_INVALID_FORMAT,
                               "Could not copy codec_data");
    }
  }
  g_mutex_unlock(&audio->mutex);
  return ok;
}

GstFlowReturn source_clock_audio_bin_push(SourceClockAudioBin *audio, GstBuffer *buffer) {
  GstFlowReturn result = GST_FLOW_OK;
  gsize bytes;
  GstClockTime pts, duration, end = 0, latest_end;
  GQueue evicted = G_QUEUE_INIT;
  if (buffer == NULL) return GST_FLOW_ERROR;
  if (audio == NULL) {
    gst_buffer_unref(buffer);
    return GST_FLOW_ERROR;
  }
  bytes = gst_buffer_get_size(buffer);
  pts = GST_BUFFER_PTS(buffer);
  duration = GST_BUFFER_DURATION(buffer);
  /* Reject promptly even while construction is paused with operation held. */
  g_mutex_lock(&audio->mutex);
  if (audio->stats.stopped) {
    ++audio->stats.rejected_frames;
    g_mutex_unlock(&audio->mutex);
    gst_buffer_unref(buffer);
    return GST_FLOW_FLUSHING;
  }
  g_mutex_unlock(&audio->mutex);
  g_mutex_lock(&audio->operation);
  g_mutex_lock(&audio->mutex);
  if (audio->stats.stopped) result = GST_FLOW_FLUSHING;
  else if (audio->stats.creation_failed) result = GST_FLOW_ERROR;
  else if (!audio->stats.configured) result = GST_FLOW_NOT_NEGOTIATED;
  else if (bytes == 0 || !GST_CLOCK_TIME_IS_VALID(pts) ||
           !GST_CLOCK_TIME_IS_VALID(duration) || duration == 0 ||
           duration >= GST_CLOCK_TIME_NONE - pts ||
           (audio->have_last_pts && pts < audio->last_pts)) result = GST_FLOW_ERROR;
  else {
    end = pts + duration;
    /* Invalid single frames never evict valid retained input. */
    if (bytes > PENDING_BYTES || duration > PENDING_TIME) result = GST_FLOW_CUSTOM_ERROR;
    while (result == GST_FLOW_OK) {
      latest_end = MAX(audio->latest_end, end);
      if (!(audio->stats.pending_frames >= PENDING_FRAMES ||
        bytes > PENDING_BYTES - audio->stats.pending_bytes ||
        duration > PENDING_TIME - audio->stats.pending_duration ||
        (audio->stats.pending_frames > 0 && latest_end - audio->stats.oldest_pts > PENDING_TIME))) break;
      if (!audio->stats.waiting_for_parent_playing || audio->stats.ready ||
          g_queue_is_empty(&audio->pending)) {
        result = GST_FLOW_CUSTOM_ERROR;
        break;
      }
      {
        GstBuffer *oldest = g_queue_pop_head(&audio->pending);
        GList *item;
        --audio->stats.pending_frames;
        audio->stats.pending_bytes -= gst_buffer_get_size(oldest);
        audio->stats.pending_duration -= GST_BUFFER_DURATION(oldest);
        audio->stats.oldest_pts = g_queue_is_empty(&audio->pending) ? GST_CLOCK_TIME_NONE :
            GST_BUFFER_PTS((GstBuffer *)g_queue_peek_head(&audio->pending));
        audio->latest_end = 0;
        for (item = audio->pending.head; item != NULL; item = item->next) {
          GstBuffer *retained = item->data;
          audio->latest_end = MAX(audio->latest_end, GST_BUFFER_PTS(retained) + GST_BUFFER_DURATION(retained));
        }
        ++audio->stats.startup_dropped_frames;
        audio->startup_discont = TRUE;
        g_queue_push_tail(&evicted, oldest);
      }
    }
  }
  if (result == GST_FLOW_OK) {
    if (audio->stats.ready) {
      g_mutex_unlock(&audio->mutex);
      result = gst_app_src_push_buffer(GST_APP_SRC(audio->input), buffer);
      g_mutex_lock(&audio->mutex);
      if (result == GST_FLOW_OK) {
        audio->last_pts = pts;
        audio->have_last_pts = TRUE;
      } else {
        ++audio->stats.rejected_frames;
      }
      g_mutex_unlock(&audio->mutex);
      g_mutex_unlock(&audio->operation);
      return result;
    }
    if (audio->stats.pending_frames == 0) audio->stats.oldest_pts = pts;
    audio->last_pts = pts;
    audio->have_last_pts = TRUE;
    audio->latest_end = MAX(audio->latest_end, end);
    ++audio->stats.pending_frames;
    audio->stats.pending_bytes += bytes;
    audio->stats.pending_duration += duration;
    g_queue_push_tail(&audio->pending, buffer);
  } else {
    ++audio->stats.rejected_frames;
  }
  g_mutex_unlock(&audio->mutex);
  g_mutex_unlock(&audio->operation);
  /* Ref finalizers can call external code; never invoke them under our mutex. */
  discard_pending(&evicted);
  if (result != GST_FLOW_OK) gst_buffer_unref(buffer);
  return result;
}

gboolean source_clock_audio_bin_get_format(SourceClockAudioBin *audio, SourceClockAudioFormat *out) {
  gboolean ok;
  if (audio == NULL || out == NULL) return FALSE;
  g_mutex_lock(&audio->mutex);
  ok = audio->stats.configured && copy_format(&audio->format, out);
  g_mutex_unlock(&audio->mutex);
  return ok;
}

void source_clock_audio_bin_get_stats(SourceClockAudioBin *audio, SourceClockAudioBinStats *out) {
  if (audio == NULL || out == NULL) return;
  g_mutex_lock(&audio->mutex);
  *out = audio->stats;
  g_mutex_unlock(&audio->mutex);
}

static GstPadProbeReturn stop_eos(GstPad *pad, GstPadProbeInfo *info, gpointer data) {
  SourceClockAudioBin *audio = data;
  (void)pad;
  if (GST_EVENT_TYPE(GST_PAD_PROBE_INFO_EVENT(info)) != GST_EVENT_EOS)
    return GST_PAD_PROBE_OK;
  g_mutex_lock(&audio->mutex);
  audio->stats.eos_observed = TRUE;
  g_cond_broadcast(&audio->stopped_cond);
  g_mutex_unlock(&audio->mutex);
  /* Branch-local EOS must not end the shared silence/video pipeline. */
  return GST_PAD_PROBE_DROP;
}

static gboolean branch_null(SourceClockAudioBin *audio) {
  GstState state = GST_STATE_VOID_PENDING;
  GstStateChangeReturn result;
  gst_element_set_locked_state(audio->branch, TRUE);
  result = gst_element_set_state(audio->branch, GST_STATE_NULL);
  if (result == GST_STATE_CHANGE_FAILURE) return FALSE;
  result = gst_element_get_state(audio->branch, &state, NULL, 0);
  return result != GST_STATE_CHANGE_FAILURE && result != GST_STATE_CHANGE_ASYNC && state == GST_STATE_NULL;
}

/* operation held, only on our dedicated control worker. NULL state changes
 * may themselves block in a plugin: caller's 2s deadline never frees this
 * worker/object in that case. No wait or state change is run on the context. */
static gboolean stop_branch(SourceClockAudioBin *audio) {
  GstPad *src;
  GstEvent *sticky;
  gulong probe = 0;
  gboolean drained = FALSE, null_ok;
  GstState state = GST_STATE_NULL;
  gint64 deadline;
  if (audio->branch == NULL) return TRUE;
  src = gst_element_get_static_pad(audio->branch, "src");
  gst_element_get_state(audio->branch, &state, NULL, 0);
  if (src != NULL && audio->input != NULL && state >= GST_STATE_PAUSED) {
    probe = gst_pad_add_probe(src, GST_PAD_PROBE_TYPE_EVENT_DOWNSTREAM, stop_eos, audio, NULL);
    sticky = gst_pad_get_sticky_event(src, GST_EVENT_EOS, 0);
    g_mutex_lock(&audio->mutex);
    if (sticky != NULL) audio->stats.eos_observed = TRUE;
    g_mutex_unlock(&audio->mutex);
    if (sticky != NULL) gst_event_unref(sticky);
    if (gst_app_src_end_of_stream(GST_APP_SRC(audio->input)) == GST_FLOW_OK) {
      g_mutex_lock(&audio->mutex);
      audio->stats.eos_sent = TRUE;
      deadline = g_get_monotonic_time() + 500 * G_TIME_SPAN_MILLISECOND;
      while (!audio->stats.eos_observed &&
             g_cond_wait_until(&audio->stopped_cond, &audio->mutex, deadline)) {}
      drained = audio->stats.eos_observed;
      g_mutex_unlock(&audio->mutex);
    }
  }
  /* Normal path: drained EOS -> unlink/release -> NULL -> refs.
   * Fallback: lack of EOS is NOT drain success. Require NULL first to prove
   * that no streaming task can touch pads we are about to detach. */
  if (!drained) {
    g_mutex_lock(&audio->mutex);
    audio->stats.forced_null = TRUE;
    g_mutex_unlock(&audio->mutex);
    if (!branch_null(audio)) {
      if (probe != 0) gst_pad_remove_probe(src, probe);
      if (src != NULL) gst_object_unref(src);
      return FALSE;
    }
  }
  if (audio->mixer_pad != NULL) {
    GstPad *peer = gst_pad_get_peer(audio->mixer_pad);
    if (peer != NULL) {
      gst_pad_unlink(peer, audio->mixer_pad);
      gst_object_unref(peer);
    }
    gst_element_release_request_pad(audio->mixer, audio->mixer_pad);
    gst_object_unref(audio->mixer_pad);
    audio->mixer_pad = NULL;
  }
  null_ok = !drained || branch_null(audio);
  if (probe != 0) gst_pad_remove_probe(src, probe);
  if (src != NULL) gst_object_unref(src);
  if (!null_ok) return FALSE;
  if (GST_OBJECT_PARENT(audio->branch) == GST_OBJECT(audio->parent) &&
      !gst_bin_remove(GST_BIN(audio->parent), audio->branch)) return FALSE;
  gst_object_unref(audio->branch);
  audio->branch = audio->input = NULL;
  return TRUE;
}

static gpointer teardown_worker(gpointer data) {
  SourceClockAudioBin *audio = data;
  GQueue pending;
  SourceClockAudioFormat format;
  gboolean complete;
  g_mutex_lock(&audio->operation);
  g_mutex_lock(&audio->mutex);
  /* Callback destroy-notify runs after dispatch has returned. Do not call
   * stop COMPLETE merely because that callback released operation. */
  while (audio->creation_refs != 0)
    g_cond_wait(&audio->stopped_cond, &audio->mutex);
  pending = take_pending(audio);
  format = audio->format;
  memset(&audio->format, 0, sizeof(audio->format));
  audio->last_pts = 0;
  audio->have_last_pts = FALSE;
  g_mutex_unlock(&audio->mutex);
  discard_pending(&pending);
  source_clock_audio_format_clear(&format);
  complete = stop_branch(audio);
  g_mutex_unlock(&audio->operation);
  g_mutex_lock(&audio->mutex);
  audio->stats.stop_complete = complete;
  audio->stats.stop_failed = !complete;
  g_cond_broadcast(&audio->stopped_cond);
  g_mutex_unlock(&audio->mutex);
  audio_unref(audio); /* Owner reference is NEVER consumed by the worker. */
  return NULL;
}

SourceClockAudioStopResult source_clock_audio_bin_stop(SourceClockAudioBin *audio) {
  GSource *source = NULL;
  SourceClockAudioStopResult result;
  gint64 deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
  if (audio == NULL) return SOURCE_CLOCK_AUDIO_STOP_COMPLETE;
  g_mutex_lock(&audio->mutex);
  if (!audio->stats.stopped) {
    audio->stats.stopped = TRUE;
    audio->stats.configured = FALSE;
    audio->stats.ready = FALSE;
    audio->stats.waiting_for_parent_playing = FALSE;
    ++audio->stats.operation_generation;
    source = audio->creation;
    audio->creation = NULL;
    g_atomic_int_inc(&audio->refs);
    audio->stop_worker = g_thread_new("audio-bin-stop", teardown_worker, audio);
  }
  g_mutex_unlock(&audio->mutex);
  if (source != NULL) {
    g_source_destroy(source);
    g_source_unref(source);
  }
  g_mutex_lock(&audio->mutex);
  if (audio->context == NULL || !g_main_context_is_owner(audio->context)) {
    while (!audio->stats.stop_complete && !audio->stats.stop_failed &&
           g_cond_wait_until(&audio->stopped_cond, &audio->mutex, deadline)) {}
  }
  result = audio->stats.stop_complete ? SOURCE_CLOCK_AUDIO_STOP_COMPLETE :
      audio->stats.stop_failed ? SOURCE_CLOCK_AUDIO_STOP_FAILED_RETAINED :
      (audio->context != NULL && g_main_context_is_owner(audio->context)) ?
      SOURCE_CLOCK_AUDIO_STOP_PENDING : SOURCE_CLOCK_AUDIO_STOP_TIMEOUT;
  g_mutex_unlock(&audio->mutex);
  return result;
}

static void audio_unref(gpointer data) {
  SourceClockAudioBin *audio = data;
  if (!g_atomic_int_dec_and_test(&audio->refs)) return;
  if (audio->mixer != NULL) gst_object_unref(audio->mixer);
  if (audio->parent != NULL) gst_object_unref(audio->parent);
  if (audio->context != NULL) g_main_context_unref(audio->context);
  g_mutex_clear(&audio->mutex);
  g_mutex_clear(&audio->operation);
  g_cond_clear(&audio->stopped_cond);
  g_free(audio);
}

gboolean source_clock_audio_bin_free(SourceClockAudioBin *audio) {
  if (audio == NULL) return TRUE;
  if (source_clock_audio_bin_stop(audio) != SOURCE_CLOCK_AUDIO_STOP_COMPLETE) return FALSE;
  if (audio->stop_worker != NULL) g_thread_join(audio->stop_worker);
  audio_unref(audio);
  return TRUE;
}
