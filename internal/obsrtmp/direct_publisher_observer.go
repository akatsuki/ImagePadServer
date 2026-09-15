package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/airplaycontract"
)

type directRecordingProbeJob struct {
	artifacts      airplaycontract.PublisherArtifacts
	finalized      airplaycontract.Event
	hasRealVideo   bool
	expectedWidth  int
	expectedHeight int
}

type directGenerationReadiness struct {
	PublisherReady bool
	MediaReady     bool
}

type directPublisherObserver struct {
	mu                           sync.Mutex
	root                         string
	rootFS                       *os.Root
	sessionID                    string
	deliveryLedger               *airplaycontract.DeliveryLedger
	recoveryOwner                airplaycontract.PublisherRecoveryOwner
	currentGeneration            uint64
	publishedGeneration          uint64
	candidateGeneration          uint64
	artifacts                    map[uint64]airplaycontract.PublisherArtifacts
	deliveryGenerations          map[uint64]airplaycontract.DeliveryGeneration
	generationReadinessState     map[uint64]directGenerationReadiness
	events                       map[uint64]airplaycontract.Event
	finalizedEvents              map[uint64]airplaycontract.Event
	completions                  map[uint64]airplaycontract.PublisherCompletion
	terminationTrackers          map[uint64]*streamTerminationTracker
	terminationEvents            map[uint64][]airplaycontract.TerminationObservation
	terminationSequence          uint64
	terminationLog               func(string, ...any)
	outcomes                     map[uint64]airplaycontract.RecordingOutcome
	recordingOutcomeDone         map[uint64]chan struct{}
	recordingProbe               directRecordingProbeFunc
	recordingProbeTimeout        time.Duration
	recordingProbeWidth          int
	recordingProbeHeight         int
	recordingProbeDone           chan struct{}
	recordingOutcomeHandler      func(airplaycontract.RecordingOutcome)
	recordingOutcomesDoneHandler func(string)
	sealed                       bool
	beforePrepareLock            func()
	beforeCandidateClaim         func()
}

var _ airplaycontract.PublisherObserver = (*directPublisherObserver)(nil)
var _ airplaycontract.TerminationInitiatorObserver = (*directPublisherObserver)(nil)

func newDirectPublisherObserver(root, sessionID string) (*directPublisherObserver, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("direct publisher artifact root is empty")
	}
	if !validDirectPublisherSessionID(sessionID) {
		return nil, errors.New("direct publisher session ID is unsafe")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve direct publisher artifact root: %w", err)
	}
	rootFS, err := os.OpenRoot(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("open direct publisher artifact root: %w", err)
	}
	return &directPublisherObserver{
		root:                     filepath.Clean(absoluteRoot),
		rootFS:                   rootFS,
		sessionID:                sessionID,
		artifacts:                make(map[uint64]airplaycontract.PublisherArtifacts),
		deliveryGenerations:      make(map[uint64]airplaycontract.DeliveryGeneration),
		generationReadinessState: make(map[uint64]directGenerationReadiness),
		events:                   make(map[uint64]airplaycontract.Event),
		finalizedEvents:          make(map[uint64]airplaycontract.Event),
		completions:              make(map[uint64]airplaycontract.PublisherCompletion),
		terminationTrackers:      make(map[uint64]*streamTerminationTracker),
		terminationEvents:        make(map[uint64][]airplaycontract.TerminationObservation),
		terminationLog:           log.Printf,
		outcomes:                 make(map[uint64]airplaycontract.RecordingOutcome),
		recordingOutcomeDone:     make(map[uint64]chan struct{}),
	}, nil
}

func newDirectPublisherObserverWithLedger(root string, ledger *airplaycontract.DeliveryLedger) (*directPublisherObserver, error) {
	if ledger == nil {
		return nil, errors.New("direct publisher delivery ledger is nil")
	}
	snapshot := ledger.Snapshot()
	observer, err := newDirectPublisherObserver(root, snapshot.SessionID)
	if err != nil {
		return nil, err
	}
	observer.deliveryLedger = ledger
	return observer, nil
}

