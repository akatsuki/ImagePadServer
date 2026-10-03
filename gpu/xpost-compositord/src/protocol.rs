use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::io::{self, Read, Write};

pub const MAX_CANVAS_DIMENSION: u32 = 16_384;
pub const MAX_ASSET_DIMENSION: u32 = 16_384;
pub const MAX_ASSETS: usize = 4_096;
pub const MAX_LAYERS: usize = 4_096;
pub const MAX_CONTROL_BYTES: usize = 8 * 1024 * 1024;
pub const MAX_ASSET_BYTES: u64 = 128 * 1024 * 1024;
pub const MAX_TOTAL_ASSET_BYTES: u64 = 512 * 1024 * 1024;
pub const MAX_FRAME_BYTES: u64 = 256 * 1024 * 1024;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Asset {
    pub id: String,
    pub path: String,
    pub width: u32,
    pub height: u32,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Layer {
    pub asset_id: String,
    pub x: f32,
    pub y: f32,
    pub width: f32,
    pub height: f32,
    pub tilt: f32,
    pub opacity: f32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub uv: Option<[f32; 4]>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Frame {
    pub layers: Vec<Layer>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Update {
    #[serde(rename = "updateID")]
    pub asset_id: String,
    pub byte_len: u64,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Ready {
    pub adapter: String,
    pub backend: String,
}

/// Read a little-endian uint32 length-prefixed JSON control record. The size
/// is checked before its body is allocated.
pub fn read_control<R: Read>(reader: &mut R) -> Result<Vec<u8>, String> {
    let mut prefix = [0u8; 4];
    reader
        .read_exact(&mut prefix)
        .map_err(|e| format!("reading control message length: {e}"))?;
    let length = u32::from_le_bytes(prefix) as usize;
    if length == 0 || length > MAX_CONTROL_BYTES {
        return Err("control message length is outside configured bounds".into());
    }
    let mut body = vec![0u8; length];
    reader
        .read_exact(&mut body)
        .map_err(|e| format!("reading control message body: {e}"))?;
    Ok(body)
}

pub fn read_control_or_eof<R: Read>(reader: &mut R) -> Result<Option<Vec<u8>>, String> {
    let mut prefix = [0u8; 4];
    let mut first = [0u8; 1];
    loop {
        match reader.read(&mut first) {
            Ok(0) => return Ok(None),
            Ok(1) => {
                prefix[0] = first[0];
                break;
            }
            Ok(_) => unreachable!(),
            Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
            Err(e) => return Err(format!("reading control message length: {e}")),
        }
    }
    reader
        .read_exact(&mut prefix[1..])
        .map_err(|e| format!("reading control message length: {e}"))?;
    let length = u32::from_le_bytes(prefix) as usize;
    if length == 0 || length > MAX_CONTROL_BYTES {
        return Err("control message length is outside configured bounds".into());
    }
    let mut body = vec![0u8; length];
    reader
        .read_exact(&mut body)
        .map_err(|e| format!("reading control message body: {e}"))?;
    Ok(Some(body))
}

pub fn write_control<W: Write>(writer: &mut W, body: &[u8]) -> io::Result<()> {
    let length = u32::try_from(body.len())
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "control message too large"))?;
    if body.is_empty() || body.len() > MAX_CONTROL_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "control message length is outside configured bounds",
        ));
    }
    writer.write_all(&length.to_le_bytes())?;
    writer.write_all(body)
}

pub fn validate_assets(width: u32, height: u32, assets: &[Asset]) -> Result<(), String> {
    if width == 0 || height == 0 || width > MAX_CANVAS_DIMENSION || height > MAX_CANVAS_DIMENSION {
        return Err("canvas dimensions are outside the supported bounds".into());
    }
    if u64::from(width) * u64::from(height) * 4 > MAX_FRAME_BYTES {
        return Err("canvas output byte size exceeds the configured limit".into());
    }
    if assets.len() > MAX_ASSETS {
        return Err("asset count exceeds the configured limit".into());
    }
    let mut ids = HashSet::with_capacity(assets.len());
    let mut total = 0u64;
    for asset in assets {
        if asset.id.is_empty() || asset.id.len() > 256 || !ids.insert(asset.id.as_str()) {
            return Err("asset IDs must be unique, non-empty, and at most 256 bytes".into());
        }
        if asset.path.is_empty() || asset.path.len() > 32_768 {
            return Err(format!("asset {} path is empty or too long", asset.id));
        }
        if asset.width == 0
            || asset.height == 0
            || asset.width > MAX_ASSET_DIMENSION
            || asset.height > MAX_ASSET_DIMENSION
        {
            return Err(format!(
                "asset {} dimensions are outside the supported bounds",
                asset.id
            ));
        }
        let bytes = u64::from(asset.width) * u64::from(asset.height) * 4;
        if bytes > MAX_ASSET_BYTES || total > MAX_TOTAL_ASSET_BYTES - bytes {
            return Err("asset pixels exceed the configured byte limits".into());
        }
        total += bytes;
    }
    Ok(())
}

