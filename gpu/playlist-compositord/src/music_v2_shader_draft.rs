#![allow(dead_code)]

use super::music_v2_pass_graph::{Pass, CANONICAL_ORDER};
use super::music_v2_shader_module::{
    ShaderBinding, ShaderBindingKind, ShaderModule, ShaderModuleDescriptor,
};

/// Draft-only shader contract. This module is intentionally not wired into the
/// legacy renderer or RenderV2 dispatch yet; it is the revision seam for the
/// next shader iteration.
pub const DRAFT_SHADER_VERSION: u16 = 2;
pub const DRAFT_WORKGROUP_SIZE: (u32, u32, u32) = (8, 8, 1);

/// Keep the pass order as data so each stage can be replaced independently.
pub const DRAFT_PASS_ORDER: [Pass; 9] = CANONICAL_ORDER;

pub const DRAFT_BINDINGS: [ShaderBinding; 10] = [
    ShaderBinding {
        index: 0,
        name: "pcm_input",
        kind: ShaderBindingKind::StorageRead,
    },
    ShaderBinding {
        index: 1,
        name: "scene",
        kind: ShaderBindingKind::Uniform,
    },
    ShaderBinding {
        index: 2,
        name: "waveform_q16",
        kind: ShaderBindingKind::StorageRead,
    },
    ShaderBinding {
        index: 3,
        name: "glyph_atlas",
        kind: ShaderBindingKind::SampledTextureRgba8,
    },
    ShaderBinding {
        index: 4,
        name: "glyph_metrics",
        kind: ShaderBindingKind::StorageRead,
    },
    ShaderBinding {
        index: 5,
        name: "glyph_sampler",
        kind: ShaderBindingKind::Sampler,
    },
    ShaderBinding {
        index: 6,
        name: "artwork_texture",
        kind: ShaderBindingKind::SampledTextureRgba8,
    },
    ShaderBinding {
        index: 7,
        name: "luma_output",
        kind: ShaderBindingKind::StorageTextureRgba8,
    },
    ShaderBinding {
        index: 8,
        name: "chroma_output",
        kind: ShaderBindingKind::StorageTextureRgba8Chroma,
    },
    ShaderBinding {
        index: 9,
        name: "rgb_output",
        kind: ShaderBindingKind::StorageTextureRgba8,
    },
];

pub struct DraftShaderModule;

pub const DRAFT_SHADER_MODULE: DraftShaderModule = DraftShaderModule;

/// H.264 uses only the RGB surface that is handed to NVENC. Keeping this as
/// a separate entry point lets the backend compiler omit the YUV/chroma write
/// path while retaining the canonical RGB scene implementation.
pub const H264_SHADER_ENTRY_POINT: &str = "render_h264";
pub const H264_WORKGROUP_SIZE: (u32, u32, u32) = (8, 8, 1);

impl ShaderModule for DraftShaderModule {
    fn descriptor(&self) -> ShaderModuleDescriptor {
        ShaderModuleDescriptor {
            version: DRAFT_SHADER_VERSION,
            entry_point: "render_draft",
            workgroup_size: DRAFT_WORKGROUP_SIZE,
            bindings: &DRAFT_BINDINGS,
            passes: &DRAFT_PASS_ORDER,
            gpu_owned_output: true,
            production_ready: false,
        }
    }

    fn wgsl(&self) -> &'static str {
        DRAFT_SHADER_WGSL
    }
}

pub struct H264ShaderModule;

pub const H264_SHADER_MODULE: H264ShaderModule = H264ShaderModule;

impl ShaderModule for H264ShaderModule {
    fn descriptor(&self) -> ShaderModuleDescriptor {
        ShaderModuleDescriptor {
            version: DRAFT_SHADER_VERSION,
            entry_point: H264_SHADER_ENTRY_POINT,
            workgroup_size: H264_WORKGROUP_SIZE,
            bindings: &DRAFT_BINDINGS,
            passes: &DRAFT_PASS_ORDER,
            gpu_owned_output: true,
            production_ready: false,
        }
    }

    fn wgsl(&self) -> &'static str {
        DRAFT_SHADER_WGSL
    }
}

/// Draft 2 WGSL shader.
///
/// This is a real diagnostic shader rather than a pass-marker placeholder. It
/// consumes a CPU-like scene payload and a GPU-readable font atlas:
/// canonical layout rects, palette, 24 spectrum bands, a 1000-point loudness
/// graph, fallback artwork, waveform, metadata-shaped text rows, progress, and
/// fade. The output remains GPU-owned YUV planes; only the diagnostic exporter
/// reads them back.
pub const DRAFT_SHADER_WGSL: &str = r#"
const TAU: f32 = 6.28318530718;

struct DraftScene {
    width: u32,
    height: u32,
    frame_index: u32,
    pcm_sample_count: u32,
    pcm_sample_offset: u32,
    fps: u32,
    total_frames: u32,
    waveform_flags: u32,
    progress_q16: u32,
    fade_in_q16: u32,
    fade_out_q16: u32,
    title_len: u32,
    artist_len: u32,
    album_len: u32,
    time_len: u32,
    waveform_columns: u32,
    palette: array<vec4<u32>, 4>,
    rects: array<vec4<i32>, 8>,
    spectrum: array<vec4<u32>, 6>,
    loudness: array<vec4<u32>, 250>,
    text: array<vec4<u32>, 32>,
};

