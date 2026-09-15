package airplaycontract

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrDeliveryGenerationInvalid     = errors.New("delivery generation is invalid")
	ErrDeliveryGenerationMismatch    = errors.New("delivery generation does not match its allocation")
	ErrDeliveryGenerationPending     = errors.New("delivery generation is already pending")
	ErrDeliveryGenerationNotPrepared = errors.New("delivery generation is not prepared")
	ErrDeliveryGenerationStale       = errors.New("delivery generation expected active generation is stale")
	ErrDeliveryGenerationOverflow    = errors.New("delivery generation counter overflow")
	ErrDeliveryLedgerTerminal        = errors.New("delivery generation ledger is terminal")
)

// DeliveryProfile is the resolved delivery-side profile for one publisher
// generation. It deliberately contains no UI labels or package-specific
// encoder types so the lifecycle contract remains dependency-free.
type DeliveryProfile struct {
	Mode               string
	Transport          string
	HLSVariant         string
	HLSSegmentCount    int
	HLSSegmentDuration string
}

// DeliveryOutput is the resolved encoder output for one publisher generation.
type DeliveryOutput struct {
	Width            int
	Height           int
	SourceFPS        int
	OutputFPS        int
	VideoBitrateKbps int
	MaxRateKbps      int
	BufferSizeKbps   int
	AudioBitrateBps  int
	GOPFrames        int
}

// DeliveryGeneration is an immutable-by-copy description of one delivery
// attempt. A failed or aborted attempt keeps its generation and artifact paths;
// neither may be reused by a later attempt.
type DeliveryGeneration struct {
	SessionID               string
	SessionEpoch            uint64
	RequestID               string
	Generation              uint64
	Profile                 DeliveryProfile
	Output                  DeliveryOutput
	ArtifactPaths           PublisherArtifacts
	ExpectedRecordingWidth  int
	ExpectedRecordingHeight int
}

type DeliveryLedgerSnapshot struct {
	SessionID               string
	SessionEpoch            uint64
	LastAllocatedGeneration uint64
	PreparedGeneration      uint64
	ActiveGeneration        uint64
	TerminalEpoch           uint64
}

type deliveryGenerationState uint8

const (
	deliveryGenerationAllocated deliveryGenerationState = iota + 1
	deliveryGenerationPrepared
	deliveryGenerationCommitted
	deliveryGenerationAborted
)

type deliveryGenerationEntry struct {
	descriptor     DeliveryGeneration
	expectedActive uint64
	state          deliveryGenerationState
}

// DeliveryLedger owns generation allocation and lifecycle state for exactly
// one receiver session epoch. It never starts or stops media processes.
type DeliveryLedger struct {
	mu                      sync.Mutex
	root                    string
	sessionID               string
	sessionEpoch            uint64
	lastAllocatedGeneration uint64
	preparedGeneration      uint64
	activeGeneration        uint64
	terminalEpoch           uint64
	entries                 map[uint64]deliveryGenerationEntry
}

func NewDeliveryLedger(root, sessionID string, sessionEpoch uint64) (*DeliveryLedger, error) {
	root = strings.TrimSpace(root)
	sessionID = strings.TrimSpace(sessionID)
	if root == "" || !validDeliverySessionID(sessionID) || sessionEpoch == 0 {
		return nil, ErrDeliveryGenerationInvalid
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve delivery artifact root: %w", err)
	}
	return &DeliveryLedger{
		root:         filepath.Clean(absoluteRoot),
		sessionID:    sessionID,
		sessionEpoch: sessionEpoch,
		entries:      make(map[uint64]deliveryGenerationEntry),
	}, nil
}

func (l *DeliveryLedger) Allocate(sessionID string, sessionEpoch, expectedActiveGeneration uint64, requestID string, profile DeliveryProfile, output DeliveryOutput) (DeliveryGeneration, error) {
	if l == nil {
		return DeliveryGeneration{}, ErrDeliveryGenerationInvalid
	}
	sessionID = strings.TrimSpace(sessionID)
	requestID = strings.TrimSpace(requestID)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validateIdentityLocked(sessionID, sessionEpoch); err != nil {
		return DeliveryGeneration{}, err
	}
	if l.terminalEpoch != 0 {
		return DeliveryGeneration{}, ErrDeliveryLedgerTerminal
	}
	if requestID == "" || !validDeliveryProfile(profile) || !validDeliveryOutput(output) {
		return DeliveryGeneration{}, ErrDeliveryGenerationInvalid
	}
	if expectedActiveGeneration != l.activeGeneration {
		return DeliveryGeneration{}, ErrDeliveryGenerationStale
	}
	if l.preparedGeneration != 0 || l.hasPendingAllocationLocked() {
		return DeliveryGeneration{}, ErrDeliveryGenerationPending
	}
	if l.lastAllocatedGeneration == math.MaxUint64 {
		return DeliveryGeneration{}, ErrDeliveryGenerationOverflow
	}
	generation := l.lastAllocatedGeneration + 1
	descriptor := DeliveryGeneration{
		SessionID:               l.sessionID,
		SessionEpoch:            l.sessionEpoch,
		RequestID:               requestID,
		Generation:              generation,
		Profile:                 profile,
		Output:                  output,
		ArtifactPaths:           NewPublisherArtifacts(l.root, l.sessionID, generation),
		ExpectedRecordingWidth:  output.Width,
		ExpectedRecordingHeight: output.Height,
	}
	l.lastAllocatedGeneration = generation
	l.entries[generation] = deliveryGenerationEntry{
		descriptor:     descriptor,
		expectedActive: expectedActiveGeneration,
		state:          deliveryGenerationAllocated,
	}
	return descriptor, nil
}

