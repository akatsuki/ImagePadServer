use std::error::Error;
use std::sync::mpsc;

use bytemuck::{Pod, Zeroable};
use nico_compositord::motion_precision::encode_motion_payload;
use nico_compositord::protocol::{frame_vpos, read_scene, Asset, Draw, Header, Scene, MAX_FRAMES};
use nico_compositord::renderer::{AssetLayoutMode, GpuOptions, Renderer};
use nico_compositord::scene::{contiguous_texture_runs, is_visible_at, SceneIndex};
use sha2::Digest;
use wgpu::util::DeviceExt;

const RED_GOLDEN: &[u8] =
    include_bytes!("../../../internal/nicorender/testdata/timeline/wire/red-33x19-3frames.nct");

fn render_one_for_test(
    scene: &Scene,
    frame: u32,
) -> Result<(Vec<u8>, String, String), Box<dyn Error>> {
    let (rgba, adapter, backend, _) =
        render_one_for_test_with_layout(scene, frame, AssetLayoutMode::Separate)?;
    Ok((rgba, adapter, backend))
}

fn render_one_for_test_with_layout(
    scene: &Scene,
    frame: u32,
    layout: AssetLayoutMode,
) -> Result<
    (
        Vec<u8>,
        String,
        String,
        nico_compositord::renderer::AssetLayoutReport,
    ),
    Box<dyn Error>,
> {
    let mut renderer = Renderer::new_with_asset_layout(
        scene,
        &GpuOptions {
            backends: wgpu::Backends::all(),
            allow_software: true,
        },
        layout,
    )?;
    let header = &scene.header;
    let target = renderer.device().create_texture(&wgpu::TextureDescriptor {
        label: Some("nct1 test render target"),
        size: wgpu::Extent3d {
            width: header.width,
            height: header.height,
            depth_or_array_layers: 1,
        },
        mip_level_count: 1,
        sample_count: 1,
        dimension: wgpu::TextureDimension::D2,
        format: wgpu::TextureFormat::Rgba8Unorm,
        usage: wgpu::TextureUsages::RENDER_ATTACHMENT | wgpu::TextureUsages::COPY_SRC,
        view_formats: &[],
    });
    renderer.submit_frame(frame, &target)?;

    let bytes_per_row = header.width * 4;
    let padded_bytes_per_row = bytes_per_row.div_ceil(wgpu::COPY_BYTES_PER_ROW_ALIGNMENT)
        * wgpu::COPY_BYTES_PER_ROW_ALIGNMENT;
    let buffer = renderer.device().create_buffer(&wgpu::BufferDescriptor {
        label: Some("nct1 test readback"),
        size: padded_bytes_per_row as u64 * header.height as u64,
        usage: wgpu::BufferUsages::COPY_DST | wgpu::BufferUsages::MAP_READ,
        mapped_at_creation: false,
    });
    let mut encoder = renderer
        .device()
        .create_command_encoder(&wgpu::CommandEncoderDescriptor {
            label: Some("nct1 test readback encoder"),
        });
    encoder.copy_texture_to_buffer(
        wgpu::ImageCopyTexture {
            texture: &target,
            mip_level: 0,
            origin: wgpu::Origin3d::ZERO,
            aspect: wgpu::TextureAspect::All,
        },
        wgpu::ImageCopyBuffer {
            buffer: &buffer,
            layout: wgpu::ImageDataLayout {
                offset: 0,
                bytes_per_row: Some(padded_bytes_per_row),
                rows_per_image: Some(header.height),
            },
        },
        wgpu::Extent3d {
            width: header.width,
            height: header.height,
            depth_or_array_layers: 1,
        },
    );
    renderer.queue().submit(Some(encoder.finish()));

    let slice = buffer.slice(..);
    let (sender, receiver) = std::sync::mpsc::channel();
    slice.map_async(wgpu::MapMode::Read, move |result| {
        let _ = sender.send(result);
    });
    renderer.device().poll(wgpu::Maintain::Wait);
    receiver.recv()??;
    let mapped = slice.get_mapped_range();
    let mut rgba = Vec::with_capacity((bytes_per_row * header.height) as usize);
    for row in 0..header.height as usize {
        let start = row * padded_bytes_per_row as usize;
        rgba.extend_from_slice(&mapped[start..start + bytes_per_row as usize]);
    }
    drop(mapped);
    buffer.unmap();

    let info = renderer.adapter_info();
    Ok((
        rgba,
        info.name.clone(),
        format!("{:?}", info.backend),
        renderer.asset_layout_report().clone(),
    ))
}

fn test_scene(draws: Vec<Draw>, assets: Vec<Asset>) -> Scene {
    Scene {
        header: Header {
            width: 33,
            height: 19,
            frame_count: 3,
            fps_num: 30,
            fps_den: 1,
            bundle_sha256: [0; 32],
        },
        assets,
        draws,
    }
}

fn asset(id: u32, rgba: [u8; 4]) -> Asset {
    let pixels = rgba.repeat(33 * 19);
    Asset {
        id,
        width: 33,
        height: 19,
        sha256: sha2::Sha256::digest(&pixels).into(),
        rgba: pixels,
    }
}

