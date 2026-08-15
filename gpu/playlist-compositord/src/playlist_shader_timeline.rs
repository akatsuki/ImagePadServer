use crate::music_v2_pass_graph::CANONICAL_ORDER;
use crate::music_v2_shader_module::{
    ShaderBinding, ShaderBindingKind, ShaderModule, ShaderModuleDescriptor,
};
use crate::playlist_timeline::ResolvedFrameControl;
use std::num::NonZeroU32;

pub const PLAYLIST_SHADER_VERSION: u16 = 1;
pub const PLAYLIST_WORKGROUP_SIZE: (u32, u32, u32) = (8, 8, 1);
pub const PLAYLIST_FRAME_UNIFORM_WORDS: usize = 20 + 64 + 64;
pub const PLAYLIST_FRAME_UNIFORM_BYTES: usize = PLAYLIST_FRAME_UNIFORM_WORDS * 4;
pub const PLAYLIST_BINDINGS: [ShaderBinding; 11] = [
    ShaderBinding {
        index: 0,
        name: "frame_control",
        kind: ShaderBindingKind::Uniform,
    },
    ShaderBinding {
        index: 1,
        name: "artwork_texture",
        kind: ShaderBindingKind::SampledTextureRgba8,
    },
    ShaderBinding {
        index: 2,
        name: "glyph_atlas",
        kind: ShaderBindingKind::SampledTextureRgba8,
    },
    ShaderBinding {
        index: 3,
        name: "glyph_sampler",
        kind: ShaderBindingKind::Sampler,
    },
    ShaderBinding {
        index: 4,
        name: "loudness_texture",
        kind: ShaderBindingKind::SampledTextureRgba8,
    },
    ShaderBinding {
        index: 5,
        name: "spectrum_q16",
        kind: ShaderBindingKind::StorageRead,
    },
    ShaderBinding {
        index: 6,
        name: "rgb_output",
        kind: ShaderBindingKind::StorageTextureRgba8,
    },
    ShaderBinding {
        index: 7,
        name: "text_overlay_texture",
        kind: ShaderBindingKind::SampledTextureRgba8,
    },
    ShaderBinding {
        index: 8,
        name: "layout_rects",
        kind: ShaderBindingKind::StorageRead,
    },
    ShaderBinding {
        index: 9,
        name: "palette_values",
        kind: ShaderBindingKind::StorageRead,
    },
    ShaderBinding {
        index: 10,
        name: "base_texture",
        kind: ShaderBindingKind::SampledTextureRgba8,
    },
];

#[repr(C)]
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PlaylistFrameUniform {
    pub frame_index: u32,
    pub sequence_lo: u32,
    pub sequence_hi: u32,
    pub pts_ns_lo: u32,
    pub pts_ns_hi: u32,
    pub progress_q16: u32,
    pub text_alpha_q16: u32,
    pub black_alpha_q16: u32,
    pub scroll_x_q16: i32,
    pub scroll_y_q16: i32,
    pub scroll_alpha_q16: u32,
    pub scroll_clip_x: i32,
    pub scroll_clip_y: i32,
    pub scroll_clip_w: i32,
    pub scroll_clip_h: i32,
    pub loudness_q16: u32,
    pub loudness_trend_q16: u32,
    pub spectrum_count: u32,
    pub waveform_count: u32,
    pub uniform_pad: u32,
    pub spectrum_q16: [u32; 64],
    pub waveform_q16: [u32; 64],
}

impl PlaylistFrameUniform {
    pub fn from_resolved(frame: &ResolvedFrameControl) -> Self {
        let sequence = frame.sequence.to_le_bytes();
        let pts_ns = frame.pts_ns.to_le_bytes();
        let mut spectrum_q16 = [0u32; 64];
        for (index, value) in frame.spectrum_q16.iter().take(64).enumerate() {
            spectrum_q16[index] = u32::from(*value);
        }
        let mut waveform_q16 = [0u32; 64];
        for (index, value) in frame.waveform_q16.iter().take(64).enumerate() {
            waveform_q16[index] = u32::from(*value);
        }
        Self {
            frame_index: frame.frame_index as u32,
            sequence_lo: u32::from_le_bytes(sequence[0..4].try_into().unwrap()),
            sequence_hi: u32::from_le_bytes(sequence[4..8].try_into().unwrap()),
            pts_ns_lo: u32::from_le_bytes(pts_ns[0..4].try_into().unwrap()),
            pts_ns_hi: u32::from_le_bytes(pts_ns[4..8].try_into().unwrap()),
            progress_q16: u32::from(frame.progress_q16),
            text_alpha_q16: u32::from(frame.text_alpha_q16),
            black_alpha_q16: u32::from(frame.black_alpha_q16),
            scroll_x_q16: frame.scroll_x_q16,
            scroll_y_q16: frame.scroll_y_q16,
            scroll_alpha_q16: u32::from(frame.scroll_alpha_q16),
            scroll_clip_x: frame.scroll_clip.x,
            scroll_clip_y: frame.scroll_clip.y,
            scroll_clip_w: frame.scroll_clip.w,
            scroll_clip_h: frame.scroll_clip.h,
            loudness_q16: u32::from(frame.loudness_q16),
            loudness_trend_q16: u32::from(frame.loudness_trend_q16),
            spectrum_count: frame.spectrum_q16.len().min(64) as u32,
            waveform_count: frame.waveform_q16.len().min(64) as u32,
            uniform_pad: 0,
            spectrum_q16,
            waveform_q16,
        }
    }

