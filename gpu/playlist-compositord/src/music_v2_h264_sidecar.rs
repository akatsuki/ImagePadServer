use super::adapter;
use super::contracts::{
    ArtworkMetadata, EncodedH264Frame, GlyphAtlasMetadata, H264AssetReceipt, MusicScenePayload,
    PixelFormat, MUSIC_PCM_WINDOW_BYTES, MUSIC_PCM_WINDOW_SAMPLES, MUSIC_SCENE_SCHEMA,
};
use super::music_v2_glyph_atlas::{build_atlas_for_text, GlyphAtlas};
use super::music_v2_shader_draft::H264_SHADER_MODULE;
use super::music_v2_shader_host::{
    create_artwork_texture, create_bind_group, create_bind_group_layout, create_compute_pipeline,
    create_glyph_atlas_texture, create_glyph_metrics_buffer, create_output_textures,
    create_pcm_storage_buffer, create_scene_uniform_buffer, create_shader_module,
    create_waveform_storage_buffer, encode_compute_pass, prepare_frame_plan,
    shader_output_dimensions, RgbaToBgraPass, ShaderFrameRequest, ShaderGpuResources,
    ShaderOutputTextures, ShaderSceneData, MAX_SHADER_WAVEFORM_SAMPLES,
};
use super::music_v2_shader_module::{ShaderModule, ShaderModuleDescriptor};
use sha2::{Digest, Sha256};

#[derive(Debug, Clone, Default, PartialEq, Eq)]
struct SessionAssetCache {
    artwork_hash: Option<String>,
    glyph_hash: Option<String>,
}

impl SessionAssetCache {
    fn initialize(scene: &MusicScenePayload) -> Result<Self, String> {
        let mut cache = Self::default();
        let artwork = scene
            .artwork
            .as_ref()
            .ok_or("direct H264 first frame requires artwork metadata")?;
        let glyph = scene
            .glyph_atlas
            .as_ref()
            .ok_or("direct H264 first frame requires glyph atlas metadata")?;
        cache.accept_artwork(artwork, true)?;
        cache.accept_glyph(glyph, true)?;
        Ok(cache)
    }

    fn validate_frame(&mut self, scene: &MusicScenePayload) -> Result<bool, String> {
        let artwork = scene
            .artwork
            .as_ref()
            .ok_or("direct H264 frame requires artwork metadata")?;
        let glyph = scene
            .glyph_atlas
            .as_ref()
            .ok_or("direct H264 frame requires glyph atlas metadata")?;
        self.accept_artwork(artwork, false)?;
        self.accept_glyph(glyph, false)
    }

    fn accept_artwork(&mut self, metadata: &ArtworkMetadata, initial: bool) -> Result<(), String> {
        validate_asset_hash("artwork", &metadata.asset_hash)?;
        if metadata.payload.is_empty() {
            if initial {
                return Err("direct H264 first artwork frame requires a full payload".into());
            }
            return match self.artwork_hash.as_deref() {
                Some(hash) if hash == metadata.asset_hash => Ok(()),
                Some(_) => Err("direct H264 artwork hash changed without a full payload".into()),
                None => Err("direct H264 artwork cache is not initialized".into()),
            };
        }
        validate_asset_payload_hash("artwork", &metadata.payload, &metadata.asset_hash)?;
        if let Some(hash) = self.artwork_hash.as_deref() {
            if hash != metadata.asset_hash {
                return Err("direct H264 artwork changed during a sidecar session".into());
            }
        }
        self.artwork_hash = Some(metadata.asset_hash.clone());
        Ok(())
    }

    fn accept_glyph(
        &mut self,
        metadata: &GlyphAtlasMetadata,
        initial: bool,
    ) -> Result<bool, String> {
        validate_asset_hash("glyph atlas", &metadata.asset_hash)?;
        if metadata.payload.is_empty() {
            if initial {
                return Err("direct H264 first glyph frame requires a full payload".into());
            }
            return match self.glyph_hash.as_deref() {
                Some(hash) if hash == metadata.asset_hash => Ok(false),
                Some(_) => Err("direct H264 glyph hash changed without a full payload".into()),
                None => Err("direct H264 glyph cache is not initialized".into()),
            };
        }
        validate_asset_payload_hash("glyph atlas", &metadata.payload, &metadata.asset_hash)?;
        let changed = self
            .glyph_hash
            .as_deref()
            .is_some_and(|hash| hash != metadata.asset_hash);
        self.glyph_hash = Some(metadata.asset_hash.clone());
        Ok(changed)
    }
}

fn validate_asset_hash(label: &str, hash: &str) -> Result<(), String> {
    if hash.len() != 64 || !hash.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return Err(format!(
            "direct H264 {label} asset hash must be a 64-character hex digest"
        ));
    }
    Ok(())
}

fn validate_asset_payload_hash(label: &str, payload: &[u8], expected: &str) -> Result<(), String> {
    let mut digest = Sha256::new();
    digest.update(payload);
    let actual = digest
        .finalize()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect::<String>();
    if actual != expected {
        return Err(format!(
            "direct H264 {label} payload hash mismatch: expected {expected}, got {actual}"
        ));
    }
    Ok(())
}

/// The H.264 shader remains draft-only until the CPU parity gate is green.
/// Diagnostic execution is explicit and never enabled by the production path.
fn direct_h264_shader_allowed(production_ready: bool, diagnostic_override: bool) -> bool {
    production_ready || diagnostic_override
}

fn direct_h264_diagnostic_override() -> bool {
    std::env::var("IMAGEPAD_GPU_H264_DRAFT").as_deref() == Ok("1")
}

#[cfg(windows)]
fn stage_log(message: String) {
    if std::env::var("IMAGEPAD_GPU_STAGE_TIMING").as_deref() == Ok("1") {
        eprintln!("[gpu-stage] {message}");
    }
}

#[cfg(windows)]
mod windows_impl {
    use super::*;

    use crate::h264_ring::BoundedH264Ring;
    use crate::native_nvenc::{
        nvenc_runtime_available, D3d12NvencEncoder, D3d12Surface, PendingBitstream,
    };
    use std::time::Instant;

    const H264_SURFACE_RING_SLOTS: usize = 64;
    const PLAYLIST_COMMAND_CHUNK_FRAMES: usize = 4;

    fn playlist_command_chunk_ranges(frame_count: usize) -> Result<Vec<(usize, usize)>, String> {
        if frame_count == 0 || frame_count > H264_SURFACE_RING_SLOTS {
            return Err("playlist command batch must contain a bounded 1..8 frames".into());
        }
        Ok((0..frame_count)
            .step_by(PLAYLIST_COMMAND_CHUNK_FRAMES)
            .map(|start| {
                (
                    start,
                    (start + PLAYLIST_COMMAND_CHUNK_FRAMES).min(frame_count),
                )
            })
            .collect())
    }

    #[cfg(test)]
    mod batching_tests {
        use super::playlist_command_chunk_ranges;

        #[test]
        fn playlist_command_chunks_keep_the_ring_bounded_and_expose_two_chunks_for_eight_frames() {
            assert_eq!(
                playlist_command_chunk_ranges(8).unwrap(),
                vec![(0, 4), (4, 8)]
            );
            assert_eq!(
                playlist_command_chunk_ranges(5).unwrap(),
                vec![(0, 4), (4, 5)]
            );
            assert_eq!(playlist_command_chunk_ranges(1).unwrap(), vec![(0, 1)]);
            assert!(playlist_command_chunk_ranges(0).is_err());
            assert!(playlist_command_chunk_ranges(super::H264_SURFACE_RING_SLOTS + 1).is_err());
        }
    }

    fn playlist_spectrum_bytes(
        timeline: &crate::protocol::TimelineChunk,
    ) -> Result<Vec<u8>, String> {
        if timeline.frames.is_empty() || timeline.frames.len() > H264_SURFACE_RING_SLOTS {
            return Err("playlist spectrum upload requires a bounded 1..8 frame batch".into());
        }
        let values = timeline
            .frames
            .iter()
            .flat_map(|frame| frame.spectrum_q16.iter().copied())
            .collect::<Vec<_>>();
        if values.is_empty() {
            return Err("playlist spectrum upload requires non-empty spectrum_q16".into());
        }
        let mut bytes = Vec::with_capacity(values.len() * std::mem::size_of::<u32>());
        for value in values {
            bytes.extend_from_slice(&(value as u32).to_le_bytes());
        }
        Ok(bytes)
    }