struct PcmInput {
    samples: array<f32>,
};

struct WaveformInput {
    samples: array<u32>,
};

struct AudioMetrics {
    rms: f32,
    peak: f32,
    bass: f32,
    mid: f32,
    treble: f32,
};

@group(0) @binding(0)
var<storage, read> pcm_input: PcmInput;

@group(0) @binding(1)
var<uniform> scene: DraftScene;

@group(0) @binding(2)
var<storage, read> waveform_samples: WaveformInput;

@group(0) @binding(3)
var glyph_atlas: texture_2d<f32>;

struct GlyphMetricsInput {
    advances: array<u32>,
};

@group(0) @binding(4)
var<storage, read> glyph_metrics: GlyphMetricsInput;

@group(0) @binding(5)
var glyph_sampler: sampler;

@group(0) @binding(6)
var artwork_texture: texture_2d<f32>;

@group(0) @binding(7)
var luma_output: texture_storage_2d<rgba8unorm, write>;

@group(0) @binding(8)
var chroma_output: texture_storage_2d<rgba8unorm, write>;

@group(0) @binding(9)
var rgb_output: texture_storage_2d<rgba8unorm, write>;

fn sample_pcm(index: u32) -> f32 {
    let count = max(scene.pcm_sample_count, 1u);
    let wrapped = (scene.pcm_sample_offset + index) % count;
    return pcm_input.samples[wrapped];
}

fn audio_rms() -> f32 {
    var sum = 0.0;
    for (var tap = 0u; tap < 16u; tap = tap + 1u) {
        let sample = sample_pcm(tap * 3u);
        sum = sum + sample * sample;
    }
    return clamp(sqrt(sum / 16.0), 0.0, 1.0);
}

fn audio_peak() -> f32 {
    var peak = 0.0;
    for (var tap = 0u; tap < 16u; tap = tap + 1u) {
        peak = max(peak, abs(sample_pcm(tap * 3u)));
    }
    return clamp(peak, 0.0, 1.0);
}

fn audio_band_energy(band: u32) -> f32 {
    let frequency = 0.25 + f32(band) * 0.18;
    var real_part = 0.0;
    var imaginary_part = 0.0;
    for (var tap = 0u; tap < 12u; tap = tap + 1u) {
        let sample = sample_pcm(band * 17u + tap * 5u);
        let phase = TAU * frequency * f32(tap) / 12.0;
        real_part = real_part + sample * cos(phase);
        imaginary_part = imaginary_part + sample * sin(phase);
    }
    return clamp(sqrt(real_part * real_part + imaginary_part * imaginary_part) / 6.0, 0.0, 1.0);
}

fn audio_metrics() -> AudioMetrics {
    var metrics: AudioMetrics;
    metrics.rms = audio_rms();
    metrics.peak = audio_peak();
    metrics.bass = audio_band_energy(0u);
    metrics.mid = audio_band_energy(3u);
    metrics.treble = audio_band_energy(7u);
    return metrics;
}

fn waveform_signal(x: f32) -> f32 {
    let count = max(scene.pcm_sample_count, 1u);
    let index = min(u32(x * f32(count - 1u)), count - 1u);
    let current = sample_pcm(index);
    let next = sample_pcm(index + 1u);
    return (current + next) * 0.5;
}

fn waveform_q16(index: u32) -> f32 {
    return f32(waveform_samples.samples[index]) / 65535.0;
}

fn signed_minmax_sample(column: u32) -> vec2<f32> {
    let low = waveform_q16(column * 2u) * 2.0 - 1.0;
    let high = waveform_q16(column * 2u + 1u) * 2.0 - 1.0;
    return vec2<f32>(low, high);
}

fn waveform_minmax(local_x: f32) -> vec2<f32> {
    let columns = max(scene.waveform_columns, 1u);
    let position = clamp(local_x, 0.0, 1.0) * f32(columns - 1u);
    let c0 = min(columns - 1u, u32(position));
    let c1 = min(columns - 1u, c0 + 1u);
    let fraction = position - f32(c0);
    let bounds0 = signed_minmax_sample(c0);
    let bounds1 = signed_minmax_sample(c1);
    return mix(bounds0, bounds1, fraction);
}

fn pixel_uv(pixel: vec2<u32>) -> vec2<f32> {
    let safe_width = max(scene.width, 1u);
    let safe_height = max(scene.height, 1u);
    let x = min(pixel.x, safe_width - 1u);
    let y = min(pixel.y, safe_height - 1u);
    return vec2<f32>(
        f32(x) / max(f32(safe_width - 1u), 1.0),
        f32(y) / max(f32(safe_height - 1u), 1.0),
    );
}

fn line_mask(distance: f32, width: f32) -> f32 {
    return 1.0 - smoothstep(0.0, width, abs(distance));
}

fn pixel_position(uv: vec2<f32>) -> vec2<f32> {
    return vec2<f32>(
        uv.x * f32(max(scene.width, 1u) - 1u),
        uv.y * f32(max(scene.height, 1u) - 1u),
    );
}

fn palette_rgb(index: u32) -> vec3<f32> {
    return vec3<f32>(scene.palette[index].xyz) / 255.0;
}

