package video

import (
	"path/filepath"
	"testing"
)

func TestSharedRingPushPopAndReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ring.map")

	producer, err := createSharedRing(path, 4, 128)
	if err != nil {
		t.Fatalf("createSharedRing: %v", err)
	}
	if !producer.tryPush([]byte("hello")) {
		t.Fatal("first push rejected")
	}
	if !producer.tryPush(make([]byte, 124)) {
		t.Fatal("max-slot push rejected")
	}
	if producer.tryPush(make([]byte, 125)) {
		t.Fatal("over-slot push accepted")
	}

	// Reopen from a "second process" and drain in order.
	consumer, err := openSharedRing(path, 4, 128)
	if err != nil {
		t.Fatalf("openSharedRing: %v", err)
	}
	if got := string(consumer.tryPop()); got != "hello" {
		t.Fatalf("first pop = %q, want hello", got)
	}
	if got := consumer.tryPop(); len(got) != 124 {
		t.Fatalf("second pop len = %d, want 124", len(got))
	}
	if got := consumer.tryPop(); got != nil {
		t.Fatalf("empty pop = %v, want nil", got)
	}
	_ = consumer.close()
	_ = producer.close()
}

func TestSharedRingFullRejectsUntilPopped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ring-full.map")

	producer, err := createSharedRing(path, 1, 32)
	if err != nil {
		t.Fatalf("createSharedRing: %v", err)
	}
	if !producer.tryPush([]byte("one")) {
		t.Fatal("first push rejected")
	}
	if producer.tryPush([]byte("two")) {
		t.Fatal("full ring accepted second push")
	}
	consumer, err := openSharedRing(path, 1, 32)
	if err != nil {
		t.Fatalf("openSharedRing: %v", err)
	}
	if got := string(consumer.tryPop()); got != "one" {
		t.Fatalf("pop = %q, want one", got)
	}
	if !producer.tryPush([]byte("three")) {
		t.Fatal("push after pop rejected")
	}
	if got := string(consumer.tryPop()); got != "three" {
		t.Fatalf("pop = %q, want three", got)
	}
	_ = consumer.close()
	_ = producer.close()
}

func TestSharedRingGeometryValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ring-bad.map")

	producer, err := createSharedRing(path, 2, 64)
	if err != nil {
		t.Fatalf("createSharedRing: %v", err)
	}
	_ = producer.close()
	if _, err := openSharedRing(path, 3, 64); err == nil {
		t.Fatal("capacity mismatch not rejected")
	}
	if _, err := openSharedRing(path, 2, 32); err == nil {
		t.Fatal("slotBytes mismatch not rejected")
	}
}
