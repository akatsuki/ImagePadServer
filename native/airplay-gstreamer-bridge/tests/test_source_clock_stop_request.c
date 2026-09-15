#include "source_clock_stop_request.h"

#include <glib/gstdio.h>
#include <stdio.h>

#define REQUIRE(condition) do { \
  if (!(condition)) { \
    fprintf(stderr, "%s:%d: requirement failed: %s\n", __FILE__, __LINE__, #condition); \
    return 1; \
  } \
} while (0)

static gboolean write_request(const char *path, const char *value) {
  GError *error = NULL;
  gboolean written = g_file_set_contents(path, value, -1, &error);
  g_clear_error(&error);
  return written;
}

int main(void) {
  GError *error = NULL;
  gchar *directory = g_dir_make_tmp("imagepad-stop-request-XXXXXX", &error);
  gchar *path;
  REQUIRE(directory != NULL && error == NULL);
  path = g_build_filename(directory, "publisher.stop", NULL);

  REQUIRE(!source_clock_stop_request_is_no_signal(NULL));
  REQUIRE(!source_clock_stop_request_is_no_signal(""));
  REQUIRE(!source_clock_stop_request_is_no_signal(path));
  REQUIRE(write_request(path, ""));
  REQUIRE(!source_clock_stop_request_is_no_signal(path));
  REQUIRE(write_request(path, "stop\n"));
  REQUIRE(!source_clock_stop_request_is_no_signal(path));
  REQUIRE(write_request(path, " no-signal\r\n"));
  REQUIRE(source_clock_stop_request_is_no_signal(path));
  REQUIRE(write_request(path, "no-signal-extra\n"));
  REQUIRE(!source_clock_stop_request_is_no_signal(path));

  REQUIRE(g_remove(path) == 0);
  REQUIRE(g_rmdir(directory) == 0);
  g_free(path);
  g_free(directory);
  return 0;
}
