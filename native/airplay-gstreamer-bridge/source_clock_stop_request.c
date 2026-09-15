#include "source_clock_stop_request.h"

gboolean source_clock_stop_request_is_no_signal(const char *path) {
  gchar *contents = NULL;
  gboolean no_signal = FALSE;
  if (path == NULL || *path == '\0' ||
      !g_file_get_contents(path, &contents, NULL, NULL)) {
    return FALSE;
  }
  g_strstrip(contents);
  no_signal = g_strcmp0(contents, "no-signal") == 0;
  g_free(contents);
  return no_signal;
}
