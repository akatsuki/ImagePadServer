use super::adapter;
use super::music_v2_glyph_atlas::build_atlas_for_text;
use super::music_v2_shader_draft::DRAFT_SHADER_MODULE;
use super::music_v2_shader_host::{
    create_artwork_texture, create_bind_group, create_bind_group_layout, create_compute_pipeline,
    create_glyph_atlas_texture, create_glyph_metrics_buffer, create_output_textures,
    create_pcm_storage_buffer, create_scene_uniform_buffer, create_shader_module,
    create_waveform_storage_buffer, encode_compute_pass, prepare_frame_plan,
    shader_output_dimensions, RgbaToBgraPass, ShaderFrameRequest, ShaderGpuResources,
    ShaderSceneData,
};
use super::music_v2_shader_module::ShaderModule;
use serde::Deserialize;
use std::fs::File;
use std::io::{BufWriter, Read, Write};
use std::num::NonZeroU32;
use std::path::{Path, PathBuf};
use std::sync::mpsc::channel;
use std::time::{Duration, Instant};

const COPY_BYTES_PER_ROW_ALIGNMENT: u32 = 256;
const DEFAULT_WIDTH: u32 = 640;
const DEFAULT_HEIGHT: u32 = 360;
const DEFAULT_FPS: u32 = 30;
const DEFAULT_FRAMES: u32 = 120;
const DEFAULT_PCM_SAMPLES: u32 = 4096;
const DEFAULT_AUDIO_SAMPLE_RATE: u32 = 44_100;
const ARTWORK_SIZE: u32 = 512;
const WAVEFORM_COLUMNS: usize = 752;

#[derive(Clone, Debug, PartialEq, Eq)]
struct ExportConfig {
    width: u32,
    height: u32,
    fps: u32,
    frames: u32,
    pcm_samples: u32,
    sample_rate: u32,
    pcm_input: Option<PathBuf>,
    scene_input: Option<PathBuf>,
    title: String,
    artist: String,
    album: String,
    artwork_input: Option<PathBuf>,
    output_yuv: PathBuf,
    output_h264: Option<PathBuf>,
}

#[derive(Clone, Debug, Deserialize)]
struct SharedSceneDocument {
    schema: u16,
    width: u32,
    height: u32,
    fps: u32,
    frames: u32,
    pcm_window_samples: u32,
    sample_rate_hz: u32,
    layout_rects: [[i32; 4]; 8],
    palette: [[u8; 4]; 4],
    frames_payload: Vec<SharedSceneFrame>,
}

#[derive(Clone, Debug, Deserialize)]
struct SharedSceneFrame {
    frame_index: u32,
    #[allow(dead_code)]
    pts_ns: i64,
    spectrum_q16: [u16; 24],
    #[serde(default)]
    waveform_q16: Vec<u16>,
    loudness_q16: Vec<u16>,
    progress_q16: u32,
    fade_in_q16: u32,
    fade_out_q16: u32,
    #[allow(dead_code)]
    rms_q15: u16,
    #[allow(dead_code)]
    peak_q15: u16,
    text_lines: [String; 4],
}

pub fn run(args: &[String]) -> Result<(), String> {
    if args.iter().any(|arg| arg == "--help" || arg == "-h") {
        print_usage();
        return Ok(());
    }
    let config = parse_args(args)?;
    if config.output_h264.is_some() {
        export_nvenc(&config)
    } else {
        export_yuv(&config)
    }
}

fn print_usage() {
    println!(
        "Usage: playlist-compositord --export-draft-shader-yuv \
         [--width N] [--height N] [--fps N] [--frames N] \
         [--pcm-samples N] [--sample-rate N] [--pcm-input PATH] \
         [--scene-input PATH] \
         [--title TEXT] [--artist TEXT] [--album TEXT] [--artwork-input PATH] \
         --output-yuv PATH|- | --output-h264 PATH"
    );
    println!("Diagnostic only: executes Draft WGSL and writes raw YUV420P frames.");
    println!("--output-yuv - writes binary YUV420P to stdout; status is written to stderr.");
    println!("--output-h264 selects the diagnostic D3D12/NVENC path; only compressed bitstream bytes leave the GPU.");
    println!(
        "--pcm-input expects mono little-endian f32 PCM; omitted means deterministic fixture PCM."
    );
}

fn parse_args(args: &[String]) -> Result<ExportConfig, String> {
    let mut config = ExportConfig {
        width: DEFAULT_WIDTH,
        height: DEFAULT_HEIGHT,
        fps: DEFAULT_FPS,
        frames: DEFAULT_FRAMES,
        pcm_samples: DEFAULT_PCM_SAMPLES,
        sample_rate: DEFAULT_AUDIO_SAMPLE_RATE,
        pcm_input: None,
        scene_input: None,
        title: "IMAGEPAD".into(),
        artist: "GPU PARITY".into(),
        album: "CANONICAL SCENE".into(),
        artwork_input: None,
        output_yuv: PathBuf::new(),
        output_h264: None,
    };
    let mut index = 0;
    while index < args.len() {
        let flag = args[index].as_str();
        let value = |index: &mut usize| -> Result<&str, String> {
            *index += 1;
            args.get(*index)
                .map(String::as_str)
                .ok_or_else(|| format!("missing value for {flag}"))
        };
        match flag {
            "--width" => config.width = parse_u32(value(&mut index)?, flag)?,
            "--height" => config.height = parse_u32(value(&mut index)?, flag)?,
            "--fps" => config.fps = parse_u32(value(&mut index)?, flag)?,
            "--frames" => config.frames = parse_u32(value(&mut index)?, flag)?,
            "--pcm-samples" => config.pcm_samples = parse_u32(value(&mut index)?, flag)?,
            "--sample-rate" => config.sample_rate = parse_u32(value(&mut index)?, flag)?,
            "--pcm-input" => config.pcm_input = Some(PathBuf::from(value(&mut index)?)),
            "--scene-input" => config.scene_input = Some(PathBuf::from(value(&mut index)?)),
            "--title" => config.title = value(&mut index)?.to_string(),
            "--artist" => config.artist = value(&mut index)?.to_string(),
            "--album" => config.album = value(&mut index)?.to_string(),
            "--artwork-input" => config.artwork_input = Some(PathBuf::from(value(&mut index)?)),
            "--output-yuv" => config.output_yuv = PathBuf::from(value(&mut index)?),
            "--output-h264" => config.output_h264 = Some(PathBuf::from(value(&mut index)?)),
            other => return Err(format!("unknown export option: {other}")),
        }
        index += 1;
    }

    if config.width == 0 || config.height == 0 || config.fps == 0 || config.frames == 0 {
        return Err("width, height, fps, and frames must be non-zero".into());
    }
    if config.pcm_samples == 0 || config.sample_rate == 0 {
        return Err("pcm-samples and sample-rate must be non-zero".into());
    }
    if config.output_yuv.as_os_str().is_empty() != config.output_h264.is_some() {
        return Err("exactly one of --output-yuv or --output-h264 is required".into());
    }
    Ok(config)
}

