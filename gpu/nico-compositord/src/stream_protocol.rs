//! GPU-independent, bounded-record reader for the NCT2 comment stream.
//!
//! This stage validates the wire envelope and scene structure, assembles
//! bounded RGBA assets, and verifies their hashes and the scene commitment.

use std::collections::HashSet;
use std::fmt;
use std::io::{self, Read};
use std::mem::size_of;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::thread::{self, JoinHandle};
use std::time::Duration;

use sha2::{Digest, Sha256};

use crate::protocol::{self, Draw};
use crate::stream_budget::{
    CpuBudget, CpuCredit, ParserCancellation, ParserEvent, ParserEventQueue, ParserEventReceiver,
    ParserEventSender,
};

pub use crate::stream_budget::ParserReceive;

pub const ENVELOPE_BYTES: usize = 16;
pub const HEADER_PAYLOAD_BYTES: usize = 80;
pub const DECLARATION_BYTES: usize = 20;
pub const DECLARATIONS_COMPLETE_BYTES: usize = 36;
pub const DRAW_BYTES: usize = 128;
pub const END_PAYLOAD_BYTES: usize = 64;
pub const MAX_RECORD_PAYLOAD: u32 = 16 * 1024 * 1024;
pub const MAX_DECLARATIONS: u32 = 100_000;
pub const MAX_DRAWS_PER_ELEMENT: u32 = 1_024;
const MAX_CHUNK_DATA: u32 = 4 * 1024 * 1024;
const ASSET_CHUNK_SCRATCH_BYTES: usize = 8 * 1024;
const MAX_ASSET_BYTES: u64 = 128 * 1024 * 1024;
const MAX_TOTAL_ASSET_BYTES: u64 = 256 * 1024 * 1024;
const MAX_EXACT_F32_INT: i64 = 1 << 24;
// Covers a conservative retained/transient parser bound: one maximum record
// (16 MiB), decoded declarations and their wire record (4 MiB), at most
// 100,000 encoded Draws (12.8 MiB), 100,000 Element slots (3.2 MiB), plus
// asset descriptors, ID sets, scratch buffers, and queue/parser bookkeeping.
// Pixel allocations are separately credited; the remaining margin covers
// allocator capacity rounding and one in-progress record's small metadata.
const PARSER_METADATA_RESERVATION_BYTES: usize = 48 * 1024 * 1024;

const RECORD_HEADER: u32 = 1;
const RECORD_DECLARATIONS: u32 = 2;
const RECORD_DECLARATIONS_COMPLETE: u32 = 3;
const RECORD_ASSET_BEGIN: u32 = 4;
const RECORD_ASSET_CHUNK: u32 = 5;
const RECORD_ASSET_END: u32 = 6;
const RECORD_ELEMENT_COMPLETE: u32 = 7;
const RECORD_WATERMARK: u32 = 8;
const RECORD_END: u32 = 9;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error(String);