fn rect_contains(pixel: vec2<f32>, rect: vec4<i32>) -> bool {
    return pixel.x >= f32(rect.x) && pixel.x < f32(rect.x + rect.z)
        && pixel.y >= f32(rect.y) && pixel.y < f32(rect.y + rect.w);
}

fn spectrum_q16(index: u32) -> f32 {
    return f32(scene.spectrum[index / 4u][index % 4u]) / 65535.0;
}

fn loudness_q16(index: u32) -> f32 {
    let bounded = min(index, 999u);
    return f32(scene.loudness[bounded / 4u][bounded % 4u]) / 65535.0;
}

fn fallback_note(local: vec2<f32>) -> f32 {
    let head = 1.0 - smoothstep(0.0, 0.045, distance(local, vec2<f32>(0.43, 0.70)));
    let stem = select(0.0, 1.0, local.x > 0.55 && local.x < 0.59 && local.y > 0.25 && local.y < 0.70);
    let flag = select(0.0, 1.0, local.x >= 0.56 && local.x < 0.76 && local.y > 0.24 && local.y < 0.30);
    return max(head, max(stem, flag));
}

fn pass_artwork_preprocess(uv: vec2<f32>, metrics: AudioMetrics) -> vec3<f32> {
    let pixel = pixel_position(uv);
    let background = palette_rgb(2u);
    let accent = palette_rgb(1u);
    var color = mix(background * 0.72, background, uv.y);
    let artwork_rect = scene.rects[0];
    if (rect_contains(pixel, artwork_rect)) {
        let local = (pixel - vec2<f32>(f32(artwork_rect.x), f32(artwork_rect.y)))
            / vec2<f32>(f32(artwork_rect.z), f32(artwork_rect.w));
        let artwork_sample = textureSampleLevel(
            artwork_texture,
            glyph_sampler,
            clamp(local, vec2<f32>(0.0), vec2<f32>(0.9999)),
            0.0,
        );
        var tile = mix(background, accent, clamp(local.y, 0.0, 1.0));
        if (artwork_sample.a > 0.5) {
            tile = artwork_sample.rgb;
        } else {
            let centered = local - vec2<f32>(0.5, 0.5);
            let radius = length(centered);
            let angle = atan2(centered.y, centered.x);
            let sector = (angle + 3.14159265) / TAU * 64.0;
            let band = min(23u, u32(max(0.0, floor(sector))) % 24u);
            let energy = spectrum_q16(band);
            let fingerprint = select(0.0, 0.26,
                radius > 0.18 && radius < 0.44 + energy * 0.10
                    && abs(fract(sector) - 0.5) < 0.10);
            tile = mix(tile, palette_rgb(0u), fingerprint);
            let note = fallback_note(local) * (0.82 + metrics.rms * 0.12);
            tile = mix(tile, vec3<f32>(1.0), clamp(note, 0.0, 1.0));
        }
        color = tile;
    }
    return color;
}

fn pass_background(color: vec3<f32>, uv: vec2<f32>, metrics: AudioMetrics) -> vec3<f32> {
    let pixel = pixel_position(uv);
    if (rect_contains(pixel, scene.rects[0])) {
        return color;
    }
    let background = palette_rgb(2u);
    let accent = palette_rgb(1u);
    let top = mix(background * 0.74, background, uv.y);
    let ambient = accent * (0.025 + metrics.rms * 0.025)
        * (1.0 - smoothstep(0.0, 0.75, distance(uv, vec2<f32>(0.42, 0.45))));
    return top + ambient;
}

fn pass_spectrum(color: vec3<f32>, uv: vec2<f32>) -> vec3<f32> {
    let pixel = pixel_position(uv);
    let rect = scene.rects[4];
    if (!rect_contains(pixel, rect)) {
        return color;
    }
    let scale = f32(scene.width) / 1280.0;
    let bar_w = max(1.0, round(18.0 * scale));
    let bar_gap = max(1.0, round(13.0 * scale));
    let first_x = f32(rect.x) + round(11.0 * scale);
    let bottom = f32(rect.y + rect.w);
    let max_height = f32(rect.w) - round(16.0 * scale);
    let min_height = max(1.0, round(4.0 * scale));
    let grid = (pixel.x - first_x) / max(1.0, bar_w + bar_gap);
    if (grid < 0.0 || grid >= 24.0) {
        return color;
    }
    let band = min(23u, u32(floor(grid)));
    let level = spectrum_q16(band);
    let bar_height = floor(min_height + level * max(0.0, max_height - min_height));
    let bar_x = first_x + f32(band) * (bar_w + bar_gap);
    let bar_y = bottom - bar_height;
    if (pixel.x < bar_x || pixel.x >= bar_x + bar_w || pixel.y < bar_y || pixel.y >= bottom) {
        return color;
    }
    let fade_px = max(1.0, round(10.0 * scale));
    let effective_fade = min(fade_px, bar_height);
    let bottom_distance = bar_height - 1.0 - (pixel.y - bar_y);
    var alpha = 0.82;
    if (bottom_distance < effective_fade) {
        alpha = select(0.0, 0.82 * bottom_distance / max(1.0, effective_fade - 1.0), effective_fade > 1.0);
    }
    return mix(color, palette_rgb(1u), clamp(alpha, 0.0, 1.0));
}