fn parse_u32(value: &str, flag: &str) -> Result<u32, String> {
    value
        .parse::<u32>()
        .map_err(|error| format!("invalid value for {flag}: {error}"))
}

fn output_yuv_is_stdout(path: &Path) -> bool {
    path == Path::new("-")
}

fn export_nvenc(config: &ExportConfig) -> Result<(), String> {
    #[cfg(windows)]
    {
        return export_nvenc_windows(config);
    }
    #[cfg(not(windows))]
    {
        let _ = config;
        Err("the diagnostic D3D12/NVENC exporter is only available on Windows".into())
    }
}

#[cfg(windows)]
fn export_nvenc_windows(config: &ExportConfig) -> Result<(), String> {
    use super::native_nvenc::{D3d12NvencEncoder, D3d12Surface};

    let export_started = Instant::now();
    let output_path = config
        .output_h264
        .as_ref()
        .ok_or_else(|| "--output-h264 is required for the NVENC exporter".to_string())?;
    let ((width, height), _) = shader_output_dimensions(config.width, config.height)?;
    let scene_load_started = Instant::now();
    let shared_scene = load_scene_document(config)?;
    let scene_load_seconds = scene_load_started.elapsed().as_secs_f64();
    let output_parent = output_path
        .parent()
        .filter(|parent| !parent.as_os_str().is_empty());
    if let Some(parent) = output_parent {
        std::fs::create_dir_all(parent)
            .map_err(|error| format!("create H.264 output directory: {error}"))?;
    }

    let device_setup_started = Instant::now();
    let instance = wgpu::Instance::default();
    let selected = adapter::select_with_backends(&instance, wgpu::Backends::DX12)?;
    let adapter_name = selected.info.name.clone();
    let backend = format!("{:?}", selected.info.backend).to_ascii_lowercase();
    let (device, queue) = pollster::block_on(
        selected
            .adapter
            .request_device(&wgpu::DeviceDescriptor::default(), None),
    )
    .map_err(|error| format!("D3D12 NVENC device: {error}"))?;
    let device_setup_seconds = device_setup_started.elapsed().as_secs_f64();

    let shader_setup_started = Instant::now();
    let descriptor = DRAFT_SHADER_MODULE.descriptor();
    let shader = create_shader_module(&device, &DRAFT_SHADER_MODULE)?;
    let layout = create_bind_group_layout(&device, descriptor)?;
    let pipeline = create_compute_pipeline(&device, &layout, &shader, descriptor)?;
    let shader_setup_seconds = shader_setup_started.elapsed().as_secs_f64();
    let resource_setup_started = Instant::now();
    let samples = load_pcm_samples(config)?;
    let first_window = pcm_window(config, &samples, 0);
    let first_waveform_q16 = shared_scene
        .as_ref()
        .map(|document| document.frames_payload[0].waveform_q16.clone())
        .unwrap_or_else(|| synthetic_waveform_minmax_q16(&first_window, WAVEFORM_COLUMNS));
    let track_loudness_q16 = track_loudness_q16(&samples, 1000);
    let pcm_buffer = create_pcm_storage_buffer(&device, &first_window)?;
    let waveform_buffer = create_waveform_storage_buffer(&device, &first_waveform_q16);
    let first_request = shared_scene
        .as_ref()
        .map(|document| shared_scene_frame_request(config, document, 0))
        .unwrap_or_else(|| {
            frame_request(
                config,
                0,
                &samples,
                &first_window,
                &first_waveform_q16,
                &track_loudness_q16,
            )
        });
    let first_plan = prepare_frame_plan(&DRAFT_SHADER_MODULE, first_request)?;
    let scene_uniform = create_scene_uniform_buffer(&device, &first_plan.uniform);
    let glyph_atlas = build_atlas_for_text([
        config.title.as_str(),
        config.artist.as_str(),
        config.album.as_str(),
        "0:00",
    ])?;
    let glyph_atlas_texture = create_glyph_atlas_texture(&device, &queue, &glyph_atlas);
    let glyph_atlas_view = glyph_atlas_texture.create_view(&wgpu::TextureViewDescriptor::default());
    let glyph_metrics = create_glyph_metrics_buffer(&device, &glyph_atlas);
    let glyph_sampler = device.create_sampler(&wgpu::SamplerDescriptor {
        label: Some("music-v2-nvenc-glyph-sampler"),
        address_mode_u: wgpu::AddressMode::ClampToEdge,
        address_mode_v: wgpu::AddressMode::ClampToEdge,
        address_mode_w: wgpu::AddressMode::ClampToEdge,
        mag_filter: wgpu::FilterMode::Linear,
        min_filter: wgpu::FilterMode::Linear,
        mipmap_filter: wgpu::FilterMode::Nearest,
        ..Default::default()
    });
    let (artwork_pixels, artwork_width, artwork_height) = load_artwork_rgba(config)?;
    let artwork_texture = create_artwork_texture(
        &device,
        &queue,
        &artwork_pixels,
        artwork_width,
        artwork_height,
    )?;
    let artwork_view = artwork_texture.create_view(&wgpu::TextureViewDescriptor::default());
    let output_textures = create_output_textures(&device, width, height)?;
    let luma_view = output_textures.luma_view();
    let chroma_view = output_textures.chroma_view();
    let rgb_view = output_textures.rgb_view();
    let bind_group = create_bind_group(
        &device,
        &layout,
        descriptor,
        ShaderGpuResources {
            pcm_buffer: &pcm_buffer,
            scene_uniform: &scene_uniform,
            waveform_buffer: &waveform_buffer,
            glyph_atlas: &glyph_atlas_view,
            glyph_metrics: &glyph_metrics,
            glyph_sampler: &glyph_sampler,
            artwork_texture: &artwork_view,
            luma_output: &luma_view,
            chroma_output: &chroma_view,
            rgb_output: &rgb_view,
        },
    )?;
    let resource_setup_seconds = resource_setup_started.elapsed().as_secs_f64();

    let native_setup_started = Instant::now();
    let surface = D3d12Surface::create(&device, width, height)?;
    let surface_view = surface
        .texture
        .create_view(&wgpu::TextureViewDescriptor::default());
    let conversion = RgbaToBgraPass::new(&device, &rgb_view);
    let mut nvenc = D3d12NvencEncoder::create(&device, width, height, config.fps)?;
    let contract = surface.contract(config.frames as u64);
    contract.validate()?;
    let encoder_setup_started = Instant::now();
    nvenc.register_surface(&surface)?;
    let encoder_setup_seconds = encoder_setup_started.elapsed().as_secs_f64();
    let native_setup_seconds = native_setup_started.elapsed().as_secs_f64();

    let file = File::create(output_path)
        .map_err(|error| format!("create H.264 output {}: {error}", output_path.display()))?;
    let mut output = BufWriter::new(file);
    let total_started = Instant::now();
    let mut prepare_seconds = Duration::ZERO;
    let mut queue_update_seconds = Duration::ZERO;
    let mut gpu_submit_seconds = Duration::ZERO;
    let mut gpu_wait_seconds = Duration::ZERO;
    let mut nvenc_encode_seconds = Duration::ZERO;
    let mut bitstream_write_seconds = Duration::ZERO;
    for frame in 0..config.frames {
        let window = pcm_window(config, &samples, frame);
        let waveform_q16 = shared_scene
            .as_ref()
            .map(|document| document.frames_payload[frame as usize].waveform_q16.clone())
            .unwrap_or_else(|| synthetic_waveform_minmax_q16(&window, WAVEFORM_COLUMNS));
        let request = shared_scene
            .as_ref()
            .map(|document| shared_scene_frame_request(config, document, frame))
            .unwrap_or_else(|| {
                frame_request(
                    config,
                    frame,
                    &samples,
                    &window,
                    &waveform_q16,
                    &track_loudness_q16,
                )
            });
        let prepare_started = Instant::now();
        let plan = prepare_frame_plan(&DRAFT_SHADER_MODULE, request)?;
        prepare_seconds += prepare_started.elapsed();

        let queue_update_started = Instant::now();
        queue.write_buffer(&pcm_buffer, 0, bytemuck::cast_slice(&window));
        let waveform_words: Vec<u32> = waveform_q16.iter().copied().map(u32::from).collect();
        queue.write_buffer(&waveform_buffer, 0, bytemuck::cast_slice(&waveform_words));
        queue.write_buffer(&scene_uniform, 0, &plan.uniform.to_bytes());
        queue_update_seconds += queue_update_started.elapsed();

        let gpu_submit_started = Instant::now();
        let mut command_encoder = device.create_command_encoder(&wgpu::CommandEncoderDescriptor {
            label: Some("music-v2-nvenc-frame"),
        });
        encode_compute_pass(&mut command_encoder, &pipeline, &bind_group, &plan);
        conversion.encode(&mut command_encoder, &surface_view);
        queue.submit(Some(command_encoder.finish()));
        nvenc.signal_input_fence(&device, u64::from(frame).saturating_add(1))?;
        gpu_submit_seconds += gpu_submit_started.elapsed();

        let nvenc_started = Instant::now();
        let bitstream = nvenc.encode_surface(&surface, frame)?;
        nvenc_encode_seconds += nvenc_started.elapsed();
        if bitstream.is_empty() {
            return Err(format!(
                "NVENC returned an empty bitstream for frame {frame}"
            ));
        }
        let bitstream_write_started = Instant::now();
        output
            .write_all(&bitstream)
            .map_err(|error| format!("write H.264 bitstream: {error}"))?;
        bitstream_write_seconds += bitstream_write_started.elapsed();
        if frame == 0 || frame + 1 == config.frames || (frame + 1) % config.fps == 0 {
            eprintln!(
                "direct NVENC scene export: frame {}/{} bytes={}",
                frame + 1,
                config.frames,
                bitstream.len()
            );
        }
    }
    output
        .flush()
        .map_err(|error| format!("flush H.264 bitstream: {error}"))?;
    let total_seconds = total_started.elapsed().as_secs_f64();
    eprintln!(
        "direct_nvenc_scene_diagnostic=pass frames={} size={}x{} fps={} adapter={} backend={} pixel_readback_bytes=0 output={}",
        config.frames,
        width,
        height,
        config.fps,
        adapter_name,
        backend,
        output_path.display()
    );
    eprintln!(
        "direct_nvenc_stage_timing=scene_load:{:.6}s device_setup:{:.6}s shader_setup:{:.6}s resource_setup:{:.6}s native_setup:{:.6}s encoder_register:{:.6}s prepare:{:.6}s queue_update:{:.6}s gpu_submit:{:.6}s gpu_wait:{:.6}s nvenc_encode:{:.6}s bitstream_write:{:.6}s loop_total:{:.6}s export_total:{:.6}s",
        scene_load_seconds,
        device_setup_seconds,
        shader_setup_seconds,
        resource_setup_seconds,
        native_setup_seconds,
        encoder_setup_seconds,
        prepare_seconds.as_secs_f64(),
        queue_update_seconds.as_secs_f64(),
        gpu_submit_seconds.as_secs_f64(),
        gpu_wait_seconds.as_secs_f64(),
        nvenc_encode_seconds.as_secs_f64(),
        bitstream_write_seconds.as_secs_f64(),
        total_seconds,
        export_started.elapsed().as_secs_f64(),
    );
    Ok(())
}

