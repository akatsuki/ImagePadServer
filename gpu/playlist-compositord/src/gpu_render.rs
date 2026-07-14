use crate::adapter;
use crate::contracts::{
    ColorSpace, GpuFrame, Ownership, PixelFormat, CONTRACT_VERSION, ROW_ALIGNMENT,
};
use std::sync::mpsc::channel;
use wgpu::util::DeviceExt;

const SHADER: &str = r#"
struct Params { width: u32, height: u32, row_words: u32, sequence: u32 }
@group(0) @binding(0) var<storage, read_write> pixels: array<u32>;
@group(0) @binding(1) var<uniform> params: Params;
@compute @workgroup_size(8, 8)
fn main(@builtin(global_invocation_id) id: vec3<u32>) {
  if (id.x >= params.width || id.y >= params.height) { return; }
  let i = id.y * params.row_words + id.x;
  let r = (id.x + params.sequence) & 255u;
  let g = (id.y + params.sequence * 3u) & 255u;
  let b = ((id.x + id.y) / 2u + params.sequence * 5u) & 255u;
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

impl Renderer {
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
        if width == 0 || height == 0 {
            return Err("invalid render dimensions".into());
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
        let params = self
            .device
            .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                label: Some("params"),
                contents: bytemuck::cast_slice(&[width, height, stride / 4, sequence as u32]),
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
