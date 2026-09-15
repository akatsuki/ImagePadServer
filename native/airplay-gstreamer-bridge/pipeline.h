#ifndef IMAGEPAD_AIRPLAY_PIPELINE_H
#define IMAGEPAD_AIRPLAY_PIPELINE_H

#include <stdint.h>

int airplay_pipeline_run(int video_port, int audio_port);
int airplay_direct_pipeline_run(int video_port, int audio_port,
                                const char *publish_url, const char *recording,
                                const char *stop_file,
                                int width, int height, int fps,
                                int bitrate_kbps, int maxrate_kbps,
                                int buffer_size_kbps, int audio_bitrate_bps,
                                int gop);
int airplay_source_clock_pipeline_run(const char *publish_url, const char *recording,
                                      const char *stop_file, const char *session_token,
                                      const char *session_id, uint64_t publisher_generation,
                                      const char *ready_file, const char *media_ready_file,
                                      const char *event_log,
                                      int video_listen_port, int audio_listen_port,
                                      int width, int height,
                                      int source_fps, int output_fps,
                                      int bitrate_kbps, int maxrate_kbps,
                                      int buffer_size_kbps, int audio_bitrate_bps,
                                      int gop, int no_signal_seconds,
                                      uint32_t proof_source_generation, uint32_t proof_video_watermark);

#endif
