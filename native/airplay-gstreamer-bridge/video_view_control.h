#ifndef IMAGEPAD_VIDEO_VIEW_CONTROL_H
#define IMAGEPAD_VIDEO_VIEW_CONTROL_H
#include <glib.h>
#include "video_view.h"
typedef struct { gchar *session_id; guint64 publisher_generation, revision; AirPlayVideoViewMode mode; } AirPlayVideoViewControl;
void airplay_video_view_control_clear(AirPlayVideoViewControl *);
gboolean airplay_video_view_control_load(const gchar *, const gchar *, const gchar *, AirPlayVideoViewControl *, GError **);
gboolean airplay_video_view_state_write(const gchar *, const gchar *, const gchar *,
    guint64, AirPlayVideoViewMode, guint64, AirPlayVideoViewMode, guint64,
    const gchar *, GError **);
#endif
