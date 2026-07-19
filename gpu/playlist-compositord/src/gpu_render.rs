use crate::adapter;
use crate::contracts::{
    ArtworkReceipt, BaseTextureReceipt, ColorSpace, GlyphAtlasReceipt, GlyphInstanceDiagnostic,
    GlyphRenderDiagnostics, GpuFrame, MusicScenePayload, Ownership, PixelFormat,
    TextOverlayReceipt, Yuv420pFrame, CONTRACT_VERSION, MUSIC_MAX_WAVEFORM_SAMPLES, ROW_ALIGNMENT,
};
use sha2::{Digest, Sha256};
use std::env;
use std::num::NonZeroU32;
use std::sync::mpsc::channel;
use wgpu::util::DeviceExt;

const SHADER: &str = r#"
struct Params {
  width: u32, height: u32, row_words: u32, sequence: u32,
  rms: u32, peak: u32, scene_enabled: u32, glyph_count: u32,
  // Uniform-buffer arrays have a 16-byte alignment/stride in WGSL. Packing
  // the 24 scalar samples into six vec4s keeps the host's 32-word layout
  // unchanged while satisfying that rule on all backends.
  spectrum: array<vec4<u32>, 6>,
  // Dynamics/palette/rects are deliberately fixed-size and 16-byte aligned.
  dynamics: array<vec4<u32>, 2>,
  palette: array<vec4<u32>, 4>,
  rects: array<vec4<i32>, 8>,
  guides: vec4<u32>,
  fallback_end: vec4<u32>,
}
@group(0) @binding(0) var<storage, read_write> pixels: array<u32>;
@group(0) @binding(1) var<uniform> params: Params;
@group(0) @binding(2) var atlas_tex: texture_2d<f32>;
@group(0) @binding(3) var atlas_sampler: sampler;
struct GlyphInstance { screen: vec4<f32>, atlas: vec4<f32>, color: vec4<f32> }
@group(0) @binding(4) var<storage, read> glyphs: array<GlyphInstance>;
@group(0) @binding(5) var artwork_tex: texture_2d<f32>;
  @group(0) @binding(6) var artwork_sampler: sampler;
  // Bounded 256-sample envelope/trend pairs, uploaded as a read-only storage buffer.
@group(0) @binding(7) var<storage, read> dynamics_samples: array<u32>;
@group(0) @binding(8) var overlay_tex: texture_2d<f32>;
@group(0) @binding(9) var overlay_sampler: sampler;
@group(0) @binding(10) var base_tex: texture_2d<f32>;
@group(0) @binding(11) var base_sampler: sampler;
@group(0) @binding(12) var waveform_tex: texture_2d<f32>;
@group(0) @binding(13) var loudness_tex: texture_2d<f32>;
@group(0) @binding(14) var spectrum_tex: texture_2d<f32>;
// Bounded Q16 waveform samples supplied by the MusicFeature contract. The
// storage path is opt-in (scene bit 256) and leaves the legacy texture path
// untouched for parity diagnostics.
@group(0) @binding(15) var<storage, read> waveform_samples: array<u32>;
@group(0) @binding(16) var<storage, read> fingerprint_samples: array<u32>;
// Static cover-scaled artwork blurred by the GPU IIR pre-pass. Values retain
// FFmpeg's byte-domain scale (0..255) until the compositor consumes them.
@group(0) @binding(17) var<storage, read> blurred_pixels: array<vec4<f32>>;

fn inside_rounded_rect(p: vec2<i32>, rect_min: vec2<i32>, rect_max: vec2<i32>, radius: i32) -> bool {
  if (p.x < rect_min.x || p.y < rect_min.y || p.x >= rect_max.x || p.y >= rect_max.y) { return false; }
  if (radius <= 0) { return true; }
  if (p.x < rect_min.x + radius && p.y < rect_min.y + radius) {
    let d = p - (rect_min + vec2<i32>(radius - 1)); return dot(d, d) <= radius * radius;
  }
  if (p.x >= rect_max.x - radius && p.y < rect_min.y + radius) {
    let d = p - vec2<i32>(rect_max.x - radius, rect_min.y + radius - 1); return dot(d, d) <= radius * radius;
  }
  if (p.x < rect_min.x + radius && p.y >= rect_max.y - radius) {
    let d = p - vec2<i32>(rect_min.x + radius - 1, rect_max.y - radius); return dot(d, d) <= radius * radius;
  }
  if (p.x >= rect_max.x - radius && p.y >= rect_max.y - radius) {
    let d = p - (rect_max - vec2<i32>(radius)); return dot(d, d) <= radius * radius;
  }
  return true;
}

fn go_nrgba_over_opaque_channel(dst: f32, src: u32, alpha: u32) -> f32 {
  // image/draw converts 8-bit NRGBA through 16-bit premultiplied arithmetic,
  // then RGBA.Set keeps the high byte. Reproduce those integer truncations.
  let d = u32(clamp(dst * 255.0 + 0.5, 0.0, 255.0));
  let a16 = min(alpha, 255u) * 257u;
  let src16 = (min(src, 255u) * 257u * a16) / 65535u;
  let dst16 = d * 257u;
  let out16 = src16 + (dst16 * (65535u - a16)) / 65535u;
  return f32(out16 >> 8u) / 255.0;
}

fn loudness_lanczos(x: f32) -> f32 {
  let ax = abs(x);
  if (ax >= 3.0) { return 0.0; }
  if (ax < 0.0000001) { return 1.0; }
  let pix = 3.141592653589793 * x;
  return (sin(pix) / pix) * (sin(pix / 3.0) / (pix / 3.0));
}

fn loudness_round_byte(value: f32) -> f32 {
  return floor(clamp(value, 0.0, 255.0) + 0.5);
}

fn loudness_source_pixel(sx: i32, sy: i32, sw: i32, sh: i32,
                         detail_radius: i32, trend_radius: i32) -> vec4<f32> {
  if (sx < 0 || sy < 0 || sx >= sw || sy >= sh) { return vec4<f32>(0.0); }
  var alpha: u32 = 0u;
  let guide_y0 = i32(floor((6.0 / 80.0) * f32(sh - 1) + 0.5));
  let guide_y1 = i32(floor((28.0 / 80.0) * f32(sh - 1) + 0.5));
  let guide_y2 = i32(floor((50.0 / 80.0) * f32(sh - 1) + 0.5));
  let guide_y3 = i32(floor((72.0 / 80.0) * f32(sh - 1) + 0.5));
  if (abs(sy - guide_y0) <= 2 || abs(sy - guide_y1) <= 2 ||
      abs(sy - guide_y2) <= 2 || abs(sy - guide_y3) <= 2) { alpha = 56u; }

  let estimate = i32(floor(f32(sx) * 999.0 / max(1.0, f32(sw - 1)) + 0.5));
  for (var offset: i32 = -4; offset <= 4; offset = offset + 1) {
    let sample_index = clamp(estimate + offset, 0, 999);
    let center_x = i32(floor(f32(sample_index) * f32(sw - 1) / 999.0 + 0.5));
    let envelope = f32(dynamics_samples[u32(sample_index)]) / 65535.0;
    let center_y = i32(floor((1.0 - envelope) * f32(sh - 1) + 0.5));
    let dx = sx - center_x;
    let dy = sy - center_y;
    if (dx * dx + dy * dy <= detail_radius * detail_radius) { alpha = 204u; }
  }

  for (var offset: i32 = -trend_radius; offset <= trend_radius; offset = offset + 1) {
    let trend_x = sx + offset;
    if (trend_x < 0 || trend_x >= sw) { continue; }
    let trend = bitcast<f32>(dynamics_samples[1000u + u32(trend_x)]);
    let trend_y = i32(floor((1.0 - clamp(trend, 0.0, 1.0)) * f32(sh - 1) + 0.5));
    let dx = sx - trend_x;
    let dy = sy - trend_y;
    if (dx * dx + dy * dy <= trend_radius * trend_radius) { alpha = 242u; }
  }

  let accent = params.palette[1];
  return vec4<f32>(
    f32((accent.x * alpha + 127u) / 255u),
    f32((accent.y * alpha + 127u) / 255u),
    f32((accent.z * alpha + 127u) / 255u),
    f32(alpha));
}

fn loudness_horizontal_pixel(dst_x: i32, src_y: i32, lw: i32, sw: i32, sh: i32,
                             detail_radius: i32, trend_radius: i32) -> vec4<f32> {
  let source_x = f32(dst_x) * f32(sw - 1) / max(1.0, f32(lw - 1));
  let base_x = i32(floor(source_x));
  var sum = vec4<f32>(0.0);
  var sum_weight = 0.0;
  for (var k: i32 = base_x - 2; k <= base_x + 3; k = k + 1) {
    if (k < 0 || k >= sw) { continue; }
    let weight = loudness_lanczos(source_x - f32(k));
    sum = sum + loudness_source_pixel(k, src_y, sw, sh, detail_radius, trend_radius) * weight;
    sum_weight = sum_weight + weight;
  }
  if (sum_weight <= 0.0) { return vec4<f32>(0.0); }
  let value = sum / sum_weight;
  return vec4<f32>(loudness_round_byte(value.r), loudness_round_byte(value.g),
                   loudness_round_byte(value.b), loudness_round_byte(value.a));
}

fn loudness_target_pixel(dst_x: i32, dst_y: i32, lw: i32, lh: i32) -> vec4<f32> {
  let sw = lw * 4;
  let sh = lh * 4;
  let detail_radius = max(1, i32(floor(f32(params.height) / 180.0 + 0.5)));
  let trend_radius = max(1, i32(floor(f32(params.height) / 120.0 + 0.5)));
  let source_y = f32(dst_y) * f32(sh - 1) / max(1.0, f32(lh - 1));
  let base_y = i32(floor(source_y));
  var sum = vec4<f32>(0.0);
  var sum_weight = 0.0;
  for (var k: i32 = base_y - 2; k <= base_y + 3; k = k + 1) {
    if (k < 0 || k >= sh) { continue; }
    let weight = loudness_lanczos(source_y - f32(k));
    sum = sum + loudness_horizontal_pixel(dst_x, k, lw, sw, sh, detail_radius, trend_radius) * weight;
    sum_weight = sum_weight + weight;
  }
  if (sum_weight <= 0.0) { return vec4<f32>(0.0); }
  let value = sum / sum_weight;
  return vec4<f32>(loudness_round_byte(value.r), loudness_round_byte(value.g),
                   loudness_round_byte(value.b), loudness_round_byte(value.a));
}

fn distance_to_segment(point: vec2<f32>, a: vec2<f32>, b: vec2<f32>) -> f32 {
  let ab = b - a;
  let t = clamp(dot(point - a, ab) / max(dot(ab, ab), 0.0001), 0.0, 1.0);
  return distance(point, a + t * ab);
}

fn fingerprint_stamp_hits(point: vec2<f32>, a: vec2<f32>, b: vec2<f32>) -> u32 {
  let delta = b - a;
  let dist = length(delta);
  if (dist < 0.5) {
    let centre = floor(a + vec2<f32>(0.5));
    return select(0u, 1u, abs(point.x - centre.x) <= 1.0 && abs(point.y - centre.y) <= 1.0);
  }
  let steps = max(1u, u32(dist) * 2u);
  var low = 0.0;
  var high = 1.0;
  let box_low = point - vec2<f32>(1.5);
  let box_high = point + vec2<f32>(1.5);
  if (abs(delta.x) < 0.000001) {
    if (a.x < box_low.x || a.x >= box_high.x) { return 0u; }
  } else {
    let tx0 = (box_low.x - a.x) / delta.x;
    let tx1 = (box_high.x - a.x) / delta.x;
    low = max(low, min(tx0, tx1)); high = min(high, max(tx0, tx1));
  }
  if (abs(delta.y) < 0.000001) {
    if (a.y < box_low.y || a.y >= box_high.y) { return 0u; }
  } else {
    let ty0 = (box_low.y - a.y) / delta.y;
    let ty1 = (box_high.y - a.y) / delta.y;
    low = max(low, min(ty0, ty1)); high = min(high, max(ty0, ty1));
  }
  if (high <= low || high < 0.0 || low > 1.0) { return 0u; }
  low = clamp(low, 0.0, 1.0); high = clamp(high, 0.0, 1.0);
  let first = min(steps, u32(max(0.0, ceil(low * f32(steps) - 0.000001))));
  var last = steps;
  if (high < 1.0) { last = u32(max(0.0, ceil(high * f32(steps) - 0.000001) - 1.0)); }
  return select(0u, last - first + 1u, last >= first);
}

fn go_rgba_over_byte(dst: u32, src: u32, alpha: u32) -> u32 {
  // image/draw's RGBA64Image fast path keeps the invalid-premultiplied source
  // channel at src*257, applies a 16-bit destination factor, then truncates and
  // wraps to uint8. The CPU fallback glyph depends on this exact boundary case.
  let inverse = (65535u - alpha * 257u) * 257u;
  return ((dst * inverse / 65535u + src * 257u) >> 8u) & 255u;
}

