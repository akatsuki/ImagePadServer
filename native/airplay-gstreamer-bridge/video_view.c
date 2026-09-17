#include "video_view.h"
#include <stdint.h>
#include <math.h>

static int even_nearest(long double value, int limit) {
  if (!isfinite(value) || value < 2.0L) return 2;
  if (value > (long double)INT64_MAX - 2.0L) return limit & ~1;
  int64_t r = (int64_t)(value / 2.0L + 0.5L) * 2;
  if (r < 2) r = 2;
  if (r > limit) r = limit & ~1;
  return (int)r;
}

gboolean airplay_video_view_layout(const GstVideoInfo *input,
    const AirPlayVideoRect *visible, int ow, int oh,
    AirPlayVideoViewMode mode, AirPlayVideoLayout *out) {
  if (!input || !out || (mode != AIRPLAY_VIDEO_VIEW_CONTAIN && mode != AIRPLAY_VIDEO_VIEW_COVER) || ow <= 0 || oh <= 0) return FALSE;
  int iw = GST_VIDEO_INFO_WIDTH(input), ih = GST_VIDEO_INFO_HEIGHT(input);
  if (iw < 2 || ih < 2 || (input->par_n == 0 || input->par_d == 0)) return FALSE;
  AirPlayVideoRect src = {0, 0, iw, ih};
  if (visible) {
    if (visible->x < 0 || visible->y < 0 || visible->width < 2 || visible->height < 2 || visible->x > iw - visible->width || visible->y > ih - visible->height) return FALSE;
    src = *visible;
    src.x &= ~1; src.y &= ~1;
    src.width &= ~1; src.height &= ~1;
    if (src.width < 2 || src.height < 2 || src.x > iw - src.width || src.y > ih - src.height) return FALSE;
  }
  long double sw = (long double)src.width * input->par_n, sh = (long double)src.height * input->par_d;
  if (!(sw > 0.0L) || !(sh > 0.0L)) return FALSE;
  AirPlayVideoRect dst = {0, 0, ow, oh};
  if (mode == AIRPLAY_VIDEO_VIEW_CONTAIN) {
    if (sw * oh >= sh * ow) {
      dst.width = ow; dst.height = even_nearest((long double)ow * sh / sw, oh);
    } else {
      dst.height = oh; dst.width = even_nearest((long double)oh * sw / sh, ow);
    }
    if (dst.width < 2 || dst.height < 2) return FALSE;
    dst.x = (ow - dst.width) / 2; dst.y = (oh - dst.height) / 2;
  } else {
    if (sw * oh < sh * ow) {
      int h = even_nearest((long double)src.width * oh * input->par_n / ((long double)ow * input->par_d), src.height);
      src.y += (src.height - h) / 2; src.height = h;
    } else if (sw * oh > sh * ow) {
      int w = even_nearest((long double)src.height * ow * input->par_d / ((long double)oh * input->par_n), src.width);
      src.x += (src.width - w) / 2; src.width = w;
    }
    src.x &= ~1; src.y &= ~1;
    if (src.x < 0 || src.y < 0 || src.x > iw - src.width || src.y > ih - src.height) return FALSE;
  }
  out->src = src; out->dst = dst; return TRUE;
}
