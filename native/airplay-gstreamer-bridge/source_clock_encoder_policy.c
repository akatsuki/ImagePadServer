#include "source_clock_encoder_policy.h"

#include <stdio.h>
#include <string.h>

typedef struct {
  gboolean enabled;
  gchar *tune;
  gchar *report_path;
  gchar *report_json;
} EncoderPolicy;

static EncoderPolicy policy;

void source_clock_encoder_policy_reset(void) {
  g_clear_pointer(&policy.tune, g_free);
  g_clear_pointer(&policy.report_path, g_free);
  g_clear_pointer(&policy.report_json, g_free);
  policy.enabled = FALSE;
}

gboolean source_clock_encoder_policy_configure(const char *tune,
                                               const char *report_path,
                                               GError **error) {
  source_clock_encoder_policy_reset();
  if ((tune == NULL || tune[0] == '\0') &&
      (report_path == NULL || report_path[0] == '\0')) return TRUE;
  if (tune != NULL && tune[0] != '\0') policy.tune = g_strdup(tune);
  if (report_path != NULL && report_path[0] != '\0')
    policy.report_path = g_strdup(report_path);
  if ((tune != NULL && tune[0] != '\0' && policy.tune == NULL) ||
      (report_path != NULL && report_path[0] != '\0' && policy.report_path == NULL)) {
    g_set_error(error, G_FILE_ERROR, G_FILE_ERROR_NOMEM,
                "source-clock encoder policy allocation failed");
    source_clock_encoder_policy_reset();
    return FALSE;
  }
  policy.enabled = TRUE;
  return TRUE;
}

gboolean source_clock_encoder_policy_enabled(void) { return policy.enabled; }

static gchar *property_text(GObject *object, const char *name) {
  GParamSpec *spec;
  GValue value = G_VALUE_INIT;
  gchar *text;
  if (object == NULL || name == NULL ||
      (spec = g_object_class_find_property(G_OBJECT_GET_CLASS(object), name)) == NULL)
    return g_strdup("");
  g_value_init(&value, G_PARAM_SPEC_VALUE_TYPE(spec));
  g_object_get_property(object, name, &value);
  text = g_strdup_value_contents(&value);
  g_value_unset(&value);
  return text == NULL ? g_strdup("") : text;
}

static gboolean set_enum_text(GObject *object, const char *name, const char *text) {
  GParamSpec *spec = g_object_class_find_property(G_OBJECT_GET_CLASS(object), name);
  GValue value = G_VALUE_INIT;
  GEnumClass *klass;
  GFlagsClass *flags;
  guint i;
  if (spec == NULL || text == NULL) return FALSE;
  if (G_TYPE_IS_FLAGS(G_PARAM_SPEC_VALUE_TYPE(spec))) {
    flags = G_FLAGS_CLASS(g_type_class_ref(G_PARAM_SPEC_VALUE_TYPE(spec)));
    for (i = 0; i < flags->n_values; ++i) {
      const GFlagsValue *entry = &flags->values[i];
      if (g_ascii_strcasecmp(entry->value_nick, text) == 0 ||
          g_ascii_strcasecmp(entry->value_name, text) == 0) {
        g_value_init(&value, G_PARAM_SPEC_VALUE_TYPE(spec));
        g_value_set_flags(&value, entry->value);
        g_object_set_property(object, name, &value);
        g_value_unset(&value);
        g_type_class_unref(flags);
        return TRUE;
      }
    }
    g_type_class_unref(flags);
    return FALSE;
  }
  if (!G_TYPE_IS_ENUM(G_PARAM_SPEC_VALUE_TYPE(spec)))
    return FALSE;
  klass = G_ENUM_CLASS(g_type_class_ref(G_PARAM_SPEC_VALUE_TYPE(spec)));
  for (i = 0; i < klass->n_values; ++i) {
    const GEnumValue *entry = &klass->values[i];
    if (g_ascii_strcasecmp(entry->value_nick, text) == 0 ||
        g_ascii_strcasecmp(entry->value_name, text) == 0) {
      g_value_init(&value, G_PARAM_SPEC_VALUE_TYPE(spec));
      g_value_set_enum(&value, entry->value);
      g_object_set_property(object, name, &value);
      g_value_unset(&value);
      g_type_class_unref(klass);
      return TRUE;
    }
  }
  g_type_class_unref(klass);
  return FALSE;
}

static gchar *caps_framerate(GstElement *capsfilter) {
  GstCaps *caps = NULL;
  const GstStructure *structure;
  gint numerator = 0, denominator = 1;
  gchar *result;
  if (capsfilter == NULL) return g_strdup("");
  g_object_get(capsfilter, "caps", &caps, NULL);
  structure = caps == NULL ? NULL : gst_caps_get_structure(caps, 0);
  if (structure == NULL || !gst_structure_get_fraction(structure, "framerate",
                                                        &numerator, &denominator)) {
    if (caps != NULL) gst_caps_unref(caps);
    return g_strdup("");
  }
  result = g_strdup_printf("%d/%d", numerator, denominator);
  gst_caps_unref(caps);
  return result;
}

static gchar *json_quote(const char *value) {
  gchar *escaped = g_strescape(value == NULL ? "" : value, NULL);
  gchar *quoted = g_strdup_printf("\"%s\"", escaped == NULL ? "" : escaped);
  g_free(escaped);
  return quoted;
}