pub fn validate_frame(frame: &Frame, ids: &HashSet<String>) -> Result<(), String> {
    if frame.layers.len() > MAX_LAYERS {
        return Err("layer count exceeds the configured limit".into());
    }
    for (i, layer) in frame.layers.iter().enumerate() {
        if !ids.contains(&layer.asset_id) {
            return Err(format!(
                "layer {i} references unknown asset {}",
                layer.asset_id
            ));
        }
        let finite = [
            layer.x,
            layer.y,
            layer.width,
            layer.height,
            layer.tilt,
            layer.opacity,
        ]
        .iter()
        .all(|v| v.is_finite());
        if !finite
            || layer.width <= 0.0
            || layer.height <= 0.0
            || !(-89.0..=89.0).contains(&layer.tilt)
            || !(0.0..=1.0).contains(&layer.opacity)
        {
            return Err(format!("layer {i} has invalid geometry, tilt, or opacity"));
        }
        if let Some([u0, v0, u1, v1]) = layer.uv {
            if ![u0, v0, u1, v1].iter().all(|v| v.is_finite())
                || !(0.0..=1.0).contains(&u0)
                || !(0.0..=1.0).contains(&v0)
                || !(0.0..=1.0).contains(&u1)
                || !(0.0..=1.0).contains(&v1)
                || u0 >= u1
                || v0 >= v1
            {
                return Err(format!("layer {i} has invalid UV range"));
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::{validate_frame, Frame};
    use std::collections::HashSet;

    #[test]
    fn legacy_layer_without_uv_decodes_as_full_image() {
        let frame: Frame = serde_json::from_str(
            r#"{"layers":[{"assetId":"tile","x":1.0,"y":2.0,"width":30.0,"height":40.0,"tilt":0.0,"opacity":1.0}]}"#,
        )
        .unwrap();
        assert_eq!(frame.layers[0].uv, None);
        validate_frame(&frame, &HashSet::from(["tile".to_string()])).unwrap();
    }

    #[test]
    fn validates_uv_range_and_rejects_out_of_bounds_or_empty_ranges() {
        let ids = HashSet::from(["tile".to_string()]);
        for uv in [
            "[-0.1,0.0,1.0,1.0]",
            "[0.0,0.0,1.1,1.0]",
            "[0.5,0.0,0.5,1.0]",
            "[0.0,0.5,1.0,0.5]",
        ] {
            let json = format!(
                r#"{{"layers":[{{"assetId":"tile","x":1.0,"y":2.0,"width":30.0,"height":40.0,"tilt":0.0,"opacity":1.0,"uv":{uv}}}]}}"#
            );
            let frame: Frame = serde_json::from_str(&json).unwrap();
            assert!(validate_frame(&frame, &ids).is_err(), "accepted UV {uv}");
        }
    }

    #[test]
    fn validates_uv_rejects_non_finite_values() {
        let ids = HashSet::from(["tile".to_string()]);
        let mut frame: Frame = serde_json::from_str(
            r#"{"layers":[{"assetId":"tile","x":1.0,"y":2.0,"width":30.0,"height":40.0,"tilt":0.0,"opacity":1.0}]}"#,
        )
        .unwrap();
        for invalid in [f32::NAN, f32::INFINITY, f32::NEG_INFINITY] {
            frame.layers[0].uv = Some([0.0, 0.0, invalid, 1.0]);
            assert!(validate_frame(&frame, &ids).is_err());
        }
    }
}
