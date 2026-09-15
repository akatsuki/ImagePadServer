/* Actual appsrc/appsink transport and source-included production callbacks.
 * Hand-authored AU fixtures here test admission, NOT codec compatibility. */
typedef struct {
  SourceClockPipeline p;
  SourceClockConnectionContext video;
  GstAppSink *compressed_sink, *output_sink, *audio_sink;
  gchar *directory, *ready, *media, *events;
} CandidateFixture;

/* Test-only I/O seam. Real event serialization and filesystem writes still run;
 * pause AFTER append to exercise the ambiguous-write/stop boundary. */
static struct {
  GMutex mutex;
  GCond cond;
  SourceClockVideoProofEvent stage;
  gboolean entered, release, fail_after_write;
  unsigned calls[4];
} candidate_event_gate;

static bool test_candidate_event_write(SourceClockEventWriter *writer,
    SourceClockVideoProofEvent stage, uint32_t generation, uint32_t sequence,
    uint64_t running_time_ns) {
  bool written = source_clock_event_write_video_proof(writer, stage, generation,
                                                      sequence, running_time_ns);
  g_mutex_lock(&candidate_event_gate.mutex);
  ++candidate_event_gate.calls[stage];
  gboolean fail = FALSE;
  if (candidate_event_gate.stage == stage) {
    candidate_event_gate.entered = TRUE;
    g_cond_broadcast(&candidate_event_gate.cond);
    while (!candidate_event_gate.release)
      g_cond_wait(&candidate_event_gate.cond, &candidate_event_gate.mutex);
    fail = candidate_event_gate.fail_after_write;
  }
  g_mutex_unlock(&candidate_event_gate.mutex);
  return written && !fail;
}

static GstPadProbeReturn candidate_codec_probe(GstPad *pad, GstPadProbeInfo *info, gpointer data) {
  SourceClockPipeline *p = data;
  g_mutex_lock(&p->mutex);
  gboolean inspect = !source_clock_video_proof_complete(&p->candidate_proof);
  g_mutex_unlock(&p->mutex);
  GstBuffer *buffer = GST_PAD_PROBE_INFO_BUFFER(info);
  GstMapInfo map = GST_MAP_INFO_INIT;
  if (inspect && buffer != NULL && gst_buffer_map(buffer, &map, GST_MAP_READ)) {
    if (!source_clock_video_h264_is_idr(map.data, map.size)) {
      GstCaps *caps = gst_pad_get_current_caps(pad);
      gchar *description = caps == NULL ? g_strdup("no caps") : gst_caps_to_string(caps);
      g_printerr("candidate synthetic encoder caps: %s; first bytes:", description);
      for (size_t i = 0; i < MIN(map.size, 8); ++i) g_printerr(" %02x", map.data[i]);
      g_printerr("\n");
      g_free(description);
      if (caps != NULL) gst_caps_unref(caps);
    }
    gst_buffer_unmap(buffer, &map);
  }
  return on_encoded_video_buffer(pad, info, data);
}

