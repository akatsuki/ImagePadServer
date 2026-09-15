#include "source_clock_pipeline_internal.h"
#include "source_clock_encoder_policy.h"

#include <gst/rtsp/gstrtsptransport.h>
#include <gst/app/gstappsrc.h>
#include <gst/app/gstappsink.h>
#include <stdio.h>
#include <string.h>

static int require_encoder_comparison_policy(void) {
  GError *error = NULL;
  gchar *path = g_build_filename(g_get_tmp_dir(),
                                 "imagepad-source-clock-encoder-policy.json", NULL);
  gchar *contents = NULL;
  gsize length = 0;
  GstElement *pipeline;
  int ok = 1;
  source_clock_encoder_policy_reset();
  if (!source_clock_encoder_policy_configure("stillimage", path, &error)) ok = 0;
  pipeline = source_clock_create_pipeline(
      "rtsp://127.0.0.1:1/source_clock_policy_test", "policy.mp4",
      320, 180, 60, 30, 1000, 1200, 2000, 128000, 30, &error);
  if (pipeline == NULL || error != NULL ||
      !source_clock_encoder_policy_apply(pipeline, &error) ||
      !g_file_get_contents(path, &contents, &length, &error)) ok = 0;
  if (contents == NULL || strstr(contents, "\"tune\":") == NULL ||
      strstr(contents, "Still image") == NULL || strstr(contents, "\"gop\":") == NULL ||
      strstr(contents, "\"b_frames\":") == NULL || strstr(contents, "\"fps\":\"30/1\"") == NULL ||
      strstr(contents, "\"payloader\":\"source_clock_audio_payloader\"") == NULL ||
      strstr(contents, "video_rtsp_queue") != NULL || strstr(contents, "\"protocol\":") == NULL) ok = 0;
  if (!ok) fprintf(stderr, "encoder comparison policy report mismatch: %s\n",
                   error == NULL ? (contents == NULL ? "missing report" : contents) : error->message);
  if (pipeline != NULL) gst_object_unref(pipeline);
  g_clear_error(&error);
  g_free(contents);
  g_remove(path);
  g_free(path);
  source_clock_encoder_policy_reset();
  return ok;
}

static int require_framerate(GstElement *element, int expected, const char *label) {
  GstCaps *caps = NULL;
  const GstStructure *structure;
  int numerator = 0;
  int denominator = 1;
  g_object_get(element, "caps", &caps, NULL);
  if (caps == NULL || gst_caps_is_empty(caps)) {
    fprintf(stderr, "%s caps are missing\n", label);
    if (caps != NULL) gst_caps_unref(caps);
    return 0;
  }
  structure = gst_caps_get_structure(caps, 0);
  if (!gst_structure_get_fraction(structure, "framerate", &numerator, &denominator) ||
      numerator != expected || denominator != 1) {
    fprintf(stderr, "%s framerate=%d/%d, want %d/1\n",
            label, numerator, denominator, expected);
    gst_caps_unref(caps);
    return 0;
  }
  gst_caps_unref(caps);
  return 1;
}

/* Catch accidentally retaining the real ingress or removing shared output/video
 * elements. Inspect the production factory in NULL state: no network or files. */
