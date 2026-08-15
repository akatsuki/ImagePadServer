mod adapter;
mod contracts;
mod gpu_render;
mod gpu_yuv_transport;
mod h264_ring;
mod hardware_surface;
mod lifecycle;
mod music_v2_contract;
mod music_v2_glyph_atlas;
mod music_v2_h264_sidecar;
mod music_v2_pass_graph;
mod music_v2_shader_draft;
mod music_v2_shader_export;
mod music_v2_shader_host;
mod music_v2_shader_module;
mod native_nvenc;
mod playlist_shader_timeline;
mod playlist_timeline;
mod protocol;
mod ring_transport;
mod shared_mapping;
mod shared_ring;
mod transport;
mod yuv420;

use crate::contracts::PixelFormat;
use crate::music_v2_shader_module::ShaderModule;
use protocol::{decode_request, encode, Request, Response, PROTOCOL_VERSION};
use std::io::{self, BufRead, Write};
use std::sync::{Arc, Mutex};
use std::thread;

struct RenderState {
    renderer: Option<gpu_render::Renderer>,
    h264: Option<music_v2_h264_sidecar::DirectNvencRenderer>,
    ready: bool,
    playlist: PlaylistSessionCache,
}

// The D3D12/NVENC handles are raw pointers and therefore not auto-Send, but the
// renderer is only ever accessed through the renderer mutex (main loop + ring
// worker serialize on it), so moving it across threads is safe.
unsafe impl Send for RenderState {}

#[derive(Default)]
struct PlaylistSessionCache {
    epoch: u64,
    track_id: String,
    assets_hash: String,
    assets: Option<protocol::TrackAssets>,
    width: u32,
    height: u32,
    fps: u32,
    timeline_version: u64,
    has_timeline_cursor: bool,
    last_timeline_sequence: u64,
    last_timeline_frame_index: u64,
    last_timeline_pts_ns: i64,
    next_timeline_batch_id: u64,
    pending_batch: Option<PendingPlaylistBatch>,
    stored_timeline: Option<protocol::TimelineChunk>,
    poisoned: bool,
}

#[derive(Clone)]
struct PendingPlaylistBatch {
    batch_id: u64,
    epoch: u64,
    timeline_version: u64,
    timeline: protocol::TimelineChunk,
}

fn ensure_playlist_renderer(state: &mut RenderState) -> Result<(), String> {
    if state.h264.is_some() {
        return Ok(());
    }
    let assets = state
        .playlist
        .assets
        .as_ref()
        .ok_or("playlist assets must be prepared before GPU renderer initialization")?
        .clone();
    let width = state.playlist.width;
    let height = state.playlist.height;
    let fps = state.playlist.fps;
    if width == 0 || height == 0 || fps == 0 {
        return Err("playlist GPU surface dimensions and fps are not prepared".into());
    }
    let scene = crate::contracts::MusicScenePayload {
        schema: crate::contracts::MUSIC_SCENE_SCHEMA,
        feature: crate::contracts::AudioFeatureFrame {
            schema: crate::contracts::CONTRACT_VERSION,
            sample_rate_hz: 48_000,
            frame_index: 0,
            pts_ns: 0,
            spectrum_q16: vec![0; 24],
            waveform_q16: Vec::new(),
            fingerprint_q16: Vec::new(),
            rms_q15: 0,
            peak_q15: 0,
        },
        pcm_f32le: vec![0; crate::contracts::MUSIC_PCM_WINDOW_BYTES],
        artwork: assets.artwork.clone(),
        base_texture: assets.base_texture.clone(),
        waveform_texture: assets.waveform_texture.clone(),
        loudness_texture: assets.loudness_texture.clone(),
        spectrum_texture: assets.spectrum_texture.clone(),
        glyph_atlas: assets.glyph_atlas.clone(),
        text_overlay: assets.text_overlay.clone(),
        layout: assets.layout.clone(),
        dynamics: crate::contracts::MusicSceneDynamics {
            duration_seconds: assets.duration_frames as f64 / fps as f64,
            loudness_envelope: vec![0; crate::contracts::MUSIC_MAX_LOUDNESS_SAMPLES],
            loudness_trend: Vec::new(),
            ..Default::default()
        },
        palette: assets.palette.clone(),
        fingerprint: assets.assets_hash.clone(),
    };
    state.h264 = Some(music_v2_h264_sidecar::DirectNvencRenderer::new(
        width, height, fps, &scene,
    )?);
    Ok(())
}

fn ensure_legacy_renderer(state: &mut RenderState) -> Result<&mut gpu_render::Renderer, String> {
    if state.renderer.is_none() {
        state.renderer = Some(gpu_render::Renderer::new()?);
    }
    state
        .renderer
        .as_mut()
        .ok_or_else(|| "GPU renderer initialization returned no renderer".into())
}

fn playlist_batch_error(
    state: &mut RenderState,
    batch_id: u64,
    code: &str,
    message: impl Into<String>,
    poison: bool,
) -> (Response, bool) {
    if poison {
        state.playlist.poisoned = true;
    }
    (
        Response::BatchError {
            batch_id,
            failed_sequence: None,
            code: code.into(),
            message: message.into(),
        },
        false,
    )
}

fn validate_playlist_static_assets(assets: &protocol::TrackAssets) -> Result<(), String> {
    fn validate_texture(
        label: &str,
        format: PixelFormat,
        width: u32,
        height: u32,
        row_stride: u32,
        payload: &[u8],
    ) -> Result<(), String> {
        if format != PixelFormat::Rgba8 {
            return Err(format!(
                "playlist {label} static texture must use canonical RGBA8"
            ));
        }
        playlist_shader_timeline::validate_playlist_texture_payload(
            width,
            height,
            row_stride,
            payload.len(),
        )?;
        let expected_len = usize::try_from(row_stride)
            .ok()
            .and_then(|stride| stride.checked_mul(height as usize))
            .ok_or_else(|| format!("playlist {label} static texture payload size overflows"))?;
        if payload.len() != expected_len {
            return Err(format!(
                "playlist {label} static texture payload length must equal row_stride*height"
            ));
        }
        Ok(())
    }

    let artwork = assets
        .artwork
        .as_ref()
        .ok_or("playlist artwork static texture is required")?;
    validate_texture(
        "artwork",
        artwork.format,
        artwork.width,
        artwork.height,
        artwork.row_stride,
        &artwork.payload,
    )?;
    let glyph = assets
        .glyph_atlas
        .as_ref()
        .ok_or("playlist glyph static texture is required")?;
    validate_texture(
        "glyph atlas",
        PixelFormat::Rgba8,
        glyph.width,
        glyph.height,
        glyph.row_stride,
        &glyph.payload,
    )?;
    if let Some(loudness) = assets.loudness_texture.as_ref() {
        validate_texture(
            "loudness",
            loudness.format,
            loudness.width,
            loudness.height,
            loudness.row_stride,
            &loudness.payload,
        )?;
    }
    Ok(())
}

