#![allow(dead_code)]

use super::contracts::MAX_DIMENSION;
use super::music_v2_glyph_atlas::{atlas_index, GlyphAtlas};
use super::music_v2_shader_module::{
    validate_descriptor, ShaderBindingKind, ShaderModule, ShaderModuleDescriptor,
};
use std::num::NonZeroU32;
use wgpu::util::DeviceExt;

/// Draft host-side bound for one resident PCM window. This is deliberately
/// independent from the eventual ring-buffer size and can be revised without
/// changing the shader module interface.
pub const MAX_SHADER_PCM_SAMPLES: u32 = 1_048_576;
pub const MAX_SHADER_WAVEFORM_SAMPLES: usize = 4096;
pub const WAVEFORM_FLAG_PRESENT: u32 = 1;
pub const WAVEFORM_FLAG_MINMAX: u32 = 2;
pub const WAVEFORM_FLAG_SIGNED: u32 = 4;
pub const CPU_PARITY_SPECTRUM_BANDS: usize = 24;
pub const CPU_PARITY_LOUDNESS_SAMPLES: usize = 1000;
pub const CPU_PARITY_TEXT_LINES: usize = 4;
pub const CPU_PARITY_TEXT_CHARS: usize = 32;
pub const SHADER_SCENE_UNIFORM_BYTES: usize = 4864;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ShaderSceneData {
    /// [primary, accent, background, overlay], encoded as RGBA bytes.
    pub palette: [[u8; 4]; 4],
    /// [artwork, title, artist, album, spectrum, loudness, progress, time].
    pub rects: [[i32; 4]; 8],
    pub spectrum_q16: [u16; CPU_PARITY_SPECTRUM_BANDS],
    pub waveform_q16: Vec<u16>,
    pub waveform_minmax: bool,
    pub loudness_q16: Vec<u16>,
    pub progress_q16: u32,
    pub fade_in_q16: u32,
    pub fade_out_q16: u32,
    pub fps: u32,
    pub total_frames: u32,
    pub text_codepoints: [[u32; CPU_PARITY_TEXT_CHARS]; CPU_PARITY_TEXT_LINES],
    pub text_lengths: [u32; CPU_PARITY_TEXT_LINES],
}

impl ShaderSceneData {
    fn pack_text_line(value: &str) -> ([u32; CPU_PARITY_TEXT_CHARS], u32) {
        let mut codepoints = [0u32; CPU_PARITY_TEXT_CHARS];
        let mut length = 0usize;
        for character in value.chars().take(CPU_PARITY_TEXT_CHARS) {
            codepoints[length] = if atlas_index(character).is_some() {
                character as u32
            } else {
                b'?' as u32
            };
            length += 1;
        }
        (codepoints, length as u32)
    }

    pub fn cpu_parity_default(
        width: u32,
        height: u32,
        frame_index: u32,
        total_frames: u32,
        fps: u32,
    ) -> Self {
        let sx = width as f32 / 1280.0;
        let sy = height as f32 / 720.0;
        let scale_x = |value: i32| (value as f32 * sx).round() as i32;
        let scale_y = |value: i32| (value as f32 * sy).round() as i32;
        let rect =
            |x: i32, y: i32, w: i32, h: i32| [scale_x(x), scale_y(y), scale_x(w), scale_y(h)];

        let phase = if total_frames == 0 {
            0.0
        } else {
            frame_index as f32 / total_frames as f32
        };
        let mut spectrum_q16 = [0u16; CPU_PARITY_SPECTRUM_BANDS];
        for (band, value) in spectrum_q16.iter_mut().enumerate() {
            let band_phase = phase * std::f32::consts::TAU * 1.35 + band as f32 * 0.37;
            let envelope = 0.18 + 0.72 * (band_phase.sin() * 0.5 + 0.5);
            let tilt = 1.0 - band as f32 / CPU_PARITY_SPECTRUM_BANDS as f32 * 0.28;
            *value = (envelope * tilt * u16::MAX as f32).round() as u16;
        }

        let mut loudness_q16 = Vec::with_capacity(CPU_PARITY_LOUDNESS_SAMPLES);
        for sample in 0..CPU_PARITY_LOUDNESS_SAMPLES {
            let u = sample as f32 / (CPU_PARITY_LOUDNESS_SAMPLES - 1) as f32;
            let value = 0.22
                + 0.48 * (u * std::f32::consts::TAU * 1.7 + phase * 2.0).sin().abs()
                + 0.18 * (u * std::f32::consts::TAU * 4.0 + phase).sin().abs();
            loudness_q16.push((value.clamp(0.0, 1.0) * u16::MAX as f32).round() as u16);
        }

        let (title, title_len) = Self::pack_text_line("IMAGEPAD");
        let (artist, artist_len) = Self::pack_text_line("GPU PARITY");
        let (album, album_len) = Self::pack_text_line("CANONICAL SCENE");
        let (time, time_len) = Self::pack_text_line("0:00");

        Self {
            palette: [
                [255, 255, 255, 255],
                [58, 134, 255, 255],
                [23, 59, 87, 255],
                [0, 0, 0, 92],
            ],
            rects: [
                rect(96, 152, 288, 288),
                rect(432, 152, 752, 58),
                rect(432, 224, 752, 34),
                rect(432, 264, 752, 30),
                rect(432, 320, 752, 168),
                rect(64, 548, 1000, 80),
                rect(64, 650, 1000, 8),
                rect(1088, 632, 128, 32),
            ],
            spectrum_q16,
            waveform_q16: Vec::new(),
            waveform_minmax: false,
            loudness_q16,
            progress_q16: (phase.clamp(0.0, 1.0) * u16::MAX as f32).round() as u32,
            fade_in_q16: u32::from(u16::MAX),
            fade_out_q16: u32::from(u16::MAX),
            fps: fps.max(1),
            total_frames,
            text_codepoints: [title, artist, album, time],
            text_lengths: [title_len, artist_len, album_len, time_len],
        }
    }