    struct H264RenderSlot {
        pcm_buffer: wgpu::Buffer,
        waveform_buffer: wgpu::Buffer,
        scene_uniform: wgpu::Buffer,
        output_textures: ShaderOutputTextures,
        luma_view: wgpu::TextureView,
        chroma_view: wgpu::TextureView,
        rgb_view: wgpu::TextureView,
        bind_group: wgpu::BindGroup,
        conversion: RgbaToBgraPass,
        surface: D3d12Surface,
        surface_view: wgpu::TextureView,
    }

    struct PlaylistFrameSlot {
        frame_uniform: wgpu::Buffer,
        output_texture: wgpu::Texture,
        output_view: wgpu::TextureView,
        bind_group: wgpu::BindGroup,
        conversion: RgbaToBgraPass,
    }

    struct PlaylistPendingFrame {
        bitstream: PendingBitstream,
        sequence: u64,
        pts_ns: i64,
        ring_slot: usize,
    }

    pub struct DirectNvencRenderer {
        device: wgpu::Device,
        queue: wgpu::Queue,
        pipeline: wgpu::ComputePipeline,
        playlist_pipeline: Option<crate::playlist_shader_timeline::PlaylistComputePipeline>,
        playlist_static_textures: Option<crate::playlist_shader_timeline::PlaylistStaticTextureSet>,
        playlist_assets_hash: Option<String>,
        playlist_artwork_hash: String,
        playlist_glyph_hash: String,
        playlist_spectrum_buffer: Option<wgpu::Buffer>,
        playlist_frame_slots: Vec<PlaylistFrameSlot>,
        playlist_pending_frames: Vec<PlaylistPendingFrame>,
        playlist_ready_frames: Vec<EncodedH264Frame>,
        layout: wgpu::BindGroupLayout,
        glyph_atlas_texture: wgpu::Texture,
        glyph_atlas_view: wgpu::TextureView,
        glyph_metrics: wgpu::Buffer,
        glyph_sampler: wgpu::Sampler,
        artwork_texture: wgpu::Texture,
        artwork_view: wgpu::TextureView,
        // Declare the encoder before the surface so Rust drops NVENC
        // registrations before the underlying D3D12 resource wrapper.
        nvenc: D3d12NvencEncoder,
        slots: Vec<H264RenderSlot>,
        width: u32,
        height: u32,
        fps: u32,
        artwork_hash: String,
        glyph_hash: String,
        asset_cache: SessionAssetCache,
        ring: BoundedH264Ring,
        last_sequence: Option<u64>,
        stage_frame_count: u64,
        stage_frame_total_seconds: f64,
        stage_submit_total_seconds: f64,
        stage_nvenc_total_seconds: f64,
        stage_nvenc_submit_total_seconds: f64,
        stage_nvenc_drain_total_seconds: f64,
        next_playlist_input_fence: u64,
        // Monotonic per-frame encoder index that survives session resets. The
        // timeline's per-track frame_index restarts at 0 for a new track, but
        // NVENC's last_submitted_frame_index and per-slot D3D12 output fences
        // are monotonic, so the encoder index must be session-global.
        session_frame_index: u32,
    }

    pub fn probe_capability() -> bool {
        if !direct_h264_shader_allowed(
            H264_SHADER_MODULE.descriptor().production_ready,
            direct_h264_diagnostic_override(),
        ) {
            return false;
        }
        let instance = wgpu::Instance::default();
        if adapter::select_with_backends(&instance, wgpu::Backends::DX12).is_err() {
            return false;
        }
        // The real session/resource/register/encode gates remain fail-closed
        // in DirectNvencRenderer::new and are exercised on the first frame.
        // Hello must not create and immediately destroy a full NVENC session.
        nvenc_runtime_available()
    }

    fn create_render_slot(
        device: &wgpu::Device,
        queue: &wgpu::Queue,
        layout: &wgpu::BindGroupLayout,
        descriptor: ShaderModuleDescriptor,
        pcm: &[f32],
        uniform: &crate::music_v2_shader_host::ShaderSceneUniform,
        glyph_atlas_view: &wgpu::TextureView,
        glyph_metrics: &wgpu::Buffer,
        glyph_sampler: &wgpu::Sampler,
        artwork_view: &wgpu::TextureView,
        width: u32,
        height: u32,
    ) -> Result<H264RenderSlot, String> {
        let pcm_buffer = create_pcm_storage_buffer(device, pcm)?;
        let waveform_buffer =
            create_waveform_storage_buffer(device, &vec![0u16; MAX_SHADER_WAVEFORM_SAMPLES]);
        let scene_uniform = create_scene_uniform_buffer(device, uniform);
        let output_textures = create_output_textures(device, width, height)?;
        let luma_view = output_textures.luma_view();
        let chroma_view = output_textures.chroma_view();
        let rgb_view = output_textures.rgb_view();
        let bind_group = create_bind_group(
            device,
            layout,
            descriptor,
            ShaderGpuResources {
                pcm_buffer: &pcm_buffer,
                scene_uniform: &scene_uniform,
                waveform_buffer: &waveform_buffer,
                glyph_atlas: glyph_atlas_view,
                glyph_metrics,
                glyph_sampler,
                artwork_texture: artwork_view,
                luma_output: &luma_view,
                chroma_output: &chroma_view,
                rgb_output: &rgb_view,
            },
        )?;
        let surface = D3d12Surface::create(device, width, height)?;
        let surface_view = surface
            .texture
            .create_view(&wgpu::TextureViewDescriptor::default());
        let conversion = RgbaToBgraPass::new(device, &rgb_view);
        Ok(H264RenderSlot {
            pcm_buffer,
            waveform_buffer,
            scene_uniform,
            output_textures,
            luma_view,
            chroma_view,
            rgb_view,
            bind_group,
            conversion,
            surface,
            surface_view,
        })
    }