static int require_tree(GstElement *pipeline, gboolean include_real_audio_ingress,
                        gboolean include_fixed_pcm_audio,
                        gboolean include_rtsp_audio,
                        const char *label, guint *element_count) {
  static const char *shared_names[] = {
      "audio_silence", "audio_mixer", "source_clock_audio_encoder", "audio_tee",
      "audio_record_queue", "rtsp_sink", "record_mux", "record_sink",
      "video_input", "video_decoder", "video_decoded_sink",
      "video_scheduled_input", "video_output_rate", "video_output_caps",
      "source_clock_video_encoder", "video_tee", "video_rtsp_queue", "video_record_queue"};
  static const char *ingress_names[] = {"audio_input", "audio_decoder"};
  static const char *fixed_names[] = {"audio_fixed_pcm_input", "audio_fixed_pcm_queue"};
  GstIterator *iterator;
  GstIteratorResult result;
  GValue item = G_VALUE_INIT;
  guint converters = 0;
  guint resamplers = 0;
  gboolean force_software_video_decoder = FALSE;
  size_t i;
  int ok = 1;

  printf("%s inventory:", label);
  {
    GstElement *queue = gst_bin_get_by_name(GST_BIN(pipeline), "audio_rtsp_queue");
    gboolean present = queue != NULL;
    printf(" audio_rtsp_queue=%s", present ? "present" : "absent");
    if (present != include_rtsp_audio) {
      fprintf(stderr, "%s: audio_rtsp_queue is %s, want %s\n", label,
              present ? "present" : "absent", include_rtsp_audio ? "present" : "absent");
      ok = 0;
    }
    if (queue != NULL) gst_object_unref(queue);
  }
  for (i = 0; i < G_N_ELEMENTS(ingress_names); ++i) {
    GstElement *element = gst_bin_get_by_name(GST_BIN(pipeline), ingress_names[i]);
    gboolean present = element != NULL;
    printf(" %s=%s", ingress_names[i], present ? "present" : "absent");
    if (present != include_real_audio_ingress) {
      fprintf(stderr, "%s: %s is %s, want %s\n", label, ingress_names[i],
              present ? "present" : "absent", include_real_audio_ingress ? "present" : "absent");
      ok = 0;
    }
    if (element != NULL) gst_object_unref(element);
  }
  for (i = 0; i < G_N_ELEMENTS(fixed_names); ++i) {
    GstElement *element = gst_bin_get_by_name(GST_BIN(pipeline), fixed_names[i]);
    gboolean present = element != NULL;
    printf(" %s=%s", fixed_names[i], present ? "present" : "absent");
    if (present != include_fixed_pcm_audio) {
      fprintf(stderr, "%s: %s is %s, want %s\n", label, fixed_names[i],
              present ? "present" : "absent",
              include_fixed_pcm_audio ? "present" : "absent");
      ok = 0;
    }
    if (element != NULL) gst_object_unref(element);
  }
  for (i = 0; i < G_N_ELEMENTS(shared_names); ++i) {
    GstElement *element = gst_bin_get_by_name(GST_BIN(pipeline), shared_names[i]);
    printf(" %s=%s", shared_names[i], element != NULL ? "present" : "absent");
    if (element == NULL) {
      fprintf(stderr, "%s: shared element %s is missing\n", label, shared_names[i]);
      ok = 0;
    } else {
      gst_object_unref(element);
    }
  }
  {
    GstElement *video_decoder = gst_bin_get_by_name(GST_BIN(pipeline), "video_decoder");
    if (video_decoder == NULL) {
      fprintf(stderr, "%s: video decoder is missing\n", label);
      ok = 0;
    } else {
      g_object_get(video_decoder, "force-sw-decoders", &force_software_video_decoder, NULL);
      if (!force_software_video_decoder) {
        fprintf(stderr, "%s: video decoder permits hardware-memory output\n", label);
        ok = 0;
      }
      gst_object_unref(video_decoder);
    }
  }
  {
    GstElement *decoded_sink = gst_bin_get_by_name(
        GST_BIN(pipeline), "video_decoded_sink");
    gboolean async = TRUE;
    if (decoded_sink == NULL) {
      fprintf(stderr, "%s: decoded video sink is missing\n", label);
      ok = 0;
    } else {
      g_object_get(decoded_sink, "async", &async, NULL);
      if (async) {
        fprintf(stderr,
                "%s: decoded video sink participates in pipeline preroll\n",
                label);
        ok = 0;
      }
      gst_object_unref(decoded_sink);
    }
  }
  /* Anonymous convert/resample elements must also survive. Count direct
   * children only; decodebin and RTSP sink own their internal elements. */
  *element_count = 0;
  iterator = gst_bin_iterate_elements(GST_BIN(pipeline));
  while ((result = gst_iterator_next(iterator, &item)) == GST_ITERATOR_OK) {
    GstElement *element = GST_ELEMENT(g_value_get_object(&item));
    GstElementFactory *factory = gst_element_get_factory(element);
    const char *name = factory == NULL ? "" : gst_plugin_feature_get_name(GST_PLUGIN_FEATURE(factory));
    ++*element_count;
    if (strcmp(name, "audioconvert") == 0) ++converters;
    if (strcmp(name, "audioresample") == 0) ++resamplers;
    g_value_unset(&item);
  }
  gst_iterator_free(iterator);
  printf(" audioconvert=%u audioresample=%u direct-elements=%u\n",
         converters, resamplers, *element_count);
  if (result != GST_ITERATOR_DONE || converters != 1 || resamplers != 1) {
    fprintf(stderr, "%s: incomplete inventory or missing audio convert/resample\n", label);
    ok = 0;
  }
  return ok;
}

static GstElement *linked_downstream_element(GstElement *element) {
  GstPad *src = NULL;
  GstPad *peer = NULL;
  GstElement *downstream = NULL;
  if (element == NULL) return NULL;
  src = gst_element_get_static_pad(element, "src");
  if (src != NULL) peer = gst_pad_get_peer(src);
  if (peer != NULL) downstream = gst_pad_get_parent_element(peer);
  if (peer != NULL) gst_object_unref(peer);
  if (src != NULL) gst_object_unref(src);
  return downstream;
}

