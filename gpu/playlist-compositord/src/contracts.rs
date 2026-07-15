use serde::{Deserialize, Serialize};

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

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct MusicScenePayload {
    pub schema: u16,
    pub feature: AudioFeatureFrame,
    #[serde(default)]
    pub artwork: Option<ArtworkMetadata>,
    #[serde(default)]
    pub glyph_atlas: Option<GlyphAtlasMetadata>,
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

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
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
    pub payload: Vec<u8>,
}

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
        self.feature
            .validate()
            .map_err(|_| ContractError::InvalidScene)?;
        if let Some(artwork) = &self.artwork {
            artwork.validate()?;
        }
        if let Some(atlas) = &self.glyph_atlas {
            atlas.validate()?;
        }
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
            || self.fallback_order.iter().map(|name| name.len()).sum::<usize>() > MUSIC_MAX_TEXT_BYTES
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
            || (!self.payload.is_empty() && self.payload.len() != self.row_stride as usize * self.height as usize)
            || self.glyphs.iter().any(|g| g.id.is_empty()
                || g.x.checked_add(g.width).map_or(true, |v| v > self.width)
                || g.y.checked_add(g.height).map_or(true, |v| v > self.height)
                || !g.advance.is_finite() || g.advance < 0.0)
            || self.text_runs.iter().any(|r| r.text.len() > MUSIC_MAX_TEXT_BYTES
                || !r.x.is_finite() || !r.y.is_finite() || !r.size_px.is_finite() || r.size_px <= 0.0
                || !r.opacity.is_finite())
        {
            return Err(ContractError::InvalidGlyphAtlas);
        }
        Ok(())
    }

    /// Returns the deterministic font lookup order.  The primary family is
    /// always attempted first, followed by declared fallbacks; an absent
    /// glyph is represented by the protocol's explicit missing-glyph id.
    pub fn font_order(&self) -> impl Iterator<Item = &str> {
        std::iter::once(self.font_family.as_str()).chain(self.fallback_order.iter().map(String::as_str))
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
            texture_id: "atlas".into(), font_family: "Noto Sans CJK".into(),
            font_weight: 400, fallback_order: vec!["Noto Color Emoji".into(), "sans".into()],
            width: 256, height: 256, row_stride: 1024, glyph_count: 3,
            missing_glyph_id: "tofu".into(), payload: Vec::new(), glyphs: Vec::new(), text_runs: Vec::new(), asset_hash: String::new(),
        };
        assert!(atlas.validate().is_ok());
        assert_eq!(atlas.font_order().collect::<Vec<_>>(), vec!["Noto Sans CJK", "Noto Color Emoji", "sans"]);
        let mut bad = atlas;
        bad.fallback_order.push(String::new());
        assert_eq!(bad.validate(), Err(ContractError::InvalidGlyphAtlas));
    }

    #[test]
    fn artwork_payload_is_bounded_and_optional() {
        let mut artwork = ArtworkMetadata {
            texture_id: "cover".into(), width: 64, height: 64, row_stride: 256,
            format: PixelFormat::Rgba8, color_space: ColorSpace::Srgb, alpha: true,
            payload: Vec::new(), asset_hash: String::new(),
        };
        assert!(artwork.validate().is_ok());
        artwork.payload = vec![0; 255];
        assert_eq!(artwork.validate(), Err(ContractError::InvalidArtwork));
    }
}