    impl DirectNvencRenderer {
        pub fn new(
            width: u32,
            height: u32,
            fps: u32,
            scene: &MusicScenePayload,
        ) -> Result<Self, String> {
            let init_started = Instant::now();
            if !direct_h264_shader_allowed(
                H264_SHADER_MODULE.descriptor().production_ready,
                direct_h264_diagnostic_override(),
            ) {
                return Err(
                    "direct H264 shader is not production-ready; CPU parity gate is closed".into(),
                );
            }
            validate_scene_inputs(width, height, fps, scene)?;
            let asset_cache = SessionAssetCache::initialize(scene)?;
            let instance = wgpu::Instance::default();
            let selected = adapter::select_with_backends(&instance, wgpu::Backends::DX12)?;
            let (device, queue) = pollster::block_on(
                selected
                    .adapter
                    .request_device(&wgpu::DeviceDescriptor::default(), None),
            )
            .map_err(|error| format!("direct H264 D3D12 device: {error}"))?;
            stage_log(format!(
                "rust_direct_init_device_seconds={:.6}",
                init_started.elapsed().as_secs_f64()
            ));

            let descriptor = H264_SHADER_MODULE.descriptor();
            let shader = create_shader_module(&device, &H264_SHADER_MODULE)?;
            let layout = create_bind_group_layout(&device, descriptor)?;
            let pipeline = create_compute_pipeline(&device, &layout, &shader, descriptor)?;
            stage_log(format!(
                "rust_direct_init_pipeline_seconds={:.6}",
                init_started.elapsed().as_secs_f64()
            ));
            let request = scene_frame_request(width, height, fps, 0, scene)?;
            let plan = prepare_frame_plan(&H264_SHADER_MODULE, request)?;

            let pcm = pcm_from_scene(scene)?;

            let glyph_hash = scene
                .glyph_atlas
                .as_ref()
                .map(|atlas| atlas.asset_hash.clone())
                .unwrap_or_default();
            let glyph_atlas = glyph_atlas_from_scene(scene)?;
            let glyph_atlas_texture = create_glyph_atlas_texture(&device, &queue, &glyph_atlas);
            let glyph_atlas_view =
                glyph_atlas_texture.create_view(&wgpu::TextureViewDescriptor::default());
            let glyph_metrics = create_glyph_metrics_buffer(&device, &glyph_atlas);
            let glyph_sampler = device.create_sampler(&wgpu::SamplerDescriptor {
                label: Some("music-v2-sidecar-glyph-sampler"),
                address_mode_u: wgpu::AddressMode::ClampToEdge,
                address_mode_v: wgpu::AddressMode::ClampToEdge,
                address_mode_w: wgpu::AddressMode::ClampToEdge,
                mag_filter: wgpu::FilterMode::Linear,
                min_filter: wgpu::FilterMode::Linear,
                mipmap_filter: wgpu::FilterMode::Nearest,
                ..Default::default()
            });

            let artwork = scene
                .artwork
                .as_ref()
                .ok_or("direct H264 scene artwork is required")?;
            let artwork_pixels = tight_rgba_payload(
                artwork.width,
                artwork.height,
                artwork.row_stride,
                artwork.format,
                &artwork.payload,
                "artwork",
            )?;
            let artwork_texture = create_artwork_texture(
                &device,
                &queue,
                &artwork_pixels,
                artwork.width,
                artwork.height,
            )?;
            let artwork_view = artwork_texture.create_view(&wgpu::TextureViewDescriptor::default());

            let mut nvenc = D3d12NvencEncoder::create(&device, width, height, fps)?;
            for _ in 1..H264_SURFACE_RING_SLOTS {
                nvenc.register_output_slot(&device)?;
            }
            let mut slots = Vec::with_capacity(H264_SURFACE_RING_SLOTS);
            for slot_index in 0..H264_SURFACE_RING_SLOTS {
                let slot = create_render_slot(
                    &device,
                    &queue,
                    &layout,
                    descriptor,
                    &pcm,
                    &plan.uniform,
                    &glyph_atlas_view,
                    &glyph_metrics,
                    &glyph_sampler,
                    &artwork_view,
                    width,
                    height,
                )?;
                nvenc.register_surface_at(slot_index, &slot.surface)?;
                slots.push(slot);
            }
            stage_log(format!(
                "rust_direct_init_total_seconds={:.6}",
                init_started.elapsed().as_secs_f64()
            ));

            Ok(Self {
                device,
                queue,
                pipeline,
                playlist_pipeline: None,
                playlist_static_textures: None,
                playlist_assets_hash: None,
                playlist_artwork_hash: String::new(),
                playlist_glyph_hash: String::new(),
                playlist_spectrum_buffer: None,
                playlist_frame_slots: Vec::new(),
                playlist_pending_frames: Vec::new(),
                playlist_ready_frames: Vec::new(),
                layout,
                glyph_atlas_texture,
                glyph_atlas_view,
                glyph_metrics,
                glyph_sampler,
                artwork_texture,
                artwork_view,
                nvenc,
                slots,
                width,
                height,
                fps,
                artwork_hash: scene
                    .artwork
                    .as_ref()
                    .map(|a| a.asset_hash.clone())
                    .unwrap_or_default(),
                glyph_hash,
                asset_cache,
                ring: BoundedH264Ring::new(H264_SURFACE_RING_SLOTS)
                    .map_err(|error| format!("direct H264 ring initialization: {error:?}"))?,
                last_sequence: None,
                stage_frame_count: 0,
                stage_frame_total_seconds: 0.0,
                stage_submit_total_seconds: 0.0,
                stage_nvenc_total_seconds: 0.0,
                stage_nvenc_submit_total_seconds: 0.0,
                stage_nvenc_drain_total_seconds: 0.0,
                next_playlist_input_fence: 1,
                session_frame_index: 0,
            })
        }

        pub fn ensure_playlist_pipeline(&mut self) -> Result<(), String> {
            if self.playlist_pipeline.is_none() {
                self.playlist_pipeline = Some(
                    crate::playlist_shader_timeline::create_playlist_compute_pipeline(
                        &self.device,
                    )?,
                );
            }
            Ok(())
        }

        pub fn prepare_playlist_static_textures(
            &mut self,
            assets: &crate::protocol::TrackAssets,
        ) -> Result<(), String> {
            if self.playlist_assets_hash.as_deref() == Some(assets.assets_hash.as_str())
                && self.playlist_static_textures.is_some()
            {
                return Ok(());
            }
            let textures = crate::playlist_shader_timeline::PlaylistStaticTextureSet::upload(
                &self.device,
                &self.queue,
                assets,
            )?;
            self.playlist_static_textures = Some(textures);
            self.playlist_assets_hash = Some(assets.assets_hash.clone());
            self.playlist_artwork_hash = assets
                .artwork
                .as_ref()
                .map(|artwork| artwork.asset_hash.clone())
                .ok_or("playlist artwork hash is missing")?;
            self.playlist_glyph_hash = assets
                .glyph_atlas
                .as_ref()
                .map(|glyphs| glyphs.asset_hash.clone())
                .ok_or("playlist glyph hash is missing")?;
            Ok(())
        }

        pub fn prepare_playlist_spectrum_buffer(
            &mut self,
            timeline: &crate::protocol::TimelineChunk,
        ) -> Result<(), String> {
            let bytes = playlist_spectrum_bytes(timeline)?;
            // Allocate a stable, maximum-size buffer once. The bind groups built
            // in ensure_playlist_frame_slots hold this buffer for the session,
            // so re-creating it (and clearing the slots) every batch forced a
            // full 32-slot rebuild (textures + bind groups + conversion passes)
            // each window — the dominant ~130ms/batch cost.
            if self.playlist_spectrum_buffer.is_none() {
                let per_frame = timeline
                    .frames
                    .first()
                    .map(|frame| frame.spectrum_q16.len() * std::mem::size_of::<u32>())
                    .unwrap_or(0);
                let capacity = (H264_SURFACE_RING_SLOTS * per_frame).max(bytes.len()) as u64;
                let buffer = self.device.create_buffer(&wgpu::BufferDescriptor {
                    label: Some("playlist-spectrum-q16-storage"),
                    size: capacity,
                    usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_DST,
                    mapped_at_creation: false,
                });
                self.playlist_spectrum_buffer = Some(buffer);
            }
            let buffer = self
                .playlist_spectrum_buffer
                .as_ref()
                .ok_or("playlist spectrum buffer is not allocated")?;
            self.queue.write_buffer(buffer, 0, &bytes);
            Ok(())
        }

        pub fn ensure_playlist_frame_slots(&mut self) -> Result<(), String> {
            if self.playlist_frame_slots.len() == H264_SURFACE_RING_SLOTS {
                return Ok(());
            }
            let pipeline = self
                .playlist_pipeline
                .as_ref()
                .ok_or("playlist pipeline must be created before frame slots")?;
            let textures = self
                .playlist_static_textures
                .as_ref()
                .ok_or("playlist static textures must be uploaded before frame slots")?;
            let spectrum_buffer = self
                .playlist_spectrum_buffer
                .as_ref()
                .ok_or("playlist spectrum buffer must be uploaded before frame slots")?;
            let mut slots = Vec::with_capacity(H264_SURFACE_RING_SLOTS);
            for slot_index in 0..H264_SURFACE_RING_SLOTS {
                let frame_uniform = self.device.create_buffer(&wgpu::BufferDescriptor {
                    label: Some("playlist-frame-uniform"),
                    size: crate::playlist_shader_timeline::PLAYLIST_FRAME_UNIFORM_BYTES as u64,
                    usage: wgpu::BufferUsages::UNIFORM | wgpu::BufferUsages::COPY_DST,
                    mapped_at_creation: false,
                });
                let output_texture = self.device.create_texture(&wgpu::TextureDescriptor {
                    label: Some("playlist-gpu-owned-rgba-output"),
                    size: wgpu::Extent3d {
                        width: self.width,
                        height: self.height,
                        depth_or_array_layers: 1,
                    },
                    mip_level_count: 1,
                    sample_count: 1,
                    dimension: wgpu::TextureDimension::D2,
                    format: wgpu::TextureFormat::Rgba8Unorm,
                    usage: wgpu::TextureUsages::STORAGE_BINDING
                        | wgpu::TextureUsages::TEXTURE_BINDING,
                    view_formats: &[],
                });
                let output_view =
                    output_texture.create_view(&wgpu::TextureViewDescriptor::default());
                if self.slots.len() != H264_SURFACE_RING_SLOTS {
                    return Err(
                        "legacy NVENC surface ring must be initialized before playlist slots"
                            .into(),
                    );
                }
                let conversion = RgbaToBgraPass::new(&self.device, &output_view);
                let bind_group = crate::playlist_shader_timeline::create_playlist_bind_group(
                    &self.device,
                    &pipeline.bind_group_layout,
                    crate::playlist_shader_timeline::PlaylistBindGroupResources {
                        frame_uniform: &frame_uniform,
                        artwork_texture: &textures.artwork_view,
                        glyph_atlas: &textures.glyph_view,
                        glyph_sampler: &textures.sampler,
                        loudness_texture: &textures.loudness_view,
                        spectrum_buffer,
                        rgb_output: &output_view,
                        text_overlay_texture: &textures.text_overlay_view,
                        layout_buffer: &textures.layout_buffer,
                        palette_buffer: &textures.palette_buffer,
                        base_texture: &textures.base_view,
                    },
                );
                let _ = slot_index;
                slots.push(PlaylistFrameSlot {
                    frame_uniform,
                    output_texture,
                    output_view,
                    bind_group,
                    conversion,
                });
            }
            self.playlist_frame_slots = slots;
            Ok(())
        }