func validDirectPublisherSessionID(sessionID string) bool {
	if len(sessionID) == 16 {
		for _, char := range sessionID {
			if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
				return false
			}
		}
		return true
	}
	if len(sessionID) == 0 || len(sessionID) > 20 {
		return false
	}
	for _, char := range sessionID {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func publisherArtifactPaths(artifacts airplaycontract.PublisherArtifacts) []string {
	return []string{
		artifacts.Recording,
		artifacts.Ready,
		artifacts.MediaReady,
		artifacts.EventLog,
		artifacts.ProcessLog,
		artifacts.StopRequest,
	}
}

func publisherArtifactRelativePaths(sessionID string, generation uint64) []string {
	artifacts := airplaycontract.NewPublisherArtifacts(".", sessionID, generation)
	return publisherArtifactPaths(artifacts)
}

func (o *directPublisherObserver) PreparePublisher(ctx context.Context, artifacts airplaycontract.PublisherArtifacts) error {
	if o != nil && o.deliveryLedger != nil {
		// Only the initial owner-issued allocation can use the legacy-shaped
		// launch call. Later generations must be allocated by the coordinator.
		snapshot := o.deliveryLedger.Snapshot()
		descriptor, allocated := o.deliveryLedger.Lookup(artifacts.Generation)
		if !allocated || artifacts.Generation != 1 || descriptor.ArtifactPaths != artifacts || snapshot.ActiveGeneration != 0 || snapshot.LastAllocatedGeneration != 1 {
			return airplaycontract.ErrDeliveryGenerationMismatch
		}
		if err := o.preparePublisher(ctx, artifacts, &descriptor); err != nil {
			return err
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if o.sealed {
			return errDirectReconfigureUnavailable
		}
		return o.deliveryLedger.Commit(0, descriptor)
	}
	return o.preparePublisher(ctx, artifacts, nil)
}

func (o *directPublisherObserver) PrepareDeliveryGeneration(ctx context.Context, descriptor airplaycontract.DeliveryGeneration) error {
	return o.preparePublisher(ctx, descriptor.ArtifactPaths, &descriptor)
}

func (o *directPublisherObserver) preparePublisher(ctx context.Context, artifacts airplaycontract.PublisherArtifacts, descriptor *airplaycontract.DeliveryGeneration) error {
	if o == nil {
		return errors.New("direct publisher observer is nil")
	}
	if ctx == nil {
		return errors.New("direct publisher prepare context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if artifacts.Generation == 0 {
		return errors.New("direct publisher generation is zero")
	}
	expected := airplaycontract.NewPublisherArtifacts(o.root, o.sessionID, artifacts.Generation)
	if artifacts != expected {
		return errors.New("direct publisher artifacts are not canonical for this session")
	}
	if descriptor != nil {
		if o.deliveryLedger == nil {
			return errors.New("direct publisher delivery ledger is unavailable")
		}
		if descriptor.SessionID != o.sessionID || descriptor.Generation != artifacts.Generation || descriptor.ArtifactPaths != artifacts ||
			descriptor.ExpectedRecordingWidth <= 0 || descriptor.ExpectedRecordingHeight <= 0 {
			return airplaycontract.ErrDeliveryGenerationMismatch
		}
	}
	if o.beforePrepareLock != nil {
		o.beforePrepareLock()
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if descriptor == nil && artifacts.Generation <= o.currentGeneration {
		return errors.New("direct publisher generation was already used")
	}
	if o.sealed {
		return errors.New("direct publisher observer is sealed")
	}
	if _, exists := o.artifacts[artifacts.Generation]; exists {
		return errors.New("direct publisher generation was already registered")
	}
	if o.rootFS == nil {
		return errors.New("direct publisher artifact root is closed")
	}
	relativeDirectory := filepath.Join("airplay", o.sessionID)
	if err := o.rootFS.MkdirAll(relativeDirectory, 0700); err != nil {
		return fmt.Errorf("create direct publisher artifact directory: %w", err)
	}
	for _, path := range publisherArtifactRelativePaths(o.sessionID, artifacts.Generation) {
		if _, err := o.rootFS.Lstat(path); err == nil {
			return fmt.Errorf("direct publisher artifact already exists: %s", filepath.Join(o.root, path))
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect direct publisher artifact %s: %w", filepath.Join(o.root, path), err)
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if descriptor != nil {
		if err := o.deliveryLedger.Prepare(*descriptor); err != nil {
			return err
		}
	}
	o.artifacts[artifacts.Generation] = artifacts
	o.terminationTrackers[artifacts.Generation] = newStreamTerminationTracker(o.sessionID, artifacts.Generation)
	if descriptor == nil {
		o.currentGeneration = artifacts.Generation
	} else {
		o.deliveryGenerations[artifacts.Generation] = *descriptor
	}
	return nil
}

func (o *directPublisherObserver) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.rootFS == nil {
		return nil
	}
	err := o.rootFS.Close()
	o.rootFS = nil
	return err
}

func (o *directPublisherObserver) ObservePublisher(event airplaycontract.Event) {
	if o == nil || event.Schema != 2 || event.SessionID != o.sessionID || event.PublisherGeneration == 0 || event.At.IsZero() {
		return
	}
	o.mu.Lock()
	if o.sealed {
		o.mu.Unlock()
		return
	}
	if artifacts, registered := o.artifacts[event.PublisherGeneration]; registered {
		readiness := o.generationReadinessState[event.PublisherGeneration]
		if event.ReadyFor(o.sessionID, artifacts.Generation) {
			readiness.PublisherReady = true
		}
		if event.MediaReadyFor(o.sessionID, artifacts.Generation) {
			readiness.MediaReady = true
		}
		o.generationReadinessState[event.PublisherGeneration] = readiness
		if event.Event == "rtsp-eof" {
			if event.RTSPEOFFor(o.sessionID, artifacts.Generation) {
				o.observeTerminationLocked(artifacts.Generation, event.At,
					airplaycontract.TerminationComponentRTSPEOF, event.ProcessID,
					airplaycontract.TerminationEvidenceRTSPEOF, 0,
					airplaycontract.TerminationReasonRTSPEOF)
			}
		} else if event.Event == "recording-finalized" {
			if event.RecordingFinalizedFor(o.sessionID, artifacts.Generation, artifacts.Recording) {
				o.finalizedEvents[event.PublisherGeneration] = event
			}
		} else {
			o.events[event.PublisherGeneration] = event
		}
	}
	o.mu.Unlock()
}

func (o *directPublisherObserver) recordingFinalizedEvent(generation uint64) (airplaycontract.Event, bool) {
	if o == nil || generation == 0 {
		return airplaycontract.Event{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	event, ok := o.finalizedEvents[generation]
	return event, ok
}

func (o *directPublisherObserver) generationReadiness(descriptor airplaycontract.DeliveryGeneration) (directGenerationReadiness, bool) {
	if o == nil || descriptor.Generation == 0 {
		return directGenerationReadiness{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	registered, ok := o.deliveryGenerations[descriptor.Generation]
	if !ok || registered != descriptor {
		return directGenerationReadiness{}, false
	}
	return o.generationReadinessState[descriptor.Generation], true
}

func (o *directPublisherObserver) CompletePublisher(completion airplaycontract.PublisherCompletion) {
	if o == nil || completion.Artifacts.Generation == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sealed {
		return
	}
	registered, ok := o.artifacts[completion.Artifacts.Generation]
	if !ok || registered != completion.Artifacts {
		return
	}
	if _, completed := o.completions[completion.Artifacts.Generation]; completed {
		return
	}
	o.completions[completion.Artifacts.Generation] = completion
	o.observePublisherTerminationLocked(completion)
}

func (o *directPublisherObserver) observePublisherTerminationLocked(completion airplaycontract.PublisherCompletion) {
	if !completion.Started || !completion.ExitConfirmed || !completion.ExitCodeKnown ||
		completion.ProcessID <= 0 || completion.CompletedAt.IsZero() {
		return
	}
	o.observeTerminationLocked(completion.Artifacts.Generation, completion.CompletedAt,
		airplaycontract.TerminationComponentPublisher, completion.ProcessID,
		airplaycontract.TerminationEvidencePublisherExit, completion.ExitCode,
		airplaycontract.TerminationReasonPublisherExited)
}

func (o *directPublisherObserver) observeMediaMTXExit(processID int, at time.Time, exitCode int, exitCodeKnown bool) bool {
	if o == nil || !exitCodeKnown || processID <= 0 || at.IsZero() {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sealed || o.currentGeneration == 0 {
		return false
	}
	return o.observeTerminationLocked(o.currentGeneration, at,
		airplaycontract.TerminationComponentMediaMTX, processID,
		airplaycontract.TerminationEvidenceProcessExit, exitCode,
		airplaycontract.TerminationReasonProcessExited)
}

func (o *directPublisherObserver) ObserveTerminationInitiator(sessionID string, generation uint64, reason string) {
	if o == nil || sessionID != o.sessionID || generation == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sealed {
		return
	}
	if tracker := o.terminationTrackers[generation]; tracker != nil {
		if tracker.MarkInitiator(reason) && o.terminationLog != nil {
			o.terminationLog("AirPlay termination: session=%s publisher_generation=%d event=initiator sequence=0 process_id=0 at_utc=%s exit_code=0 reason=%s initiator=%s",
				o.sessionID, generation, time.Now().UTC().Format(time.RFC3339Nano), reason, reason)
		}
	}
}

// Strict backend monitors bind exit evidence to the generation they own,
// never whichever publisher happens to be current when the process exits.
func (o *directPublisherObserver) observeMediaMTXGenerationExit(generation uint64, processID int, at time.Time, exitCode int, exitCodeKnown bool) bool {
	if o == nil || generation == 0 || !exitCodeKnown || processID <= 0 || at.IsZero() {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sealed {
		return false
	}
	return o.observeTerminationLocked(generation, at,
		airplaycontract.TerminationComponentMediaMTX, processID,
		airplaycontract.TerminationEvidenceProcessExit, exitCode,
		airplaycontract.TerminationReasonProcessExited)
}

func (o *directPublisherObserver) observeTerminationLocked(generation uint64, at time.Time, component string, processID int, evidence string, exitCode int, reason string) bool {
	tracker := o.terminationTrackers[generation]
	if tracker == nil || o.terminationSequence == ^uint64(0) {
		return false
	}
	observation := airplaycontract.TerminationObservation{
		Schema:              2,
		SessionID:           o.sessionID,
		PublisherGeneration: generation,
		At:                  at,
		Component:           component,
		ProcessID:           processID,
		Sequence:            o.terminationSequence + 1,
		Evidence:            evidence,
		ExitCode:            exitCode,
		Reason:              reason,
	}
	if !observation.ValidFor(o.sessionID, generation) {
		return false
	}
	o.terminationSequence = observation.Sequence
	tracker.Observe(observation)
	o.terminationEvents[generation] = append(o.terminationEvents[generation], observation)
	if o.terminationLog != nil {
		snapshot, _ := tracker.Snapshot()
		o.terminationLog("AirPlay termination: session=%s publisher_generation=%d event=%s sequence=%d process_id=%d at_utc=%s exit_code=%d reason=%s initiator=%s",
			o.sessionID, generation, component, observation.Sequence, processID,
			observation.At.UTC().Format(time.RFC3339Nano), exitCode, reason, snapshot.Initiator)
	}
	return true
}

func (o *directPublisherObserver) terminationSnapshot(generation uint64) (StreamTermination, bool) {
	if o == nil || generation == 0 {
		return StreamTermination{}, false
	}
	o.mu.Lock()
	tracker := o.terminationTrackers[generation]
	o.mu.Unlock()
	if tracker == nil {
		return StreamTermination{}, false
	}
	return tracker.Snapshot()
}

func (o *directPublisherObserver) terminationObservations(generation uint64) []airplaycontract.TerminationObservation {
	if o == nil || generation == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]airplaycontract.TerminationObservation(nil), o.terminationEvents[generation]...)
}

func (o *directPublisherObserver) publisherCompletion(generation uint64) (airplaycontract.PublisherCompletion, bool) {
	if o == nil || generation == 0 {
		return airplaycontract.PublisherCompletion{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	completion, ok := o.completions[generation]
	return completion, ok
}

func (o *directPublisherObserver) SealPublishers() {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.sealed {
		o.mu.Unlock()
		return
	}
	o.sealed = true
	rootFS := o.rootFS
	o.rootFS = nil
	artifacts := make([]airplaycontract.PublisherArtifacts, 0, len(o.artifacts))
	for _, descriptor := range o.artifacts {
		artifacts = append(artifacts, descriptor)
	}
	probe := o.recordingProbe
	probeTimeout := o.recordingProbeTimeout
	probeDone := o.recordingProbeDone
	probeJobs := o.recordingProbeJobsLocked()
	outcomesDoneHandler := o.recordingOutcomesDoneHandler
	sessionID := o.sessionID
	o.mu.Unlock()
	if rootFS != nil {
		_ = rootFS.Close()
	}
	for _, descriptor := range artifacts {
		cleanupDirectPublisherEventSnapshots(descriptor)
	}
	if probeDone != nil {
		go func() {
			defer close(probeDone)
			if len(probeJobs) == 0 {
				return
			}
			for _, job := range probeJobs {
				outcome := probeDirectRecording(context.Background(), probeTimeout, job.artifacts, job.finalized, job.hasRealVideo, job.expectedWidth, job.expectedHeight, probe)
				o.FinishRecording(outcome)
			}
			if outcomesDoneHandler != nil {
				outcomesDoneHandler(sessionID)
			}
		}()
	}
}

func (o *directPublisherObserver) configureRecordingProbe(timeout time.Duration, width, height int, probe directRecordingProbeFunc) error {
	if o == nil {
		return errors.New("direct publisher observer is nil")
	}
	if timeout <= 0 || width <= 0 || height <= 0 || probe == nil {
		return errors.New("direct recording probe configuration is incomplete")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sealed {
		return errors.New("direct publisher observer is sealed")
	}
	if o.recordingProbeDone != nil {
		return errors.New("direct recording probe is already configured")
	}
	o.recordingProbe = probe
	o.recordingProbeTimeout = timeout
	o.recordingProbeWidth = width
	o.recordingProbeHeight = height
	o.recordingProbeDone = make(chan struct{})
	return nil
}

func (o *directPublisherObserver) configureRecordingOutcomeHandler(handler func(airplaycontract.RecordingOutcome)) error {
	if o == nil {
		return errors.New("direct publisher observer is nil")
	}
	if handler == nil {
		return errors.New("direct recording outcome handler is nil")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sealed {
		return errors.New("direct publisher observer is sealed")
	}
	if o.recordingOutcomeHandler != nil {
		return errors.New("direct recording outcome handler is already configured")
	}
	o.recordingOutcomeHandler = handler
	return nil
}

func (o *directPublisherObserver) configureRecordingOutcomesDoneHandler(handler func(string)) error {
	if o == nil {
		return errors.New("direct publisher observer is nil")
	}
	if handler == nil {
		return errors.New("direct recording outcomes done handler is nil")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sealed {
		return errors.New("direct publisher observer is sealed")
	}
	if o.recordingOutcomesDoneHandler != nil {
		return errors.New("direct recording outcomes done handler is already configured")
	}
	o.recordingOutcomesDoneHandler = handler
	return nil
}

func (o *directPublisherObserver) recordingProbeJobsLocked() []directRecordingProbeJob {
	jobs := make([]directRecordingProbeJob, 0, len(o.artifacts))
	for generation, artifacts := range o.artifacts {
		mediaEvent := o.events[generation]
		readiness := o.generationReadinessState[generation]
		job := directRecordingProbeJob{
			artifacts:      artifacts,
			hasRealVideo:   readiness.MediaReady || mediaEvent.MediaReadyFor(artifacts.SessionID, artifacts.Generation),
			expectedWidth:  o.recordingProbeWidth,
			expectedHeight: o.recordingProbeHeight,
		}
		if descriptor, ok := o.deliveryGenerations[generation]; ok {
			job.expectedWidth = descriptor.ExpectedRecordingWidth
			job.expectedHeight = descriptor.ExpectedRecordingHeight
		}
		completion, completed := o.completions[generation]
		if completed && completion.Started && completion.ExitConfirmed {
			job.finalized = o.finalizedEvents[generation]
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(left, right int) bool {
		return jobs[left].artifacts.Generation < jobs[right].artifacts.Generation
	})
	return jobs
}

func (o *directPublisherObserver) waitRecordingProbes(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	done := o.recordingProbeDone
	o.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *directPublisherObserver) FinishRecording(outcome airplaycontract.RecordingOutcome) {
	if o == nil || outcome.Artifacts.Generation == 0 {
		return
	}
	o.mu.Lock()
	registered, ok := o.artifacts[outcome.Artifacts.Generation]
	if !ok || registered != outcome.Artifacts {
		o.mu.Unlock()
		return
	}
	if done, finished := o.recordingOutcomeDone[outcome.Artifacts.Generation]; finished {
		o.mu.Unlock()
		<-done
		return
	}
	done := make(chan struct{})
	o.recordingOutcomeDone[outcome.Artifacts.Generation] = done
	o.outcomes[outcome.Artifacts.Generation] = outcome
	handler := o.recordingOutcomeHandler
	o.mu.Unlock()
	defer close(done)
	if handler != nil {
		handler(outcome)
	}
}

func (o *directPublisherObserver) currentArtifacts() (airplaycontract.PublisherArtifacts, bool) {
	if o == nil {
		return airplaycontract.PublisherArtifacts{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.currentArtifactsLocked()
}

func (o *directPublisherObserver) currentArtifactsLocked() (airplaycontract.PublisherArtifacts, bool) {
	generation := o.currentGeneration
	if o.deliveryLedger != nil {
		snapshot := o.deliveryLedger.Snapshot()
		if o.sealed || snapshot.TerminalEpoch != 0 || snapshot.SessionID != o.sessionID || snapshot.ActiveGeneration == 0 {
			return airplaycontract.PublisherArtifacts{}, false
		}
		generation = snapshot.ActiveGeneration
		descriptor, ok := o.deliveryGenerations[generation]
		ready := o.generationReadinessState[generation]
		if !ok || descriptor.SessionEpoch != snapshot.SessionEpoch || !ready.PublisherReady || !ready.MediaReady {
			return airplaycontract.PublisherArtifacts{}, false
		}
	}
	artifacts, ok := o.artifacts[generation]
	return artifacts, ok
}

func (o *directPublisherObserver) claimCurrentForPublication(artifacts airplaycontract.PublisherArtifacts) bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	current, ok := o.currentArtifactsLocked()
	if !ok || current != artifacts || o.publishedGeneration != 0 {
		return false
	}
	o.publishedGeneration = artifacts.Generation
	o.candidateGeneration = artifacts.Generation
	return true
}

func (o *directPublisherObserver) claimCurrentRecordingCandidate(artifacts airplaycontract.PublisherArtifacts) bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	hook := o.beforeCandidateClaim
	o.beforeCandidateClaim = nil
	o.mu.Unlock()
	if hook != nil {
		hook()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.claimCurrentRecordingCandidateLocked(artifacts)
}

func (o *directPublisherObserver) claimCurrentRecordingCandidateLocked(artifacts airplaycontract.PublisherArtifacts) bool {
	current, ok := o.currentArtifactsLocked()
	if !ok || current != artifacts || artifacts.Generation <= o.candidateGeneration {
		return false
	}
	o.candidateGeneration = artifacts.Generation
	return true
}

func (o *directPublisherObserver) setBeforeCandidateClaimForTest(hook func()) {
	o.mu.Lock()
	o.beforeCandidateClaim = hook
	o.mu.Unlock()
}

func (o *directPublisherObserver) allArtifacts() []airplaycontract.PublisherArtifacts {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	artifacts := make([]airplaycontract.PublisherArtifacts, 0, len(o.artifacts))
	for _, descriptor := range o.artifacts {
		artifacts = append(artifacts, descriptor)
	}
	return artifacts
}
