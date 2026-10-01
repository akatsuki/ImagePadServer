#ifndef IMAGEPAD_AIRPLAY_VIDEO_VIEW_H
#define IMAGEPAD_AIRPLAY_VIDEO_VIEW_H

#include <gst/video/video.h>

typedef enum {
  AIRPLAY_VIDEO_VIEW_CONTAIN = 0,
  AIRPLAY_VIDEO_VIEW_COVER = 1
} AirPlayVideoViewMode;

typedef struct { int x, y, width, height; } AirPlayVideoRect;
typedef struct { AirPlayVideoRect src, dst; } AirPlayVideoLayout;

gboolean airplay_video_view_layout(const GstVideoInfo *input,
    const AirPlayVideoRect *visible, int output_width, int output_height,
    AirPlayVideoViewMode mode, AirPlayVideoLayout *layout);

#endif
