use std::io::{Cursor, Read, Write};
use std::process::{Command, Output, Stdio};
use std::time::{SystemTime, UNIX_EPOCH};

use nico_compositord::protocol;
use nico_compositord::stream_protocol::{
    checked_scene_asset_total_bytes, read_stream, spawn_stream_parser, ParsedStream,
};
use sha2::{Digest, Sha256};

const EMPTY: &[u8] =
    include_bytes!("../../../internal/nicorender/testdata/timeline/stream/empty-scene.nct2");
const PARITY: &[u8] = include_bytes!(
    "../../../internal/nicorender/testdata/timeline/stream/nct1-compat-multidraw.nct2"
);
const OWNER_INVERSION: &[u8] = include_bytes!(
    "../../../internal/nicorender/testdata/timeline/stream/owner-time-inversion.nct2"
);
const READINESS_VECTORS: &str =
    include_str!("../../../internal/nicorender/testdata/timeline/stream/vectors.json");
const REAL_READINESS: &str = include_str!(
    "../../../internal/nicorender/testdata/timeline/stream/real-snapshot-readiness.json"
);
const LEGACY_NCT1: &[u8] = include_bytes!(
    "../../../internal/nicorender/testdata/timeline/wire/wire-multidraw-33x19-3frames.nct"
);
const LEGACY_NCT1_SHA256: &str = "d506db27b68a294e8e8a9d5db1f512d274da8b2c62543b5213c121e26f379948";
const PARITY_SCENE_COMMITMENT: &str =
    "c3f6fa5024466409879965d7cc0471aa33d5d3fb1b5842185bee5e923f785350";

const MAX_RECORD_PAYLOAD: u32 = 16 * 1024 * 1024;

fn parse(bytes: &[u8]) -> Result<ParsedStream, nico_compositord::stream_protocol::Error> {
    read_stream(Cursor::new(bytes))
}

struct TestArtifacts(std::path::PathBuf);

impl TestArtifacts {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock after epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!("nico-t610-{}-{nonce}", std::process::id()));
        std::fs::create_dir(&path).expect("create exclusive T6.10 test directory");
        Self(path)
    }

    fn path(&self, name: &str) -> std::path::PathBuf {
        self.0.join(name)
    }
}

