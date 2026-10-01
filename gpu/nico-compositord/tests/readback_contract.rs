use std::io::{self, Cursor, Write};

use nico_compositord::readback::{padded_bytes_per_row, write_padded_rows};

mod stream_coordinator_contract {
    use std::collections::{BTreeSet, VecDeque};
    use std::sync::{Arc, Mutex};

    use nico_compositord::readback::{
        CoordinatorInput, CoordinatorMapEvent, CoordinatorWriteAck, CoordinatorWriteStatus,
        ReadbackError, StreamReadbackCoordinator, StreamReadbackDriver,
    };

    #[derive(Default)]
    pub struct State {
        trace: Vec<String>,
        input: VecDeque<CoordinatorInput>,
        input_capacity: usize,
        maps: VecDeque<Vec<CoordinatorMapEvent>>,
        map_min_submissions: VecDeque<usize>,
        write_status: VecDeque<CoordinatorWriteStatus>,
        acks: VecDeque<CoordinatorWriteAck>,
        writes_queued: VecDeque<(u32, usize)>,
        output_queue: VecDeque<(u32, usize)>,
        output_capacity: usize,
        submitted: Vec<(u32, usize)>,
        live_readback_credits: BTreeSet<usize>,
        upload_credit_live: bool,
        output_blocked: bool,
        input_unblocked: bool,
        output_unblocked: bool,
        require_unblock_before_join: bool,
        writer_join_fails: bool,
        slots: usize,
        deferred_upload_active: bool,
        deferred_recovered: bool,
        deferred_recover_on_map: bool,
        map_delay_polls: usize,
        registered_asset_pages: usize,
    }

    #[derive(Clone)]
    pub struct FakeDriver {
        state: Arc<Mutex<State>>,
    }

    impl FakeDriver {
        pub fn source_stalled_with_upload_completion() -> (Self, Arc<Mutex<State>>) {
            let state = Arc::new(Mutex::new(State {
                upload_credit_live: true,
                slots: 2,
                ..State::default()
            }));
            (
                Self {
                    state: Arc::clone(&state),
                },
                state,
            )
        }

        pub fn reversed_map_callbacks() -> (Self, Arc<Mutex<State>>) {
            let state = Arc::new(Mutex::new(State {
                input: VecDeque::from([
                    CoordinatorInput::Header {
                        frame_count: 2,
                        fps_num: 30,
                        fps_den: 1,
                    },
                    CoordinatorInput::Watermark { ready_vpos: 100 },
                    CoordinatorInput::Complete,
                ]),
                maps: VecDeque::from([vec![
                    CoordinatorMapEvent::Mapped {
                        sequence: 1,
                        slot: 1,
                    },
                    CoordinatorMapEvent::Mapped {
                        sequence: 0,
                        slot: 0,
                    },
                ]]),
                map_min_submissions: VecDeque::from([2]),
                write_status: VecDeque::from([
                    CoordinatorWriteStatus::Queued,
                    CoordinatorWriteStatus::Queued,
                ]),
                acks: VecDeque::from([
                    CoordinatorWriteAck::Flushed {
                        sequence: 0,
                        slot: 0,
                    },
                    CoordinatorWriteAck::Flushed {
                        sequence: 1,
                        slot: 1,
                    },
                ]),
                slots: 2,
                input_capacity: 1,
                output_capacity: 2,
                ..State::default()
            }));
            (
                Self {
                    state: Arc::clone(&state),
                },
                state,
            )
        }