fn fallback_artwork_pixel(local: vec2<f32>, size: vec2<f32>, start: vec3<f32>, finish: vec3<f32>, ink: vec3<f32>) -> vec3<f32> {
  let y = clamp(local.y, 0.0, size.y - 1.0);
  var c = floor(mix(start, finish, y / max(1.0, size.y - 1.0)) * 255.0) / 255.0;
  let centre = size * 0.5;
  let scale = size.x / 288.0;
  let point = local;
  var fingerprint_hits = 0u;
  for (var band: u32 = 0u; band < 64u; band = band + 1u) {
    let angle = -1.5707963268 + f32(band) * 6.2831853072 / 64.0;
    let direction = vec2<f32>(cos(angle), sin(angle));
    let energy = f32(fingerprint_samples[band]) / 65535.0;
    let a = centre + direction * (54.0 * scale);
    let b = centre + direction * ((54.0 + energy * 58.0) * scale);
    fingerprint_hits = fingerprint_hits + fingerprint_stamp_hits(point, a, b);
  }
  var fingerprint_bytes = vec3<u32>(clamp(c * 255.0 + vec3<f32>(0.5), vec3<f32>(0.0), vec3<f32>(255.0)));
  let ink_bytes_for_fingerprint = vec3<u32>(clamp(ink * 255.0 + vec3<f32>(0.5), vec3<f32>(0.0), vec3<f32>(255.0)));
  for (var hit = 0u; hit < fingerprint_hits; hit = hit + 1u) {
    fingerprint_bytes = (ink_bytes_for_fingerprint * 66u + fingerprint_bytes * 189u) / 255u;
  }
  c = vec3<f32>(fingerprint_bytes) / 255.0;
  let note_uv = (local + vec2<f32>(0.5)) / size;
  let note_alpha = textureSampleLevel(artwork_tex, artwork_sampler, note_uv, 0.0).a;
  let note_a = u32(clamp(note_alpha * 255.0 + 0.5, 0.0, 255.0));
  if (note_a > 0u) {
    let background_bytes = vec3<u32>(clamp(c * 255.0 + vec3<f32>(0.5), vec3<f32>(0.0), vec3<f32>(255.0)));
    let ink_bytes = params.palette[1].xyz;
    let wrapped = vec3<u32>(
      go_rgba_over_byte(background_bytes.r, ink_bytes.r, note_a),
      go_rgba_over_byte(background_bytes.g, ink_bytes.g, note_a),
      go_rgba_over_byte(background_bytes.b, ink_bytes.b, note_a));
    return vec3<f32>(wrapped) / 255.0;
  }
  return c;
}
@compute @workgroup_size(8, 8)
fn main(@builtin(global_invocation_id) id: vec3<u32>) {
  if (id.x >= params.width || id.y >= params.height) { return; }
  let i = id.y * params.row_words + id.x;
  // Diagnostic-only canonical text overlay readback. The reserved sequence
  // samples the uploaded overlay texture directly; production sequences never
  // enter this branch.
  if (params.sequence == 0xfffffffeu) {
    let dims = vec2<f32>(textureDimensions(overlay_tex));
    if ((params.scene_enabled & 16u) != 0u) {
      if (id.x >= u32(dims.x) || id.y >= u32(dims.y)) { pixels[i] = 0u; return; }
      let c = textureLoad(overlay_tex, vec2<i32>(id.xy), 0);
      let rr = u32(clamp(c.r * 255.0, 0.0, 255.0));
      let gg = u32(clamp(c.g * 255.0, 0.0, 255.0));
      let bb = u32(clamp(c.b * 255.0, 0.0, 255.0));
      let aa = u32(clamp(c.a * 255.0, 0.0, 255.0));
      pixels[i] = rr | (gg << 8u) | (bb << 16u) | (aa << 24u);
      return;
    }
    // Canonical CPU probe places the atlas into the title rect (rect index 1)
    // using nearest scaling; mirror that placement for the diagnostic readback.
    var rect = params.rects[1];
    var px = vec2<f32>(f32(id.x) - f32(rect.x), f32(id.y) - f32(rect.y));
    if (px.x < 0.0 || px.y < 0.0 || px.x >= f32(rect.z) || px.y >= f32(rect.w)) { rect = params.rects[2]; px = vec2<f32>(f32(id.x)-f32(rect.x),f32(id.y)-f32(rect.y)); }
    if (px.x < 0.0 || px.y < 0.0 || px.x >= f32(rect.z) || px.y >= f32(rect.w)) { rect = params.rects[3]; px = vec2<f32>(f32(id.x)-f32(rect.x),f32(id.y)-f32(rect.y)); }
    if (px.x < 0.0 || px.y < 0.0 || px.x >= f32(rect.z) || px.y >= f32(rect.w)) { rect = params.rects[7]; px = vec2<f32>(f32(id.x)-f32(rect.x),f32(id.y)-f32(rect.y)); }
    if (px.x < 0.0 || px.y < 0.0 || px.x >= f32(rect.z) || px.y >= f32(rect.w)) { pixels[i] = 0u; return; }
    let src = vec2<i32>(floor(px / max(vec2<f32>(f32(rect.z), f32(rect.w)), vec2<f32>(1.0)) * dims));
    let c = textureLoad(overlay_tex, src, 0);
    let rr = u32(clamp(c.r * 255.0, 0.0, 255.0));
    let gg = u32(clamp(c.g * 255.0, 0.0, 255.0));
    let bb = u32(clamp(c.b * 255.0, 0.0, 255.0));
    let aa = u32(clamp(c.a * 255.0, 0.0, 255.0));
    pixels[i] = rr | (gg << 8u) | (bb << 16u) | (aa << 24u);
    return;
  }
  if (params.sequence == 0xfffffffdu) {
    let ad = vec2<f32>(textureDimensions(artwork_tex));
    let src_aspect = ad.x / ad.y;
    let dst_aspect = f32(params.width) / f32(params.height);
    var sx = f32(id.x) + 0.5;
    var sy = f32(id.y) + 0.5;
    if (src_aspect > dst_aspect) {
      let crop_w = ad.y * dst_aspect;
      sx = (ad.x - crop_w) * 0.5 + sx * crop_w / f32(params.width);
      sy = sy * ad.y / f32(params.height);
    } else {
      let crop_h = ad.x / dst_aspect;
      sx = sx * ad.x / f32(params.width);
      sy = (ad.y - crop_h) * 0.5 + sy * crop_h / f32(params.height);
    }
    let ac = textureSampleLevel(artwork_tex, artwork_sampler, vec2<f32>(sx, sy) / ad, 0.0);
    let ar=u32(clamp(ac.r*255.0,0.0,255.0)); let ag=u32(clamp(ac.g*255.0,0.0,255.0)); let ab=u32(clamp(ac.b*255.0,0.0,255.0)); let aa=u32(clamp(ac.a*255.0,0.0,255.0));
    pixels[i]=ar|(ag<<8u)|(ab<<16u)|(aa<<24u); return;
  }
  // Diagnostic-only flat background probe. This deliberately bypasses every
  // artwork, waveform, glow, and text layer so the canonical palette upload
  // can be compared against the CPU background contract in isolation.
  if (params.sequence == 0xfffffffcu) {
    let c = params.palette[2];
    let rr=min(c.x,255u); let gg=min(c.y,255u);
    let bb=min(c.z,255u); let aa=min(c.w,255u);
    pixels[i]=rr|(gg<<8u)|(bb<<16u)|(aa<<24u); return;
  }
  if (params.sequence == 0xfffffffau) {
    let d = vec2<f32>(textureDimensions(base_tex));
    let uv = (vec2<f32>(f32(id.x), f32(id.y)) + vec2<f32>(0.5)) / d;
    let c = textureLoad(base_tex, vec2<i32>(id.xy), 0);
    let rr=u32(clamp(c.r*255.0,0.0,255.0)); let gg=u32(clamp(c.g*255.0,0.0,255.0));
    let bb=u32(clamp(c.b*255.0,0.0,255.0)); let aa=u32(clamp(c.a*255.0,0.0,255.0));
    pixels[i]=rr|(gg<<8u)|(bb<<16u)|(aa<<24u); return;
  }
  // Diagnostic-only base plus screen text composite. Dynamic analytic layers
  // are deliberately bypassed so the static/text contribution can be
  // compared against the CPU pre-encode reference without changing production
  // sequences.
  if (params.sequence == 0xffffffe0u) {
    var c = textureLoad(base_tex, vec2<i32>(id.xy), 0);
    let od = textureDimensions(overlay_tex);
    if (id.x < od.x && id.y < od.y) {
      let o = textureLoad(overlay_tex, vec2<i32>(id.xy), 0);
      c = mix(c, o, clamp(o.a, 0.0, 1.0));
    }
    let rr=u32(clamp(c.r*255.0,0.0,255.0)); let gg=u32(clamp(c.g*255.0,0.0,255.0));
    let bb=u32(clamp(c.b*255.0,0.0,255.0)); let aa=u32(clamp(c.a*255.0,0.0,255.0));
    pixels[i]=rr|(gg<<8u)|(bb<<16u)|(aa<<24u); return;
  }
  if (params.sequence == 0xfffffff7u) {
    let wd = textureDimensions(waveform_tex);
    if ((params.scene_enabled & 32u) == 0u || id.x >= wd.x || id.y >= wd.y) { pixels[i] = 0u; return; }
    let wc = textureLoad(waveform_tex, vec2<i32>(id.xy), 0);
    let rr=u32(clamp(wc.r*255.0,0.0,255.0)); let gg=u32(clamp(wc.g*255.0,0.0,255.0));
    let bb=u32(clamp(wc.b*255.0,0.0,255.0)); let aa=u32(clamp(wc.a*255.0,0.0,255.0));
    pixels[i]=rr|(gg<<8u)|(bb<<16u)|(aa<<24u); return;
  }
  if (params.sequence == 0xfffffff5u) {
    let sd = textureDimensions(spectrum_tex);
    if (id.x >= sd.x || id.y >= sd.y || (params.scene_enabled & 128u) == 0u) { pixels[i] = 0u; return; }
    let sc = textureLoad(spectrum_tex, vec2<i32>(id.xy), 0);
    let rr=u32(clamp(sc.r*255.0,0.0,255.0)); let gg=u32(clamp(sc.g*255.0,0.0,255.0));
    let bb=u32(clamp(sc.b*255.0,0.0,255.0)); let aa=u32(clamp(sc.a*255.0,0.0,255.0));
    pixels[i]=rr|(gg<<8u)|(bb<<16u)|(aa<<24u); return;
  }
  if (params.sequence == 0xffffffe2u) {
    let ld = textureDimensions(loudness_tex);
    if ((params.scene_enabled & 64u) == 0u || id.x >= ld.x || id.y >= ld.y) { pixels[i] = 0u; return; }
    let c = textureLoad(loudness_tex, vec2<i32>(id.xy), 0);
    let rr=u32(clamp(c.r*255.0,0.0,255.0)); let gg=u32(clamp(c.g*255.0,0.0,255.0));
    let bb=u32(clamp(c.b*255.0,0.0,255.0)); let aa=u32(clamp(c.a*255.0,0.0,255.0));
    pixels[i]=rr|(gg<<8u)|(bb<<16u)|(aa<<24u); return;
  }
  var r: u32;
  var g: u32;
  var b: u32;
  if (params.scene_enabled == 0u) {
    r = (id.x + params.sequence) & 255u;
    g = (id.y + params.sequence * 3u) & 255u;
    b = ((id.x + id.y) / 2u + params.sequence * 5u) & 255u;
  } else {
    if (params.sequence == 0xfffffff8u) {
      let probe_loud = params.rects[5];
      if (id.x < u32(max(0, probe_loud.x)) || id.y < u32(max(0, probe_loud.y)) ||
          id.x >= u32(probe_loud.x + probe_loud.z) || id.y >= u32(probe_loud.y + probe_loud.w)) {
        pixels[i] = 0u;
        return;
      }
    }
    let fx = f32(id.x) / max(1.0, f32(params.width - 1u));
    let fy = f32(id.y) / max(1.0, f32(params.height - 1u));
    let rms = f32(params.rms) / 32767.0;
    let peak = f32(params.peak) / 32767.0;
    let primary = vec3<f32>(params.palette[0].xyz) / 255.0;
    let accent = vec3<f32>(params.palette[1].xyz) / 255.0;
    let background = vec3<f32>(params.palette[2].xyz) / 255.0;
    let overlay = vec4<f32>(params.palette[3]) / 255.0;
    let has_base = (params.scene_enabled & 8u) != 0u;
    // Canonical scene background and glow, with a deterministic waveform.
    var glow = max(0.0, 1.0 - distance(vec2<f32>(fx, fy), vec2<f32>(0.5, 0.48)) * 1.7) * (0.18 + rms * 0.42);
    if (has_base) { glow = 0.0; }
    // Artwork is a first-class layer. Keep the tile bounded and deterministic
    // so malformed/absent artwork can use the same fallback texture without
    // changing the bind group contract. The soft edge is a rounded-tile
    // approximation suitable for the compute renderer.
    var artwork = 0.0;
    var artwork_color = background;
    var artwork_alpha = 0.0;
    if ((params.scene_enabled & 2u) != 0u && !has_base) {
      let artwork_rect = params.rects[0];
      let tile_min = vec2<f32>(f32(artwork_rect.x) / f32(params.width), f32(artwork_rect.y) / f32(params.height));
      let tile_max = vec2<f32>(f32(artwork_rect.x + artwork_rect.z) / f32(params.width), f32(artwork_rect.y + artwork_rect.w) / f32(params.height));
      if (fx >= tile_min.x && fx < tile_max.x && fy >= tile_min.y && fy < tile_max.y) {
        // Match the CPU `scaleCover` policy instead of stretching artwork:
        // crop the longer source axis around its centre while preserving the
        // source aspect ratio inside the canonical artwork rect.
        let tile_uv = (vec2<f32>(fx, fy) - tile_min) / (tile_max - tile_min);
        let tex_size = vec2<f32>(textureDimensions(artwork_tex));
        let tile_aspect = (tile_max.x - tile_min.x) / max(0.0001, tile_max.y - tile_min.y);
        let source_aspect = tex_size.x / max(1.0, tex_size.y);
        var uv = tile_uv;
        if (source_aspect > tile_aspect) {
          uv.x = (tile_uv.x - 0.5) * tile_aspect / source_aspect + 0.5;
        } else {
          uv.y = (tile_uv.y - 0.5) * source_aspect / tile_aspect + 0.5;
        }
        uv = clamp(uv, vec2<f32>(0.0), vec2<f32>(1.0));
        let tile_size = vec2<f32>(f32(artwork_rect.z), f32(artwork_rect.w));
        let tile_pixel = tile_uv * tile_size;
        let corner_radius = max(1.0, 24.0 * f32(params.width) / 1280.0);
        let local = vec2<i32>(i32(id.x) - artwork_rect.x, i32(id.y) - artwork_rect.y);
        let rr = i32(floor(corner_radius + 0.5));
        let inside = inside_rounded_rect(local, vec2<i32>(0), vec2<i32>(artwork_rect.z, artwork_rect.w), rr);
        let alpha = select(0.0, 1.0, inside);
        var cover_sample = textureSampleLevel(artwork_tex, artwork_sampler, uv, 0.0);
        if (u32(artwork_rect.z) == u32(tex_size.x) && u32(artwork_rect.w) == u32(tex_size.y)) {
          cover_sample = textureLoad(artwork_tex, local, 0);
        }
        artwork = cover_sample.a * alpha;
        // Feed a restrained artwork luminance into the background layer; this
        // provides a stable palette/blur approximation without extra passes.
        let cover = cover_sample.rgb;
        artwork_color = cover;
        artwork_alpha = cover_sample.a * alpha;
        let luminance = dot(cover, vec3<f32>(0.2126, 0.7152, 0.0722));
        glow = glow + luminance * 0.08 * alpha;
      }
    }
    let sx = f32(id.x);
    let sy = f32(id.y);
    var blurred_bg = background;
    // GPU-only fallback artwork. The full-frame blurred copy is produced by
    // the preceding GPU passes; this branch supplies the rounded foreground.
    if (!has_base && (params.scene_enabled & 4096u) != 0u) {
      let ar = params.rects[0];
      let ax = f32(ar.x); let ay = f32(ar.y);
      let aw = max(1.0, f32(ar.z)); let ah = max(1.0, f32(ar.w));
      if (sx >= ax && sx < ax + aw && sy >= ay && sy < ay + ah) {
        let local = vec2<f32>(sx - ax, sy - ay);
        let rr = i32(floor(max(1.0, 24.0 * f32(params.width) / 1280.0) + 0.5));
        let inside = inside_rounded_rect(vec2<i32>(i32(local.x), i32(local.y)), vec2<i32>(0), vec2<i32>(ar.z, ar.w), rr);
        artwork_color = fallback_artwork_pixel(local, vec2<f32>(aw, ah), background, vec3<f32>(params.fallback_end.xyz) / 255.0, accent);
        artwork_alpha = select(0.0, 1.0, inside);
      }
    }
    let spectrum_rect = params.rects[4];
    let in_spectrum = sx >= f32(spectrum_rect.x) && sx < f32(spectrum_rect.x + spectrum_rect.z) &&
      sy >= f32(spectrum_rect.y) && sy < f32(spectrum_rect.y + spectrum_rect.w);
    let spectrum_u = clamp((sx - f32(spectrum_rect.x)) / max(1.0, f32(spectrum_rect.z - 1)), 0.0, 1.0);
    var level = 0.0;
    var band_index: u32 = 0u;
    for (var band: u32 = 0u; band < 24u; band = band + 1u) {
      let left = f32(band) / 24.0;
      let right = f32(band + 1u) / 24.0;
      if (spectrum_u >= left && spectrum_u < right) {
        band_index = band;
        level = f32(params.spectrum[band / 4u][band % 4u]) / 65535.0;
      }
    }
    // Match drawSpectrumFixedFade: canonical bar width/gap, first-bar inset,
    // minimum height, and a fixed bottom alpha fade.
    let scale = f32(params.width) / 1280.0;
    let bar_w = max(1.0, floor(18.0 * scale + 0.5));
    let bar_gap = max(1.0, floor(13.0 * scale + 0.5));
    let first_bar_x = f32(spectrum_rect.x) + floor(11.0 * scale + 0.5);
    let bar_bottom = f32(spectrum_rect.y + spectrum_rect.w);
    let max_bar_h = f32(spectrum_rect.w) - floor(16.0 * scale + 0.5);
    let min_bar_h = max(1.0, floor(4.0 * scale + 0.5));
    // Select the band from the same screen-space bar grid as the CPU
    // renderer. Mapping across the whole spectrum rect compresses the final
    // bars into the right edge and was the source of the large mask mismatch.
    let geom_u = (sx - first_bar_x) / max(1.0, bar_w + bar_gap);
    if (geom_u >= 0.0 && geom_u < 24.0) {
      band_index = min(23u, u32(floor(geom_u)));
      level = f32(params.spectrum[band_index / 4u][band_index % 4u]) / 65535.0;
    } else {
      level = 0.0;
    }
    let bar_h = floor(min_bar_h + level * max(0.0, max_bar_h - min_bar_h));
    let bar_x = first_bar_x + f32(band_index) * (bar_w + bar_gap);
    let bar_y = bar_bottom - bar_h;
    let fade_px = max(1.0, floor(10.0 * scale + 0.5));
    let eff_fade = min(fade_px, bar_h);
    let bottom_dist = bar_h - 1.0 - (sy - bar_y);
    var bar_alpha = 209.0;
    if (bottom_dist < eff_fade) {
      if (eff_fade == 1.0) { bar_alpha = 0.0; }
      else { bar_alpha = round(209.0 * bottom_dist / (eff_fade - 1.0)); }
    }
    var bars = select(0.0, bar_alpha / 255.0,
      sx >= bar_x && sx < bar_x + bar_w && sy >= bar_y && sy < bar_bottom);
    if ((params.scene_enabled & 128u) != 0u) { bars = 0.0; }
    if (params.sequence == 0xfffffffbu) {
      let v = u32(clamp(bars * 255.0, 0.0, 255.0));
      pixels[i] = v | (v << 8u) | (v << 16u) | (255u << 24u);
      return;
    }
    // There is no spectrum-derived proxy in the CPU reference. Waveform
    // pixels are supplied only by the exact raw-PCM or canonical texture path.
    let wave = 0.0;
    // Progress rail and thumb use the canonical progress rectangle.
    let progress = f32(params.dynamics[0].z) / 65535.0;
    let rail = params.rects[6];
    let rx = f32(id.x); let ry = f32(id.y);
    let radius = max(1.0, floor(f32(rail.w) * 0.5 + 0.5));
    var in_rail = rx >= f32(rail.x) && rx < f32(rail.x + rail.z) && ry >= f32(rail.y) && ry < f32(rail.y + rail.w);
    if (in_rail) {
      if (rx < f32(rail.x) + radius && ry < f32(rail.y) + radius) {
        let dx = rx - (f32(rail.x) + radius - 1.0); let dy = ry - (f32(rail.y) + radius - 1.0); in_rail = dx*dx + dy*dy <= radius*radius;
      } else if (rx >= f32(rail.x + rail.z) - radius && ry < f32(rail.y) + radius) {
        let dx = rx - (f32(rail.x + rail.z) - radius); let dy = ry - (f32(rail.y) + radius - 1.0); in_rail = dx*dx + dy*dy <= radius*radius;
      } else if (rx < f32(rail.x) + radius && ry >= f32(rail.y + rail.w) - radius) {
        let dx = rx - (f32(rail.x) + radius - 1.0); let dy = ry - (f32(rail.y + rail.w) - radius); in_rail = dx*dx + dy*dy <= radius*radius;
      } else if (rx >= f32(rail.x + rail.z) - radius && ry >= f32(rail.y + rail.w) - radius) {
        let dx = rx - (f32(rail.x + rail.z) - radius); let dy = ry - (f32(rail.y + rail.w) - radius); in_rail = dx*dx + dy*dy <= radius*radius;
      }
    }
    let rail_track = select(0.0, 1.0, in_rail);
    let rail_center_y = f32(rail.y) + f32(rail.w) * 0.5;
    let thumb_x = f32(rail.x) + floor(f32(rail.z) * progress + 0.5);
    let thumb_radius = max(1.0, floor(9.0 * f32(rail.z) / 1000.0 + 0.5));
    let thumb = select(0.0, 1.0, distance(vec2<f32>(rx, ry), vec2<f32>(thumb_x, rail_center_y)) <= thumb_radius);
    if (params.sequence == 0xfffffff9u) {
      let a = select(0u, 89u, in_rail);
      let ma = select(0u, 224u, thumb > 0.0);
      let c = params.palette[1];
      let aa = max(a, ma);
      let cr = select(0u, c.x, aa > 0u);
      let cg = select(0u, c.y, aa > 0u);
      let cb = select(0u, c.z, aa > 0u);
      pixels[i] = cr | (cg << 8u) | (cb << 16u) | (aa << 24u);
      return;
    }
    let loud = params.rects[5];
    var loudness = 0.0;
    var loudness_premultiplied = vec3<f32>(0.0);
    if (rx >= f32(loud.x) && rx < f32(loud.x + loud.z) && ry >= f32(loud.y) && ry < f32(loud.y + loud.w)) {
      let loud_pixel = loudness_target_pixel(i32(id.x) - loud.x, i32(id.y) - loud.y, loud.z, loud.w);
      loudness = loud_pixel.a / 255.0;
      loudness_premultiplied = loud_pixel.rgb / 255.0;
      if (params.sequence == 0xfffffff8u) {
        let cr = u32(loud_pixel.r); let cg = u32(loud_pixel.g);
        let cb = u32(loud_pixel.b); let ca = u32(loud_pixel.a);
        pixels[i] = cr | (cg << 8u) | (cb << 16u) | (ca << 24u);
        return;
      }
      // Diagnostic full-composite variant: retain every production layer
      // except loudness so its contribution can be measured in isolation.
      if (params.sequence == 0xffffffe1u) { loudness = 0.0; loudness_premultiplied = vec3<f32>(0.0); }
      // When a canonical loudness raster is supplied, it is authoritative;
      // disable the analytic trace to avoid double-rendering the graph.
      if ((params.scene_enabled & 64u) != 0u) { loudness = 0.0; loudness_premultiplied = vec3<f32>(0.0); }
    }
  var glyph = 0.0;
    if (params.sequence == 0xfffffffeu) {
      let od = vec2<f32>(textureDimensions(overlay_tex));
      if (f32(id.x) < od.x && f32(id.y) < od.y) {
        let ouv = (vec2<f32>(f32(id.x), f32(id.y)) + vec2<f32>(0.5, 0.5)) / od;
        let oc = textureSampleLevel(overlay_tex, overlay_sampler, ouv, 0.0);
        let or = u32(clamp(oc.r * 255.0, 0.0, 255.0));
        let og = u32(clamp(oc.g * 255.0, 0.0, 255.0));
        let ob = u32(clamp(oc.b * 255.0, 0.0, 255.0));
        let oa = u32(clamp(oc.a * 255.0, 0.0, 255.0));
        pixels[i] = or | (og << 8u) | (ob << 16u) | (oa << 24u);
        return;
      }
    }
    // GlyphAtlas is the authoritative production text layer. A screen_rgba
    // overlay may coexist for diagnostics, but must not suppress glyph runs
    // when the GPU text route is selected.
    if (true) {
      let pixel = vec2<f32>(f32(id.x), f32(id.y));
      for (var gi: u32 = 0u; gi < params.glyph_count; gi = gi + 1u) {
        // The reserved synthetic probe isolates the first manifest glyph so
        // CPU atlas crop and GPU coverage can be compared one-to-one.
        if (params.sequence == 0xffffffffu && gi > 0u) { continue; }
        let g = glyphs[gi];
        // GlyphInstance.screen is serialized in normalized target coordinates
        // (the same contract used by the CPU manifest). Convert to pixel
        // space before the coverage test; comparing pixel IDs with [0,1]
        // values made every glyph disappear except at the origin.
        let screen = vec4<f32>(g.screen.x * f32(params.width), g.screen.y * f32(params.height),
          g.screen.z * f32(params.width), g.screen.w * f32(params.height));
        if (pixel.x >= screen.x && pixel.y >= screen.y && pixel.x < screen.x + screen.z && pixel.y < screen.y + screen.w) {
          // Downsample the immutable high-resolution glyph cell with an exact
          // box footprint. Point sampling misses one-pixel strokes when a
          // 64px cell becomes an 8-24px screen glyph; area coverage mirrors
          // the small-size antialiasing produced by the CPU font renderer.
          let atlas_dims = vec2<i32>(textureDimensions(atlas_tex));
          let cell_min = vec2<i32>(floor(g.atlas.xy * vec2<f32>(atlas_dims)));
          let cell_size = max(vec2<i32>(1), vec2<i32>(floor(g.atlas.zw * vec2<f32>(atlas_dims))));
          let local = (pixel - screen.xy) / max(screen.zw, vec2<f32>(1.0));
          let local_next = ((pixel + vec2<f32>(1.0)) - screen.xy) / max(screen.zw, vec2<f32>(1.0));
          let sample_min = clamp(vec2<i32>(floor(local * vec2<f32>(cell_size))), vec2<i32>(0), cell_size - vec2<i32>(1));
          let sample_max = clamp(vec2<i32>(ceil(local_next * vec2<f32>(cell_size))) - vec2<i32>(1), sample_min, cell_size - vec2<i32>(1));
          var alpha_sum = 0.0;
          var alpha_count = 0.0;
          for (var ay: i32 = sample_min.y; ay <= sample_max.y; ay = ay + 1) {
            for (var ax: i32 = sample_min.x; ax <= sample_max.x; ax = ax + 1) {
              alpha_sum = alpha_sum + textureLoad(atlas_tex, cell_min + vec2<i32>(ax, ay), 0).a;
              alpha_count = alpha_count + 1.0;
            }
          }
          glyph = max(glyph, alpha_sum / max(1.0, alpha_count) * g.color.a);
        }
      }
      glyph = clamp(glyph, 0.0, 1.0);
    }
    // Diagnostic-only glyph isolation. The compare harness uses the reserved
    // sequence value to obtain a synthetic glyph mask without background,
    // artwork, waveform, or overlay pixels. Production sequences never use
    // this sentinel and therefore retain the normal compositor path.
    if (params.sequence == 0xffffffffu) {
      let v = u32(clamp(glyph * 255.0, 0.0, 255.0));
      pixels[i] = v | (v << 8u) | (v << 16u) | (255u << 24u);
      return;
    }
    // The CPU compositor starts from a cover-scaled artwork background blurred
    // with FFmpeg gblur sigma=64. A preceding pair of GPU passes implements
    // the same horizontal/vertical IIR recurrence in byte space.
    blurred_bg = background;
    if ((params.scene_enabled & (2u | 4096u)) != 0u && !has_base) {
      blurred_bg = blurred_pixels[id.y * params.width + id.x].rgb / 255.0;
    }
    if (has_base) {
      // The CPU base was rasterized at the output dimensions. Use an exact
      // texel load here; linear sampling would blur the already-composited
      // artwork edges a second time.
      blurred_bg = textureLoad(base_tex, vec2<i32>(id.xy), 0).rgb;
      artwork = 0.0;
      artwork_alpha = 0.0;
      // The canonical base already contains the artwork tile and its
      // luminance/shadow treatment. Do not re-add the artwork-derived glow
      // on top of that raster; the CPU frame path has no second glow pass.
      glow = 0.0;
    }
    // Diagnostic-only raw GPU blur. This isolates cover scaling and FFmpeg's
    // gblur recurrence from readability, shadow and foreground artwork.
    if (params.sequence == 0xffffffddu) {
      let rr = u32(clamp(blurred_bg.r * 255.0, 0.0, 255.0));
      let gg = u32(clamp(blurred_bg.g * 255.0, 0.0, 255.0));
      let bb = u32(clamp(blurred_bg.b * 255.0, 0.0, 255.0));
      pixels[i] = rr | (gg << 8u) | (bb << 16u) | (255u << 24u);
      return;
    }
    // Match the CPU layer order exactly: blurred background, readability
    // overlay, shadow, rounded artwork, then dynamic/text layers.
    var mixc = blurred_bg;
    if (!has_base) {
      let overlay_bytes = params.palette[3];
      mixc = vec3<f32>(
        go_nrgba_over_opaque_channel(mixc.r, overlay_bytes.r, overlay_bytes.a),
        go_nrgba_over_opaque_channel(mixc.g, overlay_bytes.g, overlay_bytes.a),
        go_nrgba_over_opaque_channel(mixc.b, overlay_bytes.b, overlay_bytes.a));
      let ar = params.rects[0];
      let shadow_radius = max(1, i32(floor(24.0 * f32(ar.z) / 288.0 + 0.5)));
      let shadow_blur = shadow_radius;
      let shadow_offset_y = i32(floor(8.0 * f32(ar.z) / 288.0 + 0.5));
      let shadow_size = vec2<i32>(ar.z + 2 * shadow_blur, ar.w + 2 * shadow_blur);
      let shadow_local = vec2<i32>(i32(id.x) - (ar.x - shadow_blur), i32(id.y) - (ar.y - shadow_blur));
      var shadow_alpha = 0.0;
      if (shadow_local.x >= 0 && shadow_local.y >= 0 && shadow_local.x < shadow_size.x && shadow_local.y < shadow_size.y) {
        let source_min = vec2<i32>(shadow_blur, shadow_blur + shadow_offset_y);
        let source_max = source_min + vec2<i32>(ar.z, ar.w);
        let hx0 = max(0, shadow_local.x - shadow_blur);
        let hx1 = min(shadow_size.x - 1, shadow_local.x + shadow_blur);
        let hcount = f32(hx1 - hx0 + 1);
        let vy0 = max(0, shadow_local.y - shadow_blur);
        let vy1 = min(shadow_size.y - 1, shadow_local.y + shadow_blur);
        let vcount = f32(vy1 - vy0 + 1);
        var vertical_sum = 0.0;
        for (var yy: i32 = vy0; yy <= vy1; yy = yy + 1) {
          var horizontal_hits: i32 = 0;
          for (var xx: i32 = hx0; xx <= hx1; xx = xx + 1) {
            horizontal_hits = horizontal_hits + select(0, 1, inside_rounded_rect(vec2<i32>(xx, yy), source_min, source_max, shadow_radius));
          }
          vertical_sum = vertical_sum + floor(f32(horizontal_hits) * 255.0 / hcount);
        }
        let blurred_alpha = floor(vertical_sum / vcount);
        shadow_alpha = floor(blurred_alpha * 0.20) / 255.0;
      }
      mixc = mix(mixc, vec3<f32>(0.0), shadow_alpha);
      mixc = mix(mixc, artwork_color, artwork_alpha);
    }
    // Diagnostic-only native static base: blurred cover, readability overlay,
    // exact shadow and rounded artwork, before every dynamic/text layer.
    if (params.sequence == 0xffffffdfu) {
      let rr=u32(clamp(mixc.r*255.0+0.5,0.0,255.0)); let gg=u32(clamp(mixc.g*255.0+0.5,0.0,255.0));
      let bb=u32(clamp(mixc.b*255.0+0.5,0.0,255.0));
      pixels[i]=rr|(gg<<8u)|(bb<<16u)|(255u<<24u); return;
    }
    mixc = mix(mixc, accent, clamp(bars, 0.0, 1.0));
    mixc = mix(mixc, primary, clamp(wave * 0.55, 0.0, 1.0));
    mixc = mix(mixc, primary, clamp(glyph, 0.0, 1.0));
    mixc = loudness_premultiplied + mixc * (1.0 - clamp(loudness, 0.0, 1.0));
    // drawFilledRoundedRect stores progress RGB directly in the CPU frame.
    // CPU drawProgress writes the accent RGB directly with SetRGBA; its alpha
    // byte is later ignored by the RGB-to-YUV conversion. Preserve those full
    // RGB values here instead of source-over blending by the stored alpha.
    mixc = mix(mixc, accent, rail_track);
    mixc = mix(mixc, accent, thumb);
    // Optional FFmpeg showwaves ground-truth raster. It is a bounded
    // per-frame texture; GPU owns the final source-over composition.
    if ((params.scene_enabled & 32u) != 0u) {
      let wr = params.rects[4];
      if (id.x >= u32(wr.x) && id.x < u32(wr.x + wr.z) && id.y >= u32(wr.y) && id.y < u32(wr.y + wr.w)) {
        let wd = textureDimensions(waveform_tex);
        let wx = min(wd.x - 1u, u32(id.x) - u32(wr.x));
        let wy = min(wd.y - 1u, u32(id.y) - u32(wr.y));
        let wc = textureLoad(waveform_tex, vec2<i32>(i32(wx), i32(wy)), 0);
        mixc = mix(mixc, wc.rgb, wc.a);
      }
    }
    // GPU-native waveform primitive. Each output column selects one bounded
    // sample and draws a one-pixel line around its Q16 amplitude. This is
    // intentionally a small, deterministic primitive; the texture-based
    // showwaves path above remains authoritative until parity is proven.
    if ((params.scene_enabled & 256u) != 0u && params.dynamics[1].z > 0u) {
      let wr = params.rects[4];
      if (id.x >= u32(wr.x) && id.x < u32(wr.x + wr.z) && id.y >= u32(wr.y) && id.y < u32(wr.y + wr.w)) {
        let raw_stereo = (params.scene_enabled & 2048u) != 0u;
        if (raw_stereo) {
          let audio_frames = params.dynamics[1].z;
          let local_x = u32(id.x) - u32(wr.x);
          // FFmpeg derives n=48000/(wave_width*30), adds inv(n) until it
          // reaches one, then resets the rational accumulator. This produces
          // ceil(1600/width) stereo sample frames per output column.
          let samples_per_column = (6400u + u32(wr.z) - 1u) / u32(wr.z);
          let start = local_x * samples_per_column;
          let end = min(audio_frames, start + samples_per_column);
          let wave_center = wr.w / 2;
          let local_y = i32(id.y) - wr.y;
          var coverage: u32 = 0u;
          for (var sample_i = start; sample_i < end; sample_i = sample_i + 1u) {
            for (var channel: u32 = 0u; channel < 2u; channel = channel + 1u) {
              let sample = i32(waveform_samples[sample_i * 2u + channel]) - 32768;
              let magnitude = abs(sample);
              let scaled_abs = (magnitude * wave_center + 16383) / 32767;
              let scaled = select(-scaled_abs, scaled_abs, sample >= 0);
              let sample_y = clamp(wave_center - scaled, 0, wr.w - 1);
              let lo = min(wave_center, sample_y);
              let hi = max(wave_center, sample_y);
              coverage = coverage + select(0u, 1u, local_y >= lo && local_y < hi);
            }
          }
          // draw=scale precomputes x=floor(255/(channels*n)), truncates each
          // channel contribution, then accumulates into uint8 RGBA (wrapping).
          let contribution_scale = (255u * u32(wr.z)) / 12800u;
          let accent_bytes = params.palette[1];
          let accumulated_r = (accent_bytes.x * contribution_scale / 255u * coverage) & 255u;
          let accumulated_g = (accent_bytes.y * contribution_scale / 255u * coverage) & 255u;
          let accumulated_b = (accent_bytes.z * contribution_scale / 255u * coverage) & 255u;
          let accumulated_a = (140u * contribution_scale / 255u * coverage) & 255u;
          let wave_rgb = vec3<f32>(f32(accumulated_r), f32(accumulated_g), f32(accumulated_b)) / 255.0;
          let wave_alpha = f32(accumulated_a) / 255.0;
          if (params.sequence == 0xffffffdeu) {
            pixels[i] = accumulated_r | (accumulated_g << 8u) |
              (accumulated_b << 16u) | (accumulated_a << 24u);
            return;
          }
          mixc = mix(mixc, wave_rgb, wave_alpha);
        } else {
        let u = clamp((f32(id.x) - f32(wr.x)) / max(1.0, f32(wr.z - 1)), 0.0, 1.0);
        // Min/max mode stores two signed Q16 values per column. Interpolate
        // both endpoints in screen space so narrow transients survive the
        // upload instead of collapsing to a midpoint. The column count in
        // dynamics[1].z remains the logical count (not the storage length).
        let minmax = (params.scene_enabled & 1024u) != 0u;
        let columns = params.dynamics[1].z;
        let fi = u * f32(max(1u, columns - 1u));
        let c0 = min(columns - 1u, u32(fi));
        let c1 = min(columns - 1u, c0 + 1u);
        let frac = fi - f32(c0);
        let raw0 = f32(waveform_samples[c0]) / 65535.0;
        let raw1 = f32(waveform_samples[c1]) / 65535.0;
        let signed_mode = (params.scene_enabled & 512u) != 0u;
        let a0 = select(raw0, raw0 * 2.0 - 1.0, signed_mode);
        let a1 = select(raw1, raw1 * 2.0 - 1.0, signed_mode);
        var y0 = select(f32(wr.y + wr.w) - a0 * f32(wr.w),
          f32(wr.y) + f32(wr.w) * 0.5 - a0 * f32(wr.w) * 0.5,
          signed_mode);
        var y1 = select(f32(wr.y + wr.w) - a1 * f32(wr.w),
          f32(wr.y) + f32(wr.w) * 0.5 - a1 * f32(wr.w) * 0.5,
          signed_mode);
        if (minmax) {
          let lo0 = f32(waveform_samples[c0 * 2u]) / 65535.0;
          let hi0 = f32(waveform_samples[c0 * 2u + 1u]) / 65535.0;
          let lo1 = f32(waveform_samples[c1 * 2u]) / 65535.0;
          let hi1 = f32(waveform_samples[c1 * 2u + 1u]) / 65535.0;
          let low = mix(lo0, lo1, frac) * 2.0 - 1.0;
          let high = mix(hi0, hi1, frac) * 2.0 - 1.0;
          y0 = f32(wr.y) + f32(wr.w) * 0.5 - high * f32(wr.w) * 0.5;
          y1 = f32(wr.y) + f32(wr.w) * 0.5 - low * f32(wr.w) * 0.5;
        } else {
          let y = mix(y0, y1, frac);
          y0 = y;
          y1 = y;
        }
        // FFmpeg showwaves mode=line fills each sample column from the vertical
        // centre to the sample height (it is not a point-to-point trace). For
        // a min/max window, union both signed endpoints with that centre line.
        let wave_center = f32(wr.y) + f32(wr.w) * 0.5;
        var draw_min = min(y0, y1);
        var draw_max = max(y0, y1);
        if (minmax || signed_mode) {
          draw_min = min(wave_center, min(y0, y1));
          draw_max = max(wave_center, max(y0, y1));
        }
        let py = f32(id.y) + 0.5;
        if (py >= draw_min - 0.5 && py <= draw_max + 0.5) {
          // Match the CPU/FFmpeg showwaves source alpha (0.55).
          mixc = mix(mixc, accent, 0.55);
        }
        }
      }
    }
    if (params.sequence == 0xffffffdeu) { pixels[i] = 0u; return; }
    if ((params.scene_enabled & 128u) != 0u) {
      let sr = params.rects[4];
      let sd = textureDimensions(spectrum_tex);
      if (id.x >= u32(sr.x) && id.x < u32(sr.x + sr.z) && id.y >= u32(sr.y) && id.y < u32(sr.y + sr.w)) {
        let sx2 = min(sd.x - 1u, id.x - u32(sr.x));
        let sy2 = min(sd.y - 1u, id.y - u32(sr.y));
        let sc = textureLoad(spectrum_tex, vec2<i32>(i32(sx2), i32(sy2)), 0);
        mixc = mix(mixc, sc.rgb, sc.a);
      }
    }
    // Canonical CPU loudness raster. The payload is full-frame RGBA, while
    // only the bounded loudness rect contains non-transparent pixels. Apply
    // it source-over after the waveform texture and before the final text
    // overlay. Diagnostic loudness-off mode intentionally bypasses it.
    if ((params.scene_enabled & 64u) != 0u && params.sequence != 0xffffffe1u) {
      let lr = params.rects[5];
      if (id.x >= u32(lr.x) && id.x < u32(lr.x + lr.z) && id.y >= u32(lr.y) && id.y < u32(lr.y + lr.w)) {
        let lc = textureLoad(loudness_tex, vec2<i32>(id.xy), 0);
        mixc = mixc * (1.0 - lc.a) + lc.rgb;
      }
    }
    // A screen_rgba overlay is already premultiplied and uses target-space
    // texels. Apply it last, after the dynamic scene layers, matching the
    // CPU ASS ordering. The scene bit is only set for the new explicit
    // contract kind; legacy atlas payloads remain on the diagnostic path.
    if ((params.scene_enabled & 16u) != 0u) {
      let td = textureDimensions(overlay_tex);
      if (id.x < td.x && id.y < td.y) {
      let tc = textureLoad(overlay_tex, vec2<i32>(id.xy), 0);
        mixc = mixc * (1.0 - tc.a) + tc.rgb;
      }
    }
    // Edge fades belong to the animated foreground layers in the CPU
    // compositor; applying their product to the entire frame darkens the
    // artwork/background and cannot match the reference renderer.
    r = u32(clamp(mixc.r * 255.0 + 0.5, 0.0, 255.0));
    g = u32(clamp(mixc.g * 255.0 + 0.5, 0.0, 255.0));
    b = u32(clamp(mixc.b * 255.0 + 0.5, 0.0, 255.0));
  }
  pixels[i] = r | (g << 8u) | (b << 16u) | (255u << 24u);
}
"#;