static int require_direct_video_path(GstElement *pipeline, const char *label) {
  static const char *removed[] = {"video_black", "video_mixer"};
  const char *expected_factories[] = {
      "queue", "videoconvert", "videorate", "capsfilter", "x264enc"};
  GstElement *chain[5] = {0};
  GstElement *scheduled;
  GstElementFactory *factory;
  const char *factory_name;
  int ok = 1;
  size_t i;

  for (i = 0; i < G_N_ELEMENTS(removed); ++i) {
    GstElement *element = gst_bin_get_by_name(GST_BIN(pipeline), removed[i]);
    if (element != NULL) {
      fprintf(stderr, "%s: obsolete video element %s is still present\n",
              label, removed[i]);
      gst_object_unref(element);
      ok = 0;
    }
  }
  scheduled = gst_bin_get_by_name(GST_BIN(pipeline), "video_scheduled_input");
  if (scheduled == NULL) return 0;
  chain[0] = linked_downstream_element(scheduled);
  for (i = 1; i < G_N_ELEMENTS(chain); ++i)
    chain[i] = linked_downstream_element(chain[i - 1]);
  for (i = 0; i < G_N_ELEMENTS(chain); ++i) {
    factory = chain[i] == NULL ? NULL : gst_element_get_factory(chain[i]);
    factory_name = factory == NULL ? NULL :
        gst_plugin_feature_get_name(GST_PLUGIN_FEATURE(factory));
    if (factory_name == NULL || strcmp(factory_name, expected_factories[i]) != 0) {
      fprintf(stderr, "%s: scheduled video chain step %zu is %s, want %s\n",
              label, i, factory_name == NULL ? "missing" : factory_name,
              expected_factories[i]);
      ok = 0;
    }
  }
  if (chain[2] == NULL ||
      strcmp(GST_ELEMENT_NAME(chain[2]), "video_output_rate") != 0 ||
      chain[3] == NULL ||
      strcmp(GST_ELEMENT_NAME(chain[3]), "video_output_caps") != 0 ||
      chain[4] == NULL ||
      strcmp(GST_ELEMENT_NAME(chain[4]), "source_clock_video_encoder") != 0) {
    fprintf(stderr, "%s: scheduled video chain has unexpected named elements\n", label);
    ok = 0;
  }
  for (i = 0; i < G_N_ELEMENTS(chain); ++i)
    if (chain[i] != NULL) gst_object_unref(chain[i]);
  gst_object_unref(scheduled);
  printf("%s direct scheduled video path: %s\n", label, ok ? "PASS" : "FAIL");
  return ok;
}

static int require_message_forward(GstElement *pipeline, const char *label) {
  gboolean enabled = FALSE;
  g_object_get(pipeline, "message-forward", &enabled, NULL);
  if (!enabled) {
    fprintf(stderr, "%s: message-forward is disabled\n", label);
    return 0;
  }
  return 1;
}

/* Count existing request pads without requesting any new ones. Inspect their
 * linked queues and the manual audio payloader before any state transition. */
static int require_rtsp_tracks(GstElement *pipeline, gboolean include_rtsp_audio,
                               gboolean include_custom_audio_payloader,
                               guint expected_pads, const char *label) {
  GstElement *sink = gst_bin_get_by_name(GST_BIN(pipeline), "rtsp_sink");
  GstIterator *iterator;
  GstIteratorResult result;
  GValue item = G_VALUE_INIT;
  guint pads = 0;
  guint video_pads = 0;
  guint audio_pads = 0;
  int ok = 1;
  if (sink == NULL) return 0;
  iterator = gst_element_iterate_sink_pads(sink);
  while ((result = gst_iterator_next(iterator, &item)) == GST_ITERATOR_OK) {
    GstPad *pad = GST_PAD(g_value_get_object(&item));
    GstPad *peer = gst_pad_get_peer(pad);
    GstElement *upstream = peer == NULL ? NULL : gst_pad_get_parent_element(peer);
    GstElement *payloader = NULL;
    const char *expected_queue = NULL;
    gboolean audio_pad = g_strcmp0(GST_PAD_NAME(pad), "sink_1") == 0;
    ++pads;
    if (g_strcmp0(GST_PAD_NAME(pad), "sink_0") == 0) {
      ++video_pads;
      expected_queue = "video_rtsp_queue";
    } else if (audio_pad) {
      ++audio_pads;
      expected_queue = "audio_rtsp_queue";
    } else {
      ok = 0;
    }
    if (upstream == NULL || expected_queue == NULL ||
        g_strcmp0(GST_ELEMENT_NAME(upstream), expected_queue) != 0) ok = 0;
    g_object_get(pad, "payloader", &payloader, NULL);
    if (audio_pad && include_custom_audio_payloader) {
      GstElementFactory *factory = payloader == NULL ? NULL : gst_element_get_factory(payloader);
      const char *factory_name = factory == NULL ? NULL :
          gst_plugin_feature_get_name(GST_PLUGIN_FEATURE(factory));
      if (!include_rtsp_audio || payloader == NULL ||
          g_strcmp0(GST_ELEMENT_NAME(payloader), "source_clock_audio_payloader") != 0 ||
          g_strcmp0(factory_name, "rtpmp4gpay") != 0) ok = 0;
    } else if (audio_pad && payloader != NULL) {
      ok = 0;
    } else if (payloader != NULL) {
      ok = 0;
    }
    if (payloader != NULL) gst_object_unref(payloader);
    if (upstream != NULL) gst_object_unref(upstream);
    if (peer != NULL) gst_object_unref(peer);
    g_value_unset(&item);
  }
  gst_iterator_free(iterator);
  gst_object_unref(sink);
  if (result != GST_ITERATOR_DONE || pads != expected_pads || video_pads != 1 ||
      audio_pads != (include_rtsp_audio ? 1u : 0u)) ok = 0;
  printf("%s RTSP sink pads=%u (video=%u audio=%u), custom-audio-payloader=%s, want %u: %s\n",
         label, pads, video_pads, audio_pads,
         include_custom_audio_payloader ? "present" : "absent",
         expected_pads, ok ? "PASS" : "FAIL");
  return ok;
}

