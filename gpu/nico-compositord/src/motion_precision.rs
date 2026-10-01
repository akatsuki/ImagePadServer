/// Errors returned when a motion value cannot be represented as signed Q40.40.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MotionPrecisionError {
    /// An input value was NaN or infinite.
    NonFinite,
    /// A finite value rounded outside the signed 64-bit Q40.40 range.
    OutOfRange,
}

/// Encodes the base X position and horizontal speed as signed Q40.40 limbs.
///
/// The returned words are `[base_low, base_high, speed_low, speed_high]` in
/// little-endian limb order. The rectangle offset is intentionally computed
/// in binary32 before it is widened and added to the binary64 anchor.
pub fn encode_motion_payload(
    anchor_x: f64,
    rect_x: f32,
    speed_x: f64,
) -> Result<[u32; 4], MotionPrecisionError> {
    if !anchor_x.is_finite() || !rect_x.is_finite() || !speed_x.is_finite() {
        return Err(MotionPrecisionError::NonFinite);
    }

    let rect_offset = f64::from(rect_x - (anchor_x as f32));
    let base_x = anchor_x + rect_offset;
    let base = quantize_q40_40(base_x)?;
    let speed = quantize_q40_40(speed_x)?;
    let base_bits = base as u64;
    let speed_bits = speed as u64;

    Ok([
        base_bits as u32,
        (base_bits >> 32) as u32,
        speed_bits as u32,
        (speed_bits >> 32) as u32,
    ])
}

fn quantize_q40_40(value: f64) -> Result<i64, MotionPrecisionError> {
    if !value.is_finite() {
        return Err(MotionPrecisionError::OutOfRange);
    }

    let bits = value.to_bits();
    let negative = bits >> 63 != 0;
    let exponent = ((bits >> 52) & 0x7ff) as i32;
    let fraction = bits & ((1_u64 << 52) - 1);

    let (significand, shift) = if exponent == 0 {
        (fraction, -1034)
    } else {
        ((1_u64 << 52) | fraction, exponent - 1035)
    };

    let magnitude = if significand == 0 {
        0
    } else if shift >= 0 {
        let shift = shift as u32;
        if shift >= 64 || significand > (u64::MAX >> shift) {
            return Err(MotionPrecisionError::OutOfRange);
        }
        significand << shift
    } else {
        let right_shift = (-shift) as u32;
        if right_shift >= 64 {
            0
        } else {
            let quotient = significand >> right_shift;
            let remainder_mask = (1_u64 << right_shift) - 1;
            let remainder = significand & remainder_mask;
            let halfway = 1_u64 << (right_shift - 1);
            quotient + u64::from(remainder > halfway || (remainder == halfway && quotient & 1 != 0))
        }
    };

    let limit = if negative {
        1_u64 << 63
    } else {
        i64::MAX as u64
    };
    if magnitude > limit {
        return Err(MotionPrecisionError::OutOfRange);
    }

    if negative {
        if magnitude == (1_u64 << 63) {
            Ok(i64::MIN)
        } else {
            Ok(-(magnitude as i64))
        }
    } else {
        Ok(magnitude as i64)
    }
}

#[cfg(test)]
mod tests {
    use super::{encode_motion_payload, MotionPrecisionError};

    fn decode_signed(low: u32, high: u32) -> i64 {
        i64::from_le_bytes([
            low as u8,
            (low >> 8) as u8,
            (low >> 16) as u8,
            (low >> 24) as u8,
            high as u8,
            (high >> 8) as u8,
            (high >> 16) as u8,
            (high >> 24) as u8,
        ])
    }

    #[test]
    fn rounds_positive_halfway_values_to_even_neighbors() {
        let half = 2.0_f64.powi(-41);
        let payload = encode_motion_payload(0.0, 0.0, half).unwrap();
        assert_eq!(decode_signed(payload[2], payload[3]), 0);

        let payload = encode_motion_payload(0.0, 0.0, 3.0 * half).unwrap();
        assert_eq!(decode_signed(payload[2], payload[3]), 2);

        let payload = encode_motion_payload(0.0, 0.0, 5.0 * half).unwrap();
        assert_eq!(decode_signed(payload[2], payload[3]), 2);

        let payload = encode_motion_payload(0.0, 0.0, 7.0 * half).unwrap();
        assert_eq!(decode_signed(payload[2], payload[3]), 4);
    }

    #[test]
    fn rounds_negative_halfway_values_to_even_neighbors() {
        let half = 2.0_f64.powi(-41);
        for (value, expected) in [
            (-half, 0),
            (-3.0 * half, -2),
            (-5.0 * half, -2),
            (-7.0 * half, -4),
        ] {
            let payload = encode_motion_payload(0.0, 0.0, value).unwrap();
            assert_eq!(decode_signed(payload[2], payload[3]), expected);
        }
    }

    #[test]
    fn accepts_signed_lower_boundary_and_rejects_upper_boundary() {
        let payload = encode_motion_payload(0.0, -(2.0_f32).powi(23), 0.0).unwrap();
        assert_eq!(decode_signed(payload[0], payload[1]), i64::MIN);
        assert_eq!(
            encode_motion_payload(0.0, (2.0_f32).powi(23), 0.0),
            Err(MotionPrecisionError::OutOfRange)
        );
    }

    #[test]
    fn rejects_non_finite_inputs() {
        for (anchor, rect, speed) in [
            (f64::NAN, 0.0, 0.0),
            (0.0, f32::INFINITY, 0.0),
            (0.0, 0.0, f64::NEG_INFINITY),
        ] {
            assert_eq!(
                encode_motion_payload(anchor, rect, speed),
                Err(MotionPrecisionError::NonFinite)
            );
        }
    }

    #[test]
    fn rejects_values_outside_signed_q40_40_range() {
        assert_eq!(
            encode_motion_payload(0.0, 0.0, (2.0_f64).powi(23)),
            Err(MotionPrecisionError::OutOfRange)
        );
        assert_eq!(
            encode_motion_payload(0.0, -(2.0_f32).powi(23) - 1.0, 0.0),
            Err(MotionPrecisionError::OutOfRange)
        );
    }

    #[test]
    fn returns_low_then_high_limbs_for_signed_twos_complement_values() {
        let payload = encode_motion_payload(0.0, -1.5, 2.25).unwrap();
        assert_eq!(payload[0], 0x0000_0000);
        assert_eq!(payload[1], 0xffff_fe80);
        assert_eq!(payload[2], 0x0000_0000);
        assert_eq!(payload[3], 0x0000_0240);
        assert_eq!(decode_signed(payload[0], payload[1]), -1_649_267_441_664);
        assert_eq!(decode_signed(payload[2], payload[3]), 2_473_901_162_496);
    }

    #[test]
    fn computes_rectangle_offset_in_binary32_before_widening_and_adding() {
        let payload = encode_motion_payload(16_777_217.0, 0.5, 0.0).unwrap();
        assert_eq!(decode_signed(payload[0], payload[1]), 1_i64 << 40);
    }
}
