package nicorender

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestFrameTransportSplitHeader(t *testing.T) {
	transport, err := newFrameTransport(320, 180)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.close()
	client, err := websocket.Dial(transport.config.Endpoint, "", "file://localhost")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { transport.close(); client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := transport.waitConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pixels := bytes.Repeat([]byte{11, 22, 33, 128}, 320*180)
	want := frameHeader{Kind: frameKindFull, Sequence: 4, TimeMs: 133, Width: 320, Height: 180}
	packet, err := encodeFramePacket(want, pixels)
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		for _, part := range [][]byte{packet[:7], packet[7:100003], packet[100003:]} {
			if err := websocket.Message.Send(client, part); err != nil {
				sent <- err
				return
			}
		}
		sent <- nil
	}()
	got, err := transport.receivePacket(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	h, decoded, err := decodeFramePacket(got, want)
	if err != nil || h != want || !bytes.Equal(decoded, pixels) {
		t.Fatalf("frame changed: header=%+v err=%v", h, err)
	}
}

func TestFrameTransportCanceledRead(t *testing.T) {
	transport, err := newFrameTransport(16, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.close()
	client, err := websocket.Dial(transport.config.Endpoint, "", "file://localhost")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { transport.close(); client.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := transport.waitConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := transport.receivePacket(ctx, conn); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not unblock read")
	}
}

func TestFrameTransportRejectsOversizedPayload(t *testing.T) {
	transport, err := newFrameTransport(16, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.close()
	client, err := websocket.Dial(transport.config.Endpoint, "", "file://localhost")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { transport.close(); client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := transport.waitConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := encodeFramePacket(frameHeader{Kind: frameKindFull, Width: 16, Height: 16}, make([]byte, 1024))
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(packet[24:28], 1<<30)
	if err := websocket.Message.Send(client, packet[:frameHeaderSize]); err != nil {
		t.Fatal(err)
	}
	_, err = transport.receivePacket(ctx, conn)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("oversized payload was not rejected immediately: %v", err)
	}
}
