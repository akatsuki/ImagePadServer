#include <gst/gst.h>
#include <gst/video/video.h>
#include "video_view.h"

static void check(GstVideoInfo *i, int ow, int oh, AirPlayVideoViewMode m, int x,int y,int w,int h, int dx, int dy, int dw, int dh) {
  AirPlayVideoLayout l;
  g_assert_true(airplay_video_view_layout(i,NULL,ow,oh,m,&l));
  g_assert_cmpint(l.src.x,==,x); g_assert_cmpint(l.src.y,==,y);
  g_assert_cmpint(l.src.width,==,w); g_assert_cmpint(l.src.height,==,h);
  g_assert_cmpint(l.dst.x,==,dx); g_assert_cmpint(l.dst.y,==,dy); g_assert_cmpint(l.dst.width,==,dw); g_assert_cmpint(l.dst.height,==,dh);
}
int main(int argc, char **argv) {
  gst_init(&argc,&argv); GstVideoInfo i; gst_video_info_set_format(&i,GST_VIDEO_FORMAT_I420,1080,1920); i.par_n=i.par_d=1;
  check(&i,1920,1080,AIRPLAY_VIDEO_VIEW_CONTAIN,0,0,1080,1920,656,0,608,1080);
  AirPlayVideoLayout l; g_assert_true(airplay_video_view_layout(&i,NULL,1920,1080,AIRPLAY_VIDEO_VIEW_COVER,&l)); g_assert_cmpint(l.src.y,==,656); g_assert_cmpint(l.src.height,==,608);
  gst_video_info_set_format(&i,GST_VIDEO_FORMAT_I420,2560,1080); check(&i,1920,1080,AIRPLAY_VIDEO_VIEW_COVER,320,0,1920,1080,0,0,1920,1080);
  gst_video_info_set_format(&i,GST_VIDEO_FORMAT_I420,2048,1536); check(&i,1920,1080,AIRPLAY_VIDEO_VIEW_COVER,0,192,2048,1152,0,0,1920,1080);
  gst_video_info_set_format(&i,GST_VIDEO_FORMAT_I420,720,576); i.par_n=16;i.par_d=15; check(&i,1920,1080,AIRPLAY_VIDEO_VIEW_COVER,0,72,720,432,0,0,1920,1080);
  gst_video_info_set_format(&i,GST_VIDEO_FORMAT_I420,1920,1080); i.par_n=i.par_d=1;
  check(&i,1280,720,AIRPLAY_VIDEO_VIEW_CONTAIN,0,0,1920,1080,0,0,1280,720);
  check(&i,1280,720,AIRPLAY_VIDEO_VIEW_COVER,0,0,1920,1080,0,0,1280,720);
  g_assert_false(airplay_video_view_layout(NULL,NULL,1,1,0,&l));
  i.par_n=0; g_assert_false(airplay_video_view_layout(&i,NULL,1920,1080,0,&l));
  i.par_n=i.par_d=1;
  AirPlayVideoRect bad={-1,0,100,100}; g_assert_false(airplay_video_view_layout(&i,&bad,1920,1080,0,&l));
  bad.x=1000; bad.y=1900; bad.width=200; bad.height=100; g_assert_false(airplay_video_view_layout(&i,&bad,1920,1080,1,&l));
  gst_video_info_set_format(&i,GST_VIDEO_FORMAT_I420,1080,1920); i.par_n=i.par_d=1;
  AirPlayVideoRect odd={1,1,1077,1917}; g_assert_true(airplay_video_view_layout(&i,&odd,1920,1080,1,&l));
  g_assert_cmpint(l.src.x,>=,0); g_assert_cmpint(l.src.y,>=,0);
  g_assert_cmpint(l.src.x % 2,==,0); g_assert_cmpint(l.src.y % 2,==,0);
  g_assert_cmpint(l.src.width % 2,==,0); g_assert_cmpint(l.src.height % 2,==,0);
  return 0;
}
