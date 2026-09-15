#include <gst/gst.h>
#include <gst/app/gstappsink.h>

#include <stdio.h>

typedef struct {
  const char *name;
  int present;
} RequiredFactory;

static RequiredFactory required[] = {
  /* Legacy direct mode remains qualified while source-clock is opt-in. */
  {"udpsrc", 0}, {"rtpjitterbuffer", 0}, {"rtph264depay", 0},
  {"rtpL16depay", 0}, {"videorate", 0}, {"appsink", 0},
  /* Source-clock mode has no RTP input; these are its actual factories. */
  {"appsrc", 0}, {"decodebin", 0}, {"avdec_h264", 0}, {"avdec_aac", 0},
  {"avdec_alac", 0}, {"h264parse", 0}, {"x264enc", 0}, {"videoconvert", 0},
  {"videoscale", 0}, {"queue", 0}, {"compositor", 0}, {"audiomixer", 0},
  {"audioconvert", 0}, {"audioresample", 0}, {"avenc_aac", 0},
  {"aacparse", 0}, {"rtspclientsink", 0}, {"rtpmp4gpay", 0},
  {"mp4mux", 0}, {"filesink", 0}, {"fakesink", 0}
};

typedef struct {
  gboolean buffer_received;
  gboolean eos_received;
} PipelineProof;

static gboolean probe_pipeline(const char *description, PipelineProof *proof) {
  GError *error = NULL;
  GstElement *pipeline = gst_parse_launch(description, &error);
  GstElement *sink = NULL;
  GstBus *bus = NULL;
  GstStateChangeReturn state;
  gboolean ok = FALSE;
  gboolean bus_error_received = FALSE;
  proof->buffer_received = FALSE;
  proof->eos_received = FALSE;
  if (pipeline == NULL) {
    if (error != NULL) {
      fprintf(stderr, "capability pipeline parse failed: %s\n", error->message);
    }
    g_clear_error(&error);
    return FALSE;
  }
  sink = gst_bin_get_by_name(GST_BIN(pipeline), "probe_sink");
  if (!GST_IS_APP_SINK(sink)) {
    fprintf(stderr, "capability pipeline has no appsink named probe_sink\n");
    goto done;
  }
  bus = gst_element_get_bus(pipeline);
  state = gst_element_set_state(pipeline, GST_STATE_PLAYING);
  if (state != GST_STATE_CHANGE_FAILURE) {
    state = gst_element_get_state(pipeline, NULL, NULL, 2 * GST_SECOND);
    if (state != GST_STATE_CHANGE_FAILURE) {
      GstSample *sample = gst_app_sink_try_pull_sample(
          GST_APP_SINK(sink), 2 * GST_SECOND);
      if (sample != NULL) {
        proof->buffer_received = TRUE;
        gst_sample_unref(sample);
      }
      const gint64 deadline = g_get_monotonic_time() + 3 * G_USEC_PER_SEC;
      while (!proof->eos_received && !bus_error_received) {
        const gint64 remaining = deadline - g_get_monotonic_time();
        if (remaining <= 0) {
          break;
        }
        GstMessage *message = gst_bus_timed_pop_filtered(
            bus, (GstClockTime)remaining * 1000,
            GST_MESSAGE_ERROR | GST_MESSAGE_EOS);
        if (message == NULL) {
          break;
        }
        if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_EOS) {
          proof->eos_received = TRUE;
        } else if (GST_MESSAGE_TYPE(message) == GST_MESSAGE_ERROR) {
          bus_error_received = TRUE;
          GError *bus_error = NULL;
          gchar *debug = NULL;
          gst_message_parse_error(message, &bus_error, &debug);
          fprintf(stderr, "capability pipeline bus error: %s\n",
                  bus_error != NULL ? bus_error->message : "unknown error");
          g_clear_error(&bus_error);
          g_free(debug);
        }
        gst_message_unref(message);
      }
      ok = proof->buffer_received && proof->eos_received;
    }
  }
done:
  gst_element_set_state(pipeline, GST_STATE_NULL);
  if (bus != NULL) {
    gst_object_unref(bus);
  }
  if (sink != NULL) {
    gst_object_unref(sink);
  }
  gst_object_unref(pipeline);
  g_clear_error(&error);
  return ok;
}

int main(int argc, char **argv) {
  (void)argc;
  (void)argv;
  gst_init(NULL, NULL);
  int missing = 0;
  PipelineProof video_proof;
  PipelineProof audio_proof;
  guint major, minor, micro, nano;
  gst_version(&major, &minor, &micro, &nano);
  fprintf(stdout, "{\"schema\":1,\"gstreamer\":true,\"version\":\"%u.%u.%u\",\"elements\":[",
          major, minor, micro);
  for (size_t i = 0; i < sizeof(required) / sizeof(required[0]); ++i) {
    required[i].present = gst_element_factory_find(required[i].name) != NULL;
    if (!required[i].present) {
      missing++;
    }
    if (i != 0) {
      fputc(',', stdout);
    }
    fprintf(stdout, "{\"name\":\"%s\",\"present\":%s}",
            required[i].name, required[i].present ? "true" : "false");
  }
  gboolean video_pipeline_ok = probe_pipeline(
      "videotestsrc num-buffers=1 is-live=true ! videoconvert ! "
      "video/x-raw,format=I420,width=640,height=360,framerate=60/1 ! "
      "x264enc tune=zerolatency bframes=0 ! h264parse ! "
      "appsink name=probe_sink sync=false max-buffers=1 drop=true wait-on-eos=false",
      &video_proof);
  gboolean audio_pipeline_ok = probe_pipeline(
      "audiotestsrc num-buffers=8 wave=silence ! audioconvert ! "
      "audioresample ! audio/x-raw,format=F32LE,rate=48000,channels=2 ! "
      "avenc_aac bitrate=160000 ! aacparse ! "
      "appsink name=probe_sink sync=false max-buffers=1 drop=true wait-on-eos=false",
      &audio_proof);
  if (!video_pipeline_ok) {
    fputs("source-clock video capability pipeline did not produce buffer and EOS\n", stderr);
    missing++;
  }
  if (!audio_pipeline_ok) {
    fputs("source-clock audio capability pipeline did not produce buffer and EOS\n", stderr);
    missing++;
  }
  fputs("]", stdout);
  fprintf(stdout, ",\"sourceClock\":{\"videoBuffer\":%s,\"videoEOS\":%s,\"audioBuffer\":%s,\"audioEOS\":%s},\"missing\":%d}\n",
          video_proof.buffer_received ? "true" : "false",
          video_proof.eos_received ? "true" : "false",
          audio_proof.buffer_received ? "true" : "false",
          audio_proof.eos_received ? "true" : "false", missing);
  if (missing != 0) {
    for (size_t i = 0; i < sizeof(required) / sizeof(required[0]); ++i) {
      if (!required[i].present) {
        fprintf(stderr, "missing GStreamer element: %s\n", required[i].name);
      }
    }
    return 1;
  }
  return 0;
}