impl Drop for TestArtifacts {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn run_compositor(mode: &str, input: &[u8], report: &std::path::Path) -> (Output, u32) {
    let mut child = Command::new(env!("CARGO_BIN_EXE_nico-compositord"))
        .args([
            mode,
            "--backend",
            "auto",
            "--readback-slots",
            "2",
            "--report",
        ])
        .arg(report)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn compositor helper");
    let pid = child.id();
    child
        .stdin
        .take()
        .expect("piped stdin")
        .write_all(input)
        .expect("write complete scene to compositor");
    (
        child.wait_with_output().expect("wait compositor process"),
        pid,
    )
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn write_record_header<W: Write>(
    out: &mut W,
    kind: u32,
    sequence: u64,
    payload_len: usize,
) -> std::io::Result<()> {
    out.write_all(&kind.to_le_bytes())?;
    out.write_all(&(payload_len as u32).to_le_bytes())?;
    out.write_all(&sequence.to_le_bytes())
}

fn encode_draw_for_test(draw: &protocol::Draw, bytes: &mut [u8; protocol::DRAW_BYTES]) {
    bytes.fill(0);
    bytes[0..4].copy_from_slice(&draw.asset_id.to_le_bytes());
    bytes[4..8].copy_from_slice(&draw.start_vpos.to_le_bytes());
    bytes[8..12].copy_from_slice(&draw.end_vpos.to_le_bytes());
    bytes[12..16].copy_from_slice(&draw.anchor_vpos.to_le_bytes());
    bytes[16..20].copy_from_slice(&draw.owner_order.to_le_bytes());
    bytes[20..24].copy_from_slice(&draw.comment_index.to_le_bytes());
    bytes[24..28].copy_from_slice(&draw.primitive_index.to_le_bytes());
    for (index, value) in draw.rect.iter().enumerate() {
        let start = 28 + index * 4;
        bytes[start..start + 4].copy_from_slice(&value.to_le_bytes());
    }
    for (index, value) in draw.projection.iter().enumerate() {
        let start = 44 + index * 4;
        bytes[start..start + 4].copy_from_slice(&value.to_le_bytes());
    }
    bytes[108..112].copy_from_slice(&draw.alpha.to_le_bytes());
    bytes[112..120].copy_from_slice(&draw.anchor_x.to_le_bytes());
    bytes[120..128].copy_from_slice(&draw.speed_x.to_le_bytes());
}

fn scene_commitment_for_test(
    header: &nico_compositord::stream_protocol::Header,
    descriptors: &[nico_compositord::stream_protocol::AssetDescriptor],
    elements: &[nico_compositord::stream_protocol::Element],
    total_raw: u64,
) -> [u8; 32] {
    let draw_count: usize = elements.iter().map(|element| element.draws.len()).sum();
    let mut canonical_header = [0u8; protocol::HEADER_BYTES];
    canonical_header[0..4].copy_from_slice(b"NCT1");
    canonical_header[4..8].copy_from_slice(&(protocol::HEADER_BYTES as u32).to_le_bytes());
    canonical_header[8..12].copy_from_slice(&header.width.to_le_bytes());
    canonical_header[12..16].copy_from_slice(&header.height.to_le_bytes());
    canonical_header[16..20].copy_from_slice(&header.frame_count.to_le_bytes());
    canonical_header[20..24].copy_from_slice(&header.fps_num.to_le_bytes());
    canonical_header[24..28].copy_from_slice(&header.fps_den.to_le_bytes());
    canonical_header[28..32].copy_from_slice(&(descriptors.len() as u32).to_le_bytes());
    canonical_header[32..36].copy_from_slice(&(draw_count as u32).to_le_bytes());
    canonical_header[36..68].copy_from_slice(&header.bundle_sha256);
    canonical_header[68..76].copy_from_slice(&total_raw.to_le_bytes());
    canonical_header[76..80].copy_from_slice(&protocol::FLAGS.to_le_bytes());

    let mut sorted_descriptors = descriptors.to_vec();
    sorted_descriptors.sort_unstable_by_key(|descriptor| descriptor.id);
    let mut hasher = Sha256::new();
    hasher.update(b"NCT2-SCENE-DIGEST-v1\0");
    hasher.update(canonical_header);
    for descriptor in sorted_descriptors {
        hasher.update(descriptor.id.to_le_bytes());
        hasher.update(descriptor.width.to_le_bytes());
        hasher.update(descriptor.height.to_le_bytes());
        hasher.update(descriptor.raw_len.to_le_bytes());
        hasher.update(descriptor.declared_sha256);
    }
    let mut draw_bytes = [0u8; protocol::DRAW_BYTES];
    for element in elements {
        for draw in &element.draws {
            encode_draw_for_test(draw, &mut draw_bytes);
            hasher.update(draw_bytes);
        }
    }
    hasher.finalize().into()
}

fn write_max_asset_scene(path: &std::path::Path) {
    const ASSET_WIDTH: u32 = 4_096;
    const ASSET_HEIGHT: u32 = 8_192;
    const ASSET_BYTES: usize = ASSET_WIDTH as usize * ASSET_HEIGHT as usize * 4;
    const CHUNK_BYTES: usize = 4 * 1024 * 1024;
    const TOTAL_BYTES: u64 = (ASSET_BYTES as u64) * 2;

    let reference = parse(PARITY).expect("parity stream provides the frozen layout and Draws");
    let zero_chunk = vec![0u8; CHUNK_BYTES];
    let mut pixel_hasher = Sha256::new();
    for _ in 0..(ASSET_BYTES / CHUNK_BYTES) {
        pixel_hasher.update(&zero_chunk);
    }
    let asset_sha: [u8; 32] = pixel_hasher.finalize().into();
    let descriptors: Vec<_> = reference
        .asset_descriptors
        .iter()
        .map(|old| nico_compositord::stream_protocol::AssetDescriptor {
            id: old.id,
            width: ASSET_WIDTH,
            height: ASSET_HEIGHT,
            raw_len: ASSET_BYTES as u64,
            declared_sha256: asset_sha,
        })
        .collect();
    let commitment = scene_commitment_for_test(
        &reference.header,
        &descriptors,
        &reference.elements,
        TOTAL_BYTES,
    );

    let mut out = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
        .expect("create exclusive max-scene stream artifact");
    let mut sequence = 0u64;
    let mut replacing_asset = false;
    let mut descriptor_index = 0usize;
    for (offset, kind, payload_len) in record_offsets(PARITY) {
        let payload = &PARITY[offset + 16..offset + 16 + payload_len];
        match kind {
            4 => {
                replacing_asset = true;
                let descriptor = &descriptors[descriptor_index];
                descriptor_index += 1;
                let mut begin = [0u8; 52];
                begin[0..4].copy_from_slice(&descriptor.id.to_le_bytes());
                begin[4..8].copy_from_slice(&descriptor.width.to_le_bytes());
                begin[8..12].copy_from_slice(&descriptor.height.to_le_bytes());
                begin[12..20].copy_from_slice(&descriptor.raw_len.to_le_bytes());
                begin[20..52].copy_from_slice(&descriptor.declared_sha256);
                write_record_header(&mut out, 4, sequence, begin.len()).unwrap();
                out.write_all(&begin).unwrap();
                sequence += 1;

                let mut offset = 0u64;
                while offset < ASSET_BYTES as u64 {
                    let len = CHUNK_BYTES.min(ASSET_BYTES - offset as usize);
                    write_record_header(&mut out, 5, sequence, 12 + len).unwrap();
                    out.write_all(&descriptor.id.to_le_bytes()).unwrap();
                    out.write_all(&offset.to_le_bytes()).unwrap();
                    out.write_all(&zero_chunk[..len]).unwrap();
                    sequence += 1;
                    offset += len as u64;
                }

                let mut end = [0u8; 44];
                end[0..4].copy_from_slice(&descriptor.id.to_le_bytes());
                end[4..12].copy_from_slice(&descriptor.raw_len.to_le_bytes());
                end[12..44].copy_from_slice(&descriptor.declared_sha256);
                write_record_header(&mut out, 6, sequence, end.len()).unwrap();
                out.write_all(&end).unwrap();
                sequence += 1;
            }
            5 | 6 if replacing_asset => {
                if kind == 6 {
                    replacing_asset = false;
                }
            }
            _ => {
                let mut edited = payload.to_vec();
                if kind == 9 {
                    edited[12..20].copy_from_slice(&TOTAL_BYTES.to_le_bytes());
                    edited[32..64].copy_from_slice(&commitment);
                }
                write_record_header(&mut out, kind, sequence, edited.len()).unwrap();
                out.write_all(&edited).unwrap();
                sequence += 1;
            }
        }
    }
    assert_eq!(descriptor_index, 2);
    out.flush().unwrap();
}

fn recompute_declaration_digest(stream: &mut [u8]) {
    let declaration = record_offset(stream, 2) + 16;
    let count =
        u32::from_le_bytes(stream[declaration..declaration + 4].try_into().unwrap()) as usize;
    let end = declaration + 4 + count * 20;
    let digest: [u8; 32] = Sha256::digest(&stream[declaration + 4..end]).into();
    let complete = record_offset(stream, 3) + 16;
    stream[complete + 4..complete + 36].copy_from_slice(&digest);
}

fn legacy_draws_offset(scene_bytes: &[u8]) -> usize {
    let scene = protocol::read_scene(Cursor::new(scene_bytes)).expect("valid NCT1 fixture");
    protocol::HEADER_BYTES
        + scene
            .assets
            .iter()
            .map(|asset| 4 + 44 + asset.rgba.len())
            .sum::<usize>()
}

fn set_draw_visual_fields(
    bytes: &mut [u8],
    draw_offset: usize,
    owner: u32,
    comment_index: u32,
    rect: [f32; 4],
    alpha: f32,
) {
    bytes[draw_offset + 16..draw_offset + 20].copy_from_slice(&owner.to_le_bytes());
    bytes[draw_offset + 20..draw_offset + 24].copy_from_slice(&comment_index.to_le_bytes());
    for (index, value) in rect.iter().enumerate() {
        let start = draw_offset + 28 + index * 4;
        bytes[start..start + 4].copy_from_slice(&value.to_le_bytes());
    }
    bytes[draw_offset + 108..draw_offset + 112].copy_from_slice(&alpha.to_le_bytes());
}

fn translucent_owner_order_case() -> (Vec<u8>, Vec<u8>) {
    let rect = [8.25f32, 6.5, 16.0, 7.0];
    let mut legacy = LEGACY_NCT1.to_vec();
    let nct1_draw_start = legacy_draws_offset(&legacy);
    set_draw_visual_fields(&mut legacy, nct1_draw_start, 0, 0, rect, 0.55);
    set_draw_visual_fields(
        &mut legacy,
        nct1_draw_start + protocol::DRAW_BYTES,
        1,
        0,
        rect,
        0.75,
    );

    let mut stream = PARITY.to_vec();
    let declaration = record_offset(&stream, 2) + 16;
    set_u32(&mut stream, declaration + 4 + 4, 0);
    set_u32(&mut stream, declaration + 4 + 8, 0);
    set_u32(&mut stream, declaration + 24 + 4, 1);
    set_u32(&mut stream, declaration + 24 + 8, 0);
    let mut ordinal = 0usize;
    for (record, kind, length) in record_offsets(&stream) {
        if kind != 7 {
            continue;
        }
        let payload = record + 16;
        let count =
            u32::from_le_bytes(stream[payload + 4..payload + 8].try_into().unwrap()) as usize;
        for index in 0..count {
            let draw_offset = payload + 8 + index * protocol::DRAW_BYTES;
            let (owner, alpha) = if ordinal == 0 { (0, 0.55) } else { (1, 0.75) };
            set_draw_visual_fields(&mut stream, draw_offset, owner, 0, rect, alpha);
            ordinal += 1;
        }
        let _ = length;
    }
    assert_eq!(ordinal, 2);
    recompute_declaration_digest(&mut stream);
    recompute_test_asset_hashes_and_commitment(&mut stream);
    (legacy, stream)
}

fn fractional_60000_1001_case() -> (Vec<u8>, Vec<u8>) {
    let mut legacy = LEGACY_NCT1.to_vec();
    set_u32(&mut legacy, 20, 60_000);
    set_u32(&mut legacy, 24, 1_001);

    let mut stream = PARITY.to_vec();
    let header = record_offset(&stream, 1) + 16;
    set_u32(&mut stream, header + 24, 60_000);
    set_u32(&mut stream, header + 28, 1_001);
    let exclusive_end = protocol::frame_vpos(3, 60_000, 1_001).unwrap();
    let declaration = record_offset(&stream, 2) + 16;
    let count = u32::from_le_bytes(stream[declaration..declaration + 4].try_into().unwrap());
    for index in 0..count as usize {
        let entry = declaration + 4 + index * 20;
        let start = i32::from_le_bytes(stream[entry + 12..entry + 16].try_into().unwrap());
        let end = i32::from_le_bytes(stream[entry + 16..entry + 20].try_into().unwrap());
        stream[entry + 12..entry + 16]
            .copy_from_slice(&start.clamp(0, exclusive_end).to_le_bytes());
        stream[entry + 16..entry + 20].copy_from_slice(&end.clamp(0, exclusive_end).to_le_bytes());
    }
    let starts: Vec<i32> = (0..count as usize)
        .map(|index| {
            let entry = declaration + 4 + index * 20;
            i32::from_le_bytes(stream[entry + 12..entry + 16].try_into().unwrap())
        })
        .collect();
    for (record, kind, _) in record_offsets(&stream) {
        if kind == 8 {
            let payload = record + 16;
            let prefix =
                u32::from_le_bytes(stream[payload..payload + 4].try_into().unwrap()) as usize;
            let ready = starts[prefix..]
                .iter()
                .map(|value| *value as i64)
                .min()
                .unwrap_or(exclusive_end as i64);
            stream[payload + 4..payload + 12].copy_from_slice(&ready.to_le_bytes());
        } else if kind == 9 {
            let payload = record + 16;
            let prefix =
                u32::from_le_bytes(stream[payload + 20..payload + 24].try_into().unwrap()) as usize;
            stream[payload + 24..payload + 32].copy_from_slice(
                &(if prefix >= starts.len() {
                    exclusive_end as i64
                } else {
                    0
                })
                .to_le_bytes(),
            );
        }
    }
    recompute_declaration_digest(&mut stream);
    recompute_test_asset_hashes_and_commitment(&mut stream);
    (legacy, stream)
}

struct SlowBudgetWriter {
    bytes_written: std::sync::Arc<std::sync::atomic::AtomicUsize>,
}

impl Write for SlowBudgetWriter {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        std::thread::sleep(std::time::Duration::from_millis(8));
        self.bytes_written
            .fetch_add(bytes.len(), std::sync::atomic::Ordering::Relaxed);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        std::thread::sleep(std::time::Duration::from_millis(8));
        Ok(())
    }
}

#[test]
#[ignore = "requires explicit NICO_TIMELINE_STREAM_GPU_TEST=1 and an actual wgpu adapter"]
fn actual_adapter_max_asset_scene_slow_consumer_stays_within_credits() {
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
    use std::sync::Arc;
    use std::time::Duration;

    use nico_compositord::readback::{receive_incremental_header, stream_incremental_frames};
    use nico_compositord::renderer::{AssetLayoutMode, GpuOptions, Renderer};
    use nico_compositord::stream_budget::{CpuBudget, CPU_BUDGET_BYTES};
    use nico_compositord::stream_protocol::spawn_incremental_stream_parser_with_budget;
    use nico_compositord::stream_renderer::GpuBudget;

    assert_eq!(
        std::env::var("NICO_TIMELINE_STREAM_GPU_TEST").as_deref(),
        Ok("1"),
        "opt in explicitly with NICO_TIMELINE_STREAM_GPU_TEST=1"
    );
    const TOTAL_RAW: usize = 256 * 1024 * 1024;
    const FRAME_BYTES: usize = 33 * 19 * 4;
    let artifacts = TestArtifacts::new();
    let input_path = artifacts.path("max-assets.nct2");
    write_max_asset_scene(&input_path);
    assert_eq!(
        std::fs::metadata(&input_path).unwrap().len() > TOTAL_RAW as u64,
        true
    );

    let cpu_budget = CpuBudget::new();
    let gpu_budget = GpuBudget::new();
    let parser = spawn_incremental_stream_parser_with_budget(
        std::fs::File::open(&input_path).unwrap(),
        cpu_budget.clone(),
        || {},
    )
    .expect("spawn real incremental parser");
    let initial_header = receive_incremental_header(&parser).expect("validated Header event");
    let stream_header = match initial_header.payload.get() {
        nico_compositord::stream_protocol::IncrementalEvent::Header(header) => header.clone(),
        other => panic!("expected Header, got {other:?}"),
    };
    let header = protocol::Header {
        width: stream_header.width,
        height: stream_header.height,
        frame_count: stream_header.frame_count,
        fps_num: stream_header.fps_num,
        fps_den: stream_header.fps_den,
        bundle_sha256: stream_header.bundle_sha256,
    };
    let scene = protocol::Scene {
        header: header.clone(),
        assets: Vec::new(),
        draws: Vec::new(),
    };
    let mut renderer = Renderer::new_with_asset_layout_and_gpu_budget(
        &scene,
        &GpuOptions::default(),
        AssetLayoutMode::Separate,
        gpu_budget.clone(),
    )
    .expect("initialize actual adapter renderer");

    let max_cpu = Arc::new(AtomicUsize::new(cpu_budget.used_bytes()));
    let max_gpu = Arc::new(AtomicUsize::new(gpu_budget.used_bytes()));
    let sampling = Arc::new(AtomicBool::new(true));
    let sampler_cpu = cpu_budget.clone();
    let sampler_gpu = gpu_budget.clone();
    let sampler_max_cpu = Arc::clone(&max_cpu);
    let sampler_max_gpu = Arc::clone(&max_gpu);
    let sampler_running = Arc::clone(&sampling);
    let sampler = std::thread::spawn(move || {
        while sampler_running.load(Ordering::Acquire) {
            sampler_max_cpu.fetch_max(sampler_cpu.used_bytes(), Ordering::Relaxed);
            sampler_max_gpu.fetch_max(sampler_gpu.used_bytes(), Ordering::Relaxed);
            std::thread::sleep(Duration::from_micros(100));
        }
    });
    let bytes_written = Arc::new(AtomicUsize::new(0));
    eprintln!(
        "T11.4 runtime sample starting test-pid={} base=t6.10-max-assets input-bytes={} cpu-limit={} gpu-limit={}",
        std::process::id(),
        std::fs::metadata(&input_path).unwrap().len(),
        CPU_BUDGET_BYTES,
        gpu_budget.limit_bytes()
    );
    let completion = stream_incremental_frames(
        &mut renderer,
        parser,
        initial_header,
        header,
        cpu_budget.clone(),
        gpu_budget.clone(),
        2,
        SlowBudgetWriter {
            bytes_written: Arc::clone(&bytes_written),
        },
        false,
        || {},
    )
    .expect("real NCT2 stream render with slow consumer");
    sampling.store(false, Ordering::Release);
    sampler.join().expect("budget sampler joins");
    max_cpu.fetch_max(cpu_budget.used_bytes(), Ordering::Relaxed);
    max_gpu.fetch_max(gpu_budget.used_bytes(), Ordering::Relaxed);
    assert_eq!(bytes_written.load(Ordering::Relaxed), FRAME_BYTES * 3);
    let queue_metrics = completion
        .output_queue_metrics
        .expect("real NCT2 completion reports output queue metrics");
    assert_eq!(queue_metrics.capacity, 2);
    assert_eq!(queue_metrics.occupancy, 0);
    assert!(queue_metrics.high_water <= queue_metrics.capacity);
    assert!(max_cpu.load(Ordering::Relaxed) <= CPU_BUDGET_BYTES);
    assert!(max_gpu.load(Ordering::Relaxed) <= gpu_budget.limit_bytes());
    assert!(max_gpu.load(Ordering::Relaxed) >= TOTAL_RAW);
    drop(renderer);
    assert_eq!(
        cpu_budget.used_bytes(),
        0,
        "CPU credits returned after EOF/join"
    );
    assert_eq!(
        gpu_budget.used_bytes(),
        0,
        "GPU credits returned after renderer drop"
    );
    eprintln!(
        "T11.4 runtime sample raw-assets={} cpu-peak={} / {} gpu-peak={} / {} output={} final_cpu=0 final_gpu=0 writer-queue-capacity={} writer-queue-high-water={} writer-queue-final=0",
        TOTAL_RAW,
        max_cpu.load(Ordering::Relaxed),
        CPU_BUDGET_BYTES,
        max_gpu.load(Ordering::Relaxed),
        gpu_budget.limit_bytes(),
        bytes_written.load(Ordering::Relaxed),
        queue_metrics.capacity,
        queue_metrics.high_water
    );
}

#[test]
#[ignore = "requires explicit NICO_TIMELINE_STREAM_GPU_TEST=1 and an actual wgpu adapter"]
fn actual_adapter_stopped_output_is_unblocked_on_trailing_input_error() {
    use std::io;
    use std::sync::{Arc, Condvar, Mutex};
    use std::time::Duration;

    use nico_compositord::readback::{receive_incremental_header, stream_incremental_frames};
    use nico_compositord::renderer::{AssetLayoutMode, GpuOptions, Renderer};
    use nico_compositord::stream_budget::CpuBudget;
    use nico_compositord::stream_protocol::spawn_incremental_stream_parser_with_budget;
    use nico_compositord::stream_renderer::GpuBudget;

    #[derive(Default)]
    struct GateState {
        source_released: bool,
        output_unblocked: bool,
        events: Vec<&'static str>,
    }
    type Gate = Arc<(Mutex<GateState>, Condvar)>;

    struct GatedTrailingInput {
        input: Cursor<Vec<u8>>,
        valid_len: usize,
        gate: Gate,
    }
    impl Read for GatedTrailingInput {
        fn read(&mut self, output: &mut [u8]) -> io::Result<usize> {
            if self.input.position() as usize >= self.valid_len {
                let (lock, changed) = &*self.gate;
                let mut state = lock.lock().unwrap();
                state.events.push("source_wait");
                while !state.source_released {
                    state = changed.wait(state).unwrap();
                }
            }
            self.input.read(output)
        }
    }
    impl Drop for GatedTrailingInput {
        fn drop(&mut self) {
            self.gate.0.lock().unwrap().events.push("source_drop");
        }
    }

    struct BlockedOutput(Gate);
    impl Write for BlockedOutput {
        fn write(&mut self, _bytes: &[u8]) -> io::Result<usize> {
            let (lock, changed) = &*self.0;
            let mut state = lock.lock().unwrap();
            state.events.push("writer_blocked");
            state.source_released = true;
            changed.notify_all();
            let (mut next, timeout) = changed
                .wait_timeout_while(state, Duration::from_secs(5), |state| {
                    !state.output_unblocked
                })
                .unwrap();
            if timeout.timed_out() {
                next.events.push("writer_timeout");
                return Err(io::Error::new(
                    io::ErrorKind::TimedOut,
                    "test writer stayed blocked",
                ));
            }
            next.events.push("writer_return");
            Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                "test output stopped",
            ))
        }

        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }
    impl Drop for BlockedOutput {
        fn drop(&mut self) {
            self.0 .0.lock().unwrap().events.push("writer_drop");
        }
    }

    assert_eq!(
        std::env::var("NICO_TIMELINE_STREAM_GPU_TEST").as_deref(),
        Ok("1"),
        "opt in explicitly with NICO_TIMELINE_STREAM_GPU_TEST=1"
    );
    let parsed = parse(PARITY).unwrap();
    let mut input = PARITY.to_vec();
    input.push(0x7f);
    let gate: Gate = Arc::new((Mutex::new(GateState::default()), Condvar::new()));
    let input_gate = Arc::clone(&gate);
    let cpu_budget = CpuBudget::new();
    let gpu_budget = GpuBudget::new();
    let parser = spawn_incremental_stream_parser_with_budget(
        GatedTrailingInput {
            input: Cursor::new(input),
            valid_len: PARITY.len(),
            gate: Arc::clone(&gate),
        },
        cpu_budget.clone(),
        move || {
            let (lock, changed) = &*input_gate;
            let mut state = lock.lock().unwrap();
            state.events.push("input_unblock");
            state.source_released = true;
            changed.notify_all();
        },
    )
    .unwrap();
    let initial_header = receive_incremental_header(&parser).unwrap();
    let stream_header = match initial_header.payload.get() {
        nico_compositord::stream_protocol::IncrementalEvent::Header(header) => header.clone(),
        other => panic!("expected Header, got {other:?}"),
    };
    let header = protocol::Header {
        width: stream_header.width,
        height: stream_header.height,
        frame_count: stream_header.frame_count,
        fps_num: stream_header.fps_num,
        fps_den: stream_header.fps_den,
        bundle_sha256: stream_header.bundle_sha256,
    };
    let scene = protocol::Scene {
        header: header.clone(),
        assets: Vec::new(),
        draws: Vec::new(),
    };
    let mut renderer = Renderer::new_with_asset_layout_and_gpu_budget(
        &scene,
        &GpuOptions::default(),
        AssetLayoutMode::Separate,
        gpu_budget.clone(),
    )
    .unwrap();
    let output_gate = Arc::clone(&gate);
    let result = stream_incremental_frames(
        &mut renderer,
        parser,
        initial_header,
        header,
        cpu_budget.clone(),
        gpu_budget.clone(),
        2,
        BlockedOutput(Arc::clone(&gate)),
        false,
        move || {
            let (lock, changed) = &*output_gate;
            let mut state = lock.lock().unwrap();
            state.events.push("output_unblock");
            state.output_unblocked = true;
            changed.notify_all();
        },
    );
    assert!(
        result.is_err(),
        "trailing byte must prevent successful completion"
    );
    let events = gate.0.lock().unwrap().events.clone();
    assert!(
        events.contains(&"writer_blocked"),
        "output must stop during a frame"
    );
    assert!(
        events.contains(&"input_unblock"),
        "input owner must be unblocked"
    );
    assert!(
        events.contains(&"output_unblock"),
        "output owner must be unblocked"
    );
    assert!(
        events.contains(&"writer_return"),
        "blocked writer must return"
    );
    assert!(
        events.contains(&"writer_drop"),
        "writer must be joined and dropped"
    );
    assert!(
        events.contains(&"source_drop"),
        "source worker must be joined and dropped"
    );
    let input_unblock = events
        .iter()
        .position(|event| *event == "input_unblock")
        .unwrap();
    let output_unblock = events
        .iter()
        .position(|event| *event == "output_unblock")
        .unwrap();
    let writer_drop = events
        .iter()
        .position(|event| *event == "writer_drop")
        .unwrap();
    let source_drop = events
        .iter()
        .position(|event| *event == "source_drop")
        .unwrap();
    assert!(
        input_unblock < writer_drop,
        "input hook precedes writer join: {events:?}"
    );
    assert!(
        output_unblock < writer_drop,
        "output hook precedes writer join: {events:?}"
    );
    assert!(
        source_drop < writer_drop,
        "source owner is gone before the scheduler returns: {events:?}"
    );
    assert_eq!(parsed.header.frame_count, 3);
    drop(renderer);
    assert_eq!(cpu_budget.used_bytes(), 0);
    assert_eq!(gpu_budget.used_bytes(), 0);
}

