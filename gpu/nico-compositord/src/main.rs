use std::fs::{self, File};
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::process;
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};

use serde::Serialize;
use sha2::{Digest, Sha256};

use nico_compositord::protocol::{self, read_scene, Scene};
use nico_compositord::readback::{
    receive_incremental_header, stream_frames, stream_incremental_frames, WriterQueueMetrics,
};
use nico_compositord::renderer::{
    AssetLayoutMode, AssetLayoutReport, AssetTelemetryReport, GpuOptions, Renderer,
};
use nico_compositord::stream_budget::CpuBudget;
use nico_compositord::stream_protocol::{
    spawn_incremental_stream_parser_with_budget, Header as StreamHeader, IncrementalEvent,
};
use nico_compositord::stream_renderer::GpuBudget;

const SELF_TEST_SCENE: &[u8] =
    include_bytes!("../../../internal/nicorender/testdata/timeline/wire/red-33x19-3frames.nct");
// Includes one compact per-page occupancy record for each protocol-permitted asset.
const MAX_REPORT_BYTES: usize = 2 * 1024 * 1024;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum CliMode {
    SelfTest,
    Stdin,
    StdinStream,
    Capabilities,
}

#[derive(Debug, PartialEq, Eq)]
struct CliArgs {
    mode: CliMode,
    backend: String,
    readback_slots: usize,
    asset_layout: AssetLayoutMode,
    report: Option<PathBuf>,
    protocol_version: Option<u32>,
}

fn parse_args<I, S>(args: I) -> Result<CliArgs, String>
where
    I: IntoIterator<Item = S>,
    S: AsRef<str>,
{
    let mut values = args.into_iter().map(|arg| arg.as_ref().to_owned());
    let _program = values.next();
    let mut mode = None;
    let mut backend = "auto".to_owned();
    let mut readback_slots = 3usize;
    let mut asset_layout = AssetLayoutMode::Separate;
    let mut report = None;
    let mut requested_protocol_version = None;
    while let Some(arg) = values.next() {
        match arg.as_str() {
            "--self-test" => {
                if mode.replace(CliMode::SelfTest).is_some() {
                    return Err("select exactly one mode: --self-test, --stdin, --stdin-stream, or --capabilities".into());
                }
            }
            "--stdin" => {
                if mode.replace(CliMode::Stdin).is_some() {
                    return Err("select exactly one mode: --self-test, --stdin, --stdin-stream, or --capabilities".into());
                }
            }
            "--stdin-stream" => {
                if mode.replace(CliMode::StdinStream).is_some() {
                    return Err("select exactly one mode: --self-test, --stdin, --stdin-stream, or --capabilities".into());
                }
            }
            "--capabilities" => {
                if mode.replace(CliMode::Capabilities).is_some() {
                    return Err("select exactly one mode: --self-test, --stdin, --stdin-stream, or --capabilities".into());
                }
            }
            "--protocol-version" => {
                let value = values.next().ok_or("--protocol-version requires a value")?;
                if requested_protocol_version.is_some() {
                    return Err("--protocol-version may be specified only once".into());
                }
                requested_protocol_version = Some(
                    value
                        .parse::<u32>()
                        .map_err(|_| format!("invalid protocol version {value:?}"))?,
                );
            }
            "--backend" => {
                backend = values.next().ok_or("--backend requires a value")?;
                if !["auto", "dx12", "vulkan", "metal"].contains(&backend.as_str()) {
                    return Err(format!("unsupported backend {backend:?}"));
                }
            }
            "--readback-slots" => {
                let value = values.next().ok_or("--readback-slots requires a value")?;
                readback_slots = value
                    .parse()
                    .map_err(|_| format!("invalid readback slot count {value:?}"))?;
                if !(1..=3).contains(&readback_slots) {
                    return Err("readback slots must be 1, 2, or 3".into());
                }
            }
            "--asset-layout" => {
                let value = values
                    .next()
                    .ok_or("--asset-layout requires separate or atlas")?;
                asset_layout = match value.as_str() {
                    "separate" => AssetLayoutMode::Separate,
                    "atlas" => AssetLayoutMode::Atlas,
                    _ => return Err(format!("unsupported asset layout {value:?}")),
                };
            }
            "--report" => {
                let value = values.next().ok_or("--report requires a path")?;
                if value.trim().is_empty() {
                    return Err("--report path must not be empty".into());
                }
                report = Some(PathBuf::from(value));
            }
            _ => return Err(format!("unknown argument {arg:?}")),
        }
    }
    let mode = mode.ok_or("select --self-test, --stdin, --stdin-stream, or --capabilities")?;
    let protocol_version = match mode {
        CliMode::Capabilities => {
            let version = requested_protocol_version
                .ok_or("--capabilities requires an explicit --protocol-version")?;
            if version != 2 {
                return Err(format!("unsupported protocol version {version}"));
            }
            Some(version)
        }
        _ if requested_protocol_version.is_some() => {
            return Err("--protocol-version requires --capabilities".into())
        }
        _ => None,
    };
    Ok(CliArgs {
        mode,
        backend,
        readback_slots,
        asset_layout,
        report,
        protocol_version,
    })
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct CapabilityResponse {
    schema: u32,
    protocol: &'static str,
    protocol_version: u32,
    input_mode: &'static str,
    output_format: &'static str,
    capabilities: &'static [&'static str],
}

