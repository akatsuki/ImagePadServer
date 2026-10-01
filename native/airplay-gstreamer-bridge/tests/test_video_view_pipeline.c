#include <gst/gst.h>
int main(int argc,char **argv){gst_init(&argc,&argv); GstElement *p=gst_parse_launch("videotestsrc num-buffers=1 ! video/x-raw,format=I420,width=4,height=4 ! fakesink",NULL); g_assert_nonnull(p); gst_element_set_state(p,GST_STATE_PLAYING); gst_element_get_state(p,NULL,NULL,2*GST_SECOND); gst_element_set_state(p,GST_STATE_NULL); gst_object_unref(p); return 0;}