fn prepare_playlist_track(
    state: &mut RenderState,
    epoch: u64,
    track_id: String,
    width: u32,
    height: u32,
    fps: u32,
    assets: protocol::TrackAssets,
) -> (Response, bool) {
    if epoch == 0
        || track_id.is_empty()
        || width == 0
        || height == 0
        || fps == 0
        || assets.schema != protocol::PROTOCOL_VERSION
        || assets.track_id != track_id
        || assets.assets_hash.len() != 64
        || !assets
            .assets_hash
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit())
        || assets.duration_frames == 0
        || assets.artwork.is_none()
        || assets.glyph_atlas.is_none()
    {
        return (
            Response::Error {
                code: "invalid_playlist_track_assets".into(),
                message: "playlist assets identity, schema, hash, or duration is invalid".into(),
            },
            false,
        );
    }
    if let Err(message) = validate_playlist_static_assets(&assets) {
        return (
            Response::Error {
                code: "invalid_playlist_static_assets".into(),
                message,
            },
            false,
        );
    }
    if state.playlist.epoch != 0 && epoch < state.playlist.epoch {
        return (
            Response::Error {
                code: "stale_playlist_epoch".into(),
                message: format!(
                    "received epoch {epoch}, current epoch is {}",
                    state.playlist.epoch
                ),
            },
            false,
        );
    }
    if state.playlist.epoch == epoch
        && ((!state.playlist.track_id.is_empty() && state.playlist.track_id != track_id)
            || (!state.playlist.assets_hash.is_empty()
                && state.playlist.assets_hash != assets.assets_hash))
    {
        return (
            Response::Error {
                code: "playlist_epoch_identity_conflict".into(),
                message: "same playlist epoch cannot replace its track or asset hash".into(),
            },
            false,
        );
    }
    state.playlist = PlaylistSessionCache {
        epoch,
        track_id,
        assets_hash: assets.assets_hash.clone(),
        assets: Some(assets),
        width,
        height,
        fps,
        timeline_version: 0,
        has_timeline_cursor: false,
        last_timeline_sequence: 0,
        last_timeline_frame_index: 0,
        last_timeline_pts_ns: 0,
        next_timeline_batch_id: 0,
        pending_batch: None,
        stored_timeline: None,
        poisoned: false,
    };
    (
        Response::TrackReady {
            epoch,
            assets_hash: state.playlist.assets_hash.clone(),
        },
        false,
    )
}

fn reconstruct_playlist_timeline_chunk(
    cache: &PlaylistSessionCache,
    inline: Option<protocol::TimelineChunk>,
    frame_indices: Option<Vec<u64>>,
) -> Result<protocol::TimelineChunk, String> {
    match (inline, frame_indices) {
        (Some(chunk), None) => Ok(chunk),
        (None, Some(indices)) => {
            if indices.is_empty() || indices.len() > crate::protocol::MAX_H264_BATCH_FRAMES {
                return Err("frame_indices size is outside the bounded window".into());
            }
            let stored = cache
                .stored_timeline
                .as_ref()
                .ok_or("no prepared timeline; send prepare_timeline before frame-index batches")?;
            let mut frames = Vec::with_capacity(indices.len());
            for index in indices {
                let frame = stored
                    .frames
                    .iter()
                    .find(|frame| frame.frame_index == index)
                    .cloned()
                    .ok_or_else(|| {
                        format!("frame_index {index} is not in the prepared timeline")
                    })?;
                frames.push(frame);
            }
            Ok(protocol::TimelineChunk {
                scroll_profiles: stored.scroll_profiles.clone(),
                frames,
            })
        }
        (Some(_), Some(_)) => {
            Err("render_timeline_batch cannot carry both inline timeline and frame_indices".into())
        }
        (None, None) => {
            Err("render_timeline_batch requires inline timeline or frame_indices".into())
        }
    }
}

fn validate_playlist_timeline_batch(
    state: &mut RenderState,
    batch_id: u64,
    epoch: u64,
    assets_hash: &str,
    timeline_version: u64,
    timeline: &protocol::TimelineChunk,
    transition: Option<&protocol::TransitionPlan>,
    output: crate::contracts::OutputFormat,
) -> Result<(), (Response, bool)> {
    if !matches!(output, crate::contracts::OutputFormat::H264Bitstream) {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "invalid_playlist_gpu_output",
            "playlist GPU evaluation currently requires h264_bitstream",
            true,
        ));
    }
    if state.playlist.poisoned {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "playlist_session_poisoned",
            "playlist session rejected a prior contract violation; prepare a new epoch",
            false,
        ));
    }
    if state.playlist.pending_batch.is_some() {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "playlist_pending_batch_not_drained",
            "drain the pending playlist batch before accepting another timeline batch",
            true,
        ));
    }
    let Some(assets) = state.playlist.assets.as_ref() else {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "playlist_assets_not_prepared",
            "send prepare_track before render_timeline_batch",
            true,
        ));
    };
    if epoch != state.playlist.epoch || assets_hash != state.playlist.assets_hash {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "playlist_asset_epoch_mismatch",
            "timeline batch epoch or asset hash does not match the prepared track",
            true,
        ));
    }
    if timeline_version == 0 || timeline_version < state.playlist.timeline_version {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "playlist_timeline_version_invalid",
            "timeline version is stale for the prepared playlist session",
            true,
        ));
    }
    if batch_id == u64::MAX {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "playlist_batch_id_overflow",
            "timeline batch id cannot wrap around",
            true,
        ));
    }
    let expected_batch_id = if timeline_version == state.playlist.timeline_version {
        state.playlist.next_timeline_batch_id
    } else {
        0
    };
    if batch_id != expected_batch_id {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "invalid_playlist_timeline_batch_order",
            format!(
                "timeline batch id must be {} for timeline version {}",
                expected_batch_id, timeline_version
            ),
            true,
        ));
    }
    if timeline.frames.is_empty() || timeline.frames.len() > protocol::MAX_H264_BATCH_FRAMES {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "invalid_playlist_timeline_size",
            format!(
                "timeline batch must contain 1..{} frames",
                protocol::MAX_H264_BATCH_FRAMES
            ),
            true,
        ));
    }
    if timeline.scroll_profiles.iter().any(|profile| {
        profile.id.is_empty()
            || profile.fps == 0
            || profile.loop_frames == 0
            || profile.x_q16.len() as u64 != profile.loop_frames
            || profile.y_q16.len() as u64 != profile.loop_frames
            || profile.alpha_q16.len() as u64 != profile.loop_frames
    }) {
        return Err(playlist_batch_error(
            state,
            batch_id,
            "invalid_playlist_scroll_profile",
            "scroll profile arrays must match a non-zero loop length",
            true,
        ));
    }
    for frame in &timeline.frames {
        if frame.pts_ns < 0 || frame.spectrum_q16.len() > crate::contracts::MUSIC_MAX_FEATURE_BINS {
            return Err(playlist_batch_error(
                state,
                batch_id,
                "invalid_playlist_frame_control",
                "frame control contains an invalid PTS or spectrum length",
                true,
            ));
        }
    }
    for pair in timeline.frames.windows(2) {
        if pair[1].sequence != pair[0].sequence.saturating_add(1)
            || pair[1].frame_index != pair[0].frame_index.saturating_add(1)
            || pair[1].pts_ns <= pair[0].pts_ns
        {
            return Err(playlist_batch_error(
                state,
                batch_id,
                "invalid_playlist_timeline_order",
                "timeline sequence, frame index, and PTS must be strictly ordered",
                true,
            ));
        }
    }
    if state.playlist.has_timeline_cursor {
        let first = &timeline.frames[0];
        let contiguous = if timeline_version == state.playlist.timeline_version {
            state.playlist.last_timeline_sequence != u64::MAX
                && first.sequence == state.playlist.last_timeline_sequence + 1
                && state.playlist.last_timeline_frame_index != u64::MAX
                && first.frame_index == state.playlist.last_timeline_frame_index + 1
        } else {
            first.sequence > state.playlist.last_timeline_sequence
        };
        if !contiguous || first.pts_ns <= state.playlist.last_timeline_pts_ns {
            return Err(playlist_batch_error(
                state,
                batch_id,
                "invalid_playlist_timeline_order",
                "timeline batch overlaps or precedes the committed playlist cursor",
                true,
            ));
        }
    }
    if let Some(transition) = transition {
        if transition.epoch != epoch
            || transition.source_track_id != assets.track_id
            || transition.fade_start_sequence <= timeline.frames.last().unwrap().sequence
        {
            return Err(playlist_batch_error(
                state,
                batch_id,
                "invalid_playlist_transition_plan",
                "transition plan is stale, targets another track, or overlaps the submitted timeline",
                true,
            ));
        }
    }
    Ok(())
}

