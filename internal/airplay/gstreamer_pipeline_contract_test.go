package airplay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectGStreamerPipelineOwnsRTPRecoveryContract(t *testing.T) {
	sourcePath := filepath.Join("..", "..", "native", "airplay-gstreamer-bridge", "direct_pipeline.c")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read direct pipeline source: %v", err)
	}
	text := string(source)

	for _, required := range []string{
		`rtspclientsink name=rtsp_sink location=\"%s\" protocols=tcp latency=2000`,
		"udpsrc name=video_src buffer-size=67108864",
		"udpsrc name=audio_src buffer-size=67108864",
		"rtpjitterbuffer name=video_jitter mode=none latency=200 drop-on-latency=false do-lost=false",
		"rtpjitterbuffer name=audio_jitter mode=none latency=200 drop-on-latency=false do-lost=false",
		"rtph264depay name=video_depay request-keyframe=false wait-for-keyframe=false",
		"nvh264dec name=video_decoder",
		"nvh264enc name=video_encoder",
		"audiorate name=audio_rate skip-to-first=true tolerance=5000000 ! audioresample",
		"queue name=video_encode_queue max-size-buffers=30 max-size-bytes=0 max-size-time=500000000 leaky=downstream !",
		"video_tee. ! queue ! clocksync name=video_clock sync=true sync-to-first=true ! h264parse config-interval=1 ! rtsp_sink.sink_0",
		"audio_tee. ! queue ! clocksync name=audio_clock sync=true sync-to-first=true ! aacparse ! audio/mpeg,mpegversion=(int)4,stream-format=(string)raw,channels=(int)2,rate=(int)48000 ! rtsp_sink.sink_1",
		"configure_audio_rtsp_payloader",
		"gst_element_factory_make(\"rtpmp4gpay\",",
		"appsrc name=video_source",
		"appsrc name=audio_source",
		"appsink name=video_sink",
		"appsink name=audio_sink",
		"repeat_last_video",
		"reclock_pts",
		"The input PTS is deliberately not consulted here",
		"black frame",
		"silence",
		"started_monotonic_us",
		"g_get_monotonic_time() - ctx->started_monotonic_us",
		"while ((sample = gst_app_sink_try_pull_sample(GST_APP_SINK(ctx->video_sink), 0)) != NULL)",
		"while ((sample = gst_app_sink_try_pull_sample(GST_APP_SINK(ctx->audio_sink), 0)) != NULL)",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("direct pipeline is missing recovery contract %q", required)
		}
	}
}

// Structural wiring guard supplements the native callback/finalizer tests;
// it is not evidence of a real GStreamer shutdown or receiver session.
func TestSourceClockFinalWatermarkFollowsQuiescentCleanup(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "native", "airplay-gstreamer-bridge", "source_clock_pipeline.c"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, `g_printerr("AirPlay source-clock cleanup begin\n");`)
	if start < 0 {
		t.Fatal("missing cleanup boundary")
	}
	cleanup := text[start:]
	position := 0
	for _, step := range []string{
		"g_thread_join(context.video_accept_thread);",
		"context.video_accept_thread = NULL;",
		"g_thread_join(context.audio_accept_thread);",
		"context.audio_accept_thread = NULL;",
		"source_clock_close_listeners(&context);",
		"stop_video_scheduler(&context.video_scheduler);",
		"gst_pad_remove_probe(context.video_encoded_src_pad, context.video_encoded_probe_id);",
		"context.video_encoded_probe_id = 0;",
		"source_clock_recording_finalize(",
		"source_clock_recording_close_pipeline(context.pipeline);",
		"source_clock_finalize_video_watermark(&context, recording_finalize.pipeline_closed);",
		"source_clock_pipeline_release_audio_bin(",
	} {
		next := strings.Index(cleanup[position:], step)
		if next < 0 {
			t.Fatalf("missing or out-of-order cleanup step %q", step)
		}
		position += next + len(step)
	}
	if strings.Count(cleanup, "source_clock_finalize_video_watermark(&context,") != 1 {
		t.Fatal("cleanup must have exactly one final watermark call")
	}
}

func TestSourceClockCleanupOwnsRecordingFinalizeContract(t *testing.T) {
	sourcePath := filepath.Join("..", "..", "native", "airplay-gstreamer-bridge", "source_clock_pipeline.c")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source-clock pipeline source: %v", err)
	}
	text := string(source)
	busWatchRemoval := strings.Index(text, "if (context.bus_watch_id != 0) g_source_remove(context.bus_watch_id);")
	finalizeCall := strings.Index(text, "source_clock_recording_finalize(")
	audioCleanup := strings.LastIndex(text, "source_clock_pipeline_release_audio_bin(")
	if busWatchRemoval < 0 || finalizeCall < 0 || finalizeCall <= busWatchRemoval {
		t.Fatalf("recording finalize must run after the asynchronous bus watch is removed")
	}
	if audioCleanup < 0 || audioCleanup <= finalizeCall {
		t.Fatalf("dynamic audio cleanup must remain after pipeline recording finalization")
	}
	for _, required := range []string{
		`#include "source_clock_recording_finalize.h"`,
		`#include "source_clock_stop_request.h"`,
		"SourceClockRecordingFinalizeResult recording_finalize",
		"source_clock_recording_finalize(",
		"recording, 8 * GST_SECOND)",
		"source_clock_recording_close_pipeline(context.pipeline)",
		"pipeline->no_signal = source_clock_stop_request_is_no_signal(pipeline->stop_file)",
		"pipeline->exit_code = pipeline->no_signal ? 20 : 0",
		"recording_finalize.eos_complete",
		"recording_finalize.pipeline_closed",
		"recording_finalize.event_written",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("source-clock cleanup is missing finalize contract %q", required)
		}
	}
	for _, forbidden := range []string{
		"g_usleep(500 * 1000)",
		"gst_element_set_state(context.pipeline, GST_STATE_NULL);",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("source-clock cleanup retains forbidden finalize shortcut %q", forbidden)
		}
	}
}
