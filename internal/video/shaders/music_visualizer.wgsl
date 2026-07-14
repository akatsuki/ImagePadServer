struct Features {
  spectrum: array<u32, 24>,
  rms_q15: u32,
  peak_q15: u32,
};
@group(0) @binding(0) var<storage, read> features: Features;

struct VsOut { @builtin(position) pos: vec4<f32>, @location(0) uv: vec2<f32> };
@vertex fn vs(@builtin(vertex_index) i: u32) -> VsOut {
  var p = array<vec2<f32>, 3>(vec2(-1.0,-1.0), vec2(3.0,-1.0), vec2(-1.0,3.0));
  var o: VsOut; o.pos = vec4(p[i], 0.0, 1.0); o.uv = p[i] * 0.5 + vec2(0.5); return o;
}

// GPU-owned music background and spectrum glow. Textures/glyphs are separate
// overlay commands; FFmpeg showwaves/showfreqs/ASS must remain disabled.
@fragment fn fs(in: VsOut) -> @location(0) vec4<f32> {
  let x = clamp(in.uv.x, 0.0, 0.999) * 24.0;
  let band = u32(x);
  let level = f32(features.spectrum[band]) / 65535.0;
  let rms = f32(features.rms_q15) / 32767.0;
  let glow = smoothstep(0.0, 1.0, level * (0.35 + rms));
  return vec4(0.03 + glow * 0.15, 0.04 + glow * 0.35, 0.10 + glow * 0.65, 1.0);
}