        /// Resets the per-session playlist state while keeping the D3D12
        /// device, compiled compute pipeline, NVENC encoder, and surface ring.
        /// The next PrepareTrack re-uploads static textures and the next batch
        /// rebuilds frame slots + spectrum buffer, so the ~2.3s initialization
        /// is paid once per process instead of once per session.
        pub fn reset_playlist_session(&mut self) -> Result<(), String> {
            if !self.ring.is_empty() {
                return Err(
                    "playlist session reset rejected: render/encode ring is not drained".into(),
                );
            }
            self.playlist_static_textures = None;
            self.playlist_assets_hash = None;
            self.playlist_artwork_hash.clear();
            self.playlist_glyph_hash.clear();
            self.playlist_spectrum_buffer = None;
            self.playlist_frame_slots.clear();
            self.playlist_pending_frames.clear();
            self.playlist_ready_frames.clear();
            self.last_sequence = None;
            // The ring and next_playlist_input_fence are intentionally kept:
            // they are monotonic across sessions and must stay consistent with
            // the NVENC encoder's input/output fence state.
            Ok(())
        }

        pub fn submit_playlist_timeline(
            &mut self,
            uniforms: &[crate::playlist_shader_timeline::PlaylistFrameUniform],
        ) -> Result<(), String> {
            let chunk_ranges = playlist_command_chunk_ranges(uniforms.len())?;
            if !self.playlist_pending_frames.is_empty()
                || !self.playlist_ready_frames.is_empty()
                || !self.ring.is_empty()
            {
                return Err("playlist submit has unreleased in-flight slots".into());
            }
            if self.playlist_frame_slots.len() != H264_SURFACE_RING_SLOTS {
                return Err("playlist frame slots are not fully populated".into());
            }
            if self.playlist_pipeline.is_none() {
                return Err("playlist pipeline is not initialized".into());
            }
            if self.playlist_artwork_hash.is_empty() || self.playlist_glyph_hash.is_empty() {
                return Err("playlist asset receipt hashes are not initialized".into());
            }

            // Acquire every slot before submitting anything. This keeps the
            // batch atomic: a validation or ring-capacity failure cannot leave
            // a successful prefix visible to the caller.
            let mut ring_indices = Vec::with_capacity(uniforms.len());
            for uniform in uniforms {
                let sequence =
                    u64::from(uniform.sequence_lo) | (u64::from(uniform.sequence_hi) << 32);
                let pts_ns = i64::from_le_bytes(
                    (u64::from(uniform.pts_ns_lo) | (u64::from(uniform.pts_ns_hi) << 32))
                        .to_le_bytes(),
                );
                let ring_slot = self
                    .ring
                    .acquire_render(sequence, pts_ns)
                    .map_err(|error| format!("playlist ring acquire failed: {error:?}"))?;
                ring_indices.push(ring_slot);
            }

            let trace_started = Instant::now();
            let mut render_submit_seconds = 0.0;
            let mut nvenc_submit_seconds = 0.0;
            let mut pending = Vec::with_capacity(uniforms.len());
            let pipeline = self
                .playlist_pipeline
                .as_ref()
                .ok_or("playlist pipeline is not initialized")?;

            // Keep distinct command submissions at the bounded four-frame
            // boundaries. NVENC pictures from an earlier chunk remain pending
            // while the next chunk is rendered and submitted; only after all
            // chunks are queued does drain_playlist_timeline lock bitstreams.
            for (chunk_index, (start, end)) in chunk_ranges.iter().copied().enumerate() {
                let mut encoder =
                    self.device
                        .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                            label: Some("playlist-timeline-command-chunk"),
                        });
                for index in start..end {
                    let uniform = &uniforms[index];
                    let ring_slot = ring_indices[index];
                    let slot = &self.playlist_frame_slots[ring_slot];
                    crate::playlist_shader_timeline::write_playlist_frame_uniform(
                        &self.queue,
                        &slot.frame_uniform,
                        uniform,
                    );
                    crate::playlist_shader_timeline::encode_playlist_compute_pass(
                        &mut encoder,
                        pipeline,
                        &slot.bind_group,
                        self.width,
                        self.height,
                    )?;
                    slot.conversion
                        .encode(&mut encoder, &self.slots[ring_slot].surface_view);
                }
                let input_fence_value = self.next_playlist_input_fence;
                self.next_playlist_input_fence = self.next_playlist_input_fence.saturating_add(1);
                let render_submit_started = Instant::now();
                self.queue.submit(Some(encoder.finish()));
                render_submit_seconds += render_submit_started.elapsed().as_secs_f64();
                self.nvenc
                    .signal_input_fence(&self.device, input_fence_value)?;
                let input_fence_completed = self.nvenc.input_fence_completed_value();
                stage_log(format!(
                    "rust_playlist_trace event=render_chunk_submit chunk_index={} first_frame_index={} frames={} input_fence={} input_fence_completed={} elapsed_seconds={:.6}",
                    chunk_index,
                    uniforms[start].frame_index,
                    end - start,
                    input_fence_value,
                    input_fence_completed,
                    trace_started.elapsed().as_secs_f64(),
                ));

