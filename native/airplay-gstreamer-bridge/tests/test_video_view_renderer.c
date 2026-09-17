#include <gst/gst.h>
#include <gst/video/video.h>
#include <string.h>
#include "video_view_renderer.h"

int main(int argc, char **argv) {
  GstVideoInfo o, i;
  GstBuffer *input, *output;
  GstCaps *caps;
  GstSample *sample;
  GstBuffer *last;
  GstVideoFrame frame;
  AirPlayVideoViewRenderer *renderer;
  GError *error = NULL;
  gst_init(&argc, &argv);
  gst_video_info_set_format(&o, GST_VIDEO_FORMAT_I420, 6, 4);
  gst_video_info_set_format(&i, GST_VIDEO_FORMAT_I420, 2, 4);
  input = gst_buffer_new_allocate(NULL, GST_VIDEO_INFO_SIZE(&i), NULL);
  g_assert_true(gst_video_frame_map(&frame, &i, input, GST_MAP_WRITE));
  for (guint y = 0; y < 4; ++y) {
    memset((guint8 *)GST_VIDEO_FRAME_PLANE_DATA(&frame, 0) + y * GST_VIDEO_FRAME_PLANE_STRIDE(&frame, 0), 80, 2);
  }
  memset(GST_VIDEO_FRAME_PLANE_DATA(&frame, 1), 128, 2);
  memset(GST_VIDEO_FRAME_PLANE_DATA(&frame, 2), 128, 2);
  gst_video_frame_unmap(&frame);
  caps = gst_video_info_to_caps(&i);
  sample = gst_sample_new(input, caps, NULL, NULL);
  renderer = airplay_video_view_renderer_new(&o);
  g_assert_nonnull(renderer);
  output = airplay_video_view_render(renderer, sample, AIRPLAY_VIDEO_VIEW_CONTAIN, 1, &error);
  g_assert_no_error(error);
  g_assert_nonnull(output);
  g_assert_true(gst_video_frame_map(&frame, &o, output, GST_MAP_READ));
  g_assert_cmpint(((guint8 *)GST_VIDEO_FRAME_PLANE_DATA(&frame, 0))[0], ==, 16);
  g_assert_cmpint(((guint8 *)GST_VIDEO_FRAME_PLANE_DATA(&frame, 0))[1], ==, 16);
  g_assert_cmpint(((guint8 *)GST_VIDEO_FRAME_PLANE_DATA(&frame, 0))[2], ==, 80);
  g_assert_cmpint(((guint8 *)GST_VIDEO_FRAME_PLANE_DATA(&frame, 0))[3], ==, 80);
  g_assert_cmpint(((guint8 *)GST_VIDEO_FRAME_PLANE_DATA(&frame, 0))[4], ==, 16);
  g_assert_cmpint(((guint8 *)GST_VIDEO_FRAME_PLANE_DATA(&frame, 0))[5], ==, 16);
  gst_video_frame_unmap(&frame);
  gst_buffer_unref(output);
  last = airplay_video_view_renderer_last(renderer);
  g_assert_nonnull(last);
  gst_buffer_unref(last);
  GstSample *last_sample = airplay_video_view_renderer_last_sample(renderer);
  g_assert_nonnull(last_sample);
  gst_sample_unref(last_sample);
  gst_sample_unref(sample);
  gst_buffer_unref(input);
  gst_caps_unref(caps);
  airplay_video_view_renderer_free(renderer);
  return 0;
}
