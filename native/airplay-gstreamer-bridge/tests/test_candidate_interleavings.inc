/* Bounded real-codec tests. The writer seam stalls a streaming/scheduler
 * thread; the test/control thread stops it without holding the event lock. */
static void test_candidate_write_interleave(SourceClockVideoProofEvent stage, gboolean stop) {
  CandidateFixture f;
  GstBuffer *material = candidate_make_real_idr();
  g_mutex_lock(&candidate_event_gate.mutex);
  candidate_event_gate.stage = stage;
  candidate_event_gate.entered = candidate_event_gate.release = FALSE;
  candidate_event_gate.fail_after_write = !stop;
  memset(candidate_event_gate.calls, 0, sizeof(candidate_event_gate.calls));
  g_mutex_unlock(&candidate_event_gate.mutex);
  candidate_fixture_start_with_codec(&f, TRUE);
  candidate_send_real_idr(&f, material);
  gst_buffer_unref(material);
  gint64 deadline = g_get_monotonic_time() + 5 * G_USEC_PER_SEC;
  g_mutex_lock(&candidate_event_gate.mutex);
  while (!candidate_event_gate.entered &&
         g_cond_wait_until(&candidate_event_gate.cond, &candidate_event_gate.mutex, deadline)) {}
  assert(candidate_event_gate.entered);
  g_mutex_unlock(&candidate_event_gate.mutex);
  /* The real append has completed, but it is not a committed stage yet. */
  g_mutex_lock(&f.p.mutex);
  assert(!source_clock_video_proof_complete(&f.p.candidate_proof));
  assert(f.p.candidate_proof.reserved_event == stage);
  g_mutex_unlock(&f.p.mutex);
  gchar *stop_path = g_build_filename(f.directory, "stop", NULL);
  if (stop) {
    assert(g_file_set_contents(stop_path, "no-signal", -1, NULL));
    f.p.stop_file = stop_path;
    assert(source_clock_stop_watch(&f.p) == G_SOURCE_REMOVE);
  }
  g_mutex_lock(&candidate_event_gate.mutex);
  candidate_event_gate.release = TRUE;
  g_cond_broadcast(&candidate_event_gate.cond);
  g_mutex_unlock(&candidate_event_gate.mutex);
  deadline = g_get_monotonic_time() + 3 * G_USEC_PER_SEC;
  gboolean finished = FALSE;
  while (g_get_monotonic_time() < deadline) {
    g_mutex_lock(&f.p.mutex);
    finished = !f.p.candidate_proof.active && f.p.stopping &&
        (stop ? f.p.exit_code == 20 : f.p.fatal_error);
    g_mutex_unlock(&f.p.mutex);
    if (finished) break;
    g_usleep(1000);
  }
  assert(finished);
  g_mutex_lock(&f.p.mutex);
  assert(!source_clock_video_proof_complete(&f.p.candidate_proof));
  assert(f.p.exit_code == (stop ? 20 : 22));
  assert(f.p.fatal_error == !stop);
  assert(f.p.candidate_proof.committed_event == stage - 1);
  g_mutex_unlock(&f.p.mutex);
  if (!stop && stage == SOURCE_CLOCK_PROOF_EVENT_DECODED)
    assert(!g_file_test(f.media, G_FILE_TEST_EXISTS));
  g_remove(stop_path);
  candidate_fixture_end(&f); /* joins every producer before inspecting calls */
  g_free(stop_path);
  g_mutex_lock(&candidate_event_gate.mutex);
  for (unsigned i = 1; i <= 3; ++i)
    assert(candidate_event_gate.calls[i] == (i <= (unsigned)stage ? 1u : 0u));
  candidate_event_gate.stage = SOURCE_CLOCK_PROOF_EVENT_NONE;
  g_mutex_unlock(&candidate_event_gate.mutex);
}

static GPtrArray *candidate_make_real_audio(void) {
  GError *error = NULL;
  GstElement *generator = gst_parse_launch(
      "audiotestsrc num-buffers=12 samplesperbuffer=1024 wave=sine freq=1000 ! "
      "audio/x-raw,rate=44100,channels=1 ! audioconvert ! avenc_aac ! aacparse ! "
      "audio/mpeg,mpegversion=4,stream-format=adts ! appsink name=material sync=false async=false", &error);
  assert(generator != NULL && error == NULL);
  GstAppSink *sink = GST_APP_SINK(gst_bin_get_by_name(GST_BIN(generator), "material"));
  GPtrArray *frames = g_ptr_array_new_with_free_func((GDestroyNotify)gst_buffer_unref);
  assert(gst_element_set_state(generator, GST_STATE_PLAYING) != GST_STATE_CHANGE_FAILURE);
  for (unsigned i = 0; i < 12; ++i) {
    GstSample *sample = gst_app_sink_try_pull_sample(sink, GST_SECOND);
    assert(sample != NULL);
    g_ptr_array_add(frames, gst_buffer_copy_deep(gst_sample_get_buffer(sample)));
    gst_sample_unref(sample);
  }
  gst_element_set_state(generator, GST_STATE_NULL);
  gst_object_unref(sink); gst_object_unref(generator);
  return frames;
}

