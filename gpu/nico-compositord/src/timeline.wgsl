struct DrawRecord {
    rect: vec4<f32>,
    projection: mat4x4<f32>,
    interval: vec4<i32>,
    movement: vec2<f32>,
    texture_extent_packed: u32,
    _padding: u32,
    motion_precision: vec4<u32>,
};

struct DrawRecords {
    values: array<DrawRecord>,
};

struct FrameInfo {
    vpos: i32,
    _padding0: i32,
    _padding1: i32,
    _padding2: i32,
};

struct DebugFrameVPosValues {
    values: array<i32>,
};

@group(0) @binding(0)
var<storage, read> draw_records: DrawRecords;

@group(0) @binding(1)
var<uniform> frame_info: FrameInfo;

@group(1) @binding(0)
var comment_texture: texture_2d<f32>;

@group(1) @binding(1)
var comment_sampler: sampler;

struct VertexOutput {
    @builtin(position) position: vec4<f32>,
    @location(0) uv: vec2<f32>,
    @location(1) alpha: f32,
    @location(2) @interpolate(flat) texture_region: vec4<i32>,
};

struct TimelineVertex {
    corner: vec2<f32>,
    pixel: vec4<f32>,
    clip: vec4<f32>,
};

struct PositionRecord {
    pixel: vec4<f32>,
    clip: vec4<f32>,
};

struct PositionRecords {
    values: array<PositionRecord>,
};

struct I96 {
    low: u32,
    mid: u32,
    high: u32,
}

fn multiply_u32(left: u32, right: u32) -> vec2<u32> {
    let left_low = left & 0xffffu;
    let left_high = left >> 16u;
    let right_low = right & 0xffffu;
    let right_high = right >> 16u;
    let word0 = left_low * right_low;
    let partial = left_high * right_low + (word0 >> 16u);
    let word1 = partial & 0xffffu;
    let word2 = partial >> 16u;
    let combined = left_low * right_high + word1;
    let high = left_high * right_high + word2 + (combined >> 16u);
    let low = (combined << 16u) | (word0 & 0xffffu);
    return vec2<u32>(low, high);
}

fn multiply_u64_u32(low: u32, high: u32, factor: u32) -> I96 {
    let low_product = multiply_u32(low, factor);
    let high_product = multiply_u32(high, factor);
    let middle = low_product.y + high_product.x;
    let carry = select(0u, 1u, middle < low_product.y);
    return I96(low_product.x, middle, high_product.y + carry);
}

fn negate_i96(value: I96) -> I96 {
    let low = ~value.low + 1u;
    let carry = select(0u, 1u, low == 0u);
    let middle = ~value.mid + carry;
    let middle_carry = select(0u, 1u, carry == 1u && middle == 0u);
    return I96(low, middle, ~value.high + middle_carry);
}

fn bit_i96(value: I96, index: u32) -> u32 {
    if (index < 32u) {
        return (value.low >> index) & 1u;
    }
    if (index < 64u) {
        return (value.mid >> (index - 32u)) & 1u;
    }
    return (value.high >> (index - 64u)) & 1u;
}

fn any_i96_bits_below(value: I96, bit_count: u32) -> bool {
    if (bit_count == 0u) {
        return false;
    }
    if (bit_count < 32u) {
        return (value.low & ((1u << bit_count) - 1u)) != 0u;
    }
    if (bit_count == 32u) {
        return value.low != 0u;
    }
    if (bit_count < 64u) {
        return value.low != 0u
            || (value.mid & ((1u << (bit_count - 32u)) - 1u)) != 0u;
    }
    if (bit_count == 64u) {
        return value.low != 0u || value.mid != 0u;
    }
    if (bit_count < 96u) {
        return value.low != 0u
            || value.mid != 0u
            || (value.high & ((1u << (bit_count - 64u)) - 1u)) != 0u;
    }
    return value.low != 0u || value.mid != 0u || value.high != 0u;
}

fn shifted_i96_low(value: I96, shift: u32) -> u32 {
    if (shift == 0u) {
        return value.low;
    }
    if (shift < 32u) {
        return (value.low >> shift) | (value.mid << (32u - shift));
    }
    if (shift == 32u) {
        return value.mid;
    }
    if (shift < 64u) {
        return (value.mid >> (shift - 32u)) | (value.high << (64u - shift));
    }
    if (shift == 64u) {
        return value.high;
    }
    return value.high >> (shift - 64u);
}

fn highest_i96_bit(value: I96) -> i32 {
    var index = 95i;
    loop {
        if (index < 0i) {
            return -1i;
        }
        if (bit_i96(value, u32(index)) != 0u) {
            return index;
        }
        index = index - 1i;
    }
    return -1i;
}