static void on_new_payloader(GstElement *sink, GstElement *payloader, gpointer unused) {
  const gchar *marker, *end;
  gchar *factory, *prefix, *suffix, *updated;
  (void)sink; (void)unused;
  if (policy.report_path == NULL || policy.report_json == NULL || payloader == NULL) return;
  factory = g_strdup(gst_plugin_feature_get_name(
      GST_PLUGIN_FEATURE(gst_element_get_factory(payloader))));
  marker = strstr(policy.report_json, "\"payloader\":");
  if (marker == NULL) { g_free(factory); return; }
  marker += strlen("\"payloader\":");
  marker = strchr(marker, '"');
  end = marker == NULL ? NULL : strchr(marker + 1, '"');
  if (marker == NULL || end == NULL) { g_free(factory); return; }
  prefix = g_strndup(policy.report_json, marker - policy.report_json);
  suffix = g_strdup(end + 1);
  updated = g_strdup_printf("%s\"%s\"%s", prefix, factory, suffix);
  g_free(policy.report_json); policy.report_json = updated;
  (void)g_file_set_contents(policy.report_path, policy.report_json, -1, NULL);
  g_free(suffix); g_free(prefix); g_free(factory);
}

gboolean source_clock_encoder_policy_apply(GstElement *pipeline, GError **error) {
  GstElement *encoder, *capsfilter, *sink, *payloader = NULL;
  gchar *preset, *tune, *vbv, *gop, *bframes, *fps, *protocol, *payload_name;
  gchar *json;
  gchar *jpreset, *jtune, *jvbv, *jgop, *jbframes, *jfps, *jprotocol, *jpayload;
  const gchar *sink_name = "pending-new-payloader";
  if (!policy.enabled) return TRUE;
  if (pipeline == NULL) {
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_FAILED,
                "source-clock encoder policy requires a pipeline");
    return FALSE;
  }
  encoder = gst_bin_get_by_name(GST_BIN(pipeline), "source_clock_video_encoder");
  capsfilter = gst_bin_get_by_name(GST_BIN(pipeline), "video_output_caps");
  sink = gst_bin_get_by_name(GST_BIN(pipeline), "rtsp_sink");
  if (encoder == NULL || capsfilter == NULL || sink == NULL) {
    if (encoder != NULL) gst_object_unref(encoder);
    if (capsfilter != NULL) gst_object_unref(capsfilter);
    if (sink != NULL) gst_object_unref(sink);
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_MISSING_PLUGIN,
                "source-clock encoder policy elements are unavailable");
    return FALSE;
  }
  if (policy.tune != NULL && !set_enum_text(G_OBJECT(encoder), "tune", policy.tune)) {
    g_set_error(error, GST_CORE_ERROR, GST_CORE_ERROR_FAILED,
                "unsupported x264 tune '%s'", policy.tune);
    gst_object_unref(sink); gst_object_unref(capsfilter); gst_object_unref(encoder);
    return FALSE;
  }
  preset = property_text(G_OBJECT(encoder), "speed-preset");
  tune = property_text(G_OBJECT(encoder), "tune");
  vbv = property_text(G_OBJECT(encoder), "option-string");
  gop = property_text(G_OBJECT(encoder), "key-int-max");
  bframes = property_text(G_OBJECT(encoder), "bframes");
  fps = caps_framerate(capsfilter);
  protocol = property_text(G_OBJECT(sink), "protocols");
  {
    GstPad *audio_pad = gst_element_get_static_pad(sink, "sink_1");
    if (audio_pad != NULL) {
      g_object_get(audio_pad, "payloader", &payloader, NULL);
      gst_object_unref(audio_pad);
    }
  }
  if (payloader != NULL) sink_name = GST_ELEMENT_NAME(payloader);
  else g_signal_connect(sink, "new-payloader", G_CALLBACK(on_new_payloader), NULL);
  payload_name = g_strdup(sink_name);
  jpreset = json_quote(preset); jtune = json_quote(tune); jvbv = json_quote(vbv);
  jgop = json_quote(gop); jbframes = json_quote(bframes); jfps = json_quote(fps);
  jprotocol = json_quote(protocol); jpayload = json_quote(payload_name);
  json = g_strdup_printf(
      "{\"preset\":%s,\"tune\":%s,\"vbv\":%s,\"gop\":%s,"
      "\"b_frames\":%s,\"fps\":%s,\"payloader\":%s,\"protocol\":%s}\n",
      jpreset, jtune, jvbv, jgop, jbframes, jfps, jpayload, jprotocol);
  g_free(policy.report_json);
  policy.report_json = g_strdup(json);
  if (policy.report_path == NULL) {
    g_print("source-clock encoder comparison: %s", json);
  } else if (!g_file_set_contents(policy.report_path, json, -1, error)) {
    g_free(json); g_free(jpreset); g_free(jtune); g_free(jvbv); g_free(jgop);
    g_free(jbframes); g_free(jfps); g_free(jprotocol); g_free(jpayload);
    g_free(payload_name); g_free(fps); g_free(protocol);
    g_free(bframes); g_free(gop); g_free(vbv); g_free(tune); g_free(preset);
    gst_object_unref(sink); gst_object_unref(capsfilter); gst_object_unref(encoder);
    return FALSE;
  }
  g_free(json); g_free(jpreset); g_free(jtune); g_free(jvbv); g_free(jgop);
  g_free(jbframes); g_free(jfps); g_free(jprotocol); g_free(jpayload);
  g_free(payload_name); g_free(fps); g_free(protocol);
  g_free(bframes); g_free(gop); g_free(vbv); g_free(tune); g_free(preset);
  gst_object_unref(sink); gst_object_unref(capsfilter); gst_object_unref(encoder);
  if (payloader != NULL) gst_object_unref(payloader);
  return TRUE;
}
