use crate::protocol::{validate_assets, validate_frame, Asset, Frame, Update, MAX_ASSET_BYTES};
use crate::scene::{vertices, Vertex};
use std::collections::{HashMap, HashSet};
use std::fs::File;
use std::io::Read;
use std::sync::mpsc;
use wgpu::util::DeviceExt;

const ROW_ALIGNMENT: u32 = wgpu::COPY_BYTES_PER_ROW_ALIGNMENT;

struct GpuAsset {
    _texture: wgpu::Texture,
    bind_group: wgpu::BindGroup,
    width: u32,
    height: u32,
}

pub struct Renderer {
    device: wgpu::Device,
    queue: wgpu::Queue,
    pipeline: wgpu::RenderPipeline,
    bind_group_layout: wgpu::BindGroupLayout,
    sampler: wgpu::Sampler,
    assets: HashMap<String, GpuAsset>,
    ids: HashSet<String>,
    width: u32,
    height: u32,
    target: wgpu::Texture,
    readback: wgpu::Buffer,
    padded_row_bytes: u32,
    ready: super::protocol::Ready,
    background: wgpu::Color,
}

impl Renderer {
    pub fn new(width: u32, height: u32, assets: &[Asset]) -> Result<Self, String> {
        validate_assets(width, height, assets)?;
        let instance = wgpu::Instance::default();
        let adapter = pollster::block_on(instance.request_adapter(&wgpu::RequestAdapterOptions {
            power_preference: wgpu::PowerPreference::HighPerformance,
            compatible_surface: None,
            force_fallback_adapter: false,
        }))
        .or_else(|| {
            pollster::block_on(instance.request_adapter(&wgpu::RequestAdapterOptions {
                power_preference: wgpu::PowerPreference::LowPower,
                compatible_surface: None,
                force_fallback_adapter: true,
            }))
        })
        .ok_or_else(|| "no WGPU adapter is available".to_string())?;
        if width > adapter.limits().max_texture_dimension_2d
            || height > adapter.limits().max_texture_dimension_2d
        {
            return Err("canvas exceeds the selected adapter texture limit".into());
        }
        let info = adapter.get_info();
        let limits = wgpu::Limits::downlevel_defaults().using_resolution(adapter.limits());
        let (device, queue) = pollster::block_on(adapter.request_device(
            &wgpu::DeviceDescriptor {
                label: Some("xpost compositor device"),
                required_features: wgpu::Features::empty(),
                required_limits: limits,
            },
            None,
        ))
        .map_err(|e| format!("requesting WGPU device: {e}"))?;

        let bind_group_layout = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
            label: Some("xpost texture bindings"),
            entries: &[
                wgpu::BindGroupLayoutEntry {
                    binding: 0,
                    visibility: wgpu::ShaderStages::FRAGMENT,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: true },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 1,
                    visibility: wgpu::ShaderStages::FRAGMENT,
                    ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::Filtering),
                    count: None,
                },
            ],
        });
        let sampler = device.create_sampler(&wgpu::SamplerDescriptor {
            label: Some("xpost linear sampler"),
            address_mode_u: wgpu::AddressMode::ClampToEdge,
            address_mode_v: wgpu::AddressMode::ClampToEdge,
            mag_filter: wgpu::FilterMode::Linear,
            min_filter: wgpu::FilterMode::Linear,
            ..Default::default()
        });
        let shader = device.create_shader_module(wgpu::ShaderModuleDescriptor {
            label: Some("xpost card compositor shader"),
            source: wgpu::ShaderSource::Wgsl(include_str!("shader.wgsl").into()),
        });
        let pipeline_layout = device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
            label: Some("xpost compositor pipeline layout"),
            bind_group_layouts: &[&bind_group_layout],
            push_constant_ranges: &[],
        });
        let attributes = wgpu::vertex_attr_array![0 => Float32x2, 1 => Float32x2, 2 => Float32];
        let pipeline = device.create_render_pipeline(&wgpu::RenderPipelineDescriptor {
            label: Some("xpost RGBA compositor pipeline"),
            layout: Some(&pipeline_layout),
            vertex: wgpu::VertexState {
                module: &shader,
                entry_point: "vs_main",
                compilation_options: Default::default(),
                buffers: &[wgpu::VertexBufferLayout {
                    array_stride: std::mem::size_of::<Vertex>() as u64,
                    step_mode: wgpu::VertexStepMode::Vertex,
                    attributes: &attributes,
                }],
            },
            fragment: Some(wgpu::FragmentState {
                module: &shader,
                entry_point: "fs_main",
                compilation_options: Default::default(),
                targets: &[Some(wgpu::ColorTargetState {
                    format: wgpu::TextureFormat::Rgba8Unorm,
                    blend: Some(wgpu::BlendState::ALPHA_BLENDING),
                    write_mask: wgpu::ColorWrites::ALL,
                })],
            }),
            primitive: wgpu::PrimitiveState {
                topology: wgpu::PrimitiveTopology::TriangleList,
                strip_index_format: None,
                front_face: wgpu::FrontFace::Ccw,
                cull_mode: None,
                unclipped_depth: false,
                polygon_mode: wgpu::PolygonMode::Fill,
                conservative: false,
            },
            depth_stencil: None,
            multisample: wgpu::MultisampleState::default(),
            multiview: None,
        });

        let padded_row_bytes = align_up(width * 4, ROW_ALIGNMENT);
        let target = device.create_texture(&wgpu::TextureDescriptor {
            label: Some("xpost compositor target"),
            size: wgpu::Extent3d {
                width,
                height,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: wgpu::TextureFormat::Rgba8Unorm,
            usage: wgpu::TextureUsages::RENDER_ATTACHMENT | wgpu::TextureUsages::COPY_SRC,
            view_formats: &[],
        });
        let readback = device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("xpost frame readback"),
            size: u64::from(padded_row_bytes) * u64::from(height),
            usage: wgpu::BufferUsages::COPY_DST | wgpu::BufferUsages::MAP_READ,
            mapped_at_creation: false,
        });

        let mut renderer = Self {
            device,
            queue,
            pipeline,
            bind_group_layout,
            sampler,
            assets: HashMap::with_capacity(assets.len()),
            ids: assets.iter().map(|a| a.id.clone()).collect(),
            width,
            height,
            target,
            readback,
            padded_row_bytes,
            background: wgpu::Color::BLACK,
            ready: super::protocol::Ready {
                adapter: info.name,
                backend: format!("{:?}", info.backend),
            },
        };
        for asset in assets {
            let byte_count = u64::from(asset.width) * u64::from(asset.height) * 4;
            if byte_count > MAX_ASSET_BYTES {
                return Err(format!(
                    "asset {} pixel bytes exceed the configured limit",
                    asset.id
                ));
            }
            let metadata = std::fs::metadata(&asset.path)
                .map_err(|e| format!("reading asset {} metadata: {e}", asset.id))?;
            if metadata.len() != byte_count {
                return Err(format!(
                    "asset {} raw RGBA file has {} bytes; expected {byte_count}",
                    asset.id,
                    metadata.len()
                ));
            }
            let mut pixels = vec![0u8; byte_count as usize];
            let mut file = File::open(&asset.path)
                .map_err(|e| format!("opening asset {} RGBA pixels: {e}", asset.id))?;
            file.read_exact(&mut pixels)
                .map_err(|e| format!("reading asset {} RGBA pixels: {e}", asset.id))?;
            let mut extra = [0u8; 1];
            if file
                .read(&mut extra)
                .map_err(|e| format!("checking asset {} RGBA file length: {e}", asset.id))?
                != 0
            {
                return Err(format!(
                    "asset {} raw RGBA file changed size while being read",
                    asset.id
                ));
            }
            renderer.add_asset(&asset.id, asset.width, asset.height, &pixels)?;
        }
        Ok(renderer)
    }

    pub fn ready(&self) -> &super::protocol::Ready {
        &self.ready
    }

    pub fn set_light_background(&mut self, light: bool) {
        self.background = if light {
            wgpu::Color::WHITE
        } else {
            wgpu::Color::BLACK
        };
    }

    pub fn validate_request(&self, frame: &Frame, update: Option<&Update>) -> Result<(), String> {
        validate_frame(frame, &self.ids)?;
        if let Some(update) = update {
            let asset = self
                .assets
                .get(&update.asset_id)
                .ok_or_else(|| format!("update references unknown asset {}", update.asset_id))?;
            let expected = u64::from(asset.width) * u64::from(asset.height) * 4;
            if update.byte_len != expected || expected > MAX_ASSET_BYTES {
                return Err(format!(
                    "update for {} has invalid RGBA byte length",
                    update.asset_id
                ));
            }
        }
        Ok(())
    }

    pub fn render(
        &mut self,
        frame: &Frame,
        update: Option<(&Update, &[u8])>,
    ) -> Result<Vec<u8>, String> {
        self.validate_request(frame, update.map(|(metadata, _)| metadata))?;
        if let Some((update, pixels)) = update {
            let asset = self
                .assets
                .get(&update.asset_id)
                .ok_or_else(|| format!("update references unknown asset {}", update.asset_id))?;
            let expected = u64::from(asset.width) * u64::from(asset.height) * 4;
            if update.byte_len != expected
                || pixels.len() as u64 != expected
                || expected > MAX_ASSET_BYTES
            {
                return Err(format!(
                    "update for {} has invalid RGBA byte length",
                    update.asset_id
                ));
            }
            self.queue.write_texture(
                wgpu::ImageCopyTexture {
                    texture: &asset._texture,
                    mip_level: 0,
                    origin: wgpu::Origin3d::ZERO,
                    aspect: wgpu::TextureAspect::All,
                },
                pixels,
                wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(asset.width * 4),
                    rows_per_image: Some(asset.height),
                },
                wgpu::Extent3d {
                    width: asset.width,
                    height: asset.height,
                    depth_or_array_layers: 1,
                },
            );
        }

        let mut all_vertices = Vec::with_capacity(frame.layers.len() * 6);
        for layer in &frame.layers {
            all_vertices.extend_from_slice(&vertices(layer, self.width, self.height)?);
        }
        let vertex_buffer = if all_vertices.is_empty() {
            None
        } else {
            Some(
                self.device
                    .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                        label: Some("xpost frame vertices"),
                        contents: bytemuck::cast_slice(&all_vertices),
                        usage: wgpu::BufferUsages::VERTEX,
                    }),
            )
        };
        let view = self
            .target
            .create_view(&wgpu::TextureViewDescriptor::default());
        let mut encoder = self
            .device
            .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                label: Some("xpost frame encoder"),
            });
        {
            let mut pass = encoder.begin_render_pass(&wgpu::RenderPassDescriptor {
                label: Some("xpost composite frame"),
                color_attachments: &[Some(wgpu::RenderPassColorAttachment {
                    view: &view,
                    resolve_target: None,
                    ops: wgpu::Operations {
                        load: wgpu::LoadOp::Clear(self.background),
                        store: wgpu::StoreOp::Store,
                    },
                })],
                depth_stencil_attachment: None,
                occlusion_query_set: None,
                timestamp_writes: None,
            });
            pass.set_pipeline(&self.pipeline);
            if let Some(buffer) = &vertex_buffer {
                pass.set_vertex_buffer(0, buffer.slice(..));
                for (index, layer) in frame.layers.iter().enumerate() {
                    let asset = self
                        .assets
                        .get(&layer.asset_id)
                        .expect("validated asset remains registered");
                    pass.set_bind_group(0, &asset.bind_group, &[]);
                    let start = (index * 6) as u32;
                    pass.draw(start..start + 6, 0..1);
                }
            }
        }
        encoder.copy_texture_to_buffer(
            wgpu::ImageCopyTexture {
                texture: &self.target,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            wgpu::ImageCopyBuffer {
                buffer: &self.readback,
                layout: wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(self.padded_row_bytes),
                    rows_per_image: Some(self.height),
                },
            },
            wgpu::Extent3d {
                width: self.width,
                height: self.height,
                depth_or_array_layers: 1,
            },
        );
        self.queue.submit(Some(encoder.finish()));
        let slice = self.readback.slice(..);
        let (sender, receiver) = mpsc::channel();
        slice.map_async(wgpu::MapMode::Read, move |result| {
            let _ = sender.send(result);
        });
        self.device.poll(wgpu::Maintain::Wait);
        receiver
            .recv()
            .map_err(|e| format!("waiting for WGPU readback: {e}"))?
            .map_err(|e| format!("mapping WGPU readback: {e}"))?;
        let mapped = slice.get_mapped_range();
        let row_bytes = (self.width * 4) as usize;
        let mut output = vec![0u8; row_bytes * self.height as usize];
        for row in 0..self.height as usize {
            let src = row * self.padded_row_bytes as usize;
            let dst = row * row_bytes;
            output[dst..dst + row_bytes].copy_from_slice(&mapped[src..src + row_bytes]);
        }
        drop(mapped);
        self.readback.unmap();
        Ok(output)
    }

    fn add_asset(
        &mut self,
        id: &str,
        width: u32,
        height: u32,
        pixels: &[u8],
    ) -> Result<(), String> {
        let expected = u64::from(width) * u64::from(height) * 4;
        if pixels.len() as u64 != expected || expected > MAX_ASSET_BYTES {
            return Err(format!("asset {id} has invalid RGBA pixel bytes"));
        }
        let texture = self.device.create_texture(&wgpu::TextureDescriptor {
            label: Some("xpost cached asset texture"),
            size: wgpu::Extent3d {
                width,
                height,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: wgpu::TextureFormat::Rgba8Unorm,
            usage: wgpu::TextureUsages::TEXTURE_BINDING | wgpu::TextureUsages::COPY_DST,
            view_formats: &[],
        });
        self.queue.write_texture(
            wgpu::ImageCopyTexture {
                texture: &texture,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            pixels,
            wgpu::ImageDataLayout {
                offset: 0,
                bytes_per_row: Some(width * 4),
                rows_per_image: Some(height),
            },
            wgpu::Extent3d {
                width,
                height,
                depth_or_array_layers: 1,
            },
        );
        let view = texture.create_view(&wgpu::TextureViewDescriptor::default());
        let bind_group = self.device.create_bind_group(&wgpu::BindGroupDescriptor {
            label: Some("xpost cached asset bindings"),
            layout: &self.bind_group_layout,
            entries: &[
                wgpu::BindGroupEntry {
                    binding: 0,
                    resource: wgpu::BindingResource::TextureView(&view),
                },
                wgpu::BindGroupEntry {
                    binding: 1,
                    resource: wgpu::BindingResource::Sampler(&self.sampler),
                },
            ],
        });
        self.assets.insert(
            id.to_string(),
            GpuAsset {
                _texture: texture,
                bind_group,
                width,
                height,
            },
        );
        Ok(())
    }
}

fn align_up(value: u32, alignment: u32) -> u32 {
    value.div_ceil(alignment) * alignment
}