/* RTSP setup can remain ASYNC for seconds after initial media preroll.  Keep
 * the fixed PCM ingress bounded but large enough to span that startup phase. */
static int require_fixed_pcm_queue_policy(GstElement *pipeline,
                                          gboolean include_fixed_pcm_audio,
                                          const char *label) {
  GstElement *queue = gst_bin_get_by_name(GST_BIN(pipeline), "audio_fixed_pcm_queue");
  guint max_buffers = G_MAXUINT;
  guint max_bytes = G_MAXUINT;
  guint64 max_time = G_MAXUINT64;
  gint leaky = -1;
  int ok = 1;
  if (!include_fixed_pcm_audio) return queue == NULL;
  if (queue == NULL) return 0;
  g_object_get(queue,
               "max-size-buffers", &max_buffers,
               "max-size-bytes", &max_bytes,
               "max-size-time", &max_time,
               "leaky", &leaky,
               NULL);
  if (max_buffers != 192 || max_bytes != 1024 * 1024 ||
      max_time != 4 * GST_SECOND ||
      leaky != 2 /* downstream: discard stale buffers */) {
    fprintf(stderr,
            "%s: fixed PCM queue buffers=%u bytes=%u time=%llu leaky=%d, "
            "want 192/1MiB/4s/downstream\n",
            label, max_buffers, max_bytes, (unsigned long long)max_time, leaky);
    ok = 0;
  }
  gst_object_unref(queue);
  return ok;
}

/* RTSP setup can block downstream for multiple seconds.  The scheduler must
 * discard stale black/hold frames instead of accumulating the receiver's
 * pre-connect wall-clock gap and draining it as a fast-forward burst. */
static int require_scheduled_source_queue_policy(GstElement *pipeline,
                                                 const char *label) {
  GstElement *source = gst_bin_get_by_name(
      GST_BIN(pipeline), "video_scheduled_input");
  guint64 max_buffers = G_MAXUINT64;
  guint64 max_bytes = G_MAXUINT64;
  guint64 max_time = G_MAXUINT64;
  gint leaky_type = -1;
  int ok = 1;
  if (source == NULL) return 0;
  g_object_get(source,
               "max-buffers", &max_buffers,
               "max-bytes", &max_bytes,
               "max-time", &max_time,
               "leaky-type", &leaky_type,
               NULL);
  if (max_buffers != 4 || max_bytes != 0 || max_time != 0 ||
      leaky_type != 2 /* downstream: discard oldest buffers */) {
    fprintf(stderr,
            "%s: scheduled source buffers=%llu bytes=%llu time=%llu "
            "leaky-type=%d, want 4/0/0/downstream\n",
            label, (unsigned long long)max_buffers,
            (unsigned long long)max_bytes, (unsigned long long)max_time,
            leaky_type);
    ok = 0;
  }
  gst_object_unref(source);
  return ok;
}

/* RTSP ANNOUNCE/SETUP temporarily skews the two recording queue tasks.  Keep
 * enough bounded encoded media for mp4mux to resume without letting either
 * one-second default queue deadlock the live RTSP tee. */
