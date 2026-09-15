/* Private to source_clock_pipeline.c. All state uses owner->mutex; push and
 * event I/O occur outside it. There is exactly one candidate compressed input,
 * one decoded buffer, and one encoder submission before the proof is committed.
 */
/* Configure only a freshly constructed candidate, before any streaming thread
 * starts. x264's byte-stream property is a preference, not a negotiated caps
 * constraint: the MP4 branch can otherwise negotiate AVC at the proof probe.
 * h264parse downstream remains free to convert this into AVC for recording.
 * On failure the caller discards the entire unstarted candidate pipeline. */
static gboolean source_clock_candidate_configure_encoder(GstElement *pipeline) {
  GstState state, pending;
  if (pipeline == NULL ||
      gst_element_get_state(pipeline, &state, &pending, 0) == GST_STATE_CHANGE_FAILURE ||
      state != GST_STATE_NULL || pending != GST_STATE_VOID_PENDING) return FALSE;
  GstElement *encoder = gst_bin_get_by_name(GST_BIN(pipeline), "source_clock_video_encoder");
  if (encoder == NULL) return FALSE;
  GstPad *src = gst_element_get_static_pad(encoder, "src");
  GstPad *peer = src == NULL ? NULL : gst_pad_get_peer(src);
  GstElement *parser = peer == NULL ? NULL : gst_pad_get_parent_element(peer);
  gboolean linked = FALSE;
  if (parser != NULL) {
    GstCaps *caps = gst_caps_from_string("video/x-h264,stream-format=byte-stream,alignment=au");
    if (gst_pad_unlink(src, peer))
      linked = gst_element_link_pads_filtered(encoder, "src", parser, GST_PAD_NAME(peer), caps);
    gst_caps_unref(caps);
  }
  if (parser != NULL) gst_object_unref(parser);
  if (peer != NULL) gst_object_unref(peer);
  if (src != NULL) gst_object_unref(src);
  gst_object_unref(encoder);
  return linked;
}

static void source_clock_candidate_fail(SourceClockPipeline *p) {
  g_mutex_lock(&p->mutex);
  source_clock_video_proof_stop(&p->candidate_proof);
  /* User/no-signal stop wins against a callback or push finishing afterwards. */
  if (!p->stopping) {
    p->stopping = TRUE;
    p->fatal_error = TRUE;
    p->exit_code = 22;
  }
  g_mutex_unlock(&p->mutex);
  if (p->loop != NULL) g_main_loop_quit(p->loop);
}

static void source_clock_candidate_drain_events(SourceClockPipeline *p) {
  for (;;) {
    SourceClockVideoProofEvent stage;
    uint32_t generation, sequence;
    uint64_t running;
    gboolean written, committed;
    g_mutex_lock(&p->mutex);
    if (p->stopping) source_clock_video_proof_stop(&p->candidate_proof);
    stage = source_clock_video_proof_take_event(&p->candidate_proof);
    generation = p->candidate_proof.source_generation;
    sequence = p->candidate_proof.selected_sequence;
    running = stage == SOURCE_CLOCK_PROOF_EVENT_NONE ? 0 : p->candidate_stage_time[stage];
    g_mutex_unlock(&p->mutex);
    if (stage == SOURCE_CLOCK_PROOF_EVENT_NONE) return;
    g_mutex_lock(&p->event_mutex);
    written = source_clock_event_write_video_proof(&p->event_writer, stage,
                                                  generation, sequence, running);
    g_mutex_unlock(&p->event_mutex);
    g_mutex_lock(&p->mutex);
    if (p->stopping) source_clock_video_proof_stop(&p->candidate_proof);
    committed = source_clock_video_proof_finish_event(&p->candidate_proof, stage, written);
    if (committed && stage == SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR)
      p->video_input_idr_reserved = p->video_input_idr_emitted = true;
    if (committed && stage == SOURCE_CLOCK_PROOF_EVENT_DECODED)
      p->media_ready_reserved = p->media_ready_emitted = true;
    if (committed && stage == SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR) {
      p->video_encoded_reserved = p->video_encoded_emitted = true;
      /* Frames were deliberately discarded while proving this IDR. Resume at
       * another IDR rather than accepting P dependencies we did not decode. */
      p->video_au_gate.waiting_for_idr = true;
    }
    g_mutex_unlock(&p->mutex);
    if (!committed) {
      if (stage == SOURCE_CLOCK_PROOF_EVENT_DECODED) g_remove(p->event_writer.media_ready_path);
      source_clock_candidate_fail(p);
      return;
    }
  }
}

