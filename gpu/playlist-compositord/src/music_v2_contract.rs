use serde::{Deserialize, Serialize};

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub enum Provider {
    #[serde(rename = "NativeGPU")]
    NativeGpu,
    #[serde(rename = "GoldenUpload")]
    GoldenUpload,
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub enum ArtworkMode {
    #[serde(rename = "Source")]
    Source,
    #[serde(rename = "Fallback")]
    Fallback,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct LayerReceipt {
    pub name: String,
    pub provider: Provider,
    #[serde(default)]
    pub input_hash: String,
    #[serde(default)]
    pub shader_hash: String,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct Job {
    pub width: u32,
    pub height: u32,
    pub fps: u32,
    pub artwork_mode: ArtworkMode,
    pub layers: Vec<LayerReceipt>,
}

pub const REQUIRED_LOGICAL_LAYERS: [&str; 8] = [
    "background",
    "artwork",
    "spectrum",
    "waveform",
    "loudness",
    "text",
    "progress",
    "fade",
];

pub fn validate_production(job: &Job) -> Result<(), String> {
    if job.width == 0 || job.height == 0 || job.fps == 0 {
        return Err("invalid output geometry or fps".into());
    }
    if job.layers.len() != REQUIRED_LOGICAL_LAYERS.len() {
        return Err(format!(
            "expected exactly {} logical layers, got {}",
            REQUIRED_LOGICAL_LAYERS.len(),
            job.layers.len()
        ));
    }
    let mut seen = Vec::with_capacity(job.layers.len());
    for layer in &job.layers {
        if !REQUIRED_LOGICAL_LAYERS.contains(&layer.name.as_str()) {
            return Err(format!("unknown logical layer {}", layer.name));
        }
        if seen.iter().any(|name| name == &layer.name) {
            return Err(format!("duplicate logical layer {}", layer.name));
        }
        seen.push(layer.name.clone());
        if layer.name == "screen_rgba" {
            return Err(format!(
                "CPU final raster is forbidden for layer {}",
                layer.name
            ));
        }
        if layer.provider != Provider::NativeGpu {
            return Err(format!(
                "non-native provider is forbidden for layer {}",
                layer.name
            ));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_cpu_final_raster() {
        let job = Job {
            width: 1280,
            height: 720,
            fps: 30,
            artwork_mode: ArtworkMode::Fallback,
            layers: vec![LayerReceipt {
                name: "screen_rgba".into(),
                provider: Provider::GoldenUpload,
                input_hash: String::new(),
                shader_hash: String::new(),
            }],
        };
        assert!(validate_production(&job).is_err());
    }

    #[test]
    fn rejects_partial_native_layer_manifest() {
        let job = Job {
            width: 1280,
            height: 720,
            fps: 30,
            artwork_mode: ArtworkMode::Source,
            layers: vec![LayerReceipt {
                name: "background".into(),
                provider: Provider::NativeGpu,
                input_hash: String::new(),
                shader_hash: String::new(),
            }],
        };
        assert!(validate_production(&job).is_err());
    }

    #[test]
    fn uses_go_contract_provider_names() {
        let json = serde_json::to_string(&Provider::NativeGpu).unwrap();
        assert_eq!(json, "\"NativeGPU\"");
        let json = serde_json::to_string(&ArtworkMode::Fallback).unwrap();
        assert_eq!(json, "\"Fallback\"");
    }
}