        pub fn empty_stream_without_watermark() -> (Self, Arc<Mutex<State>>) {
            let state = Arc::new(Mutex::new(State {
                input: VecDeque::from([
                    CoordinatorInput::Header {
                        frame_count: 2,
                        fps_num: 30,
                        fps_den: 1,
                    },
                    CoordinatorInput::Complete,
                ]),
                maps: VecDeque::from([vec![
                    CoordinatorMapEvent::Mapped {
                        sequence: 0,
                        slot: 0,
                    },
                    CoordinatorMapEvent::Mapped {
                        sequence: 1,
                        slot: 1,
                    },
                ]]),
                map_min_submissions: VecDeque::from([2]),
                write_status: VecDeque::from([
                    CoordinatorWriteStatus::Queued,
                    CoordinatorWriteStatus::Queued,
                ]),
                acks: VecDeque::from([
                    CoordinatorWriteAck::Flushed {
                        sequence: 0,
                        slot: 0,
                    },
                    CoordinatorWriteAck::Flushed {
                        sequence: 1,
                        slot: 1,
                    },
                ]),
                slots: 2,
                input_capacity: 2,
                output_capacity: 2,
                ..State::default()
            }));
            (
                Self {
                    state: Arc::clone(&state),
                },
                state,
            )
        }

        pub fn one_slot_waiting_for_flush_ack() -> (Self, Arc<Mutex<State>>) {
            let (driver, state) = Self::reversed_map_callbacks();
            {
                let mut state = state.lock().unwrap();
                state.slots = 1;
                state.input = VecDeque::from([
                    CoordinatorInput::Header {
                        frame_count: 3,
                        fps_num: 30,
                        fps_den: 1,
                    },
                    CoordinatorInput::Watermark { ready_vpos: 100 },
                    CoordinatorInput::Complete,
                ]);
                state.input_capacity = 3;
                state.maps = VecDeque::from([
                    vec![CoordinatorMapEvent::Mapped {
                        sequence: 0,
                        slot: 0,
                    }],
                    vec![CoordinatorMapEvent::Mapped {
                        sequence: 1,
                        slot: 0,
                    }],
                ]);
                state.map_min_submissions = VecDeque::from([1, 2]);
                state.write_status = VecDeque::from([
                    CoordinatorWriteStatus::Queued,
                    CoordinatorWriteStatus::Queued,
                ]);
                state.acks.clear();
                state.output_capacity = 1;
            }
            (driver, state)
        }

        pub fn full_queues_with_blocked_output() -> (Self, Arc<Mutex<State>>) {
            let state = Arc::new(Mutex::new(State {
                input: VecDeque::from([CoordinatorInput::Empty]),
                input_capacity: 1,
                output_queue: VecDeque::from([(9, 0)]),
                output_capacity: 1,
                output_blocked: true,
                require_unblock_before_join: true,
                slots: 2,
                ..State::default()
            }));
            (
                Self {
                    state: Arc::clone(&state),
                },
                state,
            )
        }

        pub fn credits_deferred_behind_frame() -> (Self, Arc<Mutex<State>>) {
            let state = Arc::new(Mutex::new(State {
                input: VecDeque::from([
                    CoordinatorInput::Header {
                        frame_count: 1,
                        fps_num: 30,
                        fps_den: 1,
                    },
                    CoordinatorInput::Watermark { ready_vpos: 100 },
                    CoordinatorInput::Complete,
                ]),
                maps: VecDeque::from([vec![CoordinatorMapEvent::Mapped {
                    sequence: 0,
                    slot: 0,
                }]]),
                map_min_submissions: VecDeque::from([1]),
                write_status: VecDeque::from([CoordinatorWriteStatus::Queued]),
                acks: VecDeque::from([CoordinatorWriteAck::Flushed {
                    sequence: 0,
                    slot: 0,
                }]),
                slots: 1,
                input_capacity: 3,
                output_capacity: 1,
                deferred_upload_active: true,
                deferred_recover_on_map: true,
                map_delay_polls: 1,
                ..State::default()
            }));
            (
                Self {
                    state: Arc::clone(&state),
                },
                state,
            )
        }

        pub fn credits_deferred_without_progress() -> (Self, Arc<Mutex<State>>) {
            let state = Arc::new(Mutex::new(State {
                input: VecDeque::from([
                    CoordinatorInput::Header {
                        frame_count: 1,
                        fps_num: 30,
                        fps_den: 1,
                    },
                    CoordinatorInput::Watermark { ready_vpos: 0 },
                    CoordinatorInput::Complete,
                ]),
                slots: 1,
                input_capacity: 3,
                deferred_upload_active: true,
                ..State::default()
            }));
            (
                Self {
                    state: Arc::clone(&state),
                },
                state,
            )
        }