fn commit_playlist_timeline_batch(
    playlist: &mut PlaylistSessionCache,
    batch_id: u64,
    timeline_version: u64,
    timeline: &protocol::TimelineChunk,
) {
    let last = timeline
        .frames
        .last()
        .expect("validated playlist timeline batch is non-empty");
    playlist.timeline_version = timeline_version;
    playlist.has_timeline_cursor = true;
    playlist.last_timeline_sequence = last.sequence;
    playlist.last_timeline_frame_index = last.frame_index;
    playlist.last_timeline_pts_ns = last.pts_ns;
    playlist.next_timeline_batch_id = batch_id + 1;
}

fn commit_pending_playlist_timeline_batch(
    playlist: &mut PlaylistSessionCache,
    batch_id: u64,
    epoch: u64,
    timeline_version: u64,
) -> Result<(), String> {
    let pending = playlist
        .pending_batch
        .clone()
        .ok_or_else(|| "playlist pending batch is missing".to_owned())?;
    if pending.batch_id != batch_id
        || pending.epoch != epoch
        || pending.timeline_version != timeline_version
    {
        return Err("playlist pending batch identity does not match drain".into());
    }
    commit_playlist_timeline_batch(
        playlist,
        pending.batch_id,
        pending.timeline_version,
        &pending.timeline,
    );
    playlist.pending_batch = None;
    Ok(())
}

