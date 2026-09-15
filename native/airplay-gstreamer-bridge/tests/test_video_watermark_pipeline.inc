/* Included by test_video_input.c to exercise the production callback and
 * shutdown helper. These tests do not start a decoder or network publisher. */
static gboolean watermark_fail_after_append;
static unsigned watermark_append_calls;
static bool test_watermark_event_write(SourceClockEventWriter *writer,
                                       uint64_t generation, uint64_t sequence) {
  bool written = source_clock_event_write_video_watermark_final(writer, generation, sequence);
  ++watermark_append_calls;
  return written && !watermark_fail_after_append;
}

static SourceClockPipeline *watermark_reentrant_context;
static void finalize_during_reservation(void) {
  assert(watermark_reentrant_context->video_watermark_final_state == VIDEO_WATERMARK_FINALIZING);
  assert(!source_clock_finalize_video_watermark(watermark_reentrant_context, TRUE));
}

static void test_watermark_authenticated_reader_boundary(void) {
  const uint8_t pframe[] = {0,0,1,0x41,0xe0};
  const uint8_t token[SOURCE_CLOCK_SESSION_TOKEN_BYTES] = {42};
  uint8_t wire[3 * SOURCE_CLOCK_HEADER_SIZE + sizeof(token) + sizeof(pframe)];
  SourceClockHeader hello = {1,40,0,SOURCE_CLOCK_CONTROL_HELLO,0,sizeof(token),0,0,0,0,0,0};
  SourceClockHeader start = {1,40,0,SOURCE_CLOCK_CONTROL_SESSION_START,0,0,7,0,0,0,0,0};
  SourceClockHeader frame = {1,40,1,SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,0,sizeof(pframe),11,100,0,0,0,0};
  SourceClockPipeline context = {0};
  SourceClockConnectionContext connection = {0};
  SourceClockReader reader;
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  source_clock_timeline_init(&context.timeline);
  context.started_monotonic_us = g_get_monotonic_time();
  connection.pipeline = &context;
  source_clock_video_watermark_connection_init(&connection.watermark, SOURCE_CLOCK_STREAM_VIDEO);
  source_clock_reader_init(&reader, SOURCE_CLOCK_STREAM_VIDEO, token, on_received_source_frame, &connection);
  assert(source_clock_header_encode(&hello, wire, sizeof(wire)) == SOURCE_CLOCK_OK);
  memcpy(wire + 40, token, sizeof(token));
  assert(source_clock_header_encode(&start, wire + 56, sizeof(wire) - 56) == SOURCE_CLOCK_OK);
  assert(source_clock_header_encode(&frame, wire + 96, sizeof(wire) - 96) == SOURCE_CLOCK_OK);
  memcpy(wire + 136, pframe, sizeof(pframe));
  /* Actual reassembly, including partial header and partial payload. */
  for (size_t i = 0; i + 1 < sizeof(wire); ++i) {
    assert(source_clock_reader_feed(&reader, wire + i, 1) == SOURCE_CLOCK_READER_OK);
    assert(!context.video_watermark.final.has);
  }
  assert(source_clock_reader_authenticated(&reader));
  assert(source_clock_reader_feed(&reader, wire + sizeof(wire) - 1, 1) == SOURCE_CLOCK_READER_OK);
  assert(context.video_watermark.final.source_video_sequence == 11);
  assert(!context.video_watermark_invalid);
  /* A proof regression disables evidence without making this callback fatal. */
  assert(source_clock_reader_feed(&reader, wire + 96, sizeof(wire) - 96) == SOURCE_CLOCK_READER_OK);
  assert(context.video_watermark_invalid);
  assert(!context.protocol_error && !context.fatal_error && !context.stopping);
  source_clock_reader_destroy(&reader);
  source_clock_video_watermark_init(&context.video_watermark);
  context.video_watermark_invalid = FALSE;
  source_clock_video_watermark_connection_init(&connection.watermark, SOURCE_CLOCK_STREAM_VIDEO);
  source_clock_reader_init(&reader, SOURCE_CLOCK_STREAM_VIDEO, token, on_received_source_frame, &connection);
  wire[40] ^= 1;
  assert(source_clock_reader_feed(&reader, wire, sizeof(wire)) == SOURCE_CLOCK_READER_BAD_TOKEN);
  assert(context.video_watermark.generation == 0 && !context.video_watermark.final.has);
  source_clock_reader_destroy(&reader);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.mutex);
}