impl Error {
    fn invalid(message: impl Into<String>) -> Self {
        Self(message.into())
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for Error {}

/// Add declared asset bytes without exceeding the NCT2 scene limit.
pub fn checked_scene_asset_total_bytes(current_total: u64, asset_bytes: u64) -> Result<u64, Error> {
    let total = current_total
        .checked_add(asset_bytes)
        .ok_or_else(|| Error::invalid("NCT2 declared asset byte total overflow"))?;
    if total > MAX_TOTAL_ASSET_BYTES {
        return Err(Error::invalid("NCT2 declared asset bytes exceed 256 MiB"));
    }
    Ok(total)
}

fn validate_asset_metadata(width: u32, height: u32, raw_len: u64) -> Result<(), Error> {
    if width == 0 || height == 0 || width > 16_384 || height > 16_384 {
        return Err(Error::invalid("NCT2 asset dimensions exceed limits"));
    }
    let expected_raw_len = (width as u64)
        .checked_mul(height as u64)
        .and_then(|pixels| pixels.checked_mul(4))
        .ok_or_else(|| Error::invalid("NCT2 asset raw length overflow"))?;
    if raw_len != expected_raw_len {
        return Err(Error::invalid(
            "NCT2 asset raw length differs from dimensions",
        ));
    }
    if raw_len > MAX_ASSET_BYTES {
        return Err(Error::invalid("NCT2 asset exceeds 128 MiB"));
    }
    Ok(())
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Header {
    pub width: u32,
    pub height: u32,
    pub frame_count: u32,
    pub fps_num: u32,
    pub fps_den: u32,
    pub declaration_count: u32,
    pub flags: u32,
    pub bundle_sha256: [u8; 32],
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Declaration {
    pub ordinal: u32,
    pub owner_order: u32,
    pub comment_index: u32,
    pub clipped_start: i32,
    pub clipped_end: i32,
}

/// Metadata copied from AssetBegin. Its digest is trusted only after the full
/// RGBA transfer has been hashed and verified.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AssetDescriptor {
    pub id: u32,
    pub width: u32,
    pub height: u32,
    pub raw_len: u64,
    pub declared_sha256: [u8; 32],
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParsedAsset {
    pub descriptor: AssetDescriptor,
    pub rgba: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Element {
    pub ordinal: u32,
    pub draws: Vec<Draw>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct End {
    pub declaration_count: u32,
    pub asset_count: u32,
    pub draw_count: u32,
    pub total_raw_asset_bytes: u64,
    pub final_prefix: u32,
    pub final_ready_vpos: i64,
    /// Opaque in T5.3. Recomputing it requires the pixel hashes verified by T5.4.
    pub scene_commitment: [u8; 32],
}

#[derive(Debug, Clone, PartialEq)]
pub struct ParsedStream {
    pub header: Header,
    pub declarations: Vec<Declaration>,
    pub asset_descriptors: Vec<AssetDescriptor>,
    pub assets: Vec<ParsedAsset>,
    pub elements: Vec<Element>,
    pub end: End,
}

#[derive(Debug)]
enum Payload {
    Bytes(Vec<u8>),
    AssetChunk {
        asset_id: u32,
        offset: u64,
        data_len: u32,
    },
}

#[derive(Debug)]
struct OpenAsset {
    id: u32,
    descriptor_index: usize,
    raw_len: u64,
    received_bytes: u64,
    rgba: Vec<u8>,
    hasher: Sha256,
    credit: Option<CpuCredit>,
}

/// Events emitted by the cancellable parser. Asset events own the verified
/// pixel allocation and its budget credit until the event is dropped.
#[derive(Debug)]
pub enum StreamEvent {
    Asset(StreamAsset),
    Complete(Result<StreamComplete, Error>),
}

/// Credit-bearing events for scheduler-driven NCT2 consumption. The legacy
/// `StreamEvent` remains unchanged for existing Asset/Complete consumers.
#[derive(Debug)]
pub enum IncrementalEvent {
    Header(Header),
    Declarations(Vec<Declaration>),
    Asset(StreamAsset),
    Element(Element),
    Watermark { prefix: u32, ready_vpos: i64 },
    Complete(Result<StreamComplete, Error>),
}

/// Non-cloneable pixel owner used only on the incremental path. Pixel bytes
/// are exposed as a slice while the enclosing `Budgeted<StreamEvent>` retains
/// the matching CPU credit.
#[derive(Debug)]
pub struct StreamAsset {
    descriptor: AssetDescriptor,
    rgba: Vec<u8>,
}

impl StreamAsset {
    pub fn descriptor(&self) -> &AssetDescriptor {
        &self.descriptor
    }

    pub fn rgba(&self) -> &[u8] {
        &self.rgba
    }
}

/// Non-cloneable final metadata owner. Keep it inside its `Budgeted` event to
/// retain the parser's metadata reservation through downstream assembly.
#[derive(Debug)]
pub struct StreamComplete {
    parsed: ParsedStream,
}

impl StreamComplete {
    pub fn header(&self) -> &Header {
        &self.parsed.header
    }

    pub fn declarations(&self) -> &[Declaration] {
        &self.parsed.declarations
    }

    pub fn asset_descriptors(&self) -> &[AssetDescriptor] {
        &self.parsed.asset_descriptors
    }

    pub fn elements(&self) -> &[Element] {
        &self.parsed.elements
    }

    pub fn end(&self) -> &End {
        &self.parsed.end
    }
}

enum EventSender {
    Legacy(ParserEventSender<StreamEvent>),
    Incremental(ParserEventSender<IncrementalEvent>),
}

impl EventSender {
    fn send_asset(&self, asset: StreamAsset, credit: CpuCredit) -> Result<(), String> {
        match self {
            Self::Legacy(sender) => sender
                .send(StreamEvent::Asset(asset), credit)
                .map_err(|error| error.to_string()),
            Self::Incremental(sender) => sender
                .send(IncrementalEvent::Asset(asset), credit)
                .map_err(|error| error.to_string()),
        }
    }

    fn send_complete(
        &self,
        result: Result<StreamComplete, Error>,
        credit: CpuCredit,
    ) -> Result<(), String> {
        match self {
            Self::Legacy(sender) => sender
                .send(StreamEvent::Complete(result), credit)
                .map_err(|error| error.to_string()),
            Self::Incremental(sender) => sender
                .send(IncrementalEvent::Complete(result), credit)
                .map_err(|error| error.to_string()),
        }
    }

    fn reserve_error_credit(
        &self,
        budget: &CpuBudget,
        cancellation: &ParserCancellation,
        error: &Error,
    ) -> Option<CpuCredit> {
        let bytes = match self {
            Self::Legacy(_) => 0,
            Self::Incremental(_) => {
                size_of::<IncrementalEvent>().checked_add(error.0.capacity())?
            }
        };
        budget.reserve(bytes, cancellation).ok()
    }
}

struct ParseContext {
    budget: CpuBudget,
    cancellation: ParserCancellation,
    sender: EventSender,
    metadata_credit: Option<CpuCredit>,
}

impl ParseContext {
    fn emit_incremental<F>(&self, reserved_bytes: usize, create: F) -> Result<(), Error>
    where
        F: FnOnce() -> IncrementalEvent,
    {
        let EventSender::Incremental(sender) = &self.sender else {
            return Ok(());
        };
        let credit = self
            .budget
            .reserve(reserved_bytes, &self.cancellation)
            .map_err(|error| Error::invalid(error.to_string()))?;
        sender
            .send(create(), credit)
            .map_err(|error| Error::invalid(error.to_string()))
    }
}

/// A parser worker whose receiver owns the worker join boundary. Dropping it
/// cancels between records, invokes the caller's source-unblock callback, and
/// joins the parser thread. `Read` itself is not interruptible by Rust; the
/// callback must unblock any source that can stall inside `read`.
pub struct StreamParser {
    receiver: ParserEventReceiver<StreamEvent>,
    budget: CpuBudget,
    unblock_source: Arc<dyn Fn() + Send + Sync>,
    worker: Option<JoinHandle<()>>,
}

/// Scheduler-friendly incremental parser. Its event channel preserves wire
/// order and each event owns a CPU-budget reservation until dropped.
pub struct IncrementalStreamParser {
    receiver: ParserEventReceiver<IncrementalEvent>,
    budget: CpuBudget,
    unblock_source: Arc<dyn Fn() + Send + Sync>,
    worker: Option<JoinHandle<()>>,
    unblock_called: AtomicBool,
}

impl IncrementalStreamParser {
    pub fn recv(&self) -> Result<Option<ParserEvent<IncrementalEvent>>, Error> {
        self.receiver
            .recv()
            .map_err(|error| Error::invalid(error.to_string()))
    }

    pub fn try_recv(&self) -> Result<ParserReceive<IncrementalEvent>, Error> {
        self.receiver
            .try_recv()
            .map_err(|error| Error::invalid(error.to_string()))
    }

    pub fn recv_timeout(
        &self,
        timeout: Duration,
    ) -> Result<ParserReceive<IncrementalEvent>, Error> {
        self.receiver
            .recv_timeout(timeout)
            .map_err(|error| Error::invalid(error.to_string()))
    }

    pub fn cancel(&self) {
        self.receiver.cancel_and_clear();
        self.unblock();
    }

    fn unblock(&self) {
        if !self.unblock_called.swap(true, Ordering::AcqRel) {
            (self.unblock_source)();
        }
    }

    pub fn join(&mut self) -> Result<(), Error> {
        if let Some(worker) = self.worker.take() {
            worker
                .join()
                .map_err(|_| Error::invalid("NCT2 parser worker panicked"))?;
        }
        Ok(())
    }

    pub fn budget_used_bytes(&self) -> usize {
        self.budget.used_bytes()
    }
}

impl Drop for IncrementalStreamParser {
    fn drop(&mut self) {
        self.receiver.cancel_and_clear();
        if self
            .worker
            .as_ref()
            .is_some_and(|worker| !worker.is_finished())
        {
            self.unblock();
        }
        let _ = self.join();
    }
}

impl StreamParser {
    pub fn recv(&self) -> Result<Option<ParserEvent<StreamEvent>>, Error> {
        self.receiver
            .recv()
            .map_err(|error| Error::invalid(error.to_string()))
    }

    /// Join after draining events. Drop also clears queued events before join.
    pub fn join(&mut self) -> Result<(), Error> {
        if let Some(worker) = self.worker.take() {
            worker
                .join()
                .map_err(|_| Error::invalid("NCT2 parser worker panicked"))?;
        }
        Ok(())
    }

    pub fn budget_used_bytes(&self) -> usize {
        self.budget.used_bytes()
    }
}

impl Drop for StreamParser {
    fn drop(&mut self) {
        if self
            .worker
            .as_ref()
            .is_some_and(|worker| !worker.is_finished())
        {
            self.receiver.cancel_and_clear();
            (self.unblock_source)();
        } else {
            self.receiver.cancel_and_clear();
        }
        let _ = self.join();
    }
}

/// Start incremental NCT2 parsing with a 256 MiB concurrent live-allocation
/// budget. The source-unblock callback is invoked on receiver drop; callers
/// using a potentially blocking `Read` must make that callback interrupt it.
pub fn spawn_stream_parser<R, F>(reader: R, unblock_source: F) -> StreamParser
where
    R: Read + Send + 'static,
    F: Fn() + Send + Sync + 'static,
{
    spawn_stream_parser_with_budget(reader, CpuBudget::new(), unblock_source)
        .expect("fresh parser CPU budget accepts its metadata reservation")
}

/// Start a parser against a caller-shared CPU budget. The bounded metadata
/// reservation is acquired before the event queue or worker is created.
pub fn spawn_stream_parser_with_budget<R, F>(
    reader: R,
    budget: CpuBudget,
    unblock_source: F,
) -> Result<StreamParser, Error>
where
    R: Read + Send + 'static,
    F: Fn() + Send + Sync + 'static,
{
    let cancellation = ParserCancellation::new();
    let metadata_credit = budget
        .try_reserve(PARSER_METADATA_RESERVATION_BYTES)
        .map_err(|error| Error::invalid(error.to_string()))?;
    let (sender, receiver) = ParserEventQueue::new(cancellation.clone());
    let worker_budget = budget.clone();
    let worker_cancellation = cancellation.clone();
    let worker = thread::spawn(move || {
        let mut context = ParseContext {
            budget: worker_budget.clone(),
            cancellation: worker_cancellation,
            sender: EventSender::Legacy(sender),
            metadata_credit: Some(metadata_credit),
        };
        if let Err(error) = read_stream_inner(reader, Some(&mut context)) {
            if let Some(credit) =
                context
                    .sender
                    .reserve_error_credit(&worker_budget, &context.cancellation, &error)
            {
                let _ = context.sender.send_complete(Err(error), credit);
            }
        }
    });
    Ok(StreamParser {
        receiver,
        budget,
        unblock_source: Arc::new(unblock_source),
        worker: Some(worker),
    })
}

/// Start scheduler-friendly parsing with ordered, credit-bearing metadata and
/// completion events. The source-unblock callback is invoked on cancellation
/// or drop and must interrupt a potentially blocking `Read`.
pub fn spawn_incremental_stream_parser<R, F>(
    reader: R,
    unblock_source: F,
) -> IncrementalStreamParser
where
    R: Read + Send + 'static,
    F: Fn() + Send + Sync + 'static,
{
    spawn_incremental_stream_parser_with_budget(reader, CpuBudget::new(), unblock_source)
        .expect("fresh parser CPU budget accepts its metadata reservation")
}

/// Start incremental parsing against a caller-shared CPU budget.
pub fn spawn_incremental_stream_parser_with_budget<R, F>(
    reader: R,
    budget: CpuBudget,
    unblock_source: F,
) -> Result<IncrementalStreamParser, Error>
where
    R: Read + Send + 'static,
    F: Fn() + Send + Sync + 'static,
{
    let cancellation = ParserCancellation::new();
    let metadata_credit = budget
        .try_reserve(PARSER_METADATA_RESERVATION_BYTES)
        .map_err(|error| Error::invalid(error.to_string()))?;
    let (sender, receiver) = ParserEventQueue::new(cancellation.clone());
    let worker_budget = budget.clone();
    let worker_cancellation = cancellation.clone();
    let worker = thread::spawn(move || {
        let mut context = ParseContext {
            budget: worker_budget.clone(),
            cancellation: worker_cancellation,
            sender: EventSender::Incremental(sender),
            metadata_credit: Some(metadata_credit),
        };
        if let Err(error) = read_stream_inner(reader, Some(&mut context)) {
            if let Some(credit) =
                context
                    .sender
                    .reserve_error_credit(&worker_budget, &context.cancellation, &error)
            {
                let _ = context.sender.send_complete(Err(error), credit);
            }
        }
    });
    Ok(IncrementalStreamParser {
        receiver,
        budget,
        unblock_source: Arc::new(unblock_source),
        worker: Some(worker),
        unblock_called: AtomicBool::new(false),
    })
}

fn check_parser_cancelled(context: Option<&ParseContext>) -> Result<(), Error> {
    if context.map_or(false, |context| context.cancellation.is_cancelled()) {
        Err(Error::invalid("NCT2 parser cancelled"))
    } else {
        Ok(())
    }
}

fn event_copy_reservation(
    base_bytes: usize,
    count: usize,
    item_bytes: usize,
) -> Result<usize, Error> {
    count
        .checked_mul(item_bytes)
        .and_then(|items| base_bytes.checked_add(items))
        .ok_or_else(|| Error::invalid("NCT2 incremental event reservation size overflow"))
}

#[derive(Debug)]
struct Record {
    kind: u32,
    sequence: u64,
    payload: Payload,
}

/// Read and validate one NCT2 stream without buffering the encoded stream.
pub fn read_stream<R: Read>(reader: R) -> Result<ParsedStream, Error> {
    read_stream_inner(reader, None)?.ok_or_else(|| Error::invalid("NCT2 parser produced no result"))
}

fn read_stream_inner<R: Read>(
    mut reader: R,
    mut context: Option<&mut ParseContext>,
) -> Result<Option<ParsedStream>, Error> {
    check_parser_cancelled(context.as_deref())?;
    let mut expected_sequence = 0u64;
    let header_record = read_record(&mut reader, expected_sequence)?
        .ok_or_else(|| Error::invalid("missing NCT2 Header and End"))?;
    expected_sequence = next_sequence(expected_sequence, &header_record)?;
    if header_record.kind != RECORD_HEADER {
        return Err(Error::invalid("NCT2 first record is not Header"));
    }
    let header = parse_header(bytes_payload(header_record)?)?;
    let exclusive_end = frame_vpos(header.frame_count, header.fps_num, header.fps_den)? as i64;
    if let Some(context) = context.as_deref_mut() {
        context.emit_incremental(size_of::<IncrementalEvent>(), || {
            IncrementalEvent::Header(header.clone())
        })?;
    }

    let declarations_record = read_record(&mut reader, expected_sequence)?
        .ok_or_else(|| Error::invalid("NCT2 ended before Declarations"))?;
    expected_sequence = next_sequence(expected_sequence, &declarations_record)?;
    if declarations_record.kind != RECORD_DECLARATIONS {
        return Err(Error::invalid("NCT2 second record is not Declarations"));
    }
    let declarations =
        parse_declarations(bytes_payload(declarations_record)?, &header, exclusive_end)?;

    let complete_record = read_record(&mut reader, expected_sequence)?
        .ok_or_else(|| Error::invalid("NCT2 ended before DeclarationsComplete"))?;
    expected_sequence = next_sequence(expected_sequence, &complete_record)?;
    if complete_record.kind != RECORD_DECLARATIONS_COMPLETE {
        return Err(Error::invalid(
            "NCT2 third record is not DeclarationsComplete",
        ));
    }
    validate_declarations_complete(bytes_payload(complete_record)?, &header, &declarations)?;
    if let Some(context) = context.as_deref_mut() {
        let reservation = event_copy_reservation(
            size_of::<IncrementalEvent>(),
            declarations.len(),
            size_of::<Declaration>(),
        )?;
        context.emit_incremental(reservation, || {
            IncrementalEvent::Declarations(declarations.clone())
        })?;
    }

    let mut asset_descriptors = Vec::new();
    let mut assets = Vec::new();
    let mut seen_asset_ids = HashSet::new();
    let mut closed_asset_ids = HashSet::new();
    let mut open_asset: Option<OpenAsset> = None;
    let mut total_raw_asset_bytes = 0u64;
    let mut referenced_asset_ids = HashSet::new();
    let mut elements = Vec::with_capacity(declarations.len());
    let mut total_draw_count = 0u32;
    let mut last_watermark_prefix = 0u32;
    let mut last_watermark_ready = ready_vpos(&declarations, 0, exclusive_end);

    loop {
        check_parser_cancelled(context.as_deref())?;
        let record = read_record(&mut reader, expected_sequence)?
            .ok_or_else(|| Error::invalid("NCT2 EOF before End"))?;
        expected_sequence = next_sequence(expected_sequence, &record)?;

        match record.kind {
            RECORD_HEADER | RECORD_DECLARATIONS | RECORD_DECLARATIONS_COMPLETE => {
                return Err(Error::invalid(
                    "duplicate or out-of-order NCT2 prefix record",
                ));
            }
            RECORD_ASSET_BEGIN => {
                if open_asset.is_some() {
                    return Err(Error::invalid("interleaved NCT2 asset transfer"));
                }
                let payload = bytes_payload(record)?;
                if payload.len() != 52 {
                    return Err(Error::invalid("NCT2 AssetBegin payload must be 52 bytes"));
                }
                let id = read_u32(&payload[0..4]);
                let width = read_u32(&payload[4..8]);
                let height = read_u32(&payload[8..12]);
                let raw_len = read_u64(&payload[12..20]);
                validate_asset_metadata(width, height, raw_len)?;
                let mut declared_sha256 = [0u8; 32];
                declared_sha256.copy_from_slice(&payload[20..52]);
                if asset_descriptors.len() >= protocol::MAX_ASSETS as usize {
                    return Err(Error::invalid("NCT2 asset count exceeds NCT1 limit"));
                }
                if !seen_asset_ids.insert(id) {
                    return Err(Error::invalid(format!("duplicate NCT2 asset id {id}")));
                }
                total_raw_asset_bytes =
                    checked_scene_asset_total_bytes(total_raw_asset_bytes, raw_len)?;
                let credit = if let Some(context) = context.as_deref_mut() {
                    Some(
                        context
                            .budget
                            .reserve(raw_len as usize, &context.cancellation)
                            .map_err(|error| Error::invalid(error.to_string()))?,
                    )
                } else {
                    None
                };
                let rgba = allocate_asset_pixels(raw_len)?;
                let descriptor_index = asset_descriptors.len();
                asset_descriptors.push(AssetDescriptor {
                    id,
                    width,
                    height,
                    raw_len,
                    declared_sha256,
                });
                open_asset = Some(OpenAsset {
                    id,
                    descriptor_index,
                    raw_len,
                    received_bytes: 0,
                    rgba,
                    hasher: Sha256::new(),
                    credit,
                });
            }
            RECORD_ASSET_CHUNK => {
                let Payload::AssetChunk {
                    asset_id,
                    offset,
                    data_len,
                } = record.payload
                else {
                    return Err(Error::invalid("internal NCT2 AssetChunk payload mismatch"));
                };
                let asset = open_asset
                    .as_mut()
                    .ok_or_else(|| Error::invalid("NCT2 AssetChunk without AssetBegin"))?;
                if asset.id != asset_id {
                    return Err(Error::invalid(
                        "NCT2 AssetChunk does not belong to the open asset",
                    ));
                }
                if offset != asset.received_bytes {
                    return Err(Error::invalid(
                        "NCT2 AssetChunk offset is not contiguous with received bytes",
                    ));
                }
                if data_len == 0 || data_len > MAX_CHUNK_DATA {
                    return Err(Error::invalid("NCT2 AssetChunk data length is invalid"));
                }
                let remaining = asset
                    .raw_len
                    .checked_sub(asset.received_bytes)
                    .ok_or_else(|| Error::invalid("NCT2 AssetChunk exceeds declared raw length"))?;
                if remaining == 0 {
                    return Err(Error::invalid(
                        "NCT2 AssetChunk follows complete asset data",
                    ));
                }
                if remaining > MAX_CHUNK_DATA as u64 {
                    if data_len != MAX_CHUNK_DATA {
                        return Err(Error::invalid(
                            "NCT2 non-final AssetChunk must carry exactly 4 MiB",
                        ));
                    }
                } else if data_len as u64 != remaining {
                    return Err(Error::invalid(
                        "NCT2 final AssetChunk must carry the remaining 1..4 MiB",
                    ));
                }
                consume_asset_chunk(&mut reader, asset, data_len)?;
                asset.received_bytes = asset
                    .received_bytes
                    .checked_add(data_len as u64)
                    .ok_or_else(|| Error::invalid("NCT2 AssetChunk byte count overflow"))?;
            }
            RECORD_ASSET_END => {
                let payload = bytes_payload(record)?;
                if payload.len() != 44 {
                    return Err(Error::invalid("NCT2 AssetEnd payload must be 44 bytes"));
                }
                let id = read_u32(&payload[0..4]);
                let asset = open_asset
                    .as_ref()
                    .ok_or_else(|| Error::invalid("NCT2 AssetEnd without AssetBegin"))?;
                if asset.id != id {
                    return Err(Error::invalid(
                        "NCT2 AssetEnd does not match the open asset",
                    ));
                }
                let received_bytes = read_u64(&payload[4..12]);
                if received_bytes == 0
                    || received_bytes != asset.raw_len
                    || asset.received_bytes != asset.raw_len
                {
                    return Err(Error::invalid(
                        "NCT2 AssetEnd received byte count differs from declared raw length",
                    ));
                }
                let asset = open_asset.take().expect("checked open asset");
                let computed_sha256: [u8; 32] = asset.hasher.finalize().into();
                let descriptor = asset_descriptors
                    .get(asset.descriptor_index)
                    .ok_or_else(|| Error::invalid("NCT2 open asset descriptor is missing"))?;
                if computed_sha256 != descriptor.declared_sha256 {
                    return Err(Error::invalid(
                        "NCT2 AssetBegin SHA-256 differs from received RGBA pixels",
                    ));
                }
                if computed_sha256 != payload[12..44] {
                    return Err(Error::invalid(
                        "NCT2 AssetEnd SHA-256 differs from received RGBA pixels",
                    ));
                }
                let parsed_asset = ParsedAsset {
                    descriptor: descriptor.clone(),
                    rgba: asset.rgba,
                };
                if let Some(context) = context.as_deref_mut() {
                    let credit = asset.credit.expect("budgeted asset has a credit");
                    context
                        .sender
                        .send_asset(
                            StreamAsset {
                                descriptor: parsed_asset.descriptor,
                                rgba: parsed_asset.rgba,
                            },
                            credit,
                        )
                        .map_err(|error| Error::invalid(error.to_string()))?;
                } else {
                    assets.push(parsed_asset);
                }
                closed_asset_ids.insert(id);
            }
            RECORD_ELEMENT_COMPLETE => {
                if open_asset.is_some() {
                    return Err(Error::invalid(
                        "NCT2 ElementComplete inside an asset transfer",
                    ));
                }
                let payload = bytes_payload(record)?;
                let element = parse_element(
                    &payload,
                    elements.len() as u32,
                    &header,
                    &declarations,
                    exclusive_end,
                    &closed_asset_ids,
                )?;
                total_draw_count = total_draw_count
                    .checked_add(element.draws.len() as u32)
                    .ok_or_else(|| Error::invalid("NCT2 total Draw count overflow"))?;
                if total_draw_count > protocol::MAX_DRAWS {
                    return Err(Error::invalid("NCT2 total Draw count exceeds limit"));
                }
                referenced_asset_ids.extend(element.draws.iter().map(|draw| draw.asset_id));
                elements.push(element);
                if let Some(context) = context.as_deref_mut() {
                    let completed = elements.last().expect("just appended element");
                    let reservation = event_copy_reservation(
                        size_of::<IncrementalEvent>(),
                        completed.draws.len(),
                        size_of::<Draw>(),
                    )?;
                    context.emit_incremental(reservation, || {
                        IncrementalEvent::Element(completed.clone())
                    })?;
                }
            }
            RECORD_WATERMARK => {
                if open_asset.is_some() {
                    return Err(Error::invalid("NCT2 Watermark inside an asset transfer"));
                }
                let payload = bytes_payload(record)?;
                if payload.len() != 12 {
                    return Err(Error::invalid("NCT2 Watermark payload must be 12 bytes"));
                }
                let prefix = read_u32(&payload[0..4]);
                let ready = read_i64(&payload[4..12]);
                let completed_prefix = elements.len() as u32;
                let expected_ready = ready_vpos(&declarations, prefix, exclusive_end);
                if prefix == 0
                    || prefix != completed_prefix
                    || prefix <= last_watermark_prefix
                    || prefix > declarations.len() as u32
                    || ready != expected_ready
                    || ready < last_watermark_ready
                {
                    return Err(Error::invalid(
                        "invalid NCT2 Watermark prefix or ready value",
                    ));
                }
                last_watermark_prefix = prefix;
                last_watermark_ready = ready;
                if let Some(context) = context.as_deref_mut() {
                    context.emit_incremental(size_of::<IncrementalEvent>(), || {
                        IncrementalEvent::Watermark {
                            prefix,
                            ready_vpos: ready,
                        }
                    })?;
                }
            }
            RECORD_END => {
                if open_asset.is_some() {
                    return Err(Error::invalid("NCT2 End inside an asset transfer"));
                }
                if elements.len() != declarations.len() {
                    return Err(Error::invalid(
                        "NCT2 End before every declaration completed",
                    ));
                }
                if closed_asset_ids.len() != asset_descriptors.len() {
                    return Err(Error::invalid("NCT2 End before every asset transfer ended"));
                }
                if asset_descriptors
                    .iter()
                    .any(|asset| !referenced_asset_ids.contains(&asset.id))
                {
                    return Err(Error::invalid("NCT2 End contains an unreferenced asset"));
                }
                let end = parse_end(
                    bytes_payload(record)?,
                    &header,
                    asset_descriptors.len() as u32,
                    total_draw_count,
                    total_raw_asset_bytes,
                    exclusive_end,
                )?;
                let computed_commitment = scene_commitment(
                    &header,
                    &asset_descriptors,
                    &elements,
                    total_raw_asset_bytes,
                    total_draw_count,
                );
                if end.scene_commitment != computed_commitment {
                    return Err(Error::invalid("NCT2 Scene Commitment SHA-256 mismatch"));
                }
                expect_eof(&mut reader)?;
                let parsed = ParsedStream {
                    header,
                    declarations,
                    asset_descriptors,
                    assets,
                    elements,
                    end,
                };
                if let Some(context) = context.as_deref_mut() {
                    let credit = context
                        .metadata_credit
                        .take()
                        .expect("metadata credit held until completion");
                    context
                        .sender
                        .send_complete(Ok(StreamComplete { parsed }), credit)
                        .map_err(|error| Error::invalid(error.to_string()))?;
                    return Ok(None);
                }
                return Ok(Some(parsed));
            }
            _ => {
                return Err(Error::invalid(format!(
                    "unknown NCT2 record type {}",
                    record.kind
                )))
            }
        }
    }
}

fn read_record<R: Read>(reader: &mut R, expected_sequence: u64) -> Result<Option<Record>, Error> {
    let mut envelope = [0u8; ENVELOPE_BYTES];
    if !read_first_byte(reader, &mut envelope[0])? {
        return Ok(None);
    }
    read_exact(reader, &mut envelope[1..], "NCT2 record envelope")?;
    let kind = read_u32(&envelope[0..4]);
    let payload_len = read_u32(&envelope[4..8]);
    let sequence = read_u64(&envelope[8..16]);
    if !(RECORD_HEADER..=RECORD_END).contains(&kind) {
        return Err(Error::invalid(format!("unknown NCT2 record type {kind}")));
    }
    if payload_len > MAX_RECORD_PAYLOAD {
        return Err(Error::invalid(format!(
            "NCT2 payload length {payload_len} exceeds limit"
        )));
    }
    if sequence != expected_sequence {
        return Err(Error::invalid(format!(
            "NCT2 sequence {sequence}, expected {expected_sequence}"
        )));
    }

    let payload = if kind == RECORD_ASSET_CHUNK {
        if payload_len < 13 {
            return Err(Error::invalid(
                "NCT2 AssetChunk must include metadata and pixel data",
            ));
        }
        if payload_len > MAX_CHUNK_DATA + 12 {
            return Err(Error::invalid("NCT2 AssetChunk data exceeds 4 MiB"));
        }
        let mut metadata = [0u8; 12];
        read_exact(reader, &mut metadata, "NCT2 AssetChunk metadata")?;
        let asset_id = read_u32(&metadata[0..4]);
        let offset = read_u64(&metadata[4..12]);
        let data_len = payload_len - metadata.len() as u32;
        Payload::AssetChunk {
            asset_id,
            offset,
            data_len,
        }
    } else {
        let mut payload = Vec::new();
        payload
            .try_reserve_exact(payload_len as usize)
            .map_err(|error| Error::invalid(format!("allocating NCT2 record payload: {error}")))?;
        payload.resize(payload_len as usize, 0);
        read_exact(reader, &mut payload, "NCT2 record payload")?;
        Payload::Bytes(payload)
    };
    Ok(Some(Record {
        kind,
        sequence,
        payload,
    }))
}

fn next_sequence(expected: u64, record: &Record) -> Result<u64, Error> {
    if record.sequence != expected {
        return Err(Error::invalid("NCT2 record sequence changed while parsing"));
    }
    expected
        .checked_add(1)
        .ok_or_else(|| Error::invalid("NCT2 record sequence overflow"))
}

fn bytes_payload(record: Record) -> Result<Vec<u8>, Error> {
    match record.payload {
        Payload::Bytes(payload) => Ok(payload),
        Payload::AssetChunk { .. } => Err(Error::invalid("unexpected NCT2 AssetChunk payload")),
    }
}

fn allocate_asset_pixels(raw_len: u64) -> Result<Vec<u8>, Error> {
    let len = usize::try_from(raw_len)
        .map_err(|_| Error::invalid("NCT2 asset length does not fit address space"))?;
    let mut rgba = Vec::new();
    rgba.try_reserve_exact(len)
        .map_err(|error| Error::invalid(format!("allocating NCT2 asset pixels: {error}")))?;
    rgba.resize(len, 0);
    Ok(rgba)
}

fn consume_asset_chunk<R: Read>(
    reader: &mut R,
    asset: &mut OpenAsset,
    data_len: u32,
) -> Result<(), Error> {
    let start = usize::try_from(asset.received_bytes)
        .map_err(|_| Error::invalid("NCT2 asset offset does not fit address space"))?;
    let end = start
        .checked_add(data_len as usize)
        .filter(|end| *end <= asset.rgba.len())
        .ok_or_else(|| Error::invalid("NCT2 AssetChunk exceeds allocated asset pixels"))?;
    let mut scratch = [0u8; ASSET_CHUNK_SCRATCH_BYTES];
    let mut offset = start;
    while offset < end {
        let take = (end - offset).min(scratch.len());
        read_exact(reader, &mut scratch[..take], "NCT2 AssetChunk pixels")?;
        asset.rgba[offset..offset + take].copy_from_slice(&scratch[..take]);
        asset.hasher.update(&scratch[..take]);
        offset += take;
    }
    Ok(())
}

fn parse_header(payload: Vec<u8>) -> Result<Header, Error> {
    if payload.len() != HEADER_PAYLOAD_BYTES {
        return Err(Error::invalid("NCT2 Header payload must be 80 bytes"));
    }
    if &payload[0..4] != b"NCT2"
        || read_u32(&payload[4..8]) as usize != HEADER_PAYLOAD_BYTES
        || read_u32(&payload[8..12]) != 2
    {
        return Err(Error::invalid("bad NCT2 magic, header size, or version"));
    }
    let declaration_count = read_u32(&payload[32..36]);
    let flags = read_u32(&payload[36..40]);
    if declaration_count > MAX_DECLARATIONS {
        return Err(Error::invalid("NCT2 declaration count exceeds limit"));
    }
    if flags != protocol::FLAGS {
        return Err(Error::invalid(format!("unsupported NCT2 flags {flags:#x}")));
    }
    if payload[72..80].iter().any(|byte| *byte != 0) {
        return Err(Error::invalid("NCT2 Header reserved bytes must be zero"));
    }
    let mut bundle_sha256 = [0u8; 32];
    bundle_sha256.copy_from_slice(&payload[40..72]);
    let header = Header {
        width: read_u32(&payload[12..16]),
        height: read_u32(&payload[16..20]),
        frame_count: read_u32(&payload[20..24]),
        fps_num: read_u32(&payload[24..28]),
        fps_den: read_u32(&payload[28..32]),
        declaration_count,
        flags,
        bundle_sha256,
    };
    validate_header(&header)?;
    Ok(header)
}

fn validate_header(header: &Header) -> Result<(), Error> {
    if header.width == 0
        || header.width > protocol::MAX_WIDTH
        || header.height == 0
        || header.height > protocol::MAX_HEIGHT
    {
        return Err(Error::invalid("NCT2 output dimensions exceed NCT1 limits"));
    }
    if header.frame_count == 0 || header.frame_count > protocol::MAX_FRAMES {
        return Err(Error::invalid("NCT2 frame count exceeds NCT1 limits"));
    }
    frame_vpos(0, header.fps_num, header.fps_den)?;
    frame_vpos(header.frame_count - 1, header.fps_num, header.fps_den)?;
    Ok(())
}

fn parse_declarations(
    payload: Vec<u8>,
    header: &Header,
    exclusive_end: i64,
) -> Result<Vec<Declaration>, Error> {
    if payload.len() < 4 {
        return Err(Error::invalid(
            "NCT2 Declarations payload is shorter than its count",
        ));
    }
    let count = read_u32(&payload[0..4]);
    if count != header.declaration_count || count > MAX_DECLARATIONS {
        return Err(Error::invalid(
            "NCT2 Declarations count differs from Header",
        ));
    }
    let entries_len = (count as usize)
        .checked_mul(DECLARATION_BYTES)
        .ok_or_else(|| Error::invalid("NCT2 Declarations length overflow"))?;
    if payload.len() != 4 + entries_len {
        return Err(Error::invalid("NCT2 Declarations payload length mismatch"));
    }
    let mut declarations = Vec::with_capacity(count as usize);
    for ordinal in 0..count as usize {
        let offset = 4 + ordinal * DECLARATION_BYTES;
        let declaration = Declaration {
            ordinal: read_u32(&payload[offset..offset + 4]),
            owner_order: read_u32(&payload[offset + 4..offset + 8]),
            comment_index: read_u32(&payload[offset + 8..offset + 12]),
            clipped_start: read_i32(&payload[offset + 12..offset + 16]),
            clipped_end: read_i32(&payload[offset + 16..offset + 20]),
        };
        if declaration.ordinal != ordinal as u32 {
            return Err(Error::invalid("NCT2 declaration ordinal is not contiguous"));
        }
        if declaration.clipped_start < 0
            || declaration.clipped_end < declaration.clipped_start
            || declaration.clipped_end as i64 > exclusive_end
        {
            return Err(Error::invalid(
                "NCT2 declaration has invalid clipped interval",
            ));
        }
        if let Some(previous) = declarations.last() {
            let previous: &Declaration = previous;
            if (declaration.owner_order, declaration.comment_index)
                <= (previous.owner_order, previous.comment_index)
            {
                return Err(Error::invalid(
                    "NCT2 declarations are not strictly owner/index ordered",
                ));
            }
        }
        declarations.push(declaration);
    }
    Ok(declarations)
}

fn validate_declarations_complete(
    payload: Vec<u8>,
    header: &Header,
    declarations: &[Declaration],
) -> Result<(), Error> {
    if payload.len() != DECLARATIONS_COMPLETE_BYTES {
        return Err(Error::invalid(
            "NCT2 DeclarationsComplete payload must be 36 bytes",
        ));
    }
    let count = read_u32(&payload[0..4]);
    if count != header.declaration_count || count as usize != declarations.len() {
        return Err(Error::invalid("NCT2 DeclarationsComplete count mismatch"));
    }
    let mut entries = Vec::with_capacity(declarations.len() * DECLARATION_BYTES);
    for declaration in declarations {
        entries.extend_from_slice(&declaration.ordinal.to_le_bytes());
        entries.extend_from_slice(&declaration.owner_order.to_le_bytes());
        entries.extend_from_slice(&declaration.comment_index.to_le_bytes());
        entries.extend_from_slice(&declaration.clipped_start.to_le_bytes());
        entries.extend_from_slice(&declaration.clipped_end.to_le_bytes());
    }
    let digest = Sha256::digest(&entries);
    if digest.as_slice() != &payload[4..36] {
        return Err(Error::invalid("NCT2 DeclarationsComplete SHA-256 mismatch"));
    }
    Ok(())
}

fn parse_element(
    payload: &[u8],
    expected_ordinal: u32,
    header: &Header,
    declarations: &[Declaration],
    exclusive_end: i64,
    closed_asset_ids: &HashSet<u32>,
) -> Result<Element, Error> {
    if payload.len() < 8 {
        return Err(Error::invalid(
            "NCT2 ElementComplete payload is shorter than its header",
        ));
    }
    let ordinal = read_u32(&payload[0..4]);
    let draw_count = read_u32(&payload[4..8]);
    if ordinal != expected_ordinal || ordinal as usize >= declarations.len() {
        return Err(Error::invalid(
            "NCT2 ElementComplete ordinal is missing or out of order",
        ));
    }
    if draw_count > MAX_DRAWS_PER_ELEMENT {
        return Err(Error::invalid(
            "NCT2 ElementComplete Draw count exceeds per-element limit",
        ));
    }
    let draw_bytes = (draw_count as usize)
        .checked_mul(DRAW_BYTES)
        .ok_or_else(|| Error::invalid("NCT2 ElementComplete length overflow"))?;
    if payload.len() != 8 + draw_bytes {
        return Err(Error::invalid(
            "NCT2 ElementComplete payload length mismatch",
        ));
    }
    let declaration = &declarations[ordinal as usize];
    let first_vpos = frame_vpos(0, header.fps_num, header.fps_den)? as i64;
    let last_vpos = frame_vpos(header.frame_count - 1, header.fps_num, header.fps_den)? as i64;
    let mut draws = Vec::with_capacity(draw_count as usize);
    for primitive in 0..draw_count as usize {
        let offset = 8 + primitive * DRAW_BYTES;
        let draw = decode_draw(&payload[offset..offset + DRAW_BYTES]);
        if !closed_asset_ids.contains(&draw.asset_id) {
            return Err(Error::invalid(format!(
                "NCT2 Draw references asset {} before AssetEnd",
                draw.asset_id
            )));
        }
        if draw.owner_order != declaration.owner_order
            || draw.comment_index != declaration.comment_index
        {
            return Err(Error::invalid(
                "NCT2 Draw owner/index differs from declaration",
            ));
        }
        if draw.primitive_index != primitive as u32 {
            return Err(Error::invalid(
                "NCT2 Draw primitive indices are not contiguous",
            ));
        }
        let clipped_start = clip_vpos(draw.start_vpos, exclusive_end);
        let clipped_end = clip_vpos(draw.end_vpos, exclusive_end);
        if clipped_start != declaration.clipped_start as i64
            || clipped_end != declaration.clipped_end as i64
        {
            return Err(Error::invalid(
                "NCT2 Draw clipped interval differs from declaration",
            ));
        }
        validate_draw(&draw, first_vpos, last_vpos)?;
        draws.push(draw);
    }
    Ok(Element { ordinal, draws })
}

fn parse_end(
    payload: Vec<u8>,
    header: &Header,
    asset_count: u32,
    draw_count: u32,
    total_raw_asset_bytes: u64,
    exclusive_end: i64,
) -> Result<End, Error> {
    if payload.len() != END_PAYLOAD_BYTES {
        return Err(Error::invalid("NCT2 End payload must be 64 bytes"));
    }
    let mut scene_commitment = [0u8; 32];
    scene_commitment.copy_from_slice(&payload[32..64]);
    let end = End {
        declaration_count: read_u32(&payload[0..4]),
        asset_count: read_u32(&payload[4..8]),
        draw_count: read_u32(&payload[8..12]),
        total_raw_asset_bytes: read_u64(&payload[12..20]),
        final_prefix: read_u32(&payload[20..24]),
        final_ready_vpos: read_i64(&payload[24..32]),
        scene_commitment,
    };
    if end.declaration_count != header.declaration_count
        || end.asset_count != asset_count
        || end.draw_count != draw_count
        || end.total_raw_asset_bytes != total_raw_asset_bytes
        || end.final_prefix != header.declaration_count
        || end.final_ready_vpos != exclusive_end
    {
        return Err(Error::invalid(
            "NCT2 End counts, byte total, or final watermark mismatch",
        ));
    }
    Ok(end)
}

fn scene_commitment(
    header: &Header,
    asset_descriptors: &[AssetDescriptor],
    elements: &[Element],
    total_raw_asset_bytes: u64,
    draw_count: u32,
) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(b"NCT2-SCENE-DIGEST-v1\0");

    let mut canonical_header = [0u8; protocol::HEADER_BYTES];
    canonical_header[0..4].copy_from_slice(b"NCT1");
    canonical_header[4..8].copy_from_slice(&(protocol::HEADER_BYTES as u32).to_le_bytes());
    canonical_header[8..12].copy_from_slice(&header.width.to_le_bytes());
    canonical_header[12..16].copy_from_slice(&header.height.to_le_bytes());
    canonical_header[16..20].copy_from_slice(&header.frame_count.to_le_bytes());
    canonical_header[20..24].copy_from_slice(&header.fps_num.to_le_bytes());
    canonical_header[24..28].copy_from_slice(&header.fps_den.to_le_bytes());
    canonical_header[28..32].copy_from_slice(&(asset_descriptors.len() as u32).to_le_bytes());
    canonical_header[32..36].copy_from_slice(&draw_count.to_le_bytes());
    canonical_header[36..68].copy_from_slice(&header.bundle_sha256);
    canonical_header[68..76].copy_from_slice(&total_raw_asset_bytes.to_le_bytes());
    canonical_header[76..80].copy_from_slice(&protocol::FLAGS.to_le_bytes());
    hasher.update(canonical_header);

    let mut descriptors: Vec<&AssetDescriptor> = asset_descriptors.iter().collect();
    descriptors.sort_unstable_by_key(|descriptor| descriptor.id);
    for descriptor in descriptors {
        hasher.update(descriptor.id.to_le_bytes());
        hasher.update(descriptor.width.to_le_bytes());
        hasher.update(descriptor.height.to_le_bytes());
        hasher.update(descriptor.raw_len.to_le_bytes());
        hasher.update(descriptor.declared_sha256);
    }

    let mut draw_bytes = [0u8; DRAW_BYTES];
    for element in elements {
        for draw in &element.draws {
            encode_draw(draw, &mut draw_bytes);
            hasher.update(draw_bytes);
        }
    }
    hasher.finalize().into()
}

fn encode_draw(draw: &Draw, bytes: &mut [u8; DRAW_BYTES]) {
    bytes[0..4].copy_from_slice(&draw.asset_id.to_le_bytes());
    bytes[4..8].copy_from_slice(&draw.start_vpos.to_le_bytes());
    bytes[8..12].copy_from_slice(&draw.end_vpos.to_le_bytes());
    bytes[12..16].copy_from_slice(&draw.anchor_vpos.to_le_bytes());
    bytes[16..20].copy_from_slice(&draw.owner_order.to_le_bytes());
    bytes[20..24].copy_from_slice(&draw.comment_index.to_le_bytes());
    bytes[24..28].copy_from_slice(&draw.primitive_index.to_le_bytes());
    for (index, value) in draw.rect.iter().enumerate() {
        let offset = 28 + index * 4;
        bytes[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
    }
    for (index, value) in draw.projection.iter().enumerate() {
        let offset = 44 + index * 4;
        bytes[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
    }
    bytes[108..112].copy_from_slice(&draw.alpha.to_le_bytes());
    bytes[112..120].copy_from_slice(&draw.anchor_x.to_le_bytes());
    bytes[120..128].copy_from_slice(&draw.speed_x.to_le_bytes());
}

fn decode_draw(bytes: &[u8]) -> Draw {
    let mut rect = [0.0f32; 4];
    for (index, value) in rect.iter_mut().enumerate() {
        *value = f32_at(bytes, 28 + index * 4);
    }
    let mut projection = [0.0f32; 16];
    for (index, value) in projection.iter_mut().enumerate() {
        *value = f32_at(bytes, 44 + index * 4);
    }
    Draw {
        asset_id: read_u32(&bytes[0..4]),
        start_vpos: read_i32(&bytes[4..8]),
        end_vpos: read_i32(&bytes[8..12]),
        anchor_vpos: read_i32(&bytes[12..16]),
        owner_order: read_u32(&bytes[16..20]),
        comment_index: read_u32(&bytes[20..24]),
        primitive_index: read_u32(&bytes[24..28]),
        rect,
        projection,
        alpha: f32_at(bytes, 108),
        anchor_x: f64_at(bytes, 112),
        speed_x: f64_at(bytes, 120),
    }
}

fn validate_draw(draw: &Draw, first_vpos: i64, last_vpos: i64) -> Result<(), Error> {
    if draw.start_vpos >= draw.end_vpos {
        return Err(Error::invalid(
            "NCT2 Draw has an empty or reversed interval",
        ));
    }
    if !draw.rect.iter().all(|value| value.is_finite())
        || !draw.projection.iter().all(|value| value.is_finite())
        || !draw.alpha.is_finite()
        || !(0.0..=1.0).contains(&draw.alpha)
        || !draw.anchor_x.is_finite()
        || !draw.speed_x.is_finite()
    {
        return Err(Error::invalid(
            "NCT2 Draw has non-finite or invalid scalar values",
        ));
    }
    let visible_start = (draw.start_vpos as i64).max(first_vpos);
    let visible_end = (draw.end_vpos as i64 - 1).min(last_vpos);
    if visible_start <= visible_end {
        let min_delta = visible_start - draw.anchor_vpos as i64;
        let max_delta = visible_end - draw.anchor_vpos as i64;
        if min_delta < -MAX_EXACT_F32_INT || max_delta > MAX_EXACT_F32_INT {
            return Err(Error::invalid(
                "NCT2 Draw visible vpos delta exceeds exact float32 integer range",
            ));
        }
    }
    Ok(())
}

fn ready_vpos(declarations: &[Declaration], prefix: u32, exclusive_end: i64) -> i64 {
    if prefix as usize >= declarations.len() {
        return exclusive_end;
    }
    declarations[prefix as usize..]
        .iter()
        .map(|declaration| declaration.clipped_start as i64)
        .min()
        .unwrap_or(exclusive_end)
}

fn clip_vpos(value: i32, exclusive_end: i64) -> i64 {
    (value as i64).clamp(0, exclusive_end)
}

fn frame_vpos(frame: u32, fps_num: u32, fps_den: u32) -> Result<i32, Error> {
    protocol::frame_vpos(frame as i64, fps_num, fps_den)
        .map_err(|error| Error::invalid(format!("invalid NCT2 frame clock: {error}")))
}

#[cfg(test)]
mod readiness_vector_tests {
    use super::{event_copy_reservation, frame_vpos, ready_vpos, Declaration};

    const VECTORS: &str =
        include_str!("../../../internal/nicorender/testdata/timeline/stream/vectors.json");

    #[test]
    fn incremental_event_copy_reservation_checks_multiplication_and_addition() {
        assert_eq!(event_copy_reservation(80, 3, 20).unwrap(), 140);
        assert!(event_copy_reservation(0, usize::MAX, 2).is_err());
        assert!(event_copy_reservation(usize::MAX, 1, 1).is_err());
    }

    #[test]
    fn shared_ready_vectors_cover_every_prefix_and_invalid_claim() {
        let vectors: serde_json::Value = serde_json::from_str(VECTORS).unwrap();
        let ready_vectors = vectors["ready_vectors"].as_array().unwrap();
        assert_eq!(ready_vectors.len(), 4);
        for vector in ready_vectors {
            let starts: Vec<i32> = if let Some(starts) = vector["clipped_starts"].as_array() {
                starts
                    .iter()
                    .map(|value| value.as_i64().unwrap() as i32)
                    .collect()
            } else {
                vector["clipped_intervals"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .map(|interval| interval[0].as_i64().unwrap() as i32)
                    .collect()
            };
            let declarations: Vec<Declaration> = starts
                .iter()
                .enumerate()
                .map(|(ordinal, start)| Declaration {
                    ordinal: ordinal as u32,
                    owner_order: 0,
                    comment_index: ordinal as u32,
                    clipped_start: *start,
                    clipped_end: 300,
                })
                .collect();
            let exclusive_end = vector["exclusive_end"].as_i64().unwrap();
            let ready = vector["ready_by_prefix"].as_array().unwrap();
            assert_eq!(ready.len(), declarations.len() + 1);
            for (prefix, expected) in ready.iter().enumerate() {
                if prefix > 0 {
                    assert!(
                        expected.as_i64().unwrap() >= ready[prefix - 1].as_i64().unwrap(),
                        "{} readiness regressed at prefix {prefix}",
                        vector["name"]
                    );
                }
                assert_eq!(
                    ready_vpos(&declarations, prefix as u32, exclusive_end),
                    expected.as_i64().unwrap(),
                    "{} prefix {prefix}",
                    vector["name"]
                );
            }
            if let Some(claims) = vector["invalid_claims"].as_array() {
                for claim in claims {
                    let prefix = claim["prefix"].as_u64().unwrap() as u32;
                    assert_ne!(
                        claim["ready"].as_i64().unwrap(),
                        ready_vpos(&declarations, prefix, exclusive_end),
                        "{} invalid claim at prefix {prefix}",
                        vector["name"]
                    );
                }
            }
        }
    }

    #[test]
    fn shared_boundary_vectors_use_strict_ready_and_fractional_frame_clock() {
        let vectors: serde_json::Value = serde_json::from_str(VECTORS).unwrap();
        for boundary in vectors["boundaries"].as_array().unwrap() {
            match boundary["name"].as_str().unwrap() {
                "exact-start-waits" => {
                    let ready = boundary["ready"].as_i64().unwrap();
                    let frame = boundary["frame_vpos"].as_i64().unwrap();
                    assert_eq!(frame < ready, boundary["may_render"].as_bool().unwrap());
                    let at_start = boundary["at_start_vpos"].as_i64().unwrap();
                    assert_eq!(
                        at_start < ready,
                        boundary["may_render_at_start"].as_bool().unwrap()
                    );
                }
                "draw-end-is-exclusive" => {
                    let interval = boundary["draw_interval"].as_array().unwrap();
                    // T5.5 has no runtime Draw visibility gate; T6.7 will test that path.
                    assert_eq!(interval[0].as_i64().unwrap(), 100);
                    assert_eq!(interval[1].as_i64().unwrap(), 200);
                    assert!(boundary["visible_at_start"].as_bool().unwrap());
                    assert!(!boundary["visible_at_end"].as_bool().unwrap());
                }
                "fractional-fps-60000-1001" => {
                    let frames = boundary["frame_indices"].as_array().unwrap();
                    let expected = boundary["frame_vpos"].as_array().unwrap();
                    assert_eq!(frames.len(), expected.len());
                    for (frame, expected) in frames.iter().zip(expected) {
                        assert_eq!(
                            frame_vpos(frame.as_u64().unwrap() as u32, 60_000, 1_001).unwrap(),
                            expected.as_i64().unwrap() as i32
                        );
                    }
                    assert_eq!(
                        frame_vpos(61, 60_000, 1_001).unwrap(),
                        boundary["exclusive_end_at_frame_61"].as_i64().unwrap() as i32
                    );
                }
                other => panic!("unhandled shared boundary vector {other}"),
            }
        }
    }
}

fn expect_eof<R: Read>(reader: &mut R) -> Result<(), Error> {
    let mut byte = [0u8; 1];
    loop {
        match reader.read(&mut byte) {
            Ok(0) => return Ok(()),
            Ok(_) => return Err(Error::invalid("trailing bytes after NCT2 End")),
            Err(error) if error.kind() == io::ErrorKind::Interrupted => continue,
            Err(error) => return Err(Error::invalid(format!("checking NCT2 EOF: {error}"))),
        }
    }
}

fn read_first_byte<R: Read>(reader: &mut R, byte: &mut u8) -> Result<bool, Error> {
    loop {
        match reader.read(std::slice::from_mut(byte)) {
            Ok(0) => return Ok(false),
            Ok(_) => return Ok(true),
            Err(error) if error.kind() == io::ErrorKind::Interrupted => continue,
            Err(error) => return Err(Error::invalid(format!("reading NCT2 envelope: {error}"))),
        }
    }
}

fn read_exact<R: Read>(reader: &mut R, target: &mut [u8], what: &str) -> Result<(), Error> {
    reader
        .read_exact(target)
        .map_err(|error| Error::invalid(format!("reading {what}: {error}")))
}

fn read_u32(bytes: &[u8]) -> u32 {
    u32::from_le_bytes(bytes.try_into().expect("four-byte field"))
}

fn read_u64(bytes: &[u8]) -> u64 {
    u64::from_le_bytes(bytes.try_into().expect("eight-byte field"))
}

fn read_i32(bytes: &[u8]) -> i32 {
    i32::from_le_bytes(bytes.try_into().expect("four-byte field"))
}

fn read_i64(bytes: &[u8]) -> i64 {
    i64::from_le_bytes(bytes.try_into().expect("eight-byte field"))
}

fn f32_at(bytes: &[u8], offset: usize) -> f32 {
    f32::from_bits(read_u32(&bytes[offset..offset + 4]))
}

fn f64_at(bytes: &[u8], offset: usize) -> f64 {
    f64::from_bits(read_u64(&bytes[offset..offset + 8]))
}