fn pass_waveform(color: vec3<f32>, uv: vec2<f32>, metrics: AudioMetrics) -> vec3<f32> {
    let pixel = pixel_position(uv);
    let rect = scene.rects[4];
    if (!rect_contains(pixel, rect)) {
        return color;
    }
    let local_x = clamp(
        (pixel.x - f32(rect.x)) / max(1.0, f32(rect.z - 1)),
        0.0,
        1.0,
    );
    let has_waveform = (scene.waveform_flags & 1u) != 0u && scene.waveform_columns > 0u;
    var line = 0.0;
    if (has_waveform) {
        let signed_minmax = (scene.waveform_flags & 2u) != 0u;
        let signed_mode = (scene.waveform_flags & 4u) != 0u;
        var y0 = 0.0;
        var y1 = 0.0;
        if (signed_minmax) {
            let bounds = waveform_minmax(local_x);
            y0 = f32(rect.y) + f32(rect.w) * 0.5 - bounds.y * f32(rect.w) * 0.5;
            y1 = f32(rect.y) + f32(rect.w) * 0.5 - bounds.x * f32(rect.w) * 0.5;
        } else {
            let columns = max(scene.waveform_columns, 1u);
            let position = local_x * f32(columns - 1u);
            let c0 = min(columns - 1u, u32(position));
            let c1 = min(columns - 1u, c0 + 1u);
            let fraction = position - f32(c0);
            let raw0 = mix(waveform_q16(c0), waveform_q16(c1), fraction);
            let amplitude = select(raw0, raw0 * 2.0 - 1.0, signed_mode);
            y0 = select(
                f32(rect.y + rect.w) - amplitude * f32(rect.w),
                f32(rect.y) + f32(rect.w) * 0.5 - amplitude * f32(rect.w) * 0.5,
                signed_mode,
            );
            y1 = y0;
        }
        let pixel_y = pixel.y + 0.5;
        line = select(
            0.0,
            1.0,
            pixel_y >= min(y0, y1) - 0.5 && pixel_y <= max(y0, y1) + 0.5,
        );
    } else {
        let signal = waveform_signal(local_x);
        let center = f32(rect.y) + f32(rect.w) * 0.58
            - signal * f32(rect.w) * (0.18 + metrics.rms * 0.12);
        line = 1.0 - smoothstep(
            0.0,
            max(1.0, f32(scene.width) / 1280.0 * 2.0),
            abs(pixel.y - center),
        );
    }
    return mix(color, palette_rgb(0u), clamp(line * 0.55, 0.0, 1.0));
}

fn pass_loudness(color: vec3<f32>, uv: vec2<f32>, _metrics: AudioMetrics) -> vec3<f32> {
    let pixel = pixel_position(uv);
    let rect = scene.rects[5];
    if (!rect_contains(pixel, rect)) {
        return color;
    }
    let local_x = clamp((pixel.x - f32(rect.x)) / max(1.0, f32(rect.z - 1)), 0.0, 1.0);
    let sample = min(999u, u32(round(local_x * 999.0)));
    let envelope = loudness_q16(sample);
    let graph_y = f32(rect.y + rect.w) - round(envelope * f32(rect.w));
    let line = select(0.0, 0.80, abs(pixel.y - graph_y) < 1.0);
    var guides = 0.0;
    let guide0 = f32(rect.y) + round(f32(rect.w) * (6.0 / 80.0));
    let guide1 = f32(rect.y) + round(f32(rect.w) * (28.0 / 80.0));
    let guide2 = f32(rect.y) + round(f32(rect.w) * (50.0 / 80.0));
    let guide3 = f32(rect.y) + round(f32(rect.w) * (72.0 / 80.0));
    guides = max(guides, select(0.0, 0.22, abs(pixel.y - guide0) < 0.5));
    guides = max(guides, select(0.0, 0.22, abs(pixel.y - guide1) < 0.5));
    guides = max(guides, select(0.0, 0.22, abs(pixel.y - guide2) < 0.5));
    guides = max(guides, select(0.0, 0.22, abs(pixel.y - guide3) < 0.5));
    return mix(color, palette_rgb(1u), clamp(max(line, guides), 0.0, 1.0));
}