fn assert_actual_cli_parity_case(
    name: &str,
    nct1_bytes: &[u8],
    nct2_bytes: &[u8],
    artifacts: &TestArtifacts,
) -> String {
    let parsed = parse(nct2_bytes).unwrap_or_else(|error| panic!("{name}: invalid NCT2: {error}"));
    let legacy = protocol::read_scene(Cursor::new(nct1_bytes))
        .unwrap_or_else(|error| panic!("{name}: invalid NCT1 reference: {error}"));
    assert_eq!(legacy.header.width, parsed.header.width, "{name}");
    assert_eq!(legacy.header.height, parsed.header.height, "{name}");
    assert_eq!(
        legacy.header.frame_count, parsed.header.frame_count,
        "{name}"
    );
    assert_eq!(legacy.header.fps_num, parsed.header.fps_num, "{name}");
    assert_eq!(legacy.header.fps_den, parsed.header.fps_den, "{name}");
    let streamed_draws: Vec<_> = parsed
        .elements
        .iter()
        .flat_map(|element| element.draws.iter().cloned())
        .collect();
    assert_eq!(legacy.draws, streamed_draws, "{name}: normalized Draws");
    assert_eq!(
        legacy.assets.len(),
        parsed.assets.len(),
        "{name}: asset count"
    );
    for (left, right) in legacy.assets.iter().zip(&parsed.assets) {
        assert_eq!(left.id, right.descriptor.id, "{name}: asset ID");
        assert_eq!(left.width, right.descriptor.width, "{name}: asset width");
        assert_eq!(left.height, right.descriptor.height, "{name}: asset height");
        assert_eq!(
            left.sha256, right.descriptor.declared_sha256,
            "{name}: asset digest"
        );
    }

    let (nct1, nct1_pid) = run_compositor(
        "--stdin",
        nct1_bytes,
        &artifacts.path(&format!("{name}-nct1-report.json")),
    );
    let (nct2, nct2_pid) = run_compositor(
        "--stdin-stream",
        nct2_bytes,
        &artifacts.path(&format!("{name}-nct2-report.json")),
    );
    assert!(
        nct1.status.success(),
        "{name}: NCT1 pid={nct1_pid}: {}",
        String::from_utf8_lossy(&nct1.stderr)
    );
    assert!(
        nct2.status.success(),
        "{name}: NCT2 pid={nct2_pid}: {}",
        String::from_utf8_lossy(&nct2.stderr)
    );
    let nct1_report: serde_json::Value = serde_json::from_slice(
        &std::fs::read(artifacts.path(&format!("{name}-nct1-report.json"))).unwrap(),
    )
    .unwrap();
    let nct2_report: serde_json::Value = serde_json::from_slice(
        &std::fs::read(artifacts.path(&format!("{name}-nct2-report.json"))).unwrap(),
    )
    .unwrap();
    assert_eq!(nct1_report["protocol"], "NCT1", "{name}");
    assert_eq!(nct2_report["protocol"], "NCT2", "{name}");
    assert!(
        nct1_report.get("outputQueueMetrics").is_none(),
        "{name}: NCT1 report schema stays unchanged"
    );
    let queue_metrics = &nct2_report["outputQueueMetrics"];
    let queue_capacity = queue_metrics["capacity"]
        .as_u64()
        .expect("NCT2 reports queue capacity");
    let queue_occupancy = queue_metrics["finalOccupancy"]
        .as_u64()
        .expect("NCT2 reports final queue occupancy");
    let queue_high_water = queue_metrics["highWater"]
        .as_u64()
        .expect("NCT2 reports queue high-water");
    assert_eq!(
        queue_capacity, 2,
        "{name}: configured writer queue capacity"
    );
    assert_eq!(
        queue_occupancy, 0,
        "{name}: completed writer queue is empty"
    );
    assert!(
        queue_high_water <= queue_capacity,
        "{name}: observed queue high-water"
    );
    assert_eq!(
        nct1_report["completedFrames"], parsed.header.frame_count,
        "{name}"
    );
    assert_eq!(
        nct2_report["completedFrames"], parsed.header.frame_count,
        "{name}"
    );
    assert_eq!(
        nct1_report["adapterName"], nct2_report["adapterName"],
        "{name}"
    );
    assert_ne!(
        nct1_report["adapterType"], "Cpu",
        "{name}: must use an actual GPU adapter"
    );
    assert_eq!(
        nct1_report["adapterType"], nct2_report["adapterType"],
        "{name}"
    );
    let expected_done = format!("NICO_DONE {}", parsed.header.frame_count);
    assert!(
        String::from_utf8_lossy(&nct1.stderr).contains(&expected_done),
        "{name}"
    );
    assert!(
        String::from_utf8_lossy(&nct2.stderr).contains(&expected_done),
        "{name}"
    );

    let frame_bytes = parsed.header.width as usize * parsed.header.height as usize * 4;
    let expected_output = frame_bytes * parsed.header.frame_count as usize;
    assert_eq!(
        nct1.stdout.len(),
        expected_output,
        "{name}: NCT1 stdout must be RGBA only"
    );
    assert_eq!(
        nct2.stdout.len(),
        expected_output,
        "{name}: NCT2 stdout must be RGBA only"
    );
    assert_eq!(
        nct1.stdout, nct2.stdout,
        "{name}: every RGBA byte must match"
    );
    eprintln!(
        "T6.10 case={name} adapter={} type={} NCT1 pid={} NCT2 pid={} frames={} RGBA={} bytes parity=exact writer-queue-capacity={} writer-queue-high-water={} writer-queue-final={}",
        nct2_report["adapterName"],
        nct2_report["adapterType"],
        nct1_pid,
        nct2_pid,
        parsed.header.frame_count,
        expected_output,
        queue_capacity,
        queue_high_water,
        queue_occupancy
    );
    nct2_report["adapterName"].as_str().unwrap().to_owned()
}

#[test]
#[ignore = "requires explicit NICO_TIMELINE_STREAM_GPU_TEST=1 and an actual wgpu adapter"]
fn actual_adapter_nct1_and_nct2_cli_full_frame_parity() {
    assert_eq!(
        std::env::var("NICO_TIMELINE_STREAM_GPU_TEST").as_deref(),
        Ok("1"),
        "opt in explicitly with NICO_TIMELINE_STREAM_GPU_TEST=1"
    );
    assert_eq!(
        format!("{:x}", Sha256::digest(LEGACY_NCT1)),
        LEGACY_NCT1_SHA256,
        "pinned legacy NCT1 scene changed"
    );
    let fixed = parse(PARITY).expect("pinned NCT2 parity stream");
    assert_eq!(hex(&fixed.end.scene_commitment), PARITY_SCENE_COMMITMENT);
    assert_eq!(fixed.header.frame_count, 3);
    assert_eq!((fixed.header.fps_num, fixed.header.fps_den), (30, 1));
    assert_eq!(fixed.declarations.len(), 2);
    assert_eq!(
        fixed.elements.iter().map(|e| e.draws.len()).sum::<usize>(),
        2
    );
    let fixed_draws: Vec<_> = fixed.elements.iter().flat_map(|e| &e.draws).collect();
    assert!(fixed_draws
        .iter()
        .any(|draw| draw.anchor_x == 4.25 && draw.speed_x == -0.125));
    let frame_two_vpos = protocol::frame_vpos(2, 30, 1).unwrap();
    assert!(fixed_draws
        .iter()
        .any(|draw| draw.end_vpos == frame_two_vpos));

    let (owner_legacy, owner_stream) = translucent_owner_order_case();
    let owner_scene = protocol::read_scene(Cursor::new(&owner_legacy)).unwrap();
    assert_eq!(owner_scene.draws[0].owner_order, 0);
    assert_eq!(owner_scene.draws[1].owner_order, 1);
    assert_eq!(owner_scene.draws[0].rect, owner_scene.draws[1].rect);
    assert!(owner_scene.draws[0].alpha < 1.0 && owner_scene.draws[1].alpha < 1.0);
    let (rate_legacy, rate_stream) = fractional_60000_1001_case();
    let rate_scene = protocol::read_scene(Cursor::new(&rate_legacy)).unwrap();
    assert_eq!(
        (rate_scene.header.fps_num, rate_scene.header.fps_den),
        (60_000, 1_001)
    );
    assert_eq!(protocol::frame_vpos(3, 60_000, 1_001).unwrap(), 5);

    let artifacts = TestArtifacts::new();
    let adapter = assert_actual_cli_parity_case("fixed-30-1", LEGACY_NCT1, PARITY, &artifacts);
    assert_eq!(
        assert_actual_cli_parity_case(
            "translucent-owner-order",
            &owner_legacy,
            &owner_stream,
            &artifacts
        ),
        adapter
    );
    assert_eq!(
        assert_actual_cli_parity_case(
            "fractional-60000-1001",
            &rate_legacy,
            &rate_stream,
            &artifacts
        ),
        adapter
    );
}

#[test]
fn spawned_parser_transfers_assets_with_credits_and_finishes_after_validation() {
    let mut parser = spawn_stream_parser(Cursor::new(PARITY), || {});
    let mut assets = Vec::new();
    let mut complete = false;
    let mut descriptor_count = 0;
    while let Some(event) = parser.recv().expect("receive parser event") {
        match event.payload.get() {
            nico_compositord::stream_protocol::StreamEvent::Asset(asset) => {
                assets.push((asset.descriptor().id, asset.rgba().len()));
                assert!(parser.budget_used_bytes() >= 48 * 1024 * 1024 + asset.rgba().len());
            }
            nico_compositord::stream_protocol::StreamEvent::Complete(result) => {
                let parsed = result.as_ref().expect("valid stream");
                descriptor_count = parsed.asset_descriptors().len();
                assert_eq!(parsed.elements().len(), parsed.declarations().len());
                complete = true;
            }
        }
        drop(event);
    }
    parser.join().expect("parser thread joined");
    assert!(complete, "completion event");
    assert_eq!(assets.len(), descriptor_count);
    assert_eq!(
        parser.budget_used_bytes(),
        0,
        "all dropped event credits returned"
    );
}