        pub fn full_ring_blocks_ready_frame_with_deferred_credit() -> (Self, Arc<Mutex<State>>) {
            let state = Arc::new(Mutex::new(State {
                input: VecDeque::from([
                    CoordinatorInput::Header {
                        frame_count: 2,
                        fps_num: 30,
                        fps_den: 1,
                    },
                    CoordinatorInput::Watermark { ready_vpos: 100 },
                ]),
                maps: VecDeque::from([vec![CoordinatorMapEvent::Mapped {
                    sequence: 0,
                    slot: 0,
                }]]),
                map_min_submissions: VecDeque::from([1]),
                output_queue: VecDeque::from([(77, 0)]),
                output_capacity: 1,
                output_blocked: true,
                slots: 1,
                deferred_upload_active: true,
                ..State::default()
            }));
            (
                Self {
                    state: Arc::clone(&state),
                },
                state,
            )
        }
    }

    pub struct FakeMappedView {
        state: Arc<Mutex<State>>,
        slot: usize,
    }

    impl Drop for FakeMappedView {
        fn drop(&mut self) {
            self.state
                .lock()
                .unwrap()
                .trace
                .push(format!("mapped_view_drop:{}", self.slot));
        }
    }

    pub struct FakeReadbackCredit {
        state: Arc<Mutex<State>>,
        slot: usize,
    }

    impl Drop for FakeReadbackCredit {
        fn drop(&mut self) {
            let mut state = self.state.lock().unwrap();
            let removed = state.live_readback_credits.remove(&self.slot);
            assert!(removed, "readback credit released exactly once");
            state
                .trace
                .push(format!("credit_release_slot:{}", self.slot));
        }
    }

    impl StreamReadbackDriver for FakeDriver {
        type SlotCredit = FakeReadbackCredit;

        fn slot_count(&self) -> usize {
            self.state.lock().unwrap().slots
        }

        fn reserve_slot(&mut self, slot: usize) -> Result<Self::SlotCredit, ReadbackError> {
            let mut state = self.state.lock().unwrap();
            assert!(state.live_readback_credits.insert(slot));
            state.trace.push(format!("reserve_slot:{slot}"));
            drop(state);
            Ok(FakeReadbackCredit {
                state: Arc::clone(&self.state),
                slot,
            })
        }

        fn registered_asset_page_count(&self) -> usize {
            self.state.lock().unwrap().registered_asset_pages
        }

        fn try_receive_incremental(&mut self) -> Result<CoordinatorInput, ReadbackError> {
            let mut state = self.state.lock().unwrap();
            state.trace.push("parser_try_receive".into());
            if matches!(
                state.input.front(),
                Some(CoordinatorInput::Header { .. } | CoordinatorInput::Watermark { .. })
            ) {
                return Ok(state.input.pop_front().expect("front was present"));
            }
            if state.deferred_upload_active && !state.deferred_recovered {
                return Ok(CoordinatorInput::DeferredUpload);
            }
            if state.deferred_upload_active && state.deferred_recovered {
                state.deferred_upload_active = false;
            }
            Ok(state.input.pop_front().unwrap_or(CoordinatorInput::Empty))
        }

        fn poll_gpu_and_maps(&mut self) -> Result<Vec<CoordinatorMapEvent>, ReadbackError> {
            let mut state = self.state.lock().unwrap();
            state.trace.push("poll_gpu".into());
            if state.upload_credit_live {
                state.upload_credit_live = false;
                state.trace.push("retire_upload_credit".into());
            }
            if state.map_delay_polls > 0 {
                state.map_delay_polls -= 1;
                return Ok(Vec::new());
            }
            let min_submissions = state.map_min_submissions.front().copied();
            if min_submissions.is_some_and(|count| state.submitted.len() >= count) {
                state.map_min_submissions.pop_front();
                if state.deferred_recover_on_map {
                    state.deferred_recovered = true;
                }
                Ok(state.maps.pop_front().unwrap_or_default())
            } else {
                Ok(Vec::new())
            }
        }