                let nvenc_submit_started = Instant::now();
                for index in start..end {
                    let uniform = &uniforms[index];
                    let ring_slot = ring_indices[index];
                    let sequence =
                        u64::from(uniform.sequence_lo) | (u64::from(uniform.sequence_hi) << 32);
                    let pts_ns = i64::from_le_bytes(
                        (u64::from(uniform.pts_ns_lo) | (u64::from(uniform.pts_ns_hi) << 32))
                            .to_le_bytes(),
                    );
                    // The timeline frame_index restarts at 0 for a new track, but
                    // NVENC's last_submitted_frame_index and per-slot output fences
                    // are monotonic. Use the session-global counter for the encoder
                    // and keep the IDR flag tied to the per-track frame_index.
                    let frame_index = self.session_frame_index;
                    self.session_frame_index = self.session_frame_index.saturating_add(1);
                    let force_idr = uniform.frame_index == 0;
                    let encode_fence = u64::from(frame_index).saturating_add(1);
                    self.ring
                        .mark_encode_submitted(ring_slot, input_fence_value, encode_fence)
                        .map_err(|error| {
                            format!("playlist ring encode submit failed: {error:?}")
                        })?;
                    let bitstream = self.nvenc.submit_surface_at(
                        &self.slots[ring_slot].surface,
                        ring_slot,
                        frame_index,
                        input_fence_value,
                        uniform.frame_index,
                        force_idr,
                    )?;
                    stage_log(format!(
                        "rust_playlist_trace event=nvenc_submit frame_index={} ring_slot={} sequence={} input_fence={} chunk_index={} elapsed_seconds={:.6}",
                        frame_index,
                        ring_slot,
                        sequence,
                        input_fence_value,
                        chunk_index,
                        trace_started.elapsed().as_secs_f64()
                    ));
                    pending.push(PlaylistPendingFrame {
                        bitstream,
                        sequence,
                        pts_ns,
                        ring_slot,
                    });
                }
                nvenc_submit_seconds += nvenc_submit_started.elapsed().as_secs_f64();
            }
            stage_log(format!(
                "rust_playlist_batch frames={} chunks={} render_submit_seconds={:.6} nvenc_submit_seconds={:.6} pending_frames={} total_seconds={:.6}",
                uniforms.len(),
                chunk_ranges.len(),
                render_submit_seconds,
                nvenc_submit_seconds,
                pending.len(),
                trace_started.elapsed().as_secs_f64(),
            ));
            self.playlist_pending_frames = pending;
            Ok(())
        }

        fn drain_playlist_pending_frame(
            &mut self,
            pending: PlaylistPendingFrame,
        ) -> Result<EncodedH264Frame, String> {
            let frame_index = pending.bitstream.frame_index;
            let trace_started = Instant::now();
            let payload = self.nvenc.drain_bitstream(pending.bitstream)?;
            let output_fence_completed =
                self.nvenc.output_fence_completed_value(pending.ring_slot)?;
            let input_fence_completed = self.nvenc.input_fence_completed_value();
            stage_log(format!(
            	"rust_playlist_trace event=nvenc_drain frame_index={} ring_slot={} sequence={} output_fence_completed={} input_fence_completed={} elapsed_seconds={:.6}",
        		frame_index,
        		pending.ring_slot,
        		pending.sequence,
        		output_fence_completed,
        		input_fence_completed,
        		trace_started.elapsed().as_secs_f64(),
        	));
            self.ring
                .mark_bitstream_ready(pending.ring_slot, u64::from(frame_index).saturating_add(1))
                .map_err(|error| format!("playlist ring bitstream readiness failed: {error:?}"))?;
            let frame = EncodedH264Frame {
                schema: crate::contracts::CONTRACT_VERSION,
                sequence: pending.sequence,
                pts_ns: pending.pts_ns,
                width: self.width,
                height: self.height,
                codec: "h264".into(),
                profile: "high".into(),
                backend: "dx12".into(),
                pixel_readback_bytes: 0,
                asset_cache_ready: true,
                asset_receipt: H264AssetReceipt {
                    artwork_hash: self.playlist_artwork_hash.clone(),
                    glyph_hash: self.playlist_glyph_hash.clone(),
                },
                payload,
            };
            frame.validate()?;
            self.ring
                .release_next_ordered(pending.sequence)
                .map_err(|error| format!("playlist ordered ring release failed: {error:?}"))?;
            Ok(frame)
        }

        pub fn drain_playlist_timeline(&mut self) -> Result<Vec<EncodedH264Frame>, String> {
            if self.playlist_pending_frames.is_empty() && self.playlist_ready_frames.is_empty() {
                return Err("playlist drain has no pending encoded frames".into());
            }
            let pending = std::mem::take(&mut self.playlist_pending_frames);
            let mut encoded = std::mem::take(&mut self.playlist_ready_frames);
            for pending in pending {
                encoded.push(self.drain_playlist_pending_frame(pending)?);
            }
            Ok(encoded)
        }

        pub fn encode_playlist_timeline(
            &mut self,
            uniforms: &[crate::playlist_shader_timeline::PlaylistFrameUniform],
        ) -> Result<Vec<EncodedH264Frame>, String> {
            if uniforms.is_empty() || uniforms.len() > H264_SURFACE_RING_SLOTS {
                return Err("playlist encode requires a bounded 1..8 frame batch".into());
            }
            if !self.ring.is_empty() {
                return Err("playlist H264 ring has unreleased in-flight slots".into());
            }
            if self.playlist_frame_slots.len() != H264_SURFACE_RING_SLOTS {
                return Err("playlist frame slots are not fully populated".into());
            }
            let pipeline = self
                .playlist_pipeline
                .as_ref()
                .ok_or("playlist pipeline is not initialized")?;
            if self.playlist_artwork_hash.is_empty() || self.playlist_glyph_hash.is_empty() {
                return Err("playlist asset receipt hashes are not initialized".into());
            }

            let ring_indices = uniforms
                .iter()
                .map(|uniform| {
                    self.ring
                        .acquire_render(
                            u64::from(uniform.sequence_lo) | (u64::from(uniform.sequence_hi) << 32),
                            i64::from_le_bytes(
                                (u64::from(uniform.pts_ns_lo)
                                    | (u64::from(uniform.pts_ns_hi) << 32))
                                    .to_le_bytes(),
                            ),
                        )
                        .map_err(|error| format!("playlist ring acquire failed: {error:?}"))
                })
                .collect::<Result<Vec<_>, _>>()?;

            let mut encoder = self
                .device
                .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                    label: Some("playlist-timeline-encode-command-encoder"),
                });
            for (index, uniform) in uniforms.iter().enumerate() {
                let slot = &self.playlist_frame_slots[ring_indices[index]];
                crate::playlist_shader_timeline::write_playlist_frame_uniform(
                    &self.queue,
                    &slot.frame_uniform,
                    uniform,
                );
                crate::playlist_shader_timeline::encode_playlist_compute_pass(
                    &mut encoder,
                    pipeline,
                    &slot.bind_group,
                    self.width,
                    self.height,
                )?;
                slot.conversion
                    .encode(&mut encoder, &self.slots[ring_indices[index]].surface_view);
            }
            self.queue.submit(Some(encoder.finish()));

            let input_fence_value = uniforms
                .iter()
                .map(|uniform| {
                    u64::from(uniform.sequence_lo) | (u64::from(uniform.sequence_hi) << 32)
                })
                .max()
                .unwrap_or_default()
                .saturating_add(1);
            self.nvenc
                .signal_input_fence(&self.device, input_fence_value)?;

            let mut pending = Vec::with_capacity(uniforms.len());
            for (index, uniform) in uniforms.iter().enumerate() {
                let ring_slot = ring_indices[index];
                let frame_index = uniform.frame_index;
                let sequence =
                    u64::from(uniform.sequence_lo) | (u64::from(uniform.sequence_hi) << 32);
                let pts_ns = i64::from_le_bytes(
                    (u64::from(uniform.pts_ns_lo) | (u64::from(uniform.pts_ns_hi) << 32))
                        .to_le_bytes(),
                );
                let encode_fence = u64::from(frame_index).saturating_add(1);
                self.ring
                    .mark_encode_submitted(ring_slot, input_fence_value, encode_fence)
                    .map_err(|error| format!("playlist ring encode submit failed: {error:?}"))?;
                let submitted = self.nvenc.submit_surface_at(
                    &self.slots[ring_slot].surface,
                    ring_slot,
                    frame_index,
                    input_fence_value,
                    frame_index,
                    frame_index == 0,
                )?;
                pending.push((submitted, sequence, pts_ns, ring_slot));
            }

            let mut encoded = Vec::with_capacity(pending.len());
            for (submitted, sequence, pts_ns, ring_slot) in pending {
                let frame_index = submitted.frame_index;
                let payload = self.nvenc.drain_bitstream(submitted)?;
                self.ring
                    .mark_bitstream_ready(ring_slot, u64::from(frame_index).saturating_add(1))
                    .map_err(|error| {
                        format!("playlist ring bitstream readiness failed: {error:?}")
                    })?;
                let frame = EncodedH264Frame {
                    schema: crate::contracts::CONTRACT_VERSION,
                    sequence,
                    pts_ns,
                    width: self.width,
                    height: self.height,
                    codec: "h264".into(),
                    profile: "high".into(),
                    backend: "dx12".into(),
                    pixel_readback_bytes: 0,
                    asset_cache_ready: true,
                    asset_receipt: H264AssetReceipt {
                        artwork_hash: self.playlist_artwork_hash.clone(),
                        glyph_hash: self.playlist_glyph_hash.clone(),
                    },
                    payload,
                };
                frame.validate()?;
                self.ring
                    .release_next_ordered(sequence)
                    .map_err(|error| format!("playlist ordered ring release failed: {error:?}"))?;
                encoded.push(frame);
            }
            Ok(encoded)
        }

        pub fn render_frame(
            &mut self,
            sequence: u64,
            pts_ns: i64,
            scene: &MusicScenePayload,
        ) -> Result<EncodedH264Frame, String> {
            let frames = [(scene, sequence, pts_ns)];
            self.render_batch(&frames)?
                .into_iter()
                .next()
                .ok_or_else(|| "direct H264 renderer returned an empty frame batch".into())
        }

        /// Records all frames in one bounded command submission. Each frame
        /// owns a ring slot containing its storage buffers, output textures,
        /// D3D12 surface, and NVENC registration; no resource is reused until
        /// the batch fence has been handed to NVENC and its bitstream is
        /// unlocked.
        pub fn render_batch(
            &mut self,
            frames: &[(&MusicScenePayload, u64, i64)],
        ) -> Result<Vec<EncodedH264Frame>, String> {
            let frame_started = Instant::now();
            if frames.is_empty() || frames.len() > self.slots.len() {
                return Err(format!(
                    "direct H264 renderer batch must contain 1..{} frames",
                    self.slots.len()
                ));
            }
            let first_sequence = frames[0].1;
            if let Some(last_sequence) = self.last_sequence {
                if first_sequence != last_sequence.saturating_add(1) {
                    return Err("direct H264 frame sequence has a gap or duplicate".into());
                }
            }
            for window in frames.windows(2) {
                if window[1].1 != window[0].1.saturating_add(1) || window[1].2 <= window[0].2 {
                    return Err(
                        "direct H264 batch sequence and PTS must be strictly ordered".into(),
                    );
                }
            }

            let mut prepared = Vec::with_capacity(frames.len());
            for (index, (scene, sequence, pts_ns)) in frames.iter().enumerate() {
                if *pts_ns < 0 {
                    return Err("direct H264 PTS must be non-negative".into());
                }
                validate_scene_inputs(self.width, self.height, self.fps, scene)?;
                let glyph_changed = self.asset_cache.validate_frame(scene)?;
                let artwork_hash = scene
                    .artwork
                    .as_ref()
                    .map(|a| a.asset_hash.clone())
                    .unwrap_or_default();
                let glyph_hash = scene
                    .glyph_atlas
                    .as_ref()
                    .map(|a| a.asset_hash.clone())
                    .unwrap_or_default();
                if artwork_hash != self.artwork_hash {
                    return Err("direct H264 artwork asset changed during a sidecar session".into());
                }
                if glyph_changed || glyph_hash != self.glyph_hash {
                    if index != 0 {
                        return Err(
                            "direct H264 glyph asset changed inside one submitted batch".into()
                        );
                    }
                    self.refresh_glyph_binding(scene)?;
                }
                let frame_index = u32::try_from(*sequence)
                    .map_err(|_| "H264 sequence exceeds shader frame range")?;
                let request =
                    scene_frame_request(self.width, self.height, self.fps, frame_index, scene)?;
                let plan = prepare_frame_plan(&H264_SHADER_MODULE, request)?;
                let pcm = pcm_from_scene(scene)?;
                let waveform = scene
                    .feature
                    .waveform_q16
                    .iter()
                    .map(|value| u32::from(*value))
                    .collect::<Vec<_>>();
                prepared.push((*sequence, *pts_ns, frame_index, plan, pcm, waveform));
            }

            let mut ring_indices = Vec::with_capacity(prepared.len());
            for (sequence, pts_ns, _, _, _, _) in &prepared {
                ring_indices.push(
                    self.ring
                        .acquire_render(*sequence, *pts_ns)
                        .map_err(|error| format!("direct H264 ring acquire failed: {error:?}"))?,
                );
            }
            let mut command_encoder =
                self.device
                    .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                        label: Some("music-v2-sidecar-h264-batch"),
                    });
            for (batch_index, (_, _, _, plan, pcm, waveform)) in prepared.iter().enumerate() {
                let ring_slot = ring_indices[batch_index];
                let slot = &self.slots[ring_slot];
                self.queue
                    .write_buffer(&slot.pcm_buffer, 0, bytemuck::cast_slice(pcm));
                if !waveform.is_empty() {
                    self.queue.write_buffer(
                        &slot.waveform_buffer,
                        0,
                        bytemuck::cast_slice(waveform),
                    );
                }
                self.queue
                    .write_buffer(&slot.scene_uniform, 0, &plan.uniform_bytes);
                encode_compute_pass(&mut command_encoder, &self.pipeline, &slot.bind_group, plan);
                slot.conversion
                    .encode(&mut command_encoder, &slot.surface_view);
            }
            let submit_started = Instant::now();
            self.queue.submit(Some(command_encoder.finish()));
            let submit_seconds = submit_started.elapsed().as_secs_f64();
            let last_sequence = prepared
                .last()
                .map(|(sequence, _, _, _, _, _)| *sequence)
                .ok_or_else(|| "direct H264 prepared batch is empty".to_string())?;
            let input_fence_value = last_sequence.saturating_add(1);
            self.nvenc
                .signal_input_fence(&self.device, input_fence_value)?;

            let encode_submit_started = Instant::now();
            let mut pending =
                Vec::<(PendingBitstream, u64, i64, usize)>::with_capacity(prepared.len());
            for (batch_index, (sequence, pts_ns, frame_index, _, _, _)) in
                prepared.iter().enumerate()
            {
                let ring_slot = ring_indices[batch_index];
                let encode_fence = u64::from(*frame_index).saturating_add(1);
                self.ring
                    .mark_encode_submitted(ring_slot, input_fence_value, encode_fence)
                    .map_err(|error| format!("direct H264 ring encode submit failed: {error:?}"))?;
                let submitted = self.nvenc.submit_surface_at(
                    &self.slots[ring_slot].surface,
                    ring_slot,
                    *frame_index,
                    input_fence_value,
                    *frame_index,
                    *frame_index == 0,
                )?;
                pending.push((submitted, *sequence, *pts_ns, ring_slot));
            }
            let encode_submit_seconds = encode_submit_started.elapsed().as_secs_f64();
            let drain_started = Instant::now();
            let mut encoded = Vec::with_capacity(pending.len());
            for (submitted, sequence, pts_ns, ring_slot) in pending {
                let submitted_frame_index = submitted.frame_index;
                let payload = self.nvenc.drain_bitstream(submitted)?;
                self.ring
                    .mark_bitstream_ready(
                        ring_slot,
                        u64::from(submitted_frame_index).saturating_add(1),
                    )
                    .map_err(|error| {
                        format!("direct H264 ring bitstream readiness failed: {error:?}")
                    })?;
                let frame = EncodedH264Frame {
                    schema: crate::contracts::CONTRACT_VERSION,
                    sequence,
                    pts_ns,
                    width: self.width,
                    height: self.height,
                    codec: "h264".into(),
                    profile: "high".into(),
                    backend: "dx12".into(),
                    pixel_readback_bytes: 0,
                    asset_cache_ready: true,
                    asset_receipt: H264AssetReceipt {
                        artwork_hash: self.artwork_hash.clone(),
                        glyph_hash: self.glyph_hash.clone(),
                    },
                    payload,
                };
                frame.validate()?;
                self.ring
                    .release(ring_slot)
                    .map_err(|error| format!("direct H264 ring release failed: {error:?}"))?;
                encoded.push(frame);
            }
            let drain_seconds = drain_started.elapsed().as_secs_f64();
            let encode_seconds = encode_submit_seconds + drain_seconds;
            self.last_sequence = Some(last_sequence);
            self.stage_frame_count += encoded.len() as u64;
            self.stage_frame_total_seconds += frame_started.elapsed().as_secs_f64();
            self.stage_submit_total_seconds += submit_seconds;
            self.stage_nvenc_total_seconds += encode_seconds;
            self.stage_nvenc_submit_total_seconds += encode_submit_seconds;
            self.stage_nvenc_drain_total_seconds += drain_seconds;
            stage_log(format!(
                "rust_h264_batch first_sequence={} frames={} submit_seconds={:.6} nvenc_submit_seconds={:.6} bitstream_drain_seconds={:.6} nvenc_seconds={:.6} total_seconds={:.6}",
                first_sequence,
                encoded.len(),
                submit_seconds,
                encode_submit_seconds,
                drain_seconds,
                encode_seconds,
                frame_started.elapsed().as_secs_f64()
            ));
            Ok(encoded)
        }

        pub fn flush(&mut self) -> Result<(), String> {
            let started = Instant::now();
            if !self.ring.is_empty() {
                return Err("direct H264 flush rejected: render/encode ring is not drained".into());
            }
            let result = self.nvenc.flush();
            if result.is_ok() {
                stage_log(format!(
                    "rust_h264_flush_seconds={:.6}",
                    started.elapsed().as_secs_f64()
                ));
                stage_log(format!(
                    "rust_h264_frame_totals frames={} render_seconds={:.6} submit_seconds={:.6} nvenc_submit_seconds={:.6} bitstream_drain_seconds={:.6} nvenc_seconds={:.6}",
                    self.stage_frame_count,
                    self.stage_frame_total_seconds,
                    self.stage_submit_total_seconds,
                    self.stage_nvenc_submit_total_seconds,
                    self.stage_nvenc_drain_total_seconds,
                    self.stage_nvenc_total_seconds
                ));
            }
            result
        }

        fn refresh_glyph_binding(&mut self, scene: &MusicScenePayload) -> Result<(), String> {
            let glyph_atlas = glyph_atlas_from_scene(scene)?;
            let glyph_atlas_texture =
                create_glyph_atlas_texture(&self.device, &self.queue, &glyph_atlas);
            let glyph_atlas_view =
                glyph_atlas_texture.create_view(&wgpu::TextureViewDescriptor::default());
            let glyph_metrics = create_glyph_metrics_buffer(&self.device, &glyph_atlas);
            let mut bind_groups = Vec::with_capacity(self.slots.len());
            for slot in &self.slots {
                bind_groups.push(create_bind_group(
                    &self.device,
                    &self.layout,
                    H264_SHADER_MODULE.descriptor(),
                    ShaderGpuResources {
                        pcm_buffer: &slot.pcm_buffer,
                        scene_uniform: &slot.scene_uniform,
                        waveform_buffer: &slot.waveform_buffer,
                        glyph_atlas: &glyph_atlas_view,
                        glyph_metrics: &glyph_metrics,
                        glyph_sampler: &self.glyph_sampler,
                        artwork_texture: &self.artwork_view,
                        luma_output: &slot.luma_view,
                        chroma_output: &slot.chroma_view,
                        rgb_output: &slot.rgb_view,
                    },
                )?);
            }
            self.glyph_atlas_texture = glyph_atlas_texture;
            self.glyph_atlas_view = glyph_atlas_view;
            self.glyph_metrics = glyph_metrics;
            for (slot, bind_group) in self.slots.iter_mut().zip(bind_groups) {
                slot.bind_group = bind_group;
            }
            self.glyph_hash = scene
                .glyph_atlas
                .as_ref()
                .map(|atlas| atlas.asset_hash.clone())
                .unwrap_or_default();
            Ok(())
        }
    }

    fn pcm_from_scene(scene: &MusicScenePayload) -> Result<Vec<f32>, String> {
        if scene.pcm_f32le.len() != MUSIC_PCM_WINDOW_BYTES {
            return Err("direct H264 PCM payload must contain exactly 4096 float32 samples".into());
        }
        let mut samples = Vec::with_capacity(MUSIC_PCM_WINDOW_SAMPLES);
        for chunk in scene.pcm_f32le.chunks_exact(4) {
            let sample = f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]);
            if !sample.is_finite() || !(-1.0..=1.0).contains(&sample) {
                return Err("direct H264 PCM payload contains an invalid normalized sample".into());
            }
            samples.push(sample);
        }
        Ok(samples)
    }

    fn validate_scene_inputs(
        width: u32,
        height: u32,
        fps: u32,
        scene: &MusicScenePayload,
    ) -> Result<(), String> {
        let ((_, _), _) = shader_output_dimensions(width, height)?;
        if fps == 0 || scene.schema != MUSIC_SCENE_SCHEMA {
            return Err("direct H264 scene header is invalid".into());
        }
        if scene.pcm_f32le.len() != MUSIC_PCM_WINDOW_BYTES {
            return Err("direct H264 scene requires one real 4096-sample PCM window".into());
        }
        scene
            .validate()
            .map_err(|error| format!("invalid direct H264 scene: {error:?}"))?;
        if scene.feature.spectrum_q16.len() != 24 {
            return Err("direct H264 scene requires exactly 24 spectrum bands".into());
        }
        if scene.dynamics.loudness_envelope.len() != 1000 {
            return Err("direct H264 scene requires exactly 1000 loudness samples".into());
        }
        Ok(())
    }

    fn scene_frame_request(
        width: u32,
        height: u32,
        fps: u32,
        frame_index: u32,
        scene: &MusicScenePayload,
    ) -> Result<ShaderFrameRequest, String> {
        let duration_frames = if scene.dynamics.duration_seconds.is_finite()
            && scene.dynamics.duration_seconds > 0.0
        {
            (scene.dynamics.duration_seconds * f64::from(fps)).ceil() as u64
        } else {
            0
        };
        let total_frames = duration_frames
            .max(u64::from(frame_index).saturating_add(1))
            .min(u64::from(u32::MAX)) as u32;
        let scale_x = width as f64 / 1280.0;
        let scale_y = height as f64 / 720.0;
        let rect = |r: &crate::contracts::SceneRect| {
            [
                (f64::from(r.x) * scale_x).round() as i32,
                (f64::from(r.y) * scale_y).round() as i32,
                (f64::from(r.w) * scale_x).round() as i32,
                (f64::from(r.h) * scale_y).round() as i32,
            ]
        };
        let mut spectrum = [0u16; 24];
        spectrum.copy_from_slice(&scene.feature.spectrum_q16);
        let waveform = scene.feature.waveform_q16.clone();
        let mut data =
            ShaderSceneData::cpu_parity_default(width, height, frame_index, total_frames, fps)
                .with_text_lines(
                    scene
                        .text_overlay
                        .as_ref()
                        .map(|text| text.title.as_str())
                        .unwrap_or_default(),
                    scene
                        .text_overlay
                        .as_ref()
                        .map(|text| text.artist.as_str())
                        .unwrap_or_default(),
                    scene
                        .text_overlay
                        .as_ref()
                        .map(|text| text.album.as_str())
                        .unwrap_or_default(),
                    &format_timestamp(scene.dynamics.current_seconds),
                )
                .with_waveform_q16(waveform, !scene.feature.waveform_q16.is_empty());
        data.palette = [
            scene.palette.primary,
            scene.palette.accent,
            scene.palette.background,
            scene.palette.overlay,
        ];
        data.rects = [
            rect(&scene.layout.artwork),
            rect(&scene.layout.title),
            rect(&scene.layout.artist),
            rect(&scene.layout.album),
            rect(&scene.layout.spectrum),
            rect(&scene.layout.loudness),
            rect(&scene.layout.progress),
            rect(&scene.layout.time),
        ];
        data.spectrum_q16 = spectrum;
        data.loudness_q16 = scene.dynamics.loudness_envelope.clone();
        data.progress_q16 = quantize_unit(scene.dynamics.progress_ratio);
        data.fade_in_q16 = quantize_unit(f64::from(scene.dynamics.edge_fade_alpha));
        data.fade_out_q16 = quantize_unit(f64::from(scene.dynamics.end_fade_alpha));
        Ok(ShaderFrameRequest {
            width,
            height,
            frame_index,
            pcm_sample_count: MUSIC_PCM_WINDOW_SAMPLES as u32,
            pcm_sample_offset: 0,
            scene: data,
        })
    }

    fn quantize_unit(value: f64) -> u32 {
        (value.clamp(0.0, 1.0) * f64::from(u16::MAX)).round() as u32
    }

    fn format_timestamp(seconds: f64) -> String {
        let total = if seconds.is_finite() && seconds >= 0.0 {
            seconds.floor() as u64
        } else {
            0
        };
        format!("{}:{:02}", total / 60, total % 60)
    }

    fn tight_rgba_payload(
        width: u32,
        height: u32,
        row_stride: u32,
        format: PixelFormat,
        payload: &[u8],
        label: &str,
    ) -> Result<Vec<u8>, String> {
        if format != PixelFormat::Rgba8
            || width == 0
            || height == 0
            || row_stride < width.saturating_mul(4)
        {
            return Err(format!(
                "direct H264 {label} payload format or geometry is invalid"
            ));
        }
        let source_len = usize::try_from(row_stride)
            .ok()
            .and_then(|stride| stride.checked_mul(height as usize))
            .ok_or_else(|| format!("direct H264 {label} payload size overflow"))?;
        if payload.len() != source_len {
            return Err(format!(
                "direct H264 {label} payload length does not match row stride"
            ));
        }
        let tight_stride = usize::try_from(width).unwrap() * 4;
        let mut output = vec![0u8; tight_stride * height as usize];
        for row in 0..height as usize {
            let source =
                &payload[row * row_stride as usize..row * row_stride as usize + tight_stride];
            output[row * tight_stride..(row + 1) * tight_stride].copy_from_slice(source);
        }
        Ok(output)
    }

    fn glyph_atlas_from_scene(scene: &MusicScenePayload) -> Result<GlyphAtlas, String> {
        let metadata = scene
            .glyph_atlas
            .as_ref()
            .ok_or("direct H264 glyph atlas is required")?;
        if metadata.width == 0 || metadata.height == 0 || metadata.payload.is_empty() {
            return Err("direct H264 glyph atlas metadata is empty".into());
        }
        let title = scene
            .text_overlay
            .as_ref()
            .map(|text| text.title.as_str())
            .unwrap_or_default();
        let artist = scene
            .text_overlay
            .as_ref()
            .map(|text| text.artist.as_str())
            .unwrap_or_default();
        let timestamp = format_timestamp(scene.dynamics.current_seconds);
        // The Go scene carries a compact, single-row overlay atlas. The shader
        // contract requires the resident three-style fixed atlas, so rebuild it
        // from the same scene text rather than copying the compact row into the
        // wrong style layer.
        build_atlas_for_text([title, artist, "", timestamp.as_str()])
    }
}