static int source_clock_candidate_video(SourceClockPipeline *p,
    const SourceClockHeader *header, const uint8_t *payload, size_t bytes) {
  const uint8_t *prepared = payload;
  size_t prepared_bytes = bytes;
  uint8_t *owned = NULL;
  gboolean failed = FALSE;
  uint64_t now = (uint64_t)monotonic_ns();
  uint64_t started = (uint64_t)p->started_monotonic_us * 1000;
  uint64_t running = now >= started ? now - started : 0;
  g_mutex_lock(&p->mutex);
  if (p->stopping || !p->candidate_proof.active) goto done;
  p->last_signal_monotonic_us = g_get_monotonic_time();
  if (p->candidate_proof.selected_sequence != 0) goto done;
  if (header->codec != SOURCE_CLOCK_CODEC_H264_ANNEXB_AU ||
      !source_clock_video_bootstrap_prepare(&p->video_bootstrap,
          header->codec, header->flags, payload, bytes,
          &prepared, &prepared_bytes, &owned)) {
    failed = TRUE;
    goto done;
  }
  if (!source_clock_video_h264_is_idr(payload, bytes) ||
      header->sequence <= p->candidate_proof.watermark) goto done;
  SourceClockHeader normalized = *header;
  normalized.flags |= SOURCE_CLOCK_FLAG_KEYFRAME | SOURCE_CLOCK_FLAG_DISCONTINUITY;
  if (source_clock_video_au_gate_offer(&p->video_au_gate, header->codec,
          prepared, prepared_bytes, normalized.flags) == SOURCE_CLOCK_VIDEO_AU_DROP) goto done;
  if (!source_clock_video_proof_select(&p->candidate_proof,
          p->video_watermark.generation, header->sequence, TRUE)) {
    failed = !p->candidate_proof.active;
    goto done;
  }
  source_clock_timeline_offer_first(&p->timeline, SOURCE_CLOCK_STREAM_VIDEO,
                                    header->remote_ntp_ns, running);
  if (!save_pending_frame(&p->candidate_pending, &normalized,
                           prepared, prepared_bytes, running)) failed = TRUE;
done:
  if (failed) source_clock_video_proof_stop(&p->candidate_proof);
  g_mutex_unlock(&p->mutex);
  free(owned);
  if (failed) source_clock_candidate_fail(p);
  return 0;
}

/* Called from the real decoded appsink callback. A clean candidate decoder
 * has received exactly one VCL AU, so no PTS matching/meta propagation is used. */
static gboolean source_clock_candidate_decoded(SourceClockPipeline *p, GstBuffer *buffer) {
  gboolean accepted;
  if (p == NULL || !p->candidate_proof_enabled) return FALSE;
  g_mutex_lock(&p->mutex);
  if (source_clock_video_proof_complete(&p->candidate_proof)) {
    g_mutex_unlock(&p->mutex);
    return FALSE;
  }
  if (p->stopping) source_clock_video_proof_stop(&p->candidate_proof);
  accepted = source_clock_video_proof_decoded(&p->candidate_proof,
      buffer != NULL && GST_BUFFER_PTS_IS_VALID(buffer) &&
      !GST_BUFFER_FLAG_IS_SET(buffer, GST_BUFFER_FLAG_CORRUPTED));
  if (accepted) {
    p->candidate_decoded_buffer = gst_buffer_ref(buffer);
    p->candidate_stage_time[SOURCE_CLOCK_PROOF_EVENT_DECODED] = GST_BUFFER_PTS(buffer);
  }
  g_mutex_unlock(&p->mutex);
  if (accepted) source_clock_candidate_drain_events(p);
  else source_clock_candidate_fail(p);
  return TRUE;
}

static gboolean source_clock_candidate_encoded(SourceClockPipeline *p, GstBuffer *buffer) {
  GstMapInfo map = GST_MAP_INFO_INIT;
  gboolean valid, accepted;
  if (p == NULL || !p->candidate_proof_enabled) return FALSE;
  g_mutex_lock(&p->mutex);
  if (source_clock_video_proof_complete(&p->candidate_proof)) {
    g_mutex_unlock(&p->mutex);
    return FALSE;
  }
  if (p->stopping) source_clock_video_proof_stop(&p->candidate_proof);
  valid = buffer != NULL && GST_BUFFER_PTS_IS_VALID(buffer) &&
          !GST_BUFFER_FLAG_IS_SET(buffer, GST_BUFFER_FLAG_CORRUPTED) &&
          gst_buffer_map(buffer, &map, GST_MAP_READ);
  if (valid) {
    valid = source_clock_video_h264_is_idr(map.data, map.size);
    gst_buffer_unmap(buffer, &map);
  }
  accepted = source_clock_video_proof_encoded(&p->candidate_proof, valid);
  if (accepted) p->candidate_stage_time[SOURCE_CLOCK_PROOF_EVENT_ENCODED_IDR] = GST_BUFFER_PTS(buffer);
  g_mutex_unlock(&p->mutex);
  if (accepted) source_clock_candidate_drain_events(p);
  else source_clock_candidate_fail(p);
  return TRUE;
}

