package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"imagepadserver/internal/airplay/sourceclock"
)

func TestCaptureWriterPreservesHeaderAndCanRedactPayload(t *testing.T) {
	dir := t.TempDir()
	header := sourceclock.EncodeHeader(sourceclock.Header{
		StreamKind: sourceclock.StreamVideo, Codec: sourceclock.CodecH264AnnexBAU,
		PayloadBytes: 3, Sequence: 7, RemoteNTPNS: 123,
	})
	path := filepath.Join(dir, "video.ipaf")
	writer, err := newCaptureWriter(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.writeRecord(456, header, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != captureRecordHeaderBytes+len(header) {
		t.Fatalf("capture length=%d want=%d", len(data), captureRecordHeaderBytes+len(header))
	}
	if got := binary.LittleEndian.Uint64(data[:8]); got != 456 {
		t.Fatalf("arrival=%d", got)
	}
	if got := binary.LittleEndian.Uint32(data[8:12]); got != uint32(len(header)) {
		t.Fatalf("record length=%d", got)
	}
	if string(data[12:]) != string(header) {
		t.Fatal("header was not preserved")
	}
}