fn top_blue_bottom_asset(id: u32) -> Asset {
    let mut pixels = Vec::with_capacity(33 * 19 * 4);
    for y in 0..19 {
        let row = if y < 9 {
            [255, 0, 0, 255]
        } else {
            [0, 0, 255, 255]
        };
        pixels.extend(row.repeat(33));
    }
    Asset {
        id,
        width: 33,
        height: 19,
        sha256: sha2::Sha256::digest(&pixels).into(),
        rgba: pixels,
    }
}

fn ortho_projection() -> [f32; 16] {
    [
        2.0 / 33.0,
        0.0,
        0.0,
        0.0,
        0.0,
        -2.0 / 19.0,
        0.0,
        0.0,
        0.0,
        0.0,
        1.0,
        0.0,
        -1.0,
        1.0,
        0.0,
        1.0,
    ]
}

fn draw(
    asset_id: u32,
    owner_order: u32,
    comment_index: u32,
    rect: [f32; 4],
    start_vpos: i32,
    end_vpos: i32,
    speed_x: f64,
) -> Draw {
    Draw {
        asset_id,
        start_vpos,
        end_vpos,
        anchor_vpos: 0,
        owner_order,
        comment_index,
        primitive_index: 0,
        rect,
        projection: ortho_projection(),
        alpha: 1.0,
        anchor_x: rect[0] as f64,
        speed_x,
    }
}

#[repr(C)]
#[derive(Clone, Copy, Pod, Zeroable)]
struct DebugGpuDraw {
    rect: [f32; 4],
    projection: [[f32; 4]; 4],
    interval: [i32; 4],
    movement: [f32; 2],
    texture_extent_packed: u32,
    padding: u32,
    motion_precision: [u32; 4],
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Pod, Zeroable)]
struct DebugGpuPosition {
    pixel: [f32; 4],
    clip: [f32; 4],
}

fn debug_gpu_draw(draw: &Draw) -> DebugGpuDraw {
    let mut projection = [[0.0; 4]; 4];
    for (index, column) in projection.iter_mut().enumerate() {
        column.copy_from_slice(&draw.projection[index * 4..index * 4 + 4]);
    }
    let speed_x_high = draw.speed_x as f32;
    let motion_precision = encode_motion_payload(draw.anchor_x, draw.rect[0], draw.speed_x)
        .expect("diagnostic motion payload is representable");
    DebugGpuDraw {
        rect: draw.rect,
        projection,
        interval: [draw.start_vpos, draw.end_vpos, draw.anchor_vpos, 0],
        movement: [speed_x_high, draw.alpha],
        texture_extent_packed: 33 | (19 << 16),
        padding: 0,
        motion_precision,
    }
}