fn q40_to_f32_bits(signed_value: I96) -> u32 {
    let negative = (signed_value.high & 0x80000000u) != 0u;
    var magnitude = signed_value;
    if (negative) {
        magnitude = negate_i96(signed_value);
    }
    let highest = highest_i96_bit(magnitude);
    if (highest < 0i) {
        return 0u;
    }

    var exponent = highest - 40i;
    var significand: u32;
    if (highest <= 23i) {
        significand = magnitude.low << u32(23i - highest);
    } else {
        let shift = u32(highest - 23i);
        significand = shifted_i96_low(magnitude, shift);
        let guard = bit_i96(magnitude, shift - 1u) != 0u;
        let sticky = any_i96_bits_below(magnitude, shift - 1u);
        if (guard && (sticky || (significand & 1u) != 0u)) {
            significand = significand + 1u;
        }
        if (significand == 0x01000000u) {
            significand = significand >> 1u;
            exponent = exponent + 1i;
        }
    }

    let sign_bit = select(0u, 0x80000000u, negative);
    let exponent_bits = u32(exponent + 127i) << 23u;
    return sign_bit | exponent_bits | (significand & 0x007fffffu);
}

fn precise_moved_x(draw: DrawRecord, delta: i32) -> f32 {
    let payload = draw.motion_precision;
    let speed_negative = (payload.w & 0x80000000u) != 0u;
    var speed_low = payload.z;
    var speed_high = payload.w;
    if (speed_negative) {
        speed_low = ~speed_low + 1u;
        speed_high = ~speed_high + select(0u, 1u, speed_low == 0u);
    }

    let delta_negative = delta < 0i;
    let delta_bits = bitcast<u32>(delta);
    let delta_magnitude = select(delta_bits, 0u - delta_bits, delta_negative);
    var product = multiply_u64_u32(speed_low, speed_high, delta_magnitude);
    if (speed_negative != delta_negative) {
        product = negate_i96(product);
    }

    let base_sign_extension = select(0u, 0xffffffffu, (payload.y & 0x80000000u) != 0u);
    let low = product.low + payload.x;
    let low_carry = select(0u, 1u, low < product.low);
    let middle_without_carry = product.mid + payload.y;
    let middle_carry = select(0u, 1u, middle_without_carry < product.mid);
    let middle = middle_without_carry + low_carry;
    let middle_carry2 = select(0u, 1u, middle < middle_without_carry);
    let high = product.high + base_sign_extension + middle_carry + middle_carry2;
    let sum = I96(low, middle, high);
    return bitcast<f32>(q40_to_f32_bits(sum));
}

fn round_nearest_even_u32(value: f32) -> u32 {
    let lower_f32 = floor(value);
    let lower = u32(lower_f32);
    let fraction = value - lower_f32;
    if (fraction > 0.5 || (fraction == 0.5 && (lower & 1u) == 1u)) {
        return lower + 1u;
    }
    return lower;
}

fn round_nearest_even_div_65536(value: u32) -> u32 {
    let quotient = value / 65536u;
    let remainder = value % 65536u;
    if (remainder > 32768u || (remainder == 32768u && (quotient & 1u) == 1u)) {
        return quotient + 1u;
    }
    return quotient;
}

fn round_nearest_even_div_65536x4(value: vec4<u32>) -> vec4<u32> {
    return vec4<u32>(
        round_nearest_even_div_65536(value.x),
        round_nearest_even_div_65536(value.y),
        round_nearest_even_div_65536(value.z),
        round_nearest_even_div_65536(value.w),
    );
}

@group(0) @binding(2)
var<storage, read_write> debug_positions: PositionRecords;

@group(0) @binding(3)
var<storage, read> debug_frame_vpos: DebugFrameVPosValues;

fn compute_vertex_position(draw: DrawRecord, vertex_index: u32, vpos: i32) -> TimelineVertex {
    let x_is_one = vertex_index == 1u || vertex_index == 2u || vertex_index == 4u;
    let y_is_one = vertex_index == 2u || vertex_index == 4u || vertex_index == 5u;
    let corner = vec2<f32>(select(0.0, 1.0, x_is_one), select(0.0, 1.0, y_is_one));

    // Candidate bundles include a whole second. Keep the exact half-open time
    // test here so starts/ends never move early or linger for one frame.
    if (vpos < draw.interval.x || vpos >= draw.interval.y) {
        return TimelineVertex(
            corner,
            vec4<f32>(2.0, 2.0, 2.0, 0.0),
            vec4<f32>(2.0, 2.0, 2.0, 1.0),
        );
    }

    let vpos_delta = vpos - draw.interval.z;
    let x = precise_moved_x(draw, vpos_delta);
    let pixel = vec4<f32>(
        x + corner.x * draw.rect.z,
        draw.rect.y + corner.y * draw.rect.w,
        0.0,
        1.0,
    );
    var clip = draw.projection * pixel;
    // WebGL clip z is [-w,+w], while WGPU clips z to [0,+w]. The projection's
    // x/y values already use top-left pixel coordinates; do not invert y again.
    clip.z = (clip.z + clip.w) * 0.5;
    return TimelineVertex(corner, pixel, clip);
}