#[cfg(windows)]
pub use windows_impl::{probe_capability, DirectNvencRenderer};

#[cfg(not(windows))]
pub fn probe_capability() -> bool {
    false
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contracts::{ColorSpace, GlyphEntry, TextRun};

    fn artwork(payload: bool, hash: &str) -> ArtworkMetadata {
        let bytes = if payload { vec![1; 256] } else { vec![] };
        let actual_hash = if payload {
            let mut digest = Sha256::new();
            digest.update(&bytes);
            digest
                .finalize()
                .iter()
                .map(|byte| format!("{byte:02x}"))
                .collect()
        } else {
            hash.to_string()
        };
        ArtworkMetadata {
            texture_id: "cover".into(),
            width: 1,
            height: 1,
            row_stride: 256,
            format: PixelFormat::Rgba8,
            color_space: ColorSpace::Srgb,
            alpha: true,
            payload: bytes,
            asset_hash: actual_hash,
        }
    }

    #[test]
    fn draft_shader_is_not_allowed_without_explicit_diagnostic_override() {
        assert!(!direct_h264_shader_allowed(false, false));
        assert!(direct_h264_shader_allowed(false, true));
        assert!(direct_h264_shader_allowed(true, false));
    }

    fn glyph(payload: bool, hash: &str) -> GlyphAtlasMetadata {
        let bytes = if payload { vec![2; 256] } else { vec![] };
        let actual_hash = if payload {
            let mut digest = Sha256::new();
            digest.update(&bytes);
            digest
                .finalize()
                .iter()
                .map(|byte| format!("{byte:02x}"))
                .collect()
        } else {
            hash.to_string()
        };
        GlyphAtlasMetadata {
            texture_id: "atlas".into(),
            font_family: "sans".into(),
            font_weight: 400,
            fallback_order: vec![],
            width: 1,
            height: 1,
            row_stride: 256,
            glyph_count: 1,
            missing_glyph_id: "tofu".into(),
            payload: bytes,
            glyphs: vec![GlyphEntry {
                id: "tofu".into(),
                x: 0,
                y: 0,
                width: 1,
                height: 1,
                advance: 1.0,
            }],
            text_runs: vec![TextRun {
                text: "T".into(),
                x: 0.0,
                y: 0.0,
                size_px: 1.0,
                rgba: [255; 4],
                opacity: 1.0,
                font_family: "sans".into(),
                font_weight: 400,
            }],
            asset_hash: actual_hash,
        }
    }

    #[test]
    fn session_cache_accepts_same_hash_compact_frame_and_rejects_changes() {
        let mut cache = SessionAssetCache::default();
        let artwork_initial = artwork(true, &"a".repeat(64));
        let artwork_hash = artwork_initial.asset_hash.clone();
        let glyph_initial = glyph(true, &"b".repeat(64));
        let glyph_hash = glyph_initial.asset_hash.clone();
        cache.accept_artwork(&artwork_initial, true).unwrap();
        cache.accept_glyph(&glyph_initial, true).unwrap();
        cache
            .accept_artwork(&artwork(false, &artwork_hash), false)
            .unwrap();
        assert!(!cache
            .accept_glyph(&glyph(false, &glyph_hash), false)
            .unwrap());

        let changed_artwork = artwork(false, &"c".repeat(64));
        assert!(cache.accept_artwork(&changed_artwork, false).is_err());
        let changed_glyph = glyph(false, &"d".repeat(64));
        assert!(cache.accept_glyph(&changed_glyph, false).is_err());
    }

    #[test]
    fn session_cache_rejects_first_compact_frame() {
        let mut cache = SessionAssetCache::default();
        let hash = "a".repeat(64);
        assert!(cache.accept_artwork(&artwork(false, &hash), true).is_err());
        assert!(cache.accept_glyph(&glyph(false, &hash), true).is_err());
    }

    #[test]
    fn session_cache_rejects_payload_hash_mismatch() {
        let mut cache = SessionAssetCache::default();
        let mut bad_artwork = artwork(true, &"a".repeat(64));
        bad_artwork.asset_hash = "a".repeat(64);
        assert!(cache.accept_artwork(&bad_artwork, true).is_err());

        let mut bad_glyph = glyph(true, &"b".repeat(64));
        bad_glyph.asset_hash = "b".repeat(64);
        assert!(cache.accept_glyph(&bad_glyph, true).is_err());
    }
}

