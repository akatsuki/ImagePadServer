use std::collections::VecDeque;
use std::io;
use std::path::Path;
use std::sync::atomic::{fence, Ordering};

use crate::shared_mapping::SharedMapping;

pub const GPU_SHARED_RING_MAGIC: u32 = 0x4750_5531;
pub const GPU_SHARED_RING_HEADER_SIZE: usize = 64;

// RingHeader field offsets (shared with the Go-side implementation).
pub const RING_WRITE_SEQ_OFFSET: usize = 16;
pub const RING_READ_SEQ_OFFSET: usize = 24;

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

/// Cross-process single-producer/single-consumer ring backed by a
/// SharedMapping. The first 64 bytes hold a RingHeader; each slot is
/// `[4-byte little-endian length][payload]`. Producers and consumers in
/// different processes coordinate through write_seq/read_seq in the header,
/// so no locks are required.
pub struct SharedRing {
    mapping: SharedMapping,
    capacity: usize,
    slot_bytes: usize,
}

impl SharedRing {
    pub fn create(path: impl AsRef<Path>, capacity: usize, slot_bytes: usize) -> io::Result<Self> {
        if capacity == 0 || capacity > u16::MAX as usize || slot_bytes < 4 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "invalid ring geometry",
            ));
        }
        let total = GPU_SHARED_RING_HEADER_SIZE + capacity * slot_bytes;
        let mut mapping = SharedMapping::create(path, total)?;
        let header = RingHeader {
            version: 1,
            capacity: capacity as u16,
            slot_bytes: slot_bytes as u32,
            write_seq: 0,
            read_seq: 0,
            closed: 0,
        };
        header.encode(&mut mapping.bytes_mut()[..GPU_SHARED_RING_HEADER_SIZE]);
        Ok(Self {
            mapping,
            capacity,
            slot_bytes,
        })
    }

    pub fn open(path: impl AsRef<Path>, capacity: usize, slot_bytes: usize) -> io::Result<Self> {
        let total = GPU_SHARED_RING_HEADER_SIZE + capacity * slot_bytes;
        let mapping = SharedMapping::open(path, total)?;
        let header = RingHeader::decode(&mapping.bytes()[..GPU_SHARED_RING_HEADER_SIZE])
            .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "ring header is invalid"))?;
        if header.capacity as usize != capacity || header.slot_bytes as usize != slot_bytes {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "ring geometry mismatch",
            ));
        }
        Ok(Self {
            mapping,
            capacity,
            slot_bytes,
        })
    }

    fn seq_at(&self, offset: usize) -> u64 {
        let bytes = self.mapping.bytes();
        u64::from_le_bytes(bytes[offset..offset + 8].try_into().unwrap())
    }

    fn set_seq_at(&mut self, offset: usize, value: u64) {
        self.mapping.bytes_mut()[offset..offset + 8].copy_from_slice(&value.to_le_bytes());
    }

    pub fn write_seq(&self) -> u64 {
        self.seq_at(RING_WRITE_SEQ_OFFSET)
    }

    pub fn read_seq(&self) -> u64 {
        self.seq_at(RING_READ_SEQ_OFFSET)
    }

    fn slot_offset(&self, seq: u64) -> usize {
        GPU_SHARED_RING_HEADER_SIZE + (seq % self.capacity as u64) as usize * self.slot_bytes
    }

    /// Writes one message. Returns false when the ring is full or the message
    /// exceeds `slot_bytes - 4`.
    pub fn try_push(&mut self, data: &[u8]) -> bool {
        if data.len() + 4 > self.slot_bytes {
            return false;
        }
        let write = self.write_seq();
        let read = self.read_seq();
        if write.wrapping_sub(read) >= self.capacity as u64 {
            return false; // full
        }
        let offset = self.slot_offset(write);
        {
            let bytes = self.mapping.bytes_mut();
            let len = data.len() as u32;
            bytes[offset..offset + 4].copy_from_slice(&len.to_le_bytes());
            bytes[offset + 4..offset + 4 + data.len()].copy_from_slice(data);
        }
        fence(Ordering::Release);
        self.set_seq_at(RING_WRITE_SEQ_OFFSET, write + 1);
        true
    }

    /// Reads one message. Returns None when the ring is empty.
    pub fn try_pop(&mut self) -> Option<Vec<u8>> {
        let write = self.write_seq();
        let read = self.read_seq();
        if read >= write {
            return None;
        }
        fence(Ordering::Acquire);
        let offset = self.slot_offset(read);
        let data = {
            let bytes = self.mapping.bytes();
            let len = u32::from_le_bytes(bytes[offset..offset + 4].try_into().unwrap()) as usize;
            bytes[offset + 4..offset + 4 + len].to_vec()
        };
        fence(Ordering::Release);
        self.set_seq_at(RING_READ_SEQ_OFFSET, read + 1);
        Some(data)
    }
}

#[cfg(test)]
mod tests {
    use super::{FrameRing, RingHeader, SharedRing, GPU_SHARED_RING_HEADER_SIZE};
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
    #[test]
    fn shared_ring_push_pop_and_reopen() {
        let p = std::env::temp_dir().join(format!(
            "imagepad-gpu-ring-{}",
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let mut producer = SharedRing::create(&p, 4, 128).unwrap();
        assert!(producer.try_push(b"hello"));
        assert!(producer.try_push(&[0xAA; 124])); // max payload (128 - 4)
        assert!(!producer.try_push(&[0; 125])); // over slot capacity
                                                // Reopen from a "second process" and drain in order.
        let mut consumer = SharedRing::open(&p, 4, 128).unwrap();
        assert_eq!(consumer.try_pop().unwrap(), b"hello");
        assert_eq!(consumer.try_pop().unwrap(), vec![0xAA; 124]);
        assert!(consumer.try_pop().is_none());
        drop(consumer);
        drop(producer);
        let _ = std::fs::remove_file(&p);
    }
    #[test]
    fn shared_ring_full_rejects_until_popped() {
        let p = std::env::temp_dir().join(format!(
            "imagepad-gpu-ring-full-{}",
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let mut producer = SharedRing::create(&p, 1, 32).unwrap();
        assert!(producer.try_push(b"one"));
        assert!(!producer.try_push(b"two")); // capacity 1, full
        let mut consumer = SharedRing::open(&p, 1, 32).unwrap();
        assert_eq!(consumer.try_pop().unwrap(), b"one");
        assert!(producer.try_push(b"three"));
        assert_eq!(consumer.try_pop().unwrap(), b"three");
        drop(consumer);
        drop(producer);
        let _ = std::fs::remove_file(&p);
    }
}