#[test]
fn incremental_parser_events_match_collect_values_and_wire_order() {
    use nico_compositord::stream_protocol::{spawn_incremental_stream_parser, IncrementalEvent};

    let expected = parse(PARITY).expect("collect parser reference");
    let wire_events: Vec<&str> = record_offsets(PARITY)
        .into_iter()
        .filter_map(|(_, kind, _)| match kind {
            1 => Some("header"),
            2 => Some("declarations"),
            6 => Some("asset"),
            7 => Some("element"),
            8 => Some("watermark"),
            9 => Some("complete"),
            _ => None,
        })
        .collect();
    let wire_watermarks: Vec<(u32, i64)> = record_offsets(PARITY)
        .into_iter()
        .filter(|(_, kind, _)| *kind == 8)
        .map(|(offset, _, _)| {
            (
                u32::from_le_bytes(PARITY[offset + 16..offset + 20].try_into().unwrap()),
                i64::from_le_bytes(PARITY[offset + 20..offset + 28].try_into().unwrap()),
            )
        })
        .collect();

    let mut parser = spawn_incremental_stream_parser(Cursor::new(PARITY), || {});
    let mut observed_order = Vec::new();
    let mut asset_index = 0;
    let mut element_index = 0;
    let mut watermark_index = 0;
    let mut completed = false;
    while let Some(event) = parser.recv().expect("receive stream event") {
        assert!(
            event.payload.reserved_bytes() > 0,
            "every event owns a CPU credit"
        );
        match event.payload.get() {
            IncrementalEvent::Header(header) => {
                observed_order.push("header");
                assert_eq!(header, &expected.header);
            }
            IncrementalEvent::Declarations(declarations) => {
                observed_order.push("declarations");
                assert_eq!(declarations, &expected.declarations);
            }
            IncrementalEvent::Asset(asset) => {
                observed_order.push("asset");
                let reference = &expected.assets[asset_index];
                assert_eq!(asset.descriptor(), &reference.descriptor);
                assert_eq!(asset.rgba(), reference.rgba);
                asset_index += 1;
            }
            IncrementalEvent::Element(element) => {
                observed_order.push("element");
                assert_eq!(element, &expected.elements[element_index]);
                element_index += 1;
            }
            IncrementalEvent::Watermark { prefix, ready_vpos } => {
                observed_order.push("watermark");
                assert_eq!((*prefix, *ready_vpos), wire_watermarks[watermark_index]);
                watermark_index += 1;
            }
            IncrementalEvent::Complete(result) => {
                observed_order.push("complete");
                let complete = result.as_ref().expect("End and EOF validated");
                assert_eq!(complete.header(), &expected.header);
                assert_eq!(complete.declarations(), &expected.declarations);
                assert_eq!(complete.elements(), &expected.elements);
                assert_eq!(complete.end(), &expected.end);
                completed = true;
            }
        }
        drop(event);
    }
    parser.join().expect("parser joins after stream completion");
    assert_eq!(observed_order, wire_events);
    assert_eq!(asset_index, expected.assets.len());
    assert_eq!(element_index, expected.elements.len());
    assert_eq!(watermark_index, wire_watermarks.len());
    assert!(completed);
    assert_eq!(
        parser.budget_used_bytes(),
        0,
        "all event and metadata credits return"
    );
}

#[test]
fn parser_try_and_timed_receive_report_empty_timeout_and_closed() {
    use std::io;
    use std::sync::{Arc, Condvar, Mutex};
    use std::time::Duration;

    use nico_compositord::stream_protocol::{spawn_incremental_stream_parser, ParserReceive};

    struct BlockedReader {
        gate: Arc<(Mutex<bool>, Condvar)>,
    }

    impl std::io::Read for BlockedReader {
        fn read(&mut self, _bytes: &mut [u8]) -> io::Result<usize> {
            let (lock, changed) = &*self.gate;
            let mut released = lock.lock().unwrap();
            while !*released {
                released = changed.wait(released).unwrap();
            }
            Ok(0)
        }
    }

    let gate = Arc::new((Mutex::new(false), Condvar::new()));
    let unblock_gate = Arc::clone(&gate);
    let mut parser = spawn_incremental_stream_parser(BlockedReader { gate }, move || {
        let (lock, changed) = &*unblock_gate;
        *lock.lock().unwrap() = true;
        changed.notify_all();
    });
    assert!(matches!(parser.try_recv().unwrap(), ParserReceive::Empty));
    assert!(matches!(
        parser.recv_timeout(Duration::from_millis(20)).unwrap(),
        ParserReceive::TimedOut
    ));
    parser.cancel();
    assert!(matches!(parser.try_recv().unwrap(), ParserReceive::Closed));
    parser
        .join()
        .expect("cancelled parser joins after source unblocks");
    assert_eq!(
        parser.budget_used_bytes(),
        0,
        "cancel clears queued event credits"
    );
}

#[test]
fn incremental_parser_delivers_validation_error_then_closes() {
    use nico_compositord::stream_protocol::{spawn_incremental_stream_parser, IncrementalEvent};

    let mut parser = spawn_incremental_stream_parser(Cursor::new(Vec::<u8>::new()), || {});
    let event = parser
        .recv()
        .expect("receive terminal validation error")
        .expect("error event before close");
    assert!(event.payload.reserved_bytes() > 0);
    match event.payload.get() {
        IncrementalEvent::Complete(Err(error)) => {
            assert!(error.to_string().contains("missing NCT2 Header"));
        }
        other => panic!("expected Complete(Err), got {other:?}"),
    }
    drop(event);
    assert!(parser.recv().expect("terminal queue status").is_none());
    parser.join().expect("failed parser worker joins");
    assert_eq!(parser.budget_used_bytes(), 0);
}

#[test]
fn incremental_parser_waits_for_error_event_credit_before_closing() {
    use std::io;
    use std::sync::mpsc;
    use std::sync::{Arc, Condvar, Mutex};
    use std::time::Duration;

    use nico_compositord::stream_budget::{CpuBudget, CPU_BUDGET_BYTES};
    use nico_compositord::stream_protocol::{
        spawn_incremental_stream_parser_with_budget, IncrementalEvent, ParserReceive,
    };

    struct GatedEOF {
        prefix: Vec<u8>,
        position: usize,
        gate: Arc<(Mutex<bool>, Condvar)>,
        at_eof_tx: Option<mpsc::Sender<()>>,
    }

    impl std::io::Read for GatedEOF {
        fn read(&mut self, output: &mut [u8]) -> io::Result<usize> {
            if self.position < self.prefix.len() {
                let count = output.len().min(self.prefix.len() - self.position);
                output[..count].copy_from_slice(&self.prefix[self.position..self.position + count]);
                self.position += count;
                return Ok(count);
            }
            if let Some(tx) = self.at_eof_tx.take() {
                tx.send(()).expect("test is waiting for the source gate");
            }
            let (lock, changed) = &*self.gate;
            let mut released = lock.lock().unwrap();
            while !*released {
                released = changed.wait(released).unwrap();
            }
            Ok(0)
        }
    }

    let prefix_end = record_offsets(PARITY)
        .into_iter()
        .find(|(_, kind, _)| *kind == 3)
        .map(|(offset, _, length)| offset + 16 + length)
        .expect("DeclarationsComplete record")
        .min(PARITY.len());
    let (eof_tx, eof_rx) = mpsc::channel();
    let gate = Arc::new((Mutex::new(false), Condvar::new()));
    let reader = GatedEOF {
        prefix: PARITY[..prefix_end].to_vec(),
        position: 0,
        gate: Arc::clone(&gate),
        at_eof_tx: Some(eof_tx),
    };
    let budget = CpuBudget::new();
    let mut parser = spawn_incremental_stream_parser_with_budget(reader, budget.clone(), || {})
        .expect("start parser with shared budget");

    for expected in ["header", "declarations"] {
        let event = parser
            .recv_timeout(Duration::from_secs(1))
            .expect("receive prefix event");
        let ParserReceive::Event(event) = event else {
            panic!("{expected} event should arrive before source EOF");
        };
        match (expected, event.payload.get()) {
            ("header", IncrementalEvent::Header(_))
            | ("declarations", IncrementalEvent::Declarations(_)) => {}
            (_, other) => panic!("expected {expected}, received {other:?}"),
        }
        drop(event);
    }
    eof_rx
        .recv_timeout(Duration::from_secs(1))
        .expect("parser reached gated EOF after validated declarations");

    let held = budget
        .try_reserve(CPU_BUDGET_BYTES - budget.used_bytes())
        .expect("fill remaining shared CPU budget");
    let (lock, changed) = &*gate;
    *lock.lock().unwrap() = true;
    changed.notify_all();

    assert!(
        matches!(
            parser.recv_timeout(Duration::from_millis(80)).unwrap(),
            ParserReceive::TimedOut
        ),
        "the worker must remain pending while terminal-event credit is unavailable"
    );
    drop(held);

    let terminal = parser
        .recv_timeout(Duration::from_secs(1))
        .expect("receive terminal error after budget release");
    let ParserReceive::Event(terminal) = terminal else {
        panic!("Complete(Err) must follow release of the held CPU credit");
    };
    match terminal.payload.get() {
        IncrementalEvent::Complete(Err(error)) => {
            assert!(error.to_string().contains("EOF before End"));
        }
        other => panic!("expected Complete(Err), got {other:?}"),
    }
    drop(terminal);
    assert!(parser
        .recv()
        .expect("closed after terminal error")
        .is_none());
    parser.join().expect("worker joined after terminal error");
    assert_eq!(
        budget.used_bytes(),
        0,
        "all error and metadata credits return"
    );
}

#[test]
fn dropping_parser_unblocks_and_joins_caller_cancellable_source() {
    use std::io;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::{Arc, Condvar, Mutex};

    struct Gate {
        flags: Mutex<(bool, bool)>, // (reader started, unblocked)
        changed: Condvar,
    }

    struct BlockedSource {
        gate: Arc<Gate>,
        worker_dropped: Arc<AtomicBool>,
    }
    impl Drop for BlockedSource {
        fn drop(&mut self) {
            self.worker_dropped.store(true, Ordering::Release);
        }
    }
    impl std::io::Read for BlockedSource {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            let mut flags = self.gate.flags.lock().unwrap();
            flags.0 = true;
            self.gate.changed.notify_all();
            while !flags.1 {
                flags = self.gate.changed.wait(flags).unwrap();
            }
            if !buf.is_empty() {
                buf[0] = 0;
                Ok(1)
            } else {
                Ok(0)
            }
        }
    }

    let gate = Arc::new(Gate {
        flags: Mutex::new((false, false)),
        changed: Condvar::new(),
    });
    let worker_dropped = Arc::new(AtomicBool::new(false));
    let unblock = Arc::clone(&gate);
    let parser = spawn_stream_parser(
        BlockedSource {
            gate: Arc::clone(&gate),
            worker_dropped: Arc::clone(&worker_dropped),
        },
        move || {
            let mut flags = unblock.flags.lock().unwrap();
            flags.1 = true;
            unblock.changed.notify_all();
        },
    );
    let mut flags = gate.flags.lock().unwrap();
    while !flags.0 {
        flags = gate.changed.wait(flags).unwrap();
    }
    drop(flags);
    drop(parser);
    assert!(
        worker_dropped.load(Ordering::Acquire),
        "drop joins the source-owning worker"
    );
}

#[test]
fn parser_failure_is_delivered_before_receiver_closes() {
    let mut parser = spawn_stream_parser(Cursor::new(b"invalid".as_slice()), || {});
    let event = parser
        .recv()
        .expect("receive failure event")
        .expect("terminal event");
    match event.payload.get() {
        nico_compositord::stream_protocol::StreamEvent::Complete(Err(error)) => {
            assert!(error.to_string().contains("NCT2"));
        }
        other => panic!("expected parser failure, got {other:?}"),
    }
    drop(event);
    assert!(parser.recv().expect("closed receive").is_none());
    parser.join().expect("failed parser thread joined");
    assert_eq!(parser.budget_used_bytes(), 0);
}

fn record_offsets(bytes: &[u8]) -> Vec<(usize, u32, usize)> {
    let mut records = Vec::new();
    let mut offset = 0;
    while offset + 16 <= bytes.len() {
        let kind = u32::from_le_bytes(bytes[offset..offset + 4].try_into().unwrap());
        let length = u32::from_le_bytes(bytes[offset + 4..offset + 8].try_into().unwrap()) as usize;
        records.push((offset, kind, length));
        offset += 16 + length;
        if offset > bytes.len() {
            break;
        }
    }
    records
}

fn payload_offset(bytes: &[u8], record_index: usize) -> usize {
    record_offsets(bytes)[record_index].0 + 16
}