#[cfg(not(windows))]
pub struct DirectNvencRenderer;

#[cfg(not(windows))]
impl DirectNvencRenderer {
    pub fn new(_: u32, _: u32, _: u32, _: &MusicScenePayload) -> Result<Self, String> {
        Err("direct D3D12/NVENC sidecar is only available on Windows".into())
    }

    pub fn ensure_playlist_pipeline(&mut self) -> Result<(), String> {
        Err("playlist D3D12/wgpu pipeline is only available on Windows".into())
    }

    pub fn prepare_playlist_static_textures(
        &mut self,
        _: &crate::protocol::TrackAssets,
    ) -> Result<(), String> {
        Err("playlist D3D12/wgpu static textures are only available on Windows".into())
    }

    pub fn prepare_playlist_spectrum_buffer(
        &mut self,
        _: &crate::protocol::TimelineChunk,
    ) -> Result<(), String> {
        Err("playlist D3D12/wgpu spectrum buffer is only available on Windows".into())
    }

    pub fn ensure_playlist_frame_slots(&mut self) -> Result<(), String> {
        Err("playlist D3D12/wgpu frame slots are only available on Windows".into())
    }

    pub fn submit_playlist_timeline(
        &mut self,
        _: &[crate::playlist_shader_timeline::PlaylistFrameUniform],
    ) -> Result<(), String> {
        Err("playlist D3D12/wgpu compute submit is only available on Windows".into())
    }

