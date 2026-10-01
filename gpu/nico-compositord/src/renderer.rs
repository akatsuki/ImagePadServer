use std::collections::{HashMap, VecDeque};
use std::fmt;
use std::mem::size_of;
use std::sync::{Arc, Mutex};
use std::time::Instant;

use bytemuck::{Pod, Zeroable};
use serde::Serialize;
use sha2::{Digest, Sha256};
use wgpu::util::DeviceExt;

use crate::atlas::{plan_atlas, AssetExtent, AtlasDecision, AtlasRegion, SeparateReason};
use crate::motion_precision::{encode_motion_payload, MotionPrecisionError};
use crate::protocol::{frame_vpos, Draw, Scene};
use crate::scene::SceneIndex;
use crate::stream_budget::{CpuBudget, CpuCredit};
use crate::stream_protocol::Element;
use crate::stream_renderer::{GpuBudget, GpuCredit, UploadRetirement};

const MAX_EXACT_F32_INT: i64 = 1 << 24;
// At the 100,000-Draw wire cap: 12.8 MiB retained Draws, about 11.2 MiB for
// replacement SceneIndex build overlap, 2.4 MiB of candidate/index vectors,
// and about 12.4 MiB for bounded GpuDraw + texture-run scratch. The remainder
// covers Vec capacity rounding and small generation/index metadata.
const RENDERER_DRAW_METADATA_RESERVATION_BYTES: usize = 48 * 1024 * 1024;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RendererError(String);

impl RendererError {
    fn invalid(message: impl Into<String>) -> Self {
        Self(message.into())
    }
}

impl fmt::Display for RendererError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for RendererError {}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AssetLayoutMode {
    Separate,
    Atlas,
}