    pub fn to_bytes(&self) -> Vec<u8> {
        let mut bytes = Vec::with_capacity(PLAYLIST_FRAME_UNIFORM_BYTES);
        for word in [
            self.frame_index,
            self.sequence_lo,
            self.sequence_hi,
            self.pts_ns_lo,
            self.pts_ns_hi,
            self.progress_q16,
            self.text_alpha_q16,
            self.black_alpha_q16,
            self.scroll_x_q16 as u32,
            self.scroll_y_q16 as u32,
            self.scroll_alpha_q16,
            self.scroll_clip_x as u32,
            self.scroll_clip_y as u32,
            self.scroll_clip_w as u32,
            self.scroll_clip_h as u32,
            self.loudness_q16,
            self.loudness_trend_q16,
            self.spectrum_count,
            self.waveform_count,
            self.uniform_pad,
        ] {
            bytes.extend_from_slice(&word.to_ne_bytes());
        }
        for word in self.spectrum_q16.iter().chain(self.waveform_q16.iter()) {
            bytes.extend_from_slice(&word.to_ne_bytes());
        }
        bytes
    }
}

pub struct PlaylistTimelineShaderModule;
pub const PLAYLIST_TIMELINE_SHADER_MODULE: PlaylistTimelineShaderModule =
    PlaylistTimelineShaderModule;

impl ShaderModule for PlaylistTimelineShaderModule {
    fn descriptor(&self) -> ShaderModuleDescriptor {
        ShaderModuleDescriptor {
            version: PLAYLIST_SHADER_VERSION,
            entry_point: "render_playlist_timeline",
            workgroup_size: PLAYLIST_WORKGROUP_SIZE,
            bindings: &PLAYLIST_BINDINGS,
            passes: &CANONICAL_ORDER,
            gpu_owned_output: true,
            production_ready: false,
        }
    }

    fn wgsl(&self) -> &'static str {
        PLAYLIST_TIMELINE_WGSL
    }
}

pub fn validate_playlist_descriptor(descriptor: ShaderModuleDescriptor) -> Result<(), String> {
    if descriptor.version == 0 || descriptor.entry_point != "render_playlist_timeline" {
        return Err("playlist shader descriptor identity is invalid".into());
    }
    if descriptor.workgroup_size != PLAYLIST_WORKGROUP_SIZE
        || descriptor.passes != CANONICAL_ORDER.as_slice()
        || !descriptor.gpu_owned_output
        || descriptor.bindings != PLAYLIST_BINDINGS
    {
        return Err("playlist shader descriptor does not match the timeline contract".into());
    }
    if descriptor
        .bindings
        .iter()
        .any(|binding| binding.name.contains("pcm"))
    {
        return Err("playlist timeline shader must not bind PCM".into());
    }
    Ok(())
}

pub struct PlaylistComputePipeline {
    pub bind_group_layout: wgpu::BindGroupLayout,
    pub pipeline: wgpu::ComputePipeline,
}

pub struct PlaylistBindGroupResources<'a> {
    pub frame_uniform: &'a wgpu::Buffer,
    pub artwork_texture: &'a wgpu::TextureView,
    pub glyph_atlas: &'a wgpu::TextureView,
    pub glyph_sampler: &'a wgpu::Sampler,
    pub loudness_texture: &'a wgpu::TextureView,
    pub spectrum_buffer: &'a wgpu::Buffer,
    pub rgb_output: &'a wgpu::TextureView,
    pub text_overlay_texture: &'a wgpu::TextureView,
    pub layout_buffer: &'a wgpu::Buffer,
    pub palette_buffer: &'a wgpu::Buffer,
    pub base_texture: &'a wgpu::TextureView,
}

pub struct PlaylistStaticTextureSet {
    pub base_texture: wgpu::Texture,
    pub base_view: wgpu::TextureView,
    pub artwork_texture: wgpu::Texture,
    pub artwork_view: wgpu::TextureView,
    pub glyph_texture: wgpu::Texture,
    pub glyph_view: wgpu::TextureView,
    pub loudness_texture: wgpu::Texture,
    pub loudness_view: wgpu::TextureView,
    pub text_overlay_texture: wgpu::Texture,
    pub text_overlay_view: wgpu::TextureView,
    pub layout_buffer: wgpu::Buffer,
    pub palette_buffer: wgpu::Buffer,
    pub sampler: wgpu::Sampler,
}