// FFmpeg gblur is a separable recursive filter. Assigning one invocation to
// each complete row/column preserves the recurrence order while still running
// all independent rows/columns in parallel on the GPU.
const BLUR_SHADER: &str = r#"
struct BlurParams {
  width: u32,
  height: u32,
  nu: f32,
  boundary_scale: f32,
  postscale: f32,
  fallback: u32,
  artwork_size: u32,
  _pad0: u32,
  primary: vec4<u32>,
  accent: vec4<u32>,
  background: vec4<u32>,
  fallback_end: vec4<u32>,
}
@group(0) @binding(0) var source_tex: texture_2d<f32>;
@group(0) @binding(1) var source_sampler: sampler;
@group(0) @binding(2) var<storage, read_write> values: array<vec4<f32>>;
@group(0) @binding(3) var<uniform> p: BlurParams;
@group(0) @binding(4) var<storage, read> fallback_fingerprint: array<u32>;

fn blur_distance_to_segment(point: vec2<f32>, a: vec2<f32>, b: vec2<f32>) -> f32 {
  let ab = b - a;
  let t = clamp(dot(point - a, ab) / max(dot(ab, ab), 0.0001), 0.0, 1.0);
  return distance(point, a + t * ab);
}

fn blur_fingerprint_stamp_hits(point: vec2<f32>, a: vec2<f32>, b: vec2<f32>) -> u32 {
  let delta = b - a;
  let dist = length(delta);
  if (dist < 0.5) {
    let centre = floor(a + vec2<f32>(0.5));
    return select(0u, 1u, abs(point.x - centre.x) <= 1.0 && abs(point.y - centre.y) <= 1.0);
  }
  let steps = max(1u, u32(dist) * 2u);
  var low = 0.0; var high = 1.0;
  let box_low = point - vec2<f32>(1.5); let box_high = point + vec2<f32>(1.5);
  if (abs(delta.x) < 0.000001) {
    if (a.x < box_low.x || a.x >= box_high.x) { return 0u; }
  } else {
    let t0 = (box_low.x-a.x)/delta.x; let t1 = (box_high.x-a.x)/delta.x;
    low=max(low,min(t0,t1)); high=min(high,max(t0,t1));
  }
  if (abs(delta.y) < 0.000001) {
    if (a.y < box_low.y || a.y >= box_high.y) { return 0u; }
  } else {
    let t0 = (box_low.y-a.y)/delta.y; let t1 = (box_high.y-a.y)/delta.y;
    low=max(low,min(t0,t1)); high=min(high,max(t0,t1));
  }
  if (high<=low || high<0.0 || low>1.0) { return 0u; }
  low=clamp(low,0.0,1.0); high=clamp(high,0.0,1.0);
  let first=min(steps,u32(max(0.0,ceil(low*f32(steps)-0.000001))));
  var last=steps;
  if (high<1.0) { last=u32(max(0.0,ceil(high*f32(steps)-0.000001)-1.0)); }
  return select(0u,last-first+1u,last>=first);
}

