use serde::{Deserialize, Serialize};

/// Go's encoding/json marshals []byte as a base64 JSON string, while a plain
/// serde Vec<u8> is an array of numbers. Accept both wire forms so sidecars
/// can interoperate with existing Go producers, but always emit Go-compatible
/// base64 strings.
mod base64_bytes {
    use base64::{engine::general_purpose::STANDARD, Engine as _};
    use serde::{de::Error, Deserialize, Deserializer, Serializer};

    pub fn serialize<S>(bytes: &[u8], serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        serializer.serialize_str(&STANDARD.encode(bytes))
    }

    pub fn deserialize<'de, D>(deserializer: D) -> Result<Vec<u8>, D::Error>
    where
        D: Deserializer<'de>,
    {
        #[derive(Deserialize)]
        #[serde(untagged)]
        enum Wire {
            Text(String),
            Bytes(Vec<u8>),
        }
        match Wire::deserialize(deserializer)? {
            Wire::Text(value) => STANDARD.decode(value.as_bytes()).map_err(D::Error::custom),
            Wire::Bytes(value) => Ok(value),
        }
    }
}

pub const CONTRACT_VERSION: u16 = 1;
pub const ROW_ALIGNMENT: u32 = 256;
pub const MAX_DIMENSION: u32 = 16_384;
pub const MAX_PAYLOAD_BYTES: usize = 256 * 1024 * 1024;
pub const MUSIC_SCENE_SCHEMA: u16 = 1;
pub const MUSIC_MAX_FEATURE_BINS: usize = 256;
pub const MUSIC_MAX_ARTWORK_DIMENSION: u32 = 4096;
pub const MUSIC_MAX_ARTWORK_BYTES: usize = 16 * 1024 * 1024;
pub const MUSIC_MAX_GLYPHS: u32 = 4096;
pub const MUSIC_MAX_TEXT_BYTES: usize = 64 * 1024;
pub const MUSIC_MAX_GLYPH_RUNS: usize = 256;
pub const MUSIC_MAX_LOUDNESS_SAMPLES: usize = 1000;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct MusicScenePayload {
    pub schema: u16,
    pub feature: AudioFeatureFrame,
    #[serde(default)]
    pub artwork: Option<ArtworkMetadata>,
    #[serde(default)]
    pub base_texture: Option<BaseTextureMetadata>,
    #[serde(default)]
    pub glyph_atlas: Option<GlyphAtlasMetadata>,
    #[serde(default)]
    pub text_overlay: Option<TextOverlayMetadata>,
    #[serde(default)]
    pub layout: MusicSceneLayout,
    #[serde(default)]
    pub dynamics: MusicSceneDynamics,
    #[serde(default)]
    pub palette: MusicScenePalette,
    #[serde(default)]
    pub fingerprint: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TextOverlayMetadata {
    pub title: String,
    pub artist: String,
    pub album: String,
    pub font_family: String,
    pub font_weight: u16,
    pub size_px: f32,
    pub rgba: [u8; 4],
    pub opacity: f32,
    pub width: u32,
    pub height: u32,
    pub row_stride: u32,
    pub format: PixelFormat,
    pub color_space: ColorSpace,
    pub premultiplied: bool,
    #[serde(with = "base64_bytes")]
    pub payload: Vec<u8>,
    pub asset_hash: String,
    pub renderer_id: String,
    pub renderer_version: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct SceneRect {
    pub x: i32,
    pub y: i32,
    pub w: i32,
    pub h: i32,
}
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct MusicSceneLayout {
    pub artwork: SceneRect,
    pub title: SceneRect,
    pub artist: SceneRect,
    pub album: SceneRect,
    pub spectrum: SceneRect,
    pub loudness: SceneRect,
    pub progress: SceneRect,
    pub time: SceneRect,
}
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(default)]
pub struct MusicSceneDynamics {
    pub current_seconds: f64,
    pub duration_seconds: f64,
    pub progress_ratio: f64,
    pub edge_fade_alpha: f32,
    pub end_fade_alpha: f32,
    #[serde(default)]
    pub loudness_envelope: Vec<u16>,
    #[serde(default)]
    pub loudness_trend: Vec<u16>,
    #[serde(default)]
    pub loudness_guides: [u16; 4],
}
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct MusicScenePalette {
    pub primary: [u8; 4],
    pub accent: [u8; 4],
    pub background: [u8; 4],
    pub overlay: [u8; 4],
    #[serde(default)]
    pub blur_strength: u16,
    #[serde(default)]
    pub readability: u16,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ArtworkMetadata {
    pub texture_id: String,
    pub width: u32,
    pub height: u32,
    pub row_stride: u32,
    pub format: PixelFormat,
    pub color_space: ColorSpace,
    pub alpha: bool,
    #[serde(default)]
    #[serde(with = "base64_bytes")]
    pub payload: Vec<u8>,
    #[serde(default)]
    pub asset_hash: String,
}
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct BaseTextureMetadata {
    pub texture_id: String,
    pub width: u32,
    pub height: u32,
    pub row_stride: u32,
    pub format: PixelFormat,
    pub color_space: ColorSpace,
    #[serde(default)]
    #[serde(with = "base64_bytes")]
    pub payload: Vec<u8>,
    #[serde(default)]
    pub asset_hash: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GlyphAtlasMetadata {
    pub texture_id: String,
    pub font_family: String,
    pub font_weight: u16,
    #[serde(default)]
    pub fallback_order: Vec<String>,
    pub width: u32,
    pub height: u32,
    pub row_stride: u32,
    pub glyph_count: u32,
    pub missing_glyph_id: String,
    /// Optional premultiplied-RGBA atlas pixels. Empty means the renderer
    /// should use its deterministic tofu fallback.
    #[serde(default)]
    #[serde(with = "base64_bytes")]
    pub payload: Vec<u8>,
    #[serde(default)]
    pub glyphs: Vec<GlyphEntry>,
    #[serde(default)]
    pub text_runs: Vec<TextRun>,
    #[serde(default)]
    pub asset_hash: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GlyphEntry {
    pub id: String,
    pub x: u32,
    pub y: u32,
    pub width: u32,
    pub height: u32,
    pub advance: f32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TextRun {
    pub text: String,
    pub x: f32,
    pub y: f32,
    pub size_px: f32,
    pub rgba: [u8; 4],
    #[serde(default)]
    pub opacity: f32,
    #[serde(default)]
    pub font_family: String,
    #[serde(default)]
    pub font_weight: u16,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SceneSnapshot {
    pub schema: u16,
    pub sequence: u64,
    pub pts_ns: i64,
    pub width: u32,
    pub height: u32,
    pub overlays: Vec<OverlayCommand>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum OverlayCommand {
    Text {
        id: String,
        text: String,
        x: f32,
        y: f32,
        size_px: f32,
        rgba: [u8; 4],
    },
    Image {
        id: String,
        texture_id: String,
        x: f32,
        y: f32,
        width: f32,
        height: f32,
        opacity: f32,
    },
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct AudioFeatureFrame {
    pub schema: u16,
    pub sample_rate_hz: u32,
    pub frame_index: u64,
    pub pts_ns: i64,
    /// Little-endian wire values, quantized to unsigned Q0.16.
    pub spectrum_q16: Vec<u16>,
    pub rms_q15: u16,
    pub peak_q15: u16,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
pub enum PixelFormat {
    Rgba8,
    Bgra8,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
pub enum ColorSpace {
    Srgb,
    Linear,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
pub enum Ownership {
    OwnedByTransport,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GpuFrame {
    pub schema: u16,
    pub sequence: u64,
    pub pts_ns: i64,
    pub width: u32,
    pub height: u32,
    pub row_stride: u32,
    pub format: PixelFormat,
    pub color_space: ColorSpace,
    pub alpha: bool,
    pub ownership: Ownership,
    #[serde(with = "base64_bytes")]
    pub payload: Vec<u8>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub glyph_atlas_receipt: Option<GlyphAtlasReceipt>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text_overlay_receipt: Option<TextOverlayReceipt>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub artwork_receipt: Option<ArtworkReceipt>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub base_texture_receipt: Option<BaseTextureReceipt>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub glyph_diagnostics: Option<GlyphRenderDiagnostics>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GlyphRenderDiagnostics {
    pub count: usize,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub instances: Vec<GlyphInstanceDiagnostic>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GlyphInstanceDiagnostic {
    pub id: String,
    pub screen: [f32; 4],
    pub atlas: [f32; 4],
    pub color: [f32; 4],
    pub scale: f32,
    pub baseline: f32,
    pub ink_top: f32,
    pub cell_padding: [f32; 4],
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct GlyphAtlasReceipt {
    pub sha256: String,
    pub width: u32,
    pub height: u32,
    pub row_stride: u32,
    pub glyph_count: u32,
    pub text_run_count: u32,
    pub format: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct TextOverlayReceipt {
    pub sha256: String,
    pub width: u32,
    pub height: u32,
    pub row_stride: u32,
    pub format: String,
    pub color_space: String,
    pub premultiplied: bool,
    pub renderer_id: String,
    pub renderer_version: String,
}
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ArtworkReceipt { pub sha256:String, pub source_width:u32, pub source_height:u32, pub output_width:u32, pub output_height:u32, pub crop_mode:String, pub aspect_mode:String }
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct BaseTextureReceipt { pub sha256:String, pub width:u32, pub height:u32, pub row_stride:u32, pub format:String, pub color_space:String }

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ContractError {
    InvalidDimensions,
    InvalidStride,
    PayloadTooLarge,
    PayloadLength,
    InvalidAudio,
    NonMonotonicPts,
    InvalidScene,
    InvalidArtwork,
    InvalidGlyphAtlas,
}

impl MusicScenePayload {
    pub fn validate(&self) -> Result<(), ContractError> {
        if self.schema != MUSIC_SCENE_SCHEMA
            || self.feature.spectrum_q16.len() > MUSIC_MAX_FEATURE_BINS
        {
            return Err(ContractError::InvalidScene);
        }
        if self.dynamics.loudness_envelope.len() > MUSIC_MAX_LOUDNESS_SAMPLES
            || self.dynamics.loudness_trend.len() > MUSIC_MAX_LOUDNESS_SAMPLES
            || !self.dynamics.current_seconds.is_finite()
            || !self.dynamics.duration_seconds.is_finite()
            || !self.dynamics.progress_ratio.is_finite()
            || self.dynamics.current_seconds < 0.0
            || self.dynamics.duration_seconds < 0.0
            || self.dynamics.progress_ratio < 0.0
            || self.dynamics.progress_ratio > 1.0
            || !self.dynamics.edge_fade_alpha.is_finite()
            || !self.dynamics.end_fade_alpha.is_finite()
            || self.dynamics.edge_fade_alpha < 0.0
            || self.dynamics.edge_fade_alpha > 1.0
            || self.dynamics.end_fade_alpha < 0.0
            || self.dynamics.end_fade_alpha > 1.0
            || (!self.fingerprint.is_empty() && self.fingerprint.len() != 64)
        {
            return Err(ContractError::InvalidScene);
        }
        self.feature
            .validate()
            .map_err(|_| ContractError::InvalidScene)?;
        if let Some(artwork) = &self.artwork {
            artwork.validate()?;
        }
        if let Some(atlas) = &self.glyph_atlas {
            atlas.validate()?;
        }
        if let Some(overlay) = &self.text_overlay {
            overlay.validate()?;
        }
        Ok(())
    }
}

impl TextOverlayMetadata {
    pub fn validate(&self) -> Result<(), ContractError> {
        if self.title.len() + self.artist.len() + self.album.len() + self.font_family.len() > MUSIC_MAX_TEXT_BYTES
            || !self.size_px.is_finite() || self.size_px < 0.0
            || !self.opacity.is_finite() || self.opacity < 0.0 || self.opacity > 1.0
        || self.width == 0 || self.height == 0 || self.width > MUSIC_MAX_ARTWORK_DIMENSION || self.height > MUSIC_MAX_ARTWORK_DIMENSION
            || self.row_stride < self.width.saturating_mul(4) || self.row_stride % ROW_ALIGNMENT != 0
            || (self.row_stride as usize).saturating_mul(self.height as usize) > MUSIC_MAX_ARTWORK_BYTES
            || (!self.payload.is_empty() && self.payload.len() != self.row_stride as usize * self.height as usize)
            || self.asset_hash.len() != 64 || !self.asset_hash.bytes().all(|b| b.is_ascii_hexdigit())
            || self.renderer_id.is_empty() || self.renderer_version.is_empty()
        { return Err(ContractError::InvalidScene); }
        Ok(())
    }
}

impl ArtworkMetadata {
    pub fn validate(&self) -> Result<(), ContractError> {
        if self.texture_id.is_empty()
            || self.width == 0
            || self.height == 0
            || self.width > MUSIC_MAX_ARTWORK_DIMENSION
            || self.height > MUSIC_MAX_ARTWORK_DIMENSION
        {
            return Err(ContractError::InvalidArtwork);
        }
        let min_stride = self
            .width
            .checked_mul(4)
            .ok_or(ContractError::InvalidArtwork)?;
        if self.row_stride < min_stride || self.row_stride % ROW_ALIGNMENT != 0 {
            return Err(ContractError::InvalidArtwork);
        }
        let size = self.row_stride as usize * self.height as usize;
        if size > MUSIC_MAX_ARTWORK_BYTES
            || (!self.payload.is_empty() && self.payload.len() != size)
        {
            return Err(ContractError::InvalidArtwork);
        }
        Ok(())
    }
}

impl GlyphAtlasMetadata {
    pub fn validate(&self) -> Result<(), ContractError> {
        if self.texture_id.is_empty()
            || self.font_family.is_empty()
            || self.missing_glyph_id.is_empty()
            || self.width == 0
            || self.height == 0
            || self.width > MUSIC_MAX_ARTWORK_DIMENSION
            || self.height > MUSIC_MAX_ARTWORK_DIMENSION
            || self.glyph_count > MUSIC_MAX_GLYPHS
            || self.glyphs.len() > MUSIC_MAX_GLYPHS as usize
            || self.text_runs.len() > MUSIC_MAX_GLYPH_RUNS
            || self.font_family.len() + self.missing_glyph_id.len() > MUSIC_MAX_TEXT_BYTES
            || self.fallback_order.iter().any(|name| name.is_empty())
            || self
                .fallback_order
                .iter()
                .map(|name| name.len())
                .sum::<usize>()
                > MUSIC_MAX_TEXT_BYTES
        {
            return Err(ContractError::InvalidGlyphAtlas);
        }
        let min_stride = self
            .width
            .checked_mul(4)
            .ok_or(ContractError::InvalidGlyphAtlas)?;
        if self.row_stride < min_stride
            || self.row_stride % ROW_ALIGNMENT != 0
            || self.row_stride as usize * self.height as usize > MUSIC_MAX_ARTWORK_BYTES
            || (!self.payload.is_empty()
                && self.payload.len() != self.row_stride as usize * self.height as usize)
            || self.glyphs.iter().any(|g| {
                g.id.is_empty()
                    || g.x.checked_add(g.width).map_or(true, |v| v > self.width)
                    || g.y.checked_add(g.height).map_or(true, |v| v > self.height)
                    || !g.advance.is_finite()
                    || g.advance < 0.0
            })
            || self.text_runs.iter().any(|r| {
                r.text.len() > MUSIC_MAX_TEXT_BYTES
                    || !r.x.is_finite()
                    || !r.y.is_finite()
                    || !r.size_px.is_finite()
                    || r.size_px <= 0.0
                    || !r.opacity.is_finite()
            })
        {
            return Err(ContractError::InvalidGlyphAtlas);
        }
        Ok(())
    }

    /// Returns the deterministic font lookup order.  The primary family is
    /// always attempted first, followed by declared fallbacks; an absent
    /// glyph is represented by the protocol's explicit missing-glyph id.
    pub fn font_order(&self) -> impl Iterator<Item = &str> {
        std::iter::once(self.font_family.as_str())
            .chain(self.fallback_order.iter().map(String::as_str))
    }
}

impl SceneSnapshot {
    pub fn validate(&self) -> Result<(), ContractError> {
        valid_dimensions(self.width, self.height)
    }
}

impl AudioFeatureFrame {
    pub fn validate(&self) -> Result<(), ContractError> {
        if self.schema != CONTRACT_VERSION
            || self.sample_rate_hz == 0
            || self.rms_q15 > 0x7fff
            || self.peak_q15 > 0x7fff
        {
            return Err(ContractError::InvalidAudio);
        }
        Ok(())
    }
}

impl GpuFrame {
    pub fn validate(&self) -> Result<(), ContractError> {
        valid_dimensions(self.width, self.height)?;
        let min_stride = self
            .width
            .checked_mul(4)
            .ok_or(ContractError::InvalidStride)?;
        if self.row_stride < min_stride || self.row_stride % ROW_ALIGNMENT != 0 {
            return Err(ContractError::InvalidStride);
        }
        let expected = self.row_stride as usize * self.height as usize;
        if expected > MAX_PAYLOAD_BYTES {
            return Err(ContractError::PayloadTooLarge);
        }
        if self.payload.len() != expected {
            return Err(ContractError::PayloadLength);
        }
        if self.schema != CONTRACT_VERSION || self.ownership != Ownership::OwnedByTransport {
            return Err(ContractError::InvalidAudio);
        }
        Ok(())
    }
}

fn valid_dimensions(width: u32, height: u32) -> Result<(), ContractError> {
    if width == 0 || height == 0 || width > MAX_DIMENSION || height > MAX_DIMENSION {
        Err(ContractError::InvalidDimensions)
    } else {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn text_overlay_roundtrip_and_rejects_bad_provenance() {
        let overlay = TextOverlayMetadata { title: "T".into(), artist: String::new(), album: String::new(), font_family: "sans".into(), font_weight: 400, size_px: 16.0, rgba: [255,255,255,255], opacity: 1.0, width: 1, height: 1, row_stride: 256, format: PixelFormat::Rgba8, color_space: ColorSpace::Srgb, premultiplied: true, payload: vec![0;256], asset_hash: "a".repeat(64), renderer_id: "cpu-ass".into(), renderer_version: "1".into() };
        assert!(overlay.validate().is_ok());
        let encoded = serde_json::to_string(&overlay).unwrap();
        let decoded: TextOverlayMetadata = serde_json::from_str(&encoded).unwrap();
        assert_eq!(overlay, decoded);
        let mut bad = decoded;
        bad.asset_hash = "bad".into();
        assert!(bad.validate().is_err());
    }
    #[test]
    fn gpu_frame_alignment_and_bounds() {
        let good = GpuFrame {
            schema: 1,
            sequence: 1,
            pts_ns: 0,
            width: 64,
            height: 2,
            row_stride: 256,
            format: PixelFormat::Rgba8,
            color_space: ColorSpace::Srgb,
            alpha: true,
            ownership: Ownership::OwnedByTransport,
            payload: vec![0; 512],
            glyph_atlas_receipt: None,
            text_overlay_receipt: None,
            artwork_receipt: None,
            glyph_diagnostics: None,
        };
        assert!(good.validate().is_ok());
        let mut bad = good.clone();
        bad.row_stride = 260;
        assert_eq!(bad.validate(), Err(ContractError::InvalidStride));
        bad = good.clone();
        bad.payload.pop();
        assert_eq!(bad.validate(), Err(ContractError::PayloadLength));
    }
    #[test]
    fn audio_rejects_invalid_schema_and_levels() {
        let mut a = AudioFeatureFrame {
            schema: 1,
            sample_rate_hz: 48_000,
            frame_index: 1,
            pts_ns: 0,
            spectrum_q16: vec![0, 65535],
            rms_q15: 1,
            peak_q15: 2,
        };
        assert!(a.validate().is_ok());
        a.schema = 2;
        assert_eq!(a.validate(), Err(ContractError::InvalidAudio));
    }
    #[test]
    fn serde_round_trip_preserves_format_and_order() {
        let s = SceneSnapshot {
            schema: 1,
            sequence: 3,
            pts_ns: 10,
            width: 640,
            height: 480,
            overlays: vec![OverlayCommand::Text {
                id: "title".into(),
                text: "x".into(),
                x: 0.0,
                y: 0.0,
                size_px: 20.0,
                rgba: [255, 255, 255, 255],
            }],
        };
        assert_eq!(
            serde_json::from_str::<SceneSnapshot>(&serde_json::to_string(&s).unwrap()).unwrap(),
            s
        );
    }

    #[test]
    fn glyph_atlas_preserves_unicode_fallback_order_and_rejects_empty_fallback() {
        let atlas = GlyphAtlasMetadata {
            texture_id: "atlas".into(),
            font_family: "Noto Sans CJK".into(),
            font_weight: 400,
            fallback_order: vec!["Noto Color Emoji".into(), "sans".into()],
            width: 256,
            height: 256,
            row_stride: 1024,
            glyph_count: 3,
            missing_glyph_id: "tofu".into(),
            payload: Vec::new(),
            glyphs: Vec::new(),
            text_runs: Vec::new(),
            asset_hash: String::new(),
        };
        assert!(atlas.validate().is_ok());
        assert_eq!(
            atlas.font_order().collect::<Vec<_>>(),
            vec!["Noto Sans CJK", "Noto Color Emoji", "sans"]
        );
        let mut bad = atlas;
        bad.fallback_order.push(String::new());
        assert_eq!(bad.validate(), Err(ContractError::InvalidGlyphAtlas));
    }

    #[test]
    fn artwork_payload_is_bounded_and_optional() {
        let mut artwork = ArtworkMetadata {
            texture_id: "cover".into(),
            width: 64,
            height: 64,
            row_stride: 256,
            format: PixelFormat::Rgba8,
            color_space: ColorSpace::Srgb,
            alpha: true,
            payload: Vec::new(),
            asset_hash: String::new(),
        };
        assert!(artwork.validate().is_ok());
        artwork.payload = vec![0; 255];
        assert_eq!(artwork.validate(), Err(ContractError::InvalidArtwork));
    }

    #[test]
    fn byte_payload_accepts_go_base64_and_legacy_array() {
        let json = r#"{"texture_id":"cover","width":1,"height":1,"row_stride":256,"format":"Rgba8","color_space":"Srgb","alpha":true,"payload":"AQID","asset_hash":""}"#;
        let artwork: ArtworkMetadata = serde_json::from_str(json).unwrap();
        assert_eq!(artwork.payload, vec![1, 2, 3]);
        let legacy = json.replace("\"AQID\"", "[1,2,3]");
        let artwork: ArtworkMetadata = serde_json::from_str(&legacy).unwrap();
        assert_eq!(artwork.payload, vec![1, 2, 3]);
        let encoded = serde_json::to_value(&artwork).unwrap();
        assert_eq!(encoded["payload"], "AQID");
    }
}