fn glyph_bitmap(code: u32) -> u32 {
    var normalized = code;
    if (normalized >= 97u && normalized <= 122u) {
        normalized = normalized - 32u;
    }
    switch (normalized) {
        case 32u: { return 0u; }
        case 45u: { return 0u | (0u << 4u) | (15u << 8u) | (0u << 12u) | (0u << 16u); }
        case 46u: { return 0u | (0u << 4u) | (0u << 8u) | (0u << 12u) | (4u << 16u); }
        case 48u: { return 6u | (9u << 4u) | (11u << 8u) | (13u << 12u) | (6u << 16u); }
        case 49u: { return 4u | (12u << 4u) | (4u << 8u) | (4u << 12u) | (14u << 16u); }
        case 50u: { return 14u | (1u << 4u) | (6u << 8u) | (8u << 12u) | (15u << 16u); }
        case 51u: { return 14u | (1u << 4u) | (6u << 8u) | (1u << 12u) | (14u << 16u); }
        case 52u: { return 9u | (9u << 4u) | (15u << 8u) | (1u << 12u) | (1u << 16u); }
        case 53u: { return 15u | (8u << 4u) | (14u << 8u) | (1u << 12u) | (14u << 16u); }
        case 54u: { return 7u | (8u << 4u) | (14u << 8u) | (9u << 12u) | (6u << 16u); }
        case 55u: { return 15u | (1u << 4u) | (2u << 8u) | (4u << 12u) | (4u << 16u); }
        case 56u: { return 6u | (9u << 4u) | (6u << 8u) | (9u << 12u) | (6u << 16u); }
        case 57u: { return 6u | (9u << 4u) | (7u << 8u) | (1u << 12u) | (14u << 16u); }
        case 58u: { return 0u | (6u << 4u) | (0u << 8u) | (6u << 12u) | (0u << 16u); }
        case 65u: { return 6u | (9u << 4u) | (15u << 8u) | (9u << 12u) | (9u << 16u); }
        case 66u: { return 14u | (9u << 4u) | (14u << 8u) | (9u << 12u) | (14u << 16u); }
        case 67u: { return 7u | (8u << 4u) | (8u << 8u) | (8u << 12u) | (7u << 16u); }
        case 68u: { return 14u | (9u << 4u) | (9u << 8u) | (9u << 12u) | (14u << 16u); }
        case 69u: { return 15u | (8u << 4u) | (14u << 8u) | (8u << 12u) | (15u << 16u); }
        case 70u: { return 15u | (8u << 4u) | (14u << 8u) | (8u << 12u) | (8u << 16u); }
        case 71u: { return 7u | (8u << 4u) | (11u << 8u) | (9u << 12u) | (7u << 16u); }
        case 72u: { return 9u | (9u << 4u) | (15u << 8u) | (9u << 12u) | (9u << 16u); }
        case 73u: { return 15u | (6u << 4u) | (6u << 8u) | (6u << 12u) | (15u << 16u); }
        case 74u: { return 1u | (1u << 4u) | (1u << 8u) | (9u << 12u) | (6u << 16u); }
        case 75u: { return 9u | (10u << 4u) | (12u << 8u) | (10u << 12u) | (9u << 16u); }
        case 76u: { return 8u | (8u << 4u) | (8u << 8u) | (8u << 12u) | (15u << 16u); }
        case 77u: { return 9u | (15u << 4u) | (15u << 8u) | (9u << 12u) | (9u << 16u); }
        case 78u: { return 9u | (13u << 4u) | (11u << 8u) | (9u << 12u) | (9u << 16u); }
        case 79u: { return 6u | (9u << 4u) | (9u << 8u) | (9u << 12u) | (6u << 16u); }
        case 80u: { return 14u | (9u << 4u) | (14u << 8u) | (8u << 12u) | (8u << 16u); }
        case 81u: { return 6u | (9u << 4u) | (9u << 8u) | (11u << 12u) | (7u << 16u); }
        case 82u: { return 14u | (9u << 4u) | (14u << 8u) | (10u << 12u) | (9u << 16u); }
        case 83u: { return 7u | (8u << 4u) | (6u << 8u) | (1u << 12u) | (14u << 16u); }
        case 84u: { return 15u | (6u << 4u) | (6u << 8u) | (6u << 12u) | (6u << 16u); }
        case 85u: { return 9u | (9u << 4u) | (9u << 8u) | (9u << 12u) | (6u << 16u); }
        case 86u: { return 9u | (9u << 4u) | (9u << 8u) | (6u << 12u) | (6u << 16u); }
        case 87u: { return 9u | (9u << 4u) | (15u << 8u) | (15u << 12u) | (9u << 16u); }
        case 88u: { return 9u | (9u << 4u) | (6u << 8u) | (9u << 12u) | (9u << 16u); }
        case 89u: { return 9u | (9u << 4u) | (6u << 8u) | (6u << 12u) | (6u << 16u); }
        case 90u: { return 15u | (1u << 4u) | (6u << 8u) | (8u << 12u) | (15u << 16u); }
        default: { return 0u; }
    }
}

fn text_length(line: u32) -> u32 {
    if (line == 0u) { return scene.title_len; }
    if (line == 1u) { return scene.artist_len; }
    if (line == 2u) { return scene.album_len; }
    return scene.time_len;
}

fn text_codepoint(line: u32, index: u32) -> u32 {
    let packed = scene.text[line * 8u + index / 4u];
    return packed[index % 4u];
}

fn glyph_index_for_codepoint(codepoint: u32) -> u32 {
    if (codepoint >= 32u && codepoint <= 126u) {
        return codepoint - 32u;
    }
    if (codepoint == 36196u) { // U+8D64 赤
        return 95u;
    }
    if (codepoint == 26376u) { // U+6708 月
        return 96u;
    }
    return 31u; // '?'
}