static int require_recording_queue_policy(GstElement *pipeline,
                                          const char *label) {
  static const char *names[] = {"video_record_queue", "audio_record_queue"};
  size_t i;
  int ok = 1;
  for (i = 0; i < G_N_ELEMENTS(names); ++i) {
    GstElement *queue = gst_bin_get_by_name(GST_BIN(pipeline), names[i]);
    guint max_buffers = G_MAXUINT;
    guint max_bytes = G_MAXUINT;
    guint64 max_time = G_MAXUINT64;
    gint leaky = -1;
    if (queue == NULL) return 0;
    g_object_get(queue,
                 "max-size-buffers", &max_buffers,
                 "max-size-bytes", &max_bytes,
                 "max-size-time", &max_time,
                 "leaky", &leaky,
                 NULL);
    if (max_buffers != 0 || max_bytes != 16 * 1024 * 1024 ||
        max_time != 4 * GST_SECOND || leaky != 0) {
      fprintf(stderr,
              "%s: %s buffers=%u bytes=%u time=%llu leaky=%d, "
              "want 0/16MiB/4s/non-leaky\n",
              label, names[i], max_buffers, max_bytes,
              (unsigned long long)max_time, leaky);
      ok = 0;
    }
    gst_object_unref(queue);
  }
  return ok;
}

static int require_profile(GstElement *pipeline, guint expected_rtx_time,
                           const char *label) {
  GstElement *sink;
  GstElement *video_encoder;
  GstElement *audio_encoder;
  GstElement *audio_mixer;
  GstElement *scheduled_source;
  GstElement *output_caps;
  guint64 tcp_timeout = G_MAXUINT64;
  guint rtx_time = G_MAXUINT;
  gboolean keep_alive = TRUE;
  GstRTSPLowerTrans protocols = GST_RTSP_LOWER_TRANS_UNKNOWN;
  gint video_bitrate = 0;
  gint video_pass = -1;
  gint video_quantizer = -1;
  gint video_speed_preset = -1;
  guint key_int = 0;
  guint vbv_buf_capacity = 0;
  gint audio_bitrate = 0;
  guint64 audio_latency = G_MAXUINT64;
  guint64 audio_min_upstream_latency = G_MAXUINT64;
  gchar *option_string = NULL;

  sink = gst_bin_get_by_name(GST_BIN(pipeline), "rtsp_sink");
  if (sink == NULL) {
    fprintf(stderr, "source-clock RTSP sink was not found\n");
    gst_object_unref(pipeline);
    return 1;
  }
  g_object_get(sink, "tcp-timeout", &tcp_timeout, NULL);
  if (tcp_timeout != 0) {
    fprintf(stderr, "source-clock RTSP tcp-timeout=%llu, want 0\n",
            (unsigned long long)tcp_timeout);
    gst_object_unref(sink);
    gst_object_unref(pipeline);
    return 1;
  }
  g_object_get(sink, "protocols", &protocols, NULL);
  GstRTSPLowerTrans expected_protocols =
#if IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP
      GST_RTSP_LOWER_TRANS_UDP;
#else
      GST_RTSP_LOWER_TRANS_TCP;
#endif
  if (protocols != expected_protocols) {
    fprintf(stderr, "source-clock RTSP protocols=%u, want %u\n",
            (unsigned)protocols, (unsigned)expected_protocols);
    gst_object_unref(sink);
    gst_object_unref(pipeline);
    return 1;
  }
  g_object_get(sink, "do-rtsp-keep-alive", &keep_alive, NULL);
  if (keep_alive) {
    fprintf(stderr, "source-clock RTSP keep-alive is enabled, want disabled\n");
    gst_object_unref(sink);
    gst_object_unref(pipeline);
    return 1;
  }
  g_object_get(sink, "rtx-time", &rtx_time, NULL);
  if (rtx_time != expected_rtx_time) {
    fprintf(stderr, "%s: source-clock RTSP rtx-time=%u, want %u\n",
            label, rtx_time, expected_rtx_time);
    gst_object_unref(sink);
    gst_object_unref(pipeline);
    return 1;
  }

  video_encoder = gst_bin_get_by_name(GST_BIN(pipeline), "source_clock_video_encoder");
  audio_encoder = gst_bin_get_by_name(GST_BIN(pipeline), "source_clock_audio_encoder");
  audio_mixer = gst_bin_get_by_name(GST_BIN(pipeline), "audio_mixer");
  scheduled_source = gst_bin_get_by_name(GST_BIN(pipeline), "video_scheduled_input");
  output_caps = gst_bin_get_by_name(GST_BIN(pipeline), "video_output_caps");
  if (video_encoder == NULL || audio_encoder == NULL || audio_mixer == NULL ||
      scheduled_source == NULL || output_caps == NULL) {
    fprintf(stderr, "source-clock profile elements are missing\n");
    return 1;
  }
  g_object_get(video_encoder,
               "bitrate", &video_bitrate,
               "pass", &video_pass,
               "quantizer", &video_quantizer,
               "speed-preset", &video_speed_preset,
               "vbv-buf-capacity", &vbv_buf_capacity,
               "key-int-max", &key_int,
               "option-string", &option_string,
               NULL);
  if (video_bitrate != 1200 || video_pass != 5 || video_quantizer != 23 ||
      video_speed_preset != 2 || vbv_buf_capacity != 1000 || key_int != 30 ||
      option_string == NULL ||
      strstr(option_string, "vbv-maxrate=1200") == NULL ||
      strstr(option_string, "vbv-bufsize=2000") == NULL ||
      strstr(option_string, "aud=1") == NULL ||
      strstr(option_string, "repeat-headers=1") == NULL) {
    fprintf(stderr,
            "source-clock video encoder profile mismatch bitrate=%d pass=%d quantizer=%d preset=%d vbv-cap=%u gop=%u options=%s\n",
            video_bitrate, video_pass, video_quantizer, video_speed_preset,
            vbv_buf_capacity, key_int, option_string == NULL ? "" : option_string);
    return 1;
  }
  g_object_get(audio_encoder, "bitrate", &audio_bitrate, NULL);
  if (audio_bitrate != 128000) {
    fprintf(stderr, "source-clock audio bitrate=%d, want 128000\n", audio_bitrate);
    return 1;
  }
  g_object_get(audio_mixer,
               "latency", &audio_latency,
               "min-upstream-latency", &audio_min_upstream_latency,
               NULL);
  if (audio_latency != 100 * GST_MSECOND ||
      audio_min_upstream_latency != 200 * GST_MSECOND) {
    fprintf(stderr,
            "source-clock audio mixer latency=%llu min-upstream-latency=%llu, want 100ms/200ms\n",
            (unsigned long long)audio_latency,
            (unsigned long long)audio_min_upstream_latency);
    return 1;
  }
  if (!require_framerate(scheduled_source, 30, "delivery scheduler") ||
      !require_framerate(output_caps, 30, "delivery output")) {
    return 1;
  }

  g_free(option_string);
  gst_object_unref(output_caps);
  gst_object_unref(scheduled_source);
  gst_object_unref(audio_encoder);
  gst_object_unref(audio_mixer);
  gst_object_unref(video_encoder);
  gst_object_unref(sink);
  gst_object_unref(pipeline);
  return 0;
}

