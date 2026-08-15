//! Binary encoding for the zero-copy shared-ring transport.
//!
//! The JSONL control plane carries prepare/transition/flush; the high-frequency
//! per-batch bulk path (frame indices in, encoded H.264 access units out) is
//! carried over the shared ring in this fixed little-endian format so no JSON
//! or base64 round-trip is paid on the hot path.
//!
//! Render request (Go -> Rust):
//!   [1] type = RENDER_REQUEST_TAG
//!   [8] batch_id LE
//!   [8] epoch LE
//!   [8] timeline_version LE
//!   [4] frame_count LE
//!   [frame_count x 8] frame_index LE
//!
//! Encoded batch (Rust -> Go):
//!   [1] type = ENCODED_BATCH_TAG
//!   [8] batch_id LE
//!   [8] epoch LE
//!   [8] timeline_version LE
//!   [4] frame_count LE
//!   then frame_count frames, each:
//!     [8] sequence LE
//!     [8] pts_ns LE
//!     [8] pixel_readback_bytes LE
//!     [4] payload_len LE
//!     [payload_len] payload

use crate::contracts::EncodedH264Frame;

pub const RENDER_REQUEST_TAG: u8 = 0x01;
pub const ENCODED_BATCH_TAG: u8 = 0x02;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RenderRequest {
    pub batch_id: u64,
    pub epoch: u64,
    pub timeline_version: u64,
    pub frame_indices: Vec<u64>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RingEncodedFrame {
    pub sequence: u64,
    pub pts_ns: i64,
    pub pixel_readback_bytes: u64,
    pub payload: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct EncodedBatch {
    pub batch_id: u64,
    pub epoch: u64,
    pub timeline_version: u64,
    pub frames: Vec<RingEncodedFrame>,
}

fn push_u64(dst: &mut Vec<u8>, v: u64) {
    dst.extend_from_slice(&v.to_le_bytes());
}
fn push_i64(dst: &mut Vec<u8>, v: i64) {
    dst.extend_from_slice(&v.to_le_bytes());
}
fn push_u32(dst: &mut Vec<u8>, v: u32) {
    dst.extend_from_slice(&v.to_le_bytes());
}
fn read_u64(src: &[u8], at: &mut usize) -> Option<u64> {
    let b = src.get(*at..*at + 8)?.try_into().ok()?;
    *at += 8;
    Some(u64::from_le_bytes(b))
}
fn read_i64(src: &[u8], at: &mut usize) -> Option<i64> {
    let b = src.get(*at..*at + 8)?.try_into().ok()?;
    *at += 8;
    Some(i64::from_le_bytes(b))
}
fn read_u32(src: &[u8], at: &mut usize) -> Option<u32> {
    let b = src.get(*at..*at + 4)?.try_into().ok()?;
    *at += 4;
    Some(u32::from_le_bytes(b))
}

pub fn encode_render_request(req: &RenderRequest) -> Vec<u8> {
    let mut dst = Vec::with_capacity(1 + 8 + 8 + 8 + 4 + req.frame_indices.len() * 8);
    dst.push(RENDER_REQUEST_TAG);
    push_u64(&mut dst, req.batch_id);
    push_u64(&mut dst, req.epoch);
    push_u64(&mut dst, req.timeline_version);
    push_u32(&mut dst, req.frame_indices.len() as u32);
    for &idx in &req.frame_indices {
        push_u64(&mut dst, idx);
    }
    dst
}

pub fn decode_render_request(src: &[u8]) -> Option<RenderRequest> {
    if src.first()? != &RENDER_REQUEST_TAG {
        return None;
    }
    let mut at = 1;
    let batch_id = read_u64(src, &mut at)?;
    let epoch = read_u64(src, &mut at)?;
    let timeline_version = read_u64(src, &mut at)?;
    let count = read_u32(src, &mut at)? as usize;
    if src.len() < at + count * 8 {
        return None;
    }
    let mut frame_indices = Vec::with_capacity(count);
    for _ in 0..count {
        frame_indices.push(read_u64(src, &mut at)?);
    }
    Some(RenderRequest {
        batch_id,
        epoch,
        timeline_version,
        frame_indices,
    })
}

pub fn encode_encoded_batch(batch: &EncodedBatch) -> Vec<u8> {
    let mut dst = Vec::new();
    dst.push(ENCODED_BATCH_TAG);
    push_u64(&mut dst, batch.batch_id);
    push_u64(&mut dst, batch.epoch);
    push_u64(&mut dst, batch.timeline_version);
    push_u32(&mut dst, batch.frames.len() as u32);
    for frame in &batch.frames {
        push_u64(&mut dst, frame.sequence);
        push_i64(&mut dst, frame.pts_ns);
        push_u64(&mut dst, frame.pixel_readback_bytes);
        push_u32(&mut dst, frame.payload.len() as u32);
        dst.extend_from_slice(&frame.payload);
    }
    dst
}

pub fn decode_encoded_batch(src: &[u8]) -> Option<EncodedBatch> {
    if src.first()? != &ENCODED_BATCH_TAG {
        return None;
    }
    let mut at = 1;
    let batch_id = read_u64(src, &mut at)?;
    let epoch = read_u64(src, &mut at)?;
    let timeline_version = read_u64(src, &mut at)?;
    let count = read_u32(src, &mut at)? as usize;
    let mut frames = Vec::with_capacity(count);
    for _ in 0..count {
        let sequence = read_u64(src, &mut at)?;
        let pts_ns = read_i64(src, &mut at)?;
        let pixel_readback_bytes = read_u64(src, &mut at)?;
        let payload_len = read_u32(src, &mut at)? as usize;
        let payload = src.get(at..at + payload_len)?.to_vec();
        at += payload_len;
        frames.push(RingEncodedFrame {
            sequence,
            pts_ns,
            pixel_readback_bytes,
            payload,
        });
    }
    Some(EncodedBatch {
        batch_id,
        epoch,
        timeline_version,
        frames,
    })
}

/// Converts a JSONL EncodedH264Frame list into the ring frame list, keeping
/// only the fields the Go side validates on the hot path (sequence, PTS,
/// pixel-readback must stay zero, and the raw access unit).
pub fn encoded_frames_to_ring(frames: &[EncodedH264Frame]) -> Vec<RingEncodedFrame> {
    frames
        .iter()
        .map(|f| RingEncodedFrame {
            sequence: f.sequence,
            pts_ns: f.pts_ns,
            pixel_readback_bytes: f.pixel_readback_bytes,
            payload: f.payload.clone(),
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn render_request_round_trips() {
        let req = RenderRequest {
            batch_id: 7,
            epoch: 3,
            timeline_version: 11,
            frame_indices: vec![0, 1, 2, 8, 9],
        };
        let bytes = encode_render_request(&req);
        assert_eq!(decode_render_request(&bytes), Some(req));
        assert_eq!(decode_render_request(&[0x00]), None); // wrong tag
        assert_eq!(decode_render_request(&bytes[..bytes.len() - 1]), None); // truncated
    }

    #[test]
    fn encoded_batch_round_trips() {
        let batch = EncodedBatch {
            batch_id: 1,
            epoch: 2,
            timeline_version: 3,
            frames: vec![
                RingEncodedFrame {
                    sequence: 0,
                    pts_ns: 1000,
                    pixel_readback_bytes: 0,
                    payload: vec![0x00, 0x00, 0x01, 0x67],
                },
                RingEncodedFrame {
                    sequence: 1,
                    pts_ns: 2000,
                    pixel_readback_bytes: 0,
                    payload: vec![0x00, 0x00, 0x01, 0x68, 0xAA],
                },
            ],
        };
        let bytes = encode_encoded_batch(&batch);
        assert_eq!(decode_encoded_batch(&bytes), Some(batch));
        assert_eq!(decode_encoded_batch(&[0x99]), None);
        assert_eq!(decode_encoded_batch(&bytes[..bytes.len() - 2]), None);
    }
}
