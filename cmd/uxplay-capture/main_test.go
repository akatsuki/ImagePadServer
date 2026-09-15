package main

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestWriteCaptureRecordPreservesDatagramAndArrivalTime(t *testing.T) {
	var got bytes.Buffer
	wantTime := time.Unix(123, 456)
	wantPayload := []byte{0x80, 0xe0, 0x12, 0x34, 0xaa, 0xbb}

	if err := writeCaptureHeader(&got); err != nil {
		t.Fatal(err)
	}
	if err := writeCaptureRecord(&got, wantTime, wantPayload); err != nil {
		t.Fatal(err)
	}
	data := append([]byte(nil), got.Bytes()...)

	header := make([]byte, len(captureMagic))
	if _, err := got.Read(header); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(header, captureMagic) {
		t.Fatalf("capture magic = %q; want %q", header, captureMagic)
	}
	var recordHeader [captureRecordHeaderSize]byte
	if _, err := got.Read(recordHeader[:]); err != nil {
		t.Fatal(err)
	}
	if timestamp := int64(binary.LittleEndian.Uint64(recordHeader[0:8])); timestamp != wantTime.UnixNano() {
		t.Fatalf("arrival timestamp = %d; want %d", timestamp, wantTime.UnixNano())
	}
	if length := binary.LittleEndian.Uint32(recordHeader[8:12]); length != uint32(len(wantPayload)) {
		t.Fatalf("payload length = %d; want %d", length, len(wantPayload))
	}
	if !bytes.Equal(data[len(captureMagic)+captureRecordHeaderSize:], wantPayload) {
		t.Fatalf("payload was not preserved: %x", data)
	}
}