fn load_scene_document(config: &ExportConfig) -> Result<Option<SharedSceneDocument>, String> {
    let Some(path) = &config.scene_input else {
        return Ok(None);
    };
    let json = std::fs::read_to_string(path)
        .map_err(|error| format!("read scene input {}: {error}", path.display()))?;
    let document: SharedSceneDocument = serde_json::from_str(&json)
        .map_err(|error| format!("parse scene input {}: {error}", path.display()))?;
    if document.schema != 1
        || document.width != config.width
        || document.height != config.height
        || document.fps != config.fps
        || document.frames != config.frames
        || document.pcm_window_samples != config.pcm_samples
        || document.sample_rate_hz != config.sample_rate
        || document.frames_payload.len() != config.frames as usize
    {
        return Err("scene input header does not match export conditions".into());
    }
    for (index, frame) in document.frames_payload.iter().enumerate() {
        if frame.frame_index != index as u32
            || frame.loudness_q16.len() != 1000
            || frame.waveform_q16.len() > super::music_v2_shader_host::MAX_SHADER_WAVEFORM_SAMPLES
            || frame.waveform_q16.len() % 2 != 0
        {
            return Err(format!("invalid shared scene frame {index}"));
        }
    }
    Ok(Some(document))
}

fn export_yuv(config: &ExportConfig) -> Result<(), String> {
    let ((width, height), (chroma_width, chroma_height)) =
        shader_output_dimensions(config.width, config.height)?;
    let shared_scene = load_scene_document(config)?;
    let output_parent = if output_yuv_is_stdout(&config.output_yuv) {
        None
    } else {
        config
            .output_yuv
            .parent()
            .filter(|parent| !parent.as_os_str().is_empty())
    };
    if let Some(parent) = output_parent {
        std::fs::create_dir_all(parent)
            .map_err(|error| format!("create YUV output directory: {error}"))?;
    }

    let instance = wgpu::Instance::default();
    let selected = adapter::select(&instance)?;
    let adapter_name = selected.info.name.clone();
    let backend = format!("{:?}", selected.info.backend).to_ascii_lowercase();
    let (device, queue) = pollster::block_on(
        selected
            .adapter
            .request_device(&wgpu::DeviceDescriptor::default(), None),
    )
    .map_err(|error| format!("gpu device: {error}"))?;

    let descriptor = DRAFT_SHADER_MODULE.descriptor();
    let shader = create_shader_module(&device, &DRAFT_SHADER_MODULE)?;
    let layout = create_bind_group_layout(&device, descriptor)?;
    let pipeline = create_compute_pipeline(&device, &layout, &shader, descriptor)?;
    let samples = load_pcm_samples(config)?;
    let first_window = pcm_window(config, &samples, 0);
    let first_waveform_q16 = shared_scene
        .as_ref()
        .map(|document| document.frames_payload[0].waveform_q16.clone())
        .unwrap_or_else(|| synthetic_waveform_minmax_q16(&first_window, WAVEFORM_COLUMNS));
    let track_loudness_q16 = track_loudness_q16(&samples, 1000);
    let pcm_buffer = create_pcm_storage_buffer(&device, &first_window)?;
    let waveform_buffer = create_waveform_storage_buffer(&device, &first_waveform_q16);
    let first_request = shared_scene
        .as_ref()
        .map(|document| shared_scene_frame_request(config, document, 0))
        .unwrap_or_else(|| {
            frame_request(
                config,
                0,
                &samples,
                &first_window,
                &first_waveform_q16,
                &track_loudness_q16,
            )
        });
    let first_plan = prepare_frame_plan(&DRAFT_SHADER_MODULE, first_request)?;
    let scene_uniform = create_scene_uniform_buffer(&device, &first_plan.uniform);
    let glyph_atlas = build_atlas_for_text([
        config.title.as_str(),
        config.artist.as_str(),
        config.album.as_str(),
        "0:00",
    ])?;
    let glyph_atlas_texture = create_glyph_atlas_texture(&device, &queue, &glyph_atlas);
    let glyph_atlas_view = glyph_atlas_texture.create_view(&wgpu::TextureViewDescriptor::default());
    let glyph_metrics = create_glyph_metrics_buffer(&device, &glyph_atlas);
    let glyph_sampler = device.create_sampler(&wgpu::SamplerDescriptor {
        label: Some("music-v2-glyph-sampler"),
        address_mode_u: wgpu::AddressMode::ClampToEdge,
        address_mode_v: wgpu::AddressMode::ClampToEdge,
        address_mode_w: wgpu::AddressMode::ClampToEdge,
        mag_filter: wgpu::FilterMode::Linear,
        min_filter: wgpu::FilterMode::Linear,
        mipmap_filter: wgpu::FilterMode::Nearest,
        ..Default::default()
    });
    let (artwork_pixels, artwork_width, artwork_height) = load_artwork_rgba(config)?;
    let artwork_texture = create_artwork_texture(
        &device,
        &queue,
        &artwork_pixels,
        artwork_width,
        artwork_height,
    )?;
    let artwork_view = artwork_texture.create_view(&wgpu::TextureViewDescriptor::default());

    let usage = wgpu::TextureUsages::STORAGE_BINDING | wgpu::TextureUsages::COPY_SRC;
    let luma_texture = device.create_texture(&wgpu::TextureDescriptor {
        label: Some("draft-export-luma"),
        size: wgpu::Extent3d {
            width,
            height,
            depth_or_array_layers: 1,
        },
        mip_level_count: 1,
        sample_count: 1,
        dimension: wgpu::TextureDimension::D2,
        format: wgpu::TextureFormat::Rgba8Unorm,
        usage,
        view_formats: &[],
    });
    let chroma_texture = device.create_texture(&wgpu::TextureDescriptor {
        label: Some("draft-export-chroma"),
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
    let rgb_texture = device.create_texture(&wgpu::TextureDescriptor {
        label: Some("draft-export-rgb"),
        size: wgpu::Extent3d {
            width,
            height,
            depth_or_array_layers: 1,
        },
        mip_level_count: 1,
        sample_count: 1,
        dimension: wgpu::TextureDimension::D2,
        format: wgpu::TextureFormat::Rgba8Unorm,
        usage,
        view_formats: &[],
    });
    let luma_view = luma_texture.create_view(&wgpu::TextureViewDescriptor::default());
    let chroma_view = chroma_texture.create_view(&wgpu::TextureViewDescriptor::default());
    let rgb_view = rgb_texture.create_view(&wgpu::TextureViewDescriptor::default());
    let bind_group = create_bind_group(
        &device,
        &layout,
        descriptor,
        ShaderGpuResources {
            pcm_buffer: &pcm_buffer,
            scene_uniform: &scene_uniform,
            waveform_buffer: &waveform_buffer,
            glyph_atlas: &glyph_atlas_view,
            glyph_metrics: &glyph_metrics,
            glyph_sampler: &glyph_sampler,
            artwork_texture: &artwork_view,
            luma_output: &luma_view,
            chroma_output: &chroma_view,
            rgb_output: &rgb_view,
        },
    )?;

    let luma_stride = align_to(width * 4, COPY_BYTES_PER_ROW_ALIGNMENT);
    let chroma_stride = align_to(chroma_width * 4, COPY_BYTES_PER_ROW_ALIGNMENT);
    let luma_staging = create_readback_buffer(
        &device,
        "draft-export-luma-readback",
        luma_stride as u64 * height as u64,
    );
    let chroma_staging = create_readback_buffer(
        &device,
        "draft-export-chroma-readback",
        chroma_stride as u64 * chroma_height as u64,
    );

    let mut output: Box<dyn Write> = if output_yuv_is_stdout(&config.output_yuv) {
        Box::new(BufWriter::new(std::io::stdout()))
    } else {
        Box::new(BufWriter::new(
            File::create(&config.output_yuv)
                .map_err(|error| format!("create YUV output: {error}"))?,
        ))
    };
    for frame in 0..config.frames {
        let window = pcm_window(config, &samples, frame);
        let waveform_q16 = shared_scene
            .as_ref()
            .map(|document| document.frames_payload[frame as usize].waveform_q16.clone())
            .unwrap_or_else(|| synthetic_waveform_minmax_q16(&window, WAVEFORM_COLUMNS));
        let request = shared_scene
            .as_ref()
            .map(|document| shared_scene_frame_request(config, document, frame))
            .unwrap_or_else(|| {
                frame_request(
                    config,
                    frame,
                    &samples,
                    &window,
                    &waveform_q16,
                    &track_loudness_q16,
                )
            });
        let plan = prepare_frame_plan(&DRAFT_SHADER_MODULE, request)?;
        queue.write_buffer(&pcm_buffer, 0, bytemuck::cast_slice(&window));
        let waveform_words: Vec<u32> = waveform_q16.iter().copied().map(u32::from).collect();
        queue.write_buffer(&waveform_buffer, 0, bytemuck::cast_slice(&waveform_words));
        queue.write_buffer(&scene_uniform, 0, &plan.uniform.to_bytes());

        let mut encoder = device.create_command_encoder(&wgpu::CommandEncoderDescriptor {
            label: Some("draft-shader-export"),
        });
        encode_compute_pass(&mut encoder, &pipeline, &bind_group, &plan);
        encoder.copy_texture_to_buffer(
            wgpu::ImageCopyTexture {
                texture: &luma_texture,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            wgpu::ImageCopyBuffer {
                buffer: &luma_staging,
                layout: wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(NonZeroU32::new(luma_stride).unwrap().into()),
                    rows_per_image: Some(NonZeroU32::new(height).unwrap().into()),
                },
            },
            wgpu::Extent3d {
                width,
                height,
                depth_or_array_layers: 1,
            },
        );
        encoder.copy_texture_to_buffer(
            wgpu::ImageCopyTexture {
                texture: &chroma_texture,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            wgpu::ImageCopyBuffer {
                buffer: &chroma_staging,
                layout: wgpu::ImageDataLayout {
                    offset: 0,
                    bytes_per_row: Some(NonZeroU32::new(chroma_stride).unwrap().into()),
                    rows_per_image: Some(NonZeroU32::new(chroma_height).unwrap().into()),
                },
            },
            wgpu::Extent3d {
                width: chroma_width,
                height: chroma_height,
                depth_or_array_layers: 1,
            },
        );
        queue.submit(Some(encoder.finish()));

        let luma = readback(&device, &luma_staging)?;
        let chroma = readback(&device, &chroma_staging)?;
        write_yuv420p_frame(
            &mut output,
            &luma,
            luma_stride,
            width,
            height,
            &chroma,
            chroma_stride,
            chroma_width,
            chroma_height,
        )?;
        if frame == 0 || (frame + 1) == config.frames || (frame + 1) % config.fps == 0 {
            eprintln!("draft shader export: frame {}/{}", frame + 1, config.frames);
        }
    }
    output
        .flush()
        .map_err(|error| format!("flush YUV output: {error}"))?;
    let status = format!(
        "draft_shader_yuv_written={} frames={} size={}x{} fps={} adapter={} backend={}",
        config.output_yuv.display(),
        config.frames,
        config.width,
        config.height,
        config.fps,
        adapter_name,
        backend
    );
    if output_yuv_is_stdout(&config.output_yuv) {
        eprintln!("{status}");
    } else {
        println!("{status}");
    }
    Ok(())
}

fn frame_request(
    config: &ExportConfig,
    frame: u32,
    samples: &[f32],
    window: &[f32],
    waveform_q16: &[u16],
    track_loudness: &[u16],
) -> ShaderFrameRequest {
    let sample_start = window_start_sample(config, samples.len(), frame);
    let elapsed_seconds = if config.pcm_input.is_some() {
        sample_start as f32 / config.sample_rate as f32
    } else {
        frame as f32 / config.fps as f32
    };
    let progress = if config.pcm_input.is_some() {
        sample_start as f32 / samples.len().saturating_sub(1).max(1) as f32
    } else {
        frame as f32 / config.frames.saturating_sub(1).max(1) as f32
    };
    let mut scene = ShaderSceneData::cpu_parity_default(
        config.width,
        config.height,
        frame,
        config.frames,
        config.fps,
    )
    .with_waveform_q16(waveform_q16.to_vec(), true)
    .with_text_lines(
        &config.title,
        &config.artist,
        &config.album,
        &format_timestamp(elapsed_seconds),
    );
    scene.spectrum_q16 = frame_spectrum_q16(window, config.sample_rate);
    scene.loudness_q16 = track_loudness.to_vec();
    scene.progress_q16 = (progress.clamp(0.0, 1.0) * u16::MAX as f32).round() as u32;

    ShaderFrameRequest {
        width: config.width,
        height: config.height,
        frame_index: frame,
        pcm_sample_count: config.pcm_samples,
        pcm_sample_offset: 0,
        scene,
    }
}

fn shared_scene_frame_request(
    config: &ExportConfig,
    document: &SharedSceneDocument,
    frame: u32,
) -> ShaderFrameRequest {
    let payload = &document.frames_payload[frame as usize];
    let mut scene = ShaderSceneData::cpu_parity_default(
        config.width,
        config.height,
        frame,
        config.frames,
        config.fps,
    )
    .with_waveform_q16(payload.waveform_q16.clone(), true)
    .with_text_lines(
        &payload.text_lines[0],
        &payload.text_lines[1],
        &payload.text_lines[2],
        &payload.text_lines[3],
    );
    let sx = config.width as f32 / 1280.0;
    let sy = config.height as f32 / 720.0;
    scene.rects = document.layout_rects.map(|rect| {
        [
            (rect[0] as f32 * sx).round() as i32,
            (rect[1] as f32 * sy).round() as i32,
            (rect[2] as f32 * sx).round() as i32,
            (rect[3] as f32 * sy).round() as i32,
        ]
    });
    scene.palette = document.palette;
    scene.spectrum_q16 = payload.spectrum_q16;
    scene.loudness_q16 = payload.loudness_q16.clone();
    scene.progress_q16 = payload.progress_q16;
    scene.fade_in_q16 = payload.fade_in_q16;
    scene.fade_out_q16 = payload.fade_out_q16;
    ShaderFrameRequest {
        width: config.width,
        height: config.height,
        frame_index: frame,
        pcm_sample_count: config.pcm_samples,
        pcm_sample_offset: 0,
        scene,
    }
}

fn load_pcm_samples(config: &ExportConfig) -> Result<Vec<f32>, String> {
    let Some(path) = &config.pcm_input else {
        return Ok(synthetic_pcm(config.pcm_samples as usize));
    };
    let mut file = File::open(path).map_err(|error| format!("open PCM input: {error}"))?;
    let mut bytes = Vec::new();
    file.read_to_end(&mut bytes)
        .map_err(|error| format!("read PCM input: {error}"))?;
    if bytes.len() % std::mem::size_of::<f32>() != 0 {
        return Err("PCM input byte length is not aligned to f32 samples".into());
    }
    let samples = bytes
        .chunks_exact(4)
        .map(|chunk| f32::from_le_bytes([chunk[0], chunk[1], chunk[2], chunk[3]]).clamp(-1.0, 1.0))
        .collect::<Vec<_>>();
    if samples.is_empty() {
        return Err("PCM input contains no samples".into());
    }
    Ok(samples)
}

fn load_artwork_rgba(config: &ExportConfig) -> Result<(Vec<u8>, u32, u32), String> {
    let Some(path) = &config.artwork_input else {
        // Transparent 1x1 keeps the binding valid and selects the shader's
        // deterministic fallback artwork path.
        return Ok((vec![0, 0, 0, 0], 1, 1));
    };
    let pixels = std::fs::read(path).map_err(|error| format!("read artwork input: {error}"))?;
    let expected = (ARTWORK_SIZE * ARTWORK_SIZE * 4) as usize;
    if pixels.len() != expected {
        return Err(format!(
            "artwork input must be raw RGBA8 {}x{} ({} bytes), got {}",
            ARTWORK_SIZE,
            ARTWORK_SIZE,
            expected,
            pixels.len()
        ));
    }
    Ok((pixels, ARTWORK_SIZE, ARTWORK_SIZE))
}

fn window_start_sample(config: &ExportConfig, total_samples: usize, frame: u32) -> usize {
    if total_samples == 0 {
        return 0;
    }
    if config.pcm_input.is_some() {
        let sample = (frame as u64 * config.sample_rate as u64 / config.fps as u64) as usize;
        return sample.min(total_samples.saturating_sub(1));
    }
    (frame as usize * 37) % total_samples
}

fn pcm_window(config: &ExportConfig, samples: &[f32], frame: u32) -> Vec<f32> {
    let window_len = config.pcm_samples as usize;
    let start = window_start_sample(config, samples.len(), frame);
    let mut window = vec![0.0; window_len];
    if start < samples.len() {
        let available = (samples.len() - start).min(window_len);
        window[..available].copy_from_slice(&samples[start..start + available]);
    }
    window
}

fn format_timestamp(seconds: f32) -> String {
    let total = seconds.max(0.0).round() as u32;
    format!("{}:{:02}", total / 60, total % 60)
}

fn track_loudness_q16(samples: &[f32], count: usize) -> Vec<u16> {
    if samples.is_empty() || count == 0 {
        return vec![0; count];
    }
    (0..count)
        .map(|index| {
            let start = index * samples.len() / count;
            let end = ((index + 1) * samples.len() / count)
                .max(start + 1)
                .min(samples.len());
            let mean_square = samples[start..end]
                .iter()
                .map(|sample| sample * sample)
                .sum::<f32>()
                / (end - start) as f32;
            (mean_square.sqrt().clamp(0.0, 1.0) * u16::MAX as f32).round() as u16
        })
        .collect()
}

fn frame_spectrum_q16(samples: &[f32], sample_rate: u32) -> [u16; 24] {
    let mut spectrum = [0u16; 24];
    if samples.is_empty() || sample_rate == 0 {
        return spectrum;
    }
    let tap_count = samples.len().min(512);
    for (band, value) in spectrum.iter_mut().enumerate() {
        let ratio = band as f32 / 23.0;
        let frequency = 40.0_f32 * (10_000.0_f32 / 40.0_f32).powf(ratio);
        let mut real = 0.0;
        let mut imaginary = 0.0;
        for tap in 0..tap_count {
            let index = tap * samples.len() / tap_count;
            let phase = std::f32::consts::TAU * frequency * index as f32 / sample_rate as f32;
            let window =
                0.5 - 0.5 * (std::f32::consts::TAU * tap as f32 / tap_count.max(1) as f32).cos();
            real += samples[index] * window * phase.cos();
            imaginary += samples[index] * window * phase.sin();
        }
        let magnitude = (real * real + imaginary * imaginary).sqrt() / tap_count as f32;
        let normalized = (magnitude * 12.0).clamp(0.0, 1.0);
        *value = (normalized * u16::MAX as f32).round() as u16;
    }
    spectrum
}

fn synthetic_pcm(sample_count: usize) -> Vec<f32> {
    let tau = std::f32::consts::PI * 2.0;
    (0..sample_count)
        .map(|index| {
            let phase = index as f32 / sample_count as f32;
            (0.78 * (phase * tau * 5.0).sin() + 0.12 * (phase * tau * 13.0).sin()).clamp(-1.0, 1.0)
        })
        .collect()
}

fn synthetic_waveform_minmax_q16(samples: &[f32], columns: usize) -> Vec<u16> {
    if samples.is_empty() || columns == 0 {
        return Vec::new();
    }
    let column_count = columns.min(samples.len());
    let to_q16 =
        |sample: f32| -> u16 { (((sample.clamp(-1.0, 1.0) * 0.5 + 0.5) * 65535.0).round()) as u16 };
    let mut output = Vec::with_capacity(column_count * 2);
    for column in 0..column_count {
        let start = column * samples.len() / column_count;
        let end = ((column + 1) * samples.len() / column_count).max(start + 1);
        let mut low: f32 = 1.0;
        let mut high: f32 = -1.0;
        for sample in &samples[start..end.min(samples.len())] {
            low = low.min(*sample);
            high = high.max(*sample);
        }
        output.push(to_q16(low));
        output.push(to_q16(high));
    }
    output
}

fn align_to(value: u32, alignment: u32) -> u32 {
    (value + alignment - 1) / alignment * alignment
}

fn create_readback_buffer(device: &wgpu::Device, label: &str, size: u64) -> wgpu::Buffer {
    device.create_buffer(&wgpu::BufferDescriptor {
        label: Some(label),
        size,
        usage: wgpu::BufferUsages::MAP_READ | wgpu::BufferUsages::COPY_DST,
        mapped_at_creation: false,
    })
}

fn readback(device: &wgpu::Device, buffer: &wgpu::Buffer) -> Result<Vec<u8>, String> {
    let slice = buffer.slice(..);
    let (tx, rx) = channel();
    slice.map_async(wgpu::MapMode::Read, move |result| {
        let _ = tx.send(result);
    });
    device.poll(wgpu::Maintain::Wait);
    rx.recv()
        .map_err(|_| "readback channel closed".to_string())?
        .map_err(|error| format!("readback map: {error}"))?;
    let data = slice.get_mapped_range().to_vec();
    buffer.unmap();
    Ok(data)
}

#[allow(clippy::too_many_arguments)]
fn write_yuv420p_frame(
    output: &mut impl Write,
    luma: &[u8],
    luma_stride: u32,
    width: u32,
    height: u32,
    chroma: &[u8],
    chroma_stride: u32,
    chroma_width: u32,
    chroma_height: u32,
) -> Result<(), String> {
    let yuv_bytes =
        width as usize * height as usize + chroma_width as usize * chroma_height as usize * 2;
    let mut packed = Vec::with_capacity(yuv_bytes);
    for row in 0..height as usize {
        let start = row * luma_stride as usize;
        for column in 0..width as usize {
            packed.push(luma[start + column * 4]);
        }
    }
    for channel in [0usize, 1usize] {
        for row in 0..chroma_height as usize {
            let start = row * chroma_stride as usize;
            for column in 0..chroma_width as usize {
                packed.push(chroma[start + column * 4 + channel]);
            }
        }
    }
    output
        .write_all(&packed)
        .map_err(|error| format!("write YUV420P frame: {error}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_diagnostic_export_arguments() {
        let args = vec![
            "--width".into(),
            "1279".into(),
            "--height".into(),
            "719".into(),
            "--fps".into(),
            "24".into(),
            "--frames".into(),
            "12".into(),
            "--pcm-samples".into(),
            "2048".into(),
            "--scene-input".into(),
            "scene.json".into(),
            "--output-yuv".into(),
            "preview.yuv".into(),
        ];
        let config = parse_args(&args).expect("valid export args");
        assert_eq!(config.width, 1279);
        assert_eq!(config.height, 719);
        assert_eq!(config.fps, 24);
        assert_eq!(config.frames, 12);
        assert_eq!(config.pcm_samples, 2048);
        assert_eq!(config.scene_input, Some(PathBuf::from("scene.json")));
        assert_eq!(config.output_yuv, PathBuf::from("preview.yuv"));
    }

    #[test]
    fn rejects_missing_yuv_output() {
        assert!(parse_args(&[]).is_err());
    }

    #[test]
    fn aligns_copy_rows_to_wgpu_requirement() {
        assert_eq!(align_to(640, COPY_BYTES_PER_ROW_ALIGNMENT), 768);
        assert_eq!(align_to(256, COPY_BYTES_PER_ROW_ALIGNMENT), 256);
    }

    #[test]
    fn synthetic_pcm_is_bounded_and_nonempty() {
        let samples = synthetic_pcm(128);
        assert_eq!(samples.len(), 128);
        assert!(samples.iter().all(|sample| (-1.0..=1.0).contains(sample)));
        assert!(samples.iter().any(|sample| *sample > 0.5));
        assert!(samples.iter().any(|sample| *sample < -0.5));
    }

    #[test]
    fn synthetic_waveform_preserves_signed_minmax_per_column() {
        let samples = vec![-1.0, -0.25, 0.5, 1.0];
        let waveform = synthetic_waveform_minmax_q16(&samples, 2);
        assert_eq!(waveform.len(), 4);
        assert!(waveform[0] < u16::MAX / 2);
        assert!(waveform[1] < u16::MAX / 2);
        assert!(waveform[2] > u16::MAX / 2);
        assert_eq!(waveform[3], u16::MAX);
    }

    #[test]
    fn packs_rgba8_luma_and_chroma_channels_into_yuv420p() {
        let mut output = std::io::Cursor::new(Vec::new());
        let luma = vec![1, 0, 0, 255, 2, 0, 0, 255, 3, 0, 0, 255, 4, 0, 0, 255];
        let chroma = vec![10, 20, 0, 255];
        write_yuv420p_frame(&mut output, &luma, 8, 2, 2, &chroma, 4, 1, 1)
            .expect("packed diagnostic frame");
        assert_eq!(output.into_inner(), vec![1, 2, 3, 4, 10, 20]);
    }

    struct CountingWriter {
        write_calls: usize,
        bytes: Vec<u8>,
    }

    impl std::io::Write for CountingWriter {
        fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
            self.write_calls += 1;
            self.bytes.extend_from_slice(bytes);
            Ok(bytes.len())
        }

        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }

    #[test]
    fn packs_one_yuv420p_frame_with_one_batched_write() {
        let mut output = CountingWriter {
            write_calls: 0,
            bytes: Vec::new(),
        };
        let mut luma = vec![0; 256 * 2];
        luma[0] = 10;
        luma[4] = 11;
        luma[8] = 12;
        luma[12] = 13;
        luma[256] = 20;
        luma[260] = 21;
        luma[264] = 22;
        luma[268] = 23;
        let mut chroma = vec![0; 256];
        chroma[0] = 30;
        chroma[1] = 40;
        chroma[4] = 31;
        chroma[5] = 41;

        write_yuv420p_frame(&mut output, &luma, 256, 4, 2, &chroma, 256, 2, 1)
            .expect("packed batched diagnostic frame");

        assert_eq!(output.write_calls, 1);
        assert_eq!(
            output.bytes,
            vec![10, 11, 12, 13, 20, 21, 22, 23, 30, 31, 40, 41]
        );
    }

    #[test]
    fn recognizes_stdout_yuv_output_target() {
        let args = vec!["--output-yuv".into(), "-".into()];
        let config = parse_args(&args).expect("valid stdout output target");
        assert!(output_yuv_is_stdout(&config.output_yuv));
    }
}