static void test_watermark_pipeline(void) {
  const uint8_t pframe[] = {0,0,1,0x41,0xe0};
  SourceClockPipeline context = {0};
  SourceClockConnectionContext video = {0}, audio = {0};
  SourceClockHeader start = {1,40,SOURCE_CLOCK_STREAM_CONTROL,
      SOURCE_CLOCK_CONTROL_SESSION_START,0,0,7,0,0,0,0,0};
  SourceClockHeader frame = {1,40,SOURCE_CLOCK_STREAM_VIDEO,
      SOURCE_CLOCK_CODEC_H264_ANNEXB_AU,0,sizeof(pframe),11,100,0,0,0,0};
  gchar *directory = g_dir_make_tmp("imagepad-watermark-pipeline-XXXXXX", NULL);
  gchar *ready = g_build_filename(directory, "ready.json", NULL);
  gchar *media = g_build_filename(directory, "media.json", NULL);
  gchar *events = g_build_filename(directory, "events.jsonl", NULL);
  gchar *contents = NULL;
  gsize bytes;
  assert(directory != NULL);
  g_mutex_init(&context.mutex);
  g_mutex_init(&context.event_mutex);
  g_mutex_init(&context.video_scheduler.mutex);
  source_clock_timeline_init(&context.timeline);
  context.started_monotonic_us = g_get_monotonic_time();
  video.pipeline = audio.pipeline = &context;
  source_clock_video_watermark_connection_init(&video.watermark, SOURCE_CLOCK_STREAM_VIDEO);
  source_clock_video_watermark_connection_init(&audio.watermark, SOURCE_CLOCK_STREAM_AUDIO);
  assert(source_clock_event_writer_init(&context.event_writer, "session", 3, ready, media, events));
  assert(on_received_source_frame(&video, &start, NULL, 0) == 0);
  start.sequence = 8;
  assert(on_received_source_frame(&audio, &start, NULL, 0) == 0);
  assert(context.timeline.active_generation == 8);
  /* A valid P-frame dropped by the uninitialized AU gate is still received,
   * and the audio control cannot relabel it as source generation 8. */
  assert(on_received_source_frame(&video, &frame, pframe, sizeof(pframe)) == 0);
  assert(!context.pending_video.present);
  assert(context.video_watermark.final.has);
  assert(context.video_watermark.final.source_session_generation == 7);
  assert(context.video_watermark.final.source_video_sequence == 11);
  assert(!source_clock_finalize_video_watermark(&context, TRUE));
  assert(!g_file_test(events, G_FILE_TEST_EXISTS));
  context.stopping = TRUE;
  assert(!source_clock_finalize_video_watermark(&context, TRUE));
  context.video_scheduler.stopping = TRUE;
  assert(!source_clock_finalize_video_watermark(&context, FALSE));
  /* Non-NULL identities model join not yet completed; never dereferenced. */
  context.video_accept_thread = (GThread *)&context;
  assert(!source_clock_finalize_video_watermark(&context, TRUE));
  context.video_accept_thread = NULL;
  context.audio_accept_thread = (GThread *)&context;
  assert(!source_clock_finalize_video_watermark(&context, TRUE));
  context.audio_accept_thread = NULL;
  context.video_scheduler.thread = (GThread *)&context;
  assert(!source_clock_finalize_video_watermark(&context, TRUE));
  context.video_scheduler.thread = NULL;
  context.video_encoded_probe_id = 1;
  assert(!source_clock_finalize_video_watermark(&context, TRUE));
  context.video_encoded_probe_id = 0;
  /* An in-flight reader_feed can finish another AU after stop was requested. */
  frame.sequence = 12;
  assert(on_received_source_frame(&video, &frame, pframe, sizeof(pframe)) == 0);
  watermark_append_calls = 0;
  watermark_reentrant_context = &context;
  interleave_mutex = &context.mutex;
  after_mapping_unlock = finalize_during_reservation;
  assert(source_clock_finalize_video_watermark(&context, TRUE));
  assert(after_mapping_unlock == NULL);
  interleave_mutex = NULL;
  watermark_reentrant_context = NULL;
  assert(source_clock_finalize_video_watermark(&context, TRUE));
  assert(watermark_append_calls == 1);
  assert(g_file_get_contents(events, &contents, &bytes, NULL));
  assert(strstr(contents, "\"sourceSessionGeneration\":7,\"sourceVideoSequence\":12") != NULL);
  assert(strchr(contents, '\n') == contents + bytes - 1);
  g_free(contents);
  assert(!g_file_test(ready, G_FILE_TEST_EXISTS));
  assert(!g_file_test(media, G_FILE_TEST_EXISTS));
  g_remove(events);
  g_rmdir(directory);
  g_free(ready); g_free(media); g_free(events); g_free(directory);
  clear_pending_frame(&context.pending_video);
  source_clock_video_bootstrap_clear(&context.video_bootstrap);
  g_mutex_clear(&context.video_scheduler.mutex);
  g_mutex_clear(&context.event_mutex);
  g_mutex_clear(&context.mutex);
}

