#include <glib.h>
#include "video_view_control.h"

static void write_control(const gchar *path, const gchar *extra) {
  gchar *text = g_strdup_printf(
      "[video-view]\nschema=1\nsession-id=s\npublisher-generation=2\n"
      "revision=3\nmode=cover\n%s", extra == NULL ? "" : extra);
  g_assert_true(g_file_set_contents(path, text, -1, NULL));
  g_free(text);
}

int main(void) {
  gchar *path = g_strdup("video-view-control-test.ini");
  AirPlayVideoViewControl control = {0};
  GError *error = NULL;
  write_control(path, NULL);
  g_assert_true(airplay_video_view_control_load(path, "s", "2", &control, &error));
  g_assert_no_error(error);
  g_assert_cmpint(control.mode, ==, AIRPLAY_VIDEO_VIEW_COVER);
  g_assert_cmpuint(control.publisher_generation, ==, 2);
  g_assert_cmpuint(control.revision, ==, 3);
  airplay_video_view_control_clear(&control);
  write_control(path, "unknown=x\n");
  g_assert_false(airplay_video_view_control_load(path, "s", "2", &control, &error));
  g_clear_error(&error);
  write_control(path, "mode=contain\n");
  g_assert_false(airplay_video_view_control_load(path, "s", "2", &control, &error));
  g_clear_error(&error);
  write_control(path, NULL);
  g_assert_false(airplay_video_view_control_load(path, "other", "2", &control, &error));
  g_clear_error(&error);
  g_remove(path);
  g_free(path);
  return 0;
}