fn response_for(request: Request, renderer: &mut Option<RenderState>) -> (Response, bool) {
    match request {
        Request::Hello {
            version,
            session: _,
        } if version != PROTOCOL_VERSION => (
            Response::Error {
                code: "protocol_version_mismatch".into(),
                message: format!("supported version {}", PROTOCOL_VERSION),
            },
            false,
        ),
        Request::Hello { session, .. } => {
            match gpu_render::Renderer::runtime_fingerprint() {
                Err(error) => (
                    Response::Error {
                        code: "gpu_renderer_unavailable".into(),
                        // Preserve the adapter/backend diagnostic so the owner can
                        // distinguish missing hardware from backend initialization
                        // or requested-adapter failures in the acceptance report.
                        message: error,
                    },
                    false,
                ),
                Ok(mut fingerprint) => {
                    let mut h264_capable = music_v2_h264_sidecar::probe_capability();
                    if h264_capable {
                        match gpu_render::Renderer::runtime_fingerprint_with_backends(
                            wgpu::Backends::DX12,
                        ) {
                            Ok(direct_fingerprint) if direct_fingerprint.backend == "dx12" => {
                                fingerprint = direct_fingerprint;
                            }
                            Ok(direct_fingerprint) => {
                                h264_capable = false;
                                eprintln!(
                                    "[gpu-stage] direct_h264_disabled backend_mismatch={}",
                                    direct_fingerprint.backend
                                );
                            }
                            Err(error) => {
                                h264_capable = false;
                                eprintln!("[gpu-stage] direct_h264_disabled dx12_fingerprint_error={error}");
                            }
                        }
                    }
                    *renderer = Some(RenderState {
                        renderer: None,
                        h264: None,
                        ready: true,
                        playlist: PlaylistSessionCache::default(),
                    });
                    let mut formats = vec![
                        crate::contracts::OutputFormat::Rgba8,
                        crate::contracts::OutputFormat::Yuv420p,
                    ];
                    if h264_capable {
                        formats.push(crate::contracts::OutputFormat::H264Bitstream);
                    }
                    (
                        Response::HelloAck {
                            version: PROTOCOL_VERSION,
                            session,
                            adapter: fingerprint.adapter,
                            backend: fingerprint.backend,
                            toolchain: fingerprint.toolchain,
                            // Both formats are produced by the GPU renderer. The
                            // client still selects RGBA by default for backwards
                            // compatibility.
                            outputs: Some(crate::contracts::OutputCapabilities {
                                schema: crate::contracts::CONTRACT_VERSION,
                                formats,
                                max_width: crate::contracts::MAX_DIMENSION,
                                max_height: crate::contracts::MAX_DIMENSION,
                                row_alignment: crate::contracts::ROW_ALIGNMENT as u32,
                            }),
                        },
                        false,
                    )
                }
            }
        }
        Request::Health => (
            Response::Health {
                ready: renderer.as_ref().map(|state| state.ready).unwrap_or(false),
                protocol: PROTOCOL_VERSION,
            },
            false,
        ),
        Request::Flush => match renderer.as_mut().and_then(|state| state.h264.as_mut()) {
            Some(h264) => match h264.flush() {
                Ok(()) => (Response::Flushed, false),
                Err(message) => (
                    Response::Error {
                        code: "direct_h264_flush_failed".into(),
                        message,
                    },
                    false,
                ),
            },
            None => (
                Response::Error {
                    code: "direct_h264_flush_unavailable".into(),
                    message: "direct H264 renderer is not initialized".into(),
                },
                false,
            ),
        },
        Request::Render {
            width,
            height,
            sequence,
            pts_ns,
            scene,
            output,
        } => match renderer.as_mut() {
            Some(state) => {
                if let Some(scene) = scene.as_ref() {
                    if let Err(error) = scene.validate() {
                        return (
                            Response::Error {
                                code: "invalid_scene".into(),
                                message: format!("{error:?}"),
                            },
                            false,
                        );
                    }
                }
                let rendered = match output {
                    crate::contracts::OutputFormat::Rgba8 => ensure_legacy_renderer(state)
                        .and_then(|renderer| {
                            renderer
                                .render_with_scene(width, height, sequence, pts_ns, scene.as_ref())
                                .map(|frame| Response::Frame { frame })
                        }),
                    crate::contracts::OutputFormat::Yuv420p => ensure_legacy_renderer(state)
                        .and_then(|renderer| {
                            renderer
                                .render_yuv420_with_scene(
                                    width,
                                    height,
                                    sequence,
                                    pts_ns,
                                    scene.as_ref(),
                                )
                                .map(|frame| Response::YuvFrame { frame })
                        }),
                    crate::contracts::OutputFormat::H264Bitstream => {
                        let Some(scene) = scene.as_ref() else {
                            return (
                                Response::Error {
                                    code: "direct_h264_scene_required".into(),
                                    message:
                                        "direct H264 output requires a canonical scene payload"
                                            .into(),
                                },
                                false,
                            );
                        };
                        if state.h264.is_none() {
                            match music_v2_h264_sidecar::DirectNvencRenderer::new(
                                width, height, 30, scene,
                            ) {
                                Ok(direct) => state.h264 = Some(direct),
                                Err(message) => {
                                    return (
                                        Response::Error {
                                            code: "direct_h264_unavailable".into(),
                                            message,
                                        },
                                        false,
                                    )
                                }
                            }
                        }
                        state
                            .h264
                            .as_mut()
                            .expect("direct H264 renderer initialized")
                            .render_frame(sequence, pts_ns, scene)
                            .map(|frame| Response::EncodedFrame { frame })
                    }
                };
                match rendered {
                    Ok(response) => (response, false),
                    Err(message) => (
                        Response::Error {
                            code: "gpu_render_failed".into(),
                            message,
                        },
                        false,
                    ),
                }
            }
            None => (
                Response::Error {
                    code: "gpu_renderer_unavailable".into(),
                    message: "GPU renderer is not initialized; send Hello first".into(),
                },
                false,
            ),
        },
        Request::RenderBatch {
            batch_id,
            width,
            height,
            frames,
            output,
        } => match renderer.as_mut() {
            Some(state) => {
                if !matches!(output, crate::contracts::OutputFormat::H264Bitstream) {
                    return (
                        Response::BatchError {
                            batch_id,
                            failed_sequence: None,
                            code: "invalid_h264_batch_output".into(),
                            message: "render_batch currently supports only h264_bitstream".into(),
                        },
                        false,
                    );
                }
                if frames.is_empty() || frames.len() > protocol::MAX_H264_BATCH_FRAMES {
                    return (
                        Response::BatchError {
                            batch_id,
                            failed_sequence: None,
                            code: "invalid_h264_batch_size".into(),
                            message: format!(
                                "H264 batch must contain 1..{} frames",
                                protocol::MAX_H264_BATCH_FRAMES
                            ),
                        },
                        false,
                    );
                }
                for pair in frames.windows(2) {
                    if pair[1].sequence != pair[0].sequence.saturating_add(1)
                        || pair[1].pts_ns <= pair[0].pts_ns
                    {
                        return (
                            Response::BatchError {
                                batch_id,
                                failed_sequence: None,
                                code: "invalid_h264_batch_order".into(),
                                message: "H264 batch sequence and PTS must be strictly ordered"
                                    .into(),
                            },
                            false,
                        );
                    }
                }
                for frame in &frames {
                    if let Err(error) = frame.scene.validate() {
                        return (
                            Response::BatchError {
                                batch_id,
                                failed_sequence: Some(frame.sequence),
                                code: "invalid_scene".into(),
                                message: format!("{error:?}"),
                            },
                            false,
                        );
                    }
                }
                let first_scene = &frames[0].scene;
                if state.h264.is_none() {
                    match music_v2_h264_sidecar::DirectNvencRenderer::new(
                        width,
                        height,
                        30,
                        first_scene,
                    ) {
                        Ok(direct) => state.h264 = Some(direct),
                        Err(message) => {
                            return (
                                Response::BatchError {
                                    batch_id,
                                    failed_sequence: None,
                                    code: "direct_h264_unavailable".into(),
                                    message,
                                },
                                false,
                            )
                        }
                    }
                }
                let direct = state
                    .h264
                    .as_mut()
                    .expect("direct H264 renderer initialized");
                let batch_refs = frames
                    .iter()
                    .map(|frame| (&frame.scene, frame.sequence, frame.pts_ns))
                    .collect::<Vec<_>>();
                let encoded = match direct.render_batch(&batch_refs) {
                    Ok(encoded) => encoded,
                    Err(message) => {
                        return (
                            Response::BatchError {
                                batch_id,
                                failed_sequence: None,
                                code: "gpu_render_failed".into(),
                                message,
                            },
                            false,
                        )
                    }
                };
                (
                    Response::EncodedBatch {
                        batch_id,
                        frames: encoded,
                    },
                    false,
                )
            }
            None => (
                Response::Error {
                    code: "gpu_renderer_unavailable".into(),
                    message: "GPU renderer is not initialized; send Hello first".into(),
                },
                false,
            ),
        },
        Request::PrepareTrack {
            epoch,
            track_id,
            width,
            height,
            fps,
            assets,
        } => match renderer.as_mut() {
            Some(state) => {
                prepare_playlist_track(state, epoch, track_id, width, height, fps, assets)
            }
            None => (
                Response::Error {
                    code: "gpu_renderer_unavailable".into(),
                    message: "GPU renderer is not initialized; send Hello first".into(),
                },
                false,
            ),
        },
        Request::PrepareTimeline {
            epoch,
            track_id,
            assets_hash,
            timeline_version,
            timeline,
        } => match renderer.as_mut() {
            Some(state) => {
                if state.playlist.poisoned {
                    return (
                        Response::Error {
                            code: "playlist_session_poisoned".into(),
                            message: "playlist session rejected a prior contract violation".into(),
                        },
                        false,
                    );
                }
                if epoch != state.playlist.epoch
                    || track_id != state.playlist.track_id
                    || assets_hash != state.playlist.assets_hash
                {
                    return (
                        Response::Error {
                            code: "playlist_timeline_identity_mismatch".into(),
                            message: "prepare_timeline identity does not match the prepared track"
                                .into(),
                        },
                        false,
                    );
                }
                if state.playlist.assets.is_none() {
                    return (
                        Response::Error {
                            code: "playlist_assets_not_prepared".into(),
                            message: "send prepare_track before prepare_timeline".into(),
                        },
                        false,
                    );
                }
                if timeline.frames.is_empty() {
                    return (
                        Response::Error {
                            code: "playlist_timeline_empty".into(),
                            message: "prepared timeline must contain at least one frame".into(),
                        },
                        false,
                    );
                }
                let frame_count = timeline.frames.len() as u64;
                state.playlist.timeline_version = timeline_version;
                if let Err(message) = ensure_playlist_renderer(state) {
                    return (
                        Response::Error {
                            code: "playlist_renderer_unavailable".into(),
                            message,
                        },
                        true,
                    );
                }
                state.playlist.stored_timeline = Some(timeline);
                (
                    Response::TimelinePrepared {
                        epoch,
                        timeline_version,
                        frame_count,
                    },
                    false,
                )
            }
            None => (
                Response::Error {
                    code: "gpu_renderer_unavailable".into(),
                    message: "GPU renderer is not initialized; send Hello first".into(),
                },
                false,
            ),
        },
        Request::RenderTimelineBatch {
            batch_id,
            epoch,
            assets_hash,
            timeline_version,
            timeline,
            frame_indices,
            transition,
            submit_only,
            output,
        } => match renderer.as_mut() {
            Some(state) => {
                let timeline = match reconstruct_playlist_timeline_chunk(
                    &state.playlist,
                    timeline,
                    frame_indices,
                ) {
                    Ok(chunk) => chunk,
                    Err(message) => {
                        return playlist_batch_error(
                            state,
                            batch_id,
                            "playlist_timeline_lookup_failed",
                            message,
                            true,
                        );
                    }
                };
                if let Err(response) = validate_playlist_timeline_batch(
                    state,
                    batch_id,
                    epoch,
                    &assets_hash,
                    timeline_version,
                    &timeline,
                    transition.as_ref(),
                    output,
                ) {
                    return response;
                }
                let Some(assets) = state.playlist.assets.as_ref() else {
                    return playlist_batch_error(
                        state,
                        batch_id,
                        "playlist_assets_not_prepared",
                        "send prepare_track before render_timeline_batch",
                        true,
                    );
                };
                let pre_submit_started = std::time::Instant::now();
                let resolved = match playlist_timeline::resolve_chunk(assets, &timeline) {
                    Ok(resolved) => resolved,
                    Err(message) => {
                        return playlist_batch_error(
                            state,
                            batch_id,
                            "playlist_timeline_resolution_failed",
                            message,
                            true,
                        )
                    }
                };
                if let Err(message) = playlist_shader_timeline::validate_playlist_descriptor(
                    playlist_shader_timeline::PLAYLIST_TIMELINE_SHADER_MODULE.descriptor(),
                ) {
                    return playlist_batch_error(
                        state,
                        batch_id,
                        "playlist_shader_contract_invalid",
                        message,
                        true,
                    );
                }
                let uniforms = resolved
                    .iter()
                    .map(playlist_shader_timeline::PlaylistFrameUniform::from_resolved)
                    .collect::<Vec<_>>();
                let uniform_bytes = uniforms
                    .iter()
                    .map(playlist_shader_timeline::PlaylistFrameUniform::to_bytes)
                    .collect::<Vec<_>>();
                if std::env::var("IMAGEPAD_GPU_STAGE_TIMING").as_deref() == Ok("1") {
                    eprintln!(
                        "[gpu-stage] rust_playlist_resolve batch_id={} resolve_seconds={:.6}",
                        batch_id,
                        pre_submit_started.elapsed().as_secs_f64()
                    );
                }
                if uniform_bytes.iter().any(|bytes| {
                    bytes.len() != playlist_shader_timeline::PLAYLIST_FRAME_UNIFORM_BYTES
                }) {
                    return playlist_batch_error(
                        state,
                        batch_id,
                        "playlist_uniform_serialization_failed",
                        "resolved playlist controls do not match the shader uniform size",
                        true,
                    );
                }
                if let Err(message) = ensure_playlist_renderer(state) {
                    return playlist_batch_error(
                        state,
                        batch_id,
                        "playlist_gpu_renderer_init_failed",
                        message,
                        true,
                    );
                }
                let static_started = std::time::Instant::now();
                if let Some(assets) = state.playlist.assets.as_ref() {
                    let assets = assets.clone();
                    if let Some(h264) = state.h264.as_mut() {
                        if let Err(message) = h264.prepare_playlist_static_textures(&assets) {
                            return playlist_batch_error(
                                state,
                                batch_id,
                                "playlist_static_texture_upload_failed",
                                message,
                                true,
                            );
                        }
                    }
                } else {
                    return playlist_batch_error(
                        state,
                        batch_id,
                        "playlist_assets_not_prepared",
                        "playlist static textures require PrepareTrack first",
                        true,
                    );
                }
                let spectrum_started = std::time::Instant::now();
                if let Some(h264) = state.h264.as_mut() {
                    if let Err(message) = h264.prepare_playlist_spectrum_buffer(&timeline) {
                        return playlist_batch_error(
                            state,
                            batch_id,
                            "playlist_spectrum_upload_failed",
                            message,
                            true,
                        );
                    }
                }
                if std::env::var("IMAGEPAD_GPU_STAGE_TIMING").as_deref() == Ok("1") {
                    eprintln!(
                        "[gpu-stage] rust_playlist_textures batch_id={} static_seconds={:.6} spectrum_seconds={:.6}",
                        batch_id,
                        static_started.elapsed().as_secs_f64(),
                        spectrum_started.elapsed().as_secs_f64()
                    );
                }
                if let Some(h264) = state.h264.as_mut() {
                    if let Err(message) = h264.ensure_playlist_pipeline() {
                        return playlist_batch_error(
                            state,
                            batch_id,
                            "playlist_gpu_pipeline_creation_failed",
                            message,
                            true,
                        );
                    }
                }
                if let Some(h264) = state.h264.as_mut() {
                    if let Err(message) = h264.ensure_playlist_frame_slots() {
                        return playlist_batch_error(
                            state,
                            batch_id,
                            "playlist_bind_group_population_failed",
                            message,
                            true,
                        );
                    }
                }
                if let Some(h264) = state.h264.as_mut() {
                    if submit_only {
                        if state.playlist.pending_batch.is_some() {
                            return playlist_batch_error(
                                state,
                                batch_id,
                                "playlist_pending_batch_not_drained",
                                "drain the pending playlist batch before submitting another",
                                true,
                            );
                        }
                        if let Err(message) = h264.submit_playlist_timeline(&uniforms) {
                            return playlist_batch_error(
                                state,
                                batch_id,
                                "playlist_nvenc_submit_failed",
                                message,
                                true,
                            );
                        }
                        state.playlist.pending_batch = Some(PendingPlaylistBatch {
                            batch_id,
                            epoch,
                            timeline_version,
                            timeline: timeline.clone(),
                        });
                        return (
                            Response::TimelineSubmitted {
                                batch_id,
                                epoch,
                                timeline_version,
                            },
                            false,
                        );
                    }
                    if std::env::var("IMAGEPAD_GPU_STAGE_TIMING").as_deref() == Ok("1") {
                        eprintln!(
                            "[gpu-stage] rust_playlist_pre_submit batch_id={} pre_submit_seconds={:.6}",
                            batch_id,
                            pre_submit_started.elapsed().as_secs_f64()
                        );
                    }
                    let frames = match h264
                        .submit_playlist_timeline(&uniforms)
                        .and_then(|_| h264.drain_playlist_timeline())
                    {
                        Ok(frames) => frames,
                        Err(message) => {
                            return playlist_batch_error(
                                state,
                                batch_id,
                                "playlist_nvenc_encode_failed",
                                message,
                                true,
                            )
                        }
                    };
                    commit_playlist_timeline_batch(
                        &mut state.playlist,
                        batch_id,
                        timeline_version,
                        &timeline,
                    );
                    return (
                        Response::EncodedTimelineBatch {
                            batch_id,
                            epoch,
                            timeline_version,
                            applied_transition_version: None,
                            frames,
                        },
                        false,
                    );
                }
                (
                    Response::BatchError {
                        batch_id,
                        failed_sequence: None,
                        code: "playlist_gpu_execution_not_wired".into(),
                        message: format!(
                            "playlist timeline controls resolved and serialized for {} frames ({} bytes each); GPU surface executor is not wired yet",
                            uniform_bytes.len(),
                            playlist_shader_timeline::PLAYLIST_FRAME_UNIFORM_BYTES
                        ),
                    },
                    false,
                )
            }
            None => (
                Response::Error {
                    code: "gpu_renderer_unavailable".into(),
                    message: "GPU renderer is not initialized; send Hello first".into(),
                },
                false,
            ),
        },
        Request::DrainTimelineBatch {
            batch_id,
            epoch,
            timeline_version,
        } => match renderer.as_mut() {
            Some(state) => {
                let pending = match state.playlist.pending_batch.clone() {
                    Some(pending)
                        if pending.batch_id == batch_id
                            && pending.epoch == epoch
                            && pending.timeline_version == timeline_version =>
                    {
                        pending
                    }
                    _ => {
                        return playlist_batch_error(
                            state,
                            batch_id,
                            "playlist_pending_batch_mismatch",
                            "drain does not match the submitted playlist batch",
                            true,
                        )
                    }
                };
                let frames = match state.h264.as_mut() {
                    Some(h264) => match h264.drain_playlist_timeline() {
                        Ok(frames) => frames,
                        Err(message) => {
                            return playlist_batch_error(
                                state,
                                batch_id,
                                "playlist_nvenc_drain_failed",
                                message,
                                true,
                            )
                        }
                    },
                    None => {
                        return playlist_batch_error(
                            state,
                            batch_id,
                            "playlist_gpu_execution_not_wired",
                            "playlist H264 executor is not initialized",
                            true,
                        )
                    }
                };
                if let Err(message) = commit_pending_playlist_timeline_batch(
                    &mut state.playlist,
                    pending.batch_id,
                    pending.epoch,
                    pending.timeline_version,
                ) {
                    return playlist_batch_error(
                        state,
                        batch_id,
                        "playlist_pending_batch_commit_failed",
                        message,
                        true,
                    );
                }
                (
                    Response::EncodedTimelineBatch {
                        batch_id,
                        epoch,
                        timeline_version,
                        applied_transition_version: None,
                        frames,
                    },
                    false,
                )
            }
            None => (
                Response::Error {
                    code: "gpu_renderer_unavailable".into(),
                    message: "GPU renderer is not initialized; send Hello first".into(),
                },
                false,
            ),
        },
        Request::RenderV2 { job, .. } => {
            if let Err(message) = music_v2_contract::validate_production(&job) {
                return (
                    Response::Error {
                        code: "invalid_music_render_v2_job".into(),
                        message,
                    },
                    false,
                );
            }

            let shader = &crate::music_v2_shader_draft::DRAFT_SHADER_MODULE;
            let descriptor = crate::music_v2_shader_module::ShaderModule::descriptor(shader);
            if let Err(message) = crate::music_v2_shader_module::validate_descriptor(descriptor) {
                return (
                    Response::Error {
                        code: "invalid_music_render_v2_shader_module".into(),
                        message,
                    },
                    false,
                );
            }
            if !descriptor.production_ready {
                return (
                    Response::Error {
                        code: "music_render_v2_shader_not_ready".into(),
                        message: format!(
                            "V2 contract and GPU host boundary accepted; shader module v{} entry point {} is not production-ready",
                            descriptor.version, descriptor.entry_point
                        ),
                    },
                    false,
                );
            }
            (
                Response::Error {
                    code: "music_render_v2_execution_not_wired".into(),
                    message: "shader module is ready but V2 execution is not wired".into(),
                },
                false,
            )
        }
        Request::ResetSession => {
            if let Some(state) = renderer.as_mut() {
                state.playlist = PlaylistSessionCache::default();
                if let Some(h264) = state.h264.as_mut() {
                    if let Err(message) = h264.reset_playlist_session() {
                        return (
                            Response::Error {
                                code: "playlist_session_reset_failed".into(),
                                message,
                            },
                            false,
                        );
                    }
                }
            }
            (Response::SessionReset, false)
        }
        Request::Shutdown => (Response::Bye, true),
    }
}