func (l *DeliveryLedger) Prepare(descriptor DeliveryGeneration) error {
	if l == nil {
		return ErrDeliveryGenerationInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validateDescriptorLocked(descriptor); err != nil {
		return err
	}
	if l.terminalEpoch != 0 {
		return ErrDeliveryLedgerTerminal
	}
	entry := l.entries[descriptor.Generation]
	if entry.state != deliveryGenerationAllocated {
		return ErrDeliveryGenerationNotPrepared
	}
	if l.preparedGeneration != 0 {
		return ErrDeliveryGenerationPending
	}
	entry.state = deliveryGenerationPrepared
	l.entries[descriptor.Generation] = entry
	l.preparedGeneration = descriptor.Generation
	return nil
}

func (l *DeliveryLedger) Commit(expectedActiveGeneration uint64, descriptor DeliveryGeneration) error {
	if l == nil {
		return ErrDeliveryGenerationInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validateDescriptorLocked(descriptor); err != nil {
		return err
	}
	if l.terminalEpoch != 0 {
		return ErrDeliveryLedgerTerminal
	}
	entry := l.entries[descriptor.Generation]
	if entry.state != deliveryGenerationPrepared || l.preparedGeneration != descriptor.Generation {
		return ErrDeliveryGenerationNotPrepared
	}
	if expectedActiveGeneration != entry.expectedActive || expectedActiveGeneration != l.activeGeneration {
		return ErrDeliveryGenerationStale
	}
	entry.state = deliveryGenerationCommitted
	l.entries[descriptor.Generation] = entry
	l.activeGeneration = descriptor.Generation
	l.preparedGeneration = 0
	return nil
}

func (l *DeliveryLedger) Abort(descriptor DeliveryGeneration) error {
	if l == nil {
		return ErrDeliveryGenerationInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validateDescriptorLocked(descriptor); err != nil {
		return err
	}
	if l.terminalEpoch != 0 {
		return ErrDeliveryLedgerTerminal
	}
	entry := l.entries[descriptor.Generation]
	if entry.state != deliveryGenerationAllocated && entry.state != deliveryGenerationPrepared {
		return ErrDeliveryGenerationMismatch
	}
	entry.state = deliveryGenerationAborted
	l.entries[descriptor.Generation] = entry
	if l.preparedGeneration == descriptor.Generation {
		l.preparedGeneration = 0
	}
	return nil
}

func (l *DeliveryLedger) Terminate(sessionID string, sessionEpoch uint64) error {
	if l == nil {
		return ErrDeliveryGenerationInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.validateIdentityLocked(strings.TrimSpace(sessionID), sessionEpoch); err != nil {
		return err
	}
	if l.terminalEpoch != 0 {
		if l.terminalEpoch == sessionEpoch {
			return nil
		}
		return ErrDeliveryLedgerTerminal
	}
	if l.preparedGeneration != 0 {
		entry := l.entries[l.preparedGeneration]
		entry.state = deliveryGenerationAborted
		l.entries[l.preparedGeneration] = entry
		l.preparedGeneration = 0
	}
	for generation, entry := range l.entries {
		if entry.state == deliveryGenerationAllocated {
			entry.state = deliveryGenerationAborted
			l.entries[generation] = entry
		}
	}
	l.terminalEpoch = sessionEpoch
	return nil
}

func (l *DeliveryLedger) Lookup(generation uint64) (DeliveryGeneration, bool) {
	if l == nil || generation == 0 {
		return DeliveryGeneration{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[generation]
	return entry.descriptor, ok
}

func (l *DeliveryLedger) Snapshot() DeliveryLedgerSnapshot {
	if l == nil {
		return DeliveryLedgerSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return DeliveryLedgerSnapshot{
		SessionID:               l.sessionID,
		SessionEpoch:            l.sessionEpoch,
		LastAllocatedGeneration: l.lastAllocatedGeneration,
		PreparedGeneration:      l.preparedGeneration,
		ActiveGeneration:        l.activeGeneration,
		TerminalEpoch:           l.terminalEpoch,
	}
}

func (l *DeliveryLedger) validateIdentityLocked(sessionID string, sessionEpoch uint64) error {
	if sessionID != l.sessionID || sessionEpoch != l.sessionEpoch {
		return ErrDeliveryGenerationMismatch
	}
	return nil
}

func (l *DeliveryLedger) validateDescriptorLocked(descriptor DeliveryGeneration) error {
	if err := l.validateIdentityLocked(descriptor.SessionID, descriptor.SessionEpoch); err != nil {
		return err
	}
	entry, ok := l.entries[descriptor.Generation]
	if !ok || entry.descriptor != descriptor {
		return ErrDeliveryGenerationMismatch
	}
	return nil
}

func (l *DeliveryLedger) hasPendingAllocationLocked() bool {
	for _, entry := range l.entries {
		if entry.state == deliveryGenerationAllocated {
			return true
		}
	}
	return false
}

func validDeliverySessionID(sessionID string) bool {
	if sessionID == "" || len(sessionID) > 128 {
		return false
	}
	for _, character := range sessionID {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func validDeliveryProfile(profile DeliveryProfile) bool {
	return strings.TrimSpace(profile.Mode) != "" && strings.TrimSpace(profile.Transport) != ""
}

func validDeliveryOutput(output DeliveryOutput) bool {
	return output.Width > 0 && output.Height > 0 && output.SourceFPS > 0 && output.OutputFPS > 0 &&
		output.VideoBitrateKbps > 0 && output.MaxRateKbps > 0 && output.BufferSizeKbps > 0 &&
		output.AudioBitrateBps > 0 && output.GOPFrames > 0
}
