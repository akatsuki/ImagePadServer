use std::collections::VecDeque;

pub const GPU_SHARED_RING_MAGIC: u32 = 0x4750_5531;
pub const GPU_SHARED_RING_HEADER_SIZE: usize = 64;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct RingHeader {
    pub version: u16,
    pub capacity: u16,
    pub slot_bytes: u32,
    pub write_seq: u64,
    pub read_seq: u64,
    pub closed: u32,
}

impl RingHeader {
    pub fn validate(&self) -> bool {
        self.version != 0
            && self.capacity != 0
            && self.slot_bytes != 0
            && self.read_seq <= self.write_seq
            && self.write_seq - self.read_seq <= self.capacity as u64
    }
    pub fn encode(&self, dst: &mut [u8]) -> bool {
        if dst.len() < GPU_SHARED_RING_HEADER_SIZE || !self.validate() {
            return false;
        }
        dst[..GPU_SHARED_RING_HEADER_SIZE].fill(0);
        dst[0..4].copy_from_slice(&GPU_SHARED_RING_MAGIC.to_le_bytes());
        dst[4..6].copy_from_slice(&self.version.to_le_bytes());
        dst[6..8].copy_from_slice(&self.capacity.to_le_bytes());
        dst[8..12].copy_from_slice(&self.slot_bytes.to_le_bytes());
        dst[16..24].copy_from_slice(&self.write_seq.to_le_bytes());
        dst[24..32].copy_from_slice(&self.read_seq.to_le_bytes());
        dst[32..36].copy_from_slice(&self.closed.to_le_bytes());
        true
    }
    pub fn decode(src: &[u8]) -> Option<Self> {
        if src.len() < GPU_SHARED_RING_HEADER_SIZE
            || u32::from_le_bytes(src[0..4].try_into().ok()?) != GPU_SHARED_RING_MAGIC
        {
            return None;
        }
        let h = Self {
            version: u16::from_le_bytes(src[4..6].try_into().ok()?),
            capacity: u16::from_le_bytes(src[6..8].try_into().ok()?),
            slot_bytes: u32::from_le_bytes(src[8..12].try_into().ok()?),
            write_seq: u64::from_le_bytes(src[16..24].try_into().ok()?),
            read_seq: u64::from_le_bytes(src[24..32].try_into().ok()?),
            closed: u32::from_le_bytes(src[32..36].try_into().ok()?),
        };
        h.validate().then_some(h)
    }
}

/// Bounded frame ring. The owner is the sidecar; consumers must release frames
/// by popping them. Keeping this abstraction independent of mmap lets the IPC
/// transport be swapped for Windows named mappings/POSIX mmap later.
#[derive(Debug)]
pub struct FrameRing<T> {
    capacity: usize,
    queue: VecDeque<T>,
    closed: bool,
}

impl<T> FrameRing<T> {
    pub fn new(capacity: usize) -> Self {
        assert!(capacity > 0);
        Self {
            capacity,
            queue: VecDeque::with_capacity(capacity),
            closed: false,
        }
    }
    pub fn push(&mut self, frame: T) -> Result<(), T> {
        if self.closed || self.queue.len() == self.capacity {
            return Err(frame);
        }
        self.queue.push_back(frame);
        Ok(())
    }
    pub fn pop(&mut self) -> Option<T> {
        self.queue.pop_front()
    }
    pub fn len(&self) -> usize {
        self.queue.len()
    }
    pub fn close(&mut self) {
        self.closed = true;
    }
    pub fn is_closed(&self) -> bool {
        self.closed
    }
}

#[cfg(test)]
mod tests {
    use super::{FrameRing, RingHeader, GPU_SHARED_RING_HEADER_SIZE};
    #[test]
    fn bounded_and_ordered() {
        let mut r = FrameRing::new(2);
        assert!(r.push(1).is_ok());
        assert!(r.push(2).is_ok());
        assert_eq!(r.push(3), Err(3));
        assert_eq!(r.pop(), Some(1));
    }
    #[test]
    fn closed_rejects() {
        let mut r = FrameRing::new(1);
        r.close();
        assert!(r.push(1).is_err());
        assert!(r.is_closed());
    }
    #[test]
    fn header_round_trip_and_overrun_rejection() {
        let h = RingHeader {
            version: 1,
            capacity: 4,
            slot_bytes: 1024,
            write_seq: 7,
            read_seq: 4,
            closed: 0,
        };
        let mut b = [0u8; GPU_SHARED_RING_HEADER_SIZE];
        assert!(h.encode(&mut b));
        assert_eq!(RingHeader::decode(&b), Some(h));
        let bad = RingHeader { write_seq: 9, ..h };
        assert!(!bad.encode(&mut b));
    }
}
