use std::collections::VecDeque;
use std::fmt;
use std::marker::PhantomData;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

pub const CPU_BUDGET_BYTES: usize = 256 * 1024 * 1024;
pub const PARSER_EVENT_QUEUE_CAPACITY: usize = 2;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BudgetError(&'static str);

impl fmt::Display for BudgetError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.0)
    }
}

impl std::error::Error for BudgetError {}

#[derive(Debug)]
struct BudgetState {
    used: Mutex<usize>,
    available: Condvar,
}

#[derive(Debug, Clone)]
pub struct CpuBudget {
    state: Arc<BudgetState>,
}

impl Default for CpuBudget {
    fn default() -> Self {
        Self::new()
    }
}

impl CpuBudget {
    pub fn new() -> Self {
        Self {
            state: Arc::new(BudgetState {
                used: Mutex::new(0),
                available: Condvar::new(),
            }),
        }
    }

    pub fn used_bytes(&self) -> usize {
        *self.state.used.lock().expect("CPU budget mutex poisoned")
    }

    pub fn try_reserve(&self, bytes: usize) -> Result<CpuCredit, BudgetError> {
        let mut used = self.state.used.lock().expect("CPU budget mutex poisoned");
        if bytes > CPU_BUDGET_BYTES.saturating_sub(*used) {
            return Err(BudgetError("Rust CPU allocation exceeds 256 MiB budget"));
        }
        *used += bytes;
        Ok(CpuCredit {
            state: Some(Arc::clone(&self.state)),
            bytes,
        })
    }

    pub fn reserve(
        &self,
        bytes: usize,
        cancellation: &ParserCancellation,
    ) -> Result<CpuCredit, BudgetError> {
        if bytes > CPU_BUDGET_BYTES {
            return Err(BudgetError("Rust CPU allocation exceeds 256 MiB budget"));
        }
        let mut used = self.state.used.lock().expect("CPU budget mutex poisoned");
        while bytes > CPU_BUDGET_BYTES.saturating_sub(*used) {
            if cancellation.is_cancelled() {
                return Err(BudgetError("Rust CPU budget reservation cancelled"));
            }
            let (next, _) = self
                .state
                .available
                .wait_timeout(used, Duration::from_millis(10))
                .expect("CPU budget mutex poisoned while waiting");
            used = next;
        }
        if cancellation.is_cancelled() {
            return Err(BudgetError("Rust CPU budget reservation cancelled"));
        }
        *used += bytes;
        Ok(CpuCredit {
            state: Some(Arc::clone(&self.state)),
            bytes,
        })
    }
}

/// A move-only reservation. Its final drop returns exactly the reserved bytes.
#[derive(Debug)]
pub struct CpuCredit {
    state: Option<Arc<BudgetState>>,
    bytes: usize,
}

impl CpuCredit {
    pub fn bytes(&self) -> usize {
        self.bytes
    }
}

impl Drop for CpuCredit {
    fn drop(&mut self) {
        if let Some(state) = self.state.take() {
            let mut used = state.used.lock().expect("CPU budget mutex poisoned");
            *used = used
                .checked_sub(self.bytes)
                .expect("CPU budget credit returned more than once");
            state.available.notify_all();
        }
    }
}

#[derive(Debug, Clone, Default)]
pub struct ParserCancellation {
    cancelled: Arc<AtomicBool>,
}

impl ParserCancellation {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn cancel(&self) {
        self.cancelled.store(true, Ordering::Release);
    }

    pub fn is_cancelled(&self) -> bool {
        self.cancelled.load(Ordering::Acquire)
    }
}

struct QueueState<T> {
    events: VecDeque<ParserEvent<T>>,
    closed: bool,
    senders: usize,
}

struct QueueShared<T> {
    state: Mutex<QueueState<T>>,
    changed: Condvar,
}

pub struct ParserEventQueue<T>(PhantomData<T>);

pub struct ParserEventSender<T> {
    shared: Arc<QueueShared<T>>,
    cancellation: ParserCancellation,
}