fn fallback_source_pixel(coord: vec2<i32>) -> vec4<f32> {
  let size = max(1.0, f32(p.artwork_size));
  let point = vec2<f32>(clamp(coord, vec2<i32>(0), vec2<i32>(i32(p.artwork_size) - 1)));
  let start = vec3<f32>(p.background.xyz) / 255.0;
  let finish = vec3<f32>(p.fallback_end.xyz) / 255.0;
  let ink = vec3<f32>(p.primary.xyz) / 255.0;
  var c = floor(mix(start, finish, point.y / max(1.0, size - 1.0)) * 255.0) / 255.0;
  let centre = vec2<f32>(size * 0.5);
  let scale = size / 288.0;
  var fingerprint_hits = 0u;
  for (var band: u32 = 0u; band < 64u; band = band + 1u) {
    let angle = -1.5707963268 + f32(band) * 6.2831853072 / 64.0;
    let direction = vec2<f32>(cos(angle), sin(angle));
    let energy = f32(fallback_fingerprint[band]) / 65535.0;
    let a = centre + direction * (54.0 * scale);
    let b = centre + direction * ((54.0 + energy * 58.0) * scale);
    fingerprint_hits = fingerprint_hits + blur_fingerprint_stamp_hits(point, a, b);
  }
  var fingerprint_bytes = vec3<u32>(clamp(c*255.0+vec3<f32>(0.5),vec3<f32>(0.0),vec3<f32>(255.0)));
  let fingerprint_ink = p.primary.xyz;
  for (var hit=0u;hit<fingerprint_hits;hit=hit+1u) {
    fingerprint_bytes=(fingerprint_ink*66u+fingerprint_bytes*189u)/255u;
  }
  c=vec3<f32>(fingerprint_bytes)/255.0;
  let note_uv = (point + vec2<f32>(0.5)) / vec2<f32>(size);
  let note_alpha = textureSampleLevel(source_tex, source_sampler, note_uv, 0.0).a;
  let note_a = u32(clamp(note_alpha * 255.0 + 0.5, 0.0, 255.0));
  if (note_a > 0u) {
    let background_bytes = vec3<u32>(clamp(c * 255.0 + vec3<f32>(0.5), vec3<f32>(0.0), vec3<f32>(255.0)));
    let inverse = (65535u - note_a * 257u) * 257u;
    let wrapped = vec3<u32>(
      ((background_bytes.r * inverse / 65535u + p.primary.r * 257u) >> 8u) & 255u,
      ((background_bytes.g * inverse / 65535u + p.primary.g * 257u) >> 8u) & 255u,
      ((background_bytes.b * inverse / 65535u + p.primary.b * 257u) >> 8u) & 255u);
    c = vec3<f32>(wrapped) / 255.0;
  }
  return vec4<f32>(c, 1.0);
}

fn cover_uv(x: u32, y: u32) -> vec2<f32> {
  let texture_dims = vec2<f32>(textureDimensions(source_tex));
  let source_dims = select(texture_dims, vec2<f32>(f32(p.artwork_size)), p.fallback != 0u);
  let source_aspect = source_dims.x / max(1.0, source_dims.y);
  let frame_aspect = f32(p.width) / max(1.0, f32(p.height));
  var uv = (vec2<f32>(f32(x), f32(y)) + vec2<f32>(0.5)) /
    vec2<f32>(f32(p.width), f32(p.height));
  if (source_aspect > frame_aspect) {
    uv.x = (uv.x - 0.5) * frame_aspect / source_aspect + 0.5;
  } else {
    uv.y = (uv.y - 0.5) * source_aspect / frame_aspect + 0.5;
  }
  return clamp(uv, vec2<f32>(0.0), vec2<f32>(1.0));
}

fn bicubic_weight(value: f32) -> f32 {
  // swscale's default bicubic uses the Keys family with a=-0.6.
  let x = abs(value);
  let a = -0.6;
  if (x < 1.0) { return (a + 2.0) * x * x * x - (a + 3.0) * x * x + 1.0; }
  if (x < 2.0) { return a * x * x * x - 5.0 * a * x * x + 8.0 * a * x - 4.0 * a; }
  return 0.0;
}

fn bicubic_source(uv: vec2<f32>) -> vec4<f32> {
  let texture_dims_u = textureDimensions(source_tex);
  let dims_u = select(texture_dims_u, vec2<u32>(p.artwork_size), p.fallback != 0u);
  let dims = vec2<f32>(dims_u);
  let position = uv * dims - vec2<f32>(0.5);
  let base = vec2<i32>(floor(position));
  var sum = vec4<f32>(0.0);
  var total = 0.0;
  for (var oy: i32 = -1; oy <= 2; oy = oy + 1) {
    let wy = bicubic_weight(position.y - f32(base.y + oy));
    for (var ox: i32 = -1; ox <= 2; ox = ox + 1) {
      let wx = bicubic_weight(position.x - f32(base.x + ox));
      let weight = wx * wy;
      let source = vec2<i32>(clamp(base + vec2<i32>(ox, oy), vec2<i32>(0), vec2<i32>(dims_u) - vec2<i32>(1)));
      let sample = select(textureLoad(source_tex, source, 0), fallback_source_pixel(source), p.fallback != 0u);
      sum = sum + sample * weight;
      total = total + weight;
    }
  }
  return clamp(sum / max(total, 0.000001), vec4<f32>(0.0), vec4<f32>(1.0));
}

@compute @workgroup_size(64)
fn horizontal(@builtin(global_invocation_id) id: vec3<u32>) {
  let y = id.x;
  if (y >= p.height) { return; }
  let row = y * p.width;
  for (var x: u32 = 0u; x < p.width; x = x + 1u) {
    // swscale writes its scaled result to the 8-bit intermediate frame
    // before gblur reads it. Preserve that byte-domain quantization here.
    values[row + x] = floor(bicubic_source(cover_uv(x, y)) * 255.0 + vec4<f32>(0.5));
  }
  values[row] = values[row] * p.boundary_scale;
  for (var x: u32 = 1u; x < p.width; x = x + 1u) {
    values[row + x] = values[row + x] + p.nu * values[row + x - 1u];
  }
  let last = row + p.width - 1u;
  values[last] = values[last] * p.boundary_scale;
  for (var x: u32 = p.width - 1u; x > 0u; x = x - 1u) {
    values[row + x - 1u] = values[row + x - 1u] + p.nu * values[row + x];
  }
}

@compute @workgroup_size(64)
fn vertical(@builtin(global_invocation_id) id: vec3<u32>) {
  let x = id.x;
  if (x >= p.width) { return; }
  values[x] = values[x] * p.boundary_scale;
  for (var y: u32 = 1u; y < p.height; y = y + 1u) {
    let i = y * p.width + x;
    values[i] = values[i] + p.nu * values[i - p.width];
  }
  let last = (p.height - 1u) * p.width + x;
  values[last] = values[last] * p.boundary_scale;
  for (var y: u32 = p.height - 1u; y > 0u; y = y - 1u) {
    let i = y * p.width + x;
    values[i - p.width] = values[i - p.width] + p.nu * values[i];
  }
  for (var y: u32 = 0u; y < p.height; y = y + 1u) {
    let i = y * p.width + x;
    values[i] = floor(clamp(values[i] * p.postscale * p.postscale, vec4<f32>(0.0), vec4<f32>(255.0)) + vec4<f32>(0.5));
  }
}
"#;

/// GPU renderer whose expensive instance/adapter/device/pipeline setup is done
/// once at sidecar startup. Render requests only allocate frame-sized buffers
/// and submit work to the retained queue.
pub struct Renderer {
    device: wgpu::Device,
    queue: wgpu::Queue,
    layout: wgpu::BindGroupLayout,
    pipeline: wgpu::ComputePipeline,
    adapter_name: String,
    runtime_fingerprint: adapter::RuntimeFingerprint,
    yuv_layout: wgpu::BindGroupLayout,
    yuv_pipeline: wgpu::ComputePipeline,
    blur_layout: wgpu::BindGroupLayout,
    blur_horizontal_pipeline: wgpu::ComputePipeline,
    blur_vertical_pipeline: wgpu::ComputePipeline,
}

struct RenderedScene {
    output: wgpu::Buffer,
    stride: u32,
    bytes: usize,
    glyph_atlas_receipt: Option<GlyphAtlasReceipt>,
    text_overlay_receipt: Option<TextOverlayReceipt>,
    artwork_receipt: Option<ArtworkReceipt>,
    base_texture_receipt: Option<BaseTextureReceipt>,
    glyph_diagnostics: GlyphRenderDiagnostics,
}

