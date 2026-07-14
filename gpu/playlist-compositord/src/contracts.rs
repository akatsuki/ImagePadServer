use serde::{Deserialize, Serialize};

pub const CONTRACT_VERSION: u16 = 1;
pub const ROW_ALIGNMENT: u32 = 256;
pub const MAX_DIMENSION: u32 = 16_384;
pub const MAX_PAYLOAD_BYTES: usize = 256 * 1024 * 1024;

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

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
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
}