        fn submit_frame(&mut self, sequence: u32, slot: usize) -> Result<(), ReadbackError> {
            let mut state = self.state.lock().unwrap();
            state.trace.push(format!("submit:{sequence}:{slot}"));
            state.submitted.push((sequence, slot));
            Ok(())
        }

        fn try_queue_write(
            &mut self,
            sequence: u32,
            slot: usize,
        ) -> Result<CoordinatorWriteStatus, ReadbackError> {
            let status = {
                let mut state = self.state.lock().unwrap();
                state.trace.push(format!("write:{sequence}:{slot}"));
                if state.output_blocked || state.output_queue.len() >= state.output_capacity {
                    return Ok(CoordinatorWriteStatus::Full);
                }
                state
                    .write_status
                    .pop_front()
                    .unwrap_or(CoordinatorWriteStatus::Full)
            };
            if matches!(&status, CoordinatorWriteStatus::Queued) {
                self.state
                    .lock()
                    .unwrap()
                    .trace
                    .push(format!("mapped_view_acquire:{slot}"));
                let view = FakeMappedView {
                    state: Arc::clone(&self.state),
                    slot,
                };
                assert_eq!(view.slot, slot);
                drop(view);
                let mut state = self.state.lock().unwrap();
                state.trace.push(format!("unmap:{slot}"));
                state.writes_queued.push_back((sequence, slot));
                state.output_queue.push_back((sequence, slot));
            }
            Ok(status)
        }

        fn unmap_readback(&mut self, slot: usize) -> Result<(), ReadbackError> {
            self.state
                .lock()
                .unwrap()
                .trace
                .push(format!("unmap:{slot}"));
            Ok(())
        }

        fn try_writer_ack(&mut self) -> Result<Option<CoordinatorWriteAck>, ReadbackError> {
            let mut state = self.state.lock().unwrap();
            if state.output_blocked {
                return Ok(None);
            }
            let Some(next_ack) = state.acks.front() else {
                return Ok(None);
            };
            let (ack_sequence, ack_slot) = match next_ack {
                CoordinatorWriteAck::Flushed { sequence, slot } => (*sequence, *slot),
            };
            if !state
                .writes_queued
                .iter()
                .any(|written| written == &(ack_sequence, ack_slot))
            {
                return Ok(None);
            }
            let ack = state.acks.pop_front();
            if let Some(CoordinatorWriteAck::Flushed { sequence, slot }) = ack.as_ref() {
                if let Some(index) = state
                    .writes_queued
                    .iter()
                    .position(|written| written == &(*sequence, *slot))
                {
                    state.writes_queued.remove(index);
                }
                state.trace.push("flush_ack".into());
                if state.deferred_upload_active {
                    state.deferred_recovered = true;
                }
                if let Some(index) = state
                    .output_queue
                    .iter()
                    .position(|written| written == &(*sequence, *slot))
                {
                    state.output_queue.remove(index);
                }
            }
            Ok(ack)
        }

        fn unblock_input(&mut self) {
            let mut state = self.state.lock().unwrap();
            state.input_unblocked = true;
            state.input.clear();
            state.trace.push("unblock_input".into());
        }

        fn unblock_output(&mut self) {
            let mut state = self.state.lock().unwrap();
            state.output_unblocked = true;
            state.output_blocked = false;
            state.output_queue.clear();
            state.trace.push("unblock_output".into());
        }

        fn join_input(&mut self) -> Result<(), ReadbackError> {
            let mut state = self.state.lock().unwrap();
            state.trace.push("join_input".into());
            if state.require_unblock_before_join && !state.input_unblocked {
                return Err(std::io::Error::other("input joined before unblock").into());
            }
            Ok(())
        }

        fn join_writer(&mut self) -> Result<(), ReadbackError> {
            let mut state = self.state.lock().unwrap();
            state.trace.push("join_writer".into());
            if state.require_unblock_before_join && !state.output_unblocked {
                return Err(std::io::Error::other("writer joined before unblock").into());
            }
            if state.writer_join_fails {
                Err(std::io::Error::other("writer join failed").into())
            } else {
                Ok(())
            }
        }
    }

