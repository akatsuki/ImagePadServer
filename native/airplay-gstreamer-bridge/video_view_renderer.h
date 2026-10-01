#ifndef IMAGEPAD_VIDEO_VIEW_RENDERER_H
#define IMAGEPAD_VIDEO_VIEW_RENDERER_H
#include <gst/video/video.h>
#include "video_view.h"
typedef struct AirPlayVideoViewRenderer AirPlayVideoViewRenderer;
AirPlayVideoViewRenderer *airplay_video_view_renderer_new(const GstVideoInfo *output_info);
GstBuffer *airplay_video_view_render(AirPlayVideoViewRenderer *, GstSample *, AirPlayVideoViewMode, guint64, GError **);
void airplay_video_view_renderer_free(AirPlayVideoViewRenderer *);
GstBuffer *airplay_video_view_renderer_last(AirPlayVideoViewRenderer *);
GstSample *airplay_video_view_renderer_last_sample(AirPlayVideoViewRenderer *);
#endif