pub struct ParserEventReceiver<T> {
    shared: Arc<QueueShared<T>>,
    cancellation: ParserCancellation,
}

pub struct ParserEvent<T> {
    pub payload: Budgeted<T>,
}

/// Scheduler-friendly result from a nonblocking or timed queue poll.
pub enum ParserReceive<T> {
    Event(ParserEvent<T>),
    Empty,
    TimedOut,
    Closed,
}

/// A payload and its reservation are one move-only owner. Consumers can move
/// this wrapper between queues without releasing bytes before the allocation.
pub struct Budgeted<T> {
    value: T,
    credit: CpuCredit,
}

impl<T> Budgeted<T> {
    pub fn get(&self) -> &T {
        &self.value
    }

    pub fn reserved_bytes(&self) -> usize {
        self.credit.bytes()
    }
}

impl<T> ParserEventQueue<T> {
    pub fn new(cancellation: ParserCancellation) -> (ParserEventSender<T>, ParserEventReceiver<T>) {
        let shared = Arc::new(QueueShared {
            state: Mutex::new(QueueState {
                events: VecDeque::with_capacity(PARSER_EVENT_QUEUE_CAPACITY),
                closed: false,
                senders: 1,
            }),
            changed: Condvar::new(),
        });
        (
            ParserEventSender {
                shared: Arc::clone(&shared),
                cancellation: cancellation.clone(),
            },
            ParserEventReceiver {
                shared,
                cancellation,
            },
        )
    }
}

impl<T> Clone for ParserEventSender<T> {
    fn clone(&self) -> Self {
        self.shared
            .state
            .lock()
            .expect("parser event queue mutex poisoned")
            .senders += 1;
        Self {
            shared: Arc::clone(&self.shared),
            cancellation: self.cancellation.clone(),
        }
    }
}

impl<T> Drop for ParserEventSender<T> {
    fn drop(&mut self) {
        let mut state = self
            .shared
            .state
            .lock()
            .expect("parser event queue mutex poisoned");
        state.senders = state
            .senders
            .checked_sub(1)
            .expect("parser event sender count underflow");
        if state.senders == 0 {
            state.closed = true;
            self.shared.changed.notify_all();
        }
    }
}

impl<T> ParserEventSender<T> {
    pub fn send(&self, payload: T, credit: CpuCredit) -> Result<(), BudgetError> {
        let event = ParserEvent {
            payload: Budgeted {
                value: payload,
                credit,
            },
        };
        let mut state = self
            .shared
            .state
            .lock()
            .expect("parser event queue mutex poisoned");
        while state.events.len() == PARSER_EVENT_QUEUE_CAPACITY {
            if state.closed || self.cancellation.is_cancelled() {
                return Err(BudgetError("NCT2 parser event send cancelled"));
            }
            let (next, _) = self
                .shared
                .changed
                .wait_timeout(state, Duration::from_millis(10))
                .expect("parser event queue mutex poisoned while sending");
            state = next;
        }
        if state.closed || self.cancellation.is_cancelled() {
            return Err(BudgetError("NCT2 parser event send cancelled"));
        }
        state.events.push_back(event);
        self.shared.changed.notify_all();
        Ok(())
    }
}

impl<T> ParserEventReceiver<T> {
    pub fn recv(&self) -> Result<Option<ParserEvent<T>>, BudgetError> {
        let mut state = self
            .shared
            .state
            .lock()
            .expect("parser event queue mutex poisoned");
        loop {
            if let Some(event) = state.events.pop_front() {
                self.shared.changed.notify_all();
                return Ok(Some(event));
            }
            if state.closed || self.cancellation.is_cancelled() {
                return Ok(None);
            }
            let (next, _) = self
                .shared
                .changed
                .wait_timeout(state, Duration::from_millis(10))
                .expect("parser event queue mutex poisoned while receiving");
            state = next;
        }
    }