const YUV_SHADER: &str = r#"
struct Params {
 width: u32, height: u32, rgba_words: u32, flags: u32,
 wave_x: u32, wave_y: u32, wave_w: u32, wave_h: u32,
 audio_frames: u32, accent_r: u32, accent_g: u32, accent_b: u32,
 glyph_count: u32, primary_r: u32, primary_g: u32, primary_b: u32,
}
@group(0) @binding(0) var<storage, read> rgba: array<u32>;
@group(0) @binding(1) var<storage, read_write> y_plane: array<u32>;
@group(0) @binding(2) var<storage, read_write> u_plane: array<u32>;
@group(0) @binding(3) var<storage, read_write> v_plane: array<u32>;
@group(0) @binding(4) var<uniform> p: Params;
@group(0) @binding(5) var<storage, read> waveform_samples: array<u32>;
@group(0) @binding(6) var glyph_atlas: texture_2d<f32>;
struct YuvGlyphInstance { screen: vec4<f32>, atlas: vec4<f32>, color: vec4<f32> }
@group(0) @binding(7) var<storage, read> yuv_glyphs: array<YuvGlyphInstance>;
fn r(c: u32) -> f32 { return f32(c & 255u); }
fn g(c: u32) -> f32 { return f32((c >> 8u) & 255u); }
fn b(c: u32) -> f32 { return f32((c >> 16u) & 255u); }
fn rounded_byte(x: f32) -> u32 { return u32(clamp(x, 0.0, 255.0)+0.5); }
fn yv(c: u32) -> u32 { return rounded_byte(16.0+0.18258588*r(c)+0.61423059*g(c)+0.06200706*b(c)); }
fn uv(c0: u32, c1: u32, c2: u32, c3: u32, which: u32) -> u32 {
 let rr=(r(c0)+r(c1)+r(c2)+r(c3))*0.25; let gg=(g(c0)+g(c1)+g(c2)+g(c3))*0.25; let bb=(b(c0)+b(c1)+b(c2)+b(c3))*0.25;
 let x=select(128.0+0.43921569*rr-0.39894216*gg-0.04027352*bb,128.0-0.10064373*rr-0.33857195*gg+0.43921569*bb,which==1u); return rounded_byte(x);
}
// FFmpeg negotiates the RGBA showwaves overlay to yuva420p with swscale's
// limited-range BT.601 matrix before vf_overlay sees it. The main frame is
// already BT.709 YUV, so using the main-frame matrix for the overlay source
// shifts every coloured waveform edge.
fn wave_yv(c: u32) -> u32 {
 return rounded_byte(16.0+0.25678824*r(c)+0.50412941*g(c)+0.09790588*b(c));
}
fn wave_uv(c0: u32, c1: u32, c2: u32, c3: u32, which: u32) -> u32 {
 let rr=(r(c0)+r(c1)+r(c2)+r(c3))*0.25; let gg=(g(c0)+g(c1)+g(c2)+g(c3))*0.25; let bb=(b(c0)+b(c1)+b(c2)+b(c3))*0.25;
 let x=select(128.0+0.43921569*rr-0.36778824*gg-0.07142745*bb,128.0-0.14822353*rr-0.29099216*gg+0.43921569*bb,which==1u); return rounded_byte(x);
}
fn wave_rgba(px: u32, py: u32) -> vec4<u32> {
 if((p.flags&1u)==0u || px<p.wave_x || py<p.wave_y || px>=p.wave_x+p.wave_w || py>=p.wave_y+p.wave_h || p.audio_frames==0u){return vec4<u32>(0u);}
 let local_x=px-p.wave_x; let local_y=i32(py-p.wave_y); let center=i32(p.wave_h/2u);
 let samples_per_column=(6400u+p.wave_w-1u)/p.wave_w;
 let start=local_x*samples_per_column; let end=min(p.audio_frames,start+samples_per_column);
 var coverage=0u;
 for(var si=start;si<end;si=si+1u){for(var ch=0u;ch<2u;ch=ch+1u){
  let sample=i32(waveform_samples[si*2u+ch])-32768; let magnitude=abs(sample);
  let scaled_abs=(magnitude*center+16383)/32767; let scaled=select(-scaled_abs,scaled_abs,sample>=0);
  let sample_y=clamp(center-scaled,0,i32(p.wave_h)-1); let lo=min(center,sample_y);let hi=max(center,sample_y);
  coverage=coverage+select(0u,1u,local_y>=lo&&local_y<hi);
 }}
 let scale=(255u*p.wave_w)/12800u;
 return vec4<u32>((p.accent_r*scale/255u*coverage)&255u,(p.accent_g*scale/255u*coverage)&255u,(p.accent_b*scale/255u*coverage)&255u,(140u*scale/255u*coverage)&255u);
}
fn ff_mask_alpha(mask:u32,color_a:u32)->u32{return mask*((0x10307u*color_a+3u)>>8u);}
fn ff_blend_mask_byte(dst:u32,src:u32,mask:u32,color_a:u32)->u32{
 let a=ff_mask_alpha(mask,color_a);return ((0x1010101u-a)*dst+a*src)>>24u;
}
fn glyph_mask(px: u32, py: u32) -> vec2<u32> {
 let pixel=vec2<f32>(f32(px),f32(py)); var result=vec2<u32>(0u);var best_alpha=0u;
 for(var gi:u32=0u;gi<p.glyph_count;gi=gi+1u){
  let glyph=yuv_glyphs[gi];
   let screen=glyph.screen;
  if(pixel.x>=screen.x&&pixel.y>=screen.y&&pixel.x<screen.x+screen.z&&pixel.y<screen.y+screen.w){
   let dims=vec2<i32>(textureDimensions(glyph_atlas));
    let cell_min=vec2<i32>(floor(glyph.atlas.xy));
    let cell_size=max(vec2<i32>(1),vec2<i32>(floor(glyph.atlas.zw)));
    var sample_min=vec2<i32>(0);var sample_max=vec2<i32>(0);
    if(abs(screen.z-f32(cell_size.x))<0.001&&abs(screen.w-f32(cell_size.y))<0.001){
     // Bounded libass runs are 1:1 pixel copies. Avoid divide/multiply
     // roundoff here: a value infinitesimally below an integer made floor()
     // select the neighbouring antialias texel on long 48px title runs.
     sample_min=clamp(vec2<i32>(i32(px)-i32(screen.x),i32(py)-i32(screen.y)),vec2<i32>(0),cell_size-vec2<i32>(1));sample_max=sample_min;
    }else{
     let local=(pixel-screen.xy)/max(screen.zw,vec2<f32>(1.0));
     let local_next=((pixel+vec2<f32>(1.0))-screen.xy)/max(screen.zw,vec2<f32>(1.0));
     sample_min=clamp(vec2<i32>(floor(local*vec2<f32>(cell_size))),vec2<i32>(0),cell_size-vec2<i32>(1));
     sample_max=clamp(vec2<i32>(ceil(local_next*vec2<f32>(cell_size)))-vec2<i32>(1),sample_min,cell_size-vec2<i32>(1));
    }
    var sum=0.0;var count=0.0;
    for(var ay:i32=sample_min.y;ay<=sample_max.y;ay=ay+1){for(var ax:i32=sample_min.x;ax<=sample_max.x;ax=ax+1){sum=sum+textureLoad(glyph_atlas,cell_min+vec2<i32>(ax,ay),0).a;count=count+1.0;}}
    let mask=rounded_byte(sum/max(1.0,count)*255.0);let color_a=rounded_byte(glyph.color.a*255.0);let a=ff_mask_alpha(mask,color_a);
    if(a>best_alpha){best_alpha=a;result=vec2<u32>(mask,color_a);}
   }
  }
 return result;
}
fn over_byte(dst:u32,src:u32,a:u32)->u32{return (src*a+dst*(255u-a)+127u)/255u;}
@compute @workgroup_size(8,8,1) fn main(@builtin(global_invocation_id) id:vec3<u32>) {
 if(id.x>=p.width||id.y>=p.height){return;} let idx=id.y*p.width+id.x; let c=rgba[id.y*p.rgba_words+id.x]; let w0=wave_rgba(id.x,id.y);let pc=p.primary_r|(p.primary_g<<8u)|(p.primary_b<<16u);let gm= glyph_mask(id.x,id.y);let wave_y=over_byte(yv(c),wave_yv(w0.x|(w0.y<<8u)|(w0.z<<16u)),w0.a);y_plane[idx]=ff_blend_mask_byte(wave_y,yv(pc),gm.x,gm.y);
 if((id.x&1u)==0u&&(id.y&1u)==0u){let cw=(p.width+1u)/2u;let x1=min(id.x+1u,p.width-1u);let y1=min(id.y+1u,p.height-1u);let c1=rgba[id.y*p.rgba_words+x1];let c2=rgba[y1*p.rgba_words+id.x];let c3=rgba[y1*p.rgba_words+x1];let ci=(id.y/2u)*cw+id.x/2u;
  let w1=wave_rgba(x1,id.y);let w2=wave_rgba(id.x,y1);let w3=wave_rgba(x1,y1);let wa=(w0.a+w1.a+w2.a+w3.a)/4u;
  let wc0=w0.x|(w0.y<<8u)|(w0.z<<16u);let wc1=w1.x|(w1.y<<8u)|(w1.z<<16u);let wc2=w2.x|(w2.y<<8u)|(w2.z<<16u);let wc3=w3.x|(w3.y<<8u)|(w3.z<<16u);
  let wave_u=over_byte(uv(c,c1,c2,c3,1u),wave_uv(wc0,wc1,wc2,wc3,1u),wa);let wave_v=over_byte(uv(c,c1,c2,c3,0u),wave_uv(wc0,wc1,wc2,wc3,0u),wa);
  let gm1=glyph_mask(x1,id.y);let gm2=glyph_mask(id.x,y1);let gm3=glyph_mask(x1,y1);let gmask=(gm.x+gm1.x+gm2.x+gm3.x)/4u;let gca=max(max(gm.y,gm1.y),max(gm2.y,gm3.y));
  u_plane[ci]=ff_blend_mask_byte(wave_u,uv(pc,pc,pc,pc,1u),gmask,gca);v_plane[ci]=ff_blend_mask_byte(wave_v,uv(pc,pc,pc,pc,0u),gmask,gca);}
}
"#;

fn scene_uniform_words(
    width: u32,
    height: u32,
    stride: u32,
    sequence: u64,
    scene: Option<&MusicScenePayload>,
) -> [u32; 96] {
    let mut words = [0u32; 96];
    words[0] = width;
    words[1] = height;
    words[2] = stride / 4;
    words[3] = sequence as u32;
    if let Some(scene) = scene {
        words[4] = scene.feature.rms_q15 as u32;
        words[5] = scene.feature.peak_q15 as u32;
        words[6] =
            1 | if scene
                .artwork
                .as_ref()
                .is_some_and(|a| !a.payload.is_empty())
            {
                2
            } else {
                0
            } | if scene
                .glyph_atlas
                .as_ref()
                .is_some_and(|a| !a.payload.is_empty() && !a.glyphs.is_empty())
            {
                4
            } else {
                0
            } | if scene
                .base_texture
                .as_ref()
                .is_some_and(|b| !b.payload.is_empty())
            {
                8
            } else {
                0
            } | if scene
                .text_overlay
                .as_ref()
                .is_some_and(|o| o.kind == "screen_rgba" && !o.payload.is_empty())
            {
                16
            } else {
                0
            };
        words[6] |= if scene
            .waveform_texture
            .as_ref()
            .is_some_and(|w| !w.payload.is_empty())
        {
            32
        } else {
            0
        };
        words[6] |= if scene
            .loudness_texture
            .as_ref()
            .is_some_and(|w| !w.payload.is_empty())
        {
            64
        } else {
            0
        };
        words[6] |= if scene
            .spectrum_texture
            .as_ref()
            .is_some_and(|w| !w.payload.is_empty())
        {
            128
        } else {
            0
        };
        // Bit 256 enables the bounded waveform_q16 storage path. Keep the
        // legacy waveform texture bit (32) independent for parity diagnostics.
        words[6] |= if !scene.feature.waveform_q16.is_empty() {
            256
        } else {
            0
        };
        // Missing artwork is an explicit native-GPU fallback scene. No pixel
        // payload is synthesized on the control-plane CPU.
        if scene.artwork.is_none() && scene.base_texture.is_none() {
            words[6] |= 4096;
        }
        // Experimental signed-centre waveform encoding. Keep opt-in so legacy
        // unsigned payloads and parity diagnostics remain unchanged.
        if !scene.feature.waveform_q16.is_empty()
            && env::var("IMAGEPAD_GPU_WAVEFORM_SIGNED").as_deref() == Ok("1")
        {
            words[6] |= 512;
        }
        // Experimental signed min/max envelope encoding. The payload is
        // [min,max] pairs, while words[38] below carries the logical column
        // count. Keep this opt-in and independent from the legacy single
        // sample/signed paths so existing captures remain reproducible.
        if !scene.feature.waveform_q16.is_empty()
            && env::var("IMAGEPAD_GPU_WAVEFORM_MINMAX").as_deref() == Ok("1")
            && scene.feature.waveform_q16.len() >= 2
            && scene.feature.waveform_q16.len() % 2 == 0
        {
            words[6] |= 1024;
        }
        // Raw stereo mode transports one centre-biased Q16 value per channel
        // in interleaved order. The shader reproduces showwaves mode=line
        // directly from these samples.
        if !scene.feature.waveform_q16.is_empty()
            && scene.feature.waveform_q16.len() >= 2
            && scene.feature.waveform_q16.len() % 2 == 0
        {
            words[6] |= 2048;
        }
        // The shader iterates the instance buffer, not the unique atlas-cell
        // table. Repeated letters and later text runs must therefore count as
        // separate instances; using atlas.glyphs.len() truncated album/time
        // rendering as soon as the unique-rune count was reached.
        words[7] = glyph_instance_words(Some(scene), width, height).1.count as u32;
        for (dst, src) in words[8..]
            .iter_mut()
            .zip(scene.feature.spectrum_q16.iter().copied())
        {
            *dst = src as u32;
        }
        let d = &scene.dynamics;
        words[32] = (d.current_seconds.clamp(0.0, 4_294_967.0) * 1000.0) as u32;
        words[33] = (d.duration_seconds.clamp(0.0, 4_294_967.0) * 1000.0) as u32;
        words[34] = (d.progress_ratio.clamp(0.0, 1.0) * 65535.0) as u32;
        words[35] = ((d.edge_fade_alpha.clamp(0.0, 1.0) * d.end_fade_alpha.clamp(0.0, 1.0))
            * 65535.0) as u32;
        words[36] = d
            .loudness_envelope
            .first()
            .copied()
            .unwrap_or(scene.feature.rms_q15) as u32;
        words[37] = d
            .loudness_trend
            .first()
            .copied()
            .unwrap_or(scene.feature.rms_q15) as u32;
        let waveform_len = scene
            .feature
            .waveform_q16
            .len()
            .min(MUSIC_MAX_WAVEFORM_SAMPLES);
        words[38] = if waveform_len >= 2 && waveform_len % 2 == 0 {
            (waveform_len / 2) as u32
        } else if env::var("IMAGEPAD_GPU_WAVEFORM_MINMAX").as_deref() == Ok("1")
            && waveform_len % 2 == 0
        {
            (waveform_len / 2) as u32
        } else {
            waveform_len as u32
        };
        let p = &scene.palette;
        words[40..44].copy_from_slice(&p.primary.map(|v| v as u32));
        words[44..48].copy_from_slice(&p.accent.map(|v| v as u32));
        words[48..52].copy_from_slice(&p.background.map(|v| v as u32));
        words[52..56].copy_from_slice(&p.overlay.map(|v| v as u32));
        let rects = [
            &scene.layout.artwork,
            &scene.layout.title,
            &scene.layout.artist,
            &scene.layout.album,
            &scene.layout.spectrum,
            &scene.layout.loudness,
            &scene.layout.progress,
            &scene.layout.time,
        ];
        // Layout coordinates are canonical 1280x720 units; scale them to the
        // actual output so 180p comparison renders retain the same composition.
        for (i, r) in rects.iter().enumerate() {
            // The diagnostic overlay probe compares against the Go helper,
            // which places the canonical overlay using the scene's direct
            // screen-space rectangles. Production rendering retains the
            // canonical 1280x720 scaling.
            let (sx, sy) = if sequence == 0xfffffffe {
                (1.0, 1.0)
            } else {
                (width as f32 / 1280.0, height as f32 / 720.0)
            };
            words[56 + i * 4..60 + i * 4].copy_from_slice(&[
                (r.x as f32 * sx) as u32,
                (r.y as f32 * sy) as u32,
                (r.w as f32 * sx) as u32,
                (r.h as f32 * sy) as u32,
            ]);
        }
        words[88..92].copy_from_slice(&d.loudness_guides.map(|v| v as u32));
        words[92..96].copy_from_slice(&p.fallback_end.map(|v| v as u32));
    }
    words
}

fn glyph_instance_words(
    scene: Option<&MusicScenePayload>,
    width: u32,
    height: u32,
) -> (Vec<u32>, GlyphRenderDiagnostics) {
    let mut out = Vec::new();
    let mut diagnostics = Vec::new();
    let Some(atlas) = scene.and_then(|s| s.glyph_atlas.as_ref()) else {
        return (
            out,
            GlyphRenderDiagnostics {
                count: 0,
                instances: diagnostics,
            },
        );
    };
    if atlas.payload.is_empty() || (atlas.glyphs.is_empty() && atlas.bitmap_runs.is_empty()) {
        return (
            out,
            GlyphRenderDiagnostics {
                count: 0,
                instances: diagnostics,
            },
        );
    }
    // Exact libass runs are already rasterized at the output canvas size. They
    // remain bounded source textures: this function only expands placement
    // metadata and the compute shader performs the final full-frame blend.
    for run in atlas.bitmap_runs.iter().take(256) {
        if out.len() / 12 >= 256 {
            break;
        }
        let vals = [
            run.screen_rect.x as f32,
            run.screen_rect.y as f32,
            run.screen_rect.w as f32,
            run.screen_rect.h as f32,
            run.atlas_rect.x as f32,
            run.atlas_rect.y as f32,
            run.atlas_rect.w as f32,
            run.atlas_rect.h as f32,
            run.rgba[0] as f32 / 255.0,
            run.rgba[1] as f32 / 255.0,
            run.rgba[2] as f32 / 255.0,
            (run.rgba[3] as f32 / 255.0) * run.opacity.clamp(0.0, 1.0),
        ];
        out.extend(vals.into_iter().map(f32::to_bits));
        diagnostics.push(GlyphInstanceDiagnostic {
            id: run.id.clone(),
            screen: [vals[0], vals[1], vals[2], vals[3]],
            atlas: [vals[4], vals[5], vals[6], vals[7]],
            color: [vals[8], vals[9], vals[10], vals[11]],
            scale: 1.0,
            baseline: run.screen_rect.y as f32,
            ink_top: 0.0,
            cell_padding: [0.0; 4],
        });
    }
    // TextRun coordinates are part of the canonical 1280x720 scene contract.
    // Convert them to the actual render target here; the previous code treated
    // canonical pixels as target pixels, pushing title/artist/album glyphs out
    // of frame at the 360p comparison size.
    let sx = width as f32 / 1280.0;
    let sy = height as f32 / 720.0;
    for run in atlas.text_runs.iter().take(256) {
        let mut cursor = run.x * sx;
        for ch in run.text.chars() {
            if out.len() / 12 >= 256 {
                break;
            }
            let plain_id = ch.to_string();
            let id = if run.font_weight == 0 {
                plain_id.clone()
            } else {
                format!("{}:{}", run.font_weight, ch)
            };
            let weighted_missing_id = if run.font_weight == 0 {
                atlas.missing_glyph_id.clone()
            } else {
                format!("{}:{}", run.font_weight, atlas.missing_glyph_id)
            };
            let glyph = atlas
                .glyphs
                .iter()
                .find(|g| g.id == id)
                // Accept legacy atlases while all producers migrate to the
                // weight-qualified ID contract.
                .or_else(|| atlas.glyphs.iter().find(|g| g.id == plain_id))
                .or_else(|| atlas.glyphs.iter().find(|g| g.id == weighted_missing_id))
                .or_else(|| atlas.glyphs.iter().find(|g| g.id == atlas.missing_glyph_id));
            let Some(g) = glyph else { continue };
            // The canonical atlas rasterizes a 48px Noto face into a 64px cell. libass
            // sizes the CPU reference against the em box rather than the
            // visible glyph bitmap, so scale the atlas by that 64px em. Using
            // 48 here made every GPU label roughly one third too large.
            const ATLAS_FACE_SIZE: f32 = 72.0;
            const ATLAS_INK_TOP: f32 = 7.0;
            let scale = run.size_px / ATLAS_FACE_SIZE;
            let sw = (g.width as f32 * scale).max(1.0);
            let sh = (g.height as f32 * scale * sy).max(1.0);
            let screen_y = (run.y - ATLAS_INK_TOP * scale).max(0.0) * sy;
            let vals = [
                cursor,
                screen_y,
                sw * sx,
                sh,
                g.x as f32,
                g.y as f32,
                g.width as f32,
                g.height as f32,
                run.rgba[0] as f32 / 255.0,
                run.rgba[1] as f32 / 255.0,
                run.rgba[2] as f32 / 255.0,
                (run.rgba[3] as f32 / 255.0) * run.opacity.clamp(0.0, 1.0),
            ];
            out.extend(vals.into_iter().map(f32::to_bits));
            diagnostics.push(GlyphInstanceDiagnostic {
                id: g.id.clone(),
                screen: [vals[0], vals[1], vals[2], vals[3]],
                atlas: [vals[4], vals[5], vals[6], vals[7]],
                color: [vals[8], vals[9], vals[10], vals[11]],
                scale,
                baseline: run.y * sy,
                ink_top: ATLAS_INK_TOP * scale * sy,
                // The deterministic Go atlas uses 64px cells. Padding is
                // exposed so parity tooling can distinguish cell geometry
                // from ink geometry without changing the render path.
                cell_padding: [
                    (64.0 - g.width as f32).max(0.0),
                    (64.0 - g.height as f32).max(0.0),
                    0.0,
                    0.0,
                ],
            });
            // Advance is the font metric measured by the atlas producer. The
            // 64px cell width only bounds sampling; using it for layout turns
            // proportional text into widely spaced monospace text.
            cursor += g.advance.max(1.0) * scale * sx;
        }
    }
    let count = diagnostics.len();
    (
        out,
        GlyphRenderDiagnostics {
            count,
            instances: diagnostics,
        },
    )
}

fn artwork_texture_format() -> wgpu::TextureFormat {
    wgpu::TextureFormat::Rgba8Unorm
}