impl PlaylistStaticTextureSet {
    pub fn upload(
        device: &wgpu::Device,
        queue: &wgpu::Queue,
        assets: &crate::protocol::TrackAssets,
    ) -> Result<Self, String> {
        let artwork = assets
            .artwork
            .as_ref()
            .ok_or("playlist artwork static texture is required")?;
        let glyph = assets
            .glyph_atlas
            .as_ref()
            .ok_or("playlist glyph static texture is required")?;
        let loudness = assets
            .loudness_texture
            .as_ref()
            .ok_or("playlist loudness static texture is required")?;
        let artwork_texture = create_playlist_static_texture(
            device,
            queue,
            "playlist-artwork-static",
            artwork.width,
            artwork.height,
            artwork.row_stride,
            &artwork.payload,
        )?;
        let glyph_texture = create_playlist_static_texture(
            device,
            queue,
            "playlist-glyph-static",
            glyph.width,
            glyph.height,
            glyph.row_stride,
            &glyph.payload,
        )?;
        let loudness_texture = create_playlist_static_texture(
            device,
            queue,
            "playlist-loudness-static",
            loudness.width,
            loudness.height,
            loudness.row_stride,
            &loudness.payload,
        )?;
        let base_texture = match assets.base_texture.as_ref() {
            Some(base) => create_playlist_static_texture(
                device,
                queue,
                "playlist-base-static",
                base.width,
                base.height,
                base.row_stride,
                &base.payload,
            )?,
            None => create_playlist_static_texture(
                device,
                queue,
                "playlist-base-empty",
                1,
                1,
                4,
                &[0, 0, 0, 255],
            )?,
        };
        let text_overlay_texture = match assets.text_overlay.as_ref() {
            Some(text) if text.kind == "screen_rgba" => create_playlist_static_texture(
                device,
                queue,
                "playlist-text-overlay-static",
                text.width,
                text.height,
                text.row_stride,
                &text.payload,
            )?,
            Some(_) => {
                return Err("playlist text overlay must be CPU-rasterized screen_rgba".into())
            }
            None => create_playlist_static_texture(
                device,
                queue,
                "playlist-text-overlay-empty",
                1,
                1,
                4,
                &[0, 0, 0, 0],
            )?,
        };
        let layout_values = [
            assets.layout.artwork.x,
            assets.layout.artwork.y,
            assets.layout.artwork.w,
            assets.layout.artwork.h,
            assets.layout.title.x,
            assets.layout.title.y,
            assets.layout.title.w,
            assets.layout.title.h,
            assets.layout.artist.x,
            assets.layout.artist.y,
            assets.layout.artist.w,
            assets.layout.artist.h,
            assets.layout.album.x,
            assets.layout.album.y,
            assets.layout.album.w,
            assets.layout.album.h,
            assets.layout.spectrum.x,
            assets.layout.spectrum.y,
            assets.layout.spectrum.w,
            assets.layout.spectrum.h,
            assets.layout.loudness.x,
            assets.layout.loudness.y,
            assets.layout.loudness.w,
            assets.layout.loudness.h,
            assets.layout.progress.x,
            assets.layout.progress.y,
            assets.layout.progress.w,
            assets.layout.progress.h,
            assets.layout.time.x,
            assets.layout.time.y,
            assets.layout.time.w,
            assets.layout.time.h,
        ];
        let layout_bytes: Vec<u8> = layout_values
            .iter()
            .flat_map(|value| value.to_ne_bytes())
            .collect();
        let layout_buffer = device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("playlist-static-layout-rects"),
            size: layout_bytes.len() as u64,
            usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_DST,
            mapped_at_creation: false,
        });
        queue.write_buffer(&layout_buffer, 0, &layout_bytes);
        let palette_values = [
            u32::from_le_bytes(assets.palette.primary),
            u32::from_le_bytes(assets.palette.accent),
            u32::from_le_bytes(assets.palette.background),
            u32::from_le_bytes(assets.palette.overlay),
            u32::from(assets.palette.blur_strength),
            u32::from(assets.palette.readability),
            u32::from(assets.loudness_guides[0]),
            u32::from(assets.loudness_guides[1]),
            u32::from(assets.loudness_guides[2]),
            u32::from(assets.loudness_guides[3]),
        ];
        let palette_bytes: Vec<u8> = palette_values
            .iter()
            .flat_map(|value| value.to_ne_bytes())
            .collect();
        let palette_buffer = device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("playlist-static-palette-values"),
            size: palette_bytes.len() as u64,
            usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_DST,
            mapped_at_creation: false,
        });
        queue.write_buffer(&palette_buffer, 0, &palette_bytes);
        let base_view = base_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let artwork_view = artwork_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let glyph_view = glyph_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let loudness_view = loudness_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let text_overlay_view =
            text_overlay_texture.create_view(&wgpu::TextureViewDescriptor::default());
        let sampler = device.create_sampler(&wgpu::SamplerDescriptor {
            label: Some("playlist-static-texture-sampler"),
            address_mode_u: wgpu::AddressMode::ClampToEdge,
            address_mode_v: wgpu::AddressMode::ClampToEdge,
            address_mode_w: wgpu::AddressMode::ClampToEdge,
            mag_filter: wgpu::FilterMode::Linear,
            min_filter: wgpu::FilterMode::Linear,
            mipmap_filter: wgpu::FilterMode::Nearest,
            ..Default::default()
        });
        Ok(Self {
            base_texture,
            base_view,
            artwork_texture,
            artwork_view,
            glyph_texture,
            glyph_view,
            loudness_texture,
            loudness_view,
            text_overlay_texture,
            text_overlay_view,
            layout_buffer,
            palette_buffer,
            sampler,
        })
    }
}

pub fn validate_playlist_texture_payload(
    width: u32,
    height: u32,
    row_stride: u32,
    payload_len: usize,
) -> Result<(), String> {
    let tight_stride = width
        .checked_mul(4)
        .ok_or_else(|| "playlist texture width overflows RGBA8 stride".to_string())?;
    if width == 0 || height == 0 || row_stride < tight_stride {
        return Err("playlist static texture dimensions or stride are invalid".into());
    }
    let required = (row_stride as usize)
        .checked_mul(height as usize)
        .ok_or_else(|| "playlist static texture payload size overflows".to_string())?;
    if payload_len < required {
        return Err("playlist static texture payload is shorter than its row stride".into());
    }
    Ok(())
}

