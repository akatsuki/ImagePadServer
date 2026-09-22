#include "video_view_renderer.h"
#include <glib.h>
#include <string.h>
struct AirPlayVideoViewRenderer {
  GstVideoInfo output;
  GstBuffer *last;
  GstSample *last_sample;
  guint64 revision;
  AirPlayVideoViewMode mode;
  GMutex mutex;
};
AirPlayVideoViewRenderer *airplay_video_view_renderer_new(const GstVideoInfo *o) {
  if (!o || GST_VIDEO_INFO_FORMAT(o)!=GST_VIDEO_FORMAT_I420 || GST_VIDEO_INFO_WIDTH(o)<2 || GST_VIDEO_INFO_HEIGHT(o)<2) return NULL;
  AirPlayVideoViewRenderer *r=g_new0(AirPlayVideoViewRenderer,1);
  r->output=*o; r->revision=G_MAXUINT64; g_mutex_init(&r->mutex); return r;
}
GstBuffer *airplay_video_view_render(AirPlayVideoViewRenderer *r,GstSample *s,AirPlayVideoViewMode mode,guint64 rev,GError **e) {
  if (!r || !s) { g_set_error(e,GST_CORE_ERROR,GST_CORE_ERROR_FAILED,"video view requires renderer and sample"); return NULL; }
  g_mutex_lock(&r->mutex);
  GstCaps *caps=gst_sample_get_caps(s); GstVideoInfo in; GstVideoFrame sf,df; AirPlayVideoLayout l;
  if (!caps || !gst_video_info_from_caps(&in,caps) || GST_VIDEO_INFO_FORMAT(&in)!=GST_VIDEO_FORMAT_I420 ||
      !airplay_video_view_layout(&in,NULL,GST_VIDEO_INFO_WIDTH(&r->output),GST_VIDEO_INFO_HEIGHT(&r->output),mode,&l)) {
    g_set_error(e,GST_CORE_ERROR,GST_CORE_ERROR_NEGOTIATION,"unsupported video view sample"); g_mutex_unlock(&r->mutex); return NULL;
  }
  /* This converter only changes the spatial layout.  The source-clock
   * scheduler owns cadence and the buffer PTS is copied below, but
   * GstVideoConverter requires matching frame rates in its format infos. */
  in.fps_n = r->output.fps_n;
  in.fps_d = r->output.fps_d;
  GstStructure *cfg=gst_structure_new("video-view",
    GST_VIDEO_CONVERTER_OPT_SRC_X,G_TYPE_INT,l.src.x,GST_VIDEO_CONVERTER_OPT_SRC_Y,G_TYPE_INT,l.src.y,
    GST_VIDEO_CONVERTER_OPT_SRC_WIDTH,G_TYPE_INT,l.src.width,GST_VIDEO_CONVERTER_OPT_SRC_HEIGHT,G_TYPE_INT,l.src.height,
    GST_VIDEO_CONVERTER_OPT_DEST_X,G_TYPE_INT,l.dst.x,GST_VIDEO_CONVERTER_OPT_DEST_Y,G_TYPE_INT,l.dst.y,
    GST_VIDEO_CONVERTER_OPT_DEST_WIDTH,G_TYPE_INT,l.dst.width,GST_VIDEO_CONVERTER_OPT_DEST_HEIGHT,G_TYPE_INT,l.dst.height,
    GST_VIDEO_CONVERTER_OPT_FILL_BORDER,G_TYPE_BOOLEAN,TRUE,GST_VIDEO_CONVERTER_OPT_BORDER_ARGB,G_TYPE_UINT,0xff000000u,
    GST_VIDEO_CONVERTER_OPT_ASYNC_TASKS,G_TYPE_BOOLEAN,FALSE,NULL);
  GstVideoConverter *c=gst_video_converter_new(&in,&r->output,cfg); GstBuffer *out=NULL;
  if (c && gst_video_frame_map(&sf,&in,gst_sample_get_buffer(s),GST_MAP_READ)) {
    out=gst_buffer_new_allocate(NULL,GST_VIDEO_INFO_SIZE(&r->output),NULL);
    if (!out || !gst_video_frame_map(&df,&r->output,out,GST_MAP_WRITE)) { if(out) gst_buffer_unref(out); out=NULL; }
    else {
      GstBuffer *in_buffer = gst_sample_get_buffer(s);
      /* The converter API takes source first and destination second. */
      gst_video_converter_frame(c, &sf, &df);
      gst_video_frame_unmap(&df);
      /* Copy timing/flags explicitly; do not carry input crop metadata into
       * the fixed output canvas. */
      GST_BUFFER_PTS(out) = GST_BUFFER_PTS(in_buffer);
      GST_BUFFER_DTS(out) = GST_BUFFER_DTS(in_buffer);
      GST_BUFFER_DURATION(out) = GST_BUFFER_DURATION(in_buffer);
      GST_BUFFER_OFFSET(out) = GST_BUFFER_OFFSET(in_buffer);
      GST_BUFFER_OFFSET_END(out) = GST_BUFFER_OFFSET_END(in_buffer);
      GST_BUFFER_FLAGS(out) = GST_BUFFER_FLAGS(in_buffer);
    }
    gst_video_frame_unmap(&sf);
  }
  if(c) gst_video_converter_free(c);
  if(!out) { g_set_error(e,GST_CORE_ERROR,GST_CORE_ERROR_FAILED,"video view conversion failed"); g_mutex_unlock(&r->mutex); return NULL; }
  r->revision=rev; r->mode=mode; gst_clear_buffer(&r->last); r->last=gst_buffer_ref(out);
  if (r->last_sample != NULL) gst_sample_unref(r->last_sample);
  r->last_sample = gst_sample_ref(s);
  g_mutex_unlock(&r->mutex); return out;
}
void airplay_video_view_renderer_free(AirPlayVideoViewRenderer *r){ if(r){if(r->last_sample)gst_sample_unref(r->last_sample);gst_clear_buffer(&r->last);g_mutex_clear(&r->mutex);g_free(r);} }
GstBuffer *airplay_video_view_renderer_last(AirPlayVideoViewRenderer *r){GstBuffer *b=NULL;if(!r)return NULL;g_mutex_lock(&r->mutex);if(r->last)b=gst_buffer_ref(r->last);g_mutex_unlock(&r->mutex);return b;}
GstSample *airplay_video_view_renderer_last_sample(AirPlayVideoViewRenderer *r){GstSample *s=NULL;if(!r)return NULL;g_mutex_lock(&r->mutex);if(r->last_sample)s=gst_sample_ref(r->last_sample);g_mutex_unlock(&r->mutex);return s;}