fn capability_response(protocol_version: u32) -> Result<CapabilityResponse, String> {
    if protocol_version != 2 {
        return Err(format!("unsupported protocol version {protocol_version}"));
    }
    Ok(CapabilityResponse {
        schema: 1,
        protocol: "NCT2",
        protocol_version,
        input_mode: "--stdin-stream",
        output_format: "rgba8",
        capabilities: &[
            "incremental-assets",
            "ordered-elements",
            "watermarks",
            "end-and-eof",
        ],
    })
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct RuntimeReport {
    schema: u32,
    protocol: &'static str,
    renderer: &'static str,
    version: &'static str,
    requested_backend: String,
    backend: String,
    adapter_name: String,
    adapter_type: String,
    readback_slots: usize,
    requested_asset_layout: String,
    asset_layout: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    asset_layout_fallback_reason: Option<String>,
    asset_page_count: usize,
    asset_source_bytes: u64,
    asset_allocated_bytes: u64,
    asset_telemetry: AssetTelemetryReport,
    #[serde(rename = "maxTextureDimension2D")]
    max_texture_dimension_2d: u32,
    completed_frames: u32,
    #[serde(skip_serializing_if = "Option::is_none")]
    output_queue_metrics: Option<WriterQueueMetrics>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct SelfTestReport {
    schema: u32,
    protocol: &'static str,
    renderer: &'static str,
    version: &'static str,
    backend: String,
    adapter_name: String,
    adapter_type: String,
    readback_slots: usize,
    requested_asset_layout: String,
    asset_layout: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    asset_layout_fallback_reason: Option<String>,
    asset_page_count: usize,
    asset_source_bytes: u64,
    asset_allocated_bytes: u64,
    #[serde(rename = "maxTextureDimension2D")]
    max_texture_dimension_2d: u32,
    test_frame_count: u32,
    test_frame_bytes: u64,
    #[serde(rename = "testFrameSHA256")]
    test_frame_sha256: String,
}

fn main() {
    if let Err(error) = run() {
        let _ = writeln!(io::stderr().lock(), "NICO_ERROR {error}");
        process::exit(1);
    }
}

fn run() -> Result<(), String> {
    let args = parse_args(std::env::args())?;
    match args.mode {
        CliMode::SelfTest => run_self_test(&args),
        CliMode::Stdin => run_stdin(&args),
        CliMode::StdinStream => run_stdin_stream(&args),
        CliMode::Capabilities => run_capabilities(&args),
    }
}

fn run_capabilities(args: &CliArgs) -> Result<(), String> {
    let version = args
        .protocol_version
        .ok_or("capability mode requires an explicit protocol version")?;
    let response = capability_response(version)?;
    let mut stdout = io::stdout().lock();
    serde_json::to_writer(&mut stdout, &response).map_err(|error| error.to_string())?;
    stdout.write_all(b"\n").map_err(|error| error.to_string())?;
    stdout.flush().map_err(|error| error.to_string())
}

fn run_self_test(args: &CliArgs) -> Result<(), String> {
    let scene = read_scene(SELF_TEST_SCENE).map_err(|error| format!("self-test NCT1: {error}"))?;
    let (
        mut renderer,
        backend,
        adapter_name,
        adapter_type,
        max_texture_dimension_2d,
        layout_report,
    ) = create_renderer(&scene, &args.backend, args.asset_layout)?;
    let mut runtime_report = RuntimeReport {
        schema: 1,
        protocol: "NCT1",
        renderer: "wgpu",
        version: env!("CARGO_PKG_VERSION"),
        requested_backend: args.backend.clone(),
        backend: backend.clone(),
        adapter_name: adapter_name.clone(),
        adapter_type: adapter_type.clone(),
        readback_slots: args.readback_slots,
        requested_asset_layout: layout_report.requested.as_str().to_owned(),
        asset_layout: layout_report.actual.as_str().to_owned(),
        asset_layout_fallback_reason: layout_report.fallback_reason.clone(),
        asset_page_count: layout_report.page_count,
        asset_source_bytes: layout_report.source_bytes,
        asset_allocated_bytes: layout_report.allocated_bytes,
        asset_telemetry: renderer
            .asset_telemetry_report(args.readback_slots)
            .map_err(|error| error.to_string())?,
        max_texture_dimension_2d,
        completed_frames: 0,
        output_queue_metrics: None,
        error: None,
    };
    write_report_if_requested(args.report.as_deref(), &runtime_report)?;

    let captured = Arc::new(Mutex::new(Vec::new()));
    let mut completed_frames = 0;
    let render_result = stream_frames(
        &mut renderer,
        &scene,
        args.readback_slots,
        SharedVec(Arc::clone(&captured)),
        false,
        &mut completed_frames,
    );
    runtime_report.completed_frames = completed_frames;
    runtime_report.asset_telemetry = renderer
        .asset_telemetry_report(args.readback_slots)
        .map_err(|error| error.to_string())?;
    if let Err(error) = render_result {
        runtime_report.error = Some(error.to_string());
        write_report_if_requested(args.report.as_deref(), &runtime_report)?;
        return Err(format!("self-test render/readback: {error}"));
    }
    let frame_bytes = (scene.header.width as usize)
        .checked_mul(scene.header.height as usize)
        .and_then(|value| value.checked_mul(4))
        .ok_or("self-test frame byte count overflow")?;
    let rgba = captured
        .lock()
        .map_err(|_| "self-test output buffer lock poisoned")?;
    let expected_bytes = frame_bytes
        .checked_mul(scene.header.frame_count as usize)
        .ok_or("self-test output byte count overflow")?;
    if rgba.len() != expected_bytes || completed_frames != scene.header.frame_count {
        runtime_report.error = Some("self-test readback length or frame count mismatch".into());
        write_report_if_requested(args.report.as_deref(), &runtime_report)?;
        return Err(format!(
            "self-test readback returned {} bytes and {completed_frames} frames; expected {expected_bytes} bytes and {} frames",
            rgba.len(), scene.header.frame_count
        ));
    }
    let first_frame = &rgba[..frame_bytes];
    if first_frame
        .chunks_exact(4)
        .any(|pixel| pixel != [255, 0, 0, 255])
        || rgba
            .chunks_exact(frame_bytes)
            .any(|frame| frame != first_frame)
    {
        runtime_report.error = Some("self-test pixels differ from pure-red fixture".into());
        write_report_if_requested(args.report.as_deref(), &runtime_report)?;
        return Err("self-test pixels differ from pure-red fixture".into());
    }
    let report = SelfTestReport {
        schema: 1,
        protocol: "NCT1",
        renderer: "wgpu",
        version: env!("CARGO_PKG_VERSION"),
        backend,
        adapter_name,
        adapter_type,
        readback_slots: args.readback_slots,
        requested_asset_layout: layout_report.requested.as_str().to_owned(),
        asset_layout: layout_report.actual.as_str().to_owned(),
        asset_layout_fallback_reason: layout_report.fallback_reason.clone(),
        asset_page_count: layout_report.page_count,
        asset_source_bytes: layout_report.source_bytes,
        asset_allocated_bytes: layout_report.allocated_bytes,
        max_texture_dimension_2d,
        test_frame_count: completed_frames,
        test_frame_bytes: expected_bytes as u64,
        test_frame_sha256: format!("{:x}", Sha256::digest(&*rgba)),
    };
    runtime_report.completed_frames = completed_frames;
    runtime_report.asset_telemetry = renderer
        .asset_telemetry_report(args.readback_slots)
        .map_err(|error| error.to_string())?;
    write_report_if_requested(args.report.as_deref(), &runtime_report)?;
    drop(rgba);
    let mut stdout = io::stdout().lock();
    serde_json::to_writer(&mut stdout, &report).map_err(|error| error.to_string())?;
    stdout.write_all(b"\n").map_err(|error| error.to_string())?;
    stdout.flush().map_err(|error| error.to_string())
}

fn run_stdin(args: &CliArgs) -> Result<(), String> {
    // Parse and validate the entire stream before the first RGBA byte is written.
    let scene = read_scene(io::stdin().lock()).map_err(|error| format!("NCT1 input: {error}"))?;
    let (
        mut renderer,
        backend,
        adapter_name,
        adapter_type,
        max_texture_dimension_2d,
        layout_report,
    ) = create_renderer(&scene, &args.backend, args.asset_layout)?;
    let mut report = RuntimeReport {
        schema: 1,
        protocol: "NCT1",
        renderer: "wgpu",
        version: env!("CARGO_PKG_VERSION"),
        requested_backend: args.backend.clone(),
        backend,
        adapter_name,
        adapter_type,
        readback_slots: args.readback_slots,
        requested_asset_layout: layout_report.requested.as_str().to_owned(),
        asset_layout: layout_report.actual.as_str().to_owned(),
        asset_layout_fallback_reason: layout_report.fallback_reason.clone(),
        asset_page_count: layout_report.page_count,
        asset_source_bytes: layout_report.source_bytes,
        asset_allocated_bytes: layout_report.allocated_bytes,
        asset_telemetry: renderer
            .asset_telemetry_report(args.readback_slots)
            .map_err(|error| error.to_string())?,
        max_texture_dimension_2d,
        completed_frames: 0,
        output_queue_metrics: None,
        error: None,
    };
    write_report_if_requested(args.report.as_deref(), &report)?;

    let mut completed_frames = 0;
    let result = stream_frames(
        &mut renderer,
        &scene,
        args.readback_slots,
        io::stdout(),
        true,
        &mut completed_frames,
    );
    report.completed_frames = completed_frames;
    report.asset_telemetry = renderer
        .asset_telemetry_report(args.readback_slots)
        .map_err(|error| error.to_string())?;
    if let Err(error) = result {
        report.error = Some(error.to_string());
        write_report_if_requested(args.report.as_deref(), &report)?;
        return Err(error.to_string());
    }
    write_report_if_requested(args.report.as_deref(), &report)?;
    writeln!(io::stderr().lock(), "NICO_DONE {completed_frames}")
        .map_err(|error| format!("writing completion status: {error}"))?;
    Ok(())
}

fn empty_scene_for_stream_header(header: &StreamHeader) -> Scene {
    Scene {
        header: protocol::Header {
            width: header.width,
            height: header.height,
            frame_count: header.frame_count,
            fps_num: header.fps_num,
            fps_den: header.fps_den,
            bundle_sha256: header.bundle_sha256,
        },
        assets: Vec::new(),
        draws: Vec::new(),
    }
}

fn streamed_asset_layout_result(requested: AssetLayoutMode) -> (&'static str, Option<String>) {
    match requested {
        AssetLayoutMode::Separate => ("streamed-distinct-textures", None),
        AssetLayoutMode::Atlas => (
            "streamed-distinct-textures",
            Some(
                "NCT2 incremental assets use distinct per-asset textures; requested atlas layout cannot be applied"
                    .to_owned(),
            ),
        ),
    }
}

fn run_stdin_stream(args: &CliArgs) -> Result<(), String> {
    let cpu_budget = CpuBudget::new();
    let mut parser =
        spawn_incremental_stream_parser_with_budget(io::stdin(), cpu_budget.clone(), || {})
            .map_err(|error| format!("starting NCT2 parser: {error}"))?;
    let initial_header = match receive_incremental_header(&parser) {
        Ok(event) => event,
        Err(error) => {
            parser.cancel();
            let join_result = parser.join();
            return Err(match join_result {
                Ok(()) => error.to_string(),
                Err(join_error) => format!("{error}; parser cleanup failed: {join_error}"),
            });
        }
    };
    let stream_header = match initial_header.payload.get() {
        IncrementalEvent::Header(header) => header.clone(),
        _ => unreachable!("receive_incremental_header only returns Header events"),
    };
    let scene = empty_scene_for_stream_header(&stream_header);
    let expected_header = scene.header.clone();
    let gpu_budget = GpuBudget::new();
    let (mut renderer, backend, adapter_name, adapter_type, max_dimension, layout_report) =
        match create_stream_renderer(&scene, &args.backend, args.asset_layout, gpu_budget.clone()) {
            Ok(created) => created,
            Err(error) => {
                parser.cancel();
                let join_result = parser.join();
                return Err(match join_result {
                    Ok(()) => error,
                    Err(join_error) => format!("{error}; parser cleanup failed: {join_error}"),
                });
            }
        };

    let render_result = stream_incremental_frames(
        &mut renderer,
        parser,
        initial_header,
        expected_header,
        cpu_budget,
        gpu_budget,
        args.readback_slots,
        io::stdout(),
        true,
        || {},
    );
    let registered_asset_pages = render_result
        .as_ref()
        .map_or(0, |completion| completion.asset_page_count);
    let (effective_asset_layout, asset_layout_fallback_reason) =
        streamed_asset_layout_result(layout_report.requested);
    let mut report = RuntimeReport {
        schema: 1,
        protocol: "NCT2",
        renderer: "wgpu",
        version: env!("CARGO_PKG_VERSION"),
        requested_backend: args.backend.clone(),
        backend,
        adapter_name,
        adapter_type,
        readback_slots: args.readback_slots,
        requested_asset_layout: layout_report.requested.as_str().to_owned(),
        asset_layout: effective_asset_layout.to_owned(),
        asset_layout_fallback_reason,
        asset_page_count: registered_asset_pages,
        asset_source_bytes: 0,
        asset_allocated_bytes: 0,
        asset_telemetry: AssetTelemetryReport::unavailable_for_streamed_assets(
            "NCT2 assets arrive incrementally as distinct textures; exact aggregate upload and residency telemetry is not currently tracked",
        ),
        max_texture_dimension_2d: max_dimension,
        completed_frames: 0,
        output_queue_metrics: render_result
            .as_ref()
            .ok()
            .and_then(|completion| completion.output_queue_metrics),
        error: None,
    };
    if let Err(error) = render_result {
        report.error = Some(error.to_string());
        write_report_if_requested(args.report.as_deref(), &report)?;
        return Err(error.to_string());
    }

    // The coordinator returns only after parser End+EOF validation, every
    // ordered frame's flush acknowledgement, and both worker joins succeed.
    report.completed_frames = stream_header.frame_count;
    write_report_if_requested(args.report.as_deref(), &report)?;
    writeln!(io::stderr().lock(), "NICO_DONE {}", report.completed_frames)
        .map_err(|error| format!("writing completion status: {error}"))?;
    Ok(())
}

fn create_renderer(
    scene: &Scene,
    backend: &str,
    asset_layout: AssetLayoutMode,
) -> Result<(Renderer, String, String, String, u32, AssetLayoutReport), String> {
    let candidates = backend_candidates(backend)?;
    let mut errors = Vec::new();
    for candidate in candidates {
        match Renderer::new_with_asset_layout(
            scene,
            &GpuOptions {
                backends: candidate,
                allow_software: false,
            },
            asset_layout,
        ) {
            Ok(renderer) => {
                let info = renderer.adapter_info();
                let actual_backend = backend_name(info.backend);
                let adapter_type = format!("{:?}", info.device_type);
                if !matches!(adapter_type.as_str(), "DiscreteGpu" | "IntegratedGpu") {
                    return Err(format!(
                        "software or unknown WGPU adapter is not allowed: {} ({adapter_type})",
                        info.name
                    ));
                }
                let max_dimension = renderer.device().limits().max_texture_dimension_2d;
                let adapter_name = info.name.clone();
                let layout_report = renderer.asset_layout_report().clone();
                return Ok((
                    renderer,
                    actual_backend,
                    adapter_name,
                    adapter_type,
                    max_dimension,
                    layout_report,
                ));
            }
            Err(error) => errors.push(format!("{candidate:?}: {error}")),
        }
    }
    Err(format!(
        "no allowed WGPU adapter for backend {backend:?}: {}",
        errors.join("; ")
    ))
}

fn create_stream_renderer(
    scene: &Scene,
    backend: &str,
    asset_layout: AssetLayoutMode,
    gpu_budget: GpuBudget,
) -> Result<(Renderer, String, String, String, u32, AssetLayoutReport), String> {
    let candidates = backend_candidates(backend)?;
    let mut errors = Vec::new();
    for candidate in candidates {
        match Renderer::new_with_asset_layout_and_gpu_budget(
            scene,
            &GpuOptions {
                backends: candidate,
                allow_software: false,
            },
            asset_layout,
            gpu_budget.clone(),
        ) {
            Ok(renderer) => {
                let info = renderer.adapter_info();
                let actual_backend = backend_name(info.backend);
                let adapter_type = format!("{:?}", info.device_type);
                if !matches!(adapter_type.as_str(), "DiscreteGpu" | "IntegratedGpu") {
                    return Err(format!(
                        "software or unknown WGPU adapter is not allowed: {} ({adapter_type})",
                        info.name
                    ));
                }
                let max_dimension = renderer.device().limits().max_texture_dimension_2d;
                let adapter_name = info.name.clone();
                let layout_report = renderer.asset_layout_report().clone();
                return Ok((
                    renderer,
                    actual_backend,
                    adapter_name,
                    adapter_type,
                    max_dimension,
                    layout_report,
                ));
            }
            Err(error) => errors.push(format!("{candidate:?}: {error}")),
        }
    }
    Err(format!(
        "no allowed WGPU adapter for backend {backend:?}: {}",
        errors.join("; ")
    ))
}

fn backend_candidates(backend: &str) -> Result<Vec<wgpu::Backends>, String> {
    match backend {
        "dx12" => Ok(vec![wgpu::Backends::DX12]),
        "vulkan" => Ok(vec![wgpu::Backends::VULKAN]),
        "metal" => Ok(vec![wgpu::Backends::METAL]),
        "auto" => {
            #[cfg(target_os = "windows")]
            {
                Ok(vec![wgpu::Backends::DX12, wgpu::Backends::VULKAN])
            }
            #[cfg(target_os = "macos")]
            {
                Ok(vec![wgpu::Backends::METAL])
            }
            #[cfg(target_os = "linux")]
            {
                Ok(vec![wgpu::Backends::VULKAN])
            }
            #[cfg(not(any(target_os = "windows", target_os = "macos", target_os = "linux")))]
            {
                Ok(vec![wgpu::Backends::all()])
            }
        }
        _ => Err(format!("unsupported WGPU backend {backend:?}")),
    }
}

fn backend_name(backend: wgpu::Backend) -> String {
    match backend {
        wgpu::Backend::Dx12 => "dx12",
        wgpu::Backend::Vulkan => "vulkan",
        wgpu::Backend::Metal => "metal",
        other => return format!("unknown:{other:?}").to_lowercase(),
    }
    .to_owned()
}

fn write_report_if_requested(path: Option<&Path>, report: &impl Serialize) -> Result<(), String> {
    let Some(path) = path else {
        return Ok(());
    };
    let bytes = serde_json::to_vec(report).map_err(|error| error.to_string())?;
    if bytes.len() > MAX_REPORT_BYTES {
        return Err("runtime report exceeds 2 MiB".into());
    }
    let parent = path
        .parent()
        .filter(|parent| !parent.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    let file_name = path
        .file_name()
        .ok_or("runtime report path must name a file")?
        .to_string_lossy();
    let nonce = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(|error| error.to_string())?
        .as_nanos();
    let temp = parent.join(format!(".{file_name}.{}.{}.tmp", process::id(), nonce));
    let mut file = File::create(&temp).map_err(|error| format!("creating report temp: {error}"))?;
    if let Err(error) = file.write_all(&bytes).and_then(|_| file.sync_all()) {
        let _ = fs::remove_file(&temp);
        return Err(format!("writing runtime report: {error}"));
    }
    drop(file);
    if let Err(error) = fs::rename(&temp, path) {
        let _ = fs::remove_file(&temp);
        return Err(format!("replacing runtime report: {error}"));
    }
    Ok(())
}

#[derive(Clone)]
struct SharedVec(Arc<Mutex<Vec<u8>>>);

impl Write for SharedVec {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        let mut target = self
            .0
            .lock()
            .map_err(|_| io::Error::new(io::ErrorKind::Other, "self-test output lock poisoned"))?;
        target.extend_from_slice(bytes);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use super::{
        backend_candidates, capability_response, empty_scene_for_stream_header, parse_args,
        AssetLayoutMode, AssetTelemetryReport, CliMode, RuntimeReport, SelfTestReport,
        MAX_REPORT_BYTES,
    };

    #[test]
    fn explicit_nct2_capability_version_has_a_separate_stable_response() {
        let args = parse_args([
            "nico-compositord",
            "--capabilities",
            "--protocol-version",
            "2",
        ])
        .expect("NCT2 capability request");
        assert_eq!(args.mode, CliMode::Capabilities);
        assert_eq!(args.protocol_version, Some(2));
        let response = capability_response(args.protocol_version.unwrap()).unwrap();
        assert_eq!(
            serde_json::to_string(&response).unwrap(),
            r#"{"schema":1,"protocol":"NCT2","protocolVersion":2,"inputMode":"--stdin-stream","outputFormat":"rgba8","capabilities":["incremental-assets","ordered-elements","watermarks","end-and-eof"]}"#
        );
    }

    #[test]
    fn capability_request_rejects_missing_unsupported_and_ambiguous_versions() {
        assert!(parse_args(["nico-compositord", "--capabilities"]).is_err());
        assert!(parse_args([
            "nico-compositord",
            "--capabilities",
            "--protocol-version",
            "1"
        ])
        .is_err());
        assert!(parse_args([
            "nico-compositord",
            "--capabilities",
            "--protocol-version",
            "3"
        ])
        .is_err());
        assert!(parse_args([
            "nico-compositord",
            "--capabilities",
            "--protocol-version",
            "2",
            "--protocol-version",
            "2"
        ])
        .is_err());
        assert!(parse_args([
            "nico-compositord",
            "--stdin-stream",
            "--protocol-version",
            "2"
        ])
        .is_err());
        assert!(parse_args([
            "nico-compositord",
            "--capabilities",
            "--self-test",
            "--protocol-version",
            "2"
        ])
        .is_err());
    }

    #[test]
    fn nct1_self_test_response_json_shape_remains_unchanged() {
        let report = SelfTestReport {
            schema: 1,
            protocol: "NCT1",
            renderer: "wgpu",
            version: "0.1.0",
            backend: "vulkan".into(),
            adapter_name: "fixture adapter".into(),
            adapter_type: "DiscreteGpu".into(),
            readback_slots: 3,
            requested_asset_layout: "separate".into(),
            asset_layout: "separate".into(),
            asset_layout_fallback_reason: None,
            asset_page_count: 1,
            asset_source_bytes: 4,
            asset_allocated_bytes: 4,
            max_texture_dimension_2d: 16_384,
            test_frame_count: 3,
            test_frame_bytes: 7_524,
            test_frame_sha256: "00".repeat(32),
        };
        assert_eq!(
            serde_json::to_string(&report).unwrap(),
            r#"{"schema":1,"protocol":"NCT1","renderer":"wgpu","version":"0.1.0","backend":"vulkan","adapterName":"fixture adapter","adapterType":"DiscreteGpu","readbackSlots":3,"requestedAssetLayout":"separate","assetLayout":"separate","assetPageCount":1,"assetSourceBytes":4,"assetAllocatedBytes":4,"maxTextureDimension2D":16384,"testFrameCount":3,"testFrameBytes":7524,"testFrameSHA256":"0000000000000000000000000000000000000000000000000000000000000000"}"#
        );
    }

    #[test]
    fn runtime_report_with_maximum_page_telemetry_fits_the_bounded_report_limit() {
        use nico_compositord::protocol::MAX_ASSETS;
        use nico_compositord::renderer::{PageOccupancy, ResidentMemoryReport};

        let telemetry = AssetTelemetryReport {
            telemetry_scope: "nct1-static-assets",
            effective_layout: "separate",
            pages: Some(
                (0..MAX_ASSETS as usize)
                    .map(|page_index| PageOccupancy {
                        page_index,
                        width: 16_384,
                        height: 16_384,
                        used_texels: 268_435_456,
                        allocated_texels: 268_435_456,
                    })
                    .collect(),
            ),
            texture_upload_call_count: Some(u64::from(MAX_ASSETS)),
            texture_upload_bytes: Some(256 * 1024 * 1024),
            draw_order_page_bind_run_count: Some(u64::from(MAX_ASSETS)),
            bundle_build_cpu_wall_time_ns: Some(u64::MAX),
            resident_memory: ResidentMemoryReport {
                decoded_scene_pixel_bytes: Some(256 * 1024 * 1024),
                logical_texture_allocation_bytes: Some(u64::MAX),
                renderer_owned_draw_buffer_payload_bytes: Some(u64::MAX),
                render_bundle_internal_bytes: None,
                readback_ring_buffer_bytes: Some(u64::MAX),
                wgpu_transient_staging_bytes: None,
                physical_vram_bytes: None,
                unknown_reasons: vec!["WGPU internals are not observable".to_owned()],
            },
            unavailable_reasons: Vec::new(),
        };
        let report = RuntimeReport {
            schema: 1,
            protocol: "NCT1",
            renderer: "wgpu",
            version: "0.1.0",
            requested_backend: "vulkan".into(),
            backend: "vulkan".into(),
            adapter_name: "fixture".into(),
            adapter_type: "DiscreteGpu".into(),
            readback_slots: 3,
            requested_asset_layout: "separate".into(),
            asset_layout: "separate".into(),
            asset_layout_fallback_reason: None,
            asset_page_count: MAX_ASSETS as usize,
            asset_source_bytes: 256 * 1024 * 1024,
            asset_allocated_bytes: u64::MAX,
            asset_telemetry: telemetry,
            max_texture_dimension_2d: 16_384,
            completed_frames: 0,
            output_queue_metrics: None,
            error: None,
        };
        let bytes = serde_json::to_vec(&report).unwrap();
        assert!(
            bytes.len() <= MAX_REPORT_BYTES,
            "serialized {} bytes",
            bytes.len()
        );
    }

    #[test]
    fn parses_stdin_backend_slots_and_report() {
        let args = [
            "nico-compositord",
            "--stdin",
            "--backend",
            "vulkan",
            "--readback-slots",
            "2",
            "--report",
            "job/report.json",
        ];
        let parsed = parse_args(args).unwrap();
        assert_eq!(parsed.mode, CliMode::Stdin);
        assert_eq!(parsed.backend, "vulkan");
        assert_eq!(parsed.readback_slots, 2);
        assert_eq!(parsed.asset_layout, AssetLayoutMode::Separate);
        assert_eq!(parsed.report, Some(PathBuf::from("job/report.json")));
    }

    #[test]
    fn parses_stdin_stream_as_an_explicit_mode() {
        let parsed = parse_args(["nico-compositord", "--stdin-stream"]).unwrap();
        assert_eq!(parsed.mode, CliMode::StdinStream);
        assert_eq!(parsed.backend, "auto");
        assert_eq!(parsed.readback_slots, 3);
    }

    #[test]
    fn stream_bootstrap_scene_copies_only_validated_header_fields() {
        let header = super::StreamHeader {
            width: 1920,
            height: 1080,
            frame_count: 180,
            fps_num: 30,
            fps_den: 1,
            declaration_count: 72,
            flags: 3,
            bundle_sha256: [0x5a; 32],
        };
        let scene = empty_scene_for_stream_header(&header);
        assert_eq!(scene.header.width, header.width);
        assert_eq!(scene.header.height, header.height);
        assert_eq!(scene.header.frame_count, header.frame_count);
        assert_eq!(scene.header.fps_num, header.fps_num);
        assert_eq!(scene.header.fps_den, header.fps_den);
        assert_eq!(scene.header.bundle_sha256, header.bundle_sha256);
        assert!(scene.assets.is_empty());
        assert!(scene.draws.is_empty());
    }

    #[test]
    fn parses_defaulted_self_test() {
        let parsed = parse_args(["nico-compositord", "--self-test"]).unwrap();
        assert_eq!(parsed.mode, CliMode::SelfTest);
        assert_eq!(parsed.backend, "auto");
        assert_eq!(parsed.readback_slots, 3);
        assert_eq!(parsed.asset_layout, AssetLayoutMode::Separate);
    }

    #[test]
    fn parses_explicit_atlas_layout() {
        let parsed =
            parse_args(["nico-compositord", "--stdin", "--asset-layout", "atlas"]).unwrap();
        assert_eq!(parsed.asset_layout, AssetLayoutMode::Atlas);
    }

    #[test]
    fn nct2_atlas_request_reports_distinct_texture_fallback_and_unknown_metrics() {
        let (separate_actual, separate_fallback) =
            super::streamed_asset_layout_result(AssetLayoutMode::Separate);
        assert_eq!(separate_actual, "streamed-distinct-textures");
        assert_eq!(separate_fallback, None);

        let (actual, fallback) = super::streamed_asset_layout_result(AssetLayoutMode::Atlas);
        assert_eq!(actual, "streamed-distinct-textures");
        assert!(fallback
            .as_deref()
            .unwrap()
            .contains("requested atlas layout cannot be applied"));

        let telemetry = AssetTelemetryReport::unavailable_for_streamed_assets(
            "NCT2 incremental asset telemetry is not aggregated",
        );
        let report = serde_json::json!({
            "requestedAssetLayout": AssetLayoutMode::Atlas.as_str(),
            "assetLayout": actual,
            "assetLayoutFallbackReason": fallback,
            "assetTelemetry": telemetry,
        });
        assert_eq!(report["requestedAssetLayout"], "atlas");
        assert_eq!(report["assetLayout"], "streamed-distinct-textures");
        assert!(report["assetLayoutFallbackReason"]
            .as_str()
            .unwrap()
            .contains("requested atlas layout cannot be applied"));
        assert_eq!(
            report["assetTelemetry"]["effectiveLayout"],
            "streamed-distinct-textures"
        );
        assert_eq!(report["assetTelemetry"]["pages"], serde_json::Value::Null);
        assert_eq!(
            report["assetTelemetry"]["textureUploadCallCount"],
            serde_json::Value::Null
        );
        assert_eq!(
            report["assetTelemetry"]["textureUploadBytes"],
            serde_json::Value::Null
        );
        assert_eq!(
            report["assetTelemetry"]["bundleBuildCpuWallTimeNs"],
            serde_json::Value::Null
        );
        assert_eq!(
            report["assetTelemetry"]["residentMemory"]["decodedScenePixelBytes"],
            serde_json::Value::Null
        );
        assert_eq!(
            report["assetTelemetry"]["residentMemory"]["physicalVramBytes"],
            serde_json::Value::Null
        );
    }

    #[test]
    fn rejects_ambiguous_or_invalid_arguments() {
        assert!(parse_args(["nico-compositord"]).is_err());
        assert!(parse_args(["nico-compositord", "--stdin", "--self-test"]).is_err());
        assert!(parse_args(["nico-compositord", "--stdin", "--stdin-stream"]).is_err());
        assert!(parse_args(["nico-compositord", "--stdin", "--backend", "warp"]).is_err());
        assert!(parse_args(["nico-compositord", "--stdin", "--readback-slots", "4"]).is_err());
        assert!(parse_args(["nico-compositord", "--stdin", "--report"]).is_err());
        assert!(parse_args(["nico-compositord", "--stdin", "--asset-layout"]).is_err());
        assert!(parse_args(["nico-compositord", "--stdin", "--asset-layout", "gpu"]).is_err());
        assert!(parse_args(["nico-compositord", "--stdin", "--unknown"]).is_err());
    }

    #[test]
    fn auto_backend_candidates_are_platform_specific_and_hardware_only() {
        let candidates = backend_candidates("auto").unwrap();
        #[cfg(target_os = "windows")]
        assert_eq!(
            candidates,
            vec![wgpu::Backends::DX12, wgpu::Backends::VULKAN]
        );
        #[cfg(target_os = "macos")]
        assert_eq!(candidates, vec![wgpu::Backends::METAL]);
        #[cfg(target_os = "linux")]
        assert_eq!(candidates, vec![wgpu::Backends::VULKAN]);
        assert!(backend_candidates("unknown").is_err());
    }
}