impl AssetLayoutMode {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Separate => "separate",
            Self::Atlas => "atlas",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AssetLayoutReport {
    pub requested: AssetLayoutMode,
    pub actual: AssetLayoutMode,
    pub fallback_reason: Option<String>,
    pub page_count: usize,
    pub source_bytes: u64,
    pub allocated_bytes: u64,
    pub nonzero_origin_count: usize,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PageOccupancy {
    pub page_index: usize,
    pub width: u32,
    pub height: u32,
    pub used_texels: u64,
    pub allocated_texels: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ResidentMemoryReport {
    /// Decoded RGBA bytes retained in the NCT1 scene.
    pub decoded_scene_pixel_bytes: Option<u64>,
    /// Logical RGBA8 bytes requested for page textures; not physical VRAM.
    pub logical_texture_allocation_bytes: Option<u64>,
    /// Exact current cached GpuDraw payload byte count; excludes opaque bundle internals.
    pub renderer_owned_draw_buffer_payload_bytes: Option<u64>,
    /// WGPU does not expose the implementation's render-bundle storage size.
    pub render_bundle_internal_bytes: Option<u64>,
    /// Exact readback ring buffer sizes from padded row pitch × height × slots.
    pub readback_ring_buffer_bytes: Option<u64>,
    /// WGPU internal upload staging allocation is not observable through this API.
    pub wgpu_transient_staging_bytes: Option<u64>,
    /// Physical residency is driver-managed and not observable through WGPU.
    pub physical_vram_bytes: Option<u64>,
    pub unknown_reasons: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AssetTelemetryReport {
    pub telemetry_scope: &'static str,
    pub effective_layout: &'static str,
    pub pages: Option<Vec<PageOccupancy>>,
    pub texture_upload_call_count: Option<u64>,
    pub texture_upload_bytes: Option<u64>,
    pub draw_order_page_bind_run_count: Option<u64>,
    /// Host CPU elapsed wall time for preparing and finishing render bundles; not GPU time.
    pub bundle_build_cpu_wall_time_ns: Option<u64>,
    pub resident_memory: ResidentMemoryReport,
    pub unavailable_reasons: Vec<String>,
}

impl AssetTelemetryReport {
    pub fn unavailable_for_streamed_assets(reason: &str) -> Self {
        let reason = reason.to_owned();
        Self {
            telemetry_scope: "nct2-streamed-assets",
            effective_layout: "streamed-distinct-textures",
            pages: None,
            texture_upload_call_count: None,
            texture_upload_bytes: None,
            draw_order_page_bind_run_count: None,
            bundle_build_cpu_wall_time_ns: None,
            resident_memory: ResidentMemoryReport {
                decoded_scene_pixel_bytes: None,
                logical_texture_allocation_bytes: None,
                renderer_owned_draw_buffer_payload_bytes: None,
                render_bundle_internal_bytes: None,
                readback_ring_buffer_bytes: None,
                wgpu_transient_staging_bytes: None,
                physical_vram_bytes: None,
                unknown_reasons: vec![reason.clone()],
            },
            unavailable_reasons: vec![reason],
        }
    }
}

fn page_occupancies(
    pages: &[crate::atlas::AtlasPage],
    regions: &HashMap<u32, GpuRegion>,
) -> Result<Vec<PageOccupancy>, RendererError> {
    let mut used = vec![0_u64; pages.len()];
    for region in regions.values() {
        let page = pages.get(region.page as usize).ok_or_else(|| {
            RendererError::invalid(format!(
                "asset region references missing page {}",
                region.page
            ))
        })?;
        let right = region.x.checked_add(region.width);
        let bottom = region.y.checked_add(region.height);
        if right.map_or(true, |right| right > page.width)
            || bottom.map_or(true, |bottom| bottom > page.height)
        {
            return Err(RendererError::invalid(
                "asset region extends beyond its page bounds",
            ));
        }
        let page_used = used.get_mut(region.page as usize).ok_or_else(|| {
            RendererError::invalid(format!(
                "asset region references missing page {}",
                region.page
            ))
        })?;
        let area = u64::from(region.width)
            .checked_mul(u64::from(region.height))
            .ok_or_else(|| RendererError::invalid("asset region texel count overflow"))?;
        *page_used = page_used
            .checked_add(area)
            .ok_or_else(|| RendererError::invalid("page used texel count overflow"))?;
    }
    pages
        .iter()
        .enumerate()
        .map(|(page_index, page)| {
            let allocated_texels = u64::from(page.width)
                .checked_mul(u64::from(page.height))
                .ok_or_else(|| RendererError::invalid("page allocated texel count overflow"))?;
            Ok(PageOccupancy {
                page_index,
                width: page.width,
                height: page.height,
                used_texels: used[page_index],
                allocated_texels,
            })
        })
        .collect()
}

fn asset_layout_telemetry(
    scene: &Scene,
    pages: &[crate::atlas::AtlasPage],
    regions: &HashMap<u32, GpuRegion>,
    draws: &[Draw],
    report: &AssetLayoutReport,
) -> Result<AssetTelemetryReport, RendererError> {
    let pages_report = page_occupancies(pages, regions)?;
    let mut upload_bytes = 0_u64;
    for asset in &scene.assets {
        upload_bytes = upload_bytes
            .checked_add(asset.rgba.len() as u64)
            .ok_or_else(|| RendererError::invalid("texture upload byte count overflow"))?;
    }
    let mut run_count = 0_u64;
    let mut previous_page = None;
    for draw in draws {
        let region = regions.get(&draw.asset_id).ok_or_else(|| {
            RendererError::invalid(format!("draw references missing asset {}", draw.asset_id))
        })?;
        if previous_page != Some(region.page) {
            run_count = run_count
                .checked_add(1)
                .ok_or_else(|| RendererError::invalid("page bind run count overflow"))?;
            previous_page = Some(region.page);
        }
    }
    Ok(AssetTelemetryReport {
        telemetry_scope: "nct1-static-assets",
        effective_layout: report.actual.as_str(),
        pages: Some(pages_report),
        texture_upload_call_count: Some(scene.assets.len() as u64),
        texture_upload_bytes: Some(upload_bytes),
        draw_order_page_bind_run_count: Some(run_count),
        bundle_build_cpu_wall_time_ns: Some(0),
        resident_memory: ResidentMemoryReport {
            decoded_scene_pixel_bytes: Some(report.source_bytes),
            logical_texture_allocation_bytes: Some(report.allocated_bytes),
            renderer_owned_draw_buffer_payload_bytes: Some(0),
            render_bundle_internal_bytes: None,
            readback_ring_buffer_bytes: None,
            wgpu_transient_staging_bytes: None,
            physical_vram_bytes: None,
            unknown_reasons: vec![
                "WGPU render-bundle internal storage is not exposed by the API".to_owned(),
                "WGPU internal upload staging allocation is not exposed by the API".to_owned(),
                "Physical VRAM residency is driver-managed and not exposed by WGPU".to_owned(),
            ],
        },
        unavailable_reasons: Vec::new(),
    })
}

fn readback_ring_buffer_bytes(width: u32, height: u32, slots: usize) -> Result<u64, RendererError> {
    let row_bytes = crate::readback::padded_bytes_per_row(width)
        .map_err(|error| RendererError::invalid(error.to_string()))?;
    u64::from(row_bytes)
        .checked_mul(u64::from(height))
        .and_then(|bytes| bytes.checked_mul(slots as u64))
        .ok_or_else(|| RendererError::invalid("readback ring allocation size overflow"))
}

#[derive(Clone, Debug)]
pub struct GpuOptions {
    pub backends: wgpu::Backends,
    pub allow_software: bool,
}

impl Default for GpuOptions {
    fn default() -> Self {
        Self {
            backends: wgpu::Backends::all(),
            allow_software: false,
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Pod, Zeroable)]
struct FrameUniform {
    vpos: i32,
    padding: [i32; 3],
}

#[repr(C)]
#[derive(Clone, Copy, Pod, Zeroable)]
struct GpuDraw {
    rect: [f32; 4],
    projection: [[f32; 4]; 4],
    interval: [i32; 4],
    movement: [f32; 2],
    texture_extent_packed: u32,
    padding: u32,
    motion_precision: [u32; 4],
}

struct GpuPage {
    _texture: wgpu::Texture,
    bind_group: wgpu::BindGroup,
}

struct StreamGpuAsset {
    region: GpuRegion,
    bind_group: wgpu::BindGroup,
    _owner: Arc<dyn Send + Sync>,
}

struct StreamAssetRegistry<T> {
    assets: HashMap<u32, Arc<T>>,
}

impl<T> StreamAssetRegistry<T> {
    fn new() -> Self {
        Self {
            assets: HashMap::new(),
        }
    }

    fn register(&mut self, id: u32, asset: Arc<T>) -> Result<(), RendererError> {
        if self.assets.contains_key(&id) {
            return Err(RendererError::invalid(format!(
                "duplicate stream asset id {id}"
            )));
        }
        self.assets.insert(id, asset);
        Ok(())
    }

    fn get(&self, id: u32) -> Option<&Arc<T>> {
        self.assets.get(&id)
    }

    fn clear(&mut self) {
        self.assets.clear();
    }
}

#[derive(Clone, Copy, Debug)]
struct GpuRegion {
    page: u32,
    x: u32,
    y: u32,
    width: u32,
    height: u32,
}

struct CachedSecond {
    second: i32,
    generation: u64,
    bundle: Option<wgpu::RenderBundle>,
    // A bundle owns its referenced GPU resources. Holding the backing buffer and
    // bind group as well makes that lifetime explicit and keeps debug validation
    // independent of backend handle retention details.
    _draw_buffer: Option<wgpu::Buffer>,
    draw_buffer_bytes: u64,
    _draw_bind_group: Option<wgpu::BindGroup>,
    _draw_generation: Arc<SceneGeneration>,
    _draw_credit: Option<GpuCredit>,
    _stream_assets: Vec<Arc<StreamGpuAsset>>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum TextureBindingKey {
    StaticPage(u32),
    StreamAsset(u32),
}

fn contiguous_binding_runs(keys: &[TextureBindingKey]) -> Vec<(TextureBindingKey, usize, usize)> {
    let mut runs = Vec::new();
    let mut start = 0;
    while start < keys.len() {
        let key = keys[start];
        let mut end = start + 1;
        while end < keys.len() && keys[end] == key {
            end += 1;
        }
        runs.push((key, start, end));
        start = end;
    }
    runs
}

/// Immutable marker for the ordered Draw prefix a frame was encoded from.
#[derive(Debug)]
struct SceneGeneration {
    id: u64,
}

impl SceneGeneration {
    fn new(id: u64) -> Self {
        Self { id }
    }
}

fn append_element_draws(
    draws: &mut Vec<Draw>,
    next_ordinal: &mut u32,
    cpu_budget: &CpuBudget,
    draw_metadata_credit: &mut Option<CpuCredit>,
    element: &Element,
) -> Result<(), RendererError> {
    if element.ordinal != *next_ordinal {
        return Err(RendererError::invalid(format!(
            "stream Element ordinal {}, expected {}",
            element.ordinal, *next_ordinal
        )));
    }
    let draw_key = |draw: &Draw| (draw.owner_order, draw.comment_index, draw.primitive_index);
    if element
        .draws
        .windows(2)
        .any(|pair| draw_key(&pair[0]) > draw_key(&pair[1]))
    {
        return Err(RendererError::invalid(
            "stream Element Draws are not ordered by owner, comment, and primitive",
        ));
    }
    if let (Some(previous), Some(first)) = (draws.last(), element.draws.first()) {
        if draw_key(previous) > draw_key(first) {
            return Err(RendererError::invalid(
                "stream Element Draws regress owner, comment, or primitive order",
            ));
        }
    }
    let next = (*next_ordinal)
        .checked_add(1)
        .ok_or_else(|| RendererError::invalid("stream Element ordinal overflow"))?;
    if !element.draws.is_empty() {
        let max_draws = crate::protocol::MAX_DRAWS as usize;
        let new_len = draws
            .len()
            .checked_add(element.draws.len())
            .ok_or_else(|| RendererError::invalid("stream Draw count overflow"))?;
        if new_len > max_draws {
            return Err(RendererError::invalid(
                "stream Draw count exceeds the NCT2 limit",
            ));
        }
        let pending_credit = if draw_metadata_credit.is_none() {
            Some(reserve_renderer_draw_metadata(cpu_budget)?)
        } else {
            None
        };
        if draws.capacity() < max_draws {
            if let Err(error) = draws.try_reserve_exact(max_draws - draws.len()) {
                drop(pending_credit);
                return Err(RendererError::invalid(format!(
                    "reserving bounded stream Draw storage: {error}"
                )));
            }
        }
        if pending_credit.is_some() {
            *draw_metadata_credit = pending_credit;
        }
    }
    draws.extend(element.draws.iter().cloned());
    *next_ordinal = next;
    Ok(())
}

fn apply_element_state(
    draws: &mut Vec<Draw>,
    scene_index: &mut SceneIndex,
    cached_seconds: &mut VecDeque<Arc<CachedSecond>>,
    next_ordinal: &mut u32,
    generation: &mut Arc<SceneGeneration>,
    cpu_budget: &CpuBudget,
    draw_metadata_credit: &mut Option<CpuCredit>,
    element: &Element,
) -> Result<(), RendererError> {
    let next_generation = generation
        .id
        .checked_add(1)
        .ok_or_else(|| RendererError::invalid("scene generation overflow"))?;
    append_element_draws(
        draws,
        next_ordinal,
        cpu_budget,
        draw_metadata_credit,
        element,
    )?;
    *generation = Arc::new(SceneGeneration::new(next_generation));
    scene_index.rebuild(draws);
    cached_seconds.clear();
    Ok(())
}

fn reserve_renderer_draw_metadata(budget: &CpuBudget) -> Result<CpuCredit, RendererError> {
    budget
        .try_reserve(RENDERER_DRAW_METADATA_RESERVATION_BYTES)
        .map_err(|error| RendererError::invalid(error.to_string()))
}

fn retire_after_submission<T: Send + Sync + 'static>(
    resource: Arc<T>,
) -> impl FnOnce() + Send + 'static {
    move || drop(resource)
}

fn write_uniform_then_submit<T>(write_uniform: impl FnOnce(), submit: impl FnOnce() -> T) -> T {
    write_uniform();
    submit()
}

pub struct Renderer {
    device: wgpu::Device,
    queue: wgpu::Queue,
    adapter_info: wgpu::AdapterInfo,
    header_width: u32,
    header_height: u32,
    frame_count: u32,
    fps_num: u32,
    fps_den: u32,
    draws: Vec<Draw>,
    scene_index: SceneIndex,
    // Acquired before first streamed Draw allocation and kept after Draw and
    // SceneIndex fields drop. Covers at most 100k Draws, SceneIndex rebuild and
    // two-second candidate metadata, plus bounded per-second GPU-draw scratch.
    draw_metadata_credit: Option<CpuCredit>,
    next_element_ordinal: u32,
    generation: Arc<SceneGeneration>,
    generation_budget: GpuBudget,
    asset_regions: HashMap<u32, GpuRegion>,
    stream_assets: StreamAssetRegistry<StreamGpuAsset>,
    pages: Vec<GpuPage>,
    asset_layout_report: AssetLayoutReport,
    static_asset_telemetry: AssetTelemetryReport,
    bundle_build_cpu_wall_time_ns: u64,
    frame_buffer: wgpu::Buffer,
    draw_layout: wgpu::BindGroupLayout,
    texture_layout: wgpu::BindGroupLayout,
    sampler: wgpu::Sampler,
    pipeline: wgpu::RenderPipeline,
    cached_seconds: VecDeque<Arc<CachedSecond>>,
    stream_upload_completions: Arc<Mutex<Vec<u64>>>,
    next_stream_upload_token: u64,
}

impl Renderer {
    pub fn new(scene: &Scene, options: &GpuOptions) -> Result<Self, RendererError> {
        Self::new_with_asset_layout(scene, options, AssetLayoutMode::Separate)
    }

    pub fn new_with_asset_layout(
        scene: &Scene,
        options: &GpuOptions,
        requested_layout: AssetLayoutMode,
    ) -> Result<Self, RendererError> {
        Self::new_with_asset_layout_and_gpu_budget(
            scene,
            options,
            requested_layout,
            GpuBudget::new(),
        )
    }

    pub fn new_with_gpu_budget(
        scene: &Scene,
        options: &GpuOptions,
        generation_budget: GpuBudget,
    ) -> Result<Self, RendererError> {
        Self::new_with_asset_layout_and_gpu_budget(
            scene,
            options,
            AssetLayoutMode::Separate,
            generation_budget,
        )
    }

    pub fn new_with_asset_layout_and_gpu_budget(
        scene: &Scene,
        options: &GpuOptions,
        requested_layout: AssetLayoutMode,
        generation_budget: GpuBudget,
    ) -> Result<Self, RendererError> {
        validate_scene_for_render(scene)?;

        let max_dimension = scene.assets.iter().fold(
            scene.header.width.max(scene.header.height),
            |largest, asset| largest.max(asset.width).max(asset.height),
        );
        let draw_storage_bytes = (scene.draws.len().max(1) * size_of::<GpuDraw>()) as u64;
        let draw_storage_binding_bytes = u32::try_from(draw_storage_bytes).map_err(|_| {
            RendererError::invalid("draw storage buffer exceeds the WGPU u32 binding limit")
        })?;

        let instance = wgpu::Instance::new(wgpu::InstanceDescriptor {
            backends: options.backends,
            ..Default::default()
        });
        let request = |force_fallback_adapter| {
            pollster::block_on(instance.request_adapter(&wgpu::RequestAdapterOptions {
                power_preference: wgpu::PowerPreference::HighPerformance,
                compatible_surface: None,
                force_fallback_adapter,
            }))
        };
        let adapter = request(false)
            .or_else(|| options.allow_software.then(|| request(true)).flatten())
            .ok_or_else(|| RendererError::invalid("no WGPU adapter is available"))?;
        let adapter_info = adapter.get_info();
        if adapter_info.device_type == wgpu::DeviceType::Cpu && !options.allow_software {
            return Err(RendererError::invalid(format!(
                "software WGPU adapter is disabled: {}",
                adapter_info.name
            )));
        }

        let adapter_limits = adapter.limits();
        validate_adapter_limits(
            &adapter_info,
            &adapter_limits,
            max_dimension,
            draw_storage_bytes,
            draw_storage_binding_bytes,
        )?;
        let (page_specs, asset_regions, asset_layout_report) = prepare_asset_layout(
            scene,
            requested_layout,
            adapter_limits.max_texture_dimension_2d,
        )?;

        // No GPU buffers or textures are created before the requested output,
        // source images, and draw storage have been checked against the adapter.
        let (device, queue) = pollster::block_on(adapter.request_device(
            &wgpu::DeviceDescriptor {
                label: Some("nico comment timeline renderer"),
                required_features: wgpu::Features::empty(),
                required_limits: adapter_limits,
            },
            None,
        ))
        .map_err(|error| RendererError::invalid(format!("requesting WGPU device: {error}")))?;

        let draw_layout = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
            label: Some("NCT1 draws and frame vpos"),
            entries: &[
                wgpu::BindGroupLayoutEntry {
                    binding: 0,
                    visibility: wgpu::ShaderStages::VERTEX,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Storage { read_only: true },
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 1,
                    visibility: wgpu::ShaderStages::VERTEX,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Uniform,
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
            ],
        });
        let texture_layout = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
            label: Some("NCT1 premultiplied comment texture"),
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
        let frame_buffer = device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
            label: Some("NCT1 frame vpos uniform"),
            contents: bytemuck::bytes_of(&FrameUniform {
                vpos: 0,
                padding: [0; 3],
            }),
            usage: wgpu::BufferUsages::UNIFORM | wgpu::BufferUsages::COPY_DST,
        });

        let sampler = device.create_sampler(&wgpu::SamplerDescriptor {
            label: Some("NCT1 linear clamp sampler"),
            address_mode_u: wgpu::AddressMode::ClampToEdge,
            address_mode_v: wgpu::AddressMode::ClampToEdge,
            address_mode_w: wgpu::AddressMode::ClampToEdge,
            mag_filter: wgpu::FilterMode::Linear,
            min_filter: wgpu::FilterMode::Linear,
            mipmap_filter: wgpu::FilterMode::Linear,
            ..Default::default()
        });
        let mut pages = Vec::with_capacity(page_specs.len());
        for page in &page_specs {
            let texture = device.create_texture(&wgpu::TextureDescriptor {
                label: Some("NCT1 premultiplied RGBA comment atlas page"),
                size: wgpu::Extent3d {
                    width: page.width,
                    height: page.height,
                    depth_or_array_layers: 1,
                },
                mip_level_count: 1,
                sample_count: 1,
                dimension: wgpu::TextureDimension::D2,
                format: wgpu::TextureFormat::Rgba8Unorm,
                usage: wgpu::TextureUsages::TEXTURE_BINDING | wgpu::TextureUsages::COPY_DST,
                view_formats: &[],
            });
            let view = texture.create_view(&wgpu::TextureViewDescriptor::default());
            let bind_group = device.create_bind_group(&wgpu::BindGroupDescriptor {
                label: Some("NCT1 comment atlas page and sampler"),
                layout: &texture_layout,
                entries: &[
                    wgpu::BindGroupEntry {
                        binding: 0,
                        resource: wgpu::BindingResource::TextureView(&view),
                    },
                    wgpu::BindGroupEntry {
                        binding: 1,
                        resource: wgpu::BindingResource::Sampler(&sampler),
                    },
                ],
            });
            pages.push(GpuPage {
                _texture: texture,
                bind_group,
            });
        }

        for asset in &scene.assets {
            let region = asset_regions.get(&asset.id).ok_or_else(|| {
                RendererError::invalid(format!("asset {} has no GPU texture region", asset.id))
            })?;
            let page = pages.get(region.page as usize).ok_or_else(|| {
                RendererError::invalid(format!("asset {} references missing atlas page", asset.id))
            })?;
            queue.write_texture(
                wgpu::ImageCopyTexture {
                    texture: &page._texture,
                    mip_level: 0,
                    origin: wgpu::Origin3d {
                        x: region.x,
                        y: region.y,
                        z: 0,
                    },
                    aspect: wgpu::TextureAspect::All,
                },
                &asset.rgba,
                wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(region.width * 4),
                    rows_per_image: Some(region.height),
                },
                wgpu::Extent3d {
                    width: region.width,
                    height: region.height,
                    depth_or_array_layers: 1,
                },
            );
        }

        let shader = device.create_shader_module(wgpu::ShaderModuleDescriptor {
            label: Some("NCT1 comment timeline shader"),
            source: wgpu::ShaderSource::Wgsl(include_str!("timeline.wgsl").into()),
        });
        let pipeline_layout = device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
            label: Some("NCT1 timeline pipeline layout"),
            bind_group_layouts: &[&draw_layout, &texture_layout],
            push_constant_ranges: &[],
        });
        let premultiplied = wgpu::BlendComponent {
            src_factor: wgpu::BlendFactor::One,
            dst_factor: wgpu::BlendFactor::OneMinusSrcAlpha,
            operation: wgpu::BlendOperation::Add,
        };
        let pipeline = device.create_render_pipeline(&wgpu::RenderPipelineDescriptor {
            label: Some("NCT1 premultiplied RGBA pipeline"),
            layout: Some(&pipeline_layout),
            vertex: wgpu::VertexState {
                module: &shader,
                entry_point: "vs_main",
                buffers: &[],
                compilation_options: wgpu::PipelineCompilationOptions::default(),
            },
            fragment: Some(wgpu::FragmentState {
                module: &shader,
                entry_point: "fs_main",
                compilation_options: wgpu::PipelineCompilationOptions::default(),
                targets: &[Some(wgpu::ColorTargetState {
                    format: wgpu::TextureFormat::Rgba8Unorm,
                    blend: Some(wgpu::BlendState {
                        color: premultiplied,
                        alpha: premultiplied,
                    }),
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

        let draws = scene.draws.clone();
        let scene_index = SceneIndex::new(&draws);
        let static_asset_telemetry = asset_layout_telemetry(
            scene,
            &page_specs,
            &asset_regions,
            &draws,
            &asset_layout_report,
        )?;
        let generation = Arc::new(SceneGeneration::new(0));
        Ok(Self {
            device,
            queue,
            adapter_info,
            header_width: scene.header.width,
            header_height: scene.header.height,
            frame_count: scene.header.frame_count,
            fps_num: scene.header.fps_num,
            fps_den: scene.header.fps_den,
            draws,
            scene_index,
            draw_metadata_credit: None,
            next_element_ordinal: 0,
            generation,
            generation_budget,
            asset_regions,
            stream_assets: StreamAssetRegistry::new(),
            pages,
            asset_layout_report,
            static_asset_telemetry,
            bundle_build_cpu_wall_time_ns: 0,
            frame_buffer,
            draw_layout,
            texture_layout,
            sampler,
            pipeline,
            cached_seconds: VecDeque::with_capacity(2),
            stream_upload_completions: Arc::new(Mutex::new(Vec::new())),
            next_stream_upload_token: 0,
        })
    }

    pub fn submit_frame(
        &mut self,
        frame: u32,
        target: &wgpu::Texture,
    ) -> Result<wgpu::SubmissionIndex, RendererError> {
        let (encoder, vpos, cached_second) = self.encode_frame(frame, target)?;
        let submission = write_uniform_then_submit(
            || {
                self.queue.write_buffer(
                    &self.frame_buffer,
                    0,
                    bytemuck::bytes_of(&FrameUniform {
                        vpos,
                        padding: [0; 3],
                    }),
                );
            },
            || self.queue.submit(Some(encoder.finish())),
        );
        self.queue
            .on_submitted_work_done(retire_after_submission(cached_second));
        Ok(submission)
    }

    pub(crate) fn validate_stream_texture_extent(
        &self,
        width: u32,
        height: u32,
    ) -> Result<(), RendererError> {
        let limit = self.device.limits().max_texture_dimension_2d;
        if width == 0 || height == 0 || width > limit || height > limit {
            return Err(RendererError::invalid(format!(
                "stream asset extent {width}x{height} exceeds WGPU 2D texture limit {limit}"
            )));
        }
        Ok(())
    }

    pub(crate) fn create_stream_texture(
        &self,
        width: u32,
        height: u32,
    ) -> Result<wgpu::Texture, RendererError> {
        self.validate_stream_texture_extent(width, height)?;
        Ok(self.device.create_texture(&wgpu::TextureDescriptor {
            label: Some("nct2 stream resident asset"),
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
        }))
    }

    pub(crate) fn validate_stream_asset_registration(
        &self,
        asset_id: u32,
    ) -> Result<(), RendererError> {
        if self.asset_regions.contains_key(&asset_id) {
            return Err(RendererError::invalid(format!(
                "stream asset id {asset_id} conflicts with a static asset"
            )));
        }
        if self.stream_assets.get(asset_id).is_some() {
            return Err(RendererError::invalid(format!(
                "duplicate stream asset id {asset_id}"
            )));
        }
        Ok(())
    }

    pub(crate) fn register_stream_asset(
        &mut self,
        asset_id: u32,
        texture: &wgpu::Texture,
        owner: Arc<dyn Send + Sync>,
    ) {
        // The uploader validates ID and dimensions before allocation. With its
        // exclusive `&mut Renderer` borrow, this insertion cannot race another
        // registration; reject defensively before creating any registry state.
        self.validate_stream_asset_registration(asset_id)
            .expect("stream asset registration was prevalidated");
        let view = texture.create_view(&wgpu::TextureViewDescriptor::default());
        let bind_group = self.device.create_bind_group(&wgpu::BindGroupDescriptor {
            label: Some("NCT2 streamed asset and sampler"),
            layout: &self.texture_layout,
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
        let asset = Arc::new(StreamGpuAsset {
            region: GpuRegion {
                page: 0,
                x: 0,
                y: 0,
                width: texture.width(),
                height: texture.height(),
            },
            bind_group,
            _owner: owner,
        });
        self.stream_assets
            .register(asset_id, asset)
            .expect("stream asset ID remains unique after prevalidation");
    }

    pub(crate) fn release_stream_assets(&mut self) {
        self.stream_assets.clear();
        // Cached bundles can otherwise keep a closed scene's assets resident.
        // Submitted frames retain their own Arc until queue completion.
        self.cached_seconds.clear();
    }

    pub(crate) fn create_stream_staging(&self, size: u64) -> Result<wgpu::Buffer, RendererError> {
        if size == 0 || size > self.device.limits().max_buffer_size {
            return Err(RendererError::invalid(format!(
                "stream staging buffer size {size} exceeds WGPU buffer limit"
            )));
        }
        Ok(self.device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("nct2 stream padded upload staging"),
            size,
            usage: wgpu::BufferUsages::COPY_SRC,
            mapped_at_creation: true,
        }))
    }

    /// The Renderer is the sole owner of stream upload submissions. The
    /// callback token identifies the exact submission whose staging must live.
    pub(crate) fn submit_stream_upload(
        &mut self,
        staging: &wgpu::Buffer,
        texture: &wgpu::Texture,
        padded_bytes_per_row: u32,
        retirement: UploadRetirement,
    ) -> Result<u64, RendererError> {
        if staging.size() == 0 || padded_bytes_per_row == 0 || padded_bytes_per_row % 256 != 0 {
            return Err(RendererError::invalid(
                "invalid stream upload staging layout",
            ));
        }
        let mut encoder = self
            .device
            .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                label: Some("nct2 stream asset upload"),
            });
        encoder.copy_buffer_to_texture(
            wgpu::ImageCopyBuffer {
                buffer: staging,
                layout: wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(padded_bytes_per_row),
                    rows_per_image: Some(texture.height()),
                },
            },
            wgpu::ImageCopyTexture {
                texture,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            wgpu::Extent3d {
                width: texture.width(),
                height: texture.height(),
                depth_or_array_layers: 1,
            },
        );
        self.next_stream_upload_token = self
            .next_stream_upload_token
            .checked_add(1)
            .ok_or_else(|| RendererError::invalid("stream upload token overflow"))?;
        let token = self.next_stream_upload_token;
        self.queue.submit(Some(encoder.finish()));
        let completed = Arc::clone(&self.stream_upload_completions);
        self.queue.on_submitted_work_done(move || {
            completed
                .lock()
                .expect("stream upload completion mutex poisoned")
                .push(token);
            drop(retirement);
        });
        Ok(token)
    }

    /// Drive wgpu 0.20 completion callbacks without waiting for all device work.
    pub(crate) fn poll_stream_upload_completions(&self) -> Vec<u64> {
        self.device.poll(wgpu::Maintain::Poll);
        std::mem::take(
            &mut *self
                .stream_upload_completions
                .lock()
                .expect("stream upload completion mutex poisoned"),
        )
    }

    /// Render one frame and queue its texture-to-buffer copy in the same
    /// submission so readback slots stay ordered behind their source frame.
    pub fn submit_frame_with_readback(
        &mut self,
        frame: u32,
        target: &wgpu::Texture,
        readback: &wgpu::Buffer,
        bytes_per_row: u32,
    ) -> Result<wgpu::SubmissionIndex, RendererError> {
        if bytes_per_row < self.header_width.saturating_mul(4) || bytes_per_row % 256 != 0 {
            return Err(RendererError::invalid("invalid readback row pitch"));
        }
        let (mut encoder, vpos, cached_second) = self.encode_frame(frame, target)?;
        encoder.copy_texture_to_buffer(
            wgpu::ImageCopyTexture {
                texture: target,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            wgpu::ImageCopyBuffer {
                buffer: readback,
                layout: wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(bytes_per_row),
                    rows_per_image: Some(self.header_height),
                },
            },
            wgpu::Extent3d {
                width: self.header_width,
                height: self.header_height,
                depth_or_array_layers: 1,
            },
        );
        let submission = write_uniform_then_submit(
            || {
                self.queue.write_buffer(
                    &self.frame_buffer,
                    0,
                    bytemuck::bytes_of(&FrameUniform {
                        vpos,
                        padding: [0; 3],
                    }),
                );
            },
            || self.queue.submit(Some(encoder.finish())),
        );
        self.queue
            .on_submitted_work_done(retire_after_submission(cached_second));
        Ok(submission)
    }

    fn encode_frame(
        &mut self,
        frame: u32,
        target: &wgpu::Texture,
    ) -> Result<(wgpu::CommandEncoder, i32, Arc<CachedSecond>), RendererError> {
        if frame >= self.frame_count {
            return Err(RendererError::invalid(format!(
                "frame {frame} is outside the scene frame count {}",
                self.frame_count
            )));
        }
        if target.width() != self.header_width
            || target.height() != self.header_height
            || target.format() != wgpu::TextureFormat::Rgba8Unorm
        {
            return Err(RendererError::invalid(format!(
                "render target must be {}x{} Rgba8Unorm",
                self.header_width, self.header_height
            )));
        }
        let vpos = frame_vpos(frame as i64, self.fps_num, self.fps_den)
            .map_err(|error| RendererError::invalid(format!("frame clock: {error}")))?;
        let second = vpos.div_euclid(100);
        self.bundle_for_second(second, vpos)?;
        let cached_second = self
            .cached_seconds
            .back()
            .cloned()
            .ok_or_else(|| RendererError::invalid("second cache was not populated"))?;
        let bundle = cached_second.bundle.as_ref();
        let target_view = target.create_view(&wgpu::TextureViewDescriptor::default());
        let mut encoder = self
            .device
            .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                label: Some("NCT1 frame encoder"),
            });
        {
            let mut pass = encoder.begin_render_pass(&wgpu::RenderPassDescriptor {
                label: Some("NCT1 transparent comment surface"),
                color_attachments: &[Some(wgpu::RenderPassColorAttachment {
                    view: &target_view,
                    resolve_target: None,
                    ops: wgpu::Operations {
                        load: wgpu::LoadOp::Clear(wgpu::Color::TRANSPARENT),
                        store: wgpu::StoreOp::Store,
                    },
                })],
                depth_stencil_attachment: None,
                timestamp_writes: None,
                occlusion_query_set: None,
            });
            if let Some(bundle) = bundle {
                pass.execute_bundles(std::iter::once(bundle));
            }
        }
        Ok((encoder, vpos, cached_second))
    }

    pub fn device(&self) -> &wgpu::Device {
        &self.device
    }

    pub fn queue(&self) -> &wgpu::Queue {
        &self.queue
    }

    pub fn adapter_info(&self) -> &wgpu::AdapterInfo {
        &self.adapter_info
    }

    pub fn asset_layout_report(&self) -> &AssetLayoutReport {
        &self.asset_layout_report
    }

    pub fn asset_telemetry_report(
        &self,
        readback_slots: usize,
    ) -> Result<AssetTelemetryReport, RendererError> {
        let mut report = self.static_asset_telemetry.clone();
        report.bundle_build_cpu_wall_time_ns = Some(self.bundle_build_cpu_wall_time_ns);
        report
            .resident_memory
            .renderer_owned_draw_buffer_payload_bytes = Some(
            self.cached_seconds
                .iter()
                .map(|cached| cached.draw_buffer_bytes)
                .sum(),
        );
        report.resident_memory.readback_ring_buffer_bytes = Some(readback_ring_buffer_bytes(
            self.header_width,
            self.header_height,
            readback_slots,
        )?);
        Ok(report)
    }

    /// Apply one completed stream element. Draw order is append-only and the
    /// new immutable generation invalidates both SceneIndex and second bundles.
    pub fn apply_element(
        &mut self,
        element: &Element,
        cpu_budget: &CpuBudget,
    ) -> Result<(), RendererError> {
        apply_element_state(
            &mut self.draws,
            &mut self.scene_index,
            &mut self.cached_seconds,
            &mut self.next_element_ordinal,
            &mut self.generation,
            cpu_budget,
            &mut self.draw_metadata_credit,
            element,
        )
    }

    fn bundle_for_second(&mut self, second: i32, vpos: i32) -> Result<(), RendererError> {
        if let Some(position) = self
            .cached_seconds
            .iter()
            .position(|cached| cached.second == second && cached.generation == self.generation.id)
        {
            let cached = self
                .cached_seconds
                .remove(position)
                .expect("cached position exists");
            self.cached_seconds.push_back(cached);
            return Ok(());
        }

        let build_started = Instant::now();
        let candidates = self.scene_index.candidate_draw_indices(vpos);
        let mut draw_buffer_bytes = 0_u64;
        let (bundle, draw_buffer, draw_bind_group, draw_credit, stream_assets) =
            if candidates.is_empty() {
                (None, None, None, None, Vec::new())
            } else {
                let draw_bytes = candidates
                    .len()
                    .checked_mul(size_of::<GpuDraw>())
                    .ok_or_else(|| RendererError::invalid("draw buffer byte size overflow"))?;
                draw_buffer_bytes = draw_bytes as u64;
                let draw_credit = self
                    .generation_budget
                    .try_reserve(draw_bytes)
                    .map_err(|error| RendererError::invalid(error.to_string()))?;
                let mut gpu_draws = Vec::with_capacity(candidates.len());
                let mut binding_keys = Vec::with_capacity(candidates.len());
                let mut stream_assets = Vec::new();
                for &index in &candidates {
                    let draw = &self.draws[index];
                    if let Some(region) = self.asset_regions.get(&draw.asset_id) {
                        gpu_draws.push(gpu_draw(draw, *region)?);
                        binding_keys.push(TextureBindingKey::StaticPage(region.page));
                    } else if let Some(asset) = self.stream_assets.get(draw.asset_id) {
                        gpu_draws.push(gpu_draw(draw, asset.region)?);
                        binding_keys.push(TextureBindingKey::StreamAsset(draw.asset_id));
                        stream_assets.push(Arc::clone(asset));
                    } else {
                        return Err(RendererError::invalid(format!(
                            "draw references missing asset {}",
                            draw.asset_id
                        )));
                    }
                }
                let draw_buffer =
                    self.device
                        .create_buffer_init(&wgpu::util::BufferInitDescriptor {
                            label: Some("NCT1 one-second draw candidates"),
                            contents: bytemuck::cast_slice(&gpu_draws),
                            usage: wgpu::BufferUsages::STORAGE,
                        });
                let draw_bind_group = self.device.create_bind_group(&wgpu::BindGroupDescriptor {
                    label: Some("NCT1 candidate and frame bindings"),
                    layout: &self.draw_layout,
                    entries: &[
                        wgpu::BindGroupEntry {
                            binding: 0,
                            resource: draw_buffer.as_entire_binding(),
                        },
                        wgpu::BindGroupEntry {
                            binding: 1,
                            resource: self.frame_buffer.as_entire_binding(),
                        },
                    ],
                });

                let runs = contiguous_binding_runs(&binding_keys);
                let mut bundle_encoder = self.device.create_render_bundle_encoder(
                    &wgpu::RenderBundleEncoderDescriptor {
                        label: Some("NCT1 one-second ordered draw bundle"),
                        color_formats: &[Some(wgpu::TextureFormat::Rgba8Unorm)],
                        depth_stencil: None,
                        sample_count: 1,
                        multiview: None,
                    },
                );
                bundle_encoder.set_pipeline(&self.pipeline);
                bundle_encoder.set_bind_group(0, &draw_bind_group, &[]);
                for (key, start, end) in runs {
                    let bind_group = match key {
                        TextureBindingKey::StaticPage(page_index) => self
                            .pages
                            .get(page_index as usize)
                            .map(|page| &page.bind_group)
                            .ok_or_else(|| {
                                RendererError::invalid(format!(
                                    "draw references missing atlas page {page_index}"
                                ))
                            })?,
                        TextureBindingKey::StreamAsset(asset_id) => self
                            .stream_assets
                            .get(asset_id)
                            .map(|asset| &asset.bind_group)
                            .ok_or_else(|| {
                                RendererError::invalid(format!(
                                    "draw references missing stream asset {asset_id}"
                                ))
                            })?,
                    };
                    bundle_encoder.set_bind_group(1, bind_group, &[]);
                    bundle_encoder.draw(0..6, start as u32..end as u32);
                }
                (
                    Some(bundle_encoder.finish(&wgpu::RenderBundleDescriptor {
                        label: Some("NCT1 one-second render bundle"),
                    })),
                    Some(draw_buffer),
                    Some(draw_bind_group),
                    Some(draw_credit),
                    stream_assets,
                )
            };

        self.cached_seconds.push_back(Arc::new(CachedSecond {
            second,
            generation: self.generation.id,
            bundle,
            _draw_buffer: draw_buffer,
            draw_buffer_bytes,
            _draw_bind_group: draw_bind_group,
            _draw_generation: Arc::clone(&self.generation),
            _draw_credit: draw_credit,
            _stream_assets: stream_assets,
        }));
        if self.cached_seconds.len() > 2 {
            self.cached_seconds.pop_front();
        }
        self.bundle_build_cpu_wall_time_ns = self
            .bundle_build_cpu_wall_time_ns
            .saturating_add(build_started.elapsed().as_nanos().min(u64::MAX as u128) as u64);
        Ok(())
    }
}

fn gpu_draw(draw: &Draw, region: GpuRegion) -> Result<GpuDraw, RendererError> {
    let mut projection = [[0.0; 4]; 4];
    for (index, column) in projection.iter_mut().enumerate() {
        column.copy_from_slice(&draw.projection[index * 4..index * 4 + 4]);
    }
    let anchor_x_high = draw.anchor_x as f32;
    let speed_x_high = draw.speed_x as f32;
    let motion_precision = encode_motion_payload(draw.anchor_x, draw.rect[0], draw.speed_x)
        .map_err(|error| motion_precision_error("draw motion", error))?;
    let packed_origin = pack_u16_pair(region.x, region.y, "atlas origin")?;
    let packed_extent = pack_u16_pair(region.width, region.height, "asset extent")?;
    let mut rect = draw.rect;
    rect[0] = anchor_x_high;
    Ok(GpuDraw {
        rect,
        projection,
        interval: [
            draw.start_vpos,
            draw.end_vpos,
            draw.anchor_vpos,
            packed_origin as i32,
        ],
        movement: [speed_x_high, draw.alpha],
        texture_extent_packed: packed_extent,
        padding: 0,
        motion_precision,
    })
}

fn motion_precision_error(label: &str, error: MotionPrecisionError) -> RendererError {
    let reason = match error {
        MotionPrecisionError::NonFinite => "contains a non-finite value",
        MotionPrecisionError::OutOfRange => "exceeds the signed Q40.40 range",
    };
    RendererError::invalid(format!("{label} {reason}"))
}

fn pack_u16_pair(first: u32, second: u32, label: &str) -> Result<u32, RendererError> {
    if first > u16::MAX as u32 || second > u16::MAX as u32 {
        return Err(RendererError::invalid(format!(
            "NCT1 {label} exceeds the packed 16-bit GPU coordinate limit"
        )));
    }
    Ok(first | (second << 16))
}

fn prepare_asset_layout(
    scene: &Scene,
    requested: AssetLayoutMode,
    max_texture_dimension: u32,
) -> Result<
    (
        Vec<crate::atlas::AtlasPage>,
        HashMap<u32, GpuRegion>,
        AssetLayoutReport,
    ),
    RendererError,
> {
    let extents: Vec<_> = scene
        .assets
        .iter()
        .map(|asset| AssetExtent {
            id: asset.id,
            width: asset.width,
            height: asset.height,
        })
        .collect();
    let mut fallback_reason = None;
    let (actual, pages, regions, source_bytes, allocated_bytes) = match requested {
        AssetLayoutMode::Separate => {
            let (pages, regions, bytes) = separate_asset_layout(&scene.assets);
            (AssetLayoutMode::Separate, pages, regions, bytes, bytes)
        }
        AssetLayoutMode::Atlas => {
            match plan_atlas(&extents, max_texture_dimension).map_err(|error| {
                RendererError::invalid(format!("planning NCT1 texture atlas: {error}"))
            })? {
                AtlasDecision::Packed(plan) => {
                    let regions = plan
                        .regions
                        .iter()
                        .map(|(&id, region)| (id, gpu_region(*region)))
                        .collect();
                    (
                        AssetLayoutMode::Atlas,
                        plan.pages,
                        regions,
                        plan.source_bytes,
                        plan.allocated_bytes,
                    )
                }
                AtlasDecision::Separate(reason) => {
                    fallback_reason = Some(format_atlas_fallback(&reason));
                    let (pages, regions, bytes) = separate_asset_layout(&scene.assets);
                    (AssetLayoutMode::Separate, pages, regions, bytes, bytes)
                }
            }
        }
    };
    if regions.len() != scene.assets.len() {
        return Err(RendererError::invalid(format!(
            "GPU texture layout covers {} of {} assets",
            regions.len(),
            scene.assets.len()
        )));
    }
    let nonzero_origin_count = regions
        .values()
        .filter(|region| region.x != 0 || region.y != 0)
        .count();
    let report = AssetLayoutReport {
        requested,
        actual,
        fallback_reason,
        page_count: pages.len(),
        source_bytes,
        allocated_bytes,
        nonzero_origin_count,
    };
    Ok((pages, regions, report))
}

fn separate_asset_layout(
    assets: &[crate::protocol::Asset],
) -> (Vec<crate::atlas::AtlasPage>, HashMap<u32, GpuRegion>, u64) {
    let mut pages = Vec::with_capacity(assets.len());
    let mut regions = HashMap::with_capacity(assets.len());
    let mut source_bytes = 0u64;
    for asset in assets {
        let page = pages.len() as u32;
        pages.push(crate::atlas::AtlasPage {
            width: asset.width,
            height: asset.height,
        });
        regions.insert(
            asset.id,
            GpuRegion {
                page,
                x: 0,
                y: 0,
                width: asset.width,
                height: asset.height,
            },
        );
        source_bytes += asset.rgba.len() as u64;
    }
    (pages, regions, source_bytes)
}

fn gpu_region(region: AtlasRegion) -> GpuRegion {
    GpuRegion {
        page: region.page,
        x: region.x,
        y: region.y,
        width: region.width,
        height: region.height,
    }
}

fn format_atlas_fallback(reason: &SeparateReason) -> String {
    match reason {
        SeparateReason::NoCandidatePageWidth => {
            "device limit is smaller than the minimum atlas page width".to_owned()
        }
        SeparateReason::BudgetExceeded {
            allocated_bytes,
            budget_bytes,
        } => {
            format!("atlas allocation {allocated_bytes} bytes exceeds budget {budget_bytes} bytes")
        }
    }
}

fn validate_scene_for_render(scene: &Scene) -> Result<(), RendererError> {
    if scene.header.width == 0
        || scene.header.width > crate::protocol::MAX_WIDTH
        || scene.header.height == 0
        || scene.header.height > crate::protocol::MAX_HEIGHT
        || scene.header.frame_count == 0
        || scene.header.frame_count > crate::protocol::MAX_FRAMES
        || scene.header.fps_num == 0
        || scene.header.fps_den == 0
        || scene.header.fps_num > crate::protocol::MAX_FPS_COMPONENT
        || scene.header.fps_den > crate::protocol::MAX_FPS_COMPONENT
        || scene.header.fps_num as u64 > scene.header.fps_den as u64 * 60
    {
        return Err(RendererError::invalid(
            "NCT1 scene header contains zero dimensions or clock",
        ));
    }
    let first_vpos = frame_vpos(0, scene.header.fps_num, scene.header.fps_den)
        .map_err(|error| RendererError::invalid(format!("frame clock: {error}")))?;
    let last_vpos = frame_vpos(
        scene.header.frame_count as i64 - 1,
        scene.header.fps_num,
        scene.header.fps_den,
    )
    .map_err(|error| RendererError::invalid(format!("frame clock: {error}")))?;
    if scene.draws.len() > crate::protocol::MAX_DRAWS as usize
        || scene.assets.len() > crate::protocol::MAX_ASSETS as usize
    {
        return Err(RendererError::invalid(
            "NCT1 scene exceeds draw or asset limits",
        ));
    }
    let mut assets = HashMap::with_capacity(scene.assets.len());
    let mut total_bytes = 0u64;
    for asset in &scene.assets {
        if asset.width > crate::protocol::MAX_ASSET_DIMENSION
            || asset.height > crate::protocol::MAX_ASSET_DIMENSION
            || asset.width == 0
            || asset.height == 0
        {
            return Err(RendererError::invalid(format!(
                "asset {} has invalid dimensions",
                asset.id
            )));
        }
        let expected = (asset.width as u64)
            .checked_mul(asset.height as u64)
            .and_then(|pixels| pixels.checked_mul(4))
            .ok_or_else(|| {
                RendererError::invalid(format!("asset {} byte length overflows", asset.id))
            })?;
        if expected > crate::protocol::MAX_ASSET_BYTES || asset.rgba.len() as u64 != expected {
            return Err(RendererError::invalid(format!(
                "asset {} has invalid pixel byte length",
                asset.id
            )));
        }
        if Sha256::digest(&asset.rgba).as_slice() != asset.sha256 {
            return Err(RendererError::invalid(format!(
                "asset {} SHA-256 mismatch",
                asset.id
            )));
        }
        total_bytes += expected;
        if total_bytes > crate::protocol::MAX_TOTAL_ASSET_BYTES {
            return Err(RendererError::invalid(
                "NCT1 assets exceed total byte limit",
            ));
        }
        if assets.insert(asset.id, asset).is_some() {
            return Err(RendererError::invalid(format!(
                "duplicate asset id {}",
                asset.id
            )));
        }
    }

    for (index, draw) in scene.draws.iter().enumerate() {
        if !assets.contains_key(&draw.asset_id) {
            return Err(RendererError::invalid(format!(
                "draw {index} references missing asset {}",
                draw.asset_id
            )));
        }
        encode_motion_payload(draw.anchor_x, draw.rect[0], draw.speed_x).map_err(|error| {
            motion_precision_error(&format!("draw {index} Q40.40 motion"), error)
        })?;
        if draw.start_vpos >= draw.end_vpos
            || !draw.rect.iter().all(|value| value.is_finite())
            || !draw.projection.iter().all(|value| value.is_finite())
            || !draw.alpha.is_finite()
            || !(0.0..=1.0).contains(&draw.alpha)
            || !draw.anchor_x.is_finite()
            || !draw.speed_x.is_finite()
        {
            return Err(RendererError::invalid(format!(
                "draw {index} contains an invalid interval or non-finite value"
            )));
        }
        let visible_start = draw.start_vpos.max(first_vpos);
        let visible_end = draw.end_vpos.saturating_sub(1).min(last_vpos);
        if visible_start <= visible_end {
            let max_delta = (visible_start as i64 - draw.anchor_vpos as i64)
                .abs()
                .max((visible_end as i64 - draw.anchor_vpos as i64).abs());
            let speed_f32 = draw.speed_x as f32;
            if max_delta > MAX_EXACT_F32_INT
                || !speed_f32.is_finite()
                || position_x_error_bound(draw.rect[0], draw.speed_x, max_delta) > 1.0 / 256.0
            {
                return Err(RendererError::invalid(format!(
                    "draw {index} speed cannot be represented within 1/256 pixel"
                )));
            }
        }
    }
    Ok(())
}

fn position_x_error_bound(rect_x: f32, speed_x: f64, max_delta: i64) -> f64 {
    if max_delta == 0 {
        return 0.0;
    }

    let speed_f32 = speed_x as f32;
    if !speed_f32.is_finite() {
        return f64::INFINITY;
    }
    if speed_x == 0.0 {
        return 0.0;
    }

    let delta = max_delta as f64;
    let max_product = delta * (speed_f32 as f64).abs();
    let max_position = (rect_x as f64).abs() + max_product;
    let speed_conversion_error = delta * (speed_f32 as f64 - speed_x).abs();
    let multiply_rounding_error = (f32_ulp_at_or_above(max_product) * 0.5).min(max_product);
    let add_rounding_error = (f32_ulp_at_or_above(max_position) * 0.5).min(max_product);
    speed_conversion_error + multiply_rounding_error + add_rounding_error
}

fn f32_ulp_at_or_above(magnitude: f64) -> f64 {
    if !magnitude.is_finite() || magnitude > f32::MAX as f64 {
        return f64::INFINITY;
    }

    let rounded = magnitude as f32;
    let ceiling = if (rounded as f64) < magnitude {
        f32::from_bits(rounded.to_bits() + 1)
    } else {
        rounded
    };
    let next_bits = ceiling.to_bits() + 1;
    if next_bits >= f32::INFINITY.to_bits() {
        return f64::INFINITY;
    }
    (f32::from_bits(next_bits) as f64) - (ceiling as f64)
}

fn validate_adapter_limits(
    info: &wgpu::AdapterInfo,
    limits: &wgpu::Limits,
    max_dimension: u32,
    draw_storage_bytes: u64,
    draw_storage_binding_bytes: u32,
) -> Result<(), RendererError> {
    let lacking = if limits.max_texture_dimension_2d < max_dimension {
        Some(format!(
            "2D texture limit {} is below required {max_dimension}",
            limits.max_texture_dimension_2d
        ))
    } else if limits.max_buffer_size < draw_storage_bytes {
        Some(format!(
            "buffer limit {} is below required {draw_storage_bytes}",
            limits.max_buffer_size
        ))
    } else if limits.max_storage_buffer_binding_size < draw_storage_binding_bytes {
        Some(format!(
            "storage binding limit {} is below required {draw_storage_binding_bytes}",
            limits.max_storage_buffer_binding_size
        ))
    } else if limits.max_bind_groups < 2
        || limits.max_storage_buffers_per_shader_stage < 1
        || limits.max_uniform_buffers_per_shader_stage < 1
        || limits.max_sampled_textures_per_shader_stage < 1
        || limits.max_samplers_per_shader_stage < 1
        || limits.max_bindings_per_bind_group < 2
    {
        Some("adapter lacks required storage, uniform, sampler, or binding limits".to_owned())
    } else {
        None
    };
    if let Some(reason) = lacking {
        return Err(RendererError::invalid(format!(
            "WGPU adapter {} ({:?}) is unsupported: {reason}",
            info.name, info.backend
        )));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use std::cell::RefCell;
    use std::collections::VecDeque;
    use std::mem::size_of;
    use std::sync::Arc;

    use super::{
        apply_element_state, gpu_draw, pack_u16_pair, prepare_asset_layout,
        retire_after_submission, write_uniform_then_submit, AssetLayoutMode, CachedSecond, GpuDraw,
        GpuRegion, SceneGeneration, RENDERER_DRAW_METADATA_RESERVATION_BYTES,
    };
    use crate::protocol::{Asset, Draw, Header, Scene};
    use crate::scene::SceneIndex;
    use crate::stream_budget::{CpuBudget, CPU_BUDGET_BYTES};
    use crate::stream_protocol::Element;
    use crate::stream_renderer::{GpuBudget, GpuCredit};
    use sha2::{Digest, Sha256};

    #[test]
    fn stream_asset_registry_rejects_duplicates_and_keeps_retired_owner_alive() {
        struct Entry {
            _credit: GpuCredit,
        }

        let budget = GpuBudget::new();
        let credit = budget.try_reserve(4).unwrap();
        let mut registry = super::StreamAssetRegistry::new();
        registry
            .register(17, Arc::new(Entry { _credit: credit }))
            .unwrap();
        let cached_owner = Arc::clone(registry.get(17).unwrap());
        let rejected_credit = budget.try_reserve(3).unwrap();

        assert!(registry
            .register(
                17,
                Arc::new(Entry {
                    _credit: rejected_credit,
                }),
            )
            .is_err());
        assert!(registry.get(99).is_none(), "unknown assets stay unresolved");
        assert_eq!(
            budget.used_bytes(),
            4,
            "rejected duplicate releases its owner"
        );
        registry.clear();
        assert_eq!(
            budget.used_bytes(),
            4,
            "cached generation retains its owner"
        );
        drop(cached_owner);
        assert_eq!(
            budget.used_bytes(),
            0,
            "the final cached owner releases its credit"
        );
    }

    #[test]
    fn streamed_texture_binding_runs_preserve_a_b_a_order() {
        use super::{contiguous_binding_runs, TextureBindingKey};

        let keys = [
            TextureBindingKey::StreamAsset(11),
            TextureBindingKey::StreamAsset(12),
            TextureBindingKey::StreamAsset(11),
        ];
        assert_eq!(
            contiguous_binding_runs(&keys),
            vec![
                (TextureBindingKey::StreamAsset(11), 0, 1),
                (TextureBindingKey::StreamAsset(12), 1, 2),
                (TextureBindingKey::StreamAsset(11), 2, 3),
            ]
        );
    }

    fn scene_with_assets() -> Scene {
        let assets = [(1_u32, 33_u32, 19_u32), (2, 21, 11)]
            .into_iter()
            .map(|(id, width, height)| {
                let rgba = vec![u8::try_from(id).unwrap(); width as usize * height as usize * 4];
                Asset {
                    id,
                    width,
                    height,
                    sha256: Sha256::digest(&rgba).into(),
                    rgba,
                }
            })
            .collect();
        Scene {
            header: Header {
                width: 33,
                height: 19,
                frame_count: 1,
                fps_num: 30,
                fps_den: 1,
                bundle_sha256: [0; 32],
            },
            assets,
            draws: Vec::new(),
        }
    }

    #[test]
    fn packed_texture_region_fits_existing_gpu_draw_record_and_round_trips_limits() {
        let packed = pack_u16_pair(16_384, 16_384, "test").unwrap();
        assert_eq!(packed & 0xffff, 16_384);
        assert_eq!(packed >> 16, 16_384);
        assert!(pack_u16_pair(65_536, 0, "test").is_err());
        assert_eq!(size_of::<GpuDraw>(), 128);
    }

    #[test]
    fn gpu_draw_encodes_q40_motion_and_preserves_the_128_byte_record() {
        let draw = Draw {
            asset_id: 1,
            start_vpos: 0,
            end_vpos: 500,
            anchor_vpos: 317,
            owner_order: 0,
            comment_index: 22,
            primitive_index: 0,
            rect: [736.2553100585938, 398.0, 216.0, 103.0],
            projection: [0.0; 16],
            alpha: 1.0,
            anchor_x: 740.2553147877013,
            speed_x: -4.319055636896047,
        };
        let gpu = gpu_draw(
            &draw,
            GpuRegion {
                page: 0,
                x: 0,
                y: 0,
                width: 216,
                height: 103,
            },
        )
        .unwrap();

        let anchor_hi = draw.anchor_x as f32;
        let speed_hi = draw.speed_x as f32;
        assert_eq!(gpu.rect[0], anchor_hi);
        assert_eq!(
            gpu.motion_precision,
            crate::motion_precision::encode_motion_payload(
                draw.anchor_x,
                draw.rect[0],
                draw.speed_x,
            )
            .unwrap()
        );
        assert_eq!(gpu.movement[0], speed_hi);
        assert_eq!(gpu.movement[1], draw.alpha);
        assert_eq!(gpu.rect[1..], draw.rect[1..]);
        assert_eq!(gpu.texture_extent_packed, 216 | (103 << 16));
        assert_eq!(size_of::<GpuDraw>(), 128);
    }

    #[test]
    fn atlas_layout_falls_back_to_separate_pages_when_the_device_has_no_candidate_width() {
        let scene = scene_with_assets();
        let (pages, regions, report) =
            prepare_asset_layout(&scene, AssetLayoutMode::Atlas, 128).unwrap();

        assert_eq!(report.requested, AssetLayoutMode::Atlas);
        assert_eq!(report.actual, AssetLayoutMode::Separate);
        assert!(report
            .fallback_reason
            .as_deref()
            .unwrap()
            .contains("minimum atlas page width"));
        assert_eq!(report.page_count, 2);
        assert_eq!(pages.len(), scene.assets.len());
        assert_eq!(regions.len(), scene.assets.len());
        assert_eq!(report.source_bytes, report.allocated_bytes);
        assert_eq!(report.nonzero_origin_count, 0);
        let telemetry =
            super::asset_layout_telemetry(&scene, &pages, &regions, &[], &report).unwrap();
        assert_eq!(telemetry.effective_layout, "separate");
        assert_eq!(telemetry.pages.as_ref().unwrap().len(), 2);
        assert_eq!(
            telemetry.pages.as_ref().unwrap()[0].used_texels,
            telemetry.pages.as_ref().unwrap()[0].allocated_texels
        );
    }

    #[test]
    fn atlas_layout_reports_page_count_and_exact_allocated_area() {
        let scene = scene_with_assets();
        let (pages, regions, report) =
            prepare_asset_layout(&scene, AssetLayoutMode::Atlas, 16_384).unwrap();

        assert_eq!(report.requested, AssetLayoutMode::Atlas);
        assert_eq!(report.actual, AssetLayoutMode::Atlas);
        assert_eq!(report.fallback_reason, None);
        assert_eq!(pages.len(), 1);
        assert_eq!(report.page_count, 1);
        assert_eq!(regions.len(), scene.assets.len());
        assert!(report.nonzero_origin_count > 0);
        let page_bytes: u64 = pages
            .iter()
            .map(|page| page.width as u64 * page.height as u64 * 4)
            .sum();
        assert_eq!(report.allocated_bytes, page_bytes);
        assert!(report.allocated_bytes > report.source_bytes);
    }

    #[test]
    fn atlas_telemetry_reports_page_occupancy_uploads_and_ordered_page_runs() {
        use super::{asset_layout_telemetry, TextureBindingKey};

        let scene = scene_with_assets();
        let (pages, regions, report) =
            prepare_asset_layout(&scene, AssetLayoutMode::Atlas, 16_384).unwrap();
        let draws = vec![
            Draw {
                asset_id: 1,
                start_vpos: 0,
                end_vpos: 1,
                anchor_vpos: 0,
                owner_order: 0,
                comment_index: 0,
                primitive_index: 0,
                rect: [0.0; 4],
                projection: [0.0; 16],
                alpha: 1.0,
                anchor_x: 0.0,
                speed_x: 0.0,
            },
            Draw {
                asset_id: 2,
                start_vpos: 0,
                end_vpos: 1,
                anchor_vpos: 0,
                owner_order: 1,
                comment_index: 0,
                primitive_index: 0,
                rect: [0.0; 4],
                projection: [0.0; 16],
                alpha: 1.0,
                anchor_x: 0.0,
                speed_x: 0.0,
            },
            Draw {
                asset_id: 1,
                start_vpos: 0,
                end_vpos: 1,
                anchor_vpos: 0,
                owner_order: 2,
                comment_index: 0,
                primitive_index: 0,
                rect: [0.0; 4],
                projection: [0.0; 16],
                alpha: 1.0,
                anchor_x: 0.0,
                speed_x: 0.0,
            },
        ];
        let telemetry = asset_layout_telemetry(&scene, &pages, &regions, &draws, &report).unwrap();

        let page_telemetry = telemetry.pages.as_ref().unwrap();
        assert_eq!(page_telemetry.len(), pages.len());
        assert_eq!(page_telemetry[0].width, pages[0].width);
        assert_eq!(page_telemetry[0].height, pages[0].height);
        assert_eq!(page_telemetry[0].used_texels, 33 * 19 + 21 * 11);
        assert_eq!(
            page_telemetry[0].allocated_texels,
            u64::from(pages[0].width) * u64::from(pages[0].height)
        );
        assert_eq!(telemetry.texture_upload_call_count, Some(2));
        assert_eq!(telemetry.texture_upload_bytes, Some(report.source_bytes));
        let expected_runs = draws
            .iter()
            .map(|draw| TextureBindingKey::StaticPage(regions[&draw.asset_id].page))
            .collect::<Vec<_>>();
        assert_eq!(
            telemetry.draw_order_page_bind_run_count,
            Some(super::contiguous_binding_runs(&expected_runs).len() as u64)
        );
        let json = serde_json::to_value(&telemetry).unwrap();
        assert_eq!(json["telemetryScope"], "nct1-static-assets");
        assert_eq!(json["effectiveLayout"], "atlas");
        assert_eq!(json["bundleBuildCpuWallTimeNs"], 0);
        assert_eq!(
            json["residentMemory"]["physicalVramBytes"],
            serde_json::Value::Null
        );
        assert!(!json["residentMemory"]["unknownReasons"]
            .as_array()
            .unwrap()
            .is_empty());

        let (separate_pages, separate_regions, separate_report) =
            prepare_asset_layout(&scene, AssetLayoutMode::Separate, 16_384).unwrap();
        let separate_telemetry = asset_layout_telemetry(
            &scene,
            &separate_pages,
            &separate_regions,
            &draws,
            &separate_report,
        )
        .unwrap();
        assert_eq!(separate_telemetry.draw_order_page_bind_run_count, Some(3));
    }

    #[test]
    fn atlas_telemetry_handles_empty_and_multiple_pages_and_reports_overflow() {
        use super::{page_occupancies, GpuRegion};
        use crate::atlas::AtlasPage;
        use std::collections::HashMap;

        let empty = page_occupancies(&[], &HashMap::new()).unwrap();
        assert!(empty.is_empty());
        let pages = vec![
            AtlasPage {
                width: 4,
                height: 4,
            },
            AtlasPage {
                width: 8,
                height: 2,
            },
        ];
        let regions = HashMap::from([
            (
                1,
                GpuRegion {
                    page: 0,
                    x: 1,
                    y: 1,
                    width: 2,
                    height: 2,
                },
            ),
            (
                2,
                GpuRegion {
                    page: 1,
                    x: 0,
                    y: 0,
                    width: 3,
                    height: 2,
                },
            ),
        ]);
        let occupancy = page_occupancies(&pages, &regions).unwrap();
        assert_eq!(
            occupancy
                .iter()
                .map(|page| (page.used_texels, page.allocated_texels))
                .collect::<Vec<_>>(),
            vec![(4, 16), (6, 16)]
        );

        let overflowing_page = vec![AtlasPage {
            width: u32::MAX,
            height: u32::MAX,
        }];
        let overflowing_regions = HashMap::from([
            (
                1,
                GpuRegion {
                    page: 0,
                    x: 0,
                    y: 0,
                    width: u32::MAX,
                    height: u32::MAX,
                },
            ),
            (
                2,
                GpuRegion {
                    page: 0,
                    x: 0,
                    y: 0,
                    width: u32::MAX,
                    height: u32::MAX,
                },
            ),
        ]);
        assert!(page_occupancies(&overflowing_page, &overflowing_regions).is_err());
    }

    #[test]
    fn streamed_asset_telemetry_serializes_unknown_categories_with_reasons() {
        use super::AssetTelemetryReport;

        let report = AssetTelemetryReport::unavailable_for_streamed_assets(
            "NCT2 assets are uploaded incrementally as distinct textures",
        );
        let json = serde_json::to_value(report).unwrap();
        assert_eq!(json["effectiveLayout"], "streamed-distinct-textures");
        assert!(json["textureUploadCallCount"].is_null());
        assert!(json["residentMemory"]["physicalVramBytes"].is_null());
        assert!(
            json["residentMemory"]["unknownReasons"]
                .as_array()
                .unwrap()
                .len()
                >= 1
        );
    }

    #[test]
    fn each_submission_holds_its_generation_credit_until_its_completion() {
        struct GenerationResources {
            _credit: GpuCredit,
        }

        let budget = GpuBudget::new();
        let resources = Arc::new(GenerationResources {
            _credit: budget.try_reserve(128).unwrap(),
        });
        let weak = Arc::downgrade(&resources);
        let earlier_submit = retire_after_submission(Arc::clone(&resources));
        let later_submit = retire_after_submission(Arc::clone(&resources));
        drop(resources);
        assert_eq!(budget.used_bytes(), 128);

        earlier_submit();
        assert!(weak.upgrade().is_some());
        assert_eq!(budget.used_bytes(), 128);

        later_submit();
        assert!(weak.upgrade().is_none());
        assert_eq!(budget.used_bytes(), 0);
    }

    #[test]
    fn frame_uniform_write_precedes_its_submission_without_an_intervening_write() {
        let operations = RefCell::new(Vec::new());
        let submission = write_uniform_then_submit(
            || operations.borrow_mut().push("uniform"),
            || {
                operations.borrow_mut().push("submit");
                17
            },
        );
        assert_eq!(submission, 17);
        assert_eq!(*operations.borrow(), ["uniform", "submit"]);
    }

    #[test]
    fn applying_element_appends_draws_in_owner_comment_primitive_order() {
        let existing = Draw {
            asset_id: 1,
            start_vpos: 0,
            end_vpos: 100,
            anchor_vpos: 0,
            owner_order: 0,
            comment_index: 0,
            primitive_index: 0,
            rect: [0.0; 4],
            projection: [0.0; 16],
            alpha: 1.0,
            anchor_x: 0.0,
            speed_x: 0.0,
        };
        let mut late_primitive_1 = existing.clone();
        late_primitive_1.asset_id = 9;
        late_primitive_1.comment_index = 1;
        late_primitive_1.primitive_index = 1;
        let mut late_primitive_0 = late_primitive_1.clone();
        late_primitive_0.asset_id = 7;
        late_primitive_0.primitive_index = 0;
        let later_owner = Draw {
            owner_order: 1,
            ..existing.clone()
        };
        let element = Element {
            ordinal: 0,
            draws: vec![late_primitive_0, late_primitive_1, later_owner],
        };
        let mut draws = vec![existing];
        let mut index = SceneIndex::new(&draws);
        assert_eq!(index.candidate_draw_indices(42), vec![0]);
        let mut cached_seconds = VecDeque::from([Arc::new(CachedSecond {
            second: 0,
            generation: 0,
            bundle: None,
            _draw_buffer: None,
            draw_buffer_bytes: 0,
            _draw_bind_group: None,
            _draw_generation: Arc::new(SceneGeneration::new(0)),
            _draw_credit: None,
            _stream_assets: Vec::new(),
        })]);
        let mut next_ordinal = 0;
        let mut generation = Arc::new(SceneGeneration::new(0));
        let budget = CpuBudget::new();
        let mut draw_metadata_credit = None;
        apply_element_state(
            &mut draws,
            &mut index,
            &mut cached_seconds,
            &mut next_ordinal,
            &mut generation,
            &budget,
            &mut draw_metadata_credit,
            &element,
        )
        .unwrap();
        assert_eq!(next_ordinal, 1);
        assert_eq!(generation.id, 1);
        assert!(cached_seconds.is_empty());
        assert_eq!(
            draws
                .iter()
                .map(|draw| (draw.owner_order, draw.comment_index, draw.primitive_index))
                .collect::<Vec<_>>(),
            [(0, 0, 0), (0, 1, 0), (0, 1, 1), (1, 0, 0)]
        );
        assert_eq!(index.candidate_draw_indices(42), vec![0, 1, 2, 3]);
        assert_eq!(
            budget.used_bytes(),
            RENDERER_DRAW_METADATA_RESERVATION_BYTES
        );
    }

    #[test]
    fn renderer_draw_metadata_credit_is_reserved_before_copy_and_retained() {
        let budget = CpuBudget::new();
        let blocker = budget
            .try_reserve(CPU_BUDGET_BYTES - RENDERER_DRAW_METADATA_RESERVATION_BYTES + 1)
            .unwrap();
        let mut draws = Vec::new();
        let mut index = SceneIndex::new(&draws);
        assert_eq!(index.candidate_draw_indices(42), Vec::<usize>::new());
        let mut cached_seconds = VecDeque::from([Arc::new(CachedSecond {
            second: 0,
            generation: 0,
            bundle: None,
            _draw_buffer: None,
            draw_buffer_bytes: 0,
            _draw_bind_group: None,
            _draw_generation: Arc::new(SceneGeneration::new(0)),
            _draw_credit: None,
            _stream_assets: Vec::new(),
        })]);
        let mut next_ordinal = 0;
        let mut generation = Arc::new(SceneGeneration::new(0));
        let mut draw_metadata_credit = None;
        let element = Element {
            ordinal: 0,
            draws: vec![Draw {
                asset_id: 1,
                start_vpos: 0,
                end_vpos: 100,
                anchor_vpos: 0,
                owner_order: 0,
                comment_index: 0,
                primitive_index: 0,
                rect: [0.0; 4],
                projection: [0.0; 16],
                alpha: 1.0,
                anchor_x: 0.0,
                speed_x: 0.0,
            }],
        };
        let failed = apply_element_state(
            &mut draws,
            &mut index,
            &mut cached_seconds,
            &mut next_ordinal,
            &mut generation,
            &budget,
            &mut draw_metadata_credit,
            &element,
        );
        assert!(failed.is_err());
        assert!(draws.is_empty());
        assert_eq!(index.candidate_draw_indices(42), Vec::<usize>::new());
        assert_eq!(cached_seconds.len(), 1);
        assert_eq!(next_ordinal, 0);
        assert_eq!(generation.id, 0);
        assert!(draw_metadata_credit.is_none());
        assert_eq!(
            budget.used_bytes(),
            CPU_BUDGET_BYTES - RENDERER_DRAW_METADATA_RESERVATION_BYTES + 1
        );

        drop(blocker);
        apply_element_state(
            &mut draws,
            &mut index,
            &mut cached_seconds,
            &mut next_ordinal,
            &mut generation,
            &budget,
            &mut draw_metadata_credit,
            &element,
        )
        .unwrap();
        assert_eq!(draws.len(), 1);
        assert!(draws.capacity() >= crate::protocol::MAX_DRAWS as usize);
        assert_eq!(next_ordinal, 1);
        assert_eq!(
            budget.used_bytes(),
            RENDERER_DRAW_METADATA_RESERVATION_BYTES
        );

        drop(draws);
        drop(index);
        assert_eq!(
            budget.used_bytes(),
            RENDERER_DRAW_METADATA_RESERVATION_BYTES
        );
        drop(draw_metadata_credit);
        assert_eq!(budget.used_bytes(), 0);
    }

    #[test]
    fn submitted_cached_second_retains_generation_and_credit_until_last_completion() {
        let budget = GpuBudget::new();
        let old_generation = Arc::new(SceneGeneration::new(0));
        let old = Arc::new(CachedSecond {
            second: 0,
            generation: 0,
            bundle: None,
            _draw_buffer: None,
            draw_buffer_bytes: 0,
            _draw_bind_group: None,
            _draw_generation: Arc::clone(&old_generation),
            _draw_credit: Some(budget.try_reserve(128).unwrap()),
            _stream_assets: Vec::new(),
        });
        let weak_cache = Arc::downgrade(&old);
        let weak_generation = Arc::downgrade(&old_generation);
        let first = retire_after_submission(Arc::clone(&old));
        let second = retire_after_submission(Arc::clone(&old));
        drop(old);
        drop(old_generation);
        assert_eq!(budget.used_bytes(), 128);

        first();
        assert!(weak_cache.upgrade().is_some());
        assert!(weak_generation.upgrade().is_some());
        assert_eq!(budget.used_bytes(), 128);

        second();
        assert!(weak_cache.upgrade().is_none());
        assert!(weak_generation.upgrade().is_none());
        assert_eq!(budget.used_bytes(), 0);
    }
}
