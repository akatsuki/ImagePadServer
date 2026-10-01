#include "video_view_control.h"
#include <glib/gstdio.h>
#include <errno.h>
#include <string.h>
#ifdef G_OS_WIN32
#include <windows.h>
#endif

static const gchar *video_view_mode_text(AirPlayVideoViewMode mode) {
  return mode == AIRPLAY_VIDEO_VIEW_COVER ? "cover" : "contain";
}

static gboolean video_view_replace_file(const gchar *tmp, const gchar *path,
                                        GError **error) {
#ifdef G_OS_WIN32
  gunichar2 *wtmp = g_utf8_to_utf16(tmp, -1, NULL, NULL, error);
  gunichar2 *wpath;
  gboolean ok;
  if (wtmp == NULL) return FALSE;
  wpath = g_utf8_to_utf16(path, -1, NULL, NULL, error);
  if (wpath == NULL) { g_free(wtmp); return FALSE; }
  ok = MoveFileExW((LPCWSTR)wtmp, (LPCWSTR)wpath,
                   MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH);
  if (!ok) {
    g_set_error(error, G_FILE_ERROR, G_FILE_ERROR_FAILED,
                "replace video view state failed (Win32 error %lu)",
                (unsigned long)GetLastError());
  }
  g_free(wtmp); g_free(wpath);
  return ok;
#else
  if (g_rename(tmp, path) == 0) return TRUE;
  g_set_error(error, G_FILE_ERROR, g_file_error_from_errno(errno),
              "replace video view state failed");
  return FALSE;
#endif
}

static gboolean video_view_control_text_is_strict(const gchar *raw, gsize len) {
  static const gchar *allowed[] = {
      "schema", "session-id", "publisher-generation", "revision", "mode"};
  gboolean seen[5] = {FALSE, FALSE, FALSE, FALSE, FALSE};
  gboolean in_group = FALSE;
  gchar **lines;
  guint i;
  if (raw == NULL || len == 0 || memchr(raw, '\0', len) != NULL) return FALSE;
  lines = g_strsplit(raw, "\n", -1);
  for (i = 0; lines != NULL && lines[i] != NULL; ++i) {
    gchar *line = g_strstrip(lines[i]);
    gchar *equal;
    guint j;
    if (*line == '\0' || *line == '#' || *line == ';') continue;
    if (*line == '[') {
      if (!g_str_equal(line, "[video-view]")) {
        g_strfreev(lines);
        return FALSE;
      }
      if (in_group) {
        g_strfreev(lines);
        return FALSE;
      }
      in_group = TRUE;
      continue;
    }
    if (!in_group || (equal = strchr(line, '=')) == NULL) {
      g_strfreev(lines);
      return FALSE;
    }
    *equal = '\0';
    g_strstrip(line);
    for (j = 0; j < G_N_ELEMENTS(allowed); ++j) {
      if (g_str_equal(line, allowed[j])) break;
    }
    if (j == G_N_ELEMENTS(allowed) || seen[j]) {
      g_strfreev(lines);
      return FALSE;
    }
    seen[j] = TRUE;
  }
  g_strfreev(lines);
  return in_group && seen[0] && seen[1] && seen[2] && seen[3] && seen[4];
}

