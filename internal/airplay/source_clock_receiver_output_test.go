package airplay

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSourceClockReceiverOutputFansOutRawBytesAndAcceptsSplitMetrics(t *testing.T) {
	log := &limitedBuffer{max: 8192}
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(123, 456)
	diagnosticPath := filepath.Join(t.TempDir(), "receiver.log")
	output, err := newSourceClockReceiverOutput(log, diagnosticPath, gate, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}

	first := "ordinary UxPlay log\n" + canonicalSourceClockMetricsLine[:37]
	second := canonicalSourceClockMetricsLine[37:] + "\n"
	if n, err := output.Write([]byte(first)); err != nil || n != len(first) {
		t.Fatalf("first Write = (%d, %v), want (%d, nil)", n, err, len(first))
	}
	if n, err := output.Write([]byte(second)); err != nil || n != len(second) {
		t.Fatalf("second Write = (%d, %v), want (%d, nil)", n, err, len(second))
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}

	wantRaw := []byte(first + second)
	if got := limitedBufferBytes(log); !bytes.Equal(got, wantRaw) {
		t.Fatalf("limited log = %q, want %q", got, wantRaw)
	}
	gotFile, err := os.ReadFile(diagnosticPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotFile, wantRaw) {
		t.Fatalf("diagnostic file = %q, want %q", gotFile, wantRaw)
	}
	stats := gate.snapshot()
	if !stats.HasMetrics || stats.Latest.Sequence != 7 || !stats.LastAcceptedAt.Equal(base) {
		t.Fatalf("metrics stats = %#v, want current metrics at injected time", stats)
	}
}

func TestSourceClockReceiverOutputGateOnlyAcceptsCurrentFreshMetrics(t *testing.T) {
	log := &limitedBuffer{max: 8192}
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(200, 0)
	output, err := newSourceClockReceiverOutput(log, "", gate, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}

	lines := []string{
		canonicalSourceClockMetricsLine,
		strings.Replace(canonicalSourceClockMetricsLine, `"receiverId":"receiver-1"`, `"receiverId":"receiver-2"`, 1),
		canonicalSourceClockMetricsLine,
		"IMAGEPAD_METRICS_V1 {",
		"ordinary log after metrics",
	}
	for _, line := range lines {
		if _, err := output.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}

	stats := gate.snapshot()
	if !stats.HasMetrics || stats.Latest.Sequence != 7 || !stats.LastAcceptedAt.Equal(base) {
		t.Fatalf("gate accepted unexpected activity: %#v", stats)
	}
	if stats.ReceiverMismatches != 1 || stats.StaleSequences != 1 || stats.ParseFailures != 1 {
		t.Fatalf("gate rejection stats = %#v, want mismatch=1 stale=1 parse=1", stats)
	}
}

func TestSourceClockReceiverOutputRejectsOverlongLinesWithoutFakeMetrics(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	output, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, func() time.Time {
		return time.Unix(300, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write(append(bytes.Repeat([]byte{'x'}, sourceClockLiveLineMaxBytes+1), '\n')); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}

	stats := output.snapshot()
	if stats.FramerRejects != 1 {
		t.Fatalf("framer rejects = %d, want 1", stats.FramerRejects)
	}
	gateStats := gate.snapshot()
	if gateStats.HasMetrics || !gateStats.LastAcceptedAt.IsZero() {
		t.Fatalf("reject became metrics activity: %#v", gateStats)
	}
}

func TestSourceClockReceiverOutputCountsQueueDropBurstSeparately(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	gate.mu.Lock()
	output, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, func() time.Time {
		return time.Unix(350, 0)
	})
	if err != nil {
		gate.mu.Unlock()
		t.Fatal(err)
	}
	burst := bytes.Repeat([]byte("ordinary log\n"), sourceClockLiveLineQueueCapacity*2)
	if n, err := output.Write(burst); err != nil || n != len(burst) {
		gate.mu.Unlock()
		t.Fatalf("burst Write = (%d, %v), want (%d, nil)", n, err, len(burst))
	}
	gate.mu.Unlock()
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}

	if stats := output.snapshot(); stats.FramerRejects != 1 {
		t.Fatalf("queue-drop reject bursts = %d, want 1", stats.FramerRejects)
	}
	if stats := gate.snapshot(); stats.HasMetrics || !stats.LastAcceptedAt.IsZero() {
		t.Fatalf("ordinary log/drop changed metrics activity: %#v", stats)
	}
}

