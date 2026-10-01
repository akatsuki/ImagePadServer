use std::collections::HashSet;
use std::fmt;
use std::io::{self, Read};

use sha2::{Digest, Sha256};

pub const HEADER_BYTES: usize = 80;
pub const DRAW_BYTES: usize = 128;
pub const FLAGS: u32 = 3;
pub const MAX_WIDTH: u32 = 3_840;
pub const MAX_HEIGHT: u32 = 2_160;
pub const MAX_FRAMES: u32 = 1_000_000;
pub const MAX_FPS_COMPONENT: u32 = 1_000_000;
pub const MAX_ASSET_DIMENSION: u32 = 16_384;
pub const MAX_ASSETS: u32 = 10_000;
pub const MAX_DRAWS: u32 = 100_000;
pub const MAX_ASSET_BYTES: u64 = 128 * 1024 * 1024;
pub const MAX_TOTAL_ASSET_BYTES: u64 = 256 * 1024 * 1024;
const MAX_EXACT_F32_INT: i64 = 1 << 24;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error(String);

impl Error {
    fn invalid(message: impl Into<String>) -> Self {
        Self(message.into())
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for Error {}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Header {
    pub width: u32,
    pub height: u32,
    pub frame_count: u32,
    pub fps_num: u32,
    pub fps_den: u32,
    pub bundle_sha256: [u8; 32],
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Asset {
    pub id: u32,
    pub width: u32,
    pub height: u32,
    pub sha256: [u8; 32],
    pub rgba: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Draw {
    pub asset_id: u32,
    pub start_vpos: i32,
    pub end_vpos: i32,
    pub anchor_vpos: i32,
    pub owner_order: u32,
    pub comment_index: u32,
    pub primitive_index: u32,
    pub rect: [f32; 4],
    pub projection: [f32; 16],
    pub alpha: f32,
    pub anchor_x: f64,
    pub speed_x: f64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Scene {
    pub header: Header,
    pub assets: Vec<Asset>,
    pub draws: Vec<Draw>,
}

/// Return floor(frame * fps_den * 100 / fps_num), including negative frames.
pub fn frame_vpos(frame: i64, fps_num: u32, fps_den: u32) -> Result<i32, Error> {
    validate_fps(fps_num, fps_den)?;
    // The maximum product is below 2^127: i64 * 1,000,000 * 100.
    let numerator = (frame as i128) * (fps_den as i128) * 100;
    let value = numerator.div_euclid(fps_num as i128);
    i32::try_from(value).map_err(|_| Error::invalid("vpos is outside signed int32"))
}

/// Read one complete NCT1 scene, checking limits before allocating pixel data.
pub fn read_scene<R: Read>(mut reader: R) -> Result<Scene, Error> {
    let mut header_bytes = [0u8; HEADER_BYTES];
    read_exact(&mut reader, &mut header_bytes, "header")?;
    if &header_bytes[0..4] != b"NCT1" || read_u32(&header_bytes[4..8]) as usize != HEADER_BYTES {
        return Err(Error::invalid("bad NCT1 magic or header size"));
    }
    let mut bundle_sha256 = [0u8; 32];
    bundle_sha256.copy_from_slice(&header_bytes[36..68]);
    let header = Header {
        width: read_u32(&header_bytes[8..12]),
        height: read_u32(&header_bytes[12..16]),
        frame_count: read_u32(&header_bytes[16..20]),
        fps_num: read_u32(&header_bytes[20..24]),
        fps_den: read_u32(&header_bytes[24..28]),
        bundle_sha256,
    };
    let asset_count = read_u32(&header_bytes[28..32]);
    let draw_count = read_u32(&header_bytes[32..36]);
    let total_raw_expected = read_u64(&header_bytes[68..76]);
    let flags = read_u32(&header_bytes[76..80]);
    if flags != FLAGS {
        return Err(Error::invalid(format!("unsupported NCT1 flags {flags:#x}")));
    }
    validate_header(&header)?;
    if asset_count > MAX_ASSETS
        || draw_count > MAX_DRAWS
        || total_raw_expected > MAX_TOTAL_ASSET_BYTES
    {
        return Err(Error::invalid(
            "NCT1 header exceeds asset, draw, or byte limits",
        ));
    }

    let mut assets = Vec::with_capacity(asset_count as usize);
    let mut seen_ids = HashSet::with_capacity(asset_count as usize);
    let mut total_raw = 0u64;
    for index in 0..asset_count {
        let mut size_bytes = [0u8; 4];
        read_exact(
            &mut reader,
            &mut size_bytes,
            &format!("asset {index} record length"),
        )?;
        let record_bytes = read_u32(&size_bytes) as u64;
        if record_bytes < 44 {
            return Err(Error::invalid(format!(
                "asset {index} record is shorter than metadata"
            )));
        }
        let mut meta = [0u8; 44];
        read_exact(&mut reader, &mut meta, &format!("asset {index} metadata"))?;
        let id = read_u32(&meta[0..4]);
        let width = read_u32(&meta[4..8]);
        let height = read_u32(&meta[8..12]);
        if !seen_ids.insert(id) {
            return Err(Error::invalid(format!("duplicate asset id {id}")));
        }
        if width == 0 || height == 0 || width > MAX_ASSET_DIMENSION || height > MAX_ASSET_DIMENSION
        {
            return Err(Error::invalid(format!(
                "asset {id} dimensions exceed limits"
            )));
        }
        let pixels = (width as u64) * (height as u64);
        let raw_bytes = pixels * 4;
        if raw_bytes > MAX_ASSET_BYTES {
            return Err(Error::invalid(format!(
                "asset {id} pixel bytes exceed limit"
            )));
        }
        if record_bytes != 44 + raw_bytes {
            return Err(Error::invalid(format!(
                "asset {id} record length does not match dimensions"
            )));
        }
        if total_raw > total_raw_expected
            || raw_bytes > total_raw_expected - total_raw
            || total_raw + raw_bytes > MAX_TOTAL_ASSET_BYTES
        {
            return Err(Error::invalid(
                "asset bytes exceed declared or configured total",
            ));
        }
        let mut sha256 = [0u8; 32];
        sha256.copy_from_slice(&meta[12..44]);
        let mut rgba = vec![0u8; raw_bytes as usize];
        read_exact(&mut reader, &mut rgba, &format!("asset {id} pixels"))?;
        if Sha256::digest(&rgba).as_slice() != sha256 {
            return Err(Error::invalid(format!("asset {id} SHA-256 mismatch")));
        }
        total_raw += raw_bytes;
        assets.push(Asset {
            id,
            width,
            height,
            sha256,
            rgba,
        });
    }
    if total_raw != total_raw_expected {
        return Err(Error::invalid(format!(
            "asset bytes total {total_raw} differs from header {total_raw_expected}"
        )));
    }

    let mut draws = Vec::with_capacity(draw_count as usize);
    for index in 0..draw_count {
        let mut bytes = [0u8; DRAW_BYTES];
        read_exact(&mut reader, &mut bytes, &format!("draw {index}"))?;
        draws.push(decode_draw(&bytes));
    }
    let mut extra = [0u8; 1];
    loop {
        match reader.read(&mut extra) {
            Ok(0) => break,
            Ok(_) => return Err(Error::invalid("trailing bytes after declared NCT1 records")),
            Err(error) if error.kind() == io::ErrorKind::Interrupted => continue,
            Err(error) => return Err(Error::invalid(format!("checking NCT1 EOF: {error}"))),
        }
    }
    let scene = Scene {
        header,
        assets,
        draws,
    };
    validate_scene(&scene)?;
    Ok(scene)
}

fn validate_header(header: &Header) -> Result<(), Error> {
    if header.width == 0
        || header.width > MAX_WIDTH
        || header.height == 0
        || header.height > MAX_HEIGHT
    {
        return Err(Error::invalid("output dimensions exceed NCT1 limits"));
    }
    if header.frame_count == 0 || header.frame_count > MAX_FRAMES {
        return Err(Error::invalid("frame count exceeds NCT1 limits"));
    }
    validate_fps(header.fps_num, header.fps_den)?;
    frame_vpos(
        header.frame_count as i64 - 1,
        header.fps_num,
        header.fps_den,
    )?;
    Ok(())
}

fn validate_fps(fps_num: u32, fps_den: u32) -> Result<(), Error> {
    if fps_num == 0
        || fps_den == 0
        || fps_num > MAX_FPS_COMPONENT
        || fps_den > MAX_FPS_COMPONENT
        || fps_num as u64 > fps_den as u64 * 60
    {
        return Err(Error::invalid(format!(
            "invalid frame rate {fps_num}/{fps_den}"
        )));
    }
    Ok(())
}

fn validate_scene(scene: &Scene) -> Result<(), Error> {
    validate_header(&scene.header)?;
    if scene.assets.len() > MAX_ASSETS as usize || scene.draws.len() > MAX_DRAWS as usize {
        return Err(Error::invalid("NCT1 asset or draw count exceeds limit"));
    }
    let mut asset_ids = HashSet::with_capacity(scene.assets.len());
    let mut total_raw = 0u64;
    for asset in &scene.assets {
        if !asset_ids.insert(asset.id) {
            return Err(Error::invalid(format!("duplicate asset id {}", asset.id)));
        }
        if asset.width == 0
            || asset.height == 0
            || asset.width > MAX_ASSET_DIMENSION
            || asset.height > MAX_ASSET_DIMENSION
        {
            return Err(Error::invalid(format!(
                "asset {} dimensions exceed limits",
                asset.id
            )));
        }
        let raw_bytes = (asset.width as u64) * (asset.height as u64) * 4;
        if raw_bytes > MAX_ASSET_BYTES || asset.rgba.len() as u64 != raw_bytes {
            return Err(Error::invalid(format!(
                "asset {} has invalid pixel byte count",
                asset.id
            )));
        }
        total_raw += raw_bytes;
        if total_raw > MAX_TOTAL_ASSET_BYTES {
            return Err(Error::invalid("total asset bytes exceed limit"));
        }
        if Sha256::digest(&asset.rgba).as_slice() != asset.sha256 {
            return Err(Error::invalid(format!(
                "asset {} SHA-256 mismatch",
                asset.id
            )));
        }
    }
    let first_vpos = frame_vpos(0, scene.header.fps_num, scene.header.fps_den)? as i64;
    let last_vpos = frame_vpos(
        scene.header.frame_count as i64 - 1,
        scene.header.fps_num,
        scene.header.fps_den,
    )? as i64;
    for (index, draw) in scene.draws.iter().enumerate() {
        if !asset_ids.contains(&draw.asset_id) {
            return Err(Error::invalid(format!(
                "draw {index} references missing asset {}",
                draw.asset_id
            )));
        }
        if draw.start_vpos >= draw.end_vpos {
            return Err(Error::invalid(format!(
                "draw {index} has empty or reversed interval"
            )));
        }
        if !draw.rect.iter().all(|value| value.is_finite())
            || !draw.projection.iter().all(|value| value.is_finite())
            || !draw.alpha.is_finite()
            || !(0.0..=1.0).contains(&draw.alpha)
            || !draw.anchor_x.is_finite()
            || !draw.speed_x.is_finite()
        {
            return Err(Error::invalid(format!(
                "draw {index} contains non-finite or invalid float values"
            )));
        }
        let visible_start = (draw.start_vpos as i64).max(first_vpos);
        let visible_end = ((draw.end_vpos as i64) - 1).min(last_vpos);
        if visible_start <= visible_end {
            let min_delta = visible_start - draw.anchor_vpos as i64;
            let max_delta = visible_end - draw.anchor_vpos as i64;
            if min_delta < -MAX_EXACT_F32_INT || max_delta > MAX_EXACT_F32_INT {
                return Err(Error::invalid(format!(
                    "draw {index} visible vpos delta exceeds exact float32 integer range"
                )));
            }
        }
    }
    Ok(())
}

fn decode_draw(bytes: &[u8; DRAW_BYTES]) -> Draw {
    let mut rect = [0.0f32; 4];
    for (index, value) in rect.iter_mut().enumerate() {
        *value = f32_at(bytes, 28 + index * 4);
    }
    let mut projection = [0.0f32; 16];
    for (index, value) in projection.iter_mut().enumerate() {
        *value = f32_at(bytes, 44 + index * 4);
    }
    Draw {
        asset_id: read_u32(&bytes[0..4]),
        start_vpos: read_i32(&bytes[4..8]),
        end_vpos: read_i32(&bytes[8..12]),
        anchor_vpos: read_i32(&bytes[12..16]),
        owner_order: read_u32(&bytes[16..20]),
        comment_index: read_u32(&bytes[20..24]),
        primitive_index: read_u32(&bytes[24..28]),
        rect,
        projection,
        alpha: f32_at(bytes, 108),
        anchor_x: f64_at(bytes, 112),
        speed_x: f64_at(bytes, 120),
    }
}

fn read_exact<R: Read>(reader: &mut R, target: &mut [u8], what: &str) -> Result<(), Error> {
    reader
        .read_exact(target)
        .map_err(|error| Error::invalid(format!("reading {what}: {error}")))
}

fn read_u32(bytes: &[u8]) -> u32 {
    u32::from_le_bytes(bytes.try_into().expect("four byte field"))
}

fn read_u64(bytes: &[u8]) -> u64 {
    u64::from_le_bytes(bytes.try_into().expect("eight byte field"))
}

fn read_i32(bytes: &[u8]) -> i32 {
    i32::from_le_bytes(bytes.try_into().expect("four byte field"))
}

fn f32_at(bytes: &[u8], offset: usize) -> f32 {
    f32::from_bits(read_u32(&bytes[offset..offset + 4]))
}

fn f64_at(bytes: &[u8], offset: usize) -> f64 {
    f64::from_bits(read_u64(&bytes[offset..offset + 8]))
}

#[cfg(test)]
mod tests {
    use std::io::Cursor;

    use super::{frame_vpos, read_scene, MAX_TOTAL_ASSET_BYTES};

    const GOLDEN: &[u8] = include_bytes!(
        "../../../internal/nicorender/testdata/timeline/wire/wire-multidraw-33x19-3frames.nct"
    );
    const RED_GOLDEN: &[u8] =
        include_bytes!("../../../internal/nicorender/testdata/timeline/wire/red-33x19-3frames.nct");

    #[test]
    fn reads_python_golden_and_asset_hashes() {
        let scene = read_scene(Cursor::new(GOLDEN)).expect("read Python-generated NCT1 golden");
        assert_eq!(scene.header.width, 33);
        assert_eq!(scene.header.height, 19);
        assert_eq!(scene.header.frame_count, 3);
        assert_eq!(scene.header.fps_num, 30);
        assert_eq!(scene.header.fps_den, 1);
        assert_eq!(scene.assets.len(), 2);
        assert_eq!(scene.draws.len(), 2);
        assert_eq!(scene.assets[0].rgba, [255, 0, 0, 255]);
        assert_eq!(scene.draws[0].start_vpos, -4);
        assert_eq!(scene.draws[1].alpha, 0.75);
    }

    #[test]
    fn reads_single_red_render_golden_with_pixel_orthographic_projection() {
        let scene =
            read_scene(Cursor::new(RED_GOLDEN)).expect("read Python-generated red render NCT1");
        assert_eq!(scene.assets.len(), 1);
        assert_eq!(scene.assets[0].rgba, [255, 0, 0, 255]);
        assert_eq!(scene.draws.len(), 1);
        let draw = &scene.draws[0];
        assert_eq!(draw.rect, [0.0, 0.0, 33.0, 19.0]);
        assert_eq!(draw.start_vpos, 0);
        assert_eq!(draw.end_vpos, 100);
        assert_eq!(draw.anchor_x, 0.0);
        assert_eq!(draw.speed_x, 0.0);
        assert_eq!(draw.projection[0], 2.0f32 / 33.0);
        assert_eq!(draw.projection[5], -2.0f32 / 19.0);
        assert_eq!(draw.projection[10], 1.0);
        assert_eq!(draw.projection[12], -1.0);
        assert_eq!(draw.projection[13], 1.0);
        assert_eq!(draw.projection[15], 1.0);
    }

    #[test]
    fn matches_negative_floor_clock() {
        for (frame, want) in [(-1, -2), (0, 0), (1, 1), (2, 3), (60, 100)] {
            assert_eq!(frame_vpos(frame, 60_000, 1_001).unwrap(), want);
        }
        assert_eq!(frame_vpos(-1, 30, 1).unwrap(), -4);
        assert!(frame_vpos(0, 0, 1).is_err());
        assert!(frame_vpos(i64::MAX, 1, 1).is_err());
    }

    #[test]
    fn rejects_every_truncation_and_trailing_bytes() {
        for length in 0..GOLDEN.len() {
            assert!(
                read_scene(Cursor::new(&GOLDEN[..length])).is_err(),
                "accepted {length} bytes"
            );
        }
        let mut extra = GOLDEN.to_vec();
        extra.push(0);
        assert!(read_scene(Cursor::new(extra)).is_err());
    }

    #[test]
    fn rejects_malformed_inputs() {
        for bytes in [
            &include_bytes!(
                "../../../internal/nicorender/testdata/timeline/wire/unknown-flags.nct"
            )[..],
            &include_bytes!(
                "../../../internal/nicorender/testdata/timeline/wire/missing-asset.nct"
            )[..],
            &include_bytes!(
                "../../../internal/nicorender/testdata/timeline/wire/oversized-length.nct"
            )[..],
        ] {
            assert!(read_scene(Cursor::new(bytes)).is_err());
        }

        let mut duplicate_id = GOLDEN.to_vec();
        duplicate_id[136..140].copy_from_slice(&1u32.to_le_bytes());
        assert!(read_scene(Cursor::new(duplicate_id)).is_err());

        let mut bad_hash = GOLDEN.to_vec();
        bad_hash[96] ^= 0xff;
        assert!(read_scene(Cursor::new(bad_hash)).is_err());

        let mut non_finite = GOLDEN.to_vec();
        non_finite[80 + 2 * 52 + 108..80 + 2 * 52 + 112]
            .copy_from_slice(&f32::NAN.to_bits().to_le_bytes());
        assert!(read_scene(Cursor::new(non_finite)).is_err());

        for (offset, value) in [(28, 10_001u32), (32, 100_001u32)] {
            let mut oversized = GOLDEN.to_vec();
            oversized[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
            assert!(read_scene(Cursor::new(oversized)).is_err());
        }
        let mut oversized_total = GOLDEN.to_vec();
        oversized_total[68..76].copy_from_slice(&(MAX_TOTAL_ASSET_BYTES + 1).to_le_bytes());
        assert!(read_scene(Cursor::new(oversized_total)).is_err());
    }
}