    pub fn try_recv(&self) -> Result<ParserReceive<T>, BudgetError> {
        let mut state = self
            .shared
            .state
            .lock()
            .expect("parser event queue mutex poisoned");
        if let Some(event) = state.events.pop_front() {
            self.shared.changed.notify_all();
            return Ok(ParserReceive::Event(event));
        }
        if state.closed || self.cancellation.is_cancelled() {
            Ok(ParserReceive::Closed)
        } else {
            Ok(ParserReceive::Empty)
        }
    }

    pub fn recv_timeout(&self, timeout: Duration) -> Result<ParserReceive<T>, BudgetError> {
        let started = Instant::now();
        let mut state = self
            .shared
            .state
            .lock()
            .expect("parser event queue mutex poisoned");
        loop {
            if let Some(event) = state.events.pop_front() {
                self.shared.changed.notify_all();
                return Ok(ParserReceive::Event(event));
            }
            if state.closed || self.cancellation.is_cancelled() {
                return Ok(ParserReceive::Closed);
            }
            let remaining = timeout.saturating_sub(started.elapsed());
            if remaining.is_zero() {
                return Ok(ParserReceive::TimedOut);
            }
            // ParserCancellation is intentionally independent of the queue;
            // poll it promptly even when cancellation cannot notify this
            // Condvar directly.
            let wait_for = remaining.min(Duration::from_millis(10));
            let (next, timed_out) = self
                .shared
                .changed
                .wait_timeout(state, wait_for)
                .expect("parser event queue mutex poisoned while timed receiving");
            state = next;
            if timed_out.timed_out() && state.events.is_empty() {
                if state.closed || self.cancellation.is_cancelled() {
                    return Ok(ParserReceive::Closed);
                }
                if started.elapsed() >= timeout {
                    return Ok(ParserReceive::TimedOut);
                }
            }
        }
    }

    pub fn queued_events(&self) -> usize {
        self.shared
            .state
            .lock()
            .expect("parser event queue mutex poisoned")
            .events
            .len()
    }

    pub fn cancel_and_clear(&self) {
        self.cancellation.cancel();
        let mut state = self
            .shared
            .state
            .lock()
            .expect("parser event queue mutex poisoned");
        state.closed = true;
        state.events.clear();
        self.shared.changed.notify_all();
    }
}

