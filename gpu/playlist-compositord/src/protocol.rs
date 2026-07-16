use crate::contracts::{GpuFrame, MusicScenePayload};
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
        assert!(matches!(request, Request::Render { scene: None, .. }));
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
                    rms_q15: 1,
                    peak_q15: 2,
                },
                artwork: None,
                base_texture: None,
                waveform_texture: None,
                loudness_texture: None,
                glyph_atlas: None,
                text_overlay: None,
                layout: Default::default(),
                dynamics: Default::default(),
                palette: Default::default(),
                fingerprint: String::new(),
            }),
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
}