static void test_candidate_real_late_audio(void) {
  CandidateFixture f;
  GstBuffer *video = candidate_make_real_idr();
  GPtrArray *audio = candidate_make_real_audio();
  candidate_fixture_start_with_codec(&f, TRUE);
  candidate_send_real_idr(&f, video);
  gst_buffer_unref(video);
  gint64 deadline = g_get_monotonic_time() + 5 * G_USEC_PER_SEC;
  gboolean complete = FALSE;
  while (g_get_monotonic_time() < deadline) {
    g_mutex_lock(&f.p.mutex);
    complete = source_clock_video_proof_complete(&f.p.candidate_proof);
    g_mutex_unlock(&f.p.mutex);
    if (complete) break;
    g_usleep(1000);
  }
  assert(complete);
  SourceClockFixedPcmAudioStats stats;
  source_clock_fixed_pcm_audio_get_stats(f.p.fixed_pcm_audio, &stats);
  assert(!stats.decoder_created && stats.accepted_frames == 0);
  assert(gst_app_sink_try_pull_sample(f.audio_sink, 0) == NULL);
  GstClockTime base_time = gst_element_get_base_time(f.p.pipeline);
  uint64_t source_origin, output_origin;
  g_mutex_lock(&f.p.mutex);
  source_origin = f.p.timeline.source_origin_ns;
  output_origin = f.p.timeline.output_origin_ns;
  g_mutex_unlock(&f.p.mutex);
  SourceClockConnectionContext connection = {0};
  connection.pipeline = &f.p;
  source_clock_video_watermark_connection_init(&connection.watermark, SOURCE_CLOCK_STREAM_AUDIO);
  SourceClockHeader start = {1,40,0,SOURCE_CLOCK_CONTROL_SESSION_START,0,0,7,0,0,0,0,0};
  assert(on_received_source_frame(&connection, &start, NULL, 0) == 0);
  for (unsigned i = 0; i < audio->len; ++i) {
    GstBuffer *buffer = g_ptr_array_index(audio, i);
    GstMapInfo map = GST_MAP_INFO_INIT;
    assert(gst_buffer_map(buffer, &map, GST_MAP_READ));
    SourceClockHeader h = {1,40,2,SOURCE_CLOCK_CODEC_AAC_LC_ADTS,0,(uint32_t)map.size,
        i + 1, 2000000000ULL + (uint64_t)i * 1024 * GST_SECOND / 44100,1024,44100,1,0};
    assert(on_received_source_frame(&connection, &h, map.data, map.size) == 0);
    gst_buffer_unmap(buffer, &map);
  }
  g_ptr_array_unref(audio);
  gboolean nonzero = FALSE;
  GstClockTime last_pts = GST_CLOCK_TIME_NONE;
  for (unsigned i = 0; i < 8; ++i) {
    GstSample *sample = gst_app_sink_try_pull_sample(f.audio_sink, 2 * GST_SECOND);
    assert(sample != NULL);
    GstCaps *expected = gst_caps_from_string(
        "audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved");
    assert(gst_sample_get_caps(sample) != NULL);
    assert(gst_caps_is_subset(gst_sample_get_caps(sample), expected));
    gst_caps_unref(expected);
    GstBuffer *buffer = gst_sample_get_buffer(sample);
    assert(GST_BUFFER_PTS_IS_VALID(buffer));
    assert(GST_BUFFER_PTS(buffer) >= output_origin + GST_SECOND);
    assert(GST_BUFFER_PTS(buffer) < output_origin + GST_SECOND + 300000000);
    if (last_pts != GST_CLOCK_TIME_NONE) assert(GST_BUFFER_PTS(buffer) > last_pts);
    last_pts = GST_BUFFER_PTS(buffer);
    GstMapInfo map = GST_MAP_INFO_INIT;
    assert(gst_buffer_map(buffer, &map, GST_MAP_READ));
    for (size_t b = 0; b < map.size; ++b) nonzero |= map.data[b] != 0;
    gst_buffer_unmap(buffer, &map);
    gst_sample_unref(sample);
  }
  assert(nonzero);
  source_clock_fixed_pcm_audio_get_stats(f.p.fixed_pcm_audio, &stats);
  assert(stats.decoder_created && stats.decoded_frames >= 8 && !stats.failed);
  assert(stats.rejected_frames == 0 && stats.output_flow_failures == 0);
  assert(gst_element_get_base_time(f.p.pipeline) == base_time);
  g_mutex_lock(&f.p.mutex);
  assert(f.p.timeline.source_origin_ns == source_origin);
  assert(f.p.timeline.output_origin_ns == output_origin);
  assert(source_clock_video_proof_complete(&f.p.candidate_proof) && !f.p.stopping);
  assert(f.p.candidate_proof.selected_sequence == 101);
  g_mutex_unlock(&f.p.mutex);
  candidate_fixture_end(&f);
  puts("real AAC after video proof: nonzero PCM, stable video epoch/base-time: PASS");
}