fn text_mask(pixel: vec2<f32>, rect: vec4<i32>, line: u32) -> f32 {
    if (!rect_contains(pixel, rect)) {
        return 0.0;
    }
    let length = text_length(line);
    if (length == 0u) {
        return 0.0;
    }
    let local = (pixel - vec2<f32>(f32(rect.x), f32(rect.y)))
        / vec2<f32>(f32(rect.z), f32(rect.w));
    let cursor = min(0.9999, max(0.0, local.x)) * f32(length);
    let character = min(length - 1u, u32(floor(cursor)));
    let cell = fract(cursor);
    let column = u32(floor(cell * 5.0));
    if (column >= 4u) {
        return 0.0;
    }
    let row = min(4u, u32(floor(clamp(local.y, 0.0, 0.9999) * 5.0)));
    let bitmap = glyph_bitmap(text_codepoint(line, character));
    let glyph_column = 3u - column;
    return f32((bitmap >> (row * 4u + glyph_column)) & 1u);
}

fn font_style(line: u32) -> u32 {
    if (line == 0u || line == 3u) {
        return 2u;
    }
    if (line == 1u) {
        return 1u;
    }
    return 0u;
}

fn font_glyph_advance(style: u32, glyph_index: u32) -> f32 {
    let metric_index = min(290u, style * 97u + min(glyph_index, 96u));
    return max(0.01, f32(glyph_metrics.advances[metric_index]) / 65536.0);
}

fn font_text_advance(rect: vec4<i32>, line: u32) -> f32 {
    let length = text_length(line);
    let style = font_style(line);
    var normalized_width = 0.0;
    for (var index = 0u; index < 32u; index = index + 1u) {
        if (index >= length) {
            break;
        }
        let codepoint = text_codepoint(line, index);
        let glyph_index = glyph_index_for_codepoint(codepoint);
        normalized_width = normalized_width
            + font_glyph_advance(style, glyph_index);
    }
    return min(f32(rect.z), max(1.0, normalized_width * f32(rect.w)));
}

fn sample_glyph_alpha(atlas_uv: vec2<f32>, aa_step: vec2<f32>) -> f32 {
    let top_left = textureSampleLevel(
        glyph_atlas,
        glyph_sampler,
        atlas_uv + vec2<f32>(-aa_step.x, -aa_step.y),
        0.0,
    ).a;
    let top_right = textureSampleLevel(
        glyph_atlas,
        glyph_sampler,
        atlas_uv + vec2<f32>(aa_step.x, -aa_step.y),
        0.0,
    ).a;
    let bottom_left = textureSampleLevel(
        glyph_atlas,
        glyph_sampler,
        atlas_uv + vec2<f32>(-aa_step.x, aa_step.y),
        0.0,
    ).a;
    let bottom_right = textureSampleLevel(
        glyph_atlas,
        glyph_sampler,
        atlas_uv + aa_step,
        0.0,
    ).a;
    return (top_left + top_right + bottom_left + bottom_right) * 0.25;
}

fn font_text_mask(pixel: vec2<f32>, rect: vec4<i32>, line: u32) -> f32 {
    if (!rect_contains(pixel, rect)) {
        return 0.0;
    }
    let length = text_length(line);
    if (length == 0u) {
        return 0.0;
    }
    let text_width = font_text_advance(rect, line);
    let local_x = pixel.x - f32(rect.x);
    if (local_x < 0.0 || local_x >= text_width) {
        return 0.0;
    }

    let style = font_style(line);
    var character = 0u;
    var pen_x = 0.0;
    var character_advance = 1.0;
    for (var candidate = 0u; candidate < 32u; candidate = candidate + 1u) {
        if (candidate >= length) {
            break;
        }
        let candidate_codepoint = text_codepoint(line, candidate);
        let candidate_glyph_index = glyph_index_for_codepoint(candidate_codepoint);
        let candidate_advance = font_glyph_advance(style, candidate_glyph_index);
        let candidate_width = candidate_advance * f32(rect.w);
        if (local_x < pen_x + candidate_width || candidate + 1u >= length) {
            character = candidate;
            character_advance = candidate_advance;
            break;
        }
        pen_x = pen_x + candidate_width;
    }

    let codepoint = text_codepoint(line, character);
    let glyph_index = glyph_index_for_codepoint(codepoint);
    let glyph_local_x = clamp(
        (local_x - pen_x) / max(1.0, character_advance * f32(rect.w)),
        0.0,
        0.9999,
    );
    let cell_x = (glyph_index % 16u) * 64u;
    let cell_y = style * 448u + (glyph_index / 16u) * 64u;
    let local_y = (pixel.y - f32(rect.y)) / f32(rect.w);
    let cell_width = character_advance * 64.0;
    let cell = vec2<f32>(
        (64.0 - cell_width) * 0.5 + glyph_local_x * cell_width,
        clamp(local_y, 0.0, 0.9999) * 64.0,
    );
    let atlas_position = vec2<f32>(f32(cell_x), f32(cell_y))
        + cell
        + vec2<f32>(0.5, 0.5);
    let atlas_uv = atlas_position
        / vec2<f32>(1024.0, 1344.0);
    let aa_step = vec2<f32>(
        16.0 / f32(rect.w) / 1024.0,
        16.0 / f32(rect.w) / 1344.0,
    );
    return sample_glyph_alpha(atlas_uv, aa_step);
}

fn pass_text_ui(color: vec3<f32>, uv: vec2<f32>) -> vec3<f32> {
    let pixel = pixel_position(uv);
    let title = font_text_mask(pixel, scene.rects[1], 0u);
    let artist = font_text_mask(pixel, scene.rects[2], 1u);
    let album = font_text_mask(pixel, scene.rects[3], 2u);
    let time = font_text_mask(pixel, scene.rects[7], 3u);
    let primary = palette_rgb(0u);
    let accent = palette_rgb(1u);
    var result = mix(color, primary, title * 0.92);
    result = mix(result, accent, artist * 0.68);
    result = mix(result, primary, album * 0.46);
    result = mix(result, accent, time * 0.60);
    return result;
}