static void test_watermark_final_failure_and_no_media(void) {
  for (int variant = 0; variant < 7; ++variant) {
    SourceClockPipeline context = {0};
    gchar *directory = g_dir_make_tmp("imagepad-watermark-failure-XXXXXX", NULL);
    gchar *missing = g_build_filename(directory, "absent", "events.jsonl", NULL);
    gchar *good = g_build_filename(directory, "events.jsonl", NULL);
    assert(directory != NULL);
    g_mutex_init(&context.mutex);
    g_mutex_init(&context.event_mutex);
    g_mutex_init(&context.video_scheduler.mutex);
    context.stopping = TRUE;
    context.video_scheduler.stopping = TRUE;
    context.video_watermark.final = (SourceClockVideoWatermarkSnapshot){true,7,11};
    if (variant == 0) context.video_watermark.final.has = false;
    if (variant == 1) context.video_watermark_invalid = TRUE;
    if (variant == 2) context.protocol_error = TRUE;
    if (variant == 4) context.exit_code = 20;
    if (variant == 6) context.exit_code = 14;
    watermark_fail_after_append = variant == 5;
    watermark_append_calls = 0;
    assert(source_clock_event_writer_init(&context.event_writer, "session", 3,
                                         good, good, variant == 5 ? good : missing));
    assert(!source_clock_finalize_video_watermark(&context, TRUE));
    /* A later writable path cannot retry a failed/empty/invalid finalization. */
    context.event_writer.event_log_path = good;
    assert(!source_clock_finalize_video_watermark(&context, TRUE));
    assert(watermark_append_calls == (variant >= 3 ? 1 : 0));
    assert(g_file_test(good, G_FILE_TEST_EXISTS) == (variant == 5));
    assert(context.exit_code == (variant == 3 || variant == 5 ? 22 :
                                variant == 4 ? 20 : variant == 6 ? 14 : 0));
    if (variant == 5) {
      gchar *contents = NULL;
      gsize length;
      assert(g_file_get_contents(good, &contents, &length, NULL));
      assert(strchr(contents, '\n') == contents + length - 1);
      g_free(contents);
      g_remove(good);
    }
    watermark_fail_after_append = FALSE;
    g_rmdir(directory);
    g_free(missing); g_free(good); g_free(directory);
    g_mutex_clear(&context.video_scheduler.mutex);
    g_mutex_clear(&context.event_mutex);
    g_mutex_clear(&context.mutex);
  }
}