fn gblur_params(
    width: u32,
    height: u32,
    sigma: f32,
    scene: Option<&MusicScenePayload>,
    fallback: bool,
) -> [u32; 24] {
    // Match vf_gblur set_params(sigma, steps=1).
    let lambda = f64::from(sigma) * f64::from(sigma) / 2.0;
    let dnu = (1.0 + 2.0 * lambda - (1.0 + 4.0 * lambda).sqrt()) / (2.0 * lambda);
    let nu = dnu as f32;
    let postscale = (dnu / lambda) as f32;
    let boundary_scale = (1.0 / (1.0 - dnu)) as f32;
    let mut words = [0u32; 24];
    words[..8].copy_from_slice(&[
        width,
        height,
        nu.to_bits(),
        boundary_scale.to_bits(),
        postscale.to_bits(),
        u32::from(fallback),
        scene.map(|s| s.layout.artwork.w.max(1) as u32).unwrap_or(1),
        0,
    ]);
    if let Some(scene) = scene {
        words[8..12].copy_from_slice(&scene.palette.primary.map(u32::from));
        words[12..16].copy_from_slice(&scene.palette.accent.map(u32::from));
        words[16..20].copy_from_slice(&scene.palette.background.map(u32::from));
        words[20..24].copy_from_slice(&scene.palette.fallback_end.map(u32::from));
    }
    words
}

fn monotone_hermite_words(input: &[f64], output_count: usize) -> Vec<u32> {
    if input.is_empty() || output_count == 0 {
        return Vec::new();
    }
    if input.len() == 1 {
        return vec![(input[0] as f32).to_bits(); output_count];
    }
    let n = input.len();
    let mut secants = vec![0.0f64; n - 1];
    for i in 0..n - 1 {
        secants[i] = input[i + 1] - input[i];
    }
    let mut tangents = vec![0.0f64; n];
    tangents[0] = secants[0];
    tangents[n - 1] = secants[n - 2];
    for i in 1..n - 1 {
        tangents[i] = if secants[i - 1] * secants[i] > 0.0 {
            (secants[i - 1] + secants[i]) / 2.0
        } else {
            0.0
        };
    }
    for i in 0..n - 1 {
        let d = secants[i];
        if d == 0.0 {
            tangents[i] = 0.0;
            tangents[i + 1] = 0.0;
            continue;
        }
        let mut alpha = tangents[i] / d;
        let mut beta = tangents[i + 1] / d;
        if alpha < 0.0 {
            alpha = 0.0;
            tangents[i] = 0.0;
        }
        if beta < 0.0 {
            beta = 0.0;
            tangents[i + 1] = 0.0;
        }
        let square = alpha * alpha + beta * beta;
        if square > 9.0 {
            let tau = 3.0 / square.sqrt();
            tangents[i] = tau * d * alpha;
            tangents[i + 1] = tau * d * beta;
        }
    }
    let mut out = Vec::with_capacity(output_count);
    let max_position = (n - 1) as f64;
    let max_output = output_count.saturating_sub(1).max(1) as f64;
    for j in 0..output_count {
        let x = j as f64 * max_position / max_output;
        let segment = (x.floor() as usize).min(n - 2);
        let t = x - segment as f64;
        let t2 = t * t;
        let t3 = t2 * t;
        let value = (2.0 * t3 - 3.0 * t2 + 1.0) * input[segment]
            + (t3 - 2.0 * t2 + t) * tangents[segment]
            + (-2.0 * t3 + 3.0 * t2) * input[segment + 1]
            + (t3 - t2) * tangents[segment + 1];
        out.push((value.clamp(0.0, 1.0) as f32).to_bits());
    }
    out
}

fn dynamics_sample_words(scene: Option<&MusicScenePayload>, width: u32) -> Vec<u32> {
    // The first 1000 words are the Q16 detail envelope. The remainder is the
    // CPU-contract monotone Hermite trend evaluated at the 4x loudness width;
    // WGSL still owns every raster and Lanczos sample.
    let loudness_width = scene
        .map(|s| ((s.layout.loudness.w as f32 * width as f32 / 1280.0) as usize).max(1))
        .unwrap_or(1);
    let supersampled_width = loudness_width * 4;
    let mut out = vec![0u32; 1000];
    if let Some(scene) = scene {
        let env = &scene.dynamics.loudness_envelope;
        let trend = &scene.dynamics.loudness_trend;
        let mut trend_values = vec![0.0f64; 1000];
        for i in 0..1000 {
            let sample = |values: &Vec<u16>| -> u32 {
                if values.is_empty() {
                    return scene.feature.rms_q15 as u32;
                }
                let idx = i * values.len().saturating_sub(1) / 999;
                values[idx] as u32
            };
            out[i] = sample(env);
            trend_values[i] = sample(trend) as f64 / 65535.0;
        }
        out.extend(monotone_hermite_words(&trend_values, supersampled_width));
    } else {
        out.resize(1000 + supersampled_width, 0);
    }
    out
}

/// Upload the bounded Q16 waveform contract as a read-only storage buffer.
/// Keep one element even when the feature is absent so the binding remains
/// valid on every backend; the shader is gated by the explicit scene bit.
fn waveform_sample_words(scene: Option<&MusicScenePayload>) -> Vec<u32> {
    let Some(scene) = scene else {
        return vec![0];
    };
    if scene.feature.waveform_q16.is_empty() {
        return vec![0];
    }
    scene
        .feature
        .waveform_q16
        .iter()
        .copied()
        .map(u32::from)
        .collect()
}

fn scene_fingerprint_words(scene: Option<&MusicScenePayload>) -> Vec<u32> {
    let Some(scene) = scene else {
        return vec![0; 64];
    };
    let mut out = vec![0u32; 64];
    for (i, value) in scene.feature.fingerprint_q16.iter().take(64).enumerate() {
        out[i] = *value as u32;
    }
    out
}

