use crate::adapter;
use crate::contracts::{
    BaseTextureReceipt, ColorSpace, GlyphAtlasReceipt, GlyphInstanceDiagnostic, GlyphRenderDiagnostics, GpuFrame, MusicScenePayload, Ownership, PixelFormat, TextOverlayReceipt, ArtworkReceipt,
    CONTRACT_VERSION, ROW_ALIGNMENT,
};
use sha2::{Digest, Sha256};
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
  var r: u32;
  var g: u32;
  var b: u32;
  if (params.scene_enabled == 0u) {
    r = (id.x + params.sequence) & 255u;
    g = (id.y + params.sequence * 3u) & 255u;
    b = ((id.x + id.y) / 2u + params.sequence * 5u) & 255u;
  } else {
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
        let edge = min(min(uv.x, 1.0 - uv.x), min(uv.y, 1.0 - uv.y));
        let alpha = smoothstep(0.0, 0.025, edge);
        artwork = textureSampleLevel(artwork_tex, artwork_sampler, uv, 0.0).a * alpha;
        // Feed a restrained artwork luminance into the background layer; this
        // provides a stable palette/blur approximation without extra passes.
        let cover_sample = textureSampleLevel(artwork_tex, artwork_sampler, uv, 0.0);
        let cover = cover_sample.rgb;
        artwork_color = cover;
        artwork_alpha = cover_sample.a * alpha;
        let luminance = dot(cover, vec3<f32>(0.2126, 0.7152, 0.0722));
        glow = glow + luminance * 0.08 * alpha;
      }
    }
    let spectrum_rect = params.rects[4];
    let sx = f32(id.x);
    let sy = f32(id.y);
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
    let bar_w = max(1.0, round(18.0 * scale));
    let bar_gap = max(1.0, round(13.0 * scale));
    let first_bar_x = f32(spectrum_rect.x) + round(11.0 * scale);
    let bar_bottom = f32(spectrum_rect.y + spectrum_rect.w);
    let max_bar_h = f32(spectrum_rect.w) - round(16.0 * scale);
    let min_bar_h = max(1.0, round(4.0 * scale));
    let bar_h = min_bar_h + level * max(0.0, max_bar_h - min_bar_h);
    let bar_x = first_bar_x + f32(band_index) * (bar_w + bar_gap);
    let bar_y = bar_bottom - bar_h;
    let fade_px = max(1.0, round(10.0 * scale));
    let bottom_dist = bar_bottom - 1.0 - sy;
    let fade = select(0.0, min(1.0, bottom_dist / max(1.0, fade_px - 1.0)), bottom_dist < fade_px);
    let bars = select(0.0, 0.82 * (select(1.0, fade, bottom_dist < fade_px)),
      in_spectrum && sx >= bar_x && sx < bar_x + bar_w && sy >= bar_y && sy < bar_bottom);
    if (params.sequence == 0xfffffffbu) {
      let v = u32(clamp(bars * 255.0, 0.0, 255.0));
      pixels[i] = v | (v << 8u) | (v << 16u) | (255u << 24u);
      return;
    }
    // The CPU scene also carries a fine waveform over the bars.  The canonical
    // payload has bounded spectrum samples rather than a second texture, so
    // use the interpolated band energy as a deterministic proxy centered in
    // the spectrum rect.  This preserves the line silhouette and avoids a
    // second per-frame readback path.
    let wave_y = f32(spectrum_rect.y) + f32(spectrum_rect.w) * (0.70 - level * 0.42);
    let wave = select(0.0, 1.0,
      in_spectrum && abs(sy - wave_y) < 1.0);
    // Progress rail and thumb use the canonical progress rectangle.
    let progress = f32(params.dynamics[0].z) / 65535.0;
    let rail = params.rects[6];
    let rx = f32(id.x); let ry = f32(id.y);
    let in_rail = rx >= f32(rail.x) && rx < f32(rail.x + rail.z) && ry >= f32(rail.y) && ry < f32(rail.y + rail.w);
    let rail_track = select(0.0, 1.0, in_rail);
    let thumb_x = f32(rail.x) + f32(rail.z) * progress;
    let thumb_radius = max(1.0, round(9.0 * (f32(params.width) / 1280.0)));
    let thumb = select(0.0, 1.0, distance(vec2<f32>(rx, ry), vec2<f32>(thumb_x, f32(rail.y) + f32(rail.w) * 0.5)) < thumb_radius);
    if (params.sequence == 0xfffffff9u) {
      let v = u32(clamp(max(rail_track * 0.35, thumb), 0.0, 1.0) * 255.0);
      pixels[i] = v | (v << 8u) | (v << 16u) | (255u << 24u);
      return;
    }
    // Loudness envelope/trend are bounded Q0.16 samples. Render them in the
    // loudness rectangle as two thin deterministic traces.
      let loud = params.rects[5];
    var loudness = 0.0;
      if (rx >= f32(loud.x) && rx < f32(loud.x + loud.z) && ry >= f32(loud.y) && ry < f32(loud.y + loud.w)) {
      let u = clamp((rx - f32(loud.x)) / max(1.0, f32(loud.z - 1)), 0.0, 1.0);
      let sample_index = min(255u, u32(u * 255.0 + 0.5));
      let envelope = f32(dynamics_samples[sample_index]) / 65535.0;
      let trend = f32(dynamics_samples[256u + sample_index]) / 65535.0;
      let y_env = f32(loud.y + loud.w) - envelope * f32(loud.w);
      let y_trend = f32(loud.y + loud.w) - trend * f32(loud.w);
      loudness = select(0.0, 1.0, abs(ry - y_env) < 1.5) + select(0.0, 0.65, abs(ry - y_trend) < 1.5);
      // CPU loudness draws four fixed guide lines in the same rect. Their
      // quantized positions are carried in the uniform contract so the GPU
      // does not have to infer them from the envelope.
      for (var guide_index: u32 = 0u; guide_index < 4u; guide_index = guide_index + 1u) {
        let guide = f32(params.guides[guide_index]) / 65535.0;
        let guide_y = f32(loud.y + loud.w) - guide * f32(loud.w);
        loudness = loudness + select(0.0, 0.55, abs(ry - guide_y) < 0.75);
      }
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
    if ((params.scene_enabled & 4u) != 0u && (params.scene_enabled & 16u) == 0u) {
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
          // Sample at the covered pixel center to avoid a half-pixel nearest
          // filtering shift at small glyph sizes.
          let uv = g.atlas.xy + ((pixel + vec2<f32>(0.5, 0.5)) - screen.xy) / max(screen.zw, vec2<f32>(1.0)) * g.atlas.zw;
          glyph = max(glyph, textureSampleLevel(atlas_tex, atlas_sampler, uv, 0.0).a * g.color.a);
        }
      }
      // Production compositor applies a readability gain.  The sentinel is
      // an atlas parity probe, so preserve raw sampled alpha there; otherwise
      // the gain changes threshold membership rather than raster geometry.
      if (params.sequence != 0xffffffffu) {
        glyph = glyph * 0.85;
      }
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
    // The CPU compositor starts from the blurred artwork background and then
    // applies its readability overlay before foreground layers.  Reconstruct
    // the same ordering here with a bounded three-tap blur.  The payload's
    // background colour remains the deterministic fallback when artwork is
    // absent or malformed.
    var blurred_bg = background;
    if ((params.scene_enabled & 2u) != 0u && !has_base) {
      let dims = vec2<f32>(textureDimensions(artwork_tex));
      let source_aspect = dims.x / max(1.0, dims.y);
      let frame_aspect = f32(params.width) / max(1.0, f32(params.height));
      var buv = vec2<f32>(fx, fy);
      if (source_aspect > frame_aspect) {
        buv.x = (fx - 0.5) * frame_aspect / source_aspect + 0.5;
      } else {
        buv.y = (fy - 0.5) * source_aspect / frame_aspect + 0.5;
      }
      let texel = 1.0 / max(dims, vec2<f32>(1.0));
      let c0 = textureSampleLevel(artwork_tex, artwork_sampler, clamp(buv, vec2<f32>(0.0), vec2<f32>(1.0)), 0.0).rgb;
      let c1 = textureSampleLevel(artwork_tex, artwork_sampler, clamp(buv + vec2<f32>(texel.x, 0.0), vec2<f32>(0.0), vec2<f32>(1.0)), 0.0).rgb;
      let c2 = textureSampleLevel(artwork_tex, artwork_sampler, clamp(buv + vec2<f32>(0.0, texel.y), vec2<f32>(0.0), vec2<f32>(1.0)), 0.0).rgb;
      blurred_bg = mix(background, (c0 + c1 + c2) / 3.0, 0.55);
    }
    if (has_base) {
      // The CPU base was rasterized at the output dimensions. Use an exact
      // texel load here; linear sampling would blur the already-composited
      // artwork edges a second time.
      blurred_bg = textureLoad(base_tex, vec2<i32>(id.xy), 0).rgb;
      artwork = 0.0;
      artwork_alpha = 0.0;
    }
    var mixc = blurred_bg + primary * (glow + glyph + artwork * 0.35) + accent * (bars * 0.75 + wave * 0.35 + rail_track * 0.35 + thumb + loudness);
    let overlay_alpha = overlay.a;
    if (!has_base) { mixc = mix(mixc, overlay.rgb, overlay_alpha); }
    // Composite the actual artwork payload into the tile. Previously only its
    // alpha/luminance affected the background, leaving the GPU tile unlike the
    // CPU reference even when the same artwork bytes were present.
    mixc = mix(mixc, artwork_color, artwork_alpha * 0.9);
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
    r = u32(clamp(mixc.r * 255.0, 0.0, 255.0));
    g = u32(clamp(mixc.g * 255.0, 0.0, 255.0));
    b = u32(clamp(mixc.b * 255.0, 0.0, 255.0));
  }
  pixels[i] = r | (g << 8u) | (b << 16u) | (255u << 24u);
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
}

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
            } | if scene.base_texture.as_ref().is_some_and(|b| !b.payload.is_empty()) { 8 } else { 0 }
            | if scene.text_overlay.as_ref().is_some_and(|o| o.kind == "screen_rgba" && !o.payload.is_empty()) { 16 } else { 0 };
        words[7] = scene
            .glyph_atlas
            .as_ref()
            .map(|a| a.glyphs.len().min(256) as u32)
            .unwrap_or(0);
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
    }
    words
}

