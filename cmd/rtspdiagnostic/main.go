// Command rtspdiagnostic analyzes a length-prefixed RTP fixture or capture.
// It never opens a network connection; live capture orchestration is explicit.
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"imagepadserver/internal/rtspdiagnostic"
)

func main() {
	inPath := flag.String("input", "", "length-prefixed RTP input; empty means stdin")
	out := flag.String("out", ".", "capture root directory")
	maxBytes := flag.Int64("max-bytes", 64<<20, "input byte limit")
	maxDuration := flag.Duration("max-duration", 30*time.Second, "analysis duration limit")
	run := flag.String("run-id", "", "run identifier")
	source := flag.String("source-kind", "fixture", "source kind")
	tcpReads := flag.Uint64("tcp-reads", 0, "explicit TCP read count represented by this fixture")
	framing := flag.String("framing", "length32be", "input packet framing (length32be)")
	var negotiationEntries []string
	flag.Func("negotiation", "negotiated metadata key=value (repeatable)", func(value string) error { negotiationEntries = append(negotiationEntries, value); return nil })
	flag.Parse()
	dir, meta, err := rtspdiagnostic.NewCaptureDir(*out, *run, *source)
	if err != nil {
		fail(err)
	}
	negotiation, err := rtspdiagnostic.ParseNegotiation(negotiationEntries)
	if err != nil {
		fail(err)
	}
	meta.Negotiation = negotiation
	meta.TCPReads = *tcpReads
	meta.TCPFraming = *framing
	if meta.TCPFraming != "length32be" {
		meta.ExitCode = 2
		failWrite(dir, meta, fmt.Errorf("unsupported framing: %s", meta.TCPFraming))
	}
	raw, err := os.OpenFile(dir+string(os.PathSeparator)+"raw.rtpbin", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fail(err)
	}
	defer raw.Close()
	var r io.Reader = os.Stdin
	var f *os.File
	if *inPath != "" {
		f, err = os.Open(*inPath)
		if err != nil {
			fail(err)
		}
		defer f.Close()
		r = f
	}
	start := time.Now()
	a := rtspdiagnostic.NewAssembler()
	br := bufio.NewReader(r)
	var bytes int64
	for {
		if *maxDuration > 0 && time.Since(start) >= *maxDuration {
			break
		}
		var n uint32
		if err := binary.Read(br, binary.BigEndian, &n); err != nil {
			if err == io.EOF {
				break
			}
			meta.ExitCode = 2
			failWrite(dir, meta, err)
		}
		if n == 0 || int64(n) > *maxBytes-bytes {
			meta.DropCount++
			meta.ExitCode = 3
			break
		}
		packet := make([]byte, n)
		if _, err := io.ReadFull(br, packet); err != nil {
			meta.MissingCount++
			meta.ExitCode = 4
			break
		}
		bytes += int64(n)
		meta.RTPBytes += uint64(n)
		meta.RTPPackets++
		if err := binary.Write(raw, binary.BigEndian, n); err != nil {
			meta.ExitCode = 5
			failWrite(dir, meta, err)
		}
		if _, err := raw.Write(packet); err != nil {
			meta.ExitCode = 5
			failWrite(dir, meta, err)
		}
		p, err := rtspdiagnostic.ParseRTP(packet)
		if err != nil {
			meta.DropCount++
			continue
		}
		for range a.Push(p) {
			meta.AUCount++
			meta.FrameCount++
		}
	}
	_ = a.Flush()
	if meta.ExitCode == 0 {
		meta.ExitCode = 0
	}
	if err := meta.Write(dir); err != nil {
		fail(err)
	}
	fmt.Println(dir)
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(2) }
func failWrite(dir string, meta *rtspdiagnostic.CaptureMetadata, err error) {
	_ = meta.Write(dir)
	fail(err)
}