/// Spawns the zero-copy ring worker when both ring paths are configured. It
/// polls the request ring for binary render requests, runs them through the
/// same validated submit/drain path as JSONL, and writes the raw encoded batch
/// to the response ring. The JSONL control plane (prepare/transition/flush)
/// remains unchanged.
fn spawn_ring_worker(
    renderer: Arc<Mutex<Option<RenderState>>>,
    request_path: String,
    response_path: String,
    capacity: usize,
    slot_bytes: usize,
) {
    thread::spawn(move || {
        let mut request_ring =
            match shared_ring::SharedRing::open(&request_path, capacity, slot_bytes) {
                Ok(ring) => ring,
                Err(e) => {
                    eprintln!("[gpu-ring] open request ring: {e}");
                    return;
                }
            };
        let mut response_ring =
            match shared_ring::SharedRing::open(&response_path, capacity, slot_bytes) {
                Ok(ring) => ring,
                Err(e) => {
                    eprintln!("[gpu-ring] open response ring: {e}");
                    return;
                }
            };
        loop {
            let Some(msg) = request_ring.try_pop() else {
                // Tight busy-wait: the compositord is a dedicated renderer
                // process, so burning the idle core is the accepted price for
                // sub-µs request pickup. yield_now() would hand the quantum to
                // the OS scheduler and add ~10-15ms per window on Windows.
                std::hint::spin_loop();
                continue;
            };
            let Some(req) = ring_transport::decode_render_request(&msg) else {
                eprintln!("[gpu-ring] malformed render request");
                continue;
            };
            let mut guard = match renderer.lock() {
                Ok(guard) => guard,
                Err(_) => return,
            };
            let assets_hash = guard
                .as_ref()
                .map(|state| state.playlist.assets_hash.clone())
                .unwrap_or_default();
            let request = Request::RenderTimelineBatch {
                batch_id: req.batch_id,
                epoch: req.epoch,
                assets_hash,
                timeline_version: req.timeline_version,
                timeline: None,
                frame_indices: Some(req.frame_indices),
                transition: None,
                submit_only: false,
                output: crate::contracts::OutputFormat::H264Bitstream,
            };
            let (response, _poisoned) = response_for(request, &mut *guard);
            match response {
                Response::EncodedTimelineBatch { frames, .. } => {
                    let batch = ring_transport::EncodedBatch {
                        batch_id: req.batch_id,
                        epoch: req.epoch,
                        timeline_version: req.timeline_version,
                        frames: ring_transport::encoded_frames_to_ring(&frames),
                    };
                    let bytes = ring_transport::encode_encoded_batch(&batch);
                    if !response_ring.try_push(&bytes) {
                        eprintln!(
                            "[gpu-ring] response ring full; dropped batch {}",
                            req.batch_id
                        );
                    }
                }
                Response::Error { code, message } => {
                    eprintln!(
                        "[gpu-ring] batch {} render error {code}: {message}",
                        req.batch_id
                    );
                }
                other => {
                    eprintln!(
                        "[gpu-ring] unexpected response for batch {}: {:?}",
                        req.batch_id, other
                    );
                }
            }
        }
    });
}