pub fn create_playlist_static_texture(
    device: &wgpu::Device,
    queue: &wgpu::Queue,
    label: &str,
    width: u32,
    height: u32,
    row_stride: u32,
    payload: &[u8],
) -> Result<wgpu::Texture, String> {
    validate_playlist_texture_payload(width, height, row_stride, payload.len())?;
    let tight_stride = width as usize * 4;
    let mut tight_payload = vec![0u8; tight_stride * height as usize];
    for row in 0..height as usize {
        let source_start = row * row_stride as usize;
        let target_start = row * tight_stride;
        tight_payload[target_start..target_start + tight_stride]
            .copy_from_slice(&payload[source_start..source_start + tight_stride]);
    }
    let texture = device.create_texture(&wgpu::TextureDescriptor {
        label: Some(label),
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
    queue.write_texture(
        wgpu::ImageCopyTexture {
            texture: &texture,
            mip_level: 0,
            origin: wgpu::Origin3d::ZERO,
            aspect: wgpu::TextureAspect::All,
        },
        &tight_payload,
        wgpu::ImageDataLayout {
            offset: 0,
            bytes_per_row: Some(NonZeroU32::new(tight_stride as u32).unwrap().into()),
            rows_per_image: Some(NonZeroU32::new(height).unwrap().into()),
        },
        wgpu::Extent3d {
            width,
            height,
            depth_or_array_layers: 1,
        },
    );
    Ok(texture)
}

pub fn create_playlist_compute_pipeline(
    device: &wgpu::Device,
) -> Result<PlaylistComputePipeline, String> {
    validate_playlist_descriptor(PLAYLIST_TIMELINE_SHADER_MODULE.descriptor())?;
    let shader = device.create_shader_module(wgpu::ShaderModuleDescriptor {
        label: Some("playlist-timeline-shader"),
        source: wgpu::ShaderSource::Wgsl(PLAYLIST_TIMELINE_WGSL.into()),
    });
    let entries = [
        wgpu::BindGroupLayoutEntry {
            binding: 0,
            visibility: wgpu::ShaderStages::COMPUTE,
            ty: wgpu::BindingType::Buffer {
                ty: wgpu::BufferBindingType::Uniform,
                has_dynamic_offset: false,
                min_binding_size: None,
            },
            count: None,
        },
        wgpu::BindGroupLayoutEntry {
            binding: 1,
            visibility: wgpu::ShaderStages::COMPUTE,
            ty: wgpu::BindingType::Texture {
                sample_type: wgpu::TextureSampleType::Float { filterable: true },
                view_dimension: wgpu::TextureViewDimension::D2,
                multisampled: false,
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
            ty: wgpu::BindingType::Texture {
                sample_type: wgpu::TextureSampleType::Float { filterable: true },
                view_dimension: wgpu::TextureViewDimension::D2,
                multisampled: false,
            },
            count: None,
        },
        wgpu::BindGroupLayoutEntry {
            binding: 5,
            visibility: wgpu::ShaderStages::COMPUTE,
            ty: wgpu::BindingType::Buffer {
                ty: wgpu::BufferBindingType::Storage { read_only: true },
                has_dynamic_offset: false,
                min_binding_size: None,
            },
            count: None,
        },
        wgpu::BindGroupLayoutEntry {
            binding: 6,
            visibility: wgpu::ShaderStages::COMPUTE,
            ty: wgpu::BindingType::StorageTexture {
                access: wgpu::StorageTextureAccess::WriteOnly,
                format: wgpu::TextureFormat::Rgba8Unorm,
                view_dimension: wgpu::TextureViewDimension::D2,
            },
            count: None,
        },
        wgpu::BindGroupLayoutEntry {
            binding: 7,
            visibility: wgpu::ShaderStages::COMPUTE,
            ty: wgpu::BindingType::Texture {
                sample_type: wgpu::TextureSampleType::Float { filterable: true },
                view_dimension: wgpu::TextureViewDimension::D2,
                multisampled: false,
            },
            count: None,
        },
        wgpu::BindGroupLayoutEntry {
            binding: 8,
            visibility: wgpu::ShaderStages::COMPUTE,
            ty: wgpu::BindingType::Buffer {
                ty: wgpu::BufferBindingType::Storage { read_only: true },
                has_dynamic_offset: false,
                min_binding_size: None,
            },
            count: None,
        },
        wgpu::BindGroupLayoutEntry {
            binding: 9,
            visibility: wgpu::ShaderStages::COMPUTE,
            ty: wgpu::BindingType::Buffer {
                ty: wgpu::BufferBindingType::Storage { read_only: true },
                has_dynamic_offset: false,
                min_binding_size: None,
            },
            count: None,
        },
        wgpu::BindGroupLayoutEntry {
            binding: 10,
            visibility: wgpu::ShaderStages::COMPUTE,
            ty: wgpu::BindingType::Texture {
                sample_type: wgpu::TextureSampleType::Float { filterable: true },
                view_dimension: wgpu::TextureViewDimension::D2,
                multisampled: false,
            },
            count: None,
        },
    ];
    let bind_group_layout = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
        label: Some("playlist-timeline-bindings"),
        entries: &entries,
    });
    let pipeline_layout = device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
        label: Some("playlist-timeline-pipeline-layout"),
        bind_group_layouts: &[&bind_group_layout],
        push_constant_ranges: &[],
    });
    let pipeline = device.create_compute_pipeline(&wgpu::ComputePipelineDescriptor {
        label: Some("playlist-timeline-pipeline"),
        layout: Some(&pipeline_layout),
        module: &shader,
        entry_point: "render_playlist_timeline",
        compilation_options: Default::default(),
    });
    Ok(PlaylistComputePipeline {
        bind_group_layout,
        pipeline,
    })
}

pub fn playlist_dispatch_workgroups(width: u32, height: u32) -> Result<(u32, u32, u32), String> {
    if width == 0 || height == 0 {
        return Err("playlist output dimensions must be non-zero".into());
    }
    Ok((
        width.saturating_add(PLAYLIST_WORKGROUP_SIZE.0 - 1) / PLAYLIST_WORKGROUP_SIZE.0,
        height.saturating_add(PLAYLIST_WORKGROUP_SIZE.1 - 1) / PLAYLIST_WORKGROUP_SIZE.1,
        PLAYLIST_WORKGROUP_SIZE.2,
    ))
}

pub fn write_playlist_frame_uniform(
    queue: &wgpu::Queue,
    frame_uniform: &wgpu::Buffer,
    uniform: &PlaylistFrameUniform,
) {
    queue.write_buffer(frame_uniform, 0, &uniform.to_bytes());
}