func TestSourceClockReceiverOutputConcurrentWritesPreserveFanout(t *testing.T) {
	const workers = 64
	log := &limitedBuffer{max: workers * 64}
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	diagnosticPath := filepath.Join(t.TempDir(), "receiver.log")
	output, err := newSourceClockReceiverOutput(log, diagnosticPath, gate, nil)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			line := fmt.Sprintf("ordinary-%02d\n", i)
			if n, err := output.Write([]byte(line)); err != nil || n != len(line) {
				t.Errorf("Write(%q) = (%d, %v)", line, n, err)
			}
		}(i)
	}
	wg.Wait()
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}

	gotLog := limitedBufferBytes(log)
	gotFile, err := os.ReadFile(diagnosticPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotLog, gotFile) {
		t.Fatalf("fanout bytes differ: log=%q file=%q", gotLog, gotFile)
	}
	if len(gotLog) != workers*len("ordinary-00\n") {
		t.Fatalf("limited log length = %d, want %d", len(gotLog), workers*len("ordinary-00\n"))
	}
}

func TestSourceClockReceiverOutputSupportsBlankPathAndNilClock(t *testing.T) {
	log := &limitedBuffer{max: 8192}
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	output, err := newSourceClockReceiverOutput(log, "  \t", gate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := output.Write([]byte("ordinary\n")); err != nil || n != len("ordinary\n") {
		t.Fatalf("Write = (%d, %v), want all bytes and nil", n, err)
	}
	if _, err := output.Write([]byte(canonicalSourceClockMetricsLine + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if !gate.snapshot().HasMetrics {
		t.Fatal("nil clock did not use a production clock")
	}
}

func TestSourceClockReceiverOutputRejectsNilInputsBeforeOpeningFile(t *testing.T) {
	validLog := &limitedBuffer{max: 8192}
	validGate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}

	oldOpen := openSourceClockReceiverOutputFile
	t.Cleanup(func() { openSourceClockReceiverOutputFile = oldOpen })
	opened := false
	openSourceClockReceiverOutputFile = func(string) (sourceClockReceiverOutputFile, error) {
		opened = true
		return &sourceClockReceiverOutputTestFile{}, nil
	}
	for _, test := range []struct {
		name string
		log  *limitedBuffer
		gate *sourceClockMetricsGate
	}{
		{name: "nil log", log: nil, gate: validGate},
		{name: "nil gate", log: validLog, gate: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			opened = false
			if output, err := newSourceClockReceiverOutput(test.log, "must-not-open", test.gate, nil); err == nil || output != nil {
				t.Fatalf("constructor accepted nil input: output=%v err=%v", output, err)
			}
			if opened {
				t.Fatal("file opener was called before nil input validation")
			}
		})
	}
}

func TestSourceClockReceiverOutputReportsOpenWriteAndCloseErrors(t *testing.T) {
	log := &limitedBuffer{max: 8192}
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newSourceClockReceiverOutput(log, filepath.Join(t.TempDir(), "missing", "receiver.log"), gate, nil); err == nil {
		t.Fatal("constructor accepted an unopenable diagnostic path")
	}

	oldOpen := openSourceClockReceiverOutputFile
	t.Cleanup(func() { openSourceClockReceiverOutputFile = oldOpen })
	openSourceClockReceiverOutputFile = func(string) (sourceClockReceiverOutputFile, error) {
		return &sourceClockReceiverOutputTestFile{writeErr: errors.New("write failed"), closeErr: errors.New("close failed")}, nil
	}
	output, err := newSourceClockReceiverOutput(log, "injected", gate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("raw")); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("Write error = %v, want write failure", err)
	}
	if err := output.Close(); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("Close error = %v, want close failure", err)
	}
}

func TestSourceClockReceiverOutputCloseWaitsForMetricsCallbackAndRejectsLaterWrites(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	gate.mu.Lock()
	output, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, func() time.Time { return time.Unix(400, 0) })
	if err != nil {
		gate.mu.Unlock()
		t.Fatal(err)
	}
	if _, err := output.Write([]byte(canonicalSourceClockMetricsLine + "\n")); err != nil {
		gate.mu.Unlock()
		t.Fatal(err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- output.Close() }()
	select {
	case err := <-closeDone:
		gate.mu.Unlock()
		t.Fatalf("Close returned before callback completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	gate.mu.Unlock()
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if n, err := output.Write([]byte("after close")); n != 0 || err == nil {
		t.Fatalf("Write after Close = (%d, %v), want (0, error)", n, err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	stats := gate.snapshot()
	if !stats.HasMetrics || !stats.LastAcceptedAt.Equal(time.Unix(400, 0)) {
		t.Fatalf("callback did not complete before Close returned: %#v", stats)
	}
}

func limitedBufferBytes(buffer *limitedBuffer) []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.data...)
}

type sourceClockReceiverOutputTestFile struct {
	writeErr error
	closeErr error
}

func (f *sourceClockReceiverOutputTestFile) Write(p []byte) (int, error) {
	return 0, f.writeErr
}

func (f *sourceClockReceiverOutputTestFile) Close() error { return f.closeErr }
