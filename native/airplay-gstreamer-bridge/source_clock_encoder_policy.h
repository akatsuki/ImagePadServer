#ifndef IMAGEPAD_SOURCE_CLOCK_ENCODER_POLICY_H
#define IMAGEPAD_SOURCE_CLOCK_ENCODER_POLICY_H

#include <glib.h>
#include <gst/gst.h>

/* The policy is deliberately disabled unless the comparison CLI options are
 * supplied.  This keeps the established source-clock pipeline byte-for-byte
 * equivalent for normal sessions. */
void source_clock_encoder_policy_reset(void);
gboolean source_clock_encoder_policy_configure(const char *tune,
                                               const char *report_path,
                                               GError **error);
gboolean source_clock_encoder_policy_enabled(void);
gboolean source_clock_encoder_policy_apply(GstElement *pipeline, GError **error);

#endif