    pub fn with_text_lines(mut self, title: &str, artist: &str, album: &str, time: &str) -> Self {
        let (title_codepoints, title_len) = Self::pack_text_line(title);
        let (artist_codepoints, artist_len) = Self::pack_text_line(artist);
        let (album_codepoints, album_len) = Self::pack_text_line(album);
        let (time_codepoints, time_len) = Self::pack_text_line(time);
        self.text_codepoints = [
            title_codepoints,
            artist_codepoints,
            album_codepoints,
            time_codepoints,
        ];
        self.text_lengths = [title_len, artist_len, album_len, time_len];
        self
    }

    pub fn with_waveform_q16(mut self, waveform_q16: Vec<u16>, minmax: bool) -> Self {
        self.waveform_q16 = waveform_q16;
        self.waveform_minmax = minmax;
        self
    }

    fn validate(&self) -> Result<(), String> {
        if self.waveform_q16.len() > MAX_SHADER_WAVEFORM_SAMPLES {
            return Err("shader waveform payload exceeds the bounded sample count".into());
        }
        if self.waveform_minmax && self.waveform_q16.len() % 2 != 0 {
            return Err("shader min/max waveform payload must contain pairs".into());
        }
        if self.loudness_q16.len() != CPU_PARITY_LOUDNESS_SAMPLES {
            return Err("shader loudness payload must contain 1000 samples".into());
        }
        Ok(())
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ShaderFrameRequest {
    pub width: u32,
    pub height: u32,
    pub frame_index: u32,
    pub pcm_sample_count: u32,
    pub pcm_sample_offset: u32,
    pub scene: ShaderSceneData,
}

impl ShaderFrameRequest {
    pub fn validate(&self) -> Result<(), String> {
        if self.width == 0
            || self.height == 0
            || self.width > MAX_DIMENSION
            || self.height > MAX_DIMENSION
        {
            return Err("shader frame dimensions are outside the supported range".into());
        }
        if self.pcm_sample_count == 0 || self.pcm_sample_count > MAX_SHADER_PCM_SAMPLES {
            return Err("shader PCM window is outside the supported range".into());
        }
        if self.pcm_sample_offset >= self.pcm_sample_count {
            return Err("shader PCM sample offset is outside the resident window".into());
        }
        self.scene.validate()?;
        Ok(())
    }
}

/// This is the fixed uniform layout consumed by the CPU-parity Draft WGSL.
#[repr(C)]
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ShaderSceneUniform {
    pub width: u32,
    pub height: u32,
    pub frame_index: u32,
    pub pcm_sample_count: u32,
    pub pcm_sample_offset: u32,
    pub fps: u32,
    pub total_frames: u32,
    pub waveform_flags: u32,
    pub progress_q16: u32,
    pub fade_in_q16: u32,
    pub fade_out_q16: u32,
    pub title_len: u32,
    pub artist_len: u32,
    pub album_len: u32,
    pub time_len: u32,
    pub waveform_columns: u32,
    pub palette: [[u32; 4]; 4],
    pub rects: [[i32; 4]; 8],
    pub spectrum: [[u32; 4]; 6],
    pub loudness: [[u32; 4]; 250],
    pub text: [[u32; 4]; 32],
}

impl ShaderSceneUniform {
    pub fn from_request(request: &ShaderFrameRequest) -> Self {
        let mut spectrum = [[0u32; 4]; 6];
        for (index, value) in request.scene.spectrum_q16.iter().enumerate() {
            spectrum[index / 4][index % 4] = u32::from(*value);
        }
        let waveform_len = request.scene.waveform_q16.len();
        let waveform_columns = if request.scene.waveform_minmax {
            (waveform_len / 2) as u32
        } else {
            waveform_len as u32
        };
        let waveform_flags = if waveform_len == 0 {
            0
        } else {
            WAVEFORM_FLAG_PRESENT
                | if request.scene.waveform_minmax {
                    WAVEFORM_FLAG_MINMAX | WAVEFORM_FLAG_SIGNED
                } else {
                    0
                }
        };
        let mut loudness = [[0u32; 4]; 250];
        for (index, value) in request.scene.loudness_q16.iter().enumerate() {
            loudness[index / 4][index % 4] = u32::from(*value);
        }
        let mut text = [[0u32; 4]; 32];
        for (line, codepoints) in request.scene.text_codepoints.iter().enumerate() {
            for (index, value) in codepoints.iter().enumerate() {
                let row = line * 8 + index / 4;
                text[row][index % 4] = *value;
            }
        }
        Self {
            width: request.width,
            height: request.height,
            frame_index: request.frame_index,
            pcm_sample_count: request.pcm_sample_count,
            pcm_sample_offset: request.pcm_sample_offset,
            fps: request.scene.fps,
            total_frames: request.scene.total_frames,
            waveform_flags,
            progress_q16: request.scene.progress_q16,
            fade_in_q16: request.scene.fade_in_q16,
            fade_out_q16: request.scene.fade_out_q16,
            title_len: request.scene.text_lengths[0],
            artist_len: request.scene.text_lengths[1],
            album_len: request.scene.text_lengths[2],
            time_len: request.scene.text_lengths[3],
            waveform_columns,
            palette: request.scene.palette.map(|color| color.map(u32::from)),
            rects: request.scene.rects,
            spectrum,
            loudness,
            text,
        }
    }

    pub fn to_bytes(&self) -> Vec<u8> {
        fn push_u32(bytes: &mut Vec<u8>, word: u32) {
            bytes.extend_from_slice(&word.to_ne_bytes());
        }
        fn push_i32(bytes: &mut Vec<u8>, word: i32) {
            bytes.extend_from_slice(&word.to_ne_bytes());
        }

        let mut bytes = Vec::with_capacity(SHADER_SCENE_UNIFORM_BYTES);
        for word in [
            self.width,
            self.height,
            self.frame_index,
            self.pcm_sample_count,
            self.pcm_sample_offset,
            self.fps,
            self.total_frames,
            self.waveform_flags,
            self.progress_q16,
            self.fade_in_q16,
            self.fade_out_q16,
            self.title_len,
            self.artist_len,
            self.album_len,
            self.time_len,
            self.waveform_columns,
        ] {
            push_u32(&mut bytes, word);
        }
        for color in self.palette {
            for word in color {
                push_u32(&mut bytes, word);
            }
        }
        for rect in self.rects {
            for word in rect {
                push_i32(&mut bytes, word);
            }
        }
        for row in self.spectrum {
            for word in row {
                push_u32(&mut bytes, word);
            }
        }
        for row in self.loudness {
            for word in row {
                push_u32(&mut bytes, word);
            }
        }
        for row in self.text {
            for word in row {
                push_u32(&mut bytes, word);
            }
        }
        bytes
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ShaderFramePlan {
    pub descriptor: ShaderModuleDescriptor,
    pub uniform: ShaderSceneUniform,
    pub uniform_bytes: Vec<u8>,
    pub dispatch_workgroups: (u32, u32, u32),
    /// False for Draft 1 until the shader itself passes visual/numerical gates.
    pub executable: bool,
}

pub fn prepare_frame_plan(
    module: &dyn ShaderModule,
    request: ShaderFrameRequest,
) -> Result<ShaderFramePlan, String> {
    let descriptor = module.descriptor();
    validate_descriptor(descriptor).map_err(|error| format!("invalid shader module: {error}"))?;
    request.validate()?;
    let uniform = ShaderSceneUniform::from_request(&request);
    let uniform_bytes = uniform.to_bytes();
    let (group_width, group_height, group_depth) = descriptor.workgroup_size;
    let dispatch_workgroups = (
        request.width.saturating_add(group_width - 1) / group_width,
        request.height.saturating_add(group_height - 1) / group_height,
        group_depth,
    );
    Ok(ShaderFramePlan {
        descriptor,
        uniform,
        uniform_bytes,
        dispatch_workgroups,
        executable: descriptor.production_ready,
    })
}

pub struct ShaderGpuResources<'a> {
    pub pcm_buffer: &'a wgpu::Buffer,
    pub scene_uniform: &'a wgpu::Buffer,
    pub waveform_buffer: &'a wgpu::Buffer,
    pub glyph_atlas: &'a wgpu::TextureView,
    pub glyph_metrics: &'a wgpu::Buffer,
    pub glyph_sampler: &'a wgpu::Sampler,
    pub artwork_texture: &'a wgpu::TextureView,
    pub luma_output: &'a wgpu::TextureView,
    pub chroma_output: &'a wgpu::TextureView,
    pub rgb_output: &'a wgpu::TextureView,
}

pub struct ShaderOutputTextures {
    pub luma: wgpu::Texture,
    pub chroma: wgpu::Texture,
    pub rgb: wgpu::Texture,
}

impl ShaderOutputTextures {
    pub fn luma_view(&self) -> wgpu::TextureView {
        self.luma
            .create_view(&wgpu::TextureViewDescriptor::default())
    }

    pub fn chroma_view(&self) -> wgpu::TextureView {
        self.chroma
            .create_view(&wgpu::TextureViewDescriptor::default())
    }

    pub fn rgb_view(&self) -> wgpu::TextureView {
        self.rgb
            .create_view(&wgpu::TextureViewDescriptor::default())
    }
}

pub fn shader_output_dimensions(
    width: u32,
    height: u32,
) -> Result<((u32, u32), (u32, u32)), String> {
    if width == 0 || height == 0 || width > MAX_DIMENSION || height > MAX_DIMENSION {
        return Err("shader output dimensions are outside the supported range".into());
    }
    Ok(((width, height), ((width + 1) / 2, (height + 1) / 2)))
}

pub fn create_pcm_storage_buffer(
    device: &wgpu::Device,
    samples: &[f32],
) -> Result<wgpu::Buffer, String> {
    if samples.is_empty() || samples.len() > MAX_SHADER_PCM_SAMPLES as usize {
        return Err("shader PCM buffer is outside the supported range".into());
    }
    Ok(
        device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
            label: Some("music-v2-shader-pcm"),
            contents: bytemuck::cast_slice(samples),
            usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_DST,
        }),
    )
}

pub fn create_waveform_storage_buffer(device: &wgpu::Device, samples: &[u16]) -> wgpu::Buffer {
    let words: Vec<u32> = if samples.is_empty() {
        vec![0]
    } else {
        samples.iter().copied().map(u32::from).collect()
    };
    device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
        label: Some("music-v2-shader-waveform-q16"),
        contents: bytemuck::cast_slice(&words),
        usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_DST,
    })
}