    #[test]
    fn gpu_completion_retires_upload_credit_while_source_receive_is_empty() {
        let (driver, state) = FakeDriver::source_stalled_with_upload_completion();
        let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
        state.lock().unwrap().trace.clear();
        coordinator.step().expect("advance coordinated work");
        let state = state.lock().unwrap();
        assert_eq!(
            state.trace,
            ["poll_gpu", "retire_upload_credit", "parser_try_receive"],
            "GPU completion must progress while nonblocking source receive is empty"
        );
        assert!(!state.upload_credit_live);
    }

    #[test]
    fn reversed_map_callbacks_write_only_in_frame_sequence_order() {
        let (driver, state) = FakeDriver::reversed_map_callbacks();
        let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
        coordinator.run().expect("finish ordered stream");
        let state = state.lock().unwrap();
        let write_order = state
            .trace
            .iter()
            .filter_map(|event| event.strip_prefix("write:"))
            .map(str::to_owned)
            .collect::<Vec<_>>();
        assert_eq!(write_order, ["0:0", "1:1"]);
    }

    #[test]
    fn frame_at_readiness_boundary_is_not_submitted() {
        let (driver, state) = FakeDriver::reversed_map_callbacks();
        {
            let mut state = state.lock().unwrap();
            state.input = VecDeque::from([
                CoordinatorInput::Header {
                    frame_count: 2,
                    fps_num: 30,
                    fps_den: 1,
                },
                CoordinatorInput::Watermark { ready_vpos: 3 },
            ]);
            state.maps.clear();
            state.map_min_submissions.clear();
            state.acks.clear();
        }
        let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
        for _ in 0..6 {
            coordinator.step().expect("advance to strict boundary");
        }
        assert_eq!(
            state.lock().unwrap().submitted,
            [(0, 0)],
            "FrameClock(1) equals ready=3 and must wait under strict < readiness"
        );
    }

    #[test]
    fn successful_completion_reports_only_registered_asset_pages() {
        for (registered_asset_pages, expected) in [(1, 1), (0, 0)] {
            let (driver, state) = FakeDriver::reversed_map_callbacks();
            state.lock().unwrap().registered_asset_pages = registered_asset_pages;
            let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
            let completion = coordinator.run().expect("finish ordered stream");
            assert_eq!(
                completion.asset_page_count, expected,
                "completion count comes from successfully registered resident assets, not declarations"
            );
        }
    }

    #[test]
    fn zero_declaration_stream_completion_releases_all_frames_without_watermark() {
        let (driver, state) = FakeDriver::empty_stream_without_watermark();
        let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
        coordinator.step().expect("read Header");
        coordinator
            .step()
            .expect("process validated empty-stream Complete");
        let submitted = state.lock().unwrap().submitted.clone();
        coordinator
            .cancel_and_join()
            .expect("clean up incomplete fake run");
        assert_eq!(
            submitted,
            [(0, 0), (1, 1)],
            "End+EOF is the final readiness boundary when an empty stream has no declarations"
        );
    }

    #[test]
    fn credit_deferral_retries_while_readback_can_retire_temporary_budget() {
        let (driver, state) = FakeDriver::credits_deferred_behind_frame();
        let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
        coordinator.step().expect("read Header");
        coordinator.step().expect("read readiness and submit frame");
        assert_eq!(state.lock().unwrap().submitted, [(0, 0)]);
        coordinator
            .step()
            .expect("defer asset while frame can retire shared GPU credit");
        assert!(
            state
                .lock()
                .unwrap()
                .trace
                .contains(&"submit:0:0".to_owned()),
            "the deferral follows an actually submitted frame"
        );
        let completion = coordinator
            .run()
            .expect("retry succeeds after flush retires credit");
        assert!(state.lock().unwrap().deferred_recovered);
        assert_eq!(
            completion.asset_page_count, 0,
            "a deferred upload that never reaches registration does not count"
        );
    }