pub fn create_playlist_bind_group<'a>(
    device: &wgpu::Device,
    layout: &wgpu::BindGroupLayout,
    resources: PlaylistBindGroupResources<'a>,
) -> wgpu::BindGroup {
    device.create_bind_group(&wgpu::BindGroupDescriptor {
        label: Some("playlist-timeline-bind-group"),
        layout,
        entries: &[
            wgpu::BindGroupEntry {
                binding: 0,
                resource: resources.frame_uniform.as_entire_binding(),
            },
            wgpu::BindGroupEntry {
                binding: 1,
                resource: wgpu::BindingResource::TextureView(resources.artwork_texture),
            },
            wgpu::BindGroupEntry {
                binding: 2,
                resource: wgpu::BindingResource::TextureView(resources.glyph_atlas),
            },
            wgpu::BindGroupEntry {
                binding: 3,
                resource: wgpu::BindingResource::Sampler(resources.glyph_sampler),
            },
            wgpu::BindGroupEntry {
                binding: 4,
                resource: wgpu::BindingResource::TextureView(resources.loudness_texture),
            },
            wgpu::BindGroupEntry {
                binding: 5,
                resource: resources.spectrum_buffer.as_entire_binding(),
            },
            wgpu::BindGroupEntry {
                binding: 6,
                resource: wgpu::BindingResource::TextureView(resources.rgb_output),
            },
            wgpu::BindGroupEntry {
                binding: 7,
                resource: wgpu::BindingResource::TextureView(resources.text_overlay_texture),
            },
            wgpu::BindGroupEntry {
                binding: 8,
                resource: resources.layout_buffer.as_entire_binding(),
            },
            wgpu::BindGroupEntry {
                binding: 9,
                resource: resources.palette_buffer.as_entire_binding(),
            },
            wgpu::BindGroupEntry {
                binding: 10,
                resource: wgpu::BindingResource::TextureView(resources.base_texture),
            },
        ],
    })
}

pub fn encode_playlist_compute_pass(
    encoder: &mut wgpu::CommandEncoder,
    pipeline: &PlaylistComputePipeline,
    bind_group: &wgpu::BindGroup,
    width: u32,
    height: u32,
) -> Result<(), String> {
    let (workgroups_x, workgroups_y, workgroups_z) = playlist_dispatch_workgroups(width, height)?;
    let mut pass = encoder.begin_compute_pass(&wgpu::ComputePassDescriptor {
        label: Some("playlist-timeline-compute-pass"),
        timestamp_writes: None,
    });
    pass.set_pipeline(&pipeline.pipeline);
    pass.set_bind_group(0, bind_group, &[]);
    pass.dispatch_workgroups(workgroups_x, workgroups_y, workgroups_z);
    Ok(())
}

pub fn submit_playlist_frame<'a>(
    device: &wgpu::Device,
    queue: &wgpu::Queue,
    pipeline: &PlaylistComputePipeline,
    resources: PlaylistBindGroupResources<'a>,
    uniform: &PlaylistFrameUniform,
    width: u32,
    height: u32,
) -> Result<wgpu::SubmissionIndex, String> {
    write_playlist_frame_uniform(queue, resources.frame_uniform, uniform);
    let bind_group = create_playlist_bind_group(device, &pipeline.bind_group_layout, resources);
    let mut encoder = device.create_command_encoder(&wgpu::CommandEncoderDescriptor {
        label: Some("playlist-timeline-command-encoder"),
    });
    encode_playlist_compute_pass(&mut encoder, pipeline, &bind_group, width, height)?;
    Ok(queue.submit(Some(encoder.finish())))
}

pub const PLAYLIST_TIMELINE_WGSL: &str = r#"
struct FrameControl {
    frame_index: u32,
    sequence_lo: u32,
    sequence_hi: u32,
    pts_ns_lo: u32,
    pts_ns_hi: u32,
    progress_q16: u32,
    text_alpha_q16: u32,
    black_alpha_q16: u32,
    scroll_x_q16: i32,
    scroll_y_q16: i32,
    scroll_alpha_q16: u32,
    scroll_clip_x: i32,
    scroll_clip_y: i32,
    scroll_clip_w: i32,
    scroll_clip_h: i32,
    loudness_q16: u32,
    loudness_trend_q16: u32,
    spectrum_count: u32,
    waveform_count: u32,
    uniform_pad: u32,
    spectrum_q16: array<vec4<u32>, 16>,
    waveform_q16: array<vec4<u32>, 16>,
};

@group(0) @binding(0)
var<uniform> frame_control: FrameControl;
@group(0) @binding(1)
var artwork_texture: texture_2d<f32>;
@group(0) @binding(2)
var glyph_atlas: texture_2d<f32>;
@group(0) @binding(3)
var glyph_sampler: sampler;
@group(0) @binding(4)
var loudness_texture: texture_2d<f32>;
@group(0) @binding(5)
var<storage, read> spectrum_q16: array<u32>;
@group(0) @binding(6)
var rgb_output: texture_storage_2d<rgba8unorm, write>;
@group(0) @binding(7)
var text_overlay_texture: texture_2d<f32>;
@group(0) @binding(8)
var<storage, read> layout_rects: array<i32>;
@group(0) @binding(9)
var<storage, read> palette_values: array<u32>;
@group(0) @binding(10)
var base_texture: texture_2d<f32>;

fn palette_rgb(index: u32) -> vec3<f32> {
    let packed = palette_values[index];
    return vec3<f32>(
        f32(packed & 255u),
        f32((packed >> 8u) & 255u),
        f32((packed >> 16u) & 255u),
    ) / 255.0;
}

// Go's math.Round rounds half away from zero, while WGSL's round() rounds
// half to even. Use floor(x + 0.5) so half-integer pixels (e.g. barGap=6.5,
// marker radius=4.5) match the canonical CPU rasterizer exactly.
fn round_half_away(x: f32) -> f32 {
    return floor(x + 0.5);
}