    pub fn drain_playlist_timeline(&mut self) -> Result<Vec<EncodedH264Frame>, String> {
        Err("playlist D3D12/wgpu bitstream drain is only available on Windows".into())
    }

    pub fn encode_playlist_timeline(
        &mut self,
        _: &[crate::playlist_shader_timeline::PlaylistFrameUniform],
    ) -> Result<Vec<EncodedH264Frame>, String> {
        Err("playlist D3D12/wgpu NVENC encode is only available on Windows".into())
    }

    pub fn render_frame(
        &mut self,
        _: u64,
        _: i64,
        _: &MusicScenePayload,
    ) -> Result<EncodedH264Frame, String> {
        Err("direct D3D12/NVENC sidecar is only available on Windows".into())
    }
    pub fn reset_playlist_session(&mut self) -> Result<(), String> {
        Err("direct D3D12/NVENC sidecar is only available on Windows".into())
    }

    pub fn render_batch(
        &mut self,
        _: &[(&MusicScenePayload, u64, i64)],
    ) -> Result<Vec<EncodedH264Frame>, String> {
        Err("direct D3D12/NVENC sidecar is only available on Windows".into())
    }

    pub fn flush(&mut self) -> Result<(), String> {
        Err("direct D3D12/NVENC sidecar is only available on Windows".into())
    }
}