    #[test]
    fn credit_deferral_fails_when_no_inflight_or_ready_frame_can_release_budget() {
        let (driver, state) = FakeDriver::credits_deferred_without_progress();
        let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
        coordinator.step().expect("read Header");
        coordinator
            .step()
            .expect("read zero readiness without submitting a frame");
        let error = coordinator
            .step()
            .expect_err("unrecoverable GPU budget exhaustion must not retry forever");
        assert!(error.to_string().contains("GPU upload credits unavailable"));
        assert!(state.lock().unwrap().submitted.is_empty());
    }

    #[test]
    fn credit_deferral_fails_when_ready_frame_has_no_free_slot_and_output_is_blocked() {
        let (driver, state) = FakeDriver::full_ring_blocks_ready_frame_with_deferred_credit();
        let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
        coordinator.step().expect("read Header");
        coordinator
            .step()
            .expect("read watermark and submit first frame");
        let error = coordinator.step().expect_err(
            "a ready frame cannot help while the ring is full and no GPU work is pending",
        );
        assert!(error.to_string().contains("GPU upload credits unavailable"));
        let state = state.lock().unwrap();
        assert_eq!(state.submitted, [(0, 0)]);
        assert!(state.output_blocked);
    }

    #[test]
    fn writing_slot_and_credit_wait_for_flush_ack_and_unmap_follows_view_drop() {
        let (driver, state) = FakeDriver::one_slot_waiting_for_flush_ack();
        let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
        for _ in 0..8 {
            coordinator.step().unwrap();
        }
        let before_ack = state.lock().unwrap();
        assert_eq!(before_ack.submitted, [(0, 0)]);
        assert!(before_ack.live_readback_credits.contains(&0));
        let view_drop = before_ack
            .trace
            .iter()
            .position(|event| event == "mapped_view_drop:0")
            .unwrap();
        let unmap = before_ack
            .trace
            .iter()
            .position(|event| event == "unmap:0")
            .unwrap();
        assert!(view_drop < unmap);
        drop(before_ack);

        {
            let mut state = state.lock().unwrap();
            state.acks.push_back(CoordinatorWriteAck::Flushed {
                sequence: 0,
                slot: 0,
            });
        }
        for _ in 0..8 {
            coordinator.step().unwrap();
        }
        let after_ack = state.lock().unwrap();
        assert!(after_ack.live_readback_credits.contains(&0));
        assert_eq!(after_ack.submitted, [(0, 0), (1, 0)]);
        assert_eq!(
            after_ack
                .trace
                .iter()
                .filter(|entry| entry.as_str() == "credit_release_slot:0")
                .count(),
            0,
            "flush ack and physical slot reuse do not release allocation credit"
        );
        drop(after_ack);
        drop(coordinator);
        let released = state.lock().unwrap();
        assert!(!released.live_readback_credits.contains(&0));
        assert_eq!(
            released
                .trace
                .iter()
                .filter(|entry| entry.as_str() == "credit_release_slot:0")
                .count(),
            1,
            "dropping the ring owner releases its persistent slot credit once"
        );
    }

    #[test]
    fn cancel_unblocks_input_and_output_before_joins_and_reports_blocked_writer() {
        let (driver, state) = FakeDriver::full_queues_with_blocked_output();
        {
            let mut state = state.lock().unwrap();
            state.writer_join_fails = true;
            assert_eq!(state.input.len(), state.input_capacity, "input queue full");
            assert_eq!(
                state.output_queue.len(),
                state.output_capacity,
                "output queue full"
            );
            assert!(state.output_blocked, "writer is blocked downstream");
        }
        let mut coordinator = StreamReadbackCoordinator::new(driver).expect("reserve ring");
        let result = coordinator.cancel_and_join();
        assert!(
            result.is_err(),
            "writer join failure is not successful cleanup"
        );
        let state = state.lock().unwrap();
        let trace = &state.trace;
        assert!(state.input_unblocked);
        assert!(state.output_unblocked);
        let input = trace
            .iter()
            .position(|event| event == "unblock_input")
            .unwrap();
        let output = trace
            .iter()
            .position(|event| event == "unblock_output")
            .unwrap();
        let join_input = trace
            .iter()
            .position(|event| event == "join_input")
            .unwrap();
        let join_writer = trace
            .iter()
            .position(|event| event == "join_writer")
            .unwrap();
        assert!(input < join_input);
        assert!(output < join_writer);
    }
}

