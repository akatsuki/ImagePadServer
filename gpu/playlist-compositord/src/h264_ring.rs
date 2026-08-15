pub const MAX_H264_RING_SLOTS: usize = 64;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RingState {
    Free,
    RenderSubmitted,
    EncodeSubmitted,
    BitstreamReady,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RingError {
    InvalidCapacity,
    InvalidSlot,
    Full,
    InvalidState,
    FenceNotComplete,
    NotReady,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct FrameReceipt {
    pub sequence: u64,
    pub pts_ns: i64,
}

#[derive(Debug, Clone, Copy)]
struct RingSlot {
    state: RingState,
    sequence: u64,
    pts_ns: i64,
    render_fence: u64,
    encode_fence: u64,
}

pub struct BoundedH264Ring {
    slots: Vec<RingSlot>,
}

impl BoundedH264Ring {
    pub fn new(slot_count: usize) -> Result<Self, RingError> {
        if slot_count == 0 || slot_count > MAX_H264_RING_SLOTS {
            return Err(RingError::InvalidCapacity);
        }
        Ok(Self {
            slots: vec![
                RingSlot {
                    state: RingState::Free,
                    sequence: 0,
                    pts_ns: 0,
                    render_fence: 0,
                    encode_fence: 0,
                };
                slot_count
            ],
        })
    }

    pub fn acquire_render(&mut self, sequence: u64, pts_ns: i64) -> Result<usize, RingError> {
        let Some((slot_index, slot)) = self
            .slots
            .iter_mut()
            .enumerate()
            .find(|(_, slot)| slot.state == RingState::Free)
        else {
            return Err(RingError::Full);
        };
        slot.state = RingState::RenderSubmitted;
        slot.sequence = sequence;
        slot.pts_ns = pts_ns;
        slot.render_fence = 0;
        slot.encode_fence = 0;
        Ok(slot_index)
    }

    pub fn mark_encode_submitted(
        &mut self,
        slot_index: usize,
        render_fence: u64,
        encode_fence: u64,
    ) -> Result<(), RingError> {
        let slot = self
            .slots
            .get_mut(slot_index)
            .ok_or(RingError::InvalidSlot)?;
        if slot.state != RingState::RenderSubmitted || render_fence == 0 || encode_fence == 0 {
            return Err(RingError::InvalidState);
        }
        slot.render_fence = render_fence;
        slot.encode_fence = encode_fence;
        slot.state = RingState::EncodeSubmitted;
        Ok(())
    }

    pub fn mark_bitstream_ready(
        &mut self,
        slot_index: usize,
        completed_encode_fence: u64,
    ) -> Result<(), RingError> {
        let slot = self
            .slots
            .get_mut(slot_index)
            .ok_or(RingError::InvalidSlot)?;
        if slot.state != RingState::EncodeSubmitted {
            return Err(RingError::InvalidState);
        }
        if completed_encode_fence < slot.encode_fence {
            return Err(RingError::FenceNotComplete);
        }
        slot.state = RingState::BitstreamReady;
        Ok(())
    }

    pub fn release(&mut self, slot_index: usize) -> Result<FrameReceipt, RingError> {
        let slot = self
            .slots
            .get_mut(slot_index)
            .ok_or(RingError::InvalidSlot)?;
        if slot.state != RingState::BitstreamReady {
            return Err(RingError::InvalidState);
        }
        let receipt = FrameReceipt {
            sequence: slot.sequence,
            pts_ns: slot.pts_ns,
        };
        slot.state = RingState::Free;
        slot.sequence = 0;
        slot.pts_ns = 0;
        slot.render_fence = 0;
        slot.encode_fence = 0;
        Ok(receipt)
    }

    pub fn release_next_ordered(
        &mut self,
        expected_sequence: u64,
    ) -> Result<FrameReceipt, RingError> {
        let Some(index) = self
            .slots
            .iter()
            .position(|slot| slot.sequence == expected_sequence && slot.state != RingState::Free)
        else {
            return Err(RingError::NotReady);
        };
        if self.slots[index].state != RingState::BitstreamReady {
            return Err(RingError::NotReady);
        }
        self.release(index)
    }

    pub fn is_empty(&self) -> bool {
        self.slots.iter().all(|slot| slot.state == RingState::Free)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bounded_ring_enforces_render_encode_ready_release_order() {
        let mut ring = BoundedH264Ring::new(2).unwrap();
        let first = ring.acquire_render(10, 1_000).unwrap();
        let second = ring.acquire_render(11, 2_000).unwrap();
        assert_eq!(ring.acquire_render(12, 3_000), Err(RingError::Full));

        ring.mark_encode_submitted(first, 7, 11).unwrap();
        assert_eq!(
            ring.mark_bitstream_ready(first, 10),
            Err(RingError::FenceNotComplete)
        );
        ring.mark_bitstream_ready(first, 11).unwrap();
        assert_eq!(
            ring.release(first),
            Ok(FrameReceipt {
                sequence: 10,
                pts_ns: 1_000
            })
        );

        ring.mark_encode_submitted(second, 8, 12).unwrap();
        ring.mark_bitstream_ready(second, 12).unwrap();
        assert_eq!(
            ring.release(second),
            Ok(FrameReceipt {
                sequence: 11,
                pts_ns: 2_000
            })
        );
        assert!(ring.is_empty());
    }

    #[test]
    fn ring_rejects_resource_reuse_before_encoder_release() {
        let mut ring = BoundedH264Ring::new(1).unwrap();
        let slot = ring.acquire_render(1, 100).unwrap();
        ring.mark_encode_submitted(slot, 1, 5).unwrap();
        assert_eq!(ring.acquire_render(2, 200), Err(RingError::Full));
        assert_eq!(ring.release(slot), Err(RingError::InvalidState));
    }

    #[test]
    fn ordered_drain_does_not_emit_later_sequence_first() {
        let mut ring = BoundedH264Ring::new(2).unwrap();
        let first = ring.acquire_render(20, 2_000).unwrap();
        let second = ring.acquire_render(21, 3_000).unwrap();
        ring.mark_encode_submitted(first, 1, 10).unwrap();
        ring.mark_encode_submitted(second, 2, 11).unwrap();
        ring.mark_bitstream_ready(second, 11).unwrap();
        assert_eq!(ring.release_next_ordered(20), Err(RingError::NotReady));
        ring.mark_bitstream_ready(first, 10).unwrap();
        assert_eq!(
            ring.release_next_ordered(20),
            Ok(FrameReceipt {
                sequence: 20,
                pts_ns: 2_000
            })
        );
        assert_eq!(
            ring.release_next_ordered(21),
            Ok(FrameReceipt {
                sequence: 21,
                pts_ns: 3_000
            })
        );
    }

    #[test]
    fn ring_capacity_is_bounded_to_eight_slots() {
        assert!(matches!(
            BoundedH264Ring::new(0),
            Err(RingError::InvalidCapacity)
        ));
        assert!(matches!(
            BoundedH264Ring::new(MAX_H264_RING_SLOTS + 1),
            Err(RingError::InvalidCapacity)
        ));
        assert!(BoundedH264Ring::new(MAX_H264_RING_SLOTS).is_ok());
    }
}
