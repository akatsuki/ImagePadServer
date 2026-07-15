use crate::adapter;
use crate::contracts::{
    ColorSpace, GpuFrame, MusicScenePayload, Ownership, PixelFormat, CONTRACT_VERSION,
    ROW_ALIGNMENT,
};
use std::sync::mpsc::channel;
use std::num::NonZeroU32;
use wgpu::util::DeviceExt;

const SHADER: &str = r#"
struct Params {
  width: u32, height: u32, row_words: u32, sequence: u32,
  rms: u32, peak: u32, scene_enabled: u32, _pad: u32,
  spectrum: array<u32, 24>,
}
@group(0) @binding(0) var<storage, read_write> pixels: array<u32>;
@group(0) @binding(1) var<uniform> params: Params;
@compute @workgroup_size(8, 8)
fn main(@builtin(global_invocation_id) id: vec3<u32>) {
  if (id.x >= params.width || id.y >= params.height) { return; }
  let i = id.y * params.row_words + id.x;
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
    // Canonical scene background and glow, with a deterministic waveform.
    let glow = max(0.0, 1.0 - distance(vec2<f32>(fx, fy), vec2<f32>(0.5, 0.48)) * 1.7) * (0.18 + rms * 0.42);
    let wave = abs(sin(fx * 40.0 + f32(params.sequence) * 0.08 + fy * 5.0)) * (0.10 + peak * 0.25);
    var level = 0.0;
    for (var band: u32 = 0u; band < 24u; band = band + 1u) {
      let left = f32(band) / 24.0;
      let right = f32(band + 1u) / 24.0;
      if (fx >= left && fx < right) { level = f32(params.spectrum[band]) / 65535.0; }
    }
    let bars = select(0.0, 0.35 + level * 0.5, fy > (1.0 - level * 0.65));
    r = u32(clamp((0.05 + glow + bars * 0.75 + wave * 0.45) * 255.0, 0.0, 255.0));
    g = u32(clamp((0.08 + glow * 0.65 + bars * 0.20 + wave * 0.75) * 255.0, 0.0, 255.0));
    b = u32(clamp((0.18 + glow * 0.95 + bars * 0.90 + wave * 0.35) * 255.0, 0.0, 255.0));
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
}

fn scene_uniform_words(
    width: u32,
    height: u32,
    stride: u32,
    sequence: u64,
    scene: Option<&MusicScenePayload>,
) -> [u32; 32] {
    let mut words = [0u32; 32];
    words[0] = width;
    words[1] = height;
    words[2] = stride / 4;
    words[3] = sequence as u32;
    if let Some(scene) = scene {
        words[4] = scene.feature.rms_q15 as u32;
        words[5] = scene.feature.peak_q15 as u32;
        words[6] = 1;
        for (dst, src) in words[8..]
            .iter_mut()
            .zip(scene.feature.spectrum_q16.iter().copied())
        {
            *dst = src as u32;
        }
    }
    words
}

impl Renderer {
    fn upload_artwork(&self, artwork: &crate::contracts::ArtworkMetadata) -> Result<(), String> {
        artwork.validate().map_err(|e| format!("artwork: {e:?}"))?;
        if artwork.payload.is_empty() { return Ok(()); }
        let texture = self.device.create_texture(&wgpu::TextureDescriptor {
            label: Some("music-artwork"),
            size: wgpu::Extent3d { width: artwork.width, height: artwork.height, depth_or_array_layers: 1 },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: wgpu::TextureFormat::Rgba8UnormSrgb,
            usage: wgpu::TextureUsages::COPY_DST | wgpu::TextureUsages::TEXTURE_BINDING,
            view_formats: &[],
        });
        self.queue.write_texture(
            wgpu::ImageCopyTexture { texture: &texture, mip_level: 0, origin: wgpu::Origin3d::ZERO, aspect: wgpu::TextureAspect::All },
            &artwork.payload,
            wgpu::ImageDataLayout { offset: 0, bytes_per_row: Some(NonZeroU32::new(artwork.row_stride).unwrap().into()), rows_per_image: Some(NonZeroU32::new(artwork.height).unwrap().into()) },
            wgpu::Extent3d { width: artwork.width, height: artwork.height, depth_or_array_layers: 1 },
        );
        Ok(())
    }

    fn upload_glyph_atlas(&self, atlas: &crate::contracts::GlyphAtlasMetadata) -> Result<(), String> {
        atlas.validate().map_err(|e| format!("glyph atlas: {e:?}"))?;
        if atlas.payload.is_empty() { return Ok(()); }
        let texture = self.device.create_texture(&wgpu::TextureDescriptor {
            label: Some("music-glyph-atlas"),
            size: wgpu::Extent3d { width: atlas.width, height: atlas.height, depth_or_array_layers: 1 },
            mip_level_count: 1, sample_count: 1, dimension: wgpu::TextureDimension::D2,
            format: wgpu::TextureFormat::Rgba8UnormSrgb,
            usage: wgpu::TextureUsages::COPY_DST | wgpu::TextureUsages::TEXTURE_BINDING,
            view_formats: &[],
        });
        self.queue.write_texture(
            wgpu::ImageCopyTexture { texture: &texture, mip_level: 0, origin: wgpu::Origin3d::ZERO, aspect: wgpu::TextureAspect::All },
            &atlas.payload,
            wgpu::ImageDataLayout { offset: 0, bytes_per_row: Some(NonZeroU32::new(atlas.row_stride).unwrap().into()), rows_per_image: Some(NonZeroU32::new(atlas.height).unwrap().into()) },
            wgpu::Extent3d { width: atlas.width, height: atlas.height, depth_or_array_layers: 1 },
        );
        Ok(())
    }

    pub fn new() -> Result<Self, String> {
        let instance = wgpu::Instance::default();
        let selected = adapter::select(&instance).map_err(|e| format!("gpu adapter: {e}"))?;
        let adapter_name = selected.info.name;
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
        })
    }

    pub fn adapter_name(&self) -> &str {
        &self.adapter_name
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
        if let Some(scene) = scene {
            scene.validate().map_err(|e| format!("scene: {e:?}"))?;
            if let Some(artwork) = &scene.artwork { self.upload_artwork(artwork)?; }
            if let Some(atlas) = &scene.glyph_atlas { self.upload_glyph_atlas(atlas)?; }
        }
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
            glyph_atlas: None,
        };
        let words = scene_uniform_words(128, 72, 512, 9, Some(&scene));
        assert_eq!(&words[..7], &[128, 72, 128, 9, 123, 456, 1]);
        assert_eq!(words[8], 0);
        assert_eq!(words[31], 23_000);
    }
}