fn glyph_instance_words(scene: Option<&MusicScenePayload>, width: u32, height: u32) -> (Vec<u32>, GlyphRenderDiagnostics) {
    let mut out = Vec::new();
    let mut diagnostics = Vec::new();
    let Some(atlas) = scene.and_then(|s| s.glyph_atlas.as_ref()) else {
        return (out, GlyphRenderDiagnostics { count: 0, instances: diagnostics });
    };
    if atlas.payload.is_empty() || atlas.glyphs.is_empty() {
        return (out, GlyphRenderDiagnostics { count: 0, instances: diagnostics });
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
            let id = ch.to_string();
            let glyph = atlas
                .glyphs
                .iter()
                .find(|g| g.id == id)
                .or_else(|| atlas.glyphs.iter().find(|g| g.id == atlas.missing_glyph_id));
            let Some(g) = glyph else { continue };
            // The Go atlas is rasterized once at a 48px face size into 64px
            // cells (baseline 52px).  Scaling by the cell height (the old
            // implementation) shrank the ink and made its baseline differ
            // from the CPU renderer.  Scale from the actual face size and
            // keep the complete cell so the atlas coverage is sampled at the
            // same coordinates as the source raster.
            const ATLAS_FACE_SIZE: f32 = 48.0;
            const ATLAS_INK_TOP: f32 = 7.0;
            let scale = run.size_px / ATLAS_FACE_SIZE;
            let sw = (g.width as f32 * scale).max(1.0);
            let sh = (g.height as f32 * scale * sy).max(1.0);
            let screen_y = (run.y - ATLAS_INK_TOP * scale).max(0.0) * sy;
            let vals = [
                cursor / width as f32,
                screen_y / height as f32,
                (sw * sx) / width as f32,
                sh / height as f32,
                g.x as f32 / atlas.width as f32,
                g.y as f32 / atlas.height as f32,
                g.width as f32 / atlas.width as f32,
                g.height as f32 / atlas.height as f32,
                run.rgba[0] as f32 / 255.0,
                run.rgba[1] as f32 / 255.0,
                run.rgba[2] as f32 / 255.0,
                (run.rgba[3] as f32 / 255.0) * run.opacity.clamp(0.0, 1.0),
            ];
            out.extend(vals.into_iter().map(f32::to_bits));
            diagnostics.push(GlyphInstanceDiagnostic {
                id,
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
            cursor += g.advance.max(g.width as f32) * scale * sx;
        }
    }
    let count = diagnostics.len();
    (out, GlyphRenderDiagnostics { count, instances: diagnostics })
}

fn dynamics_sample_words(scene: Option<&MusicScenePayload>) -> Vec<u32> {
    // Keep the storage contract bounded while retaining enough samples to
    // preserve the CPU renderer's fine detail at 720p and below.  The source
    // analysis is capped at 1000 samples; resample it deterministically into
    // 256 points per curve for the GPU.
    let mut out = vec![0u32; 512];
    if let Some(scene) = scene {
        let env = &scene.dynamics.loudness_envelope;
        let trend = &scene.dynamics.loudness_trend;
        for i in 0..256 {
            let sample = |values: &Vec<u16>| -> u32 {
                if values.is_empty() {
                    return scene.feature.rms_q15 as u32;
                }
                let idx = i * values.len().saturating_sub(1) / 255;
                values[idx] as u32
            };
            out[i] = sample(env);
            out[256 + i] = sample(trend);
        }
    }
    out
}

impl Renderer {
    fn upload_text_overlay(&self, o: &crate::contracts::TextOverlayMetadata) -> Result<wgpu::Texture, String> {
        if o.payload.is_empty() || o.width == 0 || o.height == 0 { return Err("empty text overlay payload".into()); }
        // Keep canonical premultiplied bytes numerically identical to the Go
        // parity reference; sRGB decode would alter low-alpha edge luminance.
        let texture = self.device.create_texture(&wgpu::TextureDescriptor { label: Some("text-overlay"), size: wgpu::Extent3d { width:o.width,height:o.height,depth_or_array_layers:1 }, mip_level_count:1,sample_count:1,dimension:wgpu::TextureDimension::D2,format:wgpu::TextureFormat::Rgba8Unorm,usage:wgpu::TextureUsages::COPY_DST|wgpu::TextureUsages::TEXTURE_BINDING,view_formats:&[] });
        self.queue.write_texture(wgpu::ImageCopyTexture { texture:&texture,mip_level:0,origin:wgpu::Origin3d::ZERO,aspect:wgpu::TextureAspect::All }, &o.payload, wgpu::ImageDataLayout { offset:0,bytes_per_row:Some(NonZeroU32::new(o.row_stride).unwrap().into()),rows_per_image:Some(NonZeroU32::new(o.height).unwrap().into()) }, wgpu::Extent3d { width:o.width,height:o.height,depth_or_array_layers:1 });
        Ok(texture)
    }
    fn upload_artwork(
        &self,
        artwork: &crate::contracts::ArtworkMetadata,
        diagnostic_raw: bool,
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
            format: if diagnostic_raw { wgpu::TextureFormat::Rgba8Unorm } else { wgpu::TextureFormat::Rgba8UnormSrgb },
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

    fn upload_base_texture(&self, base: &crate::contracts::BaseTextureMetadata) -> Result<wgpu::Texture, String> {
        if base.payload.is_empty() || base.width == 0 || base.height == 0 { return Err("empty base texture payload".into()); }
        let texture = self.device.create_texture(&wgpu::TextureDescriptor { label: Some("music-base-texture"), size: wgpu::Extent3d { width: base.width, height: base.height, depth_or_array_layers: 1 }, mip_level_count: 1, sample_count: 1, dimension: wgpu::TextureDimension::D2, format: wgpu::TextureFormat::Rgba8Unorm, usage: wgpu::TextureUsages::COPY_DST|wgpu::TextureUsages::TEXTURE_BINDING, view_formats: &[] });
        self.queue.write_texture(wgpu::ImageCopyTexture { texture: &texture, mip_level: 0, origin: wgpu::Origin3d::ZERO, aspect: wgpu::TextureAspect::All }, &base.payload, wgpu::ImageDataLayout { offset: 0, bytes_per_row: Some(NonZeroU32::new(base.row_stride).unwrap().into()), rows_per_image: Some(NonZeroU32::new(base.height).unwrap().into()) }, wgpu::Extent3d { width: base.width, height: base.height, depth_or_array_layers: 1 });
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
                wgpu::BindGroupLayoutEntry { binding: 8, visibility: wgpu::ShaderStages::COMPUTE, ty: wgpu::BindingType::Texture { sample_type: wgpu::TextureSampleType::Float { filterable: true }, view_dimension: wgpu::TextureViewDimension::D2, multisampled: false }, count: None },
                wgpu::BindGroupLayoutEntry { binding: 9, visibility: wgpu::ShaderStages::COMPUTE, ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::Filtering), count: None },
                wgpu::BindGroupLayoutEntry { binding: 10, visibility: wgpu::ShaderStages::COMPUTE, ty: wgpu::BindingType::Texture { sample_type: wgpu::TextureSampleType::Float { filterable: false }, view_dimension: wgpu::TextureViewDimension::D2, multisampled: false }, count: None },
                wgpu::BindGroupLayoutEntry { binding: 11, visibility: wgpu::ShaderStages::COMPUTE, ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::NonFiltering), count: None },
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
        Ok(Self {
            device,
            queue,
            layout,
            pipeline,
            adapter_name,
            runtime_fingerprint,
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

    pub fn render_with_scene(
        &self,
        width: u32,
        height: u32,
        sequence: u64,
        pts_ns: i64,
        scene: Option<&MusicScenePayload>,
    ) -> Result<GpuFrame, String> {
        if width == 0 || height == 0 {
            return Err("invalid render dimensions".into());
        }
        let (artwork_texture, atlas_texture, overlay_texture, base_texture) = if let Some(scene) = scene {
            scene.validate().map_err(|e| format!("scene: {e:?}"))?;
            let artwork = scene
                .artwork
                .as_ref()
                .filter(|a| !a.payload.is_empty())
                .map(|a| self.upload_artwork(a, sequence == 0xfffffffdu64))
                .transpose()?;
            let atlas = scene
                .glyph_atlas
                .as_ref()
                .filter(|a| !a.payload.is_empty())
                .map(|a| self.upload_glyph_atlas(a))
                .transpose()?;
            let overlay = scene.text_overlay.as_ref().map(|o| self.upload_text_overlay(o)).transpose()?;
            let base = scene.base_texture.as_ref().map(|b| { b.validate().map_err(|e| format!("base texture: {e:?}"))?; self.upload_base_texture(b) }).transpose()?;
            (artwork, atlas, overlay, base)
        } else {
            (None, None, None, None)
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
                text_run_count: atlas.text_runs.len() as u32,
                format: "Rgba8".into(),
            }
        });
        let text_overlay_receipt = scene.and_then(|s| s.text_overlay.as_ref()).map(|overlay| {
            let mut h = Sha256::new(); h.update(&overlay.payload);
            TextOverlayReceipt { sha256: format!("{:x}", h.finalize()), width: overlay.width, height: overlay.height, row_stride: overlay.row_stride, format: format!("{:?}", overlay.format), color_space: format!("{:?}", overlay.color_space), premultiplied: overlay.premultiplied, renderer_id: overlay.renderer_id.clone(), renderer_version: overlay.renderer_version.clone() }
        });
        let artwork_receipt = scene.and_then(|s| s.artwork.as_ref()).map(|a| { let mut h=Sha256::new(); h.update(&a.payload); ArtworkReceipt { sha256:format!("{:x}",h.finalize()), source_width:a.width, source_height:a.height, output_width:width, output_height:height, crop_mode:"center-crop".into(), aspect_mode:"cover".into() } });
        let base_texture_receipt = scene.and_then(|s| s.base_texture.as_ref()).map(|b| { let mut h=Sha256::new(); h.update(&b.payload); BaseTextureReceipt { sha256:format!("{:x}",h.finalize()), width:b.width, height:b.height, row_stride:b.row_stride, format:format!("{:?}",b.format), color_space:format!("{:?}",b.color_space) } });
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
        let artwork_texture = artwork_texture.unwrap_or_else(fallback_texture);
        let atlas_texture = atlas_texture.unwrap_or_else(fallback_texture);
        let overlay_texture = overlay_texture.unwrap_or_else(|| fallback_texture());
        let base_texture = base_texture.unwrap_or_else(|| fallback_texture());
        let artwork_view = artwork_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let atlas_view = atlas_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let overlay_view = overlay_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let base_view = base_texture.create_view(&wgpu::TextureViewDescriptor::default());
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
        let artwork_sampler = self
            .device
            .create_sampler(&wgpu::SamplerDescriptor::default());
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
                contents: bytemuck::cast_slice(&dynamics_sample_words(scene)),
                usage: wgpu::BufferUsages::STORAGE,
            });
        let stride = ((width * 4 + ROW_ALIGNMENT - 1) / ROW_ALIGNMENT) * ROW_ALIGNMENT;
        let bytes = stride as usize * height as usize;
        let output = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("gpu-frame"),
            size: bytes as u64,
            usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_SRC,
            mapped_at_creation: false,
        });
        let staging = self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("gpu-readback"),
            size: bytes as u64,
            usage: wgpu::BufferUsages::MAP_READ | wgpu::BufferUsages::COPY_DST,
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
                wgpu::BindGroupEntry { binding: 8, resource: wgpu::BindingResource::TextureView(&overlay_view) },
                wgpu::BindGroupEntry { binding: 9, resource: wgpu::BindingResource::Sampler(&atlas_sampler) },
                wgpu::BindGroupEntry { binding: 10, resource: wgpu::BindingResource::TextureView(&base_view) },
                wgpu::BindGroupEntry { binding: 11, resource: wgpu::BindingResource::Sampler(&atlas_sampler) },
            ],
        });
        let mut enc = self
            .device
            .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                label: Some("render"),
            });
        {
            let mut pass = enc.begin_compute_pass(&wgpu::ComputePassDescriptor {
                label: Some("music"),
                timestamp_writes: None,
            });
            pass.set_pipeline(&self.pipeline);
            pass.set_bind_group(0, &bind, &[]);
            pass.dispatch_workgroups((width + 7) / 8, (height + 7) / 8, 1);
        }
        enc.copy_buffer_to_buffer(&output, 0, &staging, 0, bytes as u64);
        self.queue.submit(Some(enc.finish()));
        let slice = staging.slice(..);
        let (tx, rx) = channel();
        slice.map_async(wgpu::MapMode::Read, move |r| {
            let _ = tx.send(r);
        });
        self.device.poll(wgpu::Maintain::Wait);
        rx.recv()
            .map_err(|_| "readback channel closed".to_string())?
            .map_err(|e| format!("readback: {e}"))?;
        let data = slice.get_mapped_range().to_vec();
        staging.unmap();
        Ok(GpuFrame {
            schema: CONTRACT_VERSION,
            sequence,
            pts_ns,
            width,
            height,
            row_stride: stride,
            format: PixelFormat::Rgba8,
            color_space: ColorSpace::Srgb,
            alpha: true,
            ownership: Ownership::OwnedByTransport,
            payload: data,
            glyph_atlas_receipt,
            text_overlay_receipt,
            artwork_receipt,
            base_texture_receipt,
            glyph_diagnostics: Some(glyph_diagnostics),
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
                rms_q15: 123,
                peak_q15: 456,
            },
            artwork: None,
            base_texture: None,
            glyph_atlas: None,
            text_overlay: None,
            layout: Default::default(),
            dynamics: Default::default(),
            palette: Default::default(),
            fingerprint: String::new(),
        };
        let words = scene_uniform_words(128, 72, 512, 9, Some(&scene));
        assert_eq!(&words[..7], &[128, 72, 128, 9, 123, 456, 1]);
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
                rms_q15: 0,
                peak_q15: 0,
            },
            artwork: None,
            base_texture: None,
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
                        id: "A".into(),
                        x: 8,
                        y: 16,
                        width: 32,
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
                    text: "A?".into(),
                    x: 10.0,
                    y: 12.0,
                    size_px: 40.0,
                    rgba: [255, 0, 0, 255],
                    opacity: 1.0,
                    font_family: String::new(),
                    font_weight: 0,
                }],
                asset_hash: String::new(),
            }),
            text_overlay: None,
            layout: Default::default(),
            dynamics: Default::default(),
            palette: Default::default(),
            fingerprint: String::new(),
        };
        let (words, diagnostics) = glyph_instance_words(Some(&scene), 100, 100);
        assert_eq!(words.len(), 24);
        assert_eq!(diagnostics.count, 2);
        assert_eq!(diagnostics.instances[0].id, "A");
        assert!(diagnostics.instances[0].scale > 0.0);
        let x0 = f32::from_bits(words[0]);
        let atlas_x0 = f32::from_bits(words[4]);
        let x1 = f32::from_bits(words[12]);
        assert!((x0 - 10.0 / 1280.0).abs() < 1e-6);
        assert!((atlas_x0 - 8.0 / 256.0).abs() < 1e-6);
        assert!(x1 > x0);
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