/* TRUE means the proof barrier handled this tick (including waiting/failure).
 * FALSE allows the existing scheduler to run. This is also the time-driven
 * warmup flush: video-only input never depends on a second media callback. */
static gboolean source_clock_candidate_tick(SourceClockPipeline *p, uint64_t tick_ns) {
  SourceClockPendingFrame pending = {0};
  GstBuffer *output = NULL;
  uint64_t mapped = 0;
  gboolean failed = FALSE;
  if (p == NULL || !p->candidate_proof_enabled) return FALSE;
  g_mutex_lock(&p->mutex);
  if (p->stopping) source_clock_video_proof_stop(&p->candidate_proof);
  if (!p->candidate_proof.active) { g_mutex_unlock(&p->mutex); return TRUE; }
  if (source_clock_video_proof_complete(&p->candidate_proof)) {
    /* Keep the proved frame available for HOLD while awaiting the recovery IDR. */
    if (p->candidate_decoded_buffer != NULL) {
      g_mutex_lock(&p->video_scheduler.mutex);
      /* This is the already-decoded proof frame, not a new normal decode.
       * Do not write offered_frames from the scheduler thread: its producer
       * is the regular decoder callback after the barrier is released. */
      SourceClockVideoFrame frame = {0, tick_ns,
                                     p->candidate_decoded_buffer};
      if (p->video_scheduler.policy.queue_size != 0 ||
          !source_clock_video_scheduler_offer(&p->video_scheduler.policy, frame))
        gst_buffer_unref(p->candidate_decoded_buffer);
      p->candidate_decoded_buffer = NULL;
      g_mutex_unlock(&p->video_scheduler.mutex);
    }
    g_mutex_unlock(&p->mutex);
    return FALSE;
  }
  if (p->candidate_pending.present && source_clock_timeline_ready(&p->timeline, tick_ns)) {
    SourceClockMapResult result = source_clock_timeline_map(&p->timeline,
        SOURCE_CLOCK_STREAM_VIDEO, p->candidate_pending.header.remote_ntp_ns,
        p->candidate_pending.arrival_ns, tick_ns, &mapped);
    if (result != SOURCE_CLOCK_MAPPED && result != SOURCE_CLOCK_RESET_EPOCH) failed = TRUE;
    else {
      pending = p->candidate_pending;
      memset(&p->candidate_pending, 0, sizeof(p->candidate_pending));
    }
  }
  if (failed) source_clock_video_proof_stop(&p->candidate_proof);
  g_mutex_unlock(&p->mutex);
  if (pending.present) {
    gboolean pushed = p->video_source != NULL &&
        push_source_buffer(p, &pending.header, pending.payload, pending.payload_bytes, mapped, 0);
    clear_pending_frame(&pending);
    g_mutex_lock(&p->mutex);
    if (p->stopping) source_clock_video_proof_stop(&p->candidate_proof);
    p->candidate_stage_time[SOURCE_CLOCK_PROOF_EVENT_INPUT_IDR] = mapped;
    failed = !source_clock_video_proof_input_result(&p->candidate_proof, pushed);
    g_mutex_unlock(&p->mutex);
    if (!failed) source_clock_candidate_drain_events(p);
  }
  g_mutex_lock(&p->mutex);
  if (!failed && !p->stopping && p->candidate_decoded_buffer != NULL &&
      GST_BUFFER_PTS(p->candidate_decoded_buffer) <= tick_ns &&
      source_clock_video_proof_take_output(&p->candidate_proof)) {
    output = source_clock_clone_scheduled_video_buffer(p->candidate_decoded_buffer);
    if (output == NULL) { failed = TRUE; source_clock_video_proof_stop(&p->candidate_proof); }
  }
  g_mutex_unlock(&p->mutex);
  if (output != NULL) {
    GST_BUFFER_PTS(output) = tick_ns;
    GST_BUFFER_DTS(output) = GST_CLOCK_TIME_NONE;
    GST_BUFFER_DURATION(output) = p->video_scheduler.policy.cadence_ns;
    gboolean pushed = gst_app_src_push_buffer(p->video_scheduler.output_source, output) == GST_FLOW_OK;
    g_mutex_lock(&p->mutex);
    if (p->stopping) source_clock_video_proof_stop(&p->candidate_proof);
    failed = !source_clock_video_proof_output_result(&p->candidate_proof, pushed);
    g_mutex_unlock(&p->mutex);
    if (!failed) source_clock_candidate_drain_events(p);
  }
  if (failed) source_clock_candidate_fail(p);
  return TRUE;
}