fn set_u32(bytes: &mut [u8], offset: usize, value: u32) {
    bytes[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
}

fn set_u64(bytes: &mut [u8], offset: usize, value: u64) {
    bytes[offset..offset + 8].copy_from_slice(&value.to_le_bytes());
}

fn record_offset(bytes: &[u8], kind: u32) -> usize {
    record_offsets(bytes)
        .iter()
        .find(|(_, record_kind, _)| *record_kind == kind)
        .unwrap_or_else(|| panic!("missing NCT2 record kind {kind}"))
        .0
}

fn set_first_asset_metadata(bytes: &mut [u8], width: u32, height: u32, raw_len: u64) {
    let begin = record_offset(bytes, 4) + 16;
    set_u32(bytes, begin + 4, width);
    set_u32(bytes, begin + 8, height);
    set_u64(bytes, begin + 12, raw_len);

    // Keep the End total consistent so the malformed AssetBegin metadata is
    // the reason parsing must fail, rather than a downstream count mismatch.
    let end = record_offset(bytes, 9) + 16;
    set_u64(bytes, end + 12, raw_len + 4);
}

fn append_record(stream: &mut Vec<u8>, kind: u32, sequence: u64, payload: &[u8]) {
    stream.extend_from_slice(&kind.to_le_bytes());
    stream.extend_from_slice(&(payload.len() as u32).to_le_bytes());
    stream.extend_from_slice(&sequence.to_le_bytes());
    stream.extend_from_slice(payload);
}

fn parity_with_8mib_first_asset(final_chunk_len: usize, received_bytes: u64) -> Vec<u8> {
    const CHUNK_BYTES: usize = 4 * 1024 * 1024;
    const RAW_BYTES: u64 = 8 * 1024 * 1024;

    let mut stream = Vec::new();
    let mut sequence = 0u64;
    let mut first_begin = true;
    let mut first_chunk = true;
    let mut first_asset_end = true;

    for (offset, kind, payload_len) in record_offsets(PARITY) {
        let payload_start = offset + 16;
        let payload_end = payload_start + payload_len;
        let mut payload = PARITY[payload_start..payload_end].to_vec();

        match kind {
            4 if first_begin => {
                first_begin = false;
                set_u32(&mut payload, 4, 4_096);
                set_u32(&mut payload, 8, 512);
                set_u64(&mut payload, 12, RAW_BYTES);
            }
            5 if first_chunk => {
                first_chunk = false;
                let asset_id = u32::from_le_bytes(payload[0..4].try_into().unwrap());
                let mut first_payload = Vec::with_capacity(12 + CHUNK_BYTES);
                first_payload.extend_from_slice(&asset_id.to_le_bytes());
                first_payload.extend_from_slice(&0u64.to_le_bytes());
                first_payload.resize(12 + CHUNK_BYTES, 0x5a);
                append_record(&mut stream, 5, sequence, &first_payload);
                sequence += 1;

                let mut final_payload = Vec::with_capacity(12 + final_chunk_len);
                final_payload.extend_from_slice(&asset_id.to_le_bytes());
                final_payload.extend_from_slice(&(CHUNK_BYTES as u64).to_le_bytes());
                final_payload.resize(12 + final_chunk_len, 0xa5);
                append_record(&mut stream, 5, sequence, &final_payload);
                sequence += 1;
                continue;
            }
            6 if first_asset_end => {
                first_asset_end = false;
                set_u64(&mut payload, 4, received_bytes);
            }
            9 => {
                set_u64(&mut payload, 12, RAW_BYTES + 4);
            }
            _ => {}
        }

        append_record(&mut stream, kind, sequence, &payload);
        sequence += 1;
    }

    assert!(!first_begin && !first_chunk && !first_asset_end);
    recompute_test_asset_hashes_and_commitment(&mut stream);
    stream
}

fn parity_with_negative_start_vector() -> Vec<u8> {
    let vectors: serde_json::Value = serde_json::from_str(READINESS_VECTORS).unwrap();
    let vector = vectors["ready_vectors"]
        .as_array()
        .unwrap()
        .iter()
        .find(|vector| vector["name"] == "negative-start-is-clipped-only-for-readiness")
        .unwrap();
    let mut stream = PARITY.to_vec();

    let header = record_offset(&stream, 1) + 16;
    set_u32(&mut stream, header + 20, 3);
    set_u32(&mut stream, header + 24, 1);
    set_u32(&mut stream, header + 28, 1);

    let declarations = record_offset(&stream, 2) + 16;
    let clipped = vector["clipped_intervals"].as_array().unwrap();
    for (ordinal, interval) in clipped.iter().enumerate() {
        let entry = declarations + 4 + ordinal * 20;
        set_u32(
            &mut stream,
            entry + 12,
            interval[0].as_i64().unwrap() as i32 as u32,
        );
        set_u32(
            &mut stream,
            entry + 16,
            interval[1].as_i64().unwrap() as i32 as u32,
        );
    }
    let declaration_len = u32::from_le_bytes(
        stream[record_offset(&stream, 2) + 4..record_offset(&stream, 2) + 8]
            .try_into()
            .unwrap(),
    ) as usize;
    let declaration_sha = Sha256::digest(&stream[declarations + 4..declarations + declaration_len]);
    let declarations_complete = record_offset(&stream, 3) + 16;
    stream[declarations_complete + 4..declarations_complete + 36].copy_from_slice(&declaration_sha);

    let original = vector["draw_intervals_remain"].as_array().unwrap();
    let mut draw_index = 0;
    for (offset, kind, payload_len) in record_offsets(&stream) {
        if kind != 7 {
            continue;
        }
        let ordinal = u32::from_le_bytes(stream[offset + 16..offset + 20].try_into().unwrap());
        let payload = offset + 16;
        let draw_start = payload + 8;
        let interval = &original[ordinal as usize];
        set_u32(
            &mut stream,
            draw_start + 4,
            interval[0].as_i64().unwrap() as i32 as u32,
        );
        set_u32(
            &mut stream,
            draw_start + 8,
            interval[1].as_i64().unwrap() as i32 as u32,
        );
        draw_index += 1;
        assert_eq!(payload_len, 8 + 128);
    }
    assert_eq!(draw_index, 2);

    let ready = vector["ready_by_prefix"].as_array().unwrap();
    let mut prefix = 0usize;
    for (offset, kind, _) in record_offsets(&stream) {
        if kind == 8 {
            prefix += 1;
            set_u64(&mut stream, offset + 20, ready[prefix].as_u64().unwrap());
        }
    }
    let end = record_offset(&stream, 9) + 16;
    set_u64(
        &mut stream,
        end + 24,
        vector["exclusive_end"].as_u64().unwrap(),
    );
    recompute_test_asset_hashes_and_commitment(&mut stream);
    stream
}

fn wire_asset_pixels(bytes: &[u8]) -> Vec<(u32, Vec<u8>)> {
    let mut assets = Vec::new();
    let mut open: Option<(u32, Vec<u8>)> = None;
    for (offset, kind, payload_len) in record_offsets(bytes) {
        let start = offset + 16;
        match kind {
            4 => {
                let id = u32::from_le_bytes(bytes[start..start + 4].try_into().unwrap());
                assert!(open.replace((id, Vec::new())).is_none());
            }
            5 => {
                let (id, rgba) = open.as_mut().expect("AssetChunk follows AssetBegin");
                assert_eq!(
                    *id,
                    u32::from_le_bytes(bytes[start..start + 4].try_into().unwrap())
                );
                rgba.extend_from_slice(&bytes[start + 12..start + payload_len]);
            }
            6 => {
                assets.push(open.take().expect("AssetEnd follows AssetBegin"));
            }
            _ => {}
        }
    }
    assert!(open.is_none());
    assets
}

fn recompute_test_asset_hashes_and_commitment(stream: &mut [u8]) {
    let offsets = record_offsets(stream);
    let mut descriptors = Vec::<(u32, u32, u32, u64, Vec<u8>, [u8; 32], usize)>::new();
    let mut open_asset: Option<usize> = None;
    for (offset, kind, payload_len) in &offsets {
        let payload = *offset + 16;
        match *kind {
            4 => {
                let mut sha = [0; 32];
                sha.copy_from_slice(&stream[payload + 20..payload + 52]);
                descriptors.push((
                    u32::from_le_bytes(stream[payload..payload + 4].try_into().unwrap()),
                    u32::from_le_bytes(stream[payload + 4..payload + 8].try_into().unwrap()),
                    u32::from_le_bytes(stream[payload + 8..payload + 12].try_into().unwrap()),
                    u64::from_le_bytes(stream[payload + 12..payload + 20].try_into().unwrap()),
                    Vec::new(),
                    sha,
                    payload,
                ));
                open_asset = Some(descriptors.len() - 1);
            }
            5 => {
                let asset_id = u32::from_le_bytes(stream[payload..payload + 4].try_into().unwrap());
                let descriptor_index = open_asset.expect("chunk follows AssetBegin");
                assert_eq!(descriptors[descriptor_index].0, asset_id);
                let data_len = *payload_len - 12;
                let data_start = payload + 12;
                let data = &stream[data_start..data_start + data_len];
                descriptors[descriptor_index].4.extend_from_slice(data);
            }
            6 => {
                let descriptor_index = open_asset.take().expect("AssetEnd follows AssetBegin");
                let asset_id = u32::from_le_bytes(stream[payload..payload + 4].try_into().unwrap());
                assert_eq!(descriptors[descriptor_index].0, asset_id);
                let sha: [u8; 32] = Sha256::digest(&descriptors[descriptor_index].4).into();
                descriptors[descriptor_index].5 = sha;
                stream[payload + 12..payload + 44].copy_from_slice(&sha);
                let begin = descriptors[descriptor_index].6;
                stream[begin + 20..begin + 52].copy_from_slice(&sha);
            }
            _ => {}
        }
    }
    assert!(open_asset.is_none());

    let end_offset = offsets.iter().find(|(_, kind, _)| *kind == 9).unwrap().0;
    let end_payload = end_offset + 16;
    let draw_count = u32::from_le_bytes(
        stream[end_payload + 8..end_payload + 12]
            .try_into()
            .unwrap(),
    );
    let total_raw = u64::from_le_bytes(
        stream[end_payload + 12..end_payload + 20]
            .try_into()
            .unwrap(),
    );
    let nct2_header = &stream[offsets[0].0 + 16..offsets[0].0 + 16 + offsets[0].2];
    let mut canonical_header = [0u8; 80];
    canonical_header[0..4].copy_from_slice(b"NCT1");
    canonical_header[4..8].copy_from_slice(&80u32.to_le_bytes());
    canonical_header[8..12].copy_from_slice(&nct2_header[12..16]);
    canonical_header[12..16].copy_from_slice(&nct2_header[16..20]);
    canonical_header[16..20].copy_from_slice(&nct2_header[20..24]);
    canonical_header[20..24].copy_from_slice(&nct2_header[24..28]);
    canonical_header[24..28].copy_from_slice(&nct2_header[28..32]);
    canonical_header[28..32].copy_from_slice(&(descriptors.len() as u32).to_le_bytes());
    canonical_header[32..36].copy_from_slice(&draw_count.to_le_bytes());
    canonical_header[36..68].copy_from_slice(&nct2_header[40..72]);
    canonical_header[68..76].copy_from_slice(&total_raw.to_le_bytes());
    canonical_header[76..80].copy_from_slice(&3u32.to_le_bytes());

    descriptors.sort_by_key(|asset| asset.0);
    let mut hasher = Sha256::new();
    hasher.update(b"NCT2-SCENE-DIGEST-v1\0");
    hasher.update(canonical_header);
    for (id, width, height, raw_len, _, sha, _) in descriptors {
        hasher.update(id.to_le_bytes());
        hasher.update(width.to_le_bytes());
        hasher.update(height.to_le_bytes());
        hasher.update(raw_len.to_le_bytes());
        hasher.update(sha);
    }
    for (offset, kind, payload_len) in offsets {
        if kind == 7 {
            let start = offset + 16 + 8;
            let end = offset + 16 + payload_len;
            hasher.update(&stream[start..end]);
        }
    }
    stream[end_payload + 32..end_payload + 64].copy_from_slice(&hasher.finalize());
}

#[test]
fn reads_all_three_fixed_stream_goldens() {
    let empty = parse(EMPTY).expect("empty-scene golden");
    assert_eq!(empty.header.width, 33);
    assert_eq!(empty.header.height, 19);
    assert_eq!(empty.header.frame_count, 3);
    assert!(empty.declarations.is_empty());
    assert!(empty.elements.is_empty());
    assert_eq!(empty.end.final_prefix, 0);
    assert_eq!(empty.end.final_ready_vpos, 10);
    assert_eq!(
        empty.end.scene_commitment,
        hex32("7cebd6ff85b74779fbd1e897e4eab1645607652310381797183d50d7a8875e18")
    );

    let parity = parse(PARITY).expect("NCT1 parity NCT2 golden");
    assert_eq!(parity.header.width, 33);
    assert_eq!(parity.header.height, 19);
    assert_eq!(parity.declarations.len(), 2);
    assert_eq!(parity.declarations[0].ordinal, 0);
    assert_eq!(parity.declarations[0].owner_order, 0);
    assert_eq!(parity.declarations[0].comment_index, 0);
    assert_eq!(parity.declarations[0].clipped_start, 0);
    assert_eq!(parity.declarations[0].clipped_end, 10);
    assert_eq!(parity.elements.len(), 2);
    assert_eq!(parity.elements[0].draws.len(), 1);
    assert_eq!(parity.elements[1].draws.len(), 1);
    assert_eq!(parity.elements[0].draws[0].start_vpos, -4);
    assert_eq!(parity.elements[0].draws[0].alpha, 1.0);
    assert_eq!(parity.end.draw_count, 2);
    assert_eq!(parity.end.final_prefix, 2);
    assert_eq!(parity.end.final_ready_vpos, 10);
    assert_eq!(parity.asset_descriptors.len(), 2);
    assert_eq!(parity.asset_descriptors[0].width, 1);
    assert_eq!(parity.asset_descriptors[0].height, 1);
    assert_eq!(parity.asset_descriptors[0].raw_len, 4);
    assert_ne!(parity.asset_descriptors[0].declared_sha256, [0; 32]);

    let inversion = parse(OWNER_INVERSION).expect("owner/time inversion golden");
    assert_eq!(inversion.declarations.len(), 3);
    assert!(inversion
        .elements
        .iter()
        .all(|element| element.draws.is_empty()));
    assert_eq!(inversion.end.final_prefix, 3);
    assert_eq!(inversion.end.final_ready_vpos, 300);
}

#[test]
fn parity_stream_normalizes_to_legacy_nct1_golden() {
    let parity = parse(PARITY).expect("NCT1 parity NCT2 golden");
    let normalized = protocol::Scene {
        header: protocol::Header {
            width: parity.header.width,
            height: parity.header.height,
            frame_count: parity.header.frame_count,
            fps_num: parity.header.fps_num,
            fps_den: parity.header.fps_den,
            bundle_sha256: parity.header.bundle_sha256,
        },
        assets: parity
            .assets
            .iter()
            .map(|asset| protocol::Asset {
                id: asset.descriptor.id,
                width: asset.descriptor.width,
                height: asset.descriptor.height,
                sha256: asset.descriptor.declared_sha256,
                rgba: asset.rgba.clone(),
            })
            .collect(),
        draws: parity
            .elements
            .iter()
            .flat_map(|element| element.draws.iter().cloned())
            .collect(),
    };
    let legacy = protocol::read_scene(Cursor::new(LEGACY_NCT1)).expect("legacy NCT1 golden");
    assert_eq!(normalized, legacy);
    assert_eq!(
        Sha256::digest(LEGACY_NCT1).as_slice(),
        hex32("d506db27b68a294e8e8a9d5db1f512d274da8b2c62543b5213c121e26f379948")
    );
    assert_eq!(
        parity.end.scene_commitment,
        hex32("c3f6fa5024466409879965d7cc0471aa33d5d3fb1b5842185bee5e923f785350")
    );
}

#[test]
fn real_snapshot_readiness_fixture_matches_provenance_and_exact_clock() {
    let fixture: serde_json::Value = serde_json::from_str(REAL_READINESS).expect("readiness JSON");
    let provenance = &fixture["provenance_hashes"];
    for (key, expected) in [
        (
            "snapshot_sha256",
            "b7be86d86016175c99925f5f1b5651d43279eeffcc54deb1c22c5b2e6e15e946",
        ),
        (
            "timeline_capture_go_sha256",
            "0641be8fcbb0b183842c6f5e348d8715b0058af935d27e34d63dca2ad39ccf03",
        ),
        (
            "timeline_js_sha256",
            "c34e740f2a4bb91bfc9dc090cb9e484277e3dd4c11b4a6f0fa151d6c2c0119e0",
        ),
        (
            "timeline_stream_format_go_sha256",
            "16640b04ac06a5dae1c6f02b9c42c016a7651c3971d07e8252a8787c91da7597",
        ),
        (
            "renderer_bundle_sha256",
            "d62f58ae0bd045eb86c2e116eefaec34e46e9b53c2f710656efae9e79252fee7",
        ),
    ] {
        assert_eq!(provenance[key].as_str(), Some(expected), "provenance {key}");
    }

    let render = &fixture["render"];
    assert_eq!(render["width"].as_u64(), Some(1920));
    assert_eq!(render["height"].as_u64(), Some(1080));
    assert_eq!(render["duration_ms"].as_i64(), Some(6000));
    assert_eq!(render["fps_num"].as_i64(), Some(30));
    assert_eq!(render["fps_den"].as_i64(), Some(1));
    assert_eq!(render["frame_count"].as_i64(), Some(180));
    assert_eq!(render["seed"].as_u64(), Some(0x4e49434f));

    let frame_count = 180u32;
    let fps_num = 30u32;
    let fps_den = 1u32;
    let exclusive_end = readiness_frame_vpos(frame_count, fps_num, fps_den);
    assert_eq!(exclusive_end, 600);

    let declarations = fixture["declarations"]
        .as_array()
        .expect("declarations array");
    assert_eq!(declarations.len(), 72);
    let mut starts = Vec::with_capacity(declarations.len());
    let mut previous = None;
    for declaration in declarations {
        let owner = declaration["owner_order"].as_u64().expect("owner_order") as u32;
        let index = declaration["comment_index"]
            .as_u64()
            .expect("comment_index") as u32;
        let start = declaration["start_vpos"].as_i64().expect("start_vpos");
        let end = declaration["end_vpos"].as_i64().expect("end_vpos");
        assert!(
            (0..=exclusive_end).contains(&start),
            "clipped start {start}"
        );
        assert!((0..=exclusive_end).contains(&end), "clipped end {end}");
        assert!(start <= end, "invalid clipped interval [{start},{end})");
        if let Some((previous_owner, previous_index)) = previous {
            assert!(
                (owner, index) > (previous_owner, previous_index),
                "declarations are not in strict owner/index order"
            );
        }
        previous = Some((owner, index));
        starts.push(start);
    }

    let ready_by_prefix = fixture["ready_by_prefix"]
        .as_array()
        .expect("ready_by_prefix array");
    let available_frames = fixture["available_frames_by_prefix"]
        .as_array()
        .expect("available_frames_by_prefix array");
    assert_eq!(ready_by_prefix.len(), declarations.len() + 1);
    assert_eq!(available_frames.len(), declarations.len() + 1);
    for prefix in 0..=declarations.len() {
        let ready = starts[prefix..]
            .iter()
            .copied()
            .min()
            .unwrap_or(exclusive_end);
        assert_eq!(
            ready_by_prefix[prefix].as_i64(),
            Some(ready),
            "ready vpos at prefix {prefix}"
        );
        let count = (0..frame_count)
            .filter(|frame| readiness_frame_vpos(*frame, fps_num, fps_den) < ready)
            .count() as u64;
        assert_eq!(
            available_frames[prefix].as_u64(),
            Some(count),
            "strictly available frame count at prefix {prefix}"
        );
    }
}

fn readiness_frame_vpos(frame: u32, fps_num: u32, fps_den: u32) -> i64 {
    (u64::from(frame) * u64::from(fps_den) * 100 / u64::from(fps_num)) as i64
}

#[test]
fn retains_exact_rgba_pixels_from_all_static_goldens() {
    for golden in [EMPTY, PARITY, OWNER_INVERSION] {
        let parsed = parse(golden).expect("valid fixed NCT2 golden");
        let wire_pixels = wire_asset_pixels(golden);
        assert_eq!(parsed.assets.len(), wire_pixels.len());
        for (asset, (id, rgba)) in parsed.assets.iter().zip(wire_pixels) {
            assert_eq!(asset.descriptor.id, id);
            assert_eq!(asset.rgba, rgba);
        }
    }
}

fn hex32(hex: &str) -> [u8; 32] {
    let mut result = [0; 32];
    for (index, byte) in result.iter_mut().enumerate() {
        *byte = u8::from_str_radix(&hex[index * 2..index * 2 + 2], 16).unwrap();
    }
    result
}

#[test]
fn rejects_every_truncation_and_trailing_byte_for_each_golden() {
    for (name, golden) in [
        ("empty", EMPTY),
        ("parity", PARITY),
        ("owner inversion", OWNER_INVERSION),
    ] {
        for length in 0..golden.len() {
            assert!(
                parse(&golden[..length]).is_err(),
                "{name} accepted {length} bytes"
            );
        }
        let mut trailing = golden.to_vec();
        trailing.push(0);
        assert!(parse(&trailing).is_err(), "{name} accepted a trailing byte");
    }
}

#[test]
fn rejects_unknown_types_bad_sequence_and_wrong_record_order() {
    let mut unknown_type = EMPTY.to_vec();
    set_u32(&mut unknown_type, 0, 99);
    assert!(parse(&unknown_type).is_err());

    let mut bad_first_sequence = EMPTY.to_vec();
    set_u64(&mut bad_first_sequence, 8, 1);
    assert!(parse(&bad_first_sequence).is_err());

    let mut skipped_sequence = EMPTY.to_vec();
    let second = record_offsets(&skipped_sequence)[1].0;
    set_u64(&mut skipped_sequence, second + 8, 2);
    assert!(parse(&skipped_sequence).is_err());

    let mut wrong_order = EMPTY.to_vec();
    set_u32(&mut wrong_order, 0, 2);
    assert!(parse(&wrong_order).is_err());
}

#[test]
fn rejects_invalid_header_fields() {
    for (offset, value) in [
        (8, 3),  // unsupported version
        (36, 0), // flags must be 3
        (72, 1), // reserved bytes must be zero
        (12, 0), // width must be positive
        (24, 0), // FPS numerator must be positive
    ] {
        let mut bytes = EMPTY.to_vec();
        let header_payload = payload_offset(&bytes, 0);
        set_u32(&mut bytes, header_payload + offset, value);
        assert!(
            parse(&bytes).is_err(),
            "accepted invalid Header field at {offset}"
        );
    }
}

#[test]
fn rejects_oversized_record_length_before_payload_read() {
    let mut oversized = EMPTY.to_vec();
    set_u32(&mut oversized, 4, MAX_RECORD_PAYLOAD + 1);
    assert!(parse(&oversized).is_err());
}

#[test]
fn rejects_bad_declaration_digest_and_entry_order() {
    let mut bad_digest = EMPTY.to_vec();
    let complete = record_offsets(&bad_digest)
        .iter()
        .find(|(_, kind, _)| *kind == 3)
        .unwrap()
        .0;
    bad_digest[complete + 16 + 4] ^= 0xff;
    assert!(parse(&bad_digest).is_err());

    let mut bad_ordinal = PARITY.to_vec();
    let declarations = payload_offset(&bad_ordinal, 1);
    set_u32(&mut bad_ordinal, declarations + 4, 1);
    assert!(parse(&bad_ordinal).is_err());

    let mut bad_owner_order = PARITY.to_vec();
    let declarations = payload_offset(&bad_owner_order, 1);
    set_u32(&mut bad_owner_order, declarations + 4 + 20 + 4, 0);
    set_u32(&mut bad_owner_order, declarations + 4 + 20 + 8, 0);
    assert!(parse(&bad_owner_order).is_err());

    let mut negative_clipped_start = PARITY.to_vec();
    let declarations = payload_offset(&negative_clipped_start, 1);
    set_u32(
        &mut negative_clipped_start,
        declarations + 4 + 12,
        (-1i32) as u32,
    );
    assert!(parse(&negative_clipped_start).is_err());
}

#[test]
fn rejects_invalid_draw_owner_interval_primitive_and_scalars() {
    for mutation in [0, 1, 2, 3] {
        let mut bytes = PARITY.to_vec();
        let element = record_offsets(&bytes)
            .iter()
            .find(|(_, kind, _)| *kind == 7)
            .unwrap()
            .0;
        let draw = element + 16 + 8;
        match mutation {
            0 => set_u32(&mut bytes, draw + 16, 99),
            1 => set_u32(&mut bytes, draw + 8, 0),
            2 => set_u32(&mut bytes, draw + 24, 1),
            _ => bytes[draw + 108..draw + 112].copy_from_slice(&f32::NAN.to_bits().to_le_bytes()),
        }
        assert!(
            parse(&bytes).is_err(),
            "accepted malformed draw mutation {mutation}"
        );
    }
}

#[test]
fn rejects_element_ordinal_gap() {
    let mut bytes = PARITY.to_vec();
    let element = record_offsets(&bytes)
        .iter()
        .find(|(_, kind, _)| *kind == 7)
        .unwrap()
        .0;
    set_u32(&mut bytes, element + 16, 1);
    assert!(parse(&bytes).is_err());
}

#[test]
fn rejects_wrong_watermark_and_end_fixed_fields() {
    let mut bad_watermark = OWNER_INVERSION.to_vec();
    let watermark = record_offsets(&bad_watermark)
        .iter()
        .find(|(_, kind, _)| *kind == 8)
        .unwrap()
        .0;
    set_u64(&mut bad_watermark, watermark + 16 + 4, 200);
    assert!(parse(&bad_watermark).is_err());

    let mut bad_end_count = EMPTY.to_vec();
    let end = record_offsets(&bad_end_count)
        .iter()
        .find(|(_, kind, _)| *kind == 9)
        .unwrap()
        .0;
    set_u32(&mut bad_end_count, end + 16, 1);
    assert!(parse(&bad_end_count).is_err());
}

#[test]
fn scene_asset_total_helper_checks_below_equal_above_and_overflow() {
    const ASSET_LIMIT: u64 = 128 * 1024 * 1024;
    const SCENE_LIMIT: u64 = 256 * 1024 * 1024;

    assert_eq!(
        checked_scene_asset_total_bytes(SCENE_LIMIT - ASSET_LIMIT - 1, ASSET_LIMIT).unwrap(),
        SCENE_LIMIT - 1
    );
    assert_eq!(
        checked_scene_asset_total_bytes(SCENE_LIMIT - ASSET_LIMIT, ASSET_LIMIT).unwrap(),
        SCENE_LIMIT
    );
    assert!(checked_scene_asset_total_bytes(SCENE_LIMIT - ASSET_LIMIT + 1, ASSET_LIMIT).is_err());
    assert!(checked_scene_asset_total_bytes(u64::MAX - 1, 2).is_err());
}

#[test]
fn rejects_invalid_asset_begin_dimensions_length_and_per_asset_limit() {
    let mut zero_width = PARITY.to_vec();
    set_first_asset_metadata(&mut zero_width, 0, 1, 4);
    assert!(parse(&zero_width).is_err());

    let mut oversized_dimension = PARITY.to_vec();
    set_first_asset_metadata(&mut oversized_dimension, 16_385, 1, 4);
    assert!(parse(&oversized_dimension).is_err());

    let mut wrong_raw_length = PARITY.to_vec();
    set_first_asset_metadata(&mut wrong_raw_length, 2, 1, 4);
    assert!(parse(&wrong_raw_length).is_err());

    let mut oversized_asset = PARITY.to_vec();
    let raw_len = 16_384u64 * 2_049 * 4;
    set_first_asset_metadata(&mut oversized_asset, 16_384, 2_049, raw_len);
    assert!(parse(&oversized_asset).is_err());
}

#[test]
fn rejects_bad_chunk_offsets_and_short_nonfinal_chunks() {
    let mut gap = PARITY.to_vec();
    let chunk = record_offset(&gap, 5) + 16;
    set_u64(&mut gap, chunk + 4, 1);
    assert!(parse(&gap).is_err());

    let mut short_final = PARITY.to_vec();
    set_first_asset_metadata(&mut short_final, 2, 1, 8);
    assert!(parse(&short_final).is_err());

    let mut short_nonfinal = PARITY.to_vec();
    set_first_asset_metadata(&mut short_nonfinal, 4_096, 512, 8 * 1024 * 1024);
    assert!(parse(&short_nonfinal).is_err());
}

#[test]
fn rejects_chunk_overrun_and_duplicate_offset() {
    let mut overrun = PARITY.to_vec();
    let chunk = record_offset(&overrun, 5);
    let old_payload_len = u32::from_le_bytes(overrun[chunk + 4..chunk + 8].try_into().unwrap());
    let insert_at = chunk + 16 + old_payload_len as usize;
    overrun.insert(insert_at, 0xaa);
    set_u32(&mut overrun, chunk + 4, old_payload_len + 1);
    assert!(parse(&overrun).is_err());

    let mut duplicate = PARITY.to_vec();
    let chunk_offset = record_offset(&duplicate, 5);
    let chunk_len = 16
        + u32::from_le_bytes(
            duplicate[chunk_offset + 4..chunk_offset + 8]
                .try_into()
                .unwrap(),
        ) as usize;
    let duplicate_record = duplicate[chunk_offset..chunk_offset + chunk_len].to_vec();
    let insert_at = record_offset(&duplicate, 6);
    let end_sequence =
        u64::from_le_bytes(duplicate[insert_at + 8..insert_at + 16].try_into().unwrap());
    duplicate.splice(insert_at..insert_at, duplicate_record);
    let inserted_len = chunk_len;
    let inserted_record_offset = insert_at;
    duplicate[inserted_record_offset + 8..inserted_record_offset + 16]
        .copy_from_slice(&end_sequence.to_le_bytes());
    for (offset, _, _) in record_offsets(&duplicate) {
        if offset > inserted_record_offset {
            let sequence =
                u64::from_le_bytes(duplicate[offset + 8..offset + 16].try_into().unwrap());
            set_u64(&mut duplicate, offset + 8, sequence + 1);
        }
    }
    assert_eq!(inserted_len, chunk_len);
    assert!(parse(&duplicate).is_err());
}

#[test]
fn rejects_empty_or_mismatched_asset_end_received_counts() {
    for received in [0, 3] {
        let mut bytes = PARITY.to_vec();
        let end = record_offset(&bytes, 6) + 16;
        set_u64(&mut bytes, end + 4, received);
        assert!(
            parse(&bytes).is_err(),
            "accepted AssetEnd received={received}"
        );
    }
}

#[test]
fn accepts_exact_four_mib_nonfinal_chunk_and_final_chunk() {
    let bytes = parity_with_8mib_first_asset(4 * 1024 * 1024, 8 * 1024 * 1024);
    let parsed = parse(&bytes).expect("valid 8 MiB asset split into two exact 4 MiB chunks");
    assert_eq!(parsed.asset_descriptors[0].width, 4_096);
    assert_eq!(parsed.asset_descriptors[0].height, 512);
    assert_eq!(parsed.asset_descriptors[0].raw_len, 8 * 1024 * 1024);
    assert_eq!(parsed.end.total_raw_asset_bytes, 8 * 1024 * 1024 + 4);
    assert_eq!(parsed.assets[0].rgba.len(), 8 * 1024 * 1024);
    assert!(parsed.assets[0].rgba[..4 * 1024 * 1024]
        .iter()
        .all(|byte| *byte == 0x5a));
    assert!(parsed.assets[0].rgba[4 * 1024 * 1024..]
        .iter()
        .all(|byte| *byte == 0xa5));
}

#[test]
fn rejects_final_chunk_shorter_than_remaining_declared_bytes() {
    let raw_bytes = 8 * 1024 * 1024u64;
    let bytes = parity_with_8mib_first_asset(4 * 1024 * 1024 - 4, raw_bytes - 4);
    let error = parse(&bytes)
        .err()
        .expect("short final chunk must fail at chunk validation");
    assert_eq!(
        error.to_string(),
        "NCT2 final AssetChunk must carry the remaining 1..4 MiB"
    );
}

#[test]
fn rejects_corrupted_chunk_when_asset_digests_are_unchanged() {
    let mut bytes = PARITY.to_vec();
    let chunk = record_offset(&bytes, 5) + 16;
    bytes[chunk + 12] ^= 0x80;
    let error = parse(&bytes)
        .err()
        .expect("chunk bytes changed after AssetBegin SHA was declared");
    assert!(error.to_string().contains("AssetBegin SHA-256"));
}

#[test]
fn rejects_asset_end_computed_sha_that_differs_from_verified_pixels() {
    let mut bytes = PARITY.to_vec();
    let asset_end = record_offset(&bytes, 6) + 16;
    bytes[asset_end + 12] ^= 0x01;
    let error = parse(&bytes)
        .err()
        .expect("AssetEnd computed SHA differs from received pixels");
    assert!(error.to_string().contains("AssetEnd SHA-256"));
}

#[test]
fn rejects_mismatched_end_scene_commitment() {
    let mut bytes = PARITY.to_vec();
    let end = record_offset(&bytes, 9) + 16;
    bytes[end + 32] ^= 0x01;
    let error = parse(&bytes)
        .err()
        .expect("End Scene Commitment does not match the scene");
    assert!(error.to_string().contains("Scene Commitment"));
}

#[test]
fn shared_vectors_match_parser_watermarks_and_reject_wrong_prefix_claims() {
    let vectors: serde_json::Value = serde_json::from_str(READINESS_VECTORS).unwrap();
    let owner_time = vectors["ready_vectors"]
        .as_array()
        .unwrap()
        .iter()
        .find(|vector| vector["name"] == "owner-order-differs-from-time-order")
        .unwrap();
    let ready = owner_time["ready_by_prefix"].as_array().unwrap();
    let watermarks: Vec<(usize, u32)> = record_offsets(OWNER_INVERSION)
        .into_iter()
        .filter_map(|(offset, kind, _)| {
            (kind == 8).then(|| {
                let prefix = u32::from_le_bytes(
                    OWNER_INVERSION[offset + 16..offset + 20]
                        .try_into()
                        .unwrap(),
                );
                (offset, prefix)
            })
        })
        .collect();
    assert_eq!(watermarks.len(), ready.len() - 1);
    for (offset, prefix) in &watermarks {
        let serialized = u64::from_le_bytes(
            OWNER_INVERSION[offset + 20..offset + 28]
                .try_into()
                .unwrap(),
        ) as i64;
        assert_eq!(serialized, ready[*prefix as usize].as_i64().unwrap());
    }
    let parsed = parse(OWNER_INVERSION).expect("same-ready prefix advancement is valid");
    assert_eq!(parsed.elements.len(), 3);
    assert!(parsed
        .elements
        .iter()
        .all(|element| element.draws.is_empty()));

    for invalid in owner_time["invalid_claims"].as_array().unwrap() {
        let prefix = invalid["prefix"].as_u64().unwrap() as u32;
        let claimed = invalid["ready"].as_u64().unwrap();
        let mut bad = OWNER_INVERSION.to_vec();
        let offset = watermarks
            .iter()
            .find(|(_, candidate)| *candidate == prefix)
            .unwrap()
            .0;
        set_u64(&mut bad, offset + 20, claimed);
        let error = parse(&bad).expect_err("wrong suffix-min watermark must be rejected");
        assert!(error.to_string().contains("Watermark"), "{error}");
    }
}

#[test]
fn negative_starts_are_clipped_in_declarations_but_preserved_in_draws() {
    let vectors: serde_json::Value = serde_json::from_str(READINESS_VECTORS).unwrap();
    let vector = vectors["ready_vectors"]
        .as_array()
        .unwrap()
        .iter()
        .find(|vector| vector["name"] == "negative-start-is-clipped-only-for-readiness")
        .unwrap();
    let fixture = parity_with_negative_start_vector();
    let parsed = parse(&fixture).expect("vector-derived NCT2 stream");
    let expected_clipped = vector["clipped_intervals"].as_array().unwrap();
    let expected_draws = vector["draw_intervals_remain"].as_array().unwrap();
    for ordinal in 0..2 {
        assert_eq!(
            parsed.declarations[ordinal].clipped_start,
            expected_clipped[ordinal][0].as_i64().unwrap() as i32
        );
        assert_eq!(
            parsed.declarations[ordinal].clipped_end,
            expected_clipped[ordinal][1].as_i64().unwrap() as i32
        );
        assert_eq!(
            parsed.elements[ordinal].draws[0].start_vpos,
            expected_draws[ordinal][0].as_i64().unwrap() as i32
        );
        assert_eq!(
            parsed.elements[ordinal].draws[0].end_vpos,
            expected_draws[ordinal][1].as_i64().unwrap() as i32
        );
    }
}

#[test]
fn rejects_zero_draw_declaration_without_explicit_completion() {
    let parsed = parse(OWNER_INVERSION).expect("zero Draw still has explicit completion");
    assert_eq!(parsed.elements.len(), 3);
    assert!(parsed
        .elements
        .iter()
        .all(|element| element.draws.is_empty()));

    let mut incomplete = Vec::new();
    let mut sequence = 0u64;
    let mut omitted = false;
    for (offset, kind, payload_len) in record_offsets(OWNER_INVERSION) {
        let payload = &OWNER_INVERSION[offset + 16..offset + 16 + payload_len];
        if kind == 7 && u32::from_le_bytes(payload[0..4].try_into().unwrap()) == 2 {
            omitted = true;
            continue;
        }
        append_record(&mut incomplete, kind, sequence, payload);
        sequence += 1;
    }
    assert!(
        omitted,
        "fixture must omit one explicit zero-Draw completion"
    );
    assert!(parse(&incomplete).is_err());
}
#[cfg(test)]
mod stream_upload_ownership {
    use std::io::{self, Cursor, Read};
    use std::sync::{Arc, Condvar, Mutex};

    use nico_compositord::stream_budget::{CpuBudget, ParserEvent};
    use nico_compositord::stream_protocol::{
        spawn_incremental_stream_parser, spawn_stream_parser_with_budget, IncrementalEvent,
        StreamEvent, StreamParser,
    };
    use nico_compositord::stream_renderer::{
        GpuBudget, StreamAssetUploader, UploadAttempt, UploadBackend, UploadCancellation,
        UploadDeferredReason, UploadRetirement,
    };

    const PARITY: &[u8] = include_bytes!(
        "../../../internal/nicorender/testdata/timeline/stream/nct1-compat-multidraw.nct2"
    );
    const METADATA_CREDIT: usize = 48 * 1024 * 1024;

    struct Gate {
        flags: Mutex<(bool, bool)>, // (blocked after first AssetEnd, released)
        changed: Condvar,
    }

    struct GateReader {
        input: Cursor<Vec<u8>>,
        gate: Arc<Gate>,
        stop_at: usize,
    }

    impl Read for GateReader {
        fn read(&mut self, bytes: &mut [u8]) -> io::Result<usize> {
            let position = self.input.position() as usize;
            if position >= self.stop_at {
                let mut flags = self.gate.flags.lock().unwrap();
                flags.0 = true;
                self.gate.changed.notify_all();
                while !flags.1 {
                    flags = self.gate.changed.wait(flags).unwrap();
                }
            }
            self.input.read(bytes)
        }
    }

    fn first_asset_end_offset(input: &[u8]) -> usize {
        let mut offset = 0;
        while offset + 16 <= input.len() {
            let kind = u32::from_le_bytes(input[offset..offset + 4].try_into().unwrap());
            let length =
                u32::from_le_bytes(input[offset + 4..offset + 8].try_into().unwrap()) as usize;
            offset += 16 + length;
            if kind == 6 {
                return offset;
            }
        }
        panic!("fixture has no AssetEnd");
    }

    fn gated_parser() -> (StreamParser, ParserEvent<StreamEvent>) {
        let gate = Arc::new(Gate {
            flags: Mutex::new((false, false)),
            changed: Condvar::new(),
        });
        let source_gate = Arc::clone(&gate);
        let input = PARITY.to_vec();
        let stop_at = first_asset_end_offset(&input);
        let parser = spawn_stream_parser_with_budget(
            GateReader {
                input: Cursor::new(input),
                gate: Arc::clone(&gate),
                stop_at,
            },
            CpuBudget::new(),
            move || {
                let mut flags = source_gate.flags.lock().unwrap();
                flags.1 = true;
                source_gate.changed.notify_all();
            },
        )
        .expect("start bounded parser");
        let mut flags = gate.flags.lock().unwrap();
        while !flags.0 {
            flags = gate.changed.wait(flags).unwrap();
        }
        drop(flags);
        let event = parser.recv().expect("receive first event").expect("event");
        assert!(matches!(event.payload.get(), StreamEvent::Asset(_)));
        (parser, event)
    }

    struct FakeTexture {
        width: u32,
        height: u32,
    }

    struct FakeStaging {
        bytes: Vec<u8>,
        unmapped: bool,
    }

    struct FakeState {
        creates: usize,
        budget_bytes_at_create: Vec<usize>,
        submissions: usize,
        registered_asset_ids: Vec<u32>,
        completion_tokens: Vec<u64>,
        retirements: std::collections::HashMap<u64, UploadRetirement>,
        next_token: u64,
        cancel_during_copy: Option<UploadCancellation>,
    }

    struct FakeBackend {
        state: Arc<Mutex<FakeState>>,
        budget: GpuBudget,
    }

    impl UploadBackend for FakeBackend {
        type Texture = FakeTexture;
        type Staging = FakeStaging;

        fn validate_texture_extent(&self, width: u32, height: u32) -> Result<(), String> {
            if width == 0 || height == 0 {
                Err("empty texture".into())
            } else {
                Ok(())
            }
        }

        fn validate_asset_registration(&self, _asset_id: u32) -> Result<(), String> {
            Ok(())
        }

        fn create_texture(&mut self, width: u32, height: u32) -> Result<Self::Texture, String> {
            let mut state = self.state.lock().unwrap();
            state.creates += 1;
            state.budget_bytes_at_create.push(self.budget.used_bytes());
            Ok(FakeTexture { width, height })
        }

        fn create_staging(&mut self, bytes: usize) -> Result<Self::Staging, String> {
            let mut state = self.state.lock().unwrap();
            state.creates += 1;
            state.budget_bytes_at_create.push(self.budget.used_bytes());
            drop(state);
            Ok(FakeStaging {
                bytes: vec![0; bytes],
                unmapped: false,
            })
        }

        fn copy_rgba_to_staging(
            &mut self,
            staging: &mut Self::Staging,
            rgba: &[u8],
            width: u32,
            height: u32,
            padded_bytes_per_row: u32,
        ) -> Result<(), String> {
            let row_bytes = width as usize * 4;
            for row in 0..height as usize {
                let source = row * row_bytes;
                let destination = row * padded_bytes_per_row as usize;
                staging.bytes[destination..destination + row_bytes]
                    .copy_from_slice(&rgba[source..source + row_bytes]);
            }
            staging.unmapped = true;
            if let Some(cancellation) = self.state.lock().unwrap().cancel_during_copy.take() {
                cancellation.cancel();
            }
            Ok(())
        }

        fn submit_upload(
            &mut self,
            staging: &Self::Staging,
            texture: &Self::Texture,
            _padded_bytes_per_row: u32,
            retirement: UploadRetirement,
        ) -> Result<u64, String> {
            assert!(staging.unmapped, "staging must be unmapped before submit");
            assert!(texture.width > 0 && texture.height > 0);
            let mut state = self.state.lock().unwrap();
            state.submissions += 1;
            state.next_token += 1;
            let token = state.next_token;
            state.retirements.insert(token, retirement);
            Ok(token)
        }

        fn register_asset(
            &mut self,
            asset_id: u32,
            _width: u32,
            _height: u32,
            _texture: &Self::Texture,
            _owner: Arc<dyn Send + Sync>,
        ) {
            self.state
                .lock()
                .unwrap()
                .registered_asset_ids
                .push(asset_id);
        }

        fn poll_completed(&mut self) -> Result<Vec<u64>, String> {
            let mut state = self.state.lock().unwrap();
            let completed = std::mem::take(&mut state.completion_tokens);
            for token in &completed {
                state.retirements.remove(token);
            }
            Ok(completed)
        }
    }

    fn fake_backend(budget: GpuBudget) -> (FakeBackend, Arc<Mutex<FakeState>>) {
        let state = Arc::new(Mutex::new(FakeState {
            creates: 0,
            budget_bytes_at_create: Vec::new(),
            submissions: 0,
            registered_asset_ids: Vec::new(),
            completion_tokens: Vec::new(),
            retirements: std::collections::HashMap::new(),
            next_token: 0,
            cancel_during_copy: None,
        }));
        (
            FakeBackend {
                state: Arc::clone(&state),
                budget,
            },
            state,
        )
    }

    #[test]
    fn uploads_asset_before_complete_and_releases_cpu_then_staging_credits() {
        let (parser, event) = gated_parser();
        let (asset_bytes, padded_staging_bytes) = match event.payload.get() {
            StreamEvent::Asset(asset) => {
                let row = (asset.descriptor().width as usize * 4).div_ceil(256) * 256;
                (asset.rgba().len(), row * asset.descriptor().height as usize)
            }
            _ => unreachable!(),
        };
        assert_eq!(
            parser.budget_used_bytes(),
            METADATA_CREDIT + asset_bytes,
            "parser is paused after the Asset event, before Complete"
        );

        let budget = GpuBudget::new();
        let (backend, state) = fake_backend(budget.clone());
        let mut uploader = StreamAssetUploader::new(backend, budget);
        let cancellation = UploadCancellation::new();
        let attempt = uploader
            .try_upload(event, &cancellation)
            .expect("upload asset");
        let token = match attempt {
            UploadAttempt::Submitted { token, .. } => token,
            UploadAttempt::Deferred { .. } => panic!("available GPU credits should submit"),
        };
        assert_eq!(
            state.lock().unwrap().budget_bytes_at_create,
            vec![
                asset_bytes + padded_staging_bytes,
                asset_bytes + padded_staging_bytes,
            ]
        );
        assert_eq!(parser.budget_used_bytes(), METADATA_CREDIT);
        assert_eq!(state.lock().unwrap().submissions, 1);
        assert!(
            parser.budget_used_bytes() > 0,
            "Complete/EOF is not required to upload"
        );

        let gpu_used = uploader.budget_used_bytes();
        assert_eq!(gpu_used, asset_bytes + padded_staging_bytes);
        assert!(
            uploader.release_scene().is_err(),
            "resident texture cannot be released before queue completion"
        );
        state.lock().unwrap().completion_tokens.push(token);
        assert!(uploader.poll_completions().expect("poll upload completion"));
        assert_eq!(
            uploader.budget_used_bytes(),
            asset_bytes,
            "completion releases only staging credit"
        );
        uploader
            .release_scene()
            .expect("release resident after scene");
        assert_eq!(uploader.budget_used_bytes(), 0);
        drop(parser);
    }

    #[test]
    fn incremental_asset_upload_registers_a_resolvable_renderer_asset() {
        let parser = spawn_incremental_stream_parser(Cursor::new(PARITY), || {});
        let event = loop {
            let event = parser
                .recv()
                .expect("receive incremental event")
                .expect("event before Complete");
            if matches!(event.payload.get(), IncrementalEvent::Asset(_)) {
                break event;
            }
        };
        let asset_id = match event.payload.get() {
            IncrementalEvent::Asset(asset) => asset.descriptor().id,
            _ => unreachable!(),
        };

        let budget = GpuBudget::new();
        let (backend, state) = fake_backend(budget.clone());
        let mut uploader = StreamAssetUploader::new(backend, budget);
        let attempt = uploader
            .try_upload_incremental(event, &UploadCancellation::new())
            .expect("upload incremental asset");
        let token = match attempt {
            UploadAttempt::Submitted {
                asset_id: uploaded_id,
                token,
            } => {
                assert_eq!(uploaded_id, asset_id);
                token
            }
            UploadAttempt::Deferred { .. } => panic!("available credits should upload"),
        };

        assert_eq!(
            state.lock().unwrap().registered_asset_ids,
            vec![asset_id],
            "the renderer registry must receive the uploaded asset before its Element"
        );
        assert_eq!(uploader.resident_asset_count(), 1);
        state.lock().unwrap().completion_tokens.push(token);
        assert!(uploader.poll_completions().unwrap());
        uploader.release_scene().unwrap();
        drop(parser);
    }

    #[test]
    fn exhausted_or_cancelled_upload_does_not_allocate_or_submit() {
        let (parser, event) = gated_parser();
        let budget = GpuBudget::new();
        let (backend, state) = fake_backend(budget.clone());
        let cancellation = UploadCancellation::new();
        let full = budget.try_reserve(budget.limit_bytes()).unwrap();
        let mut uploader = StreamAssetUploader::new(backend, budget.clone());
        let deferred = match uploader.try_upload(event, &cancellation).unwrap() {
            UploadAttempt::Deferred {
                reason: UploadDeferredReason::CreditsUnavailable,
                event,
            } => event,
            _ => panic!("expected credit deferral"),
        };
        assert_eq!(state.lock().unwrap().creates, 0);
        assert_eq!(state.lock().unwrap().submissions, 0);
        assert_eq!(
            uploader.resident_asset_count(),
            0,
            "a deferred asset is not a registered resident page"
        );
        drop(full);

        let cancel_after_copy = UploadCancellation::new();
        state.lock().unwrap().cancel_during_copy = Some(cancel_after_copy.clone());
        let deferred = match uploader.try_upload(deferred, &cancel_after_copy).unwrap() {
            UploadAttempt::Deferred {
                reason: UploadDeferredReason::Cancelled,
                event,
            } => event,
            _ => panic!("expected cancellation before submit"),
        };
        assert_eq!(state.lock().unwrap().submissions, 0);
        assert_eq!(
            uploader.budget_used_bytes(),
            0,
            "unsubmitted allocations were dropped"
        );
        assert!(parser.budget_used_bytes() > METADATA_CREDIT);

        let attempt = uploader
            .try_upload(deferred, &UploadCancellation::new())
            .expect("retry after cancellation");
        let token = match attempt {
            UploadAttempt::Submitted { token, .. } => token,
            UploadAttempt::Deferred { .. } => panic!("released GPU credits should recover"),
        };
        assert_eq!(state.lock().unwrap().submissions, 1);
        assert_eq!(
            uploader.resident_asset_count(),
            1,
            "the page is counted only after upload submission and registration"
        );
        state.lock().unwrap().completion_tokens.push(token);
        uploader.poll_completions().unwrap();
        uploader.release_scene().unwrap();
        drop(parser);
    }

    #[test]
    fn dropping_uploader_keeps_texture_and_staging_credits_until_completion() {
        let (parser, event) = gated_parser();
        let expected_in_flight_bytes = match event.payload.get() {
            StreamEvent::Asset(asset) => {
                let row = (asset.descriptor().width as usize * 4).div_ceil(256) * 256;
                asset.rgba().len() + row * asset.descriptor().height as usize
            }
            _ => unreachable!(),
        };
        let budget = GpuBudget::new();
        let (backend, state) = fake_backend(budget.clone());
        let cancellation = UploadCancellation::new();
        let mut uploader = StreamAssetUploader::new(backend, budget.clone());
        let token = match uploader.try_upload(event, &cancellation).unwrap() {
            UploadAttempt::Submitted { token, .. } => token,
            UploadAttempt::Deferred { .. } => panic!("available GPU credits should submit"),
        };
        let in_flight_bytes = budget.used_bytes();
        assert_eq!(in_flight_bytes, expected_in_flight_bytes);

        drop(uploader);
        assert_eq!(
            budget.used_bytes(),
            in_flight_bytes,
            "dropping the uploader cannot release resources used by the pending submission"
        );
        assert_eq!(state.lock().unwrap().retirements.len(), 1);

        state.lock().unwrap().completion_tokens.push(token);
        let mut backend = FakeBackend {
            state: Arc::clone(&state),
            budget: budget.clone(),
        };
        assert_eq!(backend.poll_completed().unwrap(), vec![token]);
        assert!(state.lock().unwrap().retirements.is_empty());
        assert_eq!(
            budget.used_bytes(),
            0,
            "completion retires both allocation classes"
        );
        drop(parser);
    }
}