#[test]
fn pre_read_header_returns_the_original_budgeted_parser_event() {
    use nico_compositord::readback::receive_incremental_header;
    use nico_compositord::stream_budget::ParserReceive;
    use nico_compositord::stream_protocol::{spawn_incremental_stream_parser, IncrementalEvent};

    const STREAM: &[u8] =
        include_bytes!("../../../internal/nicorender/testdata/timeline/stream/empty-scene.nct2");
    let mut parser = spawn_incremental_stream_parser(Cursor::new(STREAM), || {});
    let event = receive_incremental_header(&parser).expect("validated Header event");
    assert!(event.payload.reserved_bytes() >= std::mem::size_of::<IncrementalEvent>());
    match event.payload.get() {
        IncrementalEvent::Header(header) => {
            assert_eq!((header.width, header.height), (33, 19));
            assert_eq!(header.frame_count, 3);
        }
        other => panic!("pre-read handoff returned {other:?}, not Header"),
    }
    assert!(matches!(
        parser
            .recv_timeout(std::time::Duration::from_secs(1))
            .unwrap(),
        ParserReceive::Event(_)
    ));
    parser.cancel();
    parser
        .join()
        .expect("parser joins after pre-read cancellation");
}

#[test]
fn readback_rows_follow_wgpu_256_byte_alignment() {
    assert_eq!(padded_bytes_per_row(33).unwrap(), 256);
    assert_eq!(padded_bytes_per_row(1920).unwrap(), 7680);
    assert!(padded_bytes_per_row(0).is_err());
    assert!(padded_bytes_per_row(u32::MAX).is_err());
}

#[test]
fn padded_rows_are_written_tightly_packed_and_in_order() {
    let stride = 256;
    let mut mapped = vec![0xcc; stride * 2];
    mapped[0..8].copy_from_slice(&[1, 2, 3, 4, 5, 6, 7, 8]);
    mapped[stride..stride + 8].copy_from_slice(&[9, 10, 11, 12, 13, 14, 15, 16]);
    let mut output = Cursor::new(Vec::new());

    write_padded_rows(&mut output, &mapped, 2, 2, stride as u32).unwrap();

    assert_eq!(output.into_inner(), (1..=16).collect::<Vec<u8>>());
}

#[derive(Default)]
struct CountingWriter {
    write_calls: usize,
    bytes: Vec<u8>,
}