impl Renderer {
    fn upload_text_overlay(
        &self,
        o: &crate::contracts::TextOverlayMetadata,
    ) -> Result<wgpu::Texture, String> {
        if o.payload.is_empty() || o.width == 0 || o.height == 0 {
            return Err("empty text overlay payload".into());
        }
        // Keep canonical premultiplied bytes numerically identical to the Go
        // parity reference; sRGB decode would alter low-alpha edge luminance.
        let texture = self.device.create_texture(&wgpu::TextureDescriptor {
            label: Some("text-overlay"),
            size: wgpu::Extent3d {
                width: o.width,
                height: o.height,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: wgpu::TextureFormat::Rgba8Unorm,
            usage: wgpu::TextureUsages::COPY_DST | wgpu::TextureUsages::TEXTURE_BINDING,
            view_formats: &[],
        });
        self.queue.write_texture(
            wgpu::ImageCopyTexture {
                texture: &texture,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            &o.payload,
            wgpu::ImageDataLayout {
                offset: 0,
                bytes_per_row: Some(NonZeroU32::new(o.row_stride).unwrap().into()),
                rows_per_image: Some(NonZeroU32::new(o.height).unwrap().into()),
            },
            wgpu::Extent3d {
                width: o.width,
                height: o.height,
                depth_or_array_layers: 1,
            },
        );
        Ok(texture)
    }
    fn upload_artwork(
        &self,
        artwork: &crate::contracts::ArtworkMetadata,
    ) -> Result<wgpu::Texture, String> {
        artwork.validate().map_err(|e| format!("artwork: {e:?}"))?;
        if artwork.payload.is_empty() {
            return Err("empty artwork payload".into());
        }
        let texture = self.device.create_texture(&wgpu::TextureDescriptor {
            label: Some("music-artwork"),
            size: wgpu::Extent3d {
                width: artwork.width,
                height: artwork.height,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            // FFmpeg's reference scale/gblur pipeline operates on the stored
            // 8-bit sRGB values. An Srgb texture would silently linearize the
            // samples and make the GPU background far too dark before those
            // values are written back as video bytes.
            format: artwork_texture_format(),
            usage: wgpu::TextureUsages::COPY_DST | wgpu::TextureUsages::TEXTURE_BINDING,
            view_formats: &[],
        });
        self.queue.write_texture(
            wgpu::ImageCopyTexture {
                texture: &texture,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            &artwork.payload,
            wgpu::ImageDataLayout {
                offset: 0,
                bytes_per_row: Some(NonZeroU32::new(artwork.row_stride).unwrap().into()),
                rows_per_image: Some(NonZeroU32::new(artwork.height).unwrap().into()),
            },
            wgpu::Extent3d {
                width: artwork.width,
                height: artwork.height,
                depth_or_array_layers: 1,
            },
        );
        Ok(texture)
    }

    fn upload_base_texture(
        &self,
        base: &crate::contracts::BaseTextureMetadata,
    ) -> Result<wgpu::Texture, String> {
        if base.payload.is_empty() || base.width == 0 || base.height == 0 {
            return Err("empty base texture payload".into());
        }
        let texture = self.device.create_texture(&wgpu::TextureDescriptor {
            label: Some("music-base-texture"),
            size: wgpu::Extent3d {
                width: base.width,
                height: base.height,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: wgpu::TextureFormat::Rgba8Unorm,
            usage: wgpu::TextureUsages::COPY_DST | wgpu::TextureUsages::TEXTURE_BINDING,
            view_formats: &[],
        });
        self.queue.write_texture(
            wgpu::ImageCopyTexture {
                texture: &texture,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            &base.payload,
            wgpu::ImageDataLayout {
                offset: 0,
                bytes_per_row: Some(NonZeroU32::new(base.row_stride).unwrap().into()),
                rows_per_image: Some(NonZeroU32::new(base.height).unwrap().into()),
            },
            wgpu::Extent3d {
                width: base.width,
                height: base.height,
                depth_or_array_layers: 1,
            },
        );
        Ok(texture)
    }

    fn upload_glyph_atlas(
        &self,
        atlas: &crate::contracts::GlyphAtlasMetadata,
    ) -> Result<wgpu::Texture, String> {
        atlas
            .validate()
            .map_err(|e| format!("glyph atlas: {e:?}"))?;
        if atlas.payload.is_empty() {
            return Err("empty glyph atlas payload".into());
        }
        let texture = self.device.create_texture(&wgpu::TextureDescriptor {
            label: Some("music-glyph-atlas"),
            size: wgpu::Extent3d {
                width: atlas.width,
                height: atlas.height,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: wgpu::TextureFormat::Rgba8UnormSrgb,
            usage: wgpu::TextureUsages::COPY_DST | wgpu::TextureUsages::TEXTURE_BINDING,
            view_formats: &[],
        });
        self.queue.write_texture(
            wgpu::ImageCopyTexture {
                texture: &texture,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            &atlas.payload,
            wgpu::ImageDataLayout {
                offset: 0,
                bytes_per_row: Some(NonZeroU32::new(atlas.row_stride).unwrap().into()),
                rows_per_image: Some(NonZeroU32::new(atlas.height).unwrap().into()),
            },
            wgpu::Extent3d {
                width: atlas.width,
                height: atlas.height,
                depth_or_array_layers: 1,
            },
        );
        Ok(texture)
    }

    pub fn new() -> Result<Self, String> {
        let instance = wgpu::Instance::default();
        let selected = adapter::select(&instance).map_err(|e| format!("gpu adapter: {e}"))?;
        let adapter_name = selected.info.name.clone();
        let runtime_fingerprint = selected.fingerprint();
        let (device, queue) = pollster::block_on(
            selected
                .adapter
                .request_device(&wgpu::DeviceDescriptor::default(), None),
        )
        .map_err(|e| format!("gpu device: {e}"))?;
        let shader = device.create_shader_module(wgpu::ShaderModuleDescriptor {
            label: Some("playlist-music"),
            source: wgpu::ShaderSource::Wgsl(SHADER.into()),
        });
        let layout = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
            label: Some("layout"),
            entries: &[
                wgpu::BindGroupLayoutEntry {
                    binding: 0,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: false },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 1,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Uniform,
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 2,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: true },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 3,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::Filtering),
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 4,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: true },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 5,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: true },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 6,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::Filtering),
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 7,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: true },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 8,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: true },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 9,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::Filtering),
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 10,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: false },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 11,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::NonFiltering),
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 12,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: false },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 13,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: false },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 14,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: false },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                // Bounded MusicFeature::waveform_q16 input. Binding 15 is
                // intentionally appended so existing texture bindings remain
                // wire-compatible with older sidecars.
                wgpu::BindGroupLayoutEntry {
                    binding: 15,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: true },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 16,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: true },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 17,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: true },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
            ],
        });
        let pipeline = device.create_compute_pipeline(&wgpu::ComputePipelineDescriptor {
            label: Some("pipeline"),
            layout: Some(
                &device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
                    label: Some("pipeline-layout"),
                    bind_group_layouts: &[&layout],
                    push_constant_ranges: &[],
                }),
            ),
            module: &shader,
            entry_point: "main",
            compilation_options: Default::default(),
        });
        let blur_layout = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
            label: Some("blur-layout"),
            entries: &[
                wgpu::BindGroupLayoutEntry {
                    binding: 0,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: true },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 1,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::Filtering),
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 2,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: false },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 3,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Uniform,
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 4,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: true },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
            ],
        });
        let blur_shader = device.create_shader_module(wgpu::ShaderModuleDescriptor {
            label: Some("ffmpeg-gblur"),
            source: wgpu::ShaderSource::Wgsl(BLUR_SHADER.into()),
        });
        let blur_pipeline_layout = device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
            label: Some("blur-pipeline-layout"),
            bind_group_layouts: &[&blur_layout],
            push_constant_ranges: &[],
        });
        let blur_horizontal_pipeline =
            device.create_compute_pipeline(&wgpu::ComputePipelineDescriptor {
                label: Some("blur-horizontal"),
                layout: Some(&blur_pipeline_layout),
                module: &blur_shader,
                entry_point: "horizontal",
                compilation_options: Default::default(),
            });
        let blur_vertical_pipeline =
            device.create_compute_pipeline(&wgpu::ComputePipelineDescriptor {
                label: Some("blur-vertical"),
                layout: Some(&blur_pipeline_layout),
                module: &blur_shader,
                entry_point: "vertical",
                compilation_options: Default::default(),
            });
        let yuv_layout = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
            label: Some("yuv-layout"),
            entries: &(0..4)
                .map(|binding| wgpu::BindGroupLayoutEntry {
                    binding,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage {
                            read_only: binding == 0,
                        },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                })
                .chain(std::iter::once(wgpu::BindGroupLayoutEntry {
                    binding: 4,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Uniform,
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                }))
                .chain(std::iter::once(wgpu::BindGroupLayoutEntry {
                    binding: 5,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: true },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                }))
                .chain(std::iter::once(wgpu::BindGroupLayoutEntry {
                    binding: 6,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: true },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                }))
                .chain(std::iter::once(wgpu::BindGroupLayoutEntry {
                    binding: 7,
                    visibility: wgpu::ShaderStages::COMPUTE,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: true },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                }))
                .collect::<Vec<_>>(),
        });
        let yuv_shader = device.create_shader_module(wgpu::ShaderModuleDescriptor {
            label: Some("rgba-to-yuv420"),
            source: wgpu::ShaderSource::Wgsl(YUV_SHADER.into()),
        });
        let yuv_pipeline = device.create_compute_pipeline(&wgpu::ComputePipelineDescriptor {
            label: Some("yuv-pipeline"),
            layout: Some(
                &device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
                    label: Some("yuv-pipeline-layout"),
                    bind_group_layouts: &[&yuv_layout],
                    push_constant_ranges: &[],
                }),
            ),
            module: &yuv_shader,
            entry_point: "main",
            compilation_options: Default::default(),
        });
        Ok(Self {
            device,
            queue,
            layout,
            pipeline,
            adapter_name,
            runtime_fingerprint,
            yuv_layout,
            yuv_pipeline,
            blur_layout,
            blur_horizontal_pipeline,
            blur_vertical_pipeline,
        })
    }

    pub fn adapter_name(&self) -> &str {
        &self.adapter_name
    }

    pub fn fingerprint(&self) -> adapter::RuntimeFingerprint {
        self.runtime_fingerprint.clone()
    }

    pub fn render(
        &self,
        width: u32,
        height: u32,
        sequence: u64,
        pts_ns: i64,
    ) -> Result<GpuFrame, String> {
        self.render_with_scene(width, height, sequence, pts_ns, None)
    }

    /// Explicit GPU-only YUV420P integration seam.
    ///
    /// This stays separate from the legacy RGBA render response so callers
    /// cannot accidentally trigger a CPU colour conversion. It will return a
    /// deterministic error until the Y/U/V compute buffers and readback pass
    /// are connected to this retained renderer.
    pub fn render_yuv420_with_scene(
        &self,
        width: u32,
        height: u32,
        sequence: u64,
        pts_ns: i64,
        scene: Option<&MusicScenePayload>,
    ) -> Result<Yuv420pFrame, String> {
        if width == 0 || height == 0 {
            return Err("invalid render dimensions".into());
        }
        let raw_wave = scene.is_some_and(|s| {
            s.feature.waveform_q16.len() >= 2 && s.feature.waveform_q16.len() % 2 == 0
        });
        let post_text = scene.is_some_and(|s| {
            s.glyph_atlas
                .as_ref()
                .is_some_and(|a| {
                    !a.payload.is_empty()
                        && (!a.glyphs.is_empty() || !a.bitmap_runs.is_empty())
                })
        });
        let mut base_scene = scene.cloned();
        if let Some(s) = base_scene.as_mut() {
            if raw_wave {
                // The CPU reference converts base/dynamics to YUV first and
                // applies showwaves afterward. Keep raw waveform out of the
                // RGBA pass and composite it in the YUV compute pass below.
                s.feature.waveform_q16.clear();
            }
            if post_text {
                // CPU reference order is base/dynamics -> YUV -> waveform ->
                // text. Keep glyphs out of RGBA and add them after waveform in
                // the YUV pass below.
                s.glyph_atlas = None;
            }
        }
        // Keep the compositor output GPU-resident. The legacy implementation
        // mapped the whole RGBA frame to the CPU, then uploaded the same bytes
        // again for this pass. Only the final transport planes are read back.
        let rgba =
            self.render_scene_output(width, height, sequence, pts_ns, base_scene.as_ref())?;
        let cw = (width + 1) / 2;
        let ch = (height + 1) / 2;
        let y_len = width as usize * height as usize;
        let c_len = cw as usize * ch as usize;
        let y = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("y"),
            size: y_len as u64 * 4,
            usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_SRC,
            mapped_at_creation: false,
        });
        let u = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("u"),
            size: c_len as u64 * 4,
            usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_SRC,
            mapped_at_creation: false,
        });
        let v = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("v"),
            size: c_len as u64 * 4,
            usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_SRC,
            mapped_at_creation: false,
        });
        let ys = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("ys"),
            size: y_len as u64 * 4,
            usage: wgpu::BufferUsages::MAP_READ | wgpu::BufferUsages::COPY_DST,
            mapped_at_creation: false,
        });
        let us = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("us"),
            size: c_len as u64 * 4,
            usage: wgpu::BufferUsages::MAP_READ | wgpu::BufferUsages::COPY_DST,
            mapped_at_creation: false,
        });
        let vs = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("vs"),
            size: c_len as u64 * 4,
            usage: wgpu::BufferUsages::MAP_READ | wgpu::BufferUsages::COPY_DST,
            mapped_at_creation: false,
        });
        let mut yuv_params = [0u32; 16];
        yuv_params[0] = width;
        yuv_params[1] = height;
        yuv_params[2] = rgba.stride / 4;
        if raw_wave {
            let s = scene.expect("raw waveform scene");
            let wr = &s.layout.spectrum;
            yuv_params[3] = 1;
            yuv_params[4] = (wr.x as f32 * width as f32 / 1280.0) as u32;
            yuv_params[5] = (wr.y as f32 * height as f32 / 720.0) as u32;
            yuv_params[6] = (wr.w as f32 * width as f32 / 1280.0) as u32;
            yuv_params[7] = (wr.h as f32 * height as f32 / 720.0) as u32;
            yuv_params[8] = (s.feature.waveform_q16.len() / 2) as u32;
            yuv_params[9] = s.palette.accent[0] as u32;
            yuv_params[10] = s.palette.accent[1] as u32;
            yuv_params[11] = s.palette.accent[2] as u32;
        }
        if let Some(s) = scene {
            yuv_params[13] = s.palette.primary[0] as u32;
            yuv_params[14] = s.palette.primary[1] as u32;
            yuv_params[15] = s.palette.primary[2] as u32;
        } else {
            yuv_params[13..16].fill(255);
        }
        let waveform_words = waveform_sample_words(scene);
        let waveform = self
            .device
            .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                label: Some("yuv-waveform-samples"),
                contents: bytemuck::cast_slice(&waveform_words),
                usage: wgpu::BufferUsages::STORAGE,
            });
        let (mut glyph_words, glyph_diagnostics) = glyph_instance_words(scene, width, height);
        yuv_params[12] = if post_text {
            glyph_diagnostics.count as u32
        } else {
            0
        };
        if glyph_words.is_empty() {
            glyph_words.resize(12, 0);
        }
        let glyph_buffer = self
            .device
            .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                label: Some("yuv-glyph-instances"),
                contents: bytemuck::cast_slice(&glyph_words),
                usage: wgpu::BufferUsages::STORAGE,
            });
        let glyph_texture = if post_text {
            self.upload_glyph_atlas(scene.and_then(|s| s.glyph_atlas.as_ref()).unwrap())?
        } else {
            let texture = self.device.create_texture(&wgpu::TextureDescriptor {
                label: Some("yuv-empty-glyph-atlas"),
                size: wgpu::Extent3d {
                    width: 1,
                    height: 1,
                    depth_or_array_layers: 1,
                },
                mip_level_count: 1,
                sample_count: 1,
                dimension: wgpu::TextureDimension::D2,
                format: wgpu::TextureFormat::Rgba8UnormSrgb,
                usage: wgpu::TextureUsages::COPY_DST | wgpu::TextureUsages::TEXTURE_BINDING,
                view_formats: &[],
            });
            self.queue.write_texture(
                wgpu::ImageCopyTexture {
                    texture: &texture,
                    mip_level: 0,
                    origin: wgpu::Origin3d::ZERO,
                    aspect: wgpu::TextureAspect::All,
                },
                &[0, 0, 0, 0],
                wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(4),
                    rows_per_image: Some(1),
                },
                wgpu::Extent3d {
                    width: 1,
                    height: 1,
                    depth_or_array_layers: 1,
                },
            );
            texture
        };
        let glyph_view = glyph_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let p = self
            .device
            .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                label: Some("yuv-params"),
                contents: bytemuck::cast_slice(&yuv_params),
                usage: wgpu::BufferUsages::UNIFORM,
            });
        let bind = self.device.create_bind_group(&wgpu::BindGroupDescriptor {
            label: Some("yuv-bind"),
            layout: &self.yuv_layout,
            entries: &[
                wgpu::BindGroupEntry {
                    binding: 0,
                    resource: rgba.output.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 1,
                    resource: y.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 2,
                    resource: u.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 3,
                    resource: v.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 4,
                    resource: p.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 5,
                    resource: waveform.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 6,
                    resource: wgpu::BindingResource::TextureView(&glyph_view),
                },
                wgpu::BindGroupEntry {
                    binding: 7,
                    resource: glyph_buffer.as_entire_binding(),
                },
            ],
        });
        let mut e = self
            .device
            .create_command_encoder(&wgpu::CommandEncoderDescriptor { label: Some("yuv") });
        {
            let mut pass = e.begin_compute_pass(&wgpu::ComputePassDescriptor {
                label: Some("yuv"),
                timestamp_writes: None,
            });
            pass.set_pipeline(&self.yuv_pipeline);
            pass.set_bind_group(0, &bind, &[]);
            pass.dispatch_workgroups((width + 7) / 8, (height + 7) / 8, 1);
        }
        e.copy_buffer_to_buffer(&y, 0, &ys, 0, y_len as u64 * 4);
        e.copy_buffer_to_buffer(&u, 0, &us, 0, c_len as u64 * 4);
        e.copy_buffer_to_buffer(&v, 0, &vs, 0, c_len as u64 * 4);
        self.queue.submit(Some(e.finish()));
        let read = |b: &wgpu::Buffer, n: usize| -> Result<Vec<u8>, String> {
            let s = b.slice(..);
            let (tx, rx) = channel();
            s.map_async(wgpu::MapMode::Read, move |r| {
                let _ = tx.send(r);
            });
            self.device.poll(wgpu::Maintain::Wait);
            rx.recv()
                .map_err(|_| "map channel".to_string())?
                .map_err(|e| format!("map: {e}"))?;
            let out = {
                let d = s.get_mapped_range();
                let mut out = Vec::with_capacity(n);
                for i in 0..n {
                    out.push(d[i * 4]);
                }
                out
            };
            b.unmap();
            Ok(out)
        };
        Ok(Yuv420pFrame {
            schema: CONTRACT_VERSION,
            sequence,
            pts_ns,
            width,
            height,
            y_stride: width,
            u_stride: cw,
            v_stride: cw,
            color_space: ColorSpace::Srgb,
            ownership: Ownership::OwnedByTransport,
            y: read(&ys, y_len)?,
            u: read(&us, c_len)?,
            v: read(&vs, c_len)?,
        })
    }

    fn render_scene_output(
        &self,
        width: u32,
        height: u32,
        sequence: u64,
        pts_ns: i64,
        scene: Option<&MusicScenePayload>,
    ) -> Result<RenderedScene, String> {
        if width == 0 || height == 0 {
            return Err("invalid render dimensions".into());
        }
        let (
            artwork_texture,
            background_artwork_texture,
            atlas_texture,
            overlay_texture,
            base_texture,
            waveform_texture,
            loudness_texture,
            spectrum_texture,
        ) = if let Some(scene) = scene {
            scene.validate().map_err(|e| format!("scene: {e:?}"))?;
            let artwork = scene
                .artwork
                .as_ref()
                .filter(|a| !a.payload.is_empty())
                .map(|a| self.upload_artwork(a))
                .transpose()?;
            let background_artwork = scene
                .background_artwork
                .as_ref()
                .filter(|a| !a.payload.is_empty())
                .map(|a| self.upload_artwork(a))
                .transpose()?;
            let atlas = scene
                .glyph_atlas
                .as_ref()
                .filter(|a| !a.payload.is_empty())
                .map(|a| self.upload_glyph_atlas(a))
                .transpose()?;
            let overlay = scene
                .text_overlay
                .as_ref()
                .map(|o| self.upload_text_overlay(o))
                .transpose()?;
            let base = scene
                .base_texture
                .as_ref()
                .map(|b| {
                    b.validate().map_err(|e| format!("base texture: {e:?}"))?;
                    self.upload_base_texture(b)
                })
                .transpose()?;
            let waveform = scene
                .waveform_texture
                .as_ref()
                .map(|w| {
                    w.validate()
                        .map_err(|e| format!("waveform texture: {e:?}"))?;
                    self.upload_base_texture(w)
                })
                .transpose()?;
            let loudness = scene
                .loudness_texture
                .as_ref()
                .map(|w| {
                    w.validate()
                        .map_err(|e| format!("loudness texture: {e:?}"))?;
                    self.upload_base_texture(w)
                })
                .transpose()?;
            let spectrum = scene
                .spectrum_texture
                .as_ref()
                .map(|w| {
                    w.validate()
                        .map_err(|e| format!("spectrum texture: {e:?}"))?;
                    self.upload_base_texture(w)
                })
                .transpose()?;
            (
                artwork,
                background_artwork,
                atlas,
                overlay,
                base,
                waveform,
                loudness,
                spectrum,
            )
        } else {
            (None, None, None, None, None, None, None, None)
        };
        let glyph_atlas_receipt = scene.and_then(|s| s.glyph_atlas.as_ref()).map(|atlas| {
            let mut h = Sha256::new();
            h.update(&atlas.payload);
            GlyphAtlasReceipt {
                sha256: format!("{:x}", h.finalize()),
                width: atlas.width,
                height: atlas.height,
                row_stride: atlas.row_stride,
                glyph_count: atlas.glyph_count,
                text_run_count: (atlas.text_runs.len() + atlas.bitmap_runs.len()) as u32,
                format: "Rgba8".into(),
            }
        });
        let text_overlay_receipt = scene.and_then(|s| s.text_overlay.as_ref()).map(|overlay| {
            let mut h = Sha256::new();
            h.update(&overlay.payload);
            TextOverlayReceipt {
                sha256: format!("{:x}", h.finalize()),
                width: overlay.width,
                height: overlay.height,
                row_stride: overlay.row_stride,
                format: format!("{:?}", overlay.format),
                color_space: format!("{:?}", overlay.color_space),
                premultiplied: overlay.premultiplied,
                renderer_id: overlay.renderer_id.clone(),
                renderer_version: overlay.renderer_version.clone(),
            }
        });
        let artwork_receipt = scene.and_then(|s| s.artwork.as_ref()).map(|a| {
            let mut h = Sha256::new();
            h.update(&a.payload);
            ArtworkReceipt {
                sha256: format!("{:x}", h.finalize()),
                source_width: a.width,
                source_height: a.height,
                output_width: width,
                output_height: height,
                crop_mode: "center-crop".into(),
                aspect_mode: "cover".into(),
            }
        });
        let base_texture_receipt = scene.and_then(|s| s.base_texture.as_ref()).map(|b| {
            let mut h = Sha256::new();
            h.update(&b.payload);
            BaseTextureReceipt {
                sha256: format!("{:x}", h.finalize()),
                width: b.width,
                height: b.height,
                row_stride: b.row_stride,
                format: format!("{:?}", b.format),
                color_space: format!("{:?}", b.color_space),
            }
        });
        let needs_gpu_blur = scene.is_some_and(|s| s.base_texture.is_none());
        let procedural_fallback = scene.is_some_and(|s| {
            s.base_texture.is_none()
                && s.artwork
                    .as_ref()
                    .is_none_or(|artwork| artwork.payload.is_empty())
        });
        let fallback = [255u8, 255, 255, 255];
        let fallback_texture = || {
            let texture = self.device.create_texture(&wgpu::TextureDescriptor {
                label: Some("fallback-atlas"),
                size: wgpu::Extent3d {
                    width: 1,
                    height: 1,
                    depth_or_array_layers: 1,
                },
                mip_level_count: 1,
                sample_count: 1,
                dimension: wgpu::TextureDimension::D2,
                format: wgpu::TextureFormat::Rgba8UnormSrgb,
                usage: wgpu::TextureUsages::COPY_DST | wgpu::TextureUsages::TEXTURE_BINDING,
                view_formats: &[],
            });
            self.queue.write_texture(
                wgpu::ImageCopyTexture {
                    texture: &texture,
                    mip_level: 0,
                    origin: wgpu::Origin3d::ZERO,
                    aspect: wgpu::TextureAspect::All,
                },
                &fallback,
                wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(NonZeroU32::new(4).unwrap().into()),
                    rows_per_image: Some(NonZeroU32::new(1).unwrap().into()),
                },
                wgpu::Extent3d {
                    width: 1,
                    height: 1,
                    depth_or_array_layers: 1,
                },
            );
            texture
        };
        let transparent_fallback_texture = || {
            let texture = self.device.create_texture(&wgpu::TextureDescriptor {
                label: Some("transparent-fallback"),
                size: wgpu::Extent3d {
                    width: 1,
                    height: 1,
                    depth_or_array_layers: 1,
                },
                mip_level_count: 1,
                sample_count: 1,
                dimension: wgpu::TextureDimension::D2,
                format: wgpu::TextureFormat::Rgba8UnormSrgb,
                usage: wgpu::TextureUsages::COPY_DST | wgpu::TextureUsages::TEXTURE_BINDING,
                view_formats: &[],
            });
            self.queue.write_texture(
                wgpu::ImageCopyTexture {
                    texture: &texture,
                    mip_level: 0,
                    origin: wgpu::Origin3d::ZERO,
                    aspect: wgpu::TextureAspect::All,
                },
                &[255, 255, 255, 0],
                wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(NonZeroU32::new(4).unwrap().into()),
                    rows_per_image: Some(NonZeroU32::new(1).unwrap().into()),
                },
                wgpu::Extent3d {
                    width: 1,
                    height: 1,
                    depth_or_array_layers: 1,
                },
            );
            texture
        };
        let fallback_note_view = if procedural_fallback {
            background_artwork_texture
                .as_ref()
                .map(|texture| texture.create_view(&wgpu::TextureViewDescriptor::default()))
        } else {
            None
        };
        let artwork_texture = artwork_texture.unwrap_or_else(fallback_texture);
        let background_artwork_texture = background_artwork_texture.unwrap_or_else(|| {
            if procedural_fallback {
                return transparent_fallback_texture();
            }
            // Normal cover art uses the same source for foreground and blur;
            // fallback art can carry the CPU reference's initial white pass
            // separately from its final accent-coloured foreground.
            scene
                .and_then(|s| s.artwork.as_ref())
                .and_then(|a| self.upload_artwork(a).ok())
                .unwrap_or_else(fallback_texture)
        });
        let atlas_texture = atlas_texture.unwrap_or_else(fallback_texture);
        let overlay_texture = overlay_texture.unwrap_or_else(|| fallback_texture());
        let base_texture = base_texture.unwrap_or_else(|| fallback_texture());
        let waveform_texture = waveform_texture.unwrap_or_else(|| fallback_texture());
        let loudness_texture = loudness_texture.unwrap_or_else(|| fallback_texture());
        let spectrum_texture = spectrum_texture.unwrap_or_else(|| fallback_texture());
        let artwork_view = fallback_note_view.unwrap_or_else(|| {
            artwork_texture.create_view(&wgpu::TextureViewDescriptor::default())
        });
        let background_artwork_view =
            background_artwork_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let atlas_view = atlas_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let overlay_view = overlay_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let base_view = base_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let waveform_view = waveform_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let loudness_view = loudness_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let spectrum_view = spectrum_texture.create_view(&wgpu::TextureViewDescriptor::default());
        // Atlas coverage is CPU-rasterized RGBA8.  Linear filtering blends
        // neighbouring fixed cells and creates halos at glyph boundaries;
        // nearest sampling preserves the source coverage contract. Keep a
        // separate linear sampler for artwork so cover/blur interpolation is
        // not changed by the glyph fix.
        let atlas_sampler = self.device.create_sampler(&wgpu::SamplerDescriptor {
            mag_filter: wgpu::FilterMode::Nearest,
            min_filter: wgpu::FilterMode::Nearest,
            mipmap_filter: wgpu::FilterMode::Nearest,
            address_mode_u: wgpu::AddressMode::ClampToEdge,
            address_mode_v: wgpu::AddressMode::ClampToEdge,
            ..Default::default()
        });
        let artwork_sampler = self.device.create_sampler(&wgpu::SamplerDescriptor {
            mag_filter: wgpu::FilterMode::Linear,
            min_filter: wgpu::FilterMode::Linear,
            mipmap_filter: wgpu::FilterMode::Nearest,
            address_mode_u: wgpu::AddressMode::ClampToEdge,
            address_mode_v: wgpu::AddressMode::ClampToEdge,
            ..Default::default()
        });
        let (mut glyph_words, glyph_diagnostics) = glyph_instance_words(scene, width, height);
        if glyph_words.is_empty() {
            glyph_words.resize(12, 0);
        }
        let glyph_buffer = self
            .device
            .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                label: Some("glyph-instances"),
                contents: bytemuck::cast_slice(&glyph_words),
                usage: wgpu::BufferUsages::STORAGE,
            });
        let dynamics_buffer = self
            .device
            .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                label: Some("dynamics-samples"),
                contents: bytemuck::cast_slice(&dynamics_sample_words(scene, width)),
                usage: wgpu::BufferUsages::STORAGE,
            });
        let waveform_buffer = self
            .device
            .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                label: Some("waveform-samples-q16"),
                contents: bytemuck::cast_slice(&waveform_sample_words(scene)),
                usage: wgpu::BufferUsages::STORAGE,
            });
        let fingerprint_buffer =
            self.device
                .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                    label: Some("fingerprint-samples-q16"),
                    contents: bytemuck::cast_slice(&scene_fingerprint_words(scene)),
                    usage: wgpu::BufferUsages::STORAGE,
                });
        let blur_buffer_size = if needs_gpu_blur {
            u64::from(width) * u64::from(height) * 16
        } else {
            16
        };
        let blur_buffer = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("gblur-values"),
            size: blur_buffer_size,
            usage: wgpu::BufferUsages::STORAGE,
            mapped_at_creation: false,
        });
        let blur_params = self
            .device
            .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                label: Some("gblur-params"),
                contents: bytemuck::cast_slice(&gblur_params(
                    width,
                    height,
                    64.0,
                    scene,
                    procedural_fallback,
                )),
                usage: wgpu::BufferUsages::UNIFORM,
            });
        let blur_bind = self.device.create_bind_group(&wgpu::BindGroupDescriptor {
            label: Some("gblur-bind"),
            layout: &self.blur_layout,
            entries: &[
                wgpu::BindGroupEntry {
                    binding: 0,
                    resource: wgpu::BindingResource::TextureView(&background_artwork_view),
                },
                wgpu::BindGroupEntry {
                    binding: 1,
                    resource: wgpu::BindingResource::Sampler(&artwork_sampler),
                },
                wgpu::BindGroupEntry {
                    binding: 2,
                    resource: blur_buffer.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 3,
                    resource: blur_params.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 4,
                    resource: fingerprint_buffer.as_entire_binding(),
                },
            ],
        });
        let stride = ((width * 4 + ROW_ALIGNMENT - 1) / ROW_ALIGNMENT) * ROW_ALIGNMENT;
        let bytes = stride as usize * height as usize;
        let output = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("gpu-frame"),
            size: bytes as u64,
            usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_SRC,
            mapped_at_creation: false,
        });
        let scene_params = scene_uniform_words(width, height, stride, sequence, scene);
        let params = self
            .device
            .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                label: Some("params"),
                contents: bytemuck::cast_slice(&scene_params),
                usage: wgpu::BufferUsages::UNIFORM,
            });
        let bind = self.device.create_bind_group(&wgpu::BindGroupDescriptor {
            label: Some("bind"),
            layout: &self.layout,
            entries: &[
                wgpu::BindGroupEntry {
                    binding: 0,
                    resource: output.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 1,
                    resource: params.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 2,
                    resource: wgpu::BindingResource::TextureView(&atlas_view),
                },
                wgpu::BindGroupEntry {
                    binding: 3,
                    resource: wgpu::BindingResource::Sampler(&atlas_sampler),
                },
                wgpu::BindGroupEntry {
                    binding: 4,
                    resource: glyph_buffer.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 5,
                    resource: wgpu::BindingResource::TextureView(&artwork_view),
                },
                wgpu::BindGroupEntry {
                    binding: 6,
                    resource: wgpu::BindingResource::Sampler(&artwork_sampler),
                },
                wgpu::BindGroupEntry {
                    binding: 7,
                    resource: dynamics_buffer.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 8,
                    resource: wgpu::BindingResource::TextureView(&overlay_view),
                },
                wgpu::BindGroupEntry {
                    binding: 9,
                    resource: wgpu::BindingResource::Sampler(&atlas_sampler),
                },
                wgpu::BindGroupEntry {
                    binding: 10,
                    resource: wgpu::BindingResource::TextureView(&base_view),
                },
                wgpu::BindGroupEntry {
                    binding: 11,
                    resource: wgpu::BindingResource::Sampler(&atlas_sampler),
                },
                wgpu::BindGroupEntry {
                    binding: 12,
                    resource: wgpu::BindingResource::TextureView(&waveform_view),
                },
                wgpu::BindGroupEntry {
                    binding: 13,
                    resource: wgpu::BindingResource::TextureView(&loudness_view),
                },
                wgpu::BindGroupEntry {
                    binding: 14,
                    resource: wgpu::BindingResource::TextureView(&spectrum_view),
                },
                wgpu::BindGroupEntry {
                    binding: 15,
                    resource: waveform_buffer.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 16,
                    resource: fingerprint_buffer.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 17,
                    resource: blur_buffer.as_entire_binding(),
                },
            ],
        });
        let mut enc = self
            .device
            .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                label: Some("render"),
            });
        if needs_gpu_blur {
            {
                let mut pass = enc.begin_compute_pass(&wgpu::ComputePassDescriptor {
                    label: Some("gblur-horizontal"),
                    timestamp_writes: None,
                });
                pass.set_pipeline(&self.blur_horizontal_pipeline);
                pass.set_bind_group(0, &blur_bind, &[]);
                pass.dispatch_workgroups((height + 63) / 64, 1, 1);
            }
            {
                let mut pass = enc.begin_compute_pass(&wgpu::ComputePassDescriptor {
                    label: Some("gblur-vertical"),
                    timestamp_writes: None,
                });
                pass.set_pipeline(&self.blur_vertical_pipeline);
                pass.set_bind_group(0, &blur_bind, &[]);
                pass.dispatch_workgroups((width + 63) / 64, 1, 1);
            }
        }
        {
            let mut pass = enc.begin_compute_pass(&wgpu::ComputePassDescriptor {
                label: Some("music"),
                timestamp_writes: None,
            });
            pass.set_pipeline(&self.pipeline);
            pass.set_bind_group(0, &bind, &[]);
            pass.dispatch_workgroups((width + 7) / 8, (height + 7) / 8, 1);
        }
        self.queue.submit(Some(enc.finish()));
        Ok(RenderedScene {
            output,
            stride,
            bytes,
            glyph_atlas_receipt,
            text_overlay_receipt,
            artwork_receipt,
            base_texture_receipt,
            glyph_diagnostics,
        })
    }

    pub fn render_with_scene(
        &self,
        width: u32,
        height: u32,
        sequence: u64,
        pts_ns: i64,
        scene: Option<&MusicScenePayload>,
    ) -> Result<GpuFrame, String> {
        let rendered = self.render_scene_output(width, height, sequence, pts_ns, scene)?;
        let staging = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("gpu-readback"),
            size: rendered.bytes as u64,
            usage: wgpu::BufferUsages::MAP_READ | wgpu::BufferUsages::COPY_DST,
            mapped_at_creation: false,
        });
        let mut encoder = self
            .device
            .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                label: Some("render-readback"),
            });
        encoder.copy_buffer_to_buffer(&rendered.output, 0, &staging, 0, rendered.bytes as u64);
        self.queue.submit(Some(encoder.finish()));
        let slice = staging.slice(..);
        let (tx, rx) = channel();
        slice.map_async(wgpu::MapMode::Read, move |result| {
            let _ = tx.send(result);
        });
        self.device.poll(wgpu::Maintain::Wait);
        rx.recv()
            .map_err(|_| "readback channel closed".to_string())?
            .map_err(|e| format!("readback: {e}"))?;
        let payload = slice.get_mapped_range().to_vec();
        staging.unmap();
        Ok(GpuFrame {
            schema: CONTRACT_VERSION,
            sequence,
            pts_ns,
            width,
            height,
            row_stride: rendered.stride,
            format: PixelFormat::Rgba8,
            color_space: ColorSpace::Srgb,
            alpha: true,
            ownership: Ownership::OwnedByTransport,
            payload,
            glyph_atlas_receipt: rendered.glyph_atlas_receipt,
            text_overlay_receipt: rendered.text_overlay_receipt,
            artwork_receipt: rendered.artwork_receipt,
            base_texture_receipt: rendered.base_texture_receipt,
            glyph_diagnostics: Some(rendered.glyph_diagnostics),
        })
    }
}

