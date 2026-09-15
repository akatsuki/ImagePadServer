package airplay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

func appendCandidateEventForTest(t *testing.T, path string, event airplaycontract.Event) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := json.NewEncoder(file).Encode(event)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal(errors.Join(writeErr, closeErr))
	}
}

func TestPreparePlannedSourceClockCandidateRequiresFinalWatermark(t *testing.T) {
	request := plannedDeliveryRequest()
	root := t.TempDir()
	old := airplaycontract.NewPublisherArtifacts(root, request.Artifacts.SessionID, 4)
	request.Artifacts = airplaycontract.NewPublisherArtifacts(root, old.SessionID, 5)
	if err := os.MkdirAll(filepath.Dir(old.EventLog), 0700); err != nil {
		t.Fatal(err)
	}
	current := buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: 48001, AudioListenPort: 48002, SessionToken: strings.Repeat("ab", 16),
		Artifacts: old, PublishURL: "rtsp://127.0.0.1/old", Output: request.Output,
	})
	watermark := sourceClockEventForWatermarkTest("video-watermark-final", old.SessionID, 4, 7, 100)
	appendCandidateEventForTest(t, old.EventLog, watermark)
	args, events, err := preparePlannedSourceClockCandidate(current, old, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || commandArgValue(args, "--proof-source-generation") != "7" || commandArgValue(args, "--proof-video-watermark") != "100" {
		t.Fatalf("proof args/events missing: %v / %+v", args, events)
	}
	if commandArgValue(args, "--video-listen-port") != "48001" || commandArgValue(args, "--audio-listen-port") != "48002" || commandArgValue(args, "--session-token") != strings.Repeat("ab", 16) {
		t.Fatal("candidate changed receiver-owned inputs")
	}
	for name, mutate := range map[string]func(){
		"missing": func() {
			if err := os.Remove(old.EventLog); err != nil {
				t.Fatal(err)
			}
		},
		"duplicate": func() { appendCandidateEventForTest(t, old.EventLog, watermark) },
		"oversized generation": func() {
			e := watermark
			e.SourceSessionGeneration = 1 << 32
			if err := os.Remove(old.EventLog); err != nil {
				t.Fatal(err)
			}
			appendCandidateEventForTest(t, old.EventLog, e)
		},
		"oversized sequence": func() {
			e := watermark
			e.SourceVideoSequence = ptrUint64(1 << 32)
			if err := os.Remove(old.EventLog); err != nil {
				t.Fatal(err)
			}
			appendCandidateEventForTest(t, old.EventLog, e)
		},
		"malformed": func() {
			if err := os.WriteFile(old.EventLog, []byte("{broken\n"), 0600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(old.EventLog, nil, 0600); err != nil {
				t.Fatal(err)
			}
			appendCandidateEventForTest(t, old.EventLog, watermark)
			mutate()
			args, _, err := preparePlannedSourceClockCandidate(current, old, request, nil)
			if err == nil || len(args) != 0 {
				t.Fatalf("invalid watermark allowed candidate: err=%v", err)
			}
		})
	}
	if _, _, err := preparePlannedSourceClockCandidate(current, old, request, errDirectGStreamerProcessExitUnconfirmed); !errors.Is(err, errDirectGStreamerProcessExitUnconfirmed) {
		t.Fatalf("unconfirmed exit=%v", err)
	}
}

func TestWaitSourceClockCandidateProofHonorsTerminalPriority(t *testing.T) {
	const sessionID = "0123456789abcdef"
	old := []airplaycontract.Event{sourceClockEventForWatermarkTest("video-watermark-final", sessionID, 1, 7, 100)}
	events := []airplaycontract.Event{
		sourceClockPublisherReadyForWatermarkTest(sessionID, 2),
		sourceClockEventForWatermarkTest("video-input-idr", sessionID, 2, 7, 101),
		sourceClockEventForWatermarkTest("video-decoded", sessionID, 2, 7, 101),
		sourceClockEventForWatermarkTest("video-encoded-idr", sessionID, 2, 7, 101),
	}
	for _, mode := range []string{"complete", "user stop", "no signal", "exit", "incomplete", "wrong sequence", "deadline during read"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			for i, event := range events {
				if mode == "incomplete" && i == 3 {
					break
				}
				if mode == "wrong sequence" && i == 3 {
					event.SourceVideoSequence = ptrUint64(102)
				}
				appendCandidateEventForTest(t, path, event)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			if mode == "user stop" {
				cancel()
				done <- errors.New("exit status 22")
			}
			if mode == "exit" {
				done <- errors.New("exit status 22")
			}
			checks := 0
			priority := func() error {
				checks++
				if mode == "no signal" || (mode == "deadline during read" && checks > 1) {
					return ErrDeliveryReconfigureUnavailable
				}
				return nil
			}
			proof, err, consumed := waitSourceClockCandidateProof(ctx, path, old, sessionID, 1, 2, done, 60*time.Millisecond, priority)
			if mode == "complete" {
				if err != nil || consumed || proof.Sequence != 101 || proof.WatermarkSequence != 100 || proof.SourceSessionGeneration != 7 {
					t.Fatalf("proof=%+v err=%v consumed=%v", proof, err, consumed)
				}
				return
			}
			if err == nil || proof != (sourceClockPostWatermarkProof{}) {
				t.Fatalf("terminal case yielded proof: %+v err=%v", proof, err)
			}
			if consumed != (mode == "exit") {
				t.Fatalf("done consumed=%v in %s", consumed, mode)
			}
			if mode == "user stop" && !errors.Is(err, context.Canceled) {
				t.Fatalf("stop priority: %v", err)
			}
			if (mode == "no signal" || mode == "deadline during read") && !errors.Is(err, ErrDeliveryReconfigureUnavailable) {
				t.Fatalf("deadline priority: %v", err)
			}
		})
	}
}

func TestPreparePlannedSourceClockCandidateNoSignalExitNeverRestarts(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockExitStatusHelperProcess$")
	cmd.Env = append(os.Environ(), "IMAGEPAD_TEST_SOURCE_CLOCK_EXIT_CODE=20")
	waitErr := cmd.Run()
	if exit := sourceClockProcessExitFromError(waitErr); !exit.Known || exit.Code != 20 {
		t.Fatalf("expected actual process exit20, got %+v / %v", exit, waitErr)
	}
	request := plannedDeliveryRequest()
	old := airplaycontract.NewPublisherArtifacts(t.TempDir(), request.Artifacts.SessionID, 4)
	// No log exists. The terminal exit must win before any file read or build.
	args, events, err := preparePlannedSourceClockCandidate(nil, old, request, waitErr)
	if !errors.Is(err, ErrDeliveryReconfigureUnavailable) || len(args) != 0 || len(events) != 0 {
		t.Fatalf("exit20 prepared a candidate: %v / %v / %v", args, events, err)
	}
}