fn draw_texture_region(draw: DrawRecord) -> vec4<i32> {
    let packed_origin = bitcast<u32>(draw.interval.w);
    return vec4<i32>(
        i32(packed_origin & 0xffffu),
        i32(packed_origin >> 16u),
        i32(draw.texture_extent_packed & 0xffffu),
        i32(draw.texture_extent_packed >> 16u),
    );
}

@vertex
fn vs_main(
    @builtin(vertex_index) vertex_index: u32,
    @builtin(instance_index) instance_index: u32,
) -> VertexOutput {
    let draw = draw_records.values[instance_index];
    let vertex = compute_vertex_position(draw, vertex_index, frame_info.vpos);
    var output: VertexOutput;
    output.uv = vertex.corner;
    output.alpha = draw.movement.y;
    output.texture_region = draw_texture_region(draw);
    output.position = vertex.clip;
    return output;
}

// The ignored hardware qualification uses this vertex entry point to copy the
// exact shared production calculation into a storage buffer for readback.
@vertex
fn vs_debug(
    @builtin(vertex_index) vertex_index: u32,
    @builtin(instance_index) instance_index: u32,
) -> VertexOutput {
    let draw_count = arrayLength(&draw_records.values);
    let frame_index = instance_index / draw_count;
    let draw_index = instance_index % draw_count;
    let draw = draw_records.values[draw_index];
    let vertex = compute_vertex_position(draw, vertex_index, debug_frame_vpos.values[frame_index]);
    debug_positions.values[instance_index * 6u + vertex_index] =
        PositionRecord(vertex.pixel, vertex.clip);
    var output: VertexOutput;
    output.uv = vertex.corner;
    output.alpha = draw.movement.y;
    output.texture_region = draw_texture_region(draw);
    output.position = vertex.clip;
    return output;
}

@fragment
fn fs_main(input: VertexOutput) -> @location(0) vec4<f32> {
    // Timeline asset RGB is already premultiplied by its alpha. Applying the
    // per-comment opacity to all channels keeps the One/OneMinusSrcAlpha blend
    // contract correct. The checked browser reference is ANGLE/D3D11, whose
    // texture-filter contract requires 8-bit subtexel precision; explicitly
    // round the four-tap interpolation weights to that reference resolution.
    let size = input.texture_region.zw;
    let origin = input.texture_region.xy;
    let sample_position = input.uv * vec2<f32>(size) - vec2<f32>(0.5);
    let base = vec2<i32>(floor(sample_position));
    // Match the browser reference's 8-bit subtexel weights and byte-rounded
    // four-tap result while keeping sampling inside the wgpu shader path.
    let fraction = vec2<u32>(
        round_nearest_even_u32(fract(sample_position.x) * 256.0),
        round_nearest_even_u32(fract(sample_position.y) * 256.0),
    );
    let maximum = size - vec2<i32>(1);
    let p00 = vec4<u32>(round(textureLoad(comment_texture, origin + clamp(base, vec2<i32>(0), maximum), 0) * 255.0));
    let p10 = vec4<u32>(round(textureLoad(comment_texture, origin + clamp(base + vec2<i32>(1, 0), vec2<i32>(0), maximum), 0) * 255.0));
    let p01 = vec4<u32>(round(textureLoad(comment_texture, origin + clamp(base + vec2<i32>(0, 1), vec2<i32>(0), maximum), 0) * 255.0));
    let p11 = vec4<u32>(round(textureLoad(comment_texture, origin + clamp(base + vec2<i32>(1, 1), vec2<i32>(0), maximum), 0) * 255.0));
    let weight_x0 = 256u - fraction.x;
    let weight_y0 = 256u - fraction.y;
    let weighted = p00 * (weight_x0 * weight_y0)
        + p10 * (fraction.x * weight_y0)
        + p01 * (weight_x0 * fraction.y)
        + p11 * (fraction.x * fraction.y);
    let sampled8 = round_nearest_even_div_65536x4(weighted);
    let sampled = vec4<f32>(sampled8) / 255.0;
    let alpha = input.alpha;
    return sampled * alpha;
}

@fragment
fn fs_debug(input: VertexOutput) -> @location(0) vec4<f32> {
    return vec4<f32>(input.uv, input.alpha, 1.0);
}
