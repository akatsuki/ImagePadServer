#include "source_clock_pipeline_internal.h"
#include "source_clock_encoder_policy.h"
/* Also exercise the legacy CPU fallback's private production factory. */
#include "../direct_pipeline.c"
#include <gst/app/gstappsink.h>
#include <stdio.h>

static unsigned count_slices(const guint8 *data, gsize size) {
  unsigned count = 0;
  gsize i;
  for (i = 0; i + 3 < size; ++i) {
    gsize header = 0;
    if (data[i] != 0 || data[i + 1] != 0) continue;
    if (data[i + 2] == 1) header = i + 3;
    else if (i + 4 < size && data[i + 2] == 0 && data[i + 3] == 1)
      header = i + 4;
    if (header != 0) {
      unsigned type = data[header] & 31;
      if (type == 1 || type == 5) ++count;
      i = header;
    }
  }
  return count;
}

/* Encode with the production factory's encoder, including its preset/tune.
 * Inspect real access units: property strings alone cannot prove x264's
 * effective slice count after tune and thread settings have been applied. */
static int require_single_slice(int width, int height, int fps, unsigned threads,
                                int direct, const char *tune) {
  GError *error = NULL;
  GstElement *factory = NULL, *encoder = NULL, *pipeline = NULL, *sink = NULL;
  GstElement *source = NULL, *capsfilter = NULL, *parser = NULL;
  unsigned frames = 0;
  int ok = 0;
  gchar *description;

  if (direct) {
    factory = create_direct_pipeline(0, 0,
        "rtsp://127.0.0.1:1/single-slice-test", "unused-single-slice.mp4",
        width, height, fps, 2500, 3000, 5000, 128000, fps / 2, FALSE, &error);
  } else {
    factory = source_clock_create_pipeline(
        "rtsp://127.0.0.1:1/single-slice-test", "unused-single-slice.mp4",
        width, height, 60, fps, 2500, 3000, 5000, 128000, fps / 2, &error);
  }
  if (factory == NULL || error != NULL) goto done;
  if (!direct && tune != NULL) {
    if (!source_clock_encoder_policy_configure(tune, NULL, &error) ||
        !source_clock_encoder_policy_apply(factory, &error)) goto done;
    source_clock_encoder_policy_reset();
  }
  encoder = gst_bin_get_by_name(GST_BIN(factory),
      direct ? "video_encoder" : "source_clock_video_encoder");
  if (encoder == NULL || !gst_bin_remove(GST_BIN(factory), encoder)) goto done;
  /* Exercise a multi-core encoder even on a small CI machine. */
  g_object_set(encoder, "threads", threads, NULL);
  description = g_strdup_printf(
      "videotestsrc name=source num-buffers=36 pattern=ball ! "
      "capsfilter name=input_caps caps=video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1 "
      "h264parse name=parser ! video/x-h264,stream-format=byte-stream,alignment=au ! "
      "appsink name=output sync=false", width, height, fps);
  pipeline = gst_parse_launch(description, &error);
  g_free(description);
  if (pipeline == NULL || error != NULL) goto done;
  capsfilter = gst_bin_get_by_name(GST_BIN(pipeline), "input_caps");
  parser = gst_bin_get_by_name(GST_BIN(pipeline), "parser");
  sink = gst_bin_get_by_name(GST_BIN(pipeline), "output");
  source = gst_bin_get_by_name(GST_BIN(pipeline), "source");
  if (capsfilter == NULL || parser == NULL || sink == NULL || source == NULL ||
      !gst_bin_add(GST_BIN(pipeline), encoder) ||
      !gst_element_link_many(capsfilter, encoder, parser, NULL)) goto done;
  if (gst_element_set_state(pipeline, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE)
    goto done;
  while (frames < 36) {
    GstSample *sample = gst_app_sink_try_pull_sample(GST_APP_SINK(sink), 5 * GST_SECOND);
    GstMapInfo map;
    unsigned slices;
    if (sample == NULL) goto done;
    if (!gst_buffer_map(gst_sample_get_buffer(sample), &map, GST_MAP_READ)) {
      gst_sample_unref(sample);
      goto done;
    }
    slices = count_slices(map.data, map.size);
    gst_buffer_unmap(gst_sample_get_buffer(sample), &map);
    gst_sample_unref(sample);
    if (slices != 1) {
      fprintf(stderr, "%s %dx%d fps=%d threads=%u frame %u has %u slices, want 1\n",
              direct ? "direct" : "source-clock", width, height, fps, threads, frames, slices);
      goto done;
    }
    ++frames;
  }
  ok = 1;
done:
  if (!ok) fprintf(stderr, "single-slice encode failed after %u frames: %s\n",
                   frames, error == NULL ? "output contract mismatch" : error->message);
  if (pipeline != NULL) gst_element_set_state(pipeline, GST_STATE_NULL);
  if (source != NULL) gst_object_unref(source);
  if (capsfilter != NULL) gst_object_unref(capsfilter);
  if (parser != NULL) gst_object_unref(parser);
  if (sink != NULL) gst_object_unref(sink);
  if (encoder != NULL) gst_object_unref(encoder);
  if (pipeline != NULL) gst_object_unref(pipeline);
  if (factory != NULL) gst_object_unref(factory);
  g_clear_error(&error);
  if (ok) printf("%s %dx%d fps=%d threads=%u: %u access units, one slice each\n",
      direct ? "direct" : "source-clock", width, height, fps, threads, frames);
  return ok;
}

int main(int argc, char **argv) {
  gst_init(&argc, &argv);
  return require_single_slice(640, 360, 30, 1, 0, NULL) &&
         require_single_slice(1280, 720, 30, 4, 0, NULL) &&
         require_single_slice(720, 1280, 30, 8, 0, NULL) &&
         require_single_slice(1920, 1080, 60, 0, 0, NULL) &&
         require_single_slice(1080, 1920, 30, 8, 0, "zerolatency") &&
         require_single_slice(1280, 720, 30, 4, 1, NULL) &&
         require_single_slice(720, 1280, 30, 8, 1, NULL) ? 0 : 1;
}