fn rounded_rect_mask(pixel: vec2<f32>, rect: vec4<i32>, radius: f32) -> f32 {
    if (!rect_contains(pixel, rect)) {
        return 0.0;
    }
    let center = vec2<f32>(f32(rect.x) + f32(rect.z) * 0.5, f32(rect.y) + f32(rect.w) * 0.5);
    let half = vec2<f32>(f32(rect.z) * 0.5, f32(rect.w) * 0.5);
    let q = abs(pixel + vec2<f32>(0.5, 0.5) - center) - half + vec2<f32>(radius, radius);
    let distance = length(max(q, vec2<f32>(0.0))) + min(max(q.x, q.y), 0.0) - radius;
    return select(0.0, 1.0, distance <= 0.5);
}

fn pass_progress(color: vec3<f32>, uv: vec2<f32>) -> vec3<f32> {
    let pixel = pixel_position(uv);
    let rect = scene.rects[6];
    let radius = max(1.0, f32(rect.w) * 0.5);
    let track = rounded_rect_mask(pixel, rect, radius);
    let progress = f32(scene.progress_q16) / 65535.0;
    let marker_x = f32(rect.x) + f32(rect.z) * clamp(progress, 0.0, 1.0);
    let marker_y = f32(rect.y) + f32(rect.w) * 0.5;
    let marker_radius = max(1.0, 9.0 * f32(rect.z) / 1000.0);
    let marker = select(0.0, 1.0,
        distance(pixel + vec2<f32>(0.5, 0.5), vec2<f32>(marker_x, marker_y)) <= marker_radius);
    let accent = palette_rgb(1u);
    var result = mix(color, accent, track * 0.35);
    result = mix(result, accent, marker * 0.88);
    return result;
}

fn pass_vignette(color: vec3<f32>, uv: vec2<f32>) -> vec3<f32> {
    let edge = uv.x * (1.0 - uv.x) * uv.y * (1.0 - uv.y);
    let factor = clamp(edge * 24.0, 0.78, 1.0);
    return color * factor;
}

fn pass_fade(color: vec3<f32>, uv: vec2<f32>) -> vec3<f32> {
    let fade = min(f32(scene.fade_in_q16), f32(scene.fade_out_q16)) / 65535.0;
    return pass_vignette(color * clamp(fade, 0.0, 1.0), uv);
}

fn pass_yuv420(rgb: vec3<f32>) -> vec3<f32> {
    let y = 0.2126 * rgb.r + 0.7152 * rgb.g + 0.0722 * rgb.b;
    let u = -0.1146 * rgb.r - 0.3854 * rgb.g + 0.5000 * rgb.b + 0.5;
    let v = 0.5000 * rgb.r - 0.4542 * rgb.g - 0.0458 * rgb.b + 0.5;
    return vec3<f32>(
        clamp(y * (219.0 / 255.0) + (16.0 / 255.0), 0.0, 1.0),
        clamp(u * (224.0 / 255.0) + (16.0 / 255.0), 0.0, 1.0),
        clamp(v * (224.0 / 255.0) + (16.0 / 255.0), 0.0, 1.0),
    );
}

fn render_rgb(uv: vec2<f32>) -> vec3<f32> {
    let metrics = audio_metrics();
    var color = pass_artwork_preprocess(uv, metrics);
    color = pass_background(color, uv, metrics);
    color = pass_spectrum(color, uv);
    color = pass_waveform(color, uv, metrics);
    color = pass_loudness(color, uv, metrics);
    color = pass_text_ui(color, uv);
    color = pass_progress(color, uv);
    color = pass_fade(color, uv);
    return clamp(color, vec3<f32>(0.0), vec3<f32>(1.0));
}

fn store_chroma(block: vec2<u32>) {
    let base = block * 2u;
    let rgb0 = render_rgb(pixel_uv(base));
    let rgb1 = render_rgb(pixel_uv(base + vec2<u32>(1u, 0u)));
    let rgb2 = render_rgb(pixel_uv(base + vec2<u32>(0u, 1u)));
    let rgb3 = render_rgb(pixel_uv(base + vec2<u32>(1u, 1u)));
    let average = (rgb0 + rgb1 + rgb2 + rgb3) * 0.25;
    let yuv = pass_yuv420(average);
    textureStore(chroma_output, block, vec4<f32>(yuv.y, yuv.z, 0.0, 1.0));
}

@compute @workgroup_size(8, 8, 1)
fn render_draft(@builtin(global_invocation_id) gid: vec3<u32>) {
    if (gid.x >= scene.width || gid.y >= scene.height) {
        return;
    }

    let rgb = render_rgb(pixel_uv(gid.xy));
    textureStore(rgb_output, gid.xy, vec4<f32>(rgb, 1.0));
    let yuv = pass_yuv420(rgb);
    textureStore(luma_output, gid.xy, vec4<f32>(yuv.x, 0.0, 0.0, 1.0));
    if ((gid.x & 1u) == 0u && (gid.y & 1u) == 0u) {
        store_chroma(gid.xy / 2u);
    }
}