impl<T> Drop for ParserEventReceiver<T> {
    fn drop(&mut self) {
        self.cancel_and_clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::thread;
    use std::time::Duration;

    #[test]
    fn cpu_budget_boundary_and_credit_moves_with_queued_event() {
        let budget = CpuBudget::new();
        let full = budget
            .try_reserve(CPU_BUDGET_BYTES)
            .expect("reserve exact cap");
        assert_eq!(budget.used_bytes(), CPU_BUDGET_BYTES);
        assert!(budget.try_reserve(1).is_err());
        drop(full);
        assert_eq!(budget.used_bytes(), 0);

        let (sender, receiver) = ParserEventQueue::new(ParserCancellation::new());
        let credit = budget.try_reserve(128 * 1024 * 1024).expect("asset credit");
        sender.send("asset", credit).expect("queue event");
        assert_eq!(budget.used_bytes(), 128 * 1024 * 1024);
        let event = receiver
            .recv()
            .expect("receive event")
            .expect("event available");
        assert_eq!(event.payload.get(), &"asset");
        assert_eq!(event.payload.reserved_bytes(), 128 * 1024 * 1024);
        drop(event);
        assert_eq!(
            budget.used_bytes(),
            0,
            "event drop returns transferred credit once"
        );
    }

    #[test]
    fn consumer_release_allows_sequential_full_sized_assets() {
        let budget = CpuBudget::new();
        let metadata = budget
            .try_reserve(48 * 1024 * 1024)
            .expect("metadata reserve");
        let (sender, receiver) = ParserEventQueue::new(ParserCancellation::new());
        for asset_number in 0..2 {
            let credit = budget
                .try_reserve(128 * 1024 * 1024)
                .expect("one maximum-sized asset fits with metadata");
            sender
                .send(asset_number, credit)
                .expect("queue verified asset");
            let event = receiver
                .recv()
                .expect("receive asset")
                .expect("asset event");
            assert_eq!(event.payload.reserved_bytes(), 128 * 1024 * 1024);
            assert_eq!(budget.used_bytes(), 176 * 1024 * 1024);
            drop(event);
            assert_eq!(budget.used_bytes(), 48 * 1024 * 1024);
        }
        drop(metadata);
        assert_eq!(budget.used_bytes(), 0);
    }

    #[test]
    fn parser_event_queue_has_two_event_backpressure_and_cancel_wakes_sender() {
        let cancellation = ParserCancellation::new();
        let (sender, receiver) = ParserEventQueue::new(cancellation.clone());
        for item in [1, 2] {
            sender
                .send(item, CpuBudget::new().try_reserve(0).unwrap())
                .expect("fill queue");
        }
        let blocked_sender = sender.clone();
        let blocked_cancel = cancellation.clone();
        let worker =
            thread::spawn(move || blocked_sender.send(3, CpuBudget::new().try_reserve(0).unwrap()));
        thread::sleep(Duration::from_millis(20));
        assert_eq!(receiver.queued_events(), 2);
        cancellation.cancel();
        assert!(worker.join().expect("sender joined").is_err());
        drop(receiver);
        assert!(blocked_cancel.is_cancelled());
    }

    #[test]
    fn dropping_event_receiver_wakes_blocked_sender() {
        let (sender, receiver) = ParserEventQueue::new(ParserCancellation::new());
        for item in [1, 2] {
            sender
                .send(item, CpuBudget::new().try_reserve(0).unwrap())
                .expect("fill queue");
        }
        let worker =
            thread::spawn(move || sender.send(3, CpuBudget::new().try_reserve(0).unwrap()));
        thread::sleep(Duration::from_millis(20));
        drop(receiver);
        assert!(worker.join().expect("sender joined").is_err());
    }

    #[test]
    fn cancellation_wakes_waiting_receiver() {
        let cancellation = ParserCancellation::new();
        let (sender, receiver) = ParserEventQueue::<u32>::new(cancellation.clone());
        let worker = thread::spawn(move || receiver.recv());
        thread::sleep(Duration::from_millis(20));
        cancellation.cancel();
        assert!(worker.join().expect("receiver joined").unwrap().is_none());
        drop(sender);
    }

    #[test]
    fn try_and_timed_receive_transfer_and_return_credit_once() {
        let budget = CpuBudget::new();
        let (sender, receiver) = ParserEventQueue::new(ParserCancellation::new());
        assert!(matches!(receiver.try_recv().unwrap(), ParserReceive::Empty));
        assert!(matches!(
            receiver.recv_timeout(Duration::from_millis(1)).unwrap(),
            ParserReceive::TimedOut
        ));

        sender
            .send("metadata", budget.try_reserve(17).unwrap())
            .unwrap();
        let event = match receiver.try_recv().unwrap() {
            ParserReceive::Event(event) => event,
            _ => panic!("queued event should be available"),
        };
        assert_eq!(event.payload.reserved_bytes(), 17);
        assert_eq!(budget.used_bytes(), 17);
        drop(event);
        assert_eq!(
            budget.used_bytes(),
            0,
            "credit returns exactly once on drop"
        );
        assert!(matches!(receiver.try_recv().unwrap(), ParserReceive::Empty));
        drop(sender);
        assert!(matches!(
            receiver.try_recv().unwrap(),
            ParserReceive::Closed
        ));
    }

    #[test]
    fn timed_receive_wakes_promptly_when_cancelled() {
        let cancellation = ParserCancellation::new();
        let (sender, receiver) = ParserEventQueue::<u32>::new(cancellation.clone());
        let (started_tx, started_rx) = std::sync::mpsc::channel();
        let worker = thread::spawn(move || {
            started_tx.send(()).unwrap();
            receiver.recv_timeout(Duration::from_secs(10))
        });
        started_rx.recv().expect("receiver started");
        cancellation.cancel();
        assert!(matches!(
            worker.join().expect("timed receiver joined").unwrap(),
            ParserReceive::Closed
        ));
        drop(sender);
    }
}