impl Write for CountingWriter {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.write_calls += 1;
        self.bytes.extend_from_slice(bytes);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

#[test]
fn tightly_packed_rows_are_written_as_one_contiguous_chunk() {
    let mapped: Vec<_> = (0..3 * 256).map(|value| value as u8).collect();
    let mut output = CountingWriter::default();

    write_padded_rows(&mut output, &mapped, 64, 3, 256).unwrap();

    assert_eq!(output.write_calls, 1);
    assert_eq!(output.bytes, mapped);
}

#[test]
fn row_writer_rejects_truncated_mapped_storage() {
    let mut output = Vec::new();
    let error = write_padded_rows(&mut output, &[0; 255], 1, 2, 256).unwrap_err();
    assert!(error
        .to_string()
        .contains("mapped readback buffer is shorter"));
    assert!(output.is_empty());
}

#[test]
fn nct2_status_markers_require_registered_upload_and_validated_end_eof() {
    use nico_compositord::readback::TimelineStatusEmitter;
    use nico_compositord::stream_budget::ParserReceive;
    use nico_compositord::stream_protocol::{spawn_incremental_stream_parser, IncrementalEvent};
    use nico_compositord::stream_renderer::{UploadAttempt, UploadDeferredReason};
    use std::time::Duration;

    let mut status = TimelineStatusEmitter::new(Vec::new());
    status
        .observe_upload_attempt(&UploadAttempt::<()>::Deferred {
            reason: UploadDeferredReason::CreditsUnavailable,
            event: (),
        })
        .unwrap();
    status
        .observe_upload_attempt(&UploadAttempt::<()>::Submitted {
            asset_id: 4,
            token: 9,
        })
        .unwrap();
    status
        .observe_upload_attempt(&UploadAttempt::<()>::Submitted {
            asset_id: 4,
            token: 9,
        })
        .unwrap();

    const STREAM: &[u8] =
        include_bytes!("../../../internal/nicorender/testdata/timeline/stream/empty-scene.nct2");
    let mut parser = spawn_incremental_stream_parser(Cursor::new(STREAM), || {});
    loop {
        match parser.recv_timeout(Duration::from_secs(1)).unwrap() {
            ParserReceive::Event(event) => {
                status.observe_parser_event(event.payload.get()).unwrap();
                if matches!(event.payload.get(), IncrementalEvent::Complete(_)) {
                    // Re-observing a successful terminal event must not duplicate the marker.
                    status.observe_parser_event(event.payload.get()).unwrap();
                    break;
                }
            }
            ParserReceive::Closed => panic!("parser closed without validated End+EOF"),
            ParserReceive::Empty | ParserReceive::TimedOut => continue,
        }
    }
    parser.join().unwrap();
    assert_eq!(
        status.into_inner(),
        b"NICO_FIRST_ASSET_READY\nNICO_STREAM_END\n"
    );
}

#[test]
fn nct2_status_markers_do_not_claim_failed_upload_or_invalid_end() {
    use nico_compositord::readback::TimelineStatusEmitter;
    use nico_compositord::stream_budget::ParserReceive;
    use nico_compositord::stream_protocol::{spawn_incremental_stream_parser, IncrementalEvent};
    use nico_compositord::stream_renderer::{UploadAttempt, UploadDeferredReason};
    use std::time::Duration;

    const STREAM: &[u8] =
        include_bytes!("../../../internal/nicorender/testdata/timeline/stream/empty-scene.nct2");
    let mut trailing = STREAM.to_vec();
    trailing.push(0xA5);
    let invalid_streams = [STREAM[..STREAM.len() - 1].to_vec(), trailing];
    for invalid in invalid_streams {
        let mut status = TimelineStatusEmitter::new(Vec::new());
        status
            .observe_upload_attempt(&UploadAttempt::<()>::Deferred {
                reason: UploadDeferredReason::Cancelled,
                event: (),
            })
            .unwrap();

        let mut parser = spawn_incremental_stream_parser(Cursor::new(invalid), || {});
        loop {
            match parser.recv_timeout(Duration::from_secs(1)).unwrap() {
                ParserReceive::Event(event) => {
                    status.observe_parser_event(event.payload.get()).unwrap();
                    if matches!(event.payload.get(), IncrementalEvent::Complete(_)) {
                        break;
                    }
                }
                ParserReceive::Closed => panic!("parser closed without a terminal event"),
                ParserReceive::Empty | ParserReceive::TimedOut => continue,
            }
        }
        parser.join().unwrap();
        assert!(status.into_inner().is_empty());
    }
}

#[test]
fn nct2_status_markers_are_not_emitted_for_cancelled_parser_input() {
    use nico_compositord::readback::TimelineStatusEmitter;
    use nico_compositord::stream_protocol::spawn_incremental_stream_parser;
    use nico_compositord::stream_renderer::{UploadAttempt, UploadDeferredReason};
    use std::sync::{Arc, Condvar, Mutex};

    struct BlockedReader {
        gate: Arc<(Mutex<bool>, Condvar)>,
    }

    impl io::Read for BlockedReader {
        fn read(&mut self, _buffer: &mut [u8]) -> io::Result<usize> {
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
    let mut status = TimelineStatusEmitter::new(Vec::new());
    status
        .observe_upload_attempt(&UploadAttempt::<()>::Deferred {
            reason: UploadDeferredReason::Cancelled,
            event: (),
        })
        .unwrap();
    parser.cancel();
    parser.join().unwrap();
    assert!(parser.budget_used_bytes() == 0);
    assert!(status.into_inner().is_empty());
}