/* Run the factory's real scheduled-output boundary without network/encoding.
 * A late first buffer and a later gap must not make a 30fps delivery catch up
 * at 60fps. Feed at the source's advertised cadence, just as the scheduler does.
 * Queue pressure is excluded here; this is a timestamp/rate contract test. */
static int require_late_output_cadence(int output_fps, GstClockTime first_pts) {
  GError *error = NULL;
  GstElement *factory = source_clock_create_pipeline(
      "rtsp://127.0.0.1:1/cadence", "unused-cadence.mp4", 320, 180,
      60, output_fps, 1000, 1200, 2000, 128000, 30, &error);
  GstElement *pipeline = NULL, *source = NULL, *rate = NULL;
  GstElement *capsfilter = NULL, *sink = NULL;
  GstCaps *caps = NULL;
  GstClockTime previous = GST_CLOCK_TIME_NONE;
  gboolean gap_preserved = FALSE;
  int numerator = 0, denominator = 0, failed = 1;
  unsigned i, frames = 0;
  if (factory == NULL || error != NULL) goto done;
  source = gst_bin_get_by_name(GST_BIN(factory), "video_scheduled_input");
  rate = gst_bin_get_by_name(GST_BIN(factory), "video_output_rate");
  capsfilter = gst_bin_get_by_name(GST_BIN(factory), "video_output_caps");
  if (source == NULL || rate == NULL || capsfilter == NULL) goto done;
  g_object_get(source, "caps", &caps, NULL);
  if (caps == NULL || !gst_structure_get_fraction(gst_caps_get_structure(caps, 0),
      "framerate", &numerator, &denominator) || numerator <= 0 || denominator != 1) goto done;
  /* Removing NULL-state elements unlinks the unused factory branches. */
  gst_bin_remove(GST_BIN(factory), source);
  gst_bin_remove(GST_BIN(factory), rate);
  gst_bin_remove(GST_BIN(factory), capsfilter);
  pipeline = gst_pipeline_new(NULL);
  sink = gst_element_factory_make("appsink", NULL);
  if (pipeline == NULL || sink == NULL) goto done;
  gst_bin_add_many(GST_BIN(pipeline), source, rate, capsfilter, sink, NULL);
  g_object_set(source, "is-live", FALSE, "max-buffers", (guint64)0,
      "max-bytes", (guint64)0, "leaky-type", 0, NULL);
  g_object_set(sink, "sync", FALSE, "async", FALSE, NULL);
  if (!gst_element_link_many(source, rate, capsfilter, sink, NULL) ||
      gst_element_set_state(pipeline, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE) goto done;
  for (i = 0; i < 120; ++i) {
    GstBuffer *buffer = gst_buffer_new_allocate(NULL, 320 * 180 * 3 / 2, NULL);
    GST_BUFFER_PTS(buffer) = first_pts +
        gst_util_uint64_scale(i, GST_SECOND, numerator) + (i >= 60 ? 2 * GST_SECOND : 0);
    GST_BUFFER_DURATION(buffer) = GST_SECOND / numerator;
    if (gst_app_src_push_buffer(GST_APP_SRC(source), buffer) != GST_FLOW_OK) goto done;
  }
  if (gst_app_src_end_of_stream(GST_APP_SRC(source)) != GST_FLOW_OK) goto done;
  for (;;) {
    GstSample *sample = gst_app_sink_try_pull_sample(GST_APP_SINK(sink), 2 * GST_SECOND);
    GstClockTime pts;
    if (sample == NULL) {
      if (!gst_app_sink_is_eos(GST_APP_SINK(sink))) goto done;
      break;
    }
    pts = GST_BUFFER_PTS(gst_sample_get_buffer(sample));
    gst_sample_unref(sample);
    if (!GST_CLOCK_TIME_IS_VALID(pts) || (frames == 0 && pts != first_pts) ||
        (GST_CLOCK_TIME_IS_VALID(previous) &&
         (pts <= previous || pts - previous + 1 < GST_SECOND / output_fps))) {
      fprintf(stderr, "late delivery exceeds %dfps: frame=%u previous=%" G_GUINT64_FORMAT
          " pts=%" G_GUINT64_FORMAT " source_fps=%d\n", output_fps, frames, previous, pts, numerator);
      goto done;
    }
    if (GST_CLOCK_TIME_IS_VALID(previous) && pts - previous >= 2 * GST_SECOND)
      gap_preserved = TRUE;
    previous = pts;
    frames++;
  }
  if (frames != 120 || !gap_preserved) {
    fprintf(stderr, "late delivery lost frames or compressed its gap: frames=%u gap=%d\n",
        frames, gap_preserved);
    goto done;
  }
  printf("late/gapped delivery cadence: PASS (fps=%d first=%" G_GUINT64_FORMAT
      " frames=%u)\n", output_fps, first_pts, frames);
  failed = 0;
done:
  if (pipeline != NULL) {
    gst_element_set_state(pipeline, GST_STATE_NULL);
    gst_object_unref(pipeline);
  }
  if (caps != NULL) gst_caps_unref(caps);
  if (source != NULL) gst_object_unref(source);
  if (rate != NULL) gst_object_unref(rate);
  if (capsfilter != NULL) gst_object_unref(capsfilter);
  if (factory != NULL) gst_object_unref(factory);
  g_clear_error(&error);
  return !failed;
}

int main(int argc, char **argv) {
  static const struct {
    const char *label;
    int factory; /* 0: default, 1: ingress compatibility, 2: options, 3: routing */
    gboolean ingress;
    gboolean fixed_pcm_audio;
    gboolean rtsp_audio;
    gboolean custom_audio_payloader;
    gboolean disable_rtsp_rtx;
    guint rtx_time;
    guint elements;
    guint rtsp_pads;
  } cases[] = {
      {"default", 0, TRUE, FALSE, TRUE, TRUE, FALSE, 500, 37, 2},
      {"ingress-true-compat", 1, TRUE, FALSE, TRUE, TRUE, FALSE, 500, 37, 2},
      {"ingress-false-compat", 1, FALSE, FALSE, TRUE, TRUE, FALSE, 500, 35, 2},
      {"ingress-true-rtsp-true", 2, TRUE, FALSE, TRUE, TRUE, FALSE, 500, 37, 2},
      {"ingress-false-rtsp-true", 2, FALSE, FALSE, TRUE, TRUE, FALSE, 500, 35, 2},
      {"ingress-false-rtsp-true-auto-payloader", 2, FALSE, FALSE, TRUE, FALSE, FALSE, 500, 35, 2},
      {"runtime-auto-payloader-default-rtx", 2, TRUE, FALSE, TRUE, FALSE, FALSE, 500, 37, 2},
      {"runtime-auto-payloader-no-rtx", 2, TRUE, FALSE, TRUE, FALSE, TRUE, 0, 37, 2},
      {"ingress-true-rtsp-false", 2, TRUE, FALSE, FALSE, TRUE, FALSE, 500, 36, 1},
      {"ingress-false-rtsp-false", 2, FALSE, FALSE, FALSE, FALSE, FALSE, 500, 34, 1},
      {"fixed-pcm-routing", 3, FALSE, TRUE, TRUE, TRUE, FALSE, 500, 37, 2}};
  guint element_counts[G_N_ELEMENTS(cases)] = {0};
  size_t i;
  int failed = 0;
  gst_init(&argc, &argv);
  if (!require_late_output_cadence(30, 1500 * GST_MSECOND) ||
      !require_late_output_cadence(30, 3000 * GST_SECOND) ||
      !require_late_output_cadence(60, 1500 * GST_MSECOND)) return 1;
  for (i = 0; i < G_N_ELEMENTS(cases); ++i) {
    GError *error = NULL;
    GstElement *pipeline;
    GstState state = GST_STATE_VOID_PENDING;
    if (cases[i].factory == 0) {
      pipeline = source_clock_create_pipeline(
          "rtsp://127.0.0.1:1/source_clock_policy_test",
          "source-clock-policy-test.mp4", 320, 180,
          60, 30, 1000, 1200, 2000, 128000, 30, &error);
    } else if (cases[i].factory == 1) {
      pipeline = source_clock_create_pipeline_with_audio_ingress(
          "rtsp://127.0.0.1:1/source_clock_policy_test",
          "source-clock-policy-test.mp4", 320, 180,
          60, 30, 1000, 1200, 2000, 128000, 30, cases[i].ingress, &error);
    } else if (cases[i].factory == 2) {
      pipeline = source_clock_create_pipeline_with_audio_options(
          "rtsp://127.0.0.1:1/source_clock_policy_test",
          "source-clock-policy-test.mp4", 320, 180,
          60, 30, 1000, 1200, 2000, 128000, 30,
          cases[i].ingress, cases[i].rtsp_audio,
          cases[i].custom_audio_payloader, cases[i].disable_rtsp_rtx,
          &error);
    } else {
      pipeline = source_clock_create_pipeline_with_audio_routing(
          "rtsp://127.0.0.1:1/source_clock_policy_test",
          "source-clock-policy-test.mp4", 320, 180,
          60, 30, 1000, 1200, 2000, 128000, 30,
          cases[i].ingress, cases[i].fixed_pcm_audio, cases[i].rtsp_audio,
          cases[i].custom_audio_payloader, cases[i].disable_rtsp_rtx,
          &error);
    }
    if (error != NULL || pipeline == NULL) {
      fprintf(stderr, "%s: source-clock pipeline construction failed: %s\n",
              cases[i].label, error == NULL ? "unknown error" : error->message);
      if (error != NULL) g_error_free(error);
      if (pipeline != NULL) gst_object_unref(pipeline);
      return 1;
    }
    if (gst_element_get_state(pipeline, &state, NULL, 0) != GST_STATE_CHANGE_SUCCESS ||
        state != GST_STATE_NULL) {
      fprintf(stderr, "%s: factory must remain in NULL state\n", cases[i].label);
      failed = 1;
    }
    if (!require_tree(pipeline, cases[i].ingress, cases[i].fixed_pcm_audio,
                      cases[i].rtsp_audio,
                      cases[i].label, &element_counts[i])) {
      failed = 1;
    }
    if (!require_message_forward(pipeline, cases[i].label)) {
      failed = 1;
    }
    if (element_counts[i] != cases[i].elements) {
      fprintf(stderr, "%s: direct-elements=%u, want %u\n",
              cases[i].label, element_counts[i], cases[i].elements);
      failed = 1;
    }
    if (!require_rtsp_tracks(pipeline, cases[i].rtsp_audio,
                             cases[i].custom_audio_payloader,
                             cases[i].rtsp_pads, cases[i].label)) {
      failed = 1;
    }
    if (!require_fixed_pcm_queue_policy(pipeline, cases[i].fixed_pcm_audio,
                                        cases[i].label)) {
      failed = 1;
    }
    if (!require_scheduled_source_queue_policy(pipeline, cases[i].label)) {
      failed = 1;
    }
    if (!require_recording_queue_policy(pipeline, cases[i].label)) {
      failed = 1;
    }
    if (!require_direct_video_path(pipeline, cases[i].label)) {
      failed = 1;
    }
    /* The existing RTSP, bitrate, GOP and framerate checks apply to all trees.
     * require_profile consumes the pipeline reference. */
    if (require_profile(pipeline, cases[i].rtx_time, cases[i].label) != 0) return 1;
    printf("%s profile: PASS\n", cases[i].label);
  }
  if (element_counts[0] != element_counts[1] ||
      element_counts[0] != element_counts[2] + 2 ||
      element_counts[3] != element_counts[8] + 1 ||
      element_counts[4] != element_counts[9] + 1 ||
      element_counts[4] != element_counts[5] ||
      element_counts[3] != element_counts[6] ||
      element_counts[6] != element_counts[7] ||
      element_counts[10] != element_counts[2] + 2) {
    fprintf(stderr, "factory element deltas: ingress removes 2; RTSP audio removes 1\n");
    failed = 1;
  }
  if (!require_encoder_comparison_policy()) failed = 1;
  return failed;
}