fn inside_cpu_rounded_rect(pixel: vec2<i32>, rect_min: vec2<i32>, rect_max: vec2<i32>, radius: i32) -> bool {
    if any(pixel < rect_min) || any(pixel >= rect_max) {
        return false;
    }
    if radius <= 0 {
        return true;
    }
    if pixel.x < rect_min.x + radius && pixel.y < rect_min.y + radius {
        let d = pixel - vec2<i32>(rect_min.x + radius - 1, rect_min.y + radius - 1);
        return dot(d, d) <= radius * radius;
    }
    if pixel.x >= rect_max.x - radius && pixel.y < rect_min.y + radius {
        let d = pixel - vec2<i32>(rect_max.x - radius, rect_min.y + radius - 1);
        return dot(d, d) <= radius * radius;
    }
    if pixel.x < rect_min.x + radius && pixel.y >= rect_max.y - radius {
        let d = pixel - vec2<i32>(rect_min.x + radius - 1, rect_max.y - radius);
        return dot(d, d) <= radius * radius;
    }
    if pixel.x >= rect_max.x - radius && pixel.y >= rect_max.y - radius {
        let d = pixel - vec2<i32>(rect_max.x - radius, rect_max.y - radius);
        return dot(d, d) <= radius * radius;
    }
    return true;
}