fn debug_vertex_pipeline(device: &wgpu::Device) -> (wgpu::RenderPipeline, wgpu::BindGroupLayout) {
    let bind_group_layout = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
        label: Some("NCT1 diagnostic draw/frame/output bindings"),
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
            wgpu::BindGroupLayoutEntry {
                binding: 2,
                visibility: wgpu::ShaderStages::VERTEX,
                ty: wgpu::BindingType::Buffer {
                    ty: wgpu::BufferBindingType::Storage { read_only: false },
                    has_dynamic_offset: false,
                    min_binding_size: None,
                },
                count: None,
            },
            wgpu::BindGroupLayoutEntry {
                binding: 3,
                visibility: wgpu::ShaderStages::VERTEX,
                ty: wgpu::BindingType::Buffer {
                    ty: wgpu::BufferBindingType::Storage { read_only: true },
                    has_dynamic_offset: false,
                    min_binding_size: None,
                },
                count: None,
            },
        ],
    });
    let shader = device.create_shader_module(wgpu::ShaderModuleDescriptor {
        label: Some("NCT1 production and diagnostic position shader"),
        source: wgpu::ShaderSource::Wgsl(include_str!("../src/timeline.wgsl").into()),
    });
    let pipeline_layout = device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
        label: Some("NCT1 diagnostic position pipeline layout"),
        bind_group_layouts: &[&bind_group_layout],
        push_constant_ranges: &[],
    });
    let pipeline = device.create_render_pipeline(&wgpu::RenderPipelineDescriptor {
        label: Some("NCT1 GPU vertex-position readback pipeline"),
        layout: Some(&pipeline_layout),
        vertex: wgpu::VertexState {
            module: &shader,
            entry_point: "vs_debug",
            buffers: &[],
            compilation_options: wgpu::PipelineCompilationOptions::default(),
        },
        fragment: Some(wgpu::FragmentState {
            module: &shader,
            entry_point: "fs_debug",
            compilation_options: wgpu::PipelineCompilationOptions::default(),
            targets: &[Some(wgpu::ColorTargetState {
                format: wgpu::TextureFormat::Rgba8Unorm,
                blend: None,
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
    (pipeline, bind_group_layout)
}

fn read_gpu_vertex_positions(
    device: &wgpu::Device,
    queue: &wgpu::Queue,
    pipeline: &wgpu::RenderPipeline,
    bind_group_layout: &wgpu::BindGroupLayout,
    draws: &[Draw],
    frame_vpos: &[i32],
) -> Result<Vec<DebugGpuPosition>, Box<dyn Error>> {
    if draws.is_empty() || frame_vpos.is_empty() {
        return Err("diagnostic GPU position readback needs draws and frames".into());
    }
    let gpu_draws: Vec<DebugGpuDraw> = draws.iter().map(debug_gpu_draw).collect();
    let draw_buffer = device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
        label: Some("NCT1 diagnostic draw records"),
        contents: bytemuck::cast_slice(&gpu_draws),
        usage: wgpu::BufferUsages::STORAGE,
    });
    let frame_buffer = device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
        label: Some("NCT1 diagnostic production-frame uniform"),
        contents: bytemuck::cast_slice(&[0_i32, 0, 0, 0]),
        usage: wgpu::BufferUsages::UNIFORM,
    });
    let debug_frames = device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
        label: Some("NCT1 diagnostic frame vpos values"),
        contents: bytemuck::cast_slice(frame_vpos),
        usage: wgpu::BufferUsages::STORAGE,
    });
    let instance_count = draws
        .len()
        .checked_mul(frame_vpos.len())
        .ok_or("diagnostic GPU instance count overflow")?;
    let position_count = instance_count
        .checked_mul(6)
        .ok_or("diagnostic GPU position count overflow")?;
    let position_bytes = (position_count * std::mem::size_of::<DebugGpuPosition>()) as u64;
    let positions = device.create_buffer(&wgpu::BufferDescriptor {
        label: Some("NCT1 diagnostic GPU vertex positions"),
        size: position_bytes,
        usage: wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_SRC,
        mapped_at_creation: false,
    });
    let readback = device.create_buffer(&wgpu::BufferDescriptor {
        label: Some("NCT1 diagnostic position readback"),
        size: position_bytes,
        usage: wgpu::BufferUsages::COPY_DST | wgpu::BufferUsages::MAP_READ,
        mapped_at_creation: false,
    });
    let bind_group = device.create_bind_group(&wgpu::BindGroupDescriptor {
        label: Some("NCT1 diagnostic position bind group"),
        layout: bind_group_layout,
        entries: &[
            wgpu::BindGroupEntry {
                binding: 0,
                resource: draw_buffer.as_entire_binding(),
            },
            wgpu::BindGroupEntry {
                binding: 1,
                resource: frame_buffer.as_entire_binding(),
            },
            wgpu::BindGroupEntry {
                binding: 2,
                resource: positions.as_entire_binding(),
            },
            wgpu::BindGroupEntry {
                binding: 3,
                resource: debug_frames.as_entire_binding(),
            },
        ],
    });
    let target = device.create_texture(&wgpu::TextureDescriptor {
        label: Some("NCT1 diagnostic vertex-only color target"),
        size: wgpu::Extent3d {
            width: 1,
            height: 1,
            depth_or_array_layers: 1,
        },
        mip_level_count: 1,
        sample_count: 1,
        dimension: wgpu::TextureDimension::D2,
        format: wgpu::TextureFormat::Rgba8Unorm,
        usage: wgpu::TextureUsages::RENDER_ATTACHMENT,
        view_formats: &[],
    });
    let target_view = target.create_view(&wgpu::TextureViewDescriptor::default());
    let mut encoder = device.create_command_encoder(&wgpu::CommandEncoderDescriptor {
        label: Some("NCT1 diagnostic position command encoder"),
    });
    {
        let mut pass = encoder.begin_render_pass(&wgpu::RenderPassDescriptor {
            label: Some("NCT1 diagnostic position render pass"),
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
        pass.set_pipeline(pipeline);
        pass.set_bind_group(0, &bind_group, &[]);
        pass.draw(0..6, 0..u32::try_from(instance_count)?);
    }
    encoder.copy_buffer_to_buffer(&positions, 0, &readback, 0, position_bytes);
    queue.submit(Some(encoder.finish()));

    let slice = readback.slice(..);
    let (sender, receiver) = mpsc::channel();
    slice.map_async(wgpu::MapMode::Read, move |result| {
        let _ = sender.send(result);
    });
    device.poll(wgpu::Maintain::Wait);
    receiver.recv()??;
    let mapped = slice.get_mapped_range();
    let result = bytemuck::cast_slice::<u8, DebugGpuPosition>(&mapped).to_vec();
    drop(mapped);
    readback.unmap();
    Ok(result)
}

struct HardwareDiagnosticDevice {
    device: wgpu::Device,
    queue: wgpu::Queue,
    adapter_info: wgpu::AdapterInfo,
}

fn hardware_diagnostic_device() -> Result<Option<HardwareDiagnosticDevice>, Box<dyn Error>> {
    let instance = wgpu::Instance::new(wgpu::InstanceDescriptor {
        backends: wgpu::Backends::all(),
        ..Default::default()
    });
    let adapter = pollster::block_on(instance.request_adapter(&wgpu::RequestAdapterOptions {
        power_preference: wgpu::PowerPreference::HighPerformance,
        compatible_surface: None,
        force_fallback_adapter: false,
    }));
    let Some(adapter) = adapter else {
        eprintln!("SKIP: no hardware WGPU adapter is available for the GPU position readback");
        return Ok(None);
    };
    let info = adapter.get_info();
    if info.device_type == wgpu::DeviceType::Cpu {
        eprintln!(
            "SKIP: GPU position readback requires a hardware adapter; found CPU adapter {} ({:?})",
            info.name, info.backend
        );
        return Ok(None);
    }
    let required_features = wgpu::Features::VERTEX_WRITABLE_STORAGE;
    if !adapter.features().contains(required_features) {
        eprintln!(
            "SKIP: adapter {} ({:?}) does not support {:?} for vertex storage readback",
            info.name, info.backend, required_features
        );
        return Ok(None);
    }
    let required_limits = adapter.limits();
    let (device, queue) = pollster::block_on(adapter.request_device(
        &wgpu::DeviceDescriptor {
            label: Some("NCT1 opt-in GPU position readback device"),
            required_features,
            required_limits,
        },
        None,
    ))?;
    Ok(Some(HardwareDiagnosticDevice {
        device,
        queue,
        adapter_info: info,
    }))
}

fn projected_pixel_from_clip(clip: [f32; 4], width: u32, height: u32) -> (f64, f64) {
    let ndc_x = clip[0] as f64 / clip[3] as f64;
    let ndc_y = clip[1] as f64 / clip[3] as f64;
    (
        (ndc_x + 1.0) * width as f64 / 2.0,
        (1.0 - ndc_y) * height as f64 / 2.0,
    )
}

fn project_reference_pixel(draw: &Draw, x: f64, y: f64, width: u32, height: u32) -> (f64, f64) {
    let p = draw.projection;
    let clip_x = p[0] as f64 * x + p[4] as f64 * y + p[12] as f64;
    let clip_y = p[1] as f64 * x + p[5] as f64 * y + p[13] as f64;
    let clip_w = p[3] as f64 * x + p[7] as f64 * y + p[15] as f64;
    (
        (clip_x / clip_w + 1.0) * width as f64 / 2.0,
        (1.0 - clip_y / clip_w) * height as f64 / 2.0,
    )
}

#[test]
fn texture_runs_preserve_a_b_a_order() {
    let order = vec![(0_u32, 0_u32, 7_u32), (0, 1, 9), (1, 2, 7)];
    let runs = contiguous_texture_runs(&order);
    assert_eq!(runs, vec![(7, 0, 1), (9, 1, 2), (7, 2, 3)]);
}

#[test]
fn second_index_uses_half_open_intervals_and_keeps_only_two_seconds() {
    let scene = test_scene(
        vec![
            draw(1, 0, 0, [0.0, 0.0, 1.0, 1.0], 0, 100, 0.0),
            draw(1, 0, 1, [0.0, 0.0, 1.0, 1.0], 100, 101, 0.0),
            draw(1, 0, 2, [0.0, 0.0, 1.0, 1.0], -1, 1, 0.0),
        ],
        vec![asset(1, [255, 255, 255, 255])],
    );
    let mut index = SceneIndex::new(&scene.draws);

    assert_eq!(index.candidate_draw_indices(0), vec![0, 2]);
    assert_eq!(index.candidate_draw_indices(99), vec![0, 2]);
    assert_eq!(index.candidate_draw_indices(100), vec![1]);
    assert!(!is_visible_at(&scene.draws[0], 100));
    assert!(is_visible_at(&scene.draws[1], 100));
    assert!(is_visible_at(&scene.draws[2], 0));
    assert!(!is_visible_at(&scene.draws[2], 1));
    assert_eq!(index.cached_seconds(), 2);
    index.candidate_draw_indices(200);
    assert_eq!(index.cached_seconds(), 2);
}

#[test]
fn second_index_sorts_by_owner_comment_and_primitive_without_texture_sorting() {
    let draws = vec![
        draw(7, 1, 2, [0.0; 4], 0, 100, 0.0),
        draw(7, 0, 1, [0.0; 4], 0, 100, 0.0),
        draw(9, 0, 0, [0.0; 4], 0, 100, 0.0),
    ];
    let mut index = SceneIndex::new(&draws);
    assert_eq!(index.candidate_draw_indices(0), vec![2, 1, 0]);
}

#[test]
#[ignore = "requires an actual WGPU adapter; run explicitly for hardware qualification"]
fn pure_red_fixture_renders_exact_rgba() {
    let scene = read_scene(RED_GOLDEN).expect("valid red NCT1 fixture");
    let (rgba, adapter, backend) = render_one_for_test(&scene, 0).expect("GPU render/readback");
    assert!(rgba.chunks_exact(4).all(|pixel| pixel == [255, 0, 0, 255]));
    eprintln!("render adapter={adapter}, backend={backend}");
}

#[test]
#[ignore = "requires an actual WGPU adapter; run explicitly for hardware qualification"]
fn top_left_projection_keeps_distinct_texture_rows_in_place() {
    let scene = test_scene(
        vec![draw(3, 0, 0, [0.0, 0.0, 33.0, 19.0], 0, 100, 0.0)],
        vec![top_blue_bottom_asset(3)],
    );
    let (rgba, adapter, backend) = render_one_for_test(&scene, 0).expect("GPU render/readback");
    let top = &rgba[(0 * 33 + 16) * 4..(0 * 33 + 16) * 4 + 4];
    let bottom = &rgba[(18 * 33 + 16) * 4..(18 * 33 + 16) * 4 + 4];
    assert_eq!(top, [255, 0, 0, 255], "top output pixel: {top:?}");
    assert_eq!(bottom, [0, 0, 255, 255], "bottom output pixel: {bottom:?}");
    eprintln!("render adapter={adapter}, backend={backend}");
}

#[test]
#[ignore = "requires an actual WGPU adapter; run explicitly for hardware qualification"]
fn alpha_compositing_preserves_owner_order_across_a_b_a_texture_runs() {
    let scene = test_scene(
        vec![
            draw(7, 1, 2, [10.0, 0.0, 10.0, 19.0], 0, 100, 0.0),
            draw(9, 0, 1, [10.0, 0.0, 10.0, 19.0], 0, 100, 0.0),
            draw(7, 0, 0, [0.0, 0.0, 33.0, 19.0], 0, 100, 0.0),
        ],
        vec![asset(7, [128, 0, 0, 128]), asset(9, [0, 128, 0, 128])],
    );
    let (rgba, adapter, backend) = render_one_for_test(&scene, 0).expect("GPU render/readback");
    let center = &rgba[(5 * 33 + 15) * 4..(5 * 33 + 15) * 4 + 4];
    assert!(center[0].abs_diff(159) <= 1, "center red: {center:?}");
    assert!(center[1].abs_diff(64) <= 1, "center green: {center:?}");
    assert_eq!(center[2], 0, "center blue: {center:?}");
    assert!(center[3].abs_diff(223) <= 1, "center alpha: {center:?}");
    let left = &rgba[(5 * 33 + 5) * 4..(5 * 33 + 5) * 4 + 4];
    assert_eq!(left, [128, 0, 0, 128]);
    eprintln!("render adapter={adapter}, backend={backend}");
}

#[test]
#[ignore = "requires an actual WGPU adapter; run explicitly for atlas parity qualification"]
fn atlas_render_is_byte_identical_and_batches_distinct_assets_on_one_page() {
    let mut draws = vec![
        draw(7, 0, 0, [0.25, 0.5, 32.5, 18.0], 0, 100, 0.0),
        draw(9, 0, 1, [0.25, 0.5, 32.5, 18.0], 0, 100, 0.0),
        draw(11, 1, 2, [0.25, 0.5, 32.5, 18.0], 0, 100, 0.0),
        draw(9, 2, 3, [0.25, 0.5, 32.5, 18.0], 0, 100, 0.0),
        draw(13, 3, 4, [0.25, 0.5, 32.5, 18.0], 0, 100, 0.0),
    ];
    for (index, draw) in draws.iter_mut().enumerate() {
        draw.alpha = [0.85, 0.7, 0.55, 0.9, 0.6][index];
    }
    let scene = test_scene(
        draws,
        vec![
            asset(7, [96, 0, 0, 128]),
            asset(9, [0, 80, 0, 128]),
            asset(11, [0, 0, 112, 160]),
            asset(13, [88, 48, 0, 160]),
        ],
    );

    let (separate, _, _, separate_report) =
        render_one_for_test_with_layout(&scene, 0, AssetLayoutMode::Separate)
            .expect("separate-texture render/readback");
    let (atlas, adapter, backend, atlas_report) =
        render_one_for_test_with_layout(&scene, 0, AssetLayoutMode::Atlas)
            .expect("atlas render/readback");

    assert_eq!(atlas, separate, "atlas changed output bytes");
    assert_eq!(separate_report.actual, AssetLayoutMode::Separate);
    assert_eq!(separate_report.page_count, 4);
    assert_eq!(atlas_report.requested, AssetLayoutMode::Atlas);
    assert_eq!(atlas_report.actual, AssetLayoutMode::Atlas);
    assert_eq!(atlas_report.page_count, 1);
    assert_eq!(atlas_report.fallback_reason, None);
    assert!(atlas_report.nonzero_origin_count > 0);
    eprintln!("atlas parity adapter={adapter}, backend={backend}");
}

#[test]
#[ignore = "requires an actual WGPU adapter; run explicitly for hardware qualification"]
fn motion_uses_absolute_vpos_and_end_vpos_is_exclusive() {
    let scene = test_scene(
        vec![draw(1, 0, 0, [0.0, 0.0, 1.0, 1.0], 0, 6, 1.0 / 3.0)],
        vec![asset(1, [255, 255, 255, 255])],
    );
    let first = render_one_for_test(&scene, 0).expect("frame 0").0;
    let second = render_one_for_test(&scene, 1).expect("frame 1").0;
    let end = render_one_for_test(&scene, 2).expect("exclusive end").0;
    assert_eq!(&first[0..4], [255, 255, 255, 255]);
    assert_eq!(&second[0..4], [0, 0, 0, 0]);
    assert_eq!(&second[4..8], [255, 255, 255, 255]);
    assert!(end.chunks_exact(4).all(|pixel| pixel == [0, 0, 0, 0]));
}

#[test]
fn timeline_shader_exposes_a_diagnostic_vertex_entry_point() {
    let shader = include_str!("../src/timeline.wgsl");
    assert!(
        shader.contains("fn compute_vertex_position("),
        "production and diagnostic entries must share vertex-position arithmetic"
    );
    assert!(
        shader.contains("fn vs_debug("),
        "the diagnostic entry point must be able to read back actual GPU vertex positions"
    );
    assert!(
        shader.contains("@group(0) @binding(3)"),
        "the diagnostic pipeline must accept a batch of frame vpos values"
    );
    assert!(
        shader.contains("debug_frame_vpos.values[frame_index]"),
        "the diagnostic vertex path must index one frame vpos per batched instance"
    );
}

#[test]
fn long_fractional_motion_rejects_unrepresentable_gpu_positions_before_adapter_use() {
    let last_vpos = frame_vpos((MAX_FRAMES - 1) as i64, 60_000, 1_001).unwrap();
    let scene = Scene {
        header: Header {
            width: 33,
            height: 19,
            frame_count: MAX_FRAMES,
            fps_num: 60_000,
            fps_den: 1_001,
            bundle_sha256: [0; 32],
        },
        assets: vec![asset(1, [255, 255, 255, 255])],
        draws: vec![draw(
            1,
            0,
            0,
            [8.125, 4.25, 12.0, 7.0],
            0,
            last_vpos + 1,
            0.1,
        )],
    };

    let error = match Renderer::new(
        &scene,
        &GpuOptions {
            backends: wgpu::Backends::empty(),
            allow_software: false,
        },
    ) {
        Ok(_) => panic!("long 0.1 px/vpos motion should fail the 1/256 px precision guard"),
        Err(error) => error,
    };
    assert!(
        error
            .to_string()
            .contains("cannot be represented within 1/256 pixel"),
        "expected the precision guard to reject before adapter selection, got: {error}"
    );
}

#[test]
fn q40_motion_range_miss_rejects_before_adapter_selection() {
    let scene = test_scene(
        vec![draw(
            1,
            0,
            0,
            [1.0, 1.0, 2.0, 2.0],
            0,
            100,
            (2.0_f64).powi(23),
        )],
        vec![asset(1, [255, 255, 255, 255])],
    );

    let error = match Renderer::new(
        &scene,
        &GpuOptions {
            backends: wgpu::Backends::empty(),
            allow_software: false,
        },
    ) {
        Ok(_) => panic!("finite motion outside signed Q40.40 must reject before adapter selection"),
        Err(error) => error,
    };
    assert!(
        error.to_string().contains("Q40.40") && error.to_string().contains("range"),
        "expected Q40.40 range error before adapter lookup, got: {error}"
    );
}

#[test]
fn non_finite_motion_rejects_before_adapter_selection() {
    let scene = test_scene(
        vec![draw(
            1,
            0,
            0,
            [1.0, 1.0, 2.0, 2.0],
            0,
            100,
            f64::NAN,
        )],
        vec![asset(1, [255, 255, 255, 255])],
    );

    let error = match Renderer::new(
        &scene,
        &GpuOptions {
            backends: wgpu::Backends::empty(),
            allow_software: false,
        },
    ) {
        Ok(_) => panic!("non-finite motion must reject before adapter selection"),
        Err(error) => error,
    };
    assert!(
        error.to_string().contains("Q40.40") && error.to_string().contains("non-finite"),
        "expected Q40.40 non-finite error before adapter lookup, got: {error}"
    );
}

#[test]
#[ignore = "opt-in hardware qualification; set NICO_TIMELINE_GPU_POSITION_TEST=1 and pass --ignored"]
fn gpu_vertex_positions_match_f64_reference_across_fractional_long_and_boundary_cases() {
    if std::env::var("NICO_TIMELINE_GPU_POSITION_TEST").as_deref() != Ok("1") {
        eprintln!(
            "SKIP: set NICO_TIMELINE_GPU_POSITION_TEST=1 to execute the GPU position readback"
        );
        return;
    }
    let diagnostic = hardware_diagnostic_device()
        .unwrap_or_else(|error| panic!("requesting diagnostic WGPU device: {error}"));
    let Some(diagnostic) = diagnostic else {
        return;
    };

    const FPS_NUM: u32 = 60_000;
    const FPS_DEN: u32 = 1_001;
    let last_frame = MAX_FRAMES - 1;
    let last_vpos = frame_vpos(last_frame as i64, FPS_NUM, FPS_DEN).unwrap();

    let long_start_frame = MAX_FRAMES - 1_000;
    let long_start_vpos = frame_vpos(long_start_frame as i64, FPS_NUM, FPS_DEN).unwrap();
    let mut long_fractional = draw(
        1,
        0,
        0,
        [8.125, 4.25, 12.0, 7.0],
        long_start_vpos,
        last_vpos + 1,
        0.1,
    );
    long_fractional.anchor_vpos = long_start_vpos;

    let fractional_end_frame = 10_000;
    let fractional_end_vpos = frame_vpos(fractional_end_frame as i64, FPS_NUM, FPS_DEN).unwrap();
    let fractional = draw(
        1,
        0,
        1,
        [7.375, 3.125, 11.0, 6.0],
        0,
        fractional_end_vpos,
        1.0 / 3.0,
    );

    let boundary_start_frame = 450_000;
    let boundary_end_frame = boundary_start_frame + 3;
    let boundary_start_vpos = frame_vpos(boundary_start_frame as i64, FPS_NUM, FPS_DEN).unwrap();
    let boundary_end_vpos = frame_vpos(boundary_end_frame as i64, FPS_NUM, FPS_DEN).unwrap();
    let mut boundary = draw(
        1,
        0,
        2,
        [10.25, 6.5, 8.0, 5.0],
        boundary_start_vpos,
        boundary_end_vpos,
        0.25,
    );
    boundary.anchor_vpos = boundary_start_vpos;

    let scene = Scene {
        header: Header {
            width: 33,
            height: 19,
            frame_count: MAX_FRAMES,
            fps_num: FPS_NUM,
            fps_den: FPS_DEN,
            bundle_sha256: [0; 32],
        },
        assets: vec![asset(1, [255, 255, 255, 255])],
        draws: vec![long_fractional, fractional, boundary],
    };
    let renderer = Renderer::new(
        &scene,
        &GpuOptions {
            backends: wgpu::Backends::all(),
            allow_software: false,
        },
    )
    .unwrap_or_else(|error| {
        panic!(
            "diagnostic adapter {} ({:?}) was available but production renderer setup failed: {error}",
            diagnostic.adapter_info.name, diagnostic.adapter_info.backend
        )
    });
    let adapter_info = renderer.adapter_info();
    eprintln!(
        "GPU POSITION READBACK DEVICE: adapter={} backend={:?}; production renderer adapter={} backend={:?} device_type={:?}",
        diagnostic.adapter_info.name,
        diagnostic.adapter_info.backend,
        adapter_info.name,
        adapter_info.backend,
        adapter_info.device_type
    );

    let (pipeline, bind_group_layout) = debug_vertex_pipeline(&diagnostic.device);
    let mut frames: Vec<usize> = (0..360).collect();
    frames.extend(
        [
            0,
            1,
            fractional_end_frame - 1,
            fractional_end_frame,
            boundary_start_frame - 1,
            boundary_start_frame,
            boundary_end_frame - 1,
            boundary_end_frame,
            long_start_frame - 1,
            long_start_frame,
            long_start_frame + 500,
            last_frame,
        ]
        .into_iter()
        .map(|frame| frame as usize),
    );
    frames.sort_unstable();
    frames.dedup();

    let frame_vpos: Vec<i32> = frames
        .iter()
        .map(|frame| frame_vpos(*frame as i64, FPS_NUM, FPS_DEN).unwrap())
        .collect();
    let positions = read_gpu_vertex_positions(
        &diagnostic.device,
        &diagnostic.queue,
        &pipeline,
        &bind_group_layout,
        &scene.draws,
        &frame_vpos,
    )
    .unwrap_or_else(|error| panic!("GPU position batch readback: {error}"));
    let positions_per_frame = scene.draws.len() * 6;
    assert_eq!(positions.len(), frames.len() * positions_per_frame);

    let mut max_pixel_error = 0.0_f64;
    let mut max_projected_error = 0.0_f64;
    let mut compared_vertices = 0usize;
    for (frame_index, frame) in frames.iter().copied().enumerate() {
        let frame_positions =
            &positions[frame_index * positions_per_frame..(frame_index + 1) * positions_per_frame];

        for (draw_index, draw) in scene.draws.iter().enumerate() {
            let vpos = frame_vpos[frame_index];
            let active = draw.start_vpos <= vpos && vpos < draw.end_vpos;
            let delta = (vpos - draw.anchor_vpos) as f64;
            for vertex_index in 0..6 {
                let observed = frame_positions[draw_index * 6 + vertex_index];
                assert_eq!(
                    observed.pixel[3] == 1.0,
                    active,
                    "GPU half-open visibility draw={draw_index} frame={frame} vpos={vpos} vertex={vertex_index} record={observed:?}"
                );
                if !active {
                    continue;
                }

                let corner_x = if matches!(vertex_index, 1 | 2 | 4) {
                    1.0
                } else {
                    0.0
                };
                let corner_y = if matches!(vertex_index, 2 | 4 | 5) {
                    1.0
                } else {
                    0.0
                };
                let expected_x =
                    draw.rect[0] as f64 + delta * draw.speed_x + corner_x * draw.rect[2] as f64;
                let expected_y = draw.rect[1] as f64 + corner_y * draw.rect[3] as f64;
                let pixel_error_x = (observed.pixel[0] as f64 - expected_x).abs();
                let pixel_error_y = (observed.pixel[1] as f64 - expected_y).abs();
                max_pixel_error = max_pixel_error.max(pixel_error_x).max(pixel_error_y);
                assert!(
                    pixel_error_x <= 1.0 / 256.0 && pixel_error_y <= 1.0 / 256.0,
                    "GPU pixel position exceeded tolerance draw={draw_index} frame={frame} vertex={vertex_index} expected=({expected_x:.9},{expected_y:.9}) observed=({:.9},{:.9}) error=({pixel_error_x:.9},{pixel_error_y:.9})",
                    observed.pixel[0], observed.pixel[1]
                );

                let expected_projected =
                    project_reference_pixel(draw, expected_x, expected_y, 33, 19);
                let observed_projected = projected_pixel_from_clip(observed.clip, 33, 19);
                let projected_error_x = (observed_projected.0 - expected_projected.0).abs();
                let projected_error_y = (observed_projected.1 - expected_projected.1).abs();
                max_projected_error = max_projected_error
                    .max(projected_error_x)
                    .max(projected_error_y);
                assert!(
                    projected_error_x <= 1.0 / 256.0 && projected_error_y <= 1.0 / 256.0,
                    "GPU projected position exceeded tolerance draw={draw_index} frame={frame} vertex={vertex_index} expected={expected_projected:?} observed={observed_projected:?} error=({projected_error_x:.9},{projected_error_y:.9})"
                );
                compared_vertices += 1;
            }
        }
    }
    assert!(
        compared_vertices > 0,
        "no active GPU vertices were compared"
    );
    eprintln!(
        "GPU POSITION READBACK PASSED: frames={} vertices={} max_pixel_error={max_pixel_error:.9}px max_projected_error={max_projected_error:.9}px tolerance={:.9}px",
        frames.len(),
        compared_vertices,
        1.0 / 256.0
    );
}

#[test]
#[ignore = "opt-in hardware qualification; set NICO_TIMELINE_GPU_POSITION_TEST=1 and pass --ignored"]
fn gpu_q40_motion_matches_browser_float32_samples_and_rounds_ties_to_even() {
    if std::env::var("NICO_TIMELINE_GPU_POSITION_TEST").as_deref() != Ok("1") {
        eprintln!(
            "SKIP: set NICO_TIMELINE_GPU_POSITION_TEST=1 to execute the GPU position readback"
        );
        return;
    }
    let diagnostic = hardware_diagnostic_device()
        .unwrap_or_else(|error| panic!("requesting diagnostic WGPU device: {error}"));
    let Some(diagnostic) = diagnostic else {
        return;
    };
    eprintln!(
        "Q40 motion adapter={} backend={:?} device_type={:?}",
        diagnostic.adapter_info.name,
        diagnostic.adapter_info.backend,
        diagnostic.adapter_info.device_type
    );
    let sample = Draw {
        asset_id: 1,
        start_vpos: 0,
        end_vpos: 500,
        anchor_vpos: 317,
        owner_order: 0,
        comment_index: 22,
        primitive_index: 0,
        rect: [736.2553100585938, 398.0, 216.0, 103.0],
        projection: ortho_projection(),
        alpha: 1.0,
        anchor_x: 740.2553147877013,
        speed_x: -4.319055636896047,
    };
    let tie_draw = |comment_index, anchor_x| Draw {
        asset_id: 1,
        start_vpos: 0,
        end_vpos: 500,
        anchor_vpos: 0,
        owner_order: 0,
        comment_index,
        primitive_index: 0,
        rect: [anchor_x as f32, 0.0, 1.0, 1.0],
        projection: ortho_projection(),
        alpha: 1.0,
        anchor_x,
        speed_x: 0.0,
    };
    let positive_midpoint = tie_draw(23, 1.0 + 2.0_f64.powi(-24));
    let negative_midpoint = tie_draw(24, -(1.0 + 2.0_f64.powi(-24)));
    let positive_odd_midpoint = tie_draw(25, 1.0 + 3.0 * 2.0_f64.powi(-24));
    let negative_odd_midpoint = tie_draw(26, -(1.0 + 3.0 * 2.0_f64.powi(-24)));
    let significand_carry = tie_draw(27, 2.0 - 2.0_f64.powi(-24));
    let draws = [
        sample,
        positive_midpoint,
        negative_midpoint,
        positive_odd_midpoint,
        negative_odd_midpoint,
        significand_carry,
    ];
    let (pipeline, bind_group_layout) = debug_vertex_pipeline(&diagnostic.device);
    let positions = read_gpu_vertex_positions(
        &diagnostic.device,
        &diagnostic.queue,
        &pipeline,
        &bind_group_layout,
        &draws,
        &[246, 363, 0],
    )
    .unwrap_or_else(|error| panic!("GPU position sample readback: {error}"));
    assert_eq!(positions.len(), 108);
    assert_eq!(positions[0].pixel[0].to_bits(), 0x4482_5d11);
    assert_eq!(positions[36].pixel[0].to_bits(), 0x4406_650a);
    assert_eq!(positions[72 + 6].pixel[0].to_bits(), 0x3f80_0000);
    assert_eq!(positions[72 + 12].pixel[0].to_bits(), 0xbf80_0000);
    assert_eq!(positions[72 + 18].pixel[0].to_bits(), 0x3f80_0002);
    assert_eq!(positions[72 + 24].pixel[0].to_bits(), 0xbf80_0002);
    assert_eq!(positions[72 + 30].pixel[0].to_bits(), 0x4000_0000);
}