/// Compatibility helper for callers that need a one-shot render.
pub fn render(width: u32, height: u32, sequence: u64, pts_ns: i64) -> Result<GpuFrame, String> {
    Renderer::new()?.render(width, height, sequence, pts_ns)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contracts::{AudioFeatureFrame, MUSIC_SCENE_SCHEMA};

    #[test]
    fn yuv_shader_matches_cpu_bt709_limited_matrix_and_rounding() {
        for coefficient in [
            "0.18258588",
            "0.61423059",
            "0.06200706",
            "0.10064373",
            "0.33857195",
            "0.43921569",
            "0.39894216",
            "0.04027352",
        ] {
            assert!(YUV_SHADER.contains(coefficient), "missing {coefficient}");
        }
        assert!(
            YUV_SHADER.contains("+0.5"),
            "shader must round like clampByte"
        );
    }

    #[test]
    fn gblur_params_match_ffmpeg_single_step_equations() {
        let words = gblur_params(640, 360, 64.0, None, false);
        let nu = f32::from_bits(words[2]);
        let boundary = f32::from_bits(words[3]);
        let postscale = f32::from_bits(words[4]);
        assert_eq!(&words[..2], &[640, 360]);
        assert!((nu - 0.978144).abs() < 0.00001, "nu={nu}");
        assert!((boundary - 45.754).abs() < 0.02, "boundary={boundary}");
        assert!(
            (postscale - 0.0004776).abs() < 0.000001,
            "postscale={postscale}"
        );
        assert!(BLUR_SHADER.contains("values[row + x] + p.nu"));
        assert!(BLUR_SHADER.contains("p.postscale * p.postscale"));
    }

    #[test]
    fn legacy_uniforms_are_stable_and_scene_is_explicitly_enabled() {
        let legacy = scene_uniform_words(128, 72, 512, 9, None);
        assert_eq!(&legacy[..7], &[128, 72, 128, 9, 0, 0, 0]);
        let scene = MusicScenePayload {
            schema: MUSIC_SCENE_SCHEMA,
            feature: AudioFeatureFrame {
                schema: CONTRACT_VERSION,
                sample_rate_hz: 48_000,
                frame_index: 3,
                pts_ns: 100,
                spectrum_q16: (0..24).map(|n| n * 1000).collect(),
                fingerprint_q16: vec![],
                waveform_q16: vec![],
                rms_q15: 123,
                peak_q15: 456,
            },
            artwork: None,
            background_artwork: None,
            base_texture: None,
            waveform_texture: None,
            loudness_texture: None,
            spectrum_texture: None,
            glyph_atlas: None,
            text_overlay: None,
            layout: Default::default(),
            dynamics: Default::default(),
            palette: Default::default(),
            fingerprint: String::new(),
        };
        let words = scene_uniform_words(128, 72, 512, 9, Some(&scene));
        assert_eq!(&words[..7], &[128, 72, 128, 9, 123, 456, 4097]);
        assert_eq!(words[8], 0);
        assert_eq!(words[31], 23_000);
    }

    #[test]
    fn glyph_instances_use_each_entry_rect_and_run_position() {
        use crate::contracts::{GlyphAtlasMetadata, GlyphEntry, TextRun};
        let scene = MusicScenePayload {
            schema: MUSIC_SCENE_SCHEMA,
            feature: AudioFeatureFrame {
                schema: CONTRACT_VERSION,
                sample_rate_hz: 48_000,
                frame_index: 0,
                pts_ns: 0,
                spectrum_q16: vec![],
                fingerprint_q16: vec![],
                waveform_q16: vec![],
                rms_q15: 0,
                peak_q15: 0,
            },
            artwork: None,
            background_artwork: None,
            base_texture: None,
            waveform_texture: None,
            loudness_texture: None,
            spectrum_texture: None,
            glyph_atlas: Some(GlyphAtlasMetadata {
                texture_id: "atlas".into(),
                font_family: "sans".into(),
                font_weight: 400,
                fallback_order: vec![],
                width: 256,
                height: 256,
                row_stride: 1024,
                glyph_count: 2,
                missing_glyph_id: "?".into(),
                payload: vec![1; 1024 * 256],
                glyphs: vec![
                    GlyphEntry {
                        id: "600:A".into(),
                        x: 8,
                        y: 16,
                        width: 64,
                        height: 40,
                        advance: 34.0,
                    },
                    GlyphEntry {
                        id: "?".into(),
                        x: 0,
                        y: 0,
                        width: 20,
                        height: 20,
                        advance: 22.0,
                    },
                ],
                text_runs: vec![TextRun {
                    text: "AA?".into(),
                    x: 10.0,
                    y: 12.0,
                    size_px: 40.0,
                    rgba: [255, 0, 0, 255],
                    opacity: 1.0,
                    font_family: String::new(),
                    font_weight: 600,
                }],
                bitmap_runs: Vec::new(),
                asset_hash: String::new(),
            }),
            text_overlay: None,
            layout: Default::default(),
            dynamics: Default::default(),
            palette: Default::default(),
            fingerprint: String::new(),
        };
        let (words, diagnostics) = glyph_instance_words(Some(&scene), 100, 100);
        assert_eq!(words.len(), 36);
        assert_eq!(diagnostics.count, 3);
        assert_eq!(diagnostics.instances[0].id, "600:A");
        assert!(diagnostics.instances[0].scale > 0.0);
        let x0 = f32::from_bits(words[0]);
        let atlas_x0 = f32::from_bits(words[4]);
        let x1 = f32::from_bits(words[12]);
        assert!((x0 - 10.0 * 100.0 / 1280.0).abs() < 1e-6);
        assert!((atlas_x0 - 8.0).abs() < 1e-6);
        let expected_x1 = (10.0 + 34.0 * (40.0 / 72.0)) * 100.0 / 1280.0;
        assert!(
            (x1 - expected_x1).abs() < 1e-6,
            "glyph cursor must use font advance, got {x1}, want {expected_x1}"
        );
        let uniform = scene_uniform_words(100, 100, 400, 1, Some(&scene));
        assert_eq!(
            uniform[7], 3,
            "shader glyph count must count instances, not unique atlas cells"
        );
    }

    #[test]
    fn shader_uniform_array_uses_wgsl_16_byte_stride() {
        // Keep this contract close to the host packing: six vec4s are exactly
        // 24 u32 samples, and avoid regressing to an illegally aligned
        // array<u32, 24> uniform declaration (which makes wgpu exit at
        // shader-module validation on some drivers).
        assert!(SHADER.contains("spectrum: array<vec4<u32>, 6>"));
        assert!(SHADER.contains("params.spectrum[band / 4u][band % 4u]"));
        assert!(!SHADER.contains("spectrum: array<u32, 24>"));
    }

    #[test]
    fn artwork_sampling_preserves_stored_srgb_bytes_for_cpu_parity() {
        assert_eq!(artwork_texture_format(), wgpu::TextureFormat::Rgba8Unorm);
    }

    #[test]
    fn shader_declares_artwork_layer_and_separate_texture_binding() {
        assert!(SHADER.contains("@group(0) @binding(5) var artwork_tex"));
        assert!(SHADER.contains("textureSampleLevel(artwork_tex"));
        assert!(SHADER.contains("params.scene_enabled & 2u"));
        assert!(SHADER.contains("let artwork_rect = params.rects[0]"));
    }

    #[test]
    fn shader_declares_screen_text_source_over_gate() {
        assert!(SHADER.contains("params.scene_enabled & 16u"));
        assert!(SHADER.contains("textureLoad(overlay_tex"));
        assert!(SHADER.contains("mixc * (1.0 - tc.a) + tc.rgb"));
    }

    #[test]
    fn shader_declares_spectrum_only_probe() {
        assert!(SHADER.contains("params.sequence == 0xfffffffbu"));
        assert!(SHADER.contains("clamp(bars * 255.0"));
    }

    #[test]
    fn shader_declares_native_static_base_probe() {
        assert!(SHADER.contains("params.sequence == 0xffffffdfu"));
    }

    #[test]
    fn raw_waveform_shader_uses_showwaves_history_and_integer_accumulation() {
        assert!(SHADER.contains("let samples_per_column = (6400u + u32(wr.z) - 1u) / u32(wr.z)"));
        assert!(SHADER.contains("let contribution_scale = (255u * u32(wr.z)) / 12800u"));
        assert!(SHADER.contains("let raw_stereo = (params.scene_enabled & 2048u) != 0u"));
        assert!(SHADER.contains(
            "let accumulated_r = (accent_bytes.x * contribution_scale / 255u * coverage) & 255u"
        ));
    }

    #[test]
    fn production_progress_matches_cpu_direct_rgba_write() {
        // drawFilledRoundedRect writes the track RGBA directly into the CPU
        // canvas. The later RGB video conversion therefore observes the
        // accent RGB at full strength even though the stored alpha is 0.35.
        // Production GPU output is opaque, so it must reproduce that observed
        // RGB rather than alpha-blending the track with the background.
        assert!(SHADER.contains("mix(mixc, accent, rail_track)"));
        assert!(SHADER.contains("mix(mixc, accent, thumb)"));
        assert!(!SHADER.contains("rail_track * (89.0 / 255.0)"));
        assert!(!SHADER.contains("thumb * (224.0 / 255.0)"));
    }

    #[test]
    fn shader_reproduces_supersampled_lanczos_loudness() {
        assert!(SHADER.contains("fn loudness_source_pixel"));
        assert!(SHADER.contains("fn loudness_lanczos"));
        assert!(SHADER.contains("let sw = lw * 4"));
        assert!(SHADER.contains("loudness_premultiplied + mixc *"));
    }

    #[test]
    fn monotone_hermite_upload_preserves_constant_curve() {
        let words = monotone_hermite_words(&[0.25; 1000], 4000);
        assert_eq!(words.len(), 4000);
        assert!(words.iter().all(|word| f32::from_bits(*word) == 0.25));
    }

    #[test]
    fn minmax_waveform_reproduces_showwaves_line_from_center() {
        assert!(SHADER.contains("let wave_center = f32(wr.y) + f32(wr.w) * 0.5"));
        assert!(SHADER.contains("min(wave_center, min(y0, y1))"));
        assert!(SHADER.contains("max(wave_center, max(y0, y1))"));
    }

    #[test]
    fn raw_stereo_waveform_scales_channel_coverage_like_showwaves() {
        assert!(SHADER.contains("params.scene_enabled & 2048u"));
        assert!(SHADER.contains("waveform_samples[sample_i * 2u + channel]"));
        assert!(SHADER.contains("contribution_scale / 255u * coverage"));
        assert!(SHADER.contains("mixc = mix(mixc, wave_rgb, wave_alpha)"));
    }

    #[test]
    fn shader_declares_flat_background_diagnostic_branch() {
        assert!(SHADER.contains("params.sequence == 0xfffffffcu"));
        assert!(SHADER.contains("let c = params.palette[2]"));
    }

    #[test]
    #[ignore = "requires a host GPU adapter; run explicitly for device smoke testing"]
    fn shader_module_creation_smoke() {
        // Renderer::new creates the shader module and compute pipeline. This
        // test is intentionally opt-in because CI runners may not expose an
        // adapter; fresh release sidecars should run it as part of the hello
        // protocol smoke test.
        Renderer::new().expect("WGSL shader module/pipeline creation");
    }
}