fn main() -> io::Result<()> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.iter().any(|arg| arg == "--version" || arg == "-V") {
        println!("playlist-compositord {}", env!("CARGO_PKG_VERSION"));
        return Ok(());
    }
    if args
        .first()
        .is_some_and(|arg| arg == "--export-draft-shader-yuv")
    {
        return music_v2_shader_export::run(&args[1..]).map_err(io::Error::other);
    }
    if args
        .first()
        .is_some_and(|arg| arg == "--export-draft-shader-nvenc")
    {
        return music_v2_shader_export::run(&args[1..]).map_err(io::Error::other);
    }
    if args.first().is_some_and(|arg| arg == "--probe-d3d12-nvenc") {
        let config = native_nvenc::parse_probe_args(&args[1..]).map_err(io::Error::other)?;
        return native_nvenc::run_probe(&config).map_err(io::Error::other);
    }
    let stdin = io::stdin();
    let mut stdout = io::BufWriter::new(io::stdout());
    let renderer = Arc::new(Mutex::new(None::<RenderState>));
    if let (Ok(request_path), Ok(response_path)) = (
        std::env::var("IMAGEPAD_PLAYLIST_RING_REQUEST"),
        std::env::var("IMAGEPAD_PLAYLIST_RING_RESPONSE"),
    ) {
        let capacity = std::env::var("IMAGEPAD_PLAYLIST_RING_CAPACITY")
            .ok()
            .and_then(|v| v.parse::<usize>().ok())
            .unwrap_or(8);
        let slot_bytes = std::env::var("IMAGEPAD_PLAYLIST_RING_SLOT_BYTES")
            .ok()
            .and_then(|v| v.parse::<usize>().ok())
            .unwrap_or(1 << 20);
        spawn_ring_worker(
            renderer.clone(),
            request_path,
            response_path,
            capacity,
            slot_bytes,
        );
    }
    for line in stdin.lock().lines() {
        let line = line?;
        if line.trim().is_empty() {
            continue;
        }
        if line.len() > protocol::MAX_H264_BATCH_REQUEST_BYTES {
            let response = Response::Error {
                code: "request_too_large".into(),
                message: format!(
                    "JSONL request exceeds {} bytes",
                    protocol::MAX_H264_BATCH_REQUEST_BYTES
                ),
            };
            writeln!(
                stdout,
                "{}",
                encode(&response).expect("response serializes")
            )?;
            stdout.flush()?;
            continue;
        }
        let response = match decode_request(&line) {
            Ok(req) => response_for(req, &mut *renderer.lock().unwrap()).0,
            Err(e) => Response::Error {
                code: "invalid_request".into(),
                message: e.to_string(),
            },
        };
        writeln!(
            stdout,
            "{}",
            encode(&response).expect("response serializes")
        )?;
        stdout.flush()?;
        if matches!(response, Response::Bye) {
            break;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reconstruct_timeline_chunk_looks_up_stored_frames_by_index() {
        let mut cache = PlaylistSessionCache::default();
        let frame = |index: u64| protocol::FrameControl {
            sequence: index,
            frame_index: index,
            pts_ns: index as i64,
            feature_index: 0,
            scroll_profile: String::new(),
            spectrum_q16: vec![0; 24],
            waveform_q16: vec![],
            progress_q16: 0,
            text_alpha_q16: u16::MAX,
            black_alpha_q16: 0,
        };
        cache.stored_timeline = Some(protocol::TimelineChunk {
            scroll_profiles: vec![],
            frames: (0..16).map(frame).collect(),
        });

        let chunk = reconstruct_playlist_timeline_chunk(
            &cache,
            None,
            Some(vec![8, 9, 10, 11, 12, 13, 14, 15]),
        )
        .unwrap();
        assert_eq!(chunk.frames.len(), 8);
        assert_eq!(
            chunk
                .frames
                .iter()
                .map(|f| f.frame_index)
                .collect::<Vec<_>>(),
            vec![8, 9, 10, 11, 12, 13, 14, 15]
        );

        // Inline path still passes through unchanged.
        let inline = protocol::TimelineChunk {
            scroll_profiles: vec![],
            frames: vec![frame(0)],
        };
        let back = reconstruct_playlist_timeline_chunk(&cache, Some(inline.clone()), None).unwrap();
        assert_eq!(back, inline);

        // Missing frame index fails closed.
        assert!(reconstruct_playlist_timeline_chunk(&cache, None, Some(vec![99])).is_err());
        // Both sources at once is rejected.
        assert!(reconstruct_playlist_timeline_chunk(&cache, Some(inline), Some(vec![0])).is_err());
    }

    #[test]
    fn mismatch_is_rejected() {
        let (r, _) = response_for(
            Request::Hello {
                version: 99,
                session: "x".into(),
            },
            &mut None,
        );
        assert!(matches!(r, Response::Error { code, .. } if code == "protocol_version_mismatch"));
    }

    #[test]
    fn v2_valid_job_stops_only_at_shader_readiness_gate() {
        let (response, shutdown) = response_for(
            Request::RenderV2 {
                width: 1280,
                height: 720,
                sequence: 1,
                pts_ns: 33_333_333,
                job: crate::music_v2_contract::Job {
                    width: 1280,
                    height: 720,
                    fps: 30,
                    artwork_mode: crate::music_v2_contract::ArtworkMode::Fallback,
                    layers: crate::music_v2_contract::REQUIRED_LOGICAL_LAYERS
                        .iter()
                        .map(|name| crate::music_v2_contract::LayerReceipt {
                            name: (*name).into(),
                            provider: crate::music_v2_contract::Provider::NativeGpu,
                            input_hash: String::new(),
                            shader_hash: String::new(),
                        })
                        .collect(),
                },
                output: crate::contracts::OutputFormat::Yuv420p,
            },
            &mut None,
        );
        assert!(!shutdown);
        assert!(matches!(
            response,
            Response::Error { code, .. } if code == "music_render_v2_shader_not_ready"
        ));
    }

    #[test]
    fn empty_h264_batch_is_rejected_before_renderer_initialization() {
        let (response, shutdown) = response_for(
            Request::RenderBatch {
                batch_id: 10,
                width: 640,
                height: 360,
                frames: Vec::new(),
                output: crate::contracts::OutputFormat::H264Bitstream,
            },
            &mut Some(RenderState {
                renderer: None,
                h264: None,
                ready: true,
                playlist: PlaylistSessionCache::default(),
            }),
        );
        assert!(!shutdown);
        assert!(matches!(
            response,
            Response::BatchError { batch_id, code, .. }
                if batch_id == 10 && code == "invalid_h264_batch_size"
        ));
    }

    #[test]
    fn playlist_session_rejects_stale_epoch_and_poisons_after_contract_failure() {
        let mut renderer = Some(RenderState {
            renderer: None,
            h264: None,
            ready: true,
            playlist: PlaylistSessionCache::default(),
        });
        let assets = protocol::TrackAssets {
            schema: 1,
            track_id: "track-a".into(),
            assets_hash: "a".repeat(64),
            artwork: Some(crate::contracts::ArtworkMetadata {
                texture_id: "artwork".into(),
                width: 1,
                height: 1,
                row_stride: 4,
                format: crate::contracts::PixelFormat::Rgba8,
                color_space: crate::contracts::ColorSpace::Srgb,
                alpha: true,
                payload: vec![0; 4],
                asset_hash: "a".repeat(64),
            }),
            glyph_atlas: Some(crate::contracts::GlyphAtlasMetadata {
                texture_id: "glyph".into(),
                font_family: "sans".into(),
                font_weight: 400,
                fallback_order: vec![],
                width: 1,
                height: 1,
                row_stride: 4,
                glyph_count: 1,
                missing_glyph_id: "tofu".into(),
                payload: vec![0; 4],
                glyphs: vec![],
                text_runs: vec![],
                asset_hash: "b".repeat(64),
            }),
            text_overlay: None,
            base_texture: None,
            waveform_texture: None,
            loudness_texture: None,
            spectrum_texture: None,
            layout: Default::default(),
            palette: Default::default(),
            loudness_envelope: vec![],
            loudness_trend: vec![],
            loudness_guides: [0; 4],
            duration_frames: 12,
        };
        let (response, _) = response_for(
            Request::PrepareTrack {
                epoch: 4,
                track_id: "track-a".into(),
                width: 640,
                height: 360,
                fps: 30,
                assets,
            },
            &mut renderer,
        );
        assert!(matches!(response, Response::TrackReady { epoch: 4, .. }));

        let timeline = protocol::TimelineChunk {
            scroll_profiles: vec![],
            frames: vec![protocol::FrameControl {
                sequence: 0,
                frame_index: 0,
                pts_ns: 0,
                feature_index: 0,
                scroll_profile: String::new(),
                spectrum_q16: vec![0; 24],
                waveform_q16: vec![],
                progress_q16: 0,
                text_alpha_q16: u16::MAX,
                black_alpha_q16: 0,
            }],
        };
        let (response, _) = response_for(
            Request::RenderTimelineBatch {
                batch_id: 1,
                epoch: 3,
                assets_hash: "a".repeat(64),
                timeline_version: 1,
                timeline: Some(timeline.clone()),
                frame_indices: None,
                transition: None,
                submit_only: false,
                output: crate::contracts::OutputFormat::H264Bitstream,
            },
            &mut renderer,
        );
        assert!(matches!(
            response,
            Response::BatchError { code, .. } if code == "playlist_asset_epoch_mismatch"
        ));

        let (response, _) = response_for(
            Request::RenderTimelineBatch {
                batch_id: 2,
                epoch: 4,
                assets_hash: "a".repeat(64),
                timeline_version: 1,
                timeline: Some(timeline),
                frame_indices: None,
                transition: None,
                submit_only: false,
                output: crate::contracts::OutputFormat::H264Bitstream,
            },
            &mut renderer,
        );
        assert!(matches!(
            response,
            Response::BatchError { code, .. } if code == "playlist_session_poisoned"
        ));
    }

    #[test]
    fn playlist_timeline_accepts_bounded_chunks_with_same_timeline_version() {
        let assets = protocol::TrackAssets {
            schema: 1,
            track_id: "track".into(),
            assets_hash: "a".repeat(64),
            artwork: None,
            glyph_atlas: None,
            text_overlay: None,
            base_texture: None,
            waveform_texture: None,
            loudness_texture: None,
            spectrum_texture: None,
            layout: Default::default(),
            palette: Default::default(),
            loudness_envelope: vec![],
            loudness_trend: vec![],
            loudness_guides: [0; 4],
            duration_frames: 4,
        };
        let mut state = RenderState {
            renderer: None,
            h264: None,
            ready: true,
            playlist: PlaylistSessionCache {
                epoch: 1,
                track_id: "track".into(),
                assets_hash: "a".repeat(64),
                assets: Some(assets),
                ..Default::default()
            },
        };
        let chunk = |sequence: u64| protocol::TimelineChunk {
            scroll_profiles: vec![protocol::ScrollProfile {
                id: "scroll".into(),
                fps: 30,
                loop_frames: 1,
                x_q16: vec![0],
                y_q16: vec![0],
                alpha_q16: vec![u16::MAX],
                clip: Default::default(),
                profile_hash: "b".repeat(64),
            }],
            frames: vec![protocol::FrameControl {
                sequence,
                frame_index: sequence,
                pts_ns: sequence as i64 * 33_333_333,
                feature_index: 0,
                scroll_profile: "scroll".into(),
                spectrum_q16: vec![0; 24],
                waveform_q16: vec![],
                progress_q16: 0,
                text_alpha_q16: u16::MAX,
                black_alpha_q16: 0,
            }],
        };

        let first = chunk(0);
        assert!(validate_playlist_timeline_batch(
            &mut state,
            0,
            1,
            "a".repeat(64).as_str(),
            1,
            &first,
            None,
            crate::contracts::OutputFormat::H264Bitstream,
        )
        .is_ok());
        commit_playlist_timeline_batch(&mut state.playlist, 0, 1, &first);

        let second = chunk(1);
        assert!(validate_playlist_timeline_batch(
            &mut state,
            1,
            1,
            "a".repeat(64).as_str(),
            1,
            &second,
            None,
            crate::contracts::OutputFormat::H264Bitstream,
        )
        .is_ok());
        commit_playlist_timeline_batch(&mut state.playlist, 1, 1, &second);

        let third = chunk(2);
        state.playlist.pending_batch = Some(PendingPlaylistBatch {
            batch_id: 1,
            epoch: 1,
            timeline_version: 1,
            timeline: second.clone(),
        });
        assert!(
            validate_playlist_timeline_batch(
                &mut state,
                2,
                1,
                "a".repeat(64).as_str(),
                1,
                &third,
                None,
                crate::contracts::OutputFormat::H264Bitstream,
            )
            .is_err(),
            "a new timeline batch was accepted while the previous batch was pending drain"
        );
        state.playlist.pending_batch = None;
        assert!(
            validate_playlist_timeline_batch(
                &mut state,
                3,
                1,
                "a".repeat(64).as_str(),
                1,
                &third,
                None,
                crate::contracts::OutputFormat::H264Bitstream,
            )
            .is_err(),
            "batch id gaps must be rejected even when frame metadata is contiguous"
        );
    }

    #[test]
    fn playlist_pending_batch_commits_only_after_drain_identity_matches() {
        let chunk = |sequence: u64| protocol::TimelineChunk {
            scroll_profiles: vec![protocol::ScrollProfile {
                id: "scroll".into(),
                fps: 30,
                loop_frames: 1,
                x_q16: vec![0],
                y_q16: vec![0],
                alpha_q16: vec![u16::MAX],
                clip: Default::default(),
                profile_hash: "b".repeat(64),
            }],
            frames: vec![protocol::FrameControl {
                sequence,
                frame_index: sequence,
                pts_ns: sequence as i64 * 33_333_333,
                feature_index: 0,
                scroll_profile: "scroll".into(),
                spectrum_q16: vec![0; 24],
                waveform_q16: vec![],
                progress_q16: 0,
                text_alpha_q16: u16::MAX,
                black_alpha_q16: 0,
            }],
        };
        let first = chunk(0);
        let second = chunk(1);
        let mut playlist = PlaylistSessionCache {
            epoch: 7,
            ..Default::default()
        };
        commit_playlist_timeline_batch(&mut playlist, 0, 1, &first);
        playlist.pending_batch = Some(PendingPlaylistBatch {
            batch_id: 1,
            epoch: 7,
            timeline_version: 1,
            timeline: second,
        });
        let before = (
            playlist.timeline_version,
            playlist.has_timeline_cursor,
            playlist.last_timeline_sequence,
            playlist.last_timeline_frame_index,
            playlist.last_timeline_pts_ns,
            playlist.next_timeline_batch_id,
        );

        assert!(commit_pending_playlist_timeline_batch(&mut playlist, 2, 7, 1).is_err());
        assert!(playlist.pending_batch.is_some());
        assert_eq!(
            before,
            (
                playlist.timeline_version,
                playlist.has_timeline_cursor,
                playlist.last_timeline_sequence,
                playlist.last_timeline_frame_index,
                playlist.last_timeline_pts_ns,
                playlist.next_timeline_batch_id,
            )
        );

        assert!(commit_pending_playlist_timeline_batch(&mut playlist, 1, 7, 1).is_ok());
        assert!(playlist.pending_batch.is_none());
        assert_eq!(playlist.timeline_version, 1);
        assert_eq!(playlist.last_timeline_sequence, 1);
        assert_eq!(playlist.last_timeline_frame_index, 1);
        assert_eq!(playlist.next_timeline_batch_id, 2);
    }

    #[test]
    fn playlist_timeline_accepts_three_bounded_chunks_with_same_timeline_version() {
        let mut state = RenderState {
            renderer: None,
            h264: None,
            ready: true,
            playlist: PlaylistSessionCache {
                epoch: 1,
                track_id: "track".into(),
                assets_hash: "a".repeat(64),
                assets: Some(protocol::TrackAssets {
                    schema: 1,
                    track_id: "track".into(),
                    assets_hash: "a".repeat(64),
                    artwork: None,
                    glyph_atlas: None,
                    text_overlay: None,
                    base_texture: None,
                    waveform_texture: None,
                    loudness_texture: None,
                    spectrum_texture: None,
                    layout: Default::default(),
                    palette: Default::default(),
                    loudness_envelope: vec![],
                    loudness_trend: vec![],
                    loudness_guides: [0; 4],
                    duration_frames: 24,
                }),
                ..Default::default()
            },
        };
        let chunk = |start_sequence: u64| protocol::TimelineChunk {
            scroll_profiles: vec![protocol::ScrollProfile {
                id: "scroll".into(),
                fps: 30,
                loop_frames: 1,
                x_q16: vec![0],
                y_q16: vec![0],
                alpha_q16: vec![u16::MAX],
                clip: Default::default(),
                profile_hash: "b".repeat(64),
            }],
            frames: (0..protocol::MAX_H264_BATCH_FRAMES)
                .map(|offset| {
                    let sequence = start_sequence + offset as u64;
                    protocol::FrameControl {
                        sequence,
                        frame_index: sequence,
                        pts_ns: sequence as i64 * 33_333_333,
                        feature_index: 0,
                        scroll_profile: "scroll".into(),
                        spectrum_q16: vec![0; 24],
                        waveform_q16: vec![],
                        progress_q16: 0,
                        text_alpha_q16: u16::MAX,
                        black_alpha_q16: 0,
                    }
                })
                .collect(),
        };

        for batch_id in 0..3 {
            let timeline = chunk(batch_id * protocol::MAX_H264_BATCH_FRAMES as u64);
            assert!(validate_playlist_timeline_batch(
                &mut state,
                batch_id,
                1,
                "a".repeat(64).as_str(),
                1,
                &timeline,
                None,
                crate::contracts::OutputFormat::H264Bitstream,
            )
            .is_ok());
            commit_playlist_timeline_batch(&mut state.playlist, batch_id, 1, &timeline);
        }
        let last = (3 * protocol::MAX_H264_BATCH_FRAMES - 1) as u64;
        assert_eq!(state.playlist.last_timeline_sequence, last);
        assert_eq!(state.playlist.last_timeline_frame_index, last);
        assert_eq!(state.playlist.next_timeline_batch_id, 3);

        let gap = chunk(24);
        assert!(validate_playlist_timeline_batch(
            &mut state,
            4,
            1,
            "a".repeat(64).as_str(),
            1,
            &gap,
            None,
            crate::contracts::OutputFormat::H264Bitstream,
        )
        .is_err());
    }

    #[test]
    fn playlist_prepare_rejects_missing_cpu_prepared_artwork_and_glyph_assets() {
        let mut renderer = Some(RenderState {
            renderer: None,
            h264: None,
            ready: true,
            playlist: PlaylistSessionCache::default(),
        });
        let (response, _) = response_for(
            Request::PrepareTrack {
                epoch: 1,
                track_id: "track-a".into(),
                width: 640,
                height: 360,
                fps: 30,
                assets: protocol::TrackAssets {
                    schema: 1,
                    track_id: "track-a".into(),
                    assets_hash: "a".repeat(64),
                    artwork: None,
                    glyph_atlas: None,
                    text_overlay: None,
                    base_texture: None,
                    waveform_texture: None,
                    loudness_texture: None,
                    spectrum_texture: None,
                    layout: Default::default(),
                    palette: Default::default(),
                    loudness_envelope: vec![],
                    loudness_trend: vec![],
                    loudness_guides: [0; 4],
                    duration_frames: 1,
                },
            },
            &mut renderer,
        );
        assert!(matches!(
            response,
            Response::Error { code, .. } if code == "invalid_playlist_track_assets"
        ));
    }
}
