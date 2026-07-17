use crate::contracts::{
    GpuFrame, MusicScenePayload, OutputCapabilities, OutputFormat, Yuv420pFrame,
};
use crate::music_v2_contract::Job as MusicRenderV2Job;
use serde::{Deserialize, Serialize};

pub const PROTOCOL_VERSION: u16 = 1;

#[derive(Debug, Serialize, Deserialize, PartialEq)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Request {
    Hello {
        version: u16,
        session: String,
    },
    Health,
    Shutdown,
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
    Error {
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
    }
}