void airplay_video_view_control_clear(AirPlayVideoViewControl *c){if(c){g_free(c->session_id);memset(c,0,sizeof(*c));}}
gboolean airplay_video_view_control_load(const gchar *path,const gchar *session,const gchar *generation,AirPlayVideoViewControl *out,GError **e){
  if(!path||!session||!generation||!out){g_set_error(e,G_FILE_ERROR,G_FILE_ERROR_INVAL,"invalid control arguments");return FALSE;}
  gchar *raw=NULL; gsize len=0; if(!g_file_get_contents(path,&raw,&len,e)||len>4096){g_free(raw);g_set_error(e,G_FILE_ERROR,G_FILE_ERROR_INVAL,"control exceeds 4KiB");return FALSE;}
  if (!video_view_control_text_is_strict(raw, len)) {
    g_free(raw);
    g_set_error(e, G_KEY_FILE_ERROR, G_KEY_FILE_ERROR_INVALID_VALUE,
                "invalid video view control syntax");
    return FALSE;
  }
  GKeyFile *k=g_key_file_new(); GError *x=NULL; if(!g_key_file_load_from_data(k,raw,len,G_KEY_FILE_NONE,&x)){g_propagate_error(e,x);g_key_file_free(k);g_free(raw);return FALSE;}
  g_free(raw); gsize nk=0; gchar **keys=g_key_file_get_keys(k,"video-view",&nk,&x); gboolean keys_ok=keys&&nk==5; const gchar *allowed[]={"schema","session-id","publisher-generation","revision","mode"};
  for(gsize i=0;i<nk&&keys_ok;i++){gboolean found=FALSE;for(gsize j=0;j<5;j++)if(g_str_equal(keys[i],allowed[j]))found=TRUE;keys_ok=found;}
  gchar *sid=g_key_file_get_string(k,"video-view","session-id",&x); gchar *gen=g_key_file_get_string(k,"video-view","publisher-generation",&x); gchar *rev=g_key_file_get_string(k,"video-view","revision",&x); gchar *mode=g_key_file_get_string(k,"video-view","mode",&x); gint schema=g_key_file_get_integer(k,"video-view","schema",&x);
  guint64 g=0,r=0; gboolean ok=keys_ok&&schema==1&&sid&&gen&&rev&&mode&&g_str_equal(sid,session)&&g_str_equal(gen,generation)&&g_ascii_string_to_unsigned(gen,10,0,G_MAXUINT64,&g,NULL)&&g_ascii_string_to_unsigned(rev,10,0,G_MAXUINT64,&r,NULL)&&(g_str_equal(mode,"contain")||g_str_equal(mode,"cover"));
  g_strfreev(keys); if(!ok){g_set_error(e,G_KEY_FILE_ERROR,G_KEY_FILE_ERROR_INVALID_VALUE,"invalid video view control");g_free(sid);g_free(gen);g_free(rev);g_free(mode);g_key_file_free(k);return FALSE;}
  g_free(out->session_id); out->session_id=sid;out->publisher_generation=g;out->revision=r;out->mode=g_str_equal(mode,"cover")?AIRPLAY_VIDEO_VIEW_COVER:AIRPLAY_VIDEO_VIEW_CONTAIN; g_free(gen);g_free(rev);g_free(mode);g_key_file_free(k);return TRUE;
}

gboolean airplay_video_view_state_write(const gchar *path, const gchar *session,
    const gchar *generation, guint64 configured_revision,
    AirPlayVideoViewMode configured_mode, guint64 applied_revision,
    AirPlayVideoViewMode applied_mode, guint64 output_pts_ns,
    const gchar *phase, GError **error) {
  gchar *tmp;
  gchar *json;
  gboolean ok;
  if (path == NULL || session == NULL || generation == NULL || phase == NULL ||
      *session == '\0' || *generation == '\0' || *phase == '\0' ||
      strpbrk(session, "\"\\\r\n") != NULL ||
      strpbrk(generation, "\"\\\r\n") != NULL ||
      strpbrk(phase, "\"\\\r\n") != NULL) {
    g_set_error(error, G_FILE_ERROR, G_FILE_ERROR_INVAL,
                "invalid video view state arguments");
    return FALSE;
  }
  json = g_strdup_printf(
      "{\"schema\":1,\"sessionId\":\"%s\","
      "\"publisherGeneration\":\"%s\","
      "\"configuredRevision\":\"%" G_GUINT64_FORMAT "\","
      "\"configuredMode\":\"%s\","
      "\"appliedRevision\":\"%" G_GUINT64_FORMAT "\","
      "\"appliedMode\":\"%s\",\"phase\":\"%s\","
      "\"outputPtsNs\":\"%" G_GUINT64_FORMAT "\"}\n",
      session, generation, configured_revision, video_view_mode_text(configured_mode),
      applied_revision, video_view_mode_text(applied_mode), phase, output_pts_ns);
  tmp = g_strdup_printf("%s.native.tmp", path);
  if (json == NULL || tmp == NULL) {
    g_free(json); g_free(tmp);
    g_set_error(error, G_FILE_ERROR, G_FILE_ERROR_NOMEM,
                "video view state allocation failed");
    return FALSE;
  }
  ok = g_file_set_contents(tmp, json, -1, error);
  g_free(json);
  if (!ok) { g_free(tmp); return FALSE; }
  if (!video_view_replace_file(tmp, path, error)) {
    g_remove(tmp);
    g_free(tmp);
    return FALSE;
  }
  g_free(tmp);
  return TRUE;
}