@compute @workgroup_size(8, 8, 1)
fn render_playlist_timeline(@builtin(global_invocation_id) id: vec3<u32>) {
    let dimensions = textureDimensions(rgb_output);
    if (id.x >= dimensions.x || id.y >= dimensions.y) {
        return;
    }
    let pixel = vec2<i32>(id.xy);
    let uv = (vec2<f32>(id.xy) + vec2<f32>(0.5)) / vec2<f32>(dimensions);
    let clip_min = vec2<i32>(frame_control.scroll_clip_x, frame_control.scroll_clip_y);
    let clip_max = clip_min + vec2<i32>(frame_control.scroll_clip_w, frame_control.scroll_clip_h);
    let inside_clip = all(pixel >= clip_min) && all(pixel < clip_max);
    let scroll = vec2<f32>(f32(frame_control.scroll_x_q16), f32(frame_control.scroll_y_q16)) / 65536.0;
    let artwork_min = vec2<f32>(f32(layout_rects[0]), f32(layout_rects[1])) / vec2<f32>(1280.0, 720.0);
    let artwork_size = max(vec2<f32>(f32(layout_rects[2]), f32(layout_rects[3])) / vec2<f32>(1280.0, 720.0), vec2<f32>(0.0001));
    let inside_artwork = all(uv >= artwork_min) && all(uv < artwork_min + artwork_size);
    let artwork_uv = clamp((uv - artwork_min) / artwork_size + scroll / artwork_size, vec2<f32>(0.0), vec2<f32>(1.0));
    let artwork_sample = textureSampleLevel(artwork_texture, glyph_sampler, artwork_uv, 0.0).rgb;
    let artwork = select(vec3<f32>(0.0), artwork_sample, inside_artwork);
    // The CPU reference copies the immutable base raster pixel-for-pixel. Do
    // not filter or resample it through the shared artwork/text sampler: use
    // the invocation's exact texel so the static base contract remains
    // numerically identical before dynamic layers are composited.
    let base_dimensions = textureDimensions(base_texture);
    let base_pixel = min(pixel, vec2<i32>(base_dimensions) - vec2<i32>(1));
    let base_sample = textureLoad(base_texture, base_pixel, 0).rgb;
    // Text is supplied as a CPU-rasterized screen overlay. The atlas remains a
    // required immutable asset for contract compatibility, but is not sampled
    // as if it were a full-screen text canvas.
    let glyph = 0.0;
    let loudness_min = vec2<f32>(f32(layout_rects[20]), f32(layout_rects[21])) / vec2<f32>(1280.0, 720.0);
    let loudness_size = max(vec2<f32>(f32(layout_rects[22]), f32(layout_rects[23])) / vec2<f32>(1280.0, 720.0), vec2<f32>(0.0001));
    let loudness_uv_x = clamp((uv.x - loudness_min.x) / loudness_size.x, 0.0, 1.0);
    let loudness_texture_value = textureSampleLevel(loudness_texture, glyph_sampler, vec2<f32>(loudness_uv_x, 0.5), 0.0).r;
    let loudness_dimensions = textureDimensions(loudness_texture);
    let loudness_pixel = min(pixel, vec2<i32>(loudness_dimensions) - vec2<i32>(1));
    let loudness_layer_sample = textureLoad(loudness_texture, loudness_pixel, 0);
    let inside_loudness = all(uv >= loudness_min) && all(uv < loudness_min + loudness_size);
    let loudness_envelope_y = loudness_min.y + loudness_size.y * (1.0 - loudness_texture_value);
    let loudness_envelope_line = inside_loudness && abs(uv.y - loudness_envelope_y) <= max(1.0 / f32(dimensions.y), 0.0015);
    let guide0_y = loudness_min.y + loudness_size.y * (1.0 - f32(palette_values[6u]) / 65535.0);
    let guide1_y = loudness_min.y + loudness_size.y * (1.0 - f32(palette_values[7u]) / 65535.0);
    let guide2_y = loudness_min.y + loudness_size.y * (1.0 - f32(palette_values[8u]) / 65535.0);
    let guide3_y = loudness_min.y + loudness_size.y * (1.0 - f32(palette_values[9u]) / 65535.0);
    let loudness_guides = inside_loudness && (
        abs(uv.y - guide0_y) <= max(1.0 / f32(dimensions.y), 0.0015) ||
        abs(uv.y - guide1_y) <= max(1.0 / f32(dimensions.y), 0.0015) ||
        abs(uv.y - guide2_y) <= max(1.0 / f32(dimensions.y), 0.0015) ||
        abs(uv.y - guide3_y) <= max(1.0 / f32(dimensions.y), 0.0015)
    );
    let spectrum_min = vec2<f32>(f32(layout_rects[16]), f32(layout_rects[17])) / vec2<f32>(1280.0, 720.0);
    let spectrum_size = max(vec2<f32>(f32(layout_rects[18]), f32(layout_rects[19])) / vec2<f32>(1280.0, 720.0), vec2<f32>(0.0001));
    let inside_spectrum = all(uv >= spectrum_min) && all(uv < spectrum_min + spectrum_size);
    let canvas_scale = f32(dimensions.x) / 1280.0;
    let spectrum_px = vec2<f32>(uv * vec2<f32>(dimensions));
    let spectrum_x_px = f32(layout_rects[16]) * canvas_scale;
    let spectrum_y_px = f32(layout_rects[17]) * canvas_scale;
    let spectrum_w_px = f32(layout_rects[18]) * canvas_scale;
    let spectrum_h_px = f32(layout_rects[19]) * canvas_scale;
    let bar_width_px = max(round_half_away(18.0 * canvas_scale), 1.0);
    let bar_gap_px = round_half_away(13.0 * canvas_scale);
    let first_bar_x_px = spectrum_x_px + round_half_away(11.0 * canvas_scale);
    let bar_bottom_px = spectrum_y_px + spectrum_h_px;
    let max_bar_h_px = spectrum_h_px - round_half_away(16.0 * canvas_scale);
    let min_bar_h_px = max(round_half_away(4.0 * canvas_scale), 1.0);
    let fade_px = max(round_half_away(10.0 * canvas_scale), 1.0);
    let bar_step_px = bar_width_px + bar_gap_px;
    let bar_position = (spectrum_px.x - first_bar_x_px) / bar_step_px;
    let bar_index = i32(floor(bar_position));
    let bar_local_x = spectrum_px.x - (first_bar_x_px + f32(bar_index) * bar_step_px);
    let valid_bar = bar_index >= 0 && bar_index < 24 && bar_local_x >= 0.0 && bar_local_x < bar_width_px;
    let bar_q16 = select(0u, frame_control.spectrum_q16[u32(bar_index) / 4u][u32(bar_index) % 4u], valid_bar);
    // CPU computes barH as an integer: minBarH + int(value * range).
    // Quantize the GPU height before deriving the covered pixel rows so the
    // top edge follows the canonical rasterizer rather than float boundary
    // comparisons at half-pixel positions.
    let bar_height_px = min_bar_h_px + floor((f32(bar_q16) / 65535.0) * max(max_bar_h_px - min_bar_h_px, 0.0));
    let bar_top_px = bar_bottom_px - bar_height_px;
    let inside_bar = valid_bar && spectrum_px.y >= bar_top_px && spectrum_px.y < bar_bottom_px;
    let bottom_distance = bar_bottom_px - 1.0 - spectrum_px.y;
    // CPU shortens the fade to the bar height for bars shorter than fadePx,
    // so the gradient spans the whole short bar instead of leaving it dim.
    let eff_fade_px = min(fade_px, bar_height_px);
    let bar_fade = 0.82 * clamp(bottom_distance / max(eff_fade_px - 1.0, 1.0), 0.0, 1.0);
    let bar_fade_alpha = select(0.0, bar_fade, eff_fade_px != 1.0);
    let bar_alpha = select(0.0, select(0.82, bar_fade_alpha, bottom_distance < eff_fade_px), inside_bar);
    let loudness = max(f32(frame_control.loudness_q16) / 65535.0, loudness_texture_value);
    let alpha = f32(frame_control.scroll_alpha_q16) / 65535.0;
    let text_alpha = f32(frame_control.text_alpha_q16) / 65535.0;
    let black = f32(frame_control.black_alpha_q16) / 65535.0;
    let palette_background = palette_rgb(2u);
    let palette_accent = palette_rgb(1u);
    let base = base_sample;
    let dynamic = mix(base, palette_accent, bar_alpha) * (1.0 - black);
    // LoudnessTexture is the CPU canonical RGBA8 layer. Its rendered RGB is
    // already coverage-weighted by the image.RGBA drawing path, so composite
    // the sampled texel as premultiplied source-over.
    let dynamic_with_loudness = loudness_layer_sample.rgb + dynamic * (1.0 - loudness_layer_sample.a);
    let progress_min = vec2<f32>(f32(layout_rects[24]), f32(layout_rects[25])) / vec2<f32>(1280.0, 720.0);
    let progress_size = max(vec2<f32>(f32(layout_rects[26]), f32(layout_rects[27])) / vec2<f32>(1280.0, 720.0), vec2<f32>(0.0001));
    let inside_progress = all(uv >= progress_min) && all(uv < progress_min + progress_size);
    let progress_ratio = f32(frame_control.progress_q16) / 65535.0;
    let progress_x = progress_min.x + progress_size.x * progress_ratio;
    let progress_px = vec2<f32>(uv * vec2<f32>(dimensions));
    let progress_min_px = progress_min * vec2<f32>(dimensions);
    let progress_size_px = progress_size * vec2<f32>(dimensions);
    let progress_center_y_px = progress_min_px.y + progress_size_px.y * 0.5;
    let progress_min_i = vec2<i32>(round(progress_min_px));
    let progress_max_i = progress_min_i + vec2<i32>(round(progress_size_px));
    let progress_radius_i = max(i32(round(progress_size_px.y * 0.5)), 1);
    let progress_track = inside_cpu_rounded_rect(pixel, progress_min_i, progress_max_i, progress_radius_i);
    let marker_radius_px = max(round_half_away(9.0 * progress_size_px.x / 1000.0), 1.0);
    // CPU drawCircle rasterizes an integer-centered circle. progress_px is a
    // pixel center (x + 0.5), so center on the integer column + 0.5 to match
    // the canonical integer coordinate system.
    let marker_center_x = round_half_away(progress_x * f32(dimensions.x)) + 0.5;
    let marker_center_y = round_half_away(progress_center_y_px) + 0.5;
    let marker_dx = progress_px.x - marker_center_x;
    let marker_dy = progress_px.y - marker_center_y;
    // CPU drawCircle is applied after the rail without clipping to the rail
    // rectangle. At progress 0 or 1 the marker therefore extends beyond the
    // rail endpoints; do not gate the circle by inside_progress.
    let progress_marker = marker_dx * marker_dx + marker_dy * marker_dy <= marker_radius_px * marker_radius_px;
    // CPU draws the rail with SetRGBA (opaque replacement on the opaque base),
    // then draws the marker with blendPixel at 88% source-over. Keep those two
    // operations separate instead of treating both shapes as one mix mask.
    let progress_base = select(dynamic_with_loudness, palette_accent, progress_track);
    let marker_alpha = select(0.0, 0.88, progress_marker);
    let dynamic_with_progress = mix(progress_base, palette_accent, marker_alpha);
    // Text is a screen-sized CPU raster. Match the CPU reference's exact
    // pixel source-over; linear filtering would interpolate neighboring glyph
    // coverage and alter edge pixels even when the overlay dimensions match.
    let text_dimensions = textureDimensions(text_overlay_texture);
    let text_pixel = min(pixel, vec2<i32>(text_dimensions) - vec2<i32>(1));
    let text_sample = textureLoad(text_overlay_texture, text_pixel, 0);
    let text_coverage = clamp(text_sample.a * text_alpha, 0.0, 1.0);
    // Text payloads are CPU-rasterized premultiplied RGBA. Composite them after
    // the timeline-driven layers so the GPU does not reinterpret fonts or runs.
    let color = text_sample.rgb * text_alpha + dynamic_with_progress * (1.0 - text_coverage);
    textureStore(rgb_output, pixel, vec4<f32>(color, 1.0));
}
"#;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn playlist_descriptor_is_gpu_owned_and_pcm_free() {
        let descriptor = PLAYLIST_TIMELINE_SHADER_MODULE.descriptor();
        assert!(validate_playlist_descriptor(descriptor).is_ok());
        assert!(!PLAYLIST_TIMELINE_WGSL.contains("pcm_input"));
        assert!(PLAYLIST_TIMELINE_WGSL.contains("textureSampleLevel"));
        assert!(PLAYLIST_TIMELINE_WGSL.contains("scroll_clip_x"));
        assert!(PLAYLIST_TIMELINE_WGSL.contains("text_alpha_q16"));
        assert!(PLAYLIST_TIMELINE_WGSL.contains("progress_q16"));
        assert!(PLAYLIST_TIMELINE_WGSL
            .contains("let uv = (vec2<f32>(id.xy) + vec2<f32>(0.5)) / vec2<f32>(dimensions);"));
        assert!(PLAYLIST_TIMELINE_WGSL.contains("progress_track"));
        assert!(PLAYLIST_TIMELINE_WGSL.contains("progress_marker"));
        assert!(PLAYLIST_TIMELINE_WGSL.contains("palette_values"));
        assert!(!descriptor.production_ready);
    }

    #[test]
    fn playlist_wgsl_parses_and_validates_with_naga() {
        let module = naga::front::wgsl::parse_str(PLAYLIST_TIMELINE_WGSL)
            .expect("playlist timeline WGSL must parse");
        let mut validator = naga::valid::Validator::new(
            naga::valid::ValidationFlags::all(),
            naga::valid::Capabilities::empty(),
        );
        validator
            .validate(&module)
            .expect("playlist timeline WGSL must validate");
    }

    #[test]
    fn playlist_dispatch_is_bounded_and_ceil_divides_dimensions() {
        assert_eq!(playlist_dispatch_workgroups(1, 1).unwrap(), (1, 1, 1));
        assert_eq!(playlist_dispatch_workgroups(8, 8).unwrap(), (1, 1, 1));
        assert_eq!(playlist_dispatch_workgroups(9, 17).unwrap(), (2, 3, 1));
        assert!(playlist_dispatch_workgroups(0, 1).is_err());
        assert!(playlist_dispatch_workgroups(1, 0).is_err());
    }

    #[test]
    fn playlist_static_texture_payload_requires_valid_stride_and_rows() {
        assert!(validate_playlist_texture_payload(2, 2, 8, 16).is_ok());
        assert!(validate_playlist_texture_payload(2, 2, 12, 24).is_ok());
        assert!(validate_playlist_texture_payload(2, 2, 7, 14).is_err());
        assert!(validate_playlist_texture_payload(2, 2, 8, 15).is_err());
        assert!(validate_playlist_texture_payload(0, 2, 0, 0).is_err());
    }

    #[test]
    fn frame_uniform_contains_resolved_timeline_values_without_pcm() {
        let frame = ResolvedFrameControl {
            sequence: 17,
            frame_index: 3,
            pts_ns: 123_456,
            feature_index: 2,
            spectrum_q16: vec![11, 22],
            waveform_q16: vec![33],
            progress_q16: 44,
            text_alpha_q16: 55,
            black_alpha_q16: 66,
            scroll_x_q16: -77,
            scroll_y_q16: 88,
            scroll_alpha_q16: 99,
            scroll_clip: Default::default(),
            loudness_q16: 111,
            loudness_trend_q16: 222,
        };
        let uniform = PlaylistFrameUniform::from_resolved(&frame);
        assert_eq!(uniform.frame_index, 3);
        assert_eq!(uniform.spectrum_q16[1], 22);
        assert_eq!(uniform.scroll_x_q16, -77);
        assert_eq!(uniform.scroll_clip_x, 0);
        assert_eq!(uniform.scroll_clip_w, 0);
        assert_eq!(uniform.black_alpha_q16, 66);
        assert_eq!(uniform.to_bytes().len(), PLAYLIST_FRAME_UNIFORM_BYTES);
    }
}
