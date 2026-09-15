#ifndef IMAGEPAD_RTSP_H264_CONTRACT_H
#define IMAGEPAD_RTSP_H264_CONTRACT_H

/* Mandatory PC/VRChat output contract, not a quality/preset preference.
 * zerolatency enables sliced threading. Disable it and all size-based slice
 * splitting explicitly, including in fallback paths. See
 * docs/RTSP_H264_COMPATIBILITY_CONTRACT.md before changing these options. */
#define IMAGEPAD_RTSP_X264_SINGLE_SLICE_OPTIONS \
  "sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0"

#endif
