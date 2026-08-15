use crate::contracts::{
    ArtworkMetadata, BaseTextureMetadata, GlyphAtlasMetadata, GpuFrame, MusicSceneLayout,
    MusicScenePalette, MusicScenePayload, OutputCapabilities, OutputFormat, SceneRect,
    TextOverlayMetadata, Yuv420pFrame,
};
use crate::music_v2_contract::Job as MusicRenderV2Job;
use serde::{Deserialize, Serialize};

pub const PROTOCOL_VERSION: u16 = 1;
pub const MAX_H264_BATCH_FRAMES: usize = 64;
pub const MAX_H264_BATCH_REQUEST_BYTES: usize = 128 * 1024 * 1024;

#[derive(Debug, Serialize, Deserialize, PartialEq)]
pub struct RenderBatchFrame {
    pub sequence: u64,
    pub pts_ns: i64,
    pub scene: MusicScenePayload,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TrackAssets {
    pub schema: u16,
    pub track_id: String,
    pub assets_hash: String,
    #[serde(default)]
    pub artwork: Option<ArtworkMetadata>,
    #[serde(default)]
    pub glyph_atlas: Option<GlyphAtlasMetadata>,
    #[serde(default)]
    pub text_overlay: Option<TextOverlayMetadata>,
    #[serde(default)]
    pub base_texture: Option<BaseTextureMetadata>,
    #[serde(default)]
    pub waveform_texture: Option<BaseTextureMetadata>,
    #[serde(default)]
    pub loudness_texture: Option<BaseTextureMetadata>,
    #[serde(default)]
    pub spectrum_texture: Option<BaseTextureMetadata>,
    #[serde(default)]
    pub layout: MusicSceneLayout,
    #[serde(default)]
    pub palette: MusicScenePalette,
    #[serde(default)]
    pub loudness_envelope: Vec<u16>,
    #[serde(default)]
    pub loudness_trend: Vec<u16>,
    #[serde(default)]
    pub loudness_guides: [u16; 4],
    pub duration_frames: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ScrollProfile {
    pub id: String,
    pub fps: u32,
    pub loop_frames: u64,
    pub x_q16: Vec<i32>,
    pub y_q16: Vec<i32>,
    pub alpha_q16: Vec<u16>,
    #[serde(default)]
    pub clip: SceneRect,
    pub profile_hash: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FrameControl {
    pub sequence: u64,
    pub frame_index: u64,
    pub pts_ns: i64,
    pub feature_index: u32,
    #[serde(default)]
    pub scroll_profile: String,
    pub spectrum_q16: Vec<u16>,
    #[serde(default)]
    pub waveform_q16: Vec<u16>,
    pub progress_q16: u16,
    pub text_alpha_q16: u16,
    pub black_alpha_q16: u16,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TimelineChunk {
    pub scroll_profiles: Vec<ScrollProfile>,
    pub frames: Vec<FrameControl>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AudioFadePlan {
    pub start_pts_ns: i64,
    pub duration_ns: i64,
    pub curve: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TransitionPlan {
    pub schema: u16,
    pub plan_version: u64,
    pub epoch: u64,
    pub reason: String,
    pub source_track_id: String,
    #[serde(default)]
    pub target_track_id: String,
    pub playback_head_sequence: u64,
    pub playback_head_pts_ns: i64,
    pub queued_tail_sequence: u64,
    pub queued_tail_pts_ns: i64,
    pub fade_start_sequence: u64,
    pub fade_start_pts_ns: i64,
    pub fade_end_sequence: u64,
    pub fade_end_pts_ns: i64,
    pub duration_frames: u64,
    pub curve: String,
    pub audio_fade_plan: AudioFadePlan,
}

#[derive(Debug, Serialize, Deserialize, PartialEq)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Request {
    Hello {
        version: u16,
        session: String,
    },
    Health,
    /// Drain the direct encoder before the client closes the elementary-stream
    /// pipe. The response is emitted only after the last output fence is ready.
    Flush,
    Shutdown,
    /// Clear the per-session playlist state (track, timeline, pending frames)
    /// while keeping the D3D12 device, compiled pipeline, and NVENC encoder so
    /// the ~2.3s initialization is paid once per process instead of per session.
    ResetSession,
    Render {
        width: u32,
        height: u32,
        sequence: u64,
        pts_ns: i64,
        #[serde(default)]
        scene: Option<MusicScenePayload>,
        /// Requested transport format. Omitted by legacy callers and therefore
        /// defaults to the existing packed RGBA response.
        #[serde(default)]
        output: OutputFormat,
    },
    /// Bounded direct-H264 batch. The first implementation keeps the
    /// compressed response atomic while preserving per-frame sequence/PTS
    /// ordering; the renderer may still submit individual frames internally.
    RenderBatch {
        batch_id: u64,
        width: u32,
        height: u32,
        frames: Vec<RenderBatchFrame>,
        #[serde(default)]
        output: OutputFormat,
    },
    PrepareTrack {
        epoch: u64,
        track_id: String,
        width: u32,
        height: u32,
        fps: u32,
        assets: TrackAssets,
    },
    /// Upload the full immutable per-track timeline once. Subsequent
    /// RenderTimelineBatch requests reference frames by frame_index instead of
    /// re-sending the per-frame controls (spectrum/waveform/scroll/fades).
    PrepareTimeline {
        epoch: u64,
        track_id: String,
        assets_hash: String,
        timeline_version: u64,
        timeline: TimelineChunk,
    },
    RenderTimelineBatch {
        batch_id: u64,
        epoch: u64,
        assets_hash: String,
        timeline_version: u64,
        /// Inline bounded chunk (legacy path). Omitted when the batch
        /// references a prepared timeline via `frame_indices`.
        #[serde(default)]
        timeline: Option<TimelineChunk>,
        /// Frame indices of the stored timeline to render, in order. When
        /// present, `timeline` must be None and a matching PrepareTimeline
        /// must have been admitted for this timeline_version.
        #[serde(default)]
        frame_indices: Option<Vec<u64>>,
        #[serde(default)]
        transition: Option<TransitionPlan>,
        #[serde(default)]
        submit_only: bool,
        #[serde(default)]
        output: OutputFormat,
    },
    DrainTimelineBatch {
        batch_id: u64,
        epoch: u64,
        timeline_version: u64,
    },
    /// V2 ownership-contract request. The rendering payload is intentionally
    /// separate from the legacy scene so CPU-final rasters cannot be reused.
    RenderV2 {
        width: u32,
        height: u32,
        sequence: u64,
        pts_ns: i64,
        job: MusicRenderV2Job,
        #[serde(default)]
        output: OutputFormat,
    },
}

#[derive(Debug, Serialize, Deserialize, PartialEq)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Response {
    HelloAck {
        version: u16,
        session: String,
        adapter: String,
        backend: String,
        toolchain: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        outputs: Option<OutputCapabilities>,
    },
    Health {
        ready: bool,
        protocol: u16,
    },
    Flushed,
    SessionReset,
    Error {
        code: String,
        message: String,
    },
    /// A batch is atomic. No encoded frames are returned when one frame
    /// fails; the client must discard the batch and retry in a fresh session.
    BatchError {
        batch_id: u64,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        failed_sequence: Option<u64>,
        code: String,
        message: String,
    },
    Bye,
    Frame {
        frame: GpuFrame,
    },
    /// Additive planar-output response. It is decoded by clients that
    /// negotiate YUV420P; legacy Frame responses remain unchanged.
    YuvFrame {
        frame: Yuv420pFrame,
    },
    /// Compressed H.264 access unit from the direct D3D12/NVENC route. The
    /// payload is CPU-readable compressed data; rendered pixels never cross
    /// the boundary.
    EncodedFrame {
        frame: crate::contracts::EncodedH264Frame,
    },
    EncodedBatch {
        batch_id: u64,
        frames: Vec<crate::contracts::EncodedH264Frame>,
    },
    TrackReady {
        epoch: u64,
        assets_hash: String,
    },
    EncodedTimelineBatch {
        batch_id: u64,
        epoch: u64,
        timeline_version: u64,
        #[serde(default)]
        applied_transition_version: Option<u64>,
        frames: Vec<crate::contracts::EncodedH264Frame>,
    },
    TimelineSubmitted {
        batch_id: u64,
        epoch: u64,
        timeline_version: u64,
    },
    TimelinePrepared {
        epoch: u64,
        timeline_version: u64,
        frame_count: u64,
    },
    /// GPU-owned planar output. No CPU plane payload is serialized.
    GpuYuvHandle {
        frame: crate::gpu_yuv_transport::GpuYuvFrameHandle,
    },
}

pub fn encode<T: Serialize>(value: &T) -> Result<String, serde_json::Error> {
    serde_json::to_string(value)
}
pub fn decode_request(line: &str) -> Result<Request, serde_json::Error> {
    serde_json::from_str(line)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn legacy_render_without_scene_remains_compatible() {
        let request =
            decode_request(r#"{"type":"render","width":64,"height":64,"sequence":1,"pts_ns":0}"#)
                .unwrap();
        assert!(matches!(
            request,
            Request::Render {
                scene: None,
                output: OutputFormat::Rgba8,
                ..
            }
        ));
    }

    #[test]
    fn scene_payload_round_trips_and_rejects_bad_schema() {
        let request = Request::Render {
            width: 64,
            height: 64,
            sequence: 1,
            pts_ns: 0,
            scene: Some(MusicScenePayload {
                schema: crate::contracts::MUSIC_SCENE_SCHEMA,
                feature: crate::contracts::AudioFeatureFrame {
                    schema: 1,
                    sample_rate_hz: 48000,
                    frame_index: 0,
                    pts_ns: 0,
                    spectrum_q16: vec![1, 2],
                    fingerprint_q16: vec![],
                    waveform_q16: vec![],
                    rms_q15: 1,
                    peak_q15: 2,
                },
                pcm_f32le: vec![],
                artwork: None,
                base_texture: None,
                waveform_texture: None,
                loudness_texture: None,
                spectrum_texture: None,
                glyph_atlas: None,
                text_overlay: None,
                layout: Default::default(),
                dynamics: Default::default(),
                palette: Default::default(),
                fingerprint: String::new(),
            }),
            output: OutputFormat::Rgba8,
        };
        let encoded = encode(&request).unwrap();
        let decoded = decode_request(&encoded).unwrap();
        if let Request::Render {
            scene: Some(scene), ..
        } = decoded
        {
            assert!(scene.validate().is_ok());
        } else {
            panic!("scene was lost");
        }

        let bad = r#"{"type":"render","width":64,"height":64,"sequence":1,"pts_ns":0,"scene":{"schema":9,"feature":{"schema":1,"sample_rate_hz":48000,"frame_index":0,"pts_ns":0,"spectrum_q16":[],"rms_q15":0,"peak_q15":0}}}"#;
        if let Request::Render {
            scene: Some(scene), ..
        } = decode_request(bad).unwrap()
        {
            assert!(scene.validate().is_err());
        } else {
            panic!("scene missing");
        }
    }

    #[test]
    fn go_base64_scene_payload_decodes_and_renders_request() {
        let line = r#"{"type":"render","width":64,"height":64,"sequence":1,"pts_ns":0,"scene":{"schema":1,"feature":{"schema":1,"sample_rate_hz":48000,"frame_index":0,"pts_ns":0,"spectrum_q16":[],"rms_q15":0,"peak_q15":0},"artwork":{"texture_id":"cover","width":1,"height":1,"row_stride":256,"format":"Rgba8","color_space":"Srgb","alpha":true,"payload":"AQID","asset_hash":""}}}"#;
        let request = decode_request(line).unwrap();
        let Request::Render {
            scene: Some(scene), ..
        } = request
        else {
            panic!("scene missing")
        };
        assert_eq!(scene.artwork.unwrap().payload, vec![1, 2, 3]);
    }

    #[test]
    fn yuv_frame_response_round_trips_without_changing_legacy_frame() {
        let response = Response::YuvFrame {
            frame: Yuv420pFrame {
                schema: crate::contracts::CONTRACT_VERSION,
                sequence: 7,
                pts_ns: 123,
                width: 3,
                height: 2,
                y_stride: 3,
                u_stride: 2,
                v_stride: 2,
                color_space: crate::contracts::ColorSpace::Srgb,
                ownership: crate::contracts::Ownership::OwnedByTransport,
                y: vec![16, 32, 48, 64, 80, 96],
                u: vec![128, 129],
                v: vec![130, 131],
            },
        };
        let encoded = encode(&response).unwrap();
        assert!(encoded.contains(r#""type":"yuv_frame""#));
        let decoded: Response = serde_json::from_str(&encoded).unwrap();
        assert_eq!(decoded, response);
        if let Response::YuvFrame { frame } = decoded {
            assert!(frame.validate().is_ok());
        } else {
            panic!("response variant changed");
        }

        let legacy = Response::Frame {
            frame: GpuFrame {
                schema: crate::contracts::CONTRACT_VERSION,
                sequence: 1,
                pts_ns: 0,
                width: 1,
                height: 1,
                row_stride: 256,
                format: crate::contracts::PixelFormat::Rgba8,
                color_space: crate::contracts::ColorSpace::Srgb,
                alpha: true,
                ownership: crate::contracts::Ownership::OwnedByTransport,
                payload: vec![0, 0, 0, 255],
                glyph_atlas_receipt: None,
                text_overlay_receipt: None,
                artwork_receipt: None,
                base_texture_receipt: None,
                glyph_diagnostics: None,
            },
        };
        let legacy_decoded: Response = serde_json::from_str(&encode(&legacy).unwrap()).unwrap();
        assert_eq!(legacy_decoded, legacy);
    }

    #[test]
    fn render_output_format_is_optional_and_round_trips() {
        let legacy =
            decode_request(r#"{"type":"render","width":2,"height":2,"sequence":3,"pts_ns":4}"#)
                .unwrap();
        assert!(matches!(
            legacy,
            Request::Render {
                output: OutputFormat::Rgba8,
                ..
            }
        ));
        let yuv = Request::Render {
            width: 2,
            height: 2,
            sequence: 3,
            pts_ns: 4,
            scene: None,
            output: OutputFormat::Yuv420p,
        };
        let decoded = decode_request(&encode(&yuv).unwrap()).unwrap();
        assert_eq!(decoded, yuv);

        let v2 = Request::RenderV2 {
            width: 1280,
            height: 720,
            sequence: 1,
            pts_ns: 33_333_333,
            job: crate::music_v2_contract::Job {
                width: 1280,
                height: 720,
                fps: 30,
                artwork_mode: crate::music_v2_contract::ArtworkMode::Fallback,
                layers: vec![crate::music_v2_contract::LayerReceipt {
                    name: "background".into(),
                    provider: crate::music_v2_contract::Provider::NativeGpu,
                    input_hash: String::new(),
                    shader_hash: String::new(),
                }],
            },
            output: OutputFormat::Rgba8,
        };
        let encoded = encode(&v2).unwrap();
        assert!(encoded.contains(r#""type":"render_v2""#));
        assert_eq!(decode_request(&encoded).unwrap(), v2);

        let gpu_yuv = Response::GpuYuvHandle {
            frame: crate::gpu_yuv_transport::GpuYuvFrameHandle {
                sequence: 1,
                pts_ns: 0,
                width: 1279,
                height: 719,
                y_stride: 1279,
                u_stride: 640,
                v_stride: 640,
                adapter: "test".into(),
                backend: "vulkan".into(),
            },
        };
        let encoded = encode(&gpu_yuv).unwrap();
        assert!(encoded.contains(r#""type":"gpu_yuv_handle""#));
        assert_eq!(serde_json::from_str::<Response>(&encoded).unwrap(), gpu_yuv);
    }

    #[test]
    fn h264_bitstream_response_round_trips_with_zero_pixel_readback() {
        let request = Request::Render {
            width: 640,
            height: 360,
            sequence: 7,
            pts_ns: 233_333_331,
            scene: None,
            output: OutputFormat::H264Bitstream,
        };
        let encoded = encode(&request).unwrap();
        assert!(encoded.contains(r#""output":"h264_bitstream""#));
        assert_eq!(decode_request(&encoded).unwrap(), request);

        let response = Response::EncodedFrame {
            frame: crate::contracts::EncodedH264Frame {
                schema: crate::contracts::CONTRACT_VERSION,
                sequence: 7,
                pts_ns: 233_333_331,
                width: 640,
                height: 360,
                codec: "h264".into(),
                profile: "High".into(),
                backend: "dx12".into(),
                pixel_readback_bytes: 0,
                asset_cache_ready: true,
                asset_receipt: crate::contracts::H264AssetReceipt {
                    artwork_hash: "a".repeat(64),
                    glyph_hash: "b".repeat(64),
                },
                payload: vec![0, 0, 0, 1, 9],
            },
        };
        let encoded = encode(&response).unwrap();
        assert!(encoded.contains(r#""type":"encoded_frame""#));
        let decoded: Response = serde_json::from_str(&encoded).unwrap();
        assert_eq!(decoded, response);
        if let Response::EncodedFrame { frame } = decoded {
            assert!(frame.validate().is_ok());
        } else {
            panic!("encoded frame response variant changed");
        }
    }

    #[test]
    fn playlist_track_and_timeline_requests_round_trip_without_pcm() {
        let assets = TrackAssets {
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
            loudness_envelope: vec![1, 2],
            loudness_trend: vec![3, 4],
            loudness_guides: [5, 6, 7, 8],
            duration_frames: 6,
        };
        let prepare = Request::PrepareTrack {
            epoch: 7,
            track_id: "track-a".into(),
            width: 640,
            height: 360,
            fps: 30,
            assets,
        };
        let encoded = encode(&prepare).unwrap();
        assert!(encoded.contains(r#""type":"prepare_track""#));
        assert_eq!(decode_request(&encoded).unwrap(), prepare);

        let batch = Request::RenderTimelineBatch {
            batch_id: 3,
            epoch: 7,
            assets_hash: "a".repeat(64),
            timeline_version: 1,
            timeline: Some(TimelineChunk {
                scroll_profiles: vec![ScrollProfile {
                    id: "default-scroll".into(),
                    fps: 30,
                    loop_frames: 1,
                    x_q16: vec![0],
                    y_q16: vec![0],
                    alpha_q16: vec![u16::MAX],
                    clip: Default::default(),
                    profile_hash: "b".repeat(64),
                }],
                frames: vec![FrameControl {
                    sequence: 0,
                    frame_index: 0,
                    pts_ns: 0,
                    feature_index: 0,
                    scroll_profile: "default-scroll".into(),
                    spectrum_q16: vec![0; 24],
                    waveform_q16: vec![],
                    progress_q16: 0,
                    text_alpha_q16: 0,
                    black_alpha_q16: u16::MAX,
                }],
            }),
            frame_indices: None,
            transition: None,
            submit_only: false,
            output: OutputFormat::H264Bitstream,
        };
        let encoded = encode(&batch).unwrap();
        assert!(!encoded.contains("pcm_f32le"));
        assert_eq!(decode_request(&encoded).unwrap(), batch);

        let submit = Request::RenderTimelineBatch {
            batch_id: 3,
            epoch: 7,
            assets_hash: "a".repeat(64),
            timeline_version: 1,
            timeline: match &batch {
                Request::RenderTimelineBatch { timeline, .. } => timeline.clone(),
                _ => unreachable!(),
            },
            frame_indices: None,
            transition: None,
            submit_only: true,
            output: OutputFormat::H264Bitstream,
        };
        let encoded = encode(&submit).unwrap();
        assert!(encoded.contains(r#""submit_only":true"#));
        assert_eq!(decode_request(&encoded).unwrap(), submit);

        let range = Request::RenderTimelineBatch {
            batch_id: 4,
            epoch: 7,
            assets_hash: "a".repeat(64),
            timeline_version: 1,
            timeline: None,
            frame_indices: Some(vec![0, 1, 2, 3, 4, 5, 6, 7]),
            transition: None,
            submit_only: false,
            output: OutputFormat::H264Bitstream,
        };
        let encoded = encode(&range).unwrap();
        assert!(encoded.contains(r#""frame_indices""#));
        assert!(!encoded.contains("spectrum_q16"));
        assert_eq!(decode_request(&encoded).unwrap(), range);

        let drain = Request::DrainTimelineBatch {
            batch_id: 3,
            epoch: 7,
            timeline_version: 1,
        };
        let encoded = encode(&drain).unwrap();
        assert!(encoded.contains(r#""type":"drain_timeline_batch""#));
        assert_eq!(decode_request(&encoded).unwrap(), drain);
    }

    #[test]
    fn playlist_timeline_batch_rejects_null_frame_sequence() {
        let raw = r#"{"type":"render_timeline_batch","batch_id":1,"epoch":1,"assets_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","timeline_version":1,"timeline":{"scroll_profiles":[],"frames":null},"output":"h264_bitstream"}"#;
        assert!(decode_request(raw).is_err());
    }
}