pub fn create_glyph_metrics_buffer(device: &wgpu::Device, atlas: &GlyphAtlas) -> wgpu::Buffer {
    let mut bytes = Vec::with_capacity(atlas.advances_q16.len() * std::mem::size_of::<u32>());
    for advance in &atlas.advances_q16 {
        bytes.extend_from_slice(&advance.to_ne_bytes());
    }
    device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
        label: Some("music-v2-glyph-metrics"),
        contents: &bytes,
        usage: wgpu::BufferUsages::STORAGE,
    })
}

pub fn create_scene_uniform_buffer(
    device: &wgpu::Device,
    uniform: &ShaderSceneUniform,
) -> wgpu::Buffer {
    device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
        label: Some("music-v2-shader-scene"),
        contents: &uniform.to_bytes(),
        usage: wgpu::BufferUsages::UNIFORM | wgpu::BufferUsages::COPY_DST,
    })
}

pub fn create_glyph_atlas_texture(
    device: &wgpu::Device,
    queue: &wgpu::Queue,
    atlas: &GlyphAtlas,
) -> wgpu::Texture {
    let texture = device.create_texture(&wgpu::TextureDescriptor {
        label: Some("music-v2-glyph-atlas"),
        size: wgpu::Extent3d {
            width: atlas.width,
            height: atlas.height,
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
        &atlas.pixels,
        wgpu::ImageDataLayout {
            offset: 0,
            bytes_per_row: Some(NonZeroU32::new(atlas.width * 4).unwrap().into()),
            rows_per_image: Some(NonZeroU32::new(atlas.height).unwrap().into()),
        },
        wgpu::Extent3d {
            width: atlas.width,
            height: atlas.height,
            depth_or_array_layers: 1,
        },
    );
    texture
}

pub fn create_artwork_texture(
    device: &wgpu::Device,
    queue: &wgpu::Queue,
    pixels: &[u8],
    width: u32,
    height: u32,
) -> Result<wgpu::Texture, String> {
    if width == 0 || height == 0 || pixels.len() != (width * height * 4) as usize {
        return Err("artwork RGBA payload dimensions are invalid".into());
    }
    let texture = device.create_texture(&wgpu::TextureDescriptor {
        label: Some("music-v2-artwork"),
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
        pixels,
        wgpu::ImageDataLayout {
            offset: 0,
            bytes_per_row: Some(NonZeroU32::new(width * 4).unwrap().into()),
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

pub fn create_output_textures(
    device: &wgpu::Device,
    width: u32,
    height: u32,
) -> Result<ShaderOutputTextures, String> {
    let ((luma_width, luma_height), (chroma_width, chroma_height)) =
        shader_output_dimensions(width, height)?;
    let usage = wgpu::TextureUsages::STORAGE_BINDING | wgpu::TextureUsages::TEXTURE_BINDING;
    let luma = device.create_texture(&wgpu::TextureDescriptor {
        label: Some("music-v2-shader-luma"),
        size: wgpu::Extent3d {
            width: luma_width,
            height: luma_height,
            depth_or_array_layers: 1,
        },
        mip_level_count: 1,
        sample_count: 1,
        dimension: wgpu::TextureDimension::D2,
        format: wgpu::TextureFormat::Rgba8Unorm,
        usage,
        view_formats: &[],
    });
    let chroma = device.create_texture(&wgpu::TextureDescriptor {
        label: Some("music-v2-shader-chroma"),
        size: wgpu::Extent3d {
            width: chroma_width,
            height: chroma_height,
            depth_or_array_layers: 1,
        },
        mip_level_count: 1,
        sample_count: 1,
        dimension: wgpu::TextureDimension::D2,
        format: wgpu::TextureFormat::Rgba8Unorm,
        usage,
        view_formats: &[],
    });
    let rgb = device.create_texture(&wgpu::TextureDescriptor {
        label: Some("music-v2-shader-rgb"),
        size: wgpu::Extent3d {
            width: luma_width,
            height: luma_height,
            depth_or_array_layers: 1,
        },
        mip_level_count: 1,
        sample_count: 1,
        dimension: wgpu::TextureDimension::D2,
        format: wgpu::TextureFormat::Rgba8Unorm,
        usage,
        view_formats: &[],
    });
    Ok(ShaderOutputTextures { luma, chroma, rgb })
}

pub fn create_shader_module(
    device: &wgpu::Device,
    module: &dyn ShaderModule,
) -> Result<wgpu::ShaderModule, String> {
    let descriptor = module.descriptor();
    validate_descriptor(descriptor).map_err(|error| format!("invalid shader module: {error}"))?;
    Ok(device.create_shader_module(wgpu::ShaderModuleDescriptor {
        label: Some("music-v2-shader-module"),
        source: wgpu::ShaderSource::Wgsl(module.wgsl().into()),
    }))
}

pub fn create_bind_group_layout(
    device: &wgpu::Device,
    descriptor: ShaderModuleDescriptor,
) -> Result<wgpu::BindGroupLayout, String> {
    validate_descriptor(descriptor).map_err(|error| format!("invalid shader module: {error}"))?;
    let entries = descriptor
        .bindings
        .iter()
        .map(|binding| {
            let ty = match binding.kind {
                ShaderBindingKind::StorageRead => wgpu::BindingType::Buffer {
                    ty: wgpu::BufferBindingType::Storage { read_only: true },
                    has_dynamic_offset: false,
                    min_binding_size: None,
                },
                ShaderBindingKind::Uniform => wgpu::BindingType::Buffer {
                    ty: wgpu::BufferBindingType::Uniform,
                    has_dynamic_offset: false,
                    min_binding_size: None,
                },
                ShaderBindingKind::SampledTextureRgba8 => wgpu::BindingType::Texture {
                    sample_type: wgpu::TextureSampleType::Float { filterable: true },
                    view_dimension: wgpu::TextureViewDimension::D2,
                    multisampled: false,
                },
                ShaderBindingKind::Sampler => {
                    wgpu::BindingType::Sampler(wgpu::SamplerBindingType::Filtering)
                }
                ShaderBindingKind::StorageTextureRgba8
                | ShaderBindingKind::StorageTextureRgba8Chroma => {
                    wgpu::BindingType::StorageTexture {
                        access: wgpu::StorageTextureAccess::WriteOnly,
                        format: wgpu::TextureFormat::Rgba8Unorm,
                        view_dimension: wgpu::TextureViewDimension::D2,
                    }
                }
            };
            wgpu::BindGroupLayoutEntry {
                binding: binding.index,
                visibility: wgpu::ShaderStages::COMPUTE,
                ty,
                count: None,
            }
        })
        .collect::<Vec<_>>();
    Ok(
        device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
            label: Some("music-v2-shader-bindings"),
            entries: &entries,
        }),
    )
}

pub fn create_bind_group<'a>(
    device: &wgpu::Device,
    layout: &wgpu::BindGroupLayout,
    descriptor: ShaderModuleDescriptor,
    resources: ShaderGpuResources<'a>,
) -> Result<wgpu::BindGroup, String> {
    validate_descriptor(descriptor).map_err(|error| format!("invalid shader module: {error}"))?;
    let entries = descriptor
        .bindings
        .iter()
        .map(|binding| {
            let resource = match binding.index {
                0 => resources.pcm_buffer.as_entire_binding(),
                1 => resources.scene_uniform.as_entire_binding(),
                2 => resources.waveform_buffer.as_entire_binding(),
                3 => wgpu::BindingResource::TextureView(resources.glyph_atlas),
                4 => resources.glyph_metrics.as_entire_binding(),
                5 => wgpu::BindingResource::Sampler(resources.glyph_sampler),
                6 => wgpu::BindingResource::TextureView(resources.artwork_texture),
                7 => wgpu::BindingResource::TextureView(resources.luma_output),
                8 => wgpu::BindingResource::TextureView(resources.chroma_output),
                9 => wgpu::BindingResource::TextureView(resources.rgb_output),
                _ => return Err(format!("unsupported shader binding {}", binding.index)),
            };
            Ok(wgpu::BindGroupEntry {
                binding: binding.index,
                resource,
            })
        })
        .collect::<Result<Vec<_>, String>>()?;
    Ok(device.create_bind_group(&wgpu::BindGroupDescriptor {
        label: Some("music-v2-shader-bind-group"),
        layout,
        entries: &entries,
    }))
}

pub fn create_compute_pipeline(
    device: &wgpu::Device,
    layout: &wgpu::BindGroupLayout,
    shader: &wgpu::ShaderModule,
    descriptor: ShaderModuleDescriptor,
) -> Result<wgpu::ComputePipeline, String> {
    validate_descriptor(descriptor).map_err(|error| format!("invalid shader module: {error}"))?;
    let pipeline_layout = device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
        label: Some("music-v2-shader-pipeline-layout"),
        bind_group_layouts: &[layout],
        push_constant_ranges: &[],
    });
    Ok(
        device.create_compute_pipeline(&wgpu::ComputePipelineDescriptor {
            label: Some("music-v2-shader-pipeline"),
            layout: Some(&pipeline_layout),
            module: shader,
            entry_point: descriptor.entry_point,
            compilation_options: Default::default(),
        }),
    )
}

pub fn encode_compute_pass(
    encoder: &mut wgpu::CommandEncoder,
    pipeline: &wgpu::ComputePipeline,
    bind_group: &wgpu::BindGroup,
    plan: &ShaderFramePlan,
) {
    let mut pass = encoder.begin_compute_pass(&wgpu::ComputePassDescriptor {
        label: Some("music-v2-shader-pass-graph"),
        timestamp_writes: None,
    });
    pass.set_pipeline(pipeline);
    pass.set_bind_group(0, bind_group, &[]);
    pass.dispatch_workgroups(
        plan.dispatch_workgroups.0,
        plan.dispatch_workgroups.1,
        plan.dispatch_workgroups.2,
    );
}

const RGBA_TO_BGRA_WGSL: &str = r#"
struct VertexOutput {
    @builtin(position) position: vec4<f32>,
    @location(0) uv: vec2<f32>,
};

@group(0) @binding(0)
var source_texture: texture_2d<f32>;

@group(0) @binding(1)
var source_sampler: sampler;

@vertex
fn fullscreen_vertex(@builtin(vertex_index) index: u32) -> VertexOutput {
    var positions = array<vec2<f32>, 3>(
        vec2<f32>(-1.0, -1.0),
        vec2<f32>(3.0, -1.0),
        vec2<f32>(-1.0, 3.0),
    );
    var uvs = array<vec2<f32>, 3>(
        vec2<f32>(0.0, 1.0),
        vec2<f32>(2.0, 1.0),
        vec2<f32>(0.0, -1.0),
    );
    var output: VertexOutput;
    output.position = vec4<f32>(positions[index], 0.0, 1.0);
    output.uv = uvs[index];
    return output;
}

@fragment
fn rgba_to_bgra(input: VertexOutput) -> @location(0) vec4<f32> {
    let rgba = textureSampleLevel(source_texture, source_sampler, input.uv, 0.0);
    return vec4<f32>(rgba.r, rgba.g, rgba.b, rgba.a);
}
"#;

/// GPU-only channel-order conversion for the D3D12/NVENC ARGB contract.
///
/// The source is the Draft shader's RGBA8 storage output and the target must
/// be a BGRA8 render target backed by the same D3D12 device. No source pixels
/// are mapped or copied to host memory.
pub struct RgbaToBgraPass {
    pipeline: wgpu::RenderPipeline,
    bind_group: wgpu::BindGroup,
}

impl RgbaToBgraPass {
    pub fn new(device: &wgpu::Device, source_view: &wgpu::TextureView) -> Self {
        let shader = device.create_shader_module(wgpu::ShaderModuleDescriptor {
            label: Some("music-v2-rgba-to-bgra-shader"),
            source: wgpu::ShaderSource::Wgsl(RGBA_TO_BGRA_WGSL.into()),
        });
        let layout = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
            label: Some("music-v2-rgba-to-bgra-layout"),
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
            label: Some("music-v2-rgba-to-bgra-sampler"),
            address_mode_u: wgpu::AddressMode::ClampToEdge,
            address_mode_v: wgpu::AddressMode::ClampToEdge,
            address_mode_w: wgpu::AddressMode::ClampToEdge,
            mag_filter: wgpu::FilterMode::Nearest,
            min_filter: wgpu::FilterMode::Nearest,
            mipmap_filter: wgpu::FilterMode::Nearest,
            ..Default::default()
        });
        let bind_group = device.create_bind_group(&wgpu::BindGroupDescriptor {
            label: Some("music-v2-rgba-to-bgra-bind-group"),
            layout: &layout,
            entries: &[
                wgpu::BindGroupEntry {
                    binding: 0,
                    resource: wgpu::BindingResource::TextureView(source_view),
                },
                wgpu::BindGroupEntry {
                    binding: 1,
                    resource: wgpu::BindingResource::Sampler(&sampler),
                },
            ],
        });
        let pipeline_layout = device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
            label: Some("music-v2-rgba-to-bgra-pipeline-layout"),
            bind_group_layouts: &[&layout],
            push_constant_ranges: &[],
        });
        let pipeline = device.create_render_pipeline(&wgpu::RenderPipelineDescriptor {
            label: Some("music-v2-rgba-to-bgra-pipeline"),
            layout: Some(&pipeline_layout),
            vertex: wgpu::VertexState {
                module: &shader,
                entry_point: "fullscreen_vertex",
                buffers: &[],
                compilation_options: Default::default(),
            },
            fragment: Some(wgpu::FragmentState {
                module: &shader,
                entry_point: "rgba_to_bgra",
                targets: &[Some(wgpu::ColorTargetState {
                    format: wgpu::TextureFormat::Bgra8Unorm,
                    blend: None,
                    write_mask: wgpu::ColorWrites::ALL,
                })],
                compilation_options: Default::default(),
            }),
            primitive: wgpu::PrimitiveState::default(),
            depth_stencil: None,
            multisample: wgpu::MultisampleState::default(),
            multiview: None,
        });
        Self {
            pipeline,
            bind_group,
        }
    }

    pub fn encode(&self, encoder: &mut wgpu::CommandEncoder, target_view: &wgpu::TextureView) {
        let mut pass = encoder.begin_render_pass(&wgpu::RenderPassDescriptor {
            label: Some("music-v2-rgba-to-bgra-pass"),
            color_attachments: &[Some(wgpu::RenderPassColorAttachment {
                view: target_view,
                resolve_target: None,
                ops: wgpu::Operations {
                    load: wgpu::LoadOp::Clear(wgpu::Color::BLACK),
                    store: wgpu::StoreOp::Store,
                },
            })],
            depth_stencil_attachment: None,
            occlusion_query_set: None,
            timestamp_writes: None,
        });
        pass.set_pipeline(&self.pipeline);
        pass.set_bind_group(0, &self.bind_group, &[]);
        pass.draw(0..3, 0..1);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rgba_to_bgra_target_preserves_decoded_rgb_channels() {
        assert!(RGBA_TO_BGRA_WGSL.contains("return vec4<f32>(rgba.r, rgba.g, rgba.b, rgba.a);"));
    }

    #[test]
    fn cpu_parity_scene_uses_canonical_layout_and_palette_contract() {
        let scene = ShaderSceneData::cpu_parity_default(1280, 720, 0, 120, 30);
        assert_eq!(scene.rects[0], [96, 152, 288, 288]);
        assert_eq!(scene.rects[4], [432, 320, 752, 168]);
        assert_eq!(scene.rects[5], [64, 548, 1000, 80]);
        assert_eq!(scene.rects[6], [64, 650, 1000, 8]);
        assert_eq!(scene.spectrum_q16.len(), 24);
        assert_eq!(scene.loudness_q16.len(), 1000);
        assert_eq!(scene.progress_q16, 0);
        assert_eq!(scene.fade_in_q16, u32::from(u16::MAX));
        assert_eq!(scene.fade_out_q16, u32::from(u16::MAX));
        assert_eq!(scene.text_lengths, [8, 10, 15, 4]);
        assert_eq!(scene.text_codepoints[0][0], u32::from(b'I'));
        assert_eq!(scene.text_codepoints[0][7], u32::from(b'D'));
        let custom = scene.with_text_lines("TITLE", "ARTIST", "ALBUM", "1:23");
        assert_eq!(custom.text_lengths, [5, 6, 5, 4]);
        assert_eq!(custom.text_codepoints[0][0], u32::from(b'T'));
    }

    #[test]
    fn frame_request_packs_the_uniform_contract() {
        let request = ShaderFrameRequest {
            width: 1280,
            height: 720,
            frame_index: 7,
            pcm_sample_count: 4800,
            pcm_sample_offset: 120,
            scene: ShaderSceneData::cpu_parity_default(1280, 720, 7, 120, 30),
        };
        let plan = prepare_frame_plan(&crate::music_v2_shader_draft::DRAFT_SHADER_MODULE, request)
            .expect("valid draft request");
        let words = bytemuck::cast_slice::<u8, u32>(&plan.uniform_bytes);
        assert_eq!(plan.uniform_bytes.len(), SHADER_SCENE_UNIFORM_BYTES);
        assert_eq!(words[0..5], [1280, 720, 7, 4800, 120]);
        assert_eq!(plan.dispatch_workgroups, (160, 90, 1));
        assert_eq!(plan.descriptor.version, 2);
        assert!(!plan.executable);
    }

    #[test]
    fn waveform_minmax_payload_sets_flags_and_column_count() {
        let waveform = vec![0, 32768, 49152, 65535];
        let request = ShaderFrameRequest {
            width: 1280,
            height: 720,
            frame_index: 7,
            pcm_sample_count: 4800,
            pcm_sample_offset: 120,
            scene: ShaderSceneData::cpu_parity_default(1280, 720, 7, 120, 30)
                .with_waveform_q16(waveform, true),
        };
        let plan = prepare_frame_plan(&crate::music_v2_shader_draft::DRAFT_SHADER_MODULE, request)
            .expect("valid waveform request");
        let words = bytemuck::cast_slice::<u8, u32>(&plan.uniform_bytes);
        assert_eq!(plan.uniform.waveform_flags, 7);
        assert_eq!(plan.uniform.waveform_columns, 2);
        assert_eq!(words[7], 7);
        assert_eq!(words[15], 2);
    }

    #[test]
    fn waveform_minmax_payload_rejects_odd_and_oversized_inputs() {
        let odd = ShaderFrameRequest {
            width: 1280,
            height: 720,
            frame_index: 0,
            pcm_sample_count: 1,
            pcm_sample_offset: 0,
            scene: ShaderSceneData::cpu_parity_default(1280, 720, 0, 1, 30)
                .with_waveform_q16(vec![32768], true),
        };
        assert!(odd.validate().is_err());

        let oversized = ShaderFrameRequest {
            width: 1280,
            height: 720,
            frame_index: 0,
            pcm_sample_count: 1,
            pcm_sample_offset: 0,
            scene: ShaderSceneData::cpu_parity_default(1280, 720, 0, 1, 30)
                .with_waveform_q16(vec![0; MAX_SHADER_WAVEFORM_SAMPLES + 1], false),
        };
        assert!(oversized.validate().is_err());
    }

    #[test]
    fn frame_request_rejects_invalid_pcm_window() {
        let request = ShaderFrameRequest {
            width: 1280,
            height: 720,
            frame_index: 7,
            pcm_sample_count: 0,
            pcm_sample_offset: 0,
            scene: ShaderSceneData::cpu_parity_default(1280, 720, 7, 120, 30),
        };
        assert!(
            prepare_frame_plan(&crate::music_v2_shader_draft::DRAFT_SHADER_MODULE, request)
                .is_err()
        );
    }

    #[test]
    fn frame_request_rejects_out_of_range_dimensions() {
        let request = ShaderFrameRequest {
            width: MAX_DIMENSION + 1,
            height: 720,
            frame_index: 7,
            pcm_sample_count: 4800,
            pcm_sample_offset: 120,
            scene: ShaderSceneData::cpu_parity_default(1280, 720, 7, 120, 30),
        };
        assert!(request.validate().is_err());
    }

    #[test]
    fn output_dimensions_round_up_chroma_for_odd_frames() {
        assert_eq!(
            shader_output_dimensions(1279, 719).unwrap(),
            ((1279, 719), (640, 360))
        );
    }
}