@compute @workgroup_size(8, 8, 1)
fn render_h264(@builtin(global_invocation_id) gid: vec3<u32>) {
    if (gid.x >= scene.width || gid.y >= scene.height) {
        return;
    }
    let rgb = render_rgb(pixel_uv(gid.xy));
    textureStore(rgb_output, gid.xy, vec4<f32>(rgb, 1.0));
}
"#;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn draft_shader_declares_pcm_resident_input_and_gpu_owned_yuv_output() {
        assert!(DRAFT_SHADER_WGSL.contains("var<storage, read> pcm_input"));
        assert!(DRAFT_SHADER_WGSL.contains("texture_storage_2d<rgba8unorm, write>"));
        assert_eq!(
            DRAFT_SHADER_WGSL
                .matches("texture_storage_2d<rgba8unorm, write>")
                .count(),
            3
        );
    }

    #[test]
    fn draft_shader_declares_revision_friendly_pass_markers() {
        for pass in [
            "pass_artwork_preprocess",
            "pass_background",
            "pass_spectrum",
            "pass_waveform",
            "pass_loudness",
            "pass_text_ui",
            "pass_progress",
            "pass_fade",
            "pass_yuv420",
        ] {
            assert!(DRAFT_SHADER_WGSL.contains(pass), "missing {pass}");
        }
    }

    #[test]
    fn draft_shader_contains_audio_driven_visual_processing() {
        for function in [
            "fn sample_pcm",
            "fn audio_rms",
            "fn audio_peak",
            "fn audio_band_energy",
            "fn waveform_signal",
            "fn pass_artwork_preprocess(uv: vec2<f32>, metrics: AudioMetrics)",
            "fn pass_vignette",
        ] {
            assert!(DRAFT_SHADER_WGSL.contains(function), "missing {function}");
        }
    }

    #[test]
    fn draft_shader_contains_cpu_parity_scene_layers() {
        for function in [
            "fn palette_rgb",
            "fn rect_contains",
            "fn spectrum_q16",
            "fn loudness_q16",
            "fn fallback_note",
            "fn glyph_bitmap",
            "fn text_mask",
            "fn font_text_advance",
            "fn font_style",
            "fn font_text_mask",
            "fn text_codepoint",
            "fn rounded_rect_mask",
        ] {
            assert!(DRAFT_SHADER_WGSL.contains(function), "missing {function}");
        }
        for field in [
            "palette: array<vec4<u32>, 4>",
            "rects: array<vec4<i32>, 8>",
            "spectrum: array<vec4<u32>, 6>",
            "loudness: array<vec4<u32>, 250>",
            "text: array<vec4<u32>, 32>",
            "progress_q16: u32",
        ] {
            assert!(DRAFT_SHADER_WGSL.contains(field), "missing {field}");
        }
    }

    #[test]
    fn draft_binding_and_pass_manifests_are_explicit() {
        assert_eq!(DRAFT_SHADER_VERSION, 2);
        assert_eq!(DRAFT_WORKGROUP_SIZE, (8, 8, 1));
        assert_eq!(DRAFT_BINDINGS.len(), 10);
        assert!(DRAFT_BINDINGS
            .iter()
            .any(|binding| binding.name == "waveform_q16"));
        assert!(DRAFT_BINDINGS
            .iter()
            .any(|binding| binding.name == "artwork_texture"));
        assert_eq!(DRAFT_PASS_ORDER, CANONICAL_ORDER);
    }

    #[test]
    fn draft_shader_contains_signed_minmax_waveform_monitor_path() {
        for marker in [
            "waveform_samples",
            "waveform_columns",
            "waveform_flags",
            "waveform_minmax",
            "signed_minmax",
        ] {
            assert!(DRAFT_SHADER_WGSL.contains(marker), "missing {marker}");
        }
    }

    #[test]
    fn draft_module_exposes_valid_nonproduction_descriptor() {
        let descriptor = DRAFT_SHADER_MODULE.descriptor();
        assert!(crate::music_v2_shader_module::validate_descriptor(descriptor).is_ok());
        assert!(!descriptor.production_ready);
        assert_eq!(DRAFT_SHADER_MODULE.wgsl(), DRAFT_SHADER_WGSL);
    }

    #[test]
    fn draft_shader_has_no_cpu_readback_or_legacy_rgba_transport_marker() {
        for forbidden in ["map_async", "readback", "PackedBytes", "rgbaToYUV420p"] {
            assert!(!DRAFT_SHADER_WGSL.contains(forbidden), "found {forbidden}");
        }
    }

    #[test]
    fn font_sampling_uses_fractional_gpu_coverage_antialiasing() {
        assert!(DRAFT_SHADER_WGSL.contains("fn sample_glyph_alpha"));
        assert!(DRAFT_SHADER_WGSL.contains("let aa_step"));
        assert!(DRAFT_SHADER_WGSL.contains("textureSampleLevel("));
        assert!(DRAFT_SHADER_WGSL.contains("glyph_atlas"));
        assert!(
            !DRAFT_SHADER_WGSL.contains("let atlas_x = min(1023u, cell_x + u32(floor(cell.x)))")
        );
    }
}
