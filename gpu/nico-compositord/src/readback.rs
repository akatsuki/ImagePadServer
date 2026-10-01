use std::collections::{BTreeMap, VecDeque};
use std::fmt;
use std::io::{self, BufWriter, Write};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, Receiver, TryRecvError, TrySendError};
use std::sync::{Arc, Condvar, Mutex};
use std::thread;
use std::time::Duration;

use serde::Serialize;

use crate::protocol::{Header as ExpectedHeader, Scene};
use crate::renderer::{Renderer, RendererError};
use crate::stream_budget::{CpuBudget, ParserEvent, ParserReceive};
use crate::stream_protocol::{IncrementalEvent, IncrementalStreamParser};
use crate::stream_renderer::{
    GpuBudget, StreamAssetUploader, UploadAttempt, UploadCancellation, UploadDeferredReason,
    WgpuUploadBackend,
};

const COPY_BYTES_PER_ROW_ALIGNMENT: u32 = 256;

#[derive(Debug)]
pub struct ReadbackError(String);

impl fmt::Display for ReadbackError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for ReadbackError {}

impl ReadbackError {
    fn invalid(message: impl Into<String>) -> Self {
        Self(message.into())
    }
}

impl From<RendererError> for ReadbackError {
    fn from(value: RendererError) -> Self {
        Self(format!("rendering timeline frame: {value}"))
    }
}

impl From<io::Error> for ReadbackError {
    fn from(value: io::Error) -> Self {
        Self(format!("writing RGBA readback: {value}"))
    }
}

/// Emits the two NCT2 lifecycle status markers at their verified transition
/// points. The writer is owned here, so marker output stays on the status
/// channel and never shares the RGBA output stream.
#[doc(hidden)]
pub struct TimelineStatusEmitter<W: Write> {
    writer: W,
    first_asset_ready: bool,
    stream_end: bool,
}

impl<W: Write> TimelineStatusEmitter<W> {
    #[doc(hidden)]
    pub fn new(writer: W) -> Self {
        Self {
            writer,
            first_asset_ready: false,
            stream_end: false,
        }
    }

    #[doc(hidden)]
    pub fn observe_upload_attempt<E>(&mut self, attempt: &UploadAttempt<E>) -> io::Result<()> {
        if matches!(attempt, UploadAttempt::Submitted { .. }) && !self.first_asset_ready {
            writeln!(self.writer, "NICO_FIRST_ASSET_READY")?;
            self.writer.flush()?;
            self.first_asset_ready = true;
        }
        Ok(())
    }

    #[doc(hidden)]
    pub fn observe_parser_event(&mut self, event: &IncrementalEvent) -> io::Result<()> {
        if matches!(event, IncrementalEvent::Complete(Ok(_))) && !self.stream_end {
            writeln!(self.writer, "NICO_STREAM_END")?;
            self.writer.flush()?;
            self.stream_end = true;
        }
        Ok(())
    }

    #[doc(hidden)]
    pub fn into_inner(self) -> W {
        self.writer
    }
}

/// Receive the parser-validated first NCT2 Header while retaining its event
/// credit. The caller must pass this exact event to `stream_incremental_frames`
/// so the scheduler still performs its normal Header contract check.
pub fn receive_incremental_header(
    parser: &IncrementalStreamParser,
) -> Result<ParserEvent<IncrementalEvent>, ReadbackError> {
    loop {
        match parser
            .recv_timeout(Duration::from_millis(100))
            .map_err(|error| ReadbackError::invalid(format!("reading NCT2 Header: {error}")))?
        {
            ParserReceive::Event(event) => match event.payload.get() {
                IncrementalEvent::Header(_) => return Ok(event),
                IncrementalEvent::Complete(Err(error)) => {
                    return Err(ReadbackError::invalid(format!(
                        "NCT2 input failed before Header: {error}"
                    )))
                }
                _ => {
                    return Err(ReadbackError::invalid(
                        "NCT2 parser emitted a non-Header event first",
                    ))
                }
            },
            ParserReceive::Closed => {
                return Err(ReadbackError::invalid(
                    "NCT2 parser closed before the initial Header",
                ))
            }
            ParserReceive::Empty | ParserReceive::TimedOut => {}
        }
    }
}

/// One nonblocking result from the incremental parser owner.
#[derive(Debug)]
pub enum CoordinatorInput {
    Empty,
    Header {
        frame_count: u32,
        fps_num: u32,
        fps_den: u32,
    },
    Watermark {
        ready_vpos: i64,
    },
    /// A verified asset is held because shared GPU credits are unavailable.
    DeferredUpload,
    Complete,
    Failed(String),
}