static void candidate_fixture_start_with_codec(CandidateFixture *f, gboolean codec) {
  GError *error = NULL;
  memset(f, 0, sizeof(*f));
  f->directory = g_dir_make_tmp("imagepad-candidate-test-XXXXXX", &error);
  assert(f->directory != NULL && error == NULL);
  f->ready = g_build_filename(f->directory, "ready.json", NULL);
  f->media = g_build_filename(f->directory, "media.json", NULL);
  f->events = g_build_filename(f->directory, "events.jsonl", NULL);
  const char *description = codec ?
      "appsrc name=video_input is-live=true format=time caps=video/x-h264,stream-format=byte-stream,alignment=au "
      "! tee name=input_tee input_tee. ! queue ! appsink name=compressed sync=false async=false max-buffers=1 drop=true "
      "input_tee. ! queue ! decodebin force-sw-decoders=true ! videoconvert ! videoscale add-borders=true "
      "! video/x-raw,format=I420,width=64,height=64,pixel-aspect-ratio=1/1 "
      "! appsink name=decoded sync=false async=false max-buffers=3 drop=true "
      "appsrc name=video_scheduled_input is-live=true format=time "
      "caps=video/x-raw,format=I420,width=64,height=64,framerate=60/1,pixel-aspect-ratio=1/1 "
      "! queue ! videoconvert ! videorate drop-only=true ! video/x-raw,framerate=30/1 "
      "! x264enc name=source_clock_video_encoder bitrate=100 key-int-max=30 bframes=0 tune=zerolatency speed-preset=ultrafast byte-stream=true "
      "! h264parse config-interval=1 ! tee name=encoded_tee "
      "encoded_tee. ! queue ! appsink name=output sync=false async=false "
      "encoded_tee. ! queue ! h264parse ! video/x-h264,stream-format=avc,alignment=au ! fakesink sync=false async=false "
      "appsrc name=audio_fixed_pcm_input is-live=true format=time do-timestamp=false block=false emit-signals=false "
      "max-buffers=32 max-bytes=1048576 max-time=500000000 leaky-type=upstream "
      "caps=audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved "
      "! appsink name=audio_output sync=false async=false" :
      "appsrc name=video_input is-live=true format=time ! appsink name=compressed sync=false async=false "
      "appsrc name=video_scheduled_input is-live=true format=time ! appsink name=output sync=false async=false";
  f->p.pipeline = gst_parse_launch(description, &error);
  assert(f->p.pipeline != NULL && error == NULL);
  f->p.video_source = GST_APP_SRC(gst_bin_get_by_name(GST_BIN(f->p.pipeline), "video_input"));
  f->p.video_scheduler.output_source = GST_APP_SRC(gst_bin_get_by_name(GST_BIN(f->p.pipeline), "video_scheduled_input"));
  f->compressed_sink = GST_APP_SINK(gst_bin_get_by_name(GST_BIN(f->p.pipeline), "compressed"));
  f->output_sink = GST_APP_SINK(gst_bin_get_by_name(GST_BIN(f->p.pipeline), "output"));
  if (codec) {
    f->p.fixed_pcm_audio_enabled = TRUE;
    f->p.fixed_pcm_source = GST_APP_SRC(gst_bin_get_by_name(GST_BIN(f->p.pipeline), "audio_fixed_pcm_input"));
    f->audio_sink = GST_APP_SINK(gst_bin_get_by_name(GST_BIN(f->p.pipeline), "audio_output"));
  }
  f->p.loop = g_main_loop_new(NULL, FALSE);
  g_mutex_init(&f->p.mutex);
  g_mutex_init(&f->p.event_mutex);
  g_mutex_init(&f->p.video_scheduler.mutex);
  f->p.started_monotonic_us = g_get_monotonic_time();
  f->p.candidate_proof_enabled = TRUE;
  f->p.video_scheduler.owner = &f->p;
  source_clock_video_scheduler_init(&f->p.video_scheduler.policy, 16666666);
  source_clock_timeline_init(&f->p.timeline);
  source_clock_video_au_gate_init(&f->p.video_au_gate);
  assert(source_clock_video_proof_init(&f->p.candidate_proof, 7, 100));
  assert(source_clock_event_writer_init(&f->p.event_writer, "candidate", 2, f->ready, f->media, f->events));
  f->video.pipeline = &f->p;
  source_clock_video_watermark_connection_init(&f->video.watermark, SOURCE_CLOCK_STREAM_VIDEO);
  if (codec) assert(source_clock_candidate_configure_encoder(f->p.pipeline));
  assert(gst_element_set_state(f->p.pipeline, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);
  /* Startup caps preparation must never relink a running publisher. The
   * subsequent real decode/encode checks also detect damage to this graph. */
  if (codec) assert(!source_clock_candidate_configure_encoder(f->p.pipeline));
  SourceClockHeader start = {1,40,0,SOURCE_CLOCK_CONTROL_SESSION_START,0,0,7,0,0,0,0,0};
  assert(on_received_source_frame(&f->video, &start, NULL, 0) == 0);
  if (codec) {
    GstElement *encoder = gst_bin_get_by_name(GST_BIN(f->p.pipeline), "source_clock_video_encoder");
    f->p.video_encoded_src_pad = gst_element_get_static_pad(encoder, "src");
    gst_object_unref(encoder);
    f->p.video_encoded_probe_id = gst_pad_add_probe(f->p.video_encoded_src_pad,
        GST_PAD_PROBE_TYPE_BUFFER, candidate_codec_probe, &f->p, NULL);
    f->p.video_scheduler.decoded_sink = GST_APP_SINK(gst_bin_get_by_name(GST_BIN(f->p.pipeline), "decoded"));
    assert(start_video_scheduler(&f->p.video_scheduler, &f->p, f->p.pipeline,
        f->p.video_scheduler.decoded_sink, f->p.video_scheduler.output_source, 60));
  }
}

static void candidate_fixture_start(CandidateFixture *f) {
  candidate_fixture_start_with_codec(f, FALSE);
}

static void candidate_fixture_end(CandidateFixture *f) {
  g_mutex_lock(&f->p.mutex);
  f->p.stopping = TRUE;
  g_mutex_unlock(&f->p.mutex);
  stop_video_scheduler(&f->p.video_scheduler);
  if (f->p.fixed_pcm_audio != NULL) {
    assert(source_clock_fixed_pcm_audio_stop(f->p.fixed_pcm_audio) == SOURCE_CLOCK_FIXED_PCM_STOP_COMPLETE);
    assert(source_clock_fixed_pcm_audio_free(f->p.fixed_pcm_audio));
    f->p.fixed_pcm_audio = NULL;
  }
  gst_element_set_state(f->p.pipeline, GST_STATE_NULL);
  if (f->p.video_encoded_src_pad != NULL) {
    gst_pad_remove_probe(f->p.video_encoded_src_pad, f->p.video_encoded_probe_id);
    gst_object_unref(f->p.video_encoded_src_pad);
  }
  if (f->p.video_scheduler.decoded_sink != NULL) gst_object_unref(f->p.video_scheduler.decoded_sink);
  clear_pending_frame(&f->p.pending_video);
  clear_pending_frame(&f->p.candidate_pending);
  source_clock_video_bootstrap_clear(&f->p.video_bootstrap);
  if (f->p.candidate_decoded_buffer != NULL) gst_buffer_unref(f->p.candidate_decoded_buffer);
  clear_video_scheduler_runtime(&f->p.video_scheduler);
  gst_object_unref(f->p.video_source);
  gst_object_unref(f->p.video_scheduler.output_source);
  gst_object_unref(f->compressed_sink); gst_object_unref(f->output_sink);
  if (f->p.fixed_pcm_source != NULL) gst_object_unref(f->p.fixed_pcm_source);
  if (f->audio_sink != NULL) gst_object_unref(f->audio_sink);
  gst_object_unref(f->p.pipeline); g_main_loop_unref(f->p.loop);
  g_mutex_clear(&f->p.mutex); g_mutex_clear(&f->p.event_mutex);
  g_mutex_clear(&f->p.video_scheduler.mutex);
  g_remove(f->ready); g_remove(f->media); g_remove(f->events); g_rmdir(f->directory);
  g_free(f->ready); g_free(f->media); g_free(f->events); g_free(f->directory);
}

static const uint8_t candidate_idr[] = {
  0,0,1,0x67,0x42,0,0x1e,0x80,0,0,1,0x68,0xc0,0,0,1,0x65,0xb8
};
static void candidate_receive_idr(CandidateFixture *f, uint32_t sequence) {
  SourceClockHeader h = {1,40,1,SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      SOURCE_CLOCK_FLAG_KEYFRAME,sizeof(candidate_idr),sequence,1000000000,0,0,0,0};
  assert(on_received_source_frame(&f->video, &h, candidate_idr, sizeof(candidate_idr)) == 0);
}

static void test_candidate_no_audio_one_idr_proof(void) {
  CandidateFixture f;
  candidate_fixture_start(&f);
  candidate_receive_idr(&f, 101);
  /* Saving to pending must NOT claim that appsrc accepted the IDR. */
  assert(!g_file_test(f.events, G_FILE_TEST_EXISTS));
  assert(f.p.candidate_pending.present && !f.p.pending_video.present);
  assert(f.p.candidate_proof.selected_sequence == 101);
  assert(source_clock_candidate_tick(&f.p, f.p.candidate_pending.arrival_ns));
  assert(gst_app_sink_try_pull_sample(f.compressed_sink, 0) == NULL);
  uint64_t tick = f.p.candidate_pending.arrival_ns + SOURCE_CLOCK_WARMUP_NS;
  assert(source_clock_candidate_tick(&f.p, tick));
  GstSample *input = gst_app_sink_try_pull_sample(f.compressed_sink, GST_SECOND);
  assert(input != NULL && gst_buffer_get_size(gst_sample_get_buffer(input)) == sizeof(candidate_idr));
  gst_sample_unref(input);
  assert(!f.p.candidate_pending.present && f.p.candidate_proof.input_done);
  candidate_receive_idr(&f, 102); /* same PTS, different sequence cannot get through */
  assert(source_clock_candidate_tick(&f.p, tick + 16666666));
  assert(gst_app_sink_try_pull_sample(f.compressed_sink, 0) == NULL);
  assert(gst_app_sink_try_pull_sample(f.output_sink, 0) == NULL);
  assert(!source_clock_video_proof_complete(&f.p.candidate_proof));
  GstBuffer *raw = gst_buffer_new_allocate(NULL, 24, NULL);
  gst_buffer_memset(raw, 0, 0xa5, 24);
  GST_BUFFER_PTS(raw) = tick;
  assert(source_clock_candidate_decoded(&f.p, raw));
  gst_buffer_unref(raw);
  assert(source_clock_candidate_tick(&f.p, tick + 33333333));
  GstSample *output = gst_app_sink_try_pull_sample(f.output_sink, GST_SECOND);
  assert(output != NULL && gst_buffer_get_size(gst_sample_get_buffer(output)) == 24);
  uint8_t copied[24];
  assert(gst_buffer_extract(gst_sample_get_buffer(output), 0, copied, sizeof(copied)) == sizeof(copied));
  for (size_t i = 0; i < sizeof(copied); ++i) assert(copied[i] == 0xa5);
  gst_sample_unref(output);
  assert(source_clock_candidate_tick(&f.p, tick + 50000000));
  assert(gst_app_sink_try_pull_sample(f.output_sink, 0) == NULL); /* no HOLD before encoded proof */
  GstBuffer *encoded = gst_buffer_new_allocate(NULL, sizeof(candidate_idr), NULL);
  gst_buffer_fill(encoded, 0, candidate_idr, sizeof(candidate_idr));
  GST_BUFFER_PTS(encoded) = tick;
  GstPadProbeInfo info = {0};
  info.type = GST_PAD_PROBE_TYPE_BUFFER;
  info.data = encoded;
  assert(on_encoded_video_buffer(NULL, &info, &f.p) == GST_PAD_PROBE_OK);
  gst_buffer_unref(encoded);
  assert(source_clock_video_proof_complete(&f.p.candidate_proof));
  assert(f.p.video_au_gate.waiting_for_idr); /* skipped P dependencies cannot resume */
  gchar *events = NULL;
  assert(g_file_get_contents(f.events, &events, NULL, NULL));
  assert(strstr(events, "\"video-input-idr\"") != NULL);
  assert(strstr(events, "\"video-decoded\"") != NULL);
  assert(strstr(events, "\"video-encoded-idr\"") != NULL);
  const gchar *cursor = events;
  unsigned count = 0;
  while ((cursor = strstr(cursor, "\"sourceSessionGeneration\":7,\"sourceVideoSequence\":101")) != NULL) { ++count; ++cursor; }
  assert(count == 3);
  g_free(events);
  candidate_fixture_end(&f);
}

static void test_candidate_terminal_and_audio_independence(void) {
  CandidateFixture f;
  candidate_fixture_start(&f);
  SourceClockConnectionContext audio = {0};
  audio.pipeline = &f.p;
  source_clock_video_watermark_connection_init(&audio.watermark, SOURCE_CLOCK_STREAM_AUDIO);
  SourceClockHeader end = {1,40,0,SOURCE_CLOCK_CONTROL_SESSION_END,0,0,7,0,0,0,0,0};
  assert(on_received_source_frame(&audio, &end, NULL, 0) == 0);
  assert(!f.p.stopping && f.p.candidate_proof.active);
  candidate_receive_idr(&f, 101);
  f.p.stopping = TRUE;
  f.p.no_signal = TRUE;
  f.p.exit_code = 20;
  GstBuffer *raw = gst_buffer_new_allocate(NULL, 24, NULL);
  GST_BUFFER_PTS(raw) = 0;
  assert(source_clock_candidate_decoded(&f.p, raw));
  gst_buffer_unref(raw);
  assert(f.p.exit_code == 20 && !f.p.fatal_error);
  assert(!g_file_test(f.events, G_FILE_TEST_EXISTS));
  candidate_fixture_end(&f);

  candidate_fixture_start(&f);
  SourceClockHeader next = {1,40,0,SOURCE_CLOCK_CONTROL_SESSION_START,0,0,8,0,0,0,0,0};
  assert(on_received_source_frame(&f.video, &next, NULL, 0) == 0);
  assert(f.p.stopping && !f.p.candidate_proof.active);
  assert(f.p.exit_code == 22);
  candidate_fixture_end(&f);
}

static void test_candidate_idr_must_be_in_payload(void) {
  CandidateFixture f;
  candidate_fixture_start(&f);
  const uint8_t delta[] = {0,0,1,0x41,0xe0};
  SourceClockHeader h = {1,40,1,SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      SOURCE_CLOCK_FLAG_KEYFRAME,sizeof(delta),101,1000000000,0,0,0,0};
  assert(on_received_source_frame(&f.video, &h, delta, sizeof(delta)) == 0);
  assert(f.p.candidate_proof.selected_sequence == 0);
  assert(!g_file_test(f.events, G_FILE_TEST_EXISTS));
  /* A GstBuffer without DELTA_UNIT is not sufficient encoded IDR evidence. */
  GstBuffer *buffer = gst_buffer_new_allocate(NULL, sizeof(delta), NULL);
  gst_buffer_fill(buffer, 0, delta, sizeof(delta));
  GST_BUFFER_PTS(buffer) = 0;
  assert(source_clock_candidate_encoded(&f.p, buffer));
  gst_buffer_unref(buffer);
  assert(!source_clock_video_proof_complete(&f.p.candidate_proof));
  assert(f.p.stopping && f.p.exit_code == 22);
  candidate_fixture_end(&f);
}

static void test_candidate_respects_presentation_deadline(void) {
  CandidateFixture f;
  candidate_fixture_start(&f);
  candidate_receive_idr(&f, 101);
  uint64_t tick = f.p.candidate_pending.arrival_ns + SOURCE_CLOCK_WARMUP_NS;
  assert(source_clock_candidate_tick(&f.p, tick));
  GstBuffer *raw = gst_buffer_new_allocate(NULL, 24, NULL);
  GST_BUFFER_PTS(raw) = tick + 100000000;
  assert(source_clock_candidate_decoded(&f.p, raw));
  gst_buffer_unref(raw);
  assert(source_clock_candidate_tick(&f.p, tick + 99999999));
  assert(!f.p.candidate_proof.output_reserved);
  assert(gst_app_sink_try_pull_sample(f.output_sink, 0) == NULL);
  assert(source_clock_candidate_tick(&f.p, tick + 100000000));
  GstSample *output = gst_app_sink_try_pull_sample(f.output_sink, GST_SECOND);
  assert(output != NULL);
  assert(GST_BUFFER_PTS(gst_sample_get_buffer(output)) == tick + 100000000);
  gst_sample_unref(output);
  candidate_fixture_end(&f);
}

static void test_candidate_split_config_same_sequence_and_late_audio_start(void) {
  CandidateFixture f;
  candidate_fixture_start(&f);
  const uint8_t config[] = {0,0,1,0x67,0x42,0,0x1e,0x80,0,0,1,0x68,0xc0};
  const uint8_t idr[] = {0,0,1,0x65,0xb8};
  SourceClockHeader h = {1,40,1,SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      SOURCE_CLOCK_FLAG_CONFIG,sizeof(config),101,1000000000,0,0,0,0};
  assert(on_received_source_frame(&f.video, &h, config, sizeof(config)) == 0);
  assert(!f.p.stopping && f.p.candidate_proof.selected_sequence == 0);
  assert(!g_file_test(f.events, G_FILE_TEST_EXISTS));
  h.flags = SOURCE_CLOCK_FLAG_KEYFRAME;
  h.payload_bytes = sizeof(idr);
  assert(on_received_source_frame(&f.video, &h, idr, sizeof(idr)) == 0);
  assert(!f.p.stopping && f.p.candidate_proof.selected_sequence == 101);
  assert(f.p.candidate_pending.present);
  SourceClockConnectionContext audio = {0};
  audio.pipeline = &f.p;
  source_clock_video_watermark_connection_init(&audio.watermark, SOURCE_CLOCK_STREAM_AUDIO);
  SourceClockHeader start = {1,40,0,SOURCE_CLOCK_CONTROL_SESSION_START,0,0,7,0,0,0,0,0};
  assert(on_received_source_frame(&audio, &start, NULL, 0) == 0);
  assert(f.p.candidate_pending.present && f.p.timeline.has_first_video);
  uint64_t tick = f.p.candidate_pending.arrival_ns + SOURCE_CLOCK_WARMUP_NS;
  assert(source_clock_candidate_tick(&f.p, tick));
  GstSample *input = gst_app_sink_try_pull_sample(f.compressed_sink, GST_SECOND);
  assert(input != NULL);
  uint8_t copied[sizeof(candidate_idr)];
  GstBuffer *buffer = gst_sample_get_buffer(input);
  assert(gst_buffer_get_size(buffer) == sizeof(candidate_idr));
  assert(gst_buffer_extract(buffer, 0, copied, sizeof(copied)) == sizeof(copied));
  assert(memcmp(copied, candidate_idr, sizeof(copied)) == 0);
  assert(f.p.candidate_proof.input_done && !f.p.stopping);
  gst_sample_unref(input);
  candidate_fixture_end(&f);
}

static GstBuffer *candidate_make_real_idr(void) {
  GError *error = NULL;
  GstElement *generator = gst_parse_launch(
      "videotestsrc num-buffers=1 pattern=red ! video/x-raw,format=I420,width=64,height=64,framerate=60/1 "
      "! x264enc tune=zerolatency speed-preset=ultrafast bframes=0 byte-stream=true "
      "! video/x-h264,stream-format=byte-stream,alignment=au ! appsink name=fixture sync=false", &error);
  assert(generator != NULL && error == NULL);
  GstAppSink *sink = GST_APP_SINK(gst_bin_get_by_name(GST_BIN(generator), "fixture"));
  assert(gst_element_set_state(generator, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);
  GstSample *sample = gst_app_sink_try_pull_sample(sink, 5 * GST_SECOND);
  assert(sample != NULL);
  GstMapInfo map = GST_MAP_INFO_INIT;
  assert(gst_buffer_map(gst_sample_get_buffer(sample), &map, GST_MAP_READ));
  assert(source_clock_video_h264_is_idr(map.data, map.size));
  GstBuffer *result = gst_buffer_copy_deep(gst_sample_get_buffer(sample));
  gst_buffer_unmap(gst_sample_get_buffer(sample), &map);
  gst_sample_unref(sample);
  gst_element_set_state(generator, GST_STATE_NULL);
  gst_object_unref(sink); gst_object_unref(generator);
  return result;
}

static void candidate_send_real_idr(CandidateFixture *f, GstBuffer *buffer) {
  GstMapInfo map = GST_MAP_INFO_INIT;
  assert(gst_buffer_map(buffer, &map, GST_MAP_READ));
  SourceClockHeader h = {1,40,1,SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,
      SOURCE_CLOCK_FLAG_KEYFRAME,(uint32_t)map.size,101,1000000000,0,0,0,0};
  assert(on_received_source_frame(&f->video, &h, map.data, map.size) == 0);
  gst_buffer_unmap(buffer, &map);
}

static void test_candidate_real_codec_single_idr(void) {
  CandidateFixture f;
  GError *error = NULL;
  GstBuffer *material = candidate_make_real_idr();
  candidate_fixture_start_with_codec(&f, TRUE);
  candidate_send_real_idr(&f, material);
  gst_buffer_unref(material);
  gint64 deadline = g_get_monotonic_time() + 5 * G_USEC_PER_SEC;
  gboolean complete = FALSE, failed = FALSE;
  while (g_get_monotonic_time() < deadline) {
    g_mutex_lock(&f.p.mutex);
    complete = source_clock_video_proof_complete(&f.p.candidate_proof);
    failed = f.p.fatal_error;
    g_mutex_unlock(&f.p.mutex);
    if (complete || failed) break;
    g_usleep(5000);
  }
  if (!complete) g_printerr("candidate codec incomplete: failed=%d input=%d decoded=%d output=%d encoded=%d\n",
      failed, f.p.candidate_proof.input_done, f.p.candidate_proof.decoded_seen,
      f.p.candidate_proof.output_done, f.p.candidate_proof.encoded_seen);
  assert(complete && !failed);
  GstSample *encoded = gst_app_sink_try_pull_sample(f.output_sink, GST_SECOND);
  assert(encoded != NULL);
  /* Decode the actual first encoded output: a BLACK/HOLD substitution must
   * fail even if somebody preserved the correct event identity on that frame. */
  GstElement *verify = gst_parse_launch(
      "appsrc name=in format=time caps=video/x-h264,stream-format=byte-stream,alignment=au "
      "! h264parse ! avdec_h264 max-threads=1 ! videoconvert ! video/x-raw,format=RGB "
      "! appsink name=rgb sync=false async=false", &error);
  assert(verify != NULL && error == NULL);
  GstAppSrc *verify_input = GST_APP_SRC(gst_bin_get_by_name(GST_BIN(verify), "in"));
  GstAppSink *verify_output = GST_APP_SINK(gst_bin_get_by_name(GST_BIN(verify), "rgb"));
  assert(gst_element_set_state(verify, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);
  gst_app_src_set_caps(verify_input, gst_sample_get_caps(encoded));
  assert(gst_app_src_push_buffer(verify_input, gst_buffer_ref(gst_sample_get_buffer(encoded))) == GST_FLOW_OK);
  assert(gst_app_src_end_of_stream(verify_input) == GST_FLOW_OK);
  GstSample *rgb = gst_app_sink_try_pull_sample(verify_output, GST_SECOND);
  assert(rgb != NULL);
  GstVideoInfo vi;
  GstVideoFrame vf;
  assert(gst_video_info_from_caps(&vi, gst_sample_get_caps(rgb)));
  assert(GST_VIDEO_INFO_WIDTH(&vi) == 64 && GST_VIDEO_INFO_HEIGHT(&vi) == 64);
  assert(gst_video_frame_map(&vf, &vi, gst_sample_get_buffer(rgb), GST_MAP_READ));
  uint8_t *center = (uint8_t *)GST_VIDEO_FRAME_PLANE_DATA(&vf, 0) +
      32 * GST_VIDEO_FRAME_PLANE_STRIDE(&vf, 0) + 32 * 3;
  assert(center[0] > 200 && center[1] < 30 && center[2] < 30);
  gst_video_frame_unmap(&vf);
  gst_sample_unref(rgb);
  gst_element_set_state(verify, GST_STATE_NULL);
  gst_object_unref(verify_input); gst_object_unref(verify_output); gst_object_unref(verify);
  gst_sample_unref(encoded);
  gchar *events = NULL;
  assert(g_file_get_contents(f.events, &events, NULL, NULL));
  const gchar *cursor = events;
  unsigned count = 0;
  while ((cursor = strstr(cursor, "\"sourceSessionGeneration\":7,\"sourceVideoSequence\":101")) != NULL) { ++count; ++cursor; }
  assert(count == 3);
  g_free(events);
  candidate_fixture_end(&f);
  puts("single IDR -> real decode -> real scheduler -> real x264 IDR: PASS");
}
