//! GPU-side BT.709 limited-range conversion primitives.
//!
//! The production transport is intentionally not wired here yet.  Keeping the
//! shader source and its scalar reference together makes the format contract
//! testable before the compute pass is connected to the renderer.

/// WGSL helpers for full-resolution luma and 2x2 subsampled chroma planes.
pub const WGSL_BT709_LIMITED: &str = r#"
fn bt709_limited_y(rgb: vec3<f32>) -> f32 {
    return clamp(16.0 / 255.0 + (219.0 / 255.0) * dot(rgb, vec3<f32>(0.2126, 0.7152, 0.0722)), 0.0, 1.0);
}
fn bt709_limited_u(rgb: vec3<f32>) -> f32 {
    return clamp(128.0 / 255.0 + (224.0 / 255.0) * dot(rgb, vec3<f32>(-0.114572, -0.385428, 0.5)), 0.0, 1.0);
}
fn bt709_limited_v(rgb: vec3<f32>) -> f32 {
    return clamp(128.0 / 255.0 + (224.0 / 255.0) * dot(rgb, vec3<f32>(0.5, -0.454153, -0.045847)), 0.0, 1.0);
}
"#;

/// Ceil-divided dimensions of the YUV420 chroma plane.
pub fn chroma_dimensions(width: u32, height: u32) -> (u32, u32) {
    (width.div_ceil(2), height.div_ceil(2))
}

fn limited_byte(value: f32) -> u8 {
    (value.clamp(0.0, 1.0) * 255.0).round() as u8
}

/// Scalar reference for a normalized linear RGB sample, useful for shader tests.
pub fn bt709_limited_rgb(rgb: [f32; 3]) -> [u8; 3] {
    let y = 16.0 / 255.0 + (219.0 / 255.0) * (0.2126 * rgb[0] + 0.7152 * rgb[1] + 0.0722 * rgb[2]);
    let u =
        128.0 / 255.0 + (224.0 / 255.0) * (-0.114572 * rgb[0] - 0.385428 * rgb[1] + 0.5 * rgb[2]);
    let v =
        128.0 / 255.0 + (224.0 / 255.0) * (0.5 * rgb[0] - 0.454153 * rgb[1] - 0.045847 * rgb[2]);
    [limited_byte(y), limited_byte(u), limited_byte(v)]
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn odd_dimensions_use_ceil_chroma_geometry() {
        assert_eq!(chroma_dimensions(3, 3), (2, 2));
        assert_eq!(chroma_dimensions(1920, 1080), (960, 540));
        assert_eq!(chroma_dimensions(0, 1), (0, 1));
    }

    #[test]
    fn bt709_limited_neutral_endpoints() {
        assert_eq!(bt709_limited_rgb([0.0, 0.0, 0.0]), [16, 128, 128]);
        assert_eq!(bt709_limited_rgb([1.0, 1.0, 1.0]), [235, 128, 128]);
    }

    #[test]
    fn bt709_limited_primary_samples() {
        let [y, u, v] = bt709_limited_rgb([1.0, 0.0, 0.0]);
        assert!((y as i16 - 63).abs() <= 1);
        assert!((u as i16 - 102).abs() <= 1);
        assert!((v as i16 - 240).abs() <= 1);
        assert!(WGSL_BT709_LIMITED.contains("bt709_limited_y"));
        assert!(WGSL_BT709_LIMITED.contains("bt709_limited_u"));
        assert!(WGSL_BT709_LIMITED.contains("bt709_limited_v"));
    }
}