/// A completed asynchronous readback map, possibly delivered out of order.
#[derive(Debug)]
pub enum CoordinatorMapEvent {
    Mapped {
        sequence: u32,
        slot: usize,
    },
    Failed {
        sequence: u32,
        slot: usize,
        error: String,
    },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CoordinatorWriteStatus {
    Queued,
    Full,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CoordinatorWriteAck {
    Flushed { sequence: u32, slot: usize },
}

/// The single owner that advances input, GPU/map polling, rendering, and
/// ordered output. Slot credits represent persistent readback allocations
/// and live for the lifetime of the coordinator's ring.
pub trait StreamReadbackDriver {
    type SlotCredit;

    fn slot_count(&self) -> usize;
    fn reserve_slot(&mut self, slot: usize) -> Result<Self::SlotCredit, ReadbackError>;
    /// Number of asset pages successfully uploaded and registered resident.
    /// Declarations and deferred, never-submitted assets are not counted.
    fn registered_asset_page_count(&self) -> usize;
    /// Observed output queue capacity and occupancy after the writer has joined.
    fn output_queue_metrics(&self) -> Option<WriterQueueMetrics> {
        None
    }
    fn try_receive_incremental(&mut self) -> Result<CoordinatorInput, ReadbackError>;
    fn poll_gpu_and_maps(&mut self) -> Result<Vec<CoordinatorMapEvent>, ReadbackError>;
    fn submit_frame(&mut self, sequence: u32, slot: usize) -> Result<(), ReadbackError>;
    fn try_queue_write(
        &mut self,
        sequence: u32,
        slot: usize,
    ) -> Result<CoordinatorWriteStatus, ReadbackError>;
    /// Cancel a mapped slot that has not been transferred to the writer.
    fn unmap_readback(&mut self, slot: usize) -> Result<(), ReadbackError>;
    fn try_writer_ack(&mut self) -> Result<Option<CoordinatorWriteAck>, ReadbackError>;
    fn unblock_input(&mut self);
    fn unblock_output(&mut self);
    fn join_input(&mut self) -> Result<(), ReadbackError>;
    fn join_writer(&mut self) -> Result<(), ReadbackError>;
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct StreamReadbackCompletion {
    pub asset_page_count: usize,
    pub output_queue_metrics: Option<WriterQueueMetrics>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum CoordinatorSlotState {
    Free,
    Submitted(u32),
    Mapped(u32),
    Writing(u32),
}

struct CoordinatorSlot<C> {
    state: CoordinatorSlotState,
    _allocation_credit: C,
}

/// Coordinates one bounded readback ring. Each slot transitions through
/// Free, Submitted, Mapped, Writing, and Free; its allocation credit stays
/// attached to the physical slot across frames and flush acknowledgements.
pub struct StreamReadbackCoordinator<D: StreamReadbackDriver> {
    driver: D,
    slots: Vec<CoordinatorSlot<D::SlotCredit>>,
    mapped_ready: BTreeMap<u32, usize>,
    frame_count: Option<u32>,
    fps_num: u32,
    fps_den: u32,
    ready_vpos: Option<i64>,
    complete: bool,
    deferred_upload: bool,
    next_frame: u32,
    next_write: u32,
    next_ack: u32,
    closed: bool,
}

impl<D: StreamReadbackDriver> StreamReadbackCoordinator<D> {
    pub fn new(mut driver: D) -> Result<Self, ReadbackError> {
        let slot_count = driver.slot_count();
        if !(1..=3).contains(&slot_count) {
            return Err(ReadbackError::invalid("readback slots must be 1, 2, or 3"));
        }
        let mut slots = Vec::with_capacity(slot_count);
        for slot in 0..slot_count {
            let allocation_credit = driver.reserve_slot(slot)?;
            slots.push(CoordinatorSlot {
                state: CoordinatorSlotState::Free,
                _allocation_credit: allocation_credit,
            });
        }
        Ok(Self {
            driver,
            slots,
            mapped_ready: BTreeMap::new(),
            frame_count: None,
            fps_num: 0,
            fps_den: 0,
            ready_vpos: None,
            complete: false,
            deferred_upload: false,
            next_frame: 0,
            next_write: 0,
            next_ack: 0,
            closed: false,
        })
    }

    /// Poll each nonblocking owner once, then advance ordered writes and
    /// submit only frames strictly below the current applied watermark.
    /// Returns true after parser completion, all frames, and all flush acks.
    pub fn step(&mut self) -> Result<bool, ReadbackError> {
        if self.closed {
            return Ok(true);
        }
        let mut progressed = false;

        for event in self.driver.poll_gpu_and_maps()? {
            progressed = true;
            match event {
                CoordinatorMapEvent::Mapped { sequence, slot } => {
                    let entry = self.slots.get_mut(slot).ok_or_else(|| {
                        ReadbackError::invalid("map callback returned bad slot id")
                    })?;
                    if entry.state != CoordinatorSlotState::Submitted(sequence) {
                        return Err(ReadbackError::invalid(format!(
                            "slot {slot} mapped out of state {:?} for sequence {sequence}",
                            entry.state
                        )));
                    }
                    entry.state = CoordinatorSlotState::Mapped(sequence);
                    if self.mapped_ready.insert(sequence, slot).is_some() {
                        return Err(ReadbackError::invalid(format!(
                            "duplicate map callback for sequence {sequence}"
                        )));
                    }
                }
                CoordinatorMapEvent::Failed {
                    sequence,
                    slot,
                    error,
                } => {
                    return Err(ReadbackError::invalid(format!(
                        "mapping frame {sequence} in slot {slot}: {error}"
                    )));
                }
            }
        }

        if let Some(CoordinatorWriteAck::Flushed { sequence, slot }) =
            self.driver.try_writer_ack()?
        {
            progressed = true;
            if sequence != self.next_ack {
                return Err(ReadbackError::invalid(format!(
                    "writer flushed frame {sequence}; expected {}",
                    self.next_ack
                )));
            }
            let entry = self
                .slots
                .get_mut(slot)
                .ok_or_else(|| ReadbackError::invalid("writer returned bad slot id"))?;
            if entry.state != CoordinatorSlotState::Writing(sequence) {
                return Err(ReadbackError::invalid(format!(
                    "slot {slot} released out of state {:?} for sequence {sequence}",
                    entry.state
                )));
            }
            entry.state = CoordinatorSlotState::Free;
            self.next_ack += 1;
        }

        if !self.complete {
            match self.driver.try_receive_incremental()? {
                CoordinatorInput::Empty => self.deferred_upload = false,
                CoordinatorInput::DeferredUpload => self.deferred_upload = true,
                CoordinatorInput::Header {
                    frame_count,
                    fps_num,
                    fps_den,
                } => {
                    self.deferred_upload = false;
                    progressed = true;
                    if self.frame_count.is_some()
                        || frame_count == 0
                        || fps_num == 0
                        || fps_den == 0
                    {
                        return Err(ReadbackError::invalid(
                            "invalid or duplicate incremental readback Header",
                        ));
                    }
                    self.frame_count = Some(frame_count);
                    self.fps_num = fps_num;
                    self.fps_den = fps_den;
                }
                CoordinatorInput::Watermark { ready_vpos } => {
                    self.deferred_upload = false;
                    progressed = true;
                    if self.frame_count.is_none()
                        || self.ready_vpos.is_some_and(|prior| ready_vpos < prior)
                    {
                        return Err(ReadbackError::invalid(
                            "invalid or decreasing readback Watermark",
                        ));
                    }
                    self.ready_vpos = Some(ready_vpos);
                }
                CoordinatorInput::Complete => {
                    self.deferred_upload = false;
                    progressed = true;
                    if self.frame_count.is_none() {
                        return Err(ReadbackError::invalid(
                            "incremental stream completed before Header",
                        ));
                    }
                    if self.ready_vpos.is_none() {
                        // A parser-validated zero-declaration stream has no
                        // Element Watermark records. End+EOF supplies its
                        // final exclusive frame boundary.
                        let frame_count = self.frame_count.expect("checked above");
                        let end_vpos = crate::protocol::frame_vpos(
                            i64::from(frame_count),
                            self.fps_num,
                            self.fps_den,
                        )
                        .map_err(|error| {
                            ReadbackError::invalid(format!("final frame clock: {error}"))
                        })?;
                        self.ready_vpos = Some(i64::from(end_vpos));
                    }
                    self.complete = true;
                }
                CoordinatorInput::Failed(error) => {
                    self.deferred_upload = false;
                    return Err(ReadbackError::invalid(format!(
                        "incremental parser failed: {error}"
                    )));
                }
            }
        }

        self.queue_ordered_writes(&mut progressed)?;
        self.submit_ready_frames(&mut progressed)?;
        if !self.complete
            && self.deferred_upload
            && !self
                .slots
                .iter()
                .any(|slot| matches!(slot.state, CoordinatorSlotState::Submitted(_)))
        {
            return Err(ReadbackError::invalid(
                "GPU upload credits unavailable and no in-flight GPU completion can release them",
            ));
        }
        if self.is_complete() {
            self.driver.join_input()?;
            self.driver.join_writer()?;
            self.closed = true;
            return Ok(true);
        }
        if !progressed {
            thread::sleep(Duration::from_millis(1));
        }
        Ok(false)
    }

    pub fn run(&mut self) -> Result<StreamReadbackCompletion, ReadbackError> {
        loop {
            match self.step() {
                Ok(true) => {
                    return Ok(StreamReadbackCompletion {
                        asset_page_count: self.driver.registered_asset_page_count(),
                        output_queue_metrics: self.driver.output_queue_metrics(),
                    })
                }
                Ok(false) => {}
                Err(error) => {
                    return match self.cancel_and_join() {
                        Ok(()) => Err(error),
                        Err(cleanup) => Err(ReadbackError::invalid(format!(
                            "{error}; cancellation cleanup failed: {cleanup}"
                        ))),
                    };
                }
            }
        }
    }

    /// Invoke both unblock owners before either join. Failed joins propagate;
    /// cancellation never treats a detached worker as successful cleanup.
    pub fn cancel_and_join(&mut self) -> Result<(), ReadbackError> {
        self.driver.unblock_input();
        self.driver.unblock_output();
        let mut first_error = None;
        for (slot, entry) in self.slots.iter_mut().enumerate() {
            if matches!(entry.state, CoordinatorSlotState::Mapped(_)) {
                if let Err(error) = self.driver.unmap_readback(slot) {
                    first_error.get_or_insert(error);
                }
            }
            entry.state = CoordinatorSlotState::Free;
        }
        if let Err(error) = self.driver.join_input() {
            first_error.get_or_insert(error);
        }
        if let Err(error) = self.driver.join_writer() {
            first_error.get_or_insert(error);
        }
        self.closed = true;
        first_error.map_or(Ok(()), Err)
    }

    fn queue_ordered_writes(&mut self, progressed: &mut bool) -> Result<(), ReadbackError> {
        loop {
            let Some(&slot_index) = self.mapped_ready.get(&self.next_write) else {
                return Ok(());
            };
            let slot = &mut self.slots[slot_index];
            if slot.state != CoordinatorSlotState::Mapped(self.next_write) {
                return Err(ReadbackError::invalid(format!(
                    "slot {slot_index} is not mapped for sequence {}",
                    self.next_write
                )));
            }
            let status = self.driver.try_queue_write(self.next_write, slot_index)?;
            if status == CoordinatorWriteStatus::Full {
                return Ok(());
            }
            slot.state = CoordinatorSlotState::Writing(self.next_write);
            self.mapped_ready.remove(&self.next_write);
            self.next_write += 1;
            *progressed = true;
        }
    }

    fn submit_ready_frames(&mut self, progressed: &mut bool) -> Result<(), ReadbackError> {
        let Some(frame_count) = self.frame_count else {
            return Ok(());
        };
        let Some(ready_vpos) = self.ready_vpos else {
            return Ok(());
        };
        while self.next_frame < frame_count {
            let frame_vpos =
                crate::protocol::frame_vpos(self.next_frame as i64, self.fps_num, self.fps_den)
                    .map_err(|error| ReadbackError::invalid(format!("frame clock: {error}")))?;
            if i64::from(frame_vpos) >= ready_vpos {
                break;
            }
            let Some(slot_index) = self
                .slots
                .iter()
                .position(|slot| slot.state == CoordinatorSlotState::Free)
            else {
                break;
            };
            self.driver.submit_frame(self.next_frame, slot_index)?;
            self.slots[slot_index].state = CoordinatorSlotState::Submitted(self.next_frame);
            self.next_frame += 1;
            *progressed = true;
        }
        Ok(())
    }

    fn is_complete(&self) -> bool {
        self.complete
            && self.frame_count == Some(self.next_frame)
            && self.next_ack == self.next_frame
            && self
                .slots
                .iter()
                .all(|slot| slot.state == CoordinatorSlotState::Free)
    }
}

impl<D: StreamReadbackDriver> Drop for StreamReadbackCoordinator<D> {
    fn drop(&mut self) {
        if !self.closed {
            let _ = self.cancel_and_join();
        }
    }
}

/// Return the WGPU copy row pitch for tightly packed RGBA8 pixels.
pub fn padded_bytes_per_row(width: u32) -> Result<u32, ReadbackError> {
    if width == 0 {
        return Err(ReadbackError("readback width must be positive".into()));
    }
    let row_bytes = width
        .checked_mul(4)
        .ok_or_else(|| ReadbackError("RGBA row byte count overflow".into()))?;
    let aligned = row_bytes
        .checked_add(COPY_BYTES_PER_ROW_ALIGNMENT - 1)
        .ok_or_else(|| ReadbackError("padded RGBA row byte count overflow".into()))?
        & !(COPY_BYTES_PER_ROW_ALIGNMENT - 1);
    Ok(aligned)
}

/// Write padded GPU readback rows as tightly packed RGBA8 bytes.
pub fn write_padded_rows<W: Write>(
    writer: &mut W,
    mapped: &[u8],
    width: u32,
    height: u32,
    bytes_per_row: u32,
) -> Result<(), ReadbackError> {
    if width == 0 || height == 0 {
        return Err(ReadbackError("readback dimensions must be positive".into()));
    }
    let row_bytes = width
        .checked_mul(4)
        .ok_or_else(|| ReadbackError("RGBA row byte count overflow".into()))?;
    if bytes_per_row < row_bytes || bytes_per_row % COPY_BYTES_PER_ROW_ALIGNMENT != 0 {
        return Err(ReadbackError("invalid padded RGBA row pitch".into()));
    }
    let expected_bytes = (bytes_per_row as usize)
        .checked_mul(height as usize)
        .ok_or_else(|| ReadbackError("mapped readback byte count overflow".into()))?;
    if mapped.len() < expected_bytes {
        return Err(ReadbackError(format!(
            "mapped readback buffer is shorter than declared rows: {} < {expected_bytes}",
            mapped.len()
        )));
    }
    if bytes_per_row == row_bytes {
        writer.write_all(&mapped[..expected_bytes])?;
        return Ok(());
    }
    for row in 0..height as usize {
        let start = row * bytes_per_row as usize;
        writer.write_all(&mapped[start..start + row_bytes as usize])?;
    }
    Ok(())
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum SlotState {
    Free,
    Submitted(u32),
    Mapped(u32),
    Writing(u32),
}

struct Slot {
    buffer: Arc<wgpu::Buffer>,
    state: SlotState,
}

struct MapComplete {
    slot: usize,
    sequence: u32,
    result: Result<(), String>,
    _keep_alive: Option<(
        Arc<crate::stream_renderer::GpuCredit>,
        Arc<crate::stream_renderer::GpuCredit>,
    )>,
}

struct FrameWritten {
    slot: usize,
    sequence: u32,
    error: Option<String>,
}

enum WriterMessage {
    Frame {
        slot: usize,
        sequence: u32,
        buffer: Arc<wgpu::Buffer>,
    },
    Stop,
}

enum WriterEvent {
    Frame(FrameWritten),
    Finished(Result<(), String>),
}

/// Queue depth counts pending frame messages, excluding control messages and
/// the message the writer has already dequeued. A successful stream reports
/// this snapshot after the writer has joined.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WriterQueueMetrics {
    pub capacity: usize,
    #[serde(rename = "finalOccupancy")]
    pub occupancy: usize,
    pub high_water: usize,
}

struct BoundedOutputQueueState<T> {
    messages: VecDeque<(T, bool)>,
    capacity: usize,
    occupancy: usize,
    high_water: usize,
    sender_open: bool,
    receiver_open: bool,
}

struct BoundedOutputQueueInner<T> {
    state: Mutex<BoundedOutputQueueState<T>>,
    message_available: Condvar,
    space_available: Condvar,
}

struct BoundedOutputSender<T> {
    inner: Arc<BoundedOutputQueueInner<T>>,
}

struct BoundedOutputReceiver<T> {
    inner: Arc<BoundedOutputQueueInner<T>>,
}

#[derive(Debug, PartialEq, Eq)]
enum BoundedTrySendError<T> {
    Full(T),
    Disconnected(T),
}

#[derive(Debug, PartialEq, Eq)]
struct BoundedRecvError;

fn bounded_output_channel<T>(
    capacity: usize,
) -> (BoundedOutputSender<T>, BoundedOutputReceiver<T>) {
    assert!(
        capacity > 0,
        "bounded output queue capacity must be positive"
    );
    let inner = Arc::new(BoundedOutputQueueInner {
        state: Mutex::new(BoundedOutputQueueState {
            messages: VecDeque::with_capacity(capacity),
            capacity,
            occupancy: 0,
            high_water: 0,
            sender_open: true,
            receiver_open: true,
        }),
        message_available: Condvar::new(),
        space_available: Condvar::new(),
    });
    (
        BoundedOutputSender {
            inner: Arc::clone(&inner),
        },
        BoundedOutputReceiver { inner },
    )
}

impl<T> BoundedOutputSender<T> {
    fn try_send(
        &self,
        message: T,
        count_toward_occupancy: bool,
    ) -> Result<(), BoundedTrySendError<T>> {
        let mut state = self
            .inner
            .state
            .lock()
            .unwrap_or_else(|poison| poison.into_inner());
        if !state.receiver_open {
            return Err(BoundedTrySendError::Disconnected(message));
        }
        if state.messages.len() >= state.capacity {
            return Err(BoundedTrySendError::Full(message));
        }
        state.messages.push_back((message, count_toward_occupancy));
        if count_toward_occupancy {
            state.occupancy += 1;
            state.high_water = state.high_water.max(state.occupancy);
        }
        self.inner.message_available.notify_one();
        Ok(())
    }

    fn send(&self, message: T, count_toward_occupancy: bool) -> Result<(), T> {
        let mut state = self
            .inner
            .state
            .lock()
            .unwrap_or_else(|poison| poison.into_inner());
        let mut message = Some(message);
        loop {
            if !state.receiver_open {
                return Err(message.take().expect("message is present until sent"));
            }
            if state.messages.len() < state.capacity {
                state.messages.push_back((
                    message.take().expect("message is present until sent"),
                    count_toward_occupancy,
                ));
                if count_toward_occupancy {
                    state.occupancy += 1;
                    state.high_water = state.high_water.max(state.occupancy);
                }
                self.inner.message_available.notify_one();
                return Ok(());
            }
            state = self
                .inner
                .space_available
                .wait(state)
                .unwrap_or_else(|poison| poison.into_inner());
        }
    }
}

impl<T> Drop for BoundedOutputSender<T> {
    fn drop(&mut self) {
        let mut state = self
            .inner
            .state
            .lock()
            .unwrap_or_else(|poison| poison.into_inner());
        state.sender_open = false;
        self.inner.message_available.notify_all();
    }
}

impl<T> BoundedOutputReceiver<T> {
    fn recv(&self) -> Result<T, BoundedRecvError> {
        let mut state = self
            .inner
            .state
            .lock()
            .unwrap_or_else(|poison| poison.into_inner());
        loop {
            if let Some((message, count_toward_occupancy)) = state.messages.pop_front() {
                if count_toward_occupancy {
                    state.occupancy -= 1;
                }
                self.inner.space_available.notify_one();
                return Ok(message);
            }
            if !state.sender_open {
                return Err(BoundedRecvError);
            }
            state = self
                .inner
                .message_available
                .wait(state)
                .unwrap_or_else(|poison| poison.into_inner());
        }
    }
}

impl<T> Drop for BoundedOutputReceiver<T> {
    fn drop(&mut self) {
        let mut state = self
            .inner
            .state
            .lock()
            .unwrap_or_else(|poison| poison.into_inner());
        state.receiver_open = false;
        state.messages.clear();
        state.occupancy = 0;
        self.inner.space_available.notify_all();
    }
}

fn bounded_output_queue_metrics<T>(inner: &BoundedOutputQueueInner<T>) -> WriterQueueMetrics {
    let state = inner
        .state
        .lock()
        .unwrap_or_else(|poison| poison.into_inner());
    WriterQueueMetrics {
        capacity: state.capacity,
        occupancy: state.occupancy,
        high_water: state.high_water,
    }
}

trait WriterMessageSource {
    fn recv_message(&self) -> Result<WriterMessage, ()>;
}

impl WriterMessageSource for Receiver<WriterMessage> {
    fn recv_message(&self) -> Result<WriterMessage, ()> {
        self.recv().map_err(|_| ())
    }
}

impl WriterMessageSource for BoundedOutputReceiver<WriterMessage> {
    fn recv_message(&self) -> Result<WriterMessage, ()> {
        self.recv().map_err(|_| ())
    }
}

/// Stream complete frames through a bounded readback ring and a single writer.
/// Mapping views never leave the writer thread, and a slot is returned only
/// after row padding has been removed and the buffer has been unmapped.
pub fn stream_frames<W: Write + Send + 'static>(
    renderer: &mut Renderer,
    scene: &Scene,
    readback_slots: usize,
    output: W,
    emit_progress: bool,
    completed_frames: &mut u32,
) -> Result<(), ReadbackError> {
    if !(1..=3).contains(&readback_slots) {
        return Err(ReadbackError::invalid("readback slots must be 1, 2, or 3"));
    }
    if scene.header.width == 0 || scene.header.height == 0 || scene.header.frame_count == 0 {
        return Err(ReadbackError::invalid(
            "invalid scene dimensions or frame count",
        ));
    }
    *completed_frames = 0;
    let width = scene.header.width;
    let height = scene.header.height;
    let frame_count = scene.header.frame_count;
    let padded_row_bytes = padded_bytes_per_row(width)?;
    let buffer_size = (padded_row_bytes as u64)
        .checked_mul(height as u64)
        .ok_or_else(|| ReadbackError::invalid("readback buffer size overflow"))?;

    let target = renderer.device().create_texture(&wgpu::TextureDescriptor {
        label: Some("NCT1 RGBA output frame"),
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
    let slots = (0..readback_slots)
        .map(|index| Slot {
            buffer: Arc::new(renderer.device().create_buffer(&wgpu::BufferDescriptor {
                label: Some(&format!("NCT1 readback slot {index}")),
                size: buffer_size,
                usage: wgpu::BufferUsages::COPY_DST | wgpu::BufferUsages::MAP_READ,
                mapped_at_creation: false,
            })),
            state: SlotState::Free,
        })
        .collect::<Vec<_>>();

    let (map_tx, map_rx) = mpsc::channel::<MapComplete>();
    let (writer_tx, writer_rx) = mpsc::sync_channel::<WriterMessage>(2);
    let (writer_event_tx, writer_event_rx) = mpsc::channel::<WriterEvent>();
    let writer = thread::spawn(move || {
        writer_loop(
            output,
            writer_rx,
            writer_event_tx,
            width,
            height,
            padded_row_bytes,
            frame_count,
            emit_progress,
        )
    });

    let mut slots = slots;
    let mut mapped_ready = BTreeMap::<u32, usize>::new();
    let mut next_frame = 0u32;
    let mut next_writer_sequence = 0u32;
    let mut failure: Option<ReadbackError> = None;

    while *completed_frames < frame_count && failure.is_none() {
        renderer.device().poll(wgpu::Maintain::Poll);
        let mut progressed = false;

        while let Ok(event) = map_rx.try_recv() {
            progressed = true;
            match event.result {
                Ok(()) => {
                    let Some(slot) = slots.get_mut(event.slot) else {
                        failure = Some(ReadbackError::invalid("map callback returned bad slot id"));
                        break;
                    };
                    if slot.state != SlotState::Submitted(event.sequence) {
                        failure = Some(ReadbackError::invalid(format!(
                            "slot {} mapped out of state {:?}",
                            event.slot, slot.state
                        )));
                        break;
                    }
                    slot.state = SlotState::Mapped(event.sequence);
                    mapped_ready.insert(event.sequence, event.slot);
                }
                Err(error) => {
                    failure = Some(ReadbackError::invalid(format!(
                        "mapping frame {} readback: {error}",
                        event.sequence
                    )));
                    break;
                }
            }
        }

        while let Ok(event) = writer_event_rx.try_recv() {
            progressed = true;
            match event {
                WriterEvent::Frame(done) => {
                    let Some(slot) = slots.get_mut(done.slot) else {
                        failure = Some(ReadbackError::invalid("writer returned bad slot id"));
                        break;
                    };
                    if slot.state != SlotState::Writing(done.sequence) {
                        failure = Some(ReadbackError::invalid(format!(
                            "slot {} released out of state {:?}",
                            done.slot, slot.state
                        )));
                        break;
                    }
                    slot.state = SlotState::Free;
                    if let Some(error) = done.error {
                        failure = Some(ReadbackError::invalid(format!(
                            "writing frame {}: {error}",
                            done.sequence
                        )));
                        break;
                    }
                    *completed_frames += 1;
                }
                WriterEvent::Finished(result) => {
                    if let Err(error) = result {
                        failure = Some(ReadbackError::invalid(error));
                    }
                }
            }
        }

        while failure.is_none() {
            let Some(&slot_index) = mapped_ready.get(&next_writer_sequence) else {
                break;
            };
            let slot = &mut slots[slot_index];
            if slot.state != SlotState::Mapped(next_writer_sequence) {
                failure = Some(ReadbackError::invalid(format!(
                    "slot {slot_index} is not mapped for sequence {next_writer_sequence}"
                )));
                break;
            }
            slot.state = SlotState::Writing(next_writer_sequence);
            let message = WriterMessage::Frame {
                slot: slot_index,
                sequence: next_writer_sequence,
                buffer: Arc::clone(&slot.buffer),
            };
            match writer_tx.try_send(message) {
                Ok(()) => {
                    mapped_ready.remove(&next_writer_sequence);
                    next_writer_sequence += 1;
                    progressed = true;
                }
                Err(TrySendError::Full(WriterMessage::Frame { .. })) => {
                    slot.state = SlotState::Mapped(next_writer_sequence);
                    break;
                }
                Err(TrySendError::Full(WriterMessage::Stop)) => unreachable!(),
                Err(TrySendError::Disconnected(_)) => {
                    slot.state = SlotState::Mapped(next_writer_sequence);
                    failure = Some(ReadbackError::invalid("RGBA writer stopped unexpectedly"));
                    break;
                }
            }
        }

        if failure.is_some() {
            break;
        }

        if next_frame < frame_count {
            if let Some(slot_index) = slots.iter().position(|slot| slot.state == SlotState::Free) {
                let sequence = next_frame;
                let slot = &mut slots[slot_index];
                slot.state = SlotState::Submitted(sequence);
                if let Err(error) = renderer.submit_frame_with_readback(
                    sequence,
                    &target,
                    &slot.buffer,
                    padded_row_bytes,
                ) {
                    slot.state = SlotState::Free;
                    failure = Some(error.into());
                    break;
                }
                let callback_tx = map_tx.clone();
                let buffer = Arc::clone(&slot.buffer);
                buffer
                    .slice(..)
                    .map_async(wgpu::MapMode::Read, move |result| {
                        let _ = callback_tx.send(MapComplete {
                            slot: slot_index,
                            sequence,
                            result: result.map_err(|error| error.to_string()),
                            _keep_alive: None,
                        });
                    });
                next_frame += 1;
                progressed = true;
            }
        }

        if !progressed {
            thread::sleep(Duration::from_millis(1));
        }
    }

    if let Some(error) = failure {
        // The caller owns process-level cancellation. Detaching here avoids
        // waiting forever if stdout is deliberately back-pressured.
        drop(writer_tx);
        drop(writer);
        return Err(error);
    }

    writer_tx
        .send(WriterMessage::Stop)
        .map_err(|_| ReadbackError::invalid("RGBA writer stopped before flush"))?;
    drop(writer_tx);
    let writer_result = loop {
        match writer_event_rx.recv() {
            Ok(WriterEvent::Finished(result)) => break result,
            Ok(WriterEvent::Frame(_)) => continue,
            Err(_) => break Err("RGBA writer exited without flush status".into()),
        }
    };
    writer
        .join()
        .map_err(|_| ReadbackError::invalid("RGBA writer thread panicked"))?;
    writer_result.map_err(ReadbackError::invalid)
}

fn writer_loop<W: Write, M: WriterMessageSource>(
    output: W,
    messages: M,
    events: mpsc::Sender<WriterEvent>,
    width: u32,
    height: u32,
    bytes_per_row: u32,
    total_frames: u32,
    emit_progress: bool,
) {
    // Keep scanline writes in a bounded buffer so they do not become one OS
    // write call per row. Flush before progress so it tracks pipe delivery.
    let mut output = BufWriter::with_capacity(1024 * 1024, output);
    let mut next_sequence = 0u32;
    let mut first_error: Option<String> = None;
    while let Ok(message) = messages.recv_message() {
        match message {
            WriterMessage::Frame {
                slot,
                sequence,
                buffer,
            } => {
                let mut frame_error = first_error.clone();
                if frame_error.is_none() && sequence != next_sequence {
                    frame_error = Some(format!(
                        "out-of-order frame {sequence}; expected {next_sequence}"
                    ));
                }
                if frame_error.is_none() {
                    let mapped = buffer.slice(..).get_mapped_range();
                    let write_result =
                        write_padded_rows(&mut output, &mapped, width, height, bytes_per_row);
                    drop(mapped);
                    if let Err(error) = write_result {
                        frame_error = Some(error.to_string());
                    } else if let Err(error) = output.flush() {
                        frame_error = Some(format!("flushing RGBA frame: {error}"));
                    }
                }
                // The mapped view is always dropped before unmap. A failed
                // output still releases the slot and drains bounded messages.
                buffer.unmap();
                if let Some(error) = &frame_error {
                    if first_error.is_none() {
                        first_error = Some(error.clone());
                    }
                } else if emit_progress {
                    let _ = writeln!(
                        io::stderr().lock(),
                        "NICO_PROGRESS {} {}",
                        sequence + 1,
                        total_frames
                    );
                }
                next_sequence = sequence.saturating_add(1);
                if events
                    .send(WriterEvent::Frame(FrameWritten {
                        slot,
                        sequence,
                        error: frame_error,
                    }))
                    .is_err()
                {
                    return;
                }
            }
            WriterMessage::Stop => {
                let result = match first_error {
                    Some(error) => Err(error),
                    None => output.flush().map_err(|error| error.to_string()),
                };
                let _ = events.send(WriterEvent::Finished(result));
                return;
            }
        }
    }
}

/// Render an incremental NCT2 parser stream directly to an RGBA writer.
///
/// The parser, upload completion callbacks, frame maps, and writer acknowledgements
/// are advanced by one coordinator around the existing Renderer submission owner.
/// `unblock_output` must make a potentially blocked plain `Write` return before
/// cancellation joins the writer; this function never closes caller-owned output.
pub fn stream_incremental_frames<W, F>(
    renderer: &mut Renderer,
    parser: IncrementalStreamParser,
    initial_header: ParserEvent<IncrementalEvent>,
    expected: ExpectedHeader,
    cpu_budget: CpuBudget,
    gpu_budget: GpuBudget,
    readback_slots: usize,
    output: W,
    emit_progress: bool,
    unblock_output: F,
) -> Result<StreamReadbackCompletion, ReadbackError>
where
    W: Write + Send + 'static,
    F: Fn() + Send + Sync + 'static,
{
    let uploader = StreamAssetUploader::new(WgpuUploadBackend::new(renderer), gpu_budget.clone());
    let driver = WgpuStreamReadbackDriver::new(
        uploader,
        parser,
        initial_header,
        expected,
        cpu_budget,
        gpu_budget,
        readback_slots,
        output,
        emit_progress,
        unblock_output,
    )?;
    StreamReadbackCoordinator::new(driver)?.run()
}

struct WgpuReadbackSlot {
    buffer: Arc<wgpu::Buffer>,
}

struct WgpuStreamReadbackDriver<'a> {
    uploader: StreamAssetUploader<WgpuUploadBackend<'a>>,
    parser: IncrementalStreamParser,
    pending_event: Option<ParserEvent<IncrementalEvent>>,
    expected: ExpectedHeader,
    cpu_budget: CpuBudget,
    slots: Vec<WgpuReadbackSlot>,
    slot_credits: Vec<Arc<crate::stream_renderer::GpuCredit>>,
    target: Arc<wgpu::Texture>,
    target_credit: Arc<crate::stream_renderer::GpuCredit>,
    map_sender: mpsc::Sender<MapComplete>,
    map_receiver: Receiver<MapComplete>,
    writer_sender: Option<BoundedOutputSender<WriterMessage>>,
    output_queue: Arc<BoundedOutputQueueInner<WriterMessage>>,
    writer_events: Receiver<WriterEvent>,
    writer: Option<thread::JoinHandle<()>>,
    status_markers: TimelineStatusEmitter<io::Stderr>,
    bytes_per_row: u32,
    unblock_output: Arc<dyn Fn() + Send + Sync>,
    output_unblocked: AtomicBool,
    pending_asset: Option<ParserEvent<IncrementalEvent>>,
    pending_complete: Option<ParserEvent<IncrementalEvent>>,
    upload_cancellation: UploadCancellation,
    registered_asset_pages: usize,
    saw_complete: bool,
    writer_joined: bool,
}

impl<'a> WgpuStreamReadbackDriver<'a> {
    #[allow(clippy::too_many_arguments)]
    fn new<W, F>(
        mut uploader: StreamAssetUploader<WgpuUploadBackend<'a>>,
        parser: IncrementalStreamParser,
        initial_header: ParserEvent<IncrementalEvent>,
        expected: ExpectedHeader,
        cpu_budget: CpuBudget,
        gpu_budget: GpuBudget,
        readback_slots: usize,
        output: W,
        emit_progress: bool,
        unblock_output: F,
    ) -> Result<Self, ReadbackError>
    where
        W: Write + Send + 'static,
        F: Fn() + Send + Sync + 'static,
    {
        if !(1..=3).contains(&readback_slots) {
            return Err(ReadbackError::invalid("readback slots must be 1, 2, or 3"));
        }
        if expected.width == 0 || expected.height == 0 || expected.frame_count == 0 {
            return Err(ReadbackError::invalid(
                "invalid expected stream dimensions or frame count",
            ));
        }
        let bytes_per_row = padded_bytes_per_row(expected.width)?;
        let buffer_size = u64::from(bytes_per_row)
            .checked_mul(u64::from(expected.height))
            .ok_or_else(|| ReadbackError::invalid("readback buffer size overflow"))?;
        let target_bytes = usize::try_from(expected.width)
            .ok()
            .and_then(|width| {
                usize::try_from(expected.height)
                    .ok()
                    .and_then(|height| width.checked_mul(height))
            })
            .and_then(|pixels| pixels.checked_mul(4))
            .ok_or_else(|| ReadbackError::invalid("render target byte size overflow"))?;
        let target_credit = Arc::new(
            gpu_budget
                .try_reserve(target_bytes)
                .map_err(|error| ReadbackError::invalid(error.to_string()))?,
        );
        let renderer = uploader.backend_mut().renderer_mut();
        let target = Arc::new(renderer.device().create_texture(&wgpu::TextureDescriptor {
            label: Some("NCT2 RGBA output frame"),
            size: wgpu::Extent3d {
                width: expected.width,
                height: expected.height,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: wgpu::TextureFormat::Rgba8Unorm,
            usage: wgpu::TextureUsages::RENDER_ATTACHMENT | wgpu::TextureUsages::COPY_SRC,
            view_formats: &[],
        }));
        let mut slots = Vec::with_capacity(readback_slots);
        let mut slot_credits = Vec::with_capacity(readback_slots);
        for index in 0..readback_slots {
            let bytes = usize::try_from(buffer_size)
                .map_err(|_| ReadbackError::invalid("readback buffer size exceeds usize"))?;
            slot_credits.push(Arc::new(
                gpu_budget
                    .try_reserve(bytes)
                    .map_err(|error| ReadbackError::invalid(error.to_string()))?,
            ));
            slots.push(WgpuReadbackSlot {
                buffer: Arc::new(renderer.device().create_buffer(&wgpu::BufferDescriptor {
                    label: Some(&format!("NCT2 readback slot {index}")),
                    size: buffer_size,
                    usage: wgpu::BufferUsages::COPY_DST | wgpu::BufferUsages::MAP_READ,
                    mapped_at_creation: false,
                })),
            });
        }

        let (map_sender, map_receiver) = mpsc::channel();
        let (writer_sender, writer_receiver) = bounded_output_channel(2);
        let output_queue = Arc::clone(&writer_sender.inner);
        let (writer_event_sender, writer_events) = mpsc::channel();
        let width = expected.width;
        let height = expected.height;
        let frame_count = expected.frame_count;
        let writer = thread::spawn(move || {
            writer_loop(
                output,
                writer_receiver,
                writer_event_sender,
                width,
                height,
                bytes_per_row,
                frame_count,
                emit_progress,
            )
        });
        Ok(Self {
            uploader,
            parser,
            pending_event: Some(initial_header),
            expected,
            cpu_budget,
            slots,
            slot_credits,
            target,
            target_credit,
            map_sender,
            map_receiver,
            writer_sender: Some(writer_sender),
            output_queue,
            writer_events,
            writer: Some(writer),
            status_markers: TimelineStatusEmitter::new(io::stderr()),
            bytes_per_row,
            unblock_output: Arc::new(unblock_output),
            output_unblocked: AtomicBool::new(false),
            pending_asset: None,
            pending_complete: None,
            upload_cancellation: UploadCancellation::new(),
            registered_asset_pages: 0,
            saw_complete: false,
            writer_joined: false,
        })
    }

    fn handle_event(
        &mut self,
        event: ParserEvent<IncrementalEvent>,
    ) -> Result<CoordinatorInput, ReadbackError> {
        self.status_markers
            .observe_parser_event(event.payload.get())
            .map_err(|error| {
                ReadbackError::invalid(format!("writing NCT2 status marker: {error}"))
            })?;
        match event.payload.get() {
            IncrementalEvent::Header(header) => {
                if header.width != self.expected.width
                    || header.height != self.expected.height
                    || header.frame_count != self.expected.frame_count
                    || header.fps_num != self.expected.fps_num
                    || header.fps_den != self.expected.fps_den
                    || header.bundle_sha256 != self.expected.bundle_sha256
                {
                    return Err(ReadbackError::invalid(
                        "incremental Header differs from Renderer scene parameters",
                    ));
                }
                Ok(CoordinatorInput::Header {
                    frame_count: header.frame_count,
                    fps_num: header.fps_num,
                    fps_den: header.fps_den,
                })
            }
            IncrementalEvent::Declarations(_) => Ok(CoordinatorInput::Empty),
            IncrementalEvent::Asset(_) => self.upload_or_defer(event),
            IncrementalEvent::Element(element) => {
                self.uploader
                    .backend_mut()
                    .renderer_mut()
                    .apply_element(element, &self.cpu_budget)?;
                Ok(CoordinatorInput::Empty)
            }
            IncrementalEvent::Watermark { ready_vpos, .. } => Ok(CoordinatorInput::Watermark {
                ready_vpos: *ready_vpos,
            }),
            IncrementalEvent::Complete(Err(error)) => {
                Ok(CoordinatorInput::Failed(error.to_string()))
            }
            IncrementalEvent::Complete(Ok(_)) => {
                self.saw_complete = true;
                if self.uploader.has_pending_upload() {
                    self.pending_complete = Some(event);
                    Ok(CoordinatorInput::Empty)
                } else {
                    Ok(CoordinatorInput::Complete)
                }
            }
        }
    }

    fn upload_or_defer(
        &mut self,
        event: ParserEvent<IncrementalEvent>,
    ) -> Result<CoordinatorInput, ReadbackError> {
        let attempt = self
            .uploader
            .try_upload_incremental(event, &self.upload_cancellation)
            .map_err(ReadbackError::invalid)?;
        self.status_markers
            .observe_upload_attempt(&attempt)
            .map_err(|error| {
                ReadbackError::invalid(format!("writing NCT2 status marker: {error}"))
            })?;
        match attempt {
            UploadAttempt::Submitted { .. } => {
                self.registered_asset_pages =
                    self.registered_asset_pages.checked_add(1).ok_or_else(|| {
                        ReadbackError::invalid("registered asset page count overflow")
                    })?;
                Ok(CoordinatorInput::Empty)
            }
            UploadAttempt::Deferred { reason, event } => {
                self.pending_asset = Some(event);
                match reason {
                    UploadDeferredReason::CreditsUnavailable => {
                        Ok(CoordinatorInput::DeferredUpload)
                    }
                    UploadDeferredReason::StagingBusy => Ok(CoordinatorInput::Empty),
                    UploadDeferredReason::Cancelled => {
                        Err(ReadbackError::invalid("stream asset upload was cancelled"))
                    }
                    UploadDeferredReason::NotAsset => Err(ReadbackError::invalid(
                        "incremental upload was deferred for a non-asset event",
                    )),
                }
            }
        }
    }

    fn finish_writer(&mut self) -> Result<(), ReadbackError> {
        while self.uploader.has_pending_upload() {
            self.uploader
                .poll_completions()
                .map_err(ReadbackError::invalid)?;
            if self.uploader.has_pending_upload() {
                thread::sleep(Duration::from_millis(1));
            }
        }
        self.uploader
            .release_scene()
            .map_err(ReadbackError::invalid)?;
        if let Some(sender) = self.writer_sender.take() {
            sender
                .send(WriterMessage::Stop, false)
                .map_err(|_| ReadbackError::invalid("RGBA writer stopped before flush"))?;
        }
        let mut result = loop {
            match self.writer_events.recv() {
                Ok(WriterEvent::Finished(writer_result)) => {
                    break writer_result.map_err(ReadbackError::invalid);
                }
                Ok(WriterEvent::Frame(_)) => {}
                Err(_) => {
                    break Err(ReadbackError::invalid(
                        "RGBA writer exited without flush status",
                    ));
                }
            }
        };
        if self
            .writer
            .take()
            .is_some_and(|writer| writer.join().is_err())
        {
            result = Err(ReadbackError::invalid("RGBA writer thread panicked"));
        }
        self.writer_joined = true;
        result
    }
}

impl StreamReadbackDriver for WgpuStreamReadbackDriver<'_> {
    type SlotCredit = Arc<crate::stream_renderer::GpuCredit>;

    fn slot_count(&self) -> usize {
        self.slots.len()
    }

    fn reserve_slot(&mut self, slot: usize) -> Result<Self::SlotCredit, ReadbackError> {
        Ok(Arc::clone(&self.slot_credits[slot]))
    }

    fn registered_asset_page_count(&self) -> usize {
        self.registered_asset_pages
    }

    fn output_queue_metrics(&self) -> Option<WriterQueueMetrics> {
        Some(bounded_output_queue_metrics(&self.output_queue))
    }

    fn try_receive_incremental(&mut self) -> Result<CoordinatorInput, ReadbackError> {
        if let Some(event) = self.pending_event.take() {
            return self.handle_event(event);
        }
        if let Some(event) = self.pending_asset.take() {
            return self.upload_or_defer(event);
        }
        if let Some(event) = self.pending_complete.take() {
            if self.uploader.has_pending_upload() {
                self.pending_complete = Some(event);
                return Ok(CoordinatorInput::Empty);
            }
            return Ok(CoordinatorInput::Complete);
        }
        if self.saw_complete {
            return Ok(CoordinatorInput::Empty);
        }
        match self
            .parser
            .try_recv()
            .map_err(|error| ReadbackError::invalid(error.to_string()))?
        {
            ParserReceive::Event(event) => self.handle_event(event),
            ParserReceive::Empty | ParserReceive::TimedOut => Ok(CoordinatorInput::Empty),
            ParserReceive::Closed => Err(ReadbackError::invalid(
                "incremental parser closed before Complete",
            )),
        }
    }

    fn poll_gpu_and_maps(&mut self) -> Result<Vec<CoordinatorMapEvent>, ReadbackError> {
        self.uploader
            .backend_mut()
            .renderer_mut()
            .device()
            .poll(wgpu::Maintain::Poll);
        self.uploader
            .poll_completions()
            .map_err(ReadbackError::invalid)?;
        let mut events = Vec::new();
        while let Ok(event) = self.map_receiver.try_recv() {
            events.push(match event.result {
                Ok(()) => CoordinatorMapEvent::Mapped {
                    sequence: event.sequence,
                    slot: event.slot,
                },
                Err(error) => CoordinatorMapEvent::Failed {
                    sequence: event.sequence,
                    slot: event.slot,
                    error,
                },
            });
        }
        Ok(events)
    }

    fn submit_frame(&mut self, sequence: u32, slot: usize) -> Result<(), ReadbackError> {
        let renderer = self.uploader.backend_mut().renderer_mut();
        renderer.submit_frame_with_readback(
            sequence,
            &self.target,
            &self.slots[slot].buffer,
            self.bytes_per_row,
        )?;
        let sender = self.map_sender.clone();
        let buffer = Arc::clone(&self.slots[slot].buffer);
        let callback_buffer = Arc::clone(&buffer);
        let callback_target = Arc::clone(&self.target);
        let keep_slot_credit = Arc::clone(&self.slot_credits[slot]);
        let keep_target_credit = Arc::clone(&self.target_credit);
        buffer
            .slice(..)
            .map_async(wgpu::MapMode::Read, move |result| {
                let _resource_keepalive = (callback_buffer, callback_target);
                let _ = sender.send(MapComplete {
                    slot,
                    sequence,
                    result: result.map_err(|error| error.to_string()),
                    _keep_alive: Some((keep_slot_credit, keep_target_credit)),
                });
            });
        Ok(())
    }

    fn try_queue_write(
        &mut self,
        sequence: u32,
        slot: usize,
    ) -> Result<CoordinatorWriteStatus, ReadbackError> {
        let sender = self
            .writer_sender
            .as_ref()
            .ok_or_else(|| ReadbackError::invalid("RGBA writer is already stopped"))?;
        let message = WriterMessage::Frame {
            slot,
            sequence,
            buffer: Arc::clone(&self.slots[slot].buffer),
        };
        match sender.try_send(message, true) {
            Ok(()) => Ok(CoordinatorWriteStatus::Queued),
            Err(BoundedTrySendError::Full(WriterMessage::Frame { .. })) => {
                Ok(CoordinatorWriteStatus::Full)
            }
            Err(BoundedTrySendError::Full(WriterMessage::Stop)) => unreachable!(),
            Err(BoundedTrySendError::Disconnected(_)) => {
                Err(ReadbackError::invalid("RGBA writer stopped unexpectedly"))
            }
        }
    }

    fn unmap_readback(&mut self, slot: usize) -> Result<(), ReadbackError> {
        self.slots[slot].buffer.unmap();
        Ok(())
    }

    fn try_writer_ack(&mut self) -> Result<Option<CoordinatorWriteAck>, ReadbackError> {
        match self.writer_events.try_recv() {
            Ok(WriterEvent::Frame(done)) => {
                if let Some(error) = done.error {
                    Err(ReadbackError::invalid(format!(
                        "writing frame {}: {error}",
                        done.sequence
                    )))
                } else {
                    Ok(Some(CoordinatorWriteAck::Flushed {
                        sequence: done.sequence,
                        slot: done.slot,
                    }))
                }
            }
            Ok(WriterEvent::Finished(Err(error))) => Err(ReadbackError::invalid(error)),
            Ok(WriterEvent::Finished(Ok(()))) => Ok(None),
            Err(TryRecvError::Empty) => Ok(None),
            Err(TryRecvError::Disconnected) => Err(ReadbackError::invalid(
                "RGBA writer event channel disconnected",
            )),
        }
    }

    fn unblock_input(&mut self) {
        self.upload_cancellation.cancel();
        self.parser.cancel();
    }

    fn unblock_output(&mut self) {
        if !self.output_unblocked.swap(true, Ordering::AcqRel) {
            (self.unblock_output)();
        }
    }

    fn join_input(&mut self) -> Result<(), ReadbackError> {
        self.parser
            .join()
            .map_err(|error| ReadbackError::invalid(error.to_string()))
    }

    fn join_writer(&mut self) -> Result<(), ReadbackError> {
        if self.writer_joined {
            return Ok(());
        }
        self.finish_writer()
    }
}

impl Drop for WgpuStreamReadbackDriver<'_> {
    fn drop(&mut self) {
        if !self.writer_joined {
            self.upload_cancellation.cancel();
            self.parser.cancel();
            if !self.output_unblocked.swap(true, Ordering::AcqRel) {
                (self.unblock_output)();
            }
            self.writer_sender.take();
            if let Some(writer) = self.writer.take() {
                let _ = writer.join();
            }
        }
    }
}

#[cfg(test)]
mod writer_queue_tests {
    use super::*;

    #[test]
    fn bounded_writer_queue_reports_exact_saturation_and_drain() {
        let (sender, receiver) = bounded_output_channel(2);
        assert_eq!(
            bounded_output_queue_metrics(&sender.inner),
            WriterQueueMetrics {
                capacity: 2,
                occupancy: 0,
                high_water: 0,
            }
        );

        sender.try_send(10, true).unwrap();
        sender.try_send(20, true).unwrap();
        assert_eq!(
            bounded_output_queue_metrics(&sender.inner),
            WriterQueueMetrics {
                capacity: 2,
                occupancy: 2,
                high_water: 2,
            }
        );
        assert!(matches!(
            sender.try_send(30, true),
            Err(BoundedTrySendError::Full(30))
        ));

        assert_eq!(receiver.recv(), Ok(10));
        assert_eq!(
            bounded_output_queue_metrics(&sender.inner),
            WriterQueueMetrics {
                capacity: 2,
                occupancy: 1,
                high_water: 2,
            }
        );
        sender.try_send(30, true).unwrap();
        assert_eq!(receiver.recv(), Ok(20));
        assert_eq!(receiver.recv(), Ok(30));
        sender.try_send(40, false).unwrap();
        sender.try_send(50, true).unwrap();
        assert_eq!(
            bounded_output_queue_metrics(&sender.inner),
            WriterQueueMetrics {
                capacity: 2,
                occupancy: 1,
                high_water: 2,
            }
        );
        assert!(matches!(
            sender.try_send(60, true),
            Err(BoundedTrySendError::Full(60))
        ));
        assert_eq!(receiver.recv(), Ok(40));
        assert_eq!(receiver.recv(), Ok(50));
        drop(sender);
        assert_eq!(receiver.recv(), Err(BoundedRecvError));
        assert_eq!(
            bounded_output_queue_metrics(&receiver.inner),
            WriterQueueMetrics {
                capacity: 2,
                occupancy: 0,
                high_water: 2,
            }
        );
    }
}
