package video

import (
	"bytes"
	"errors"
	"testing"
)

func TestMaxMediaSourceBytes(t *testing.T) {
	if MaxMediaSourceBytes != 4294967295 {
		t.Fatalf("MaxMediaSourceBytes = %d, want 4294967295", MaxMediaSourceBytes)
	}
}

func TestCopyWithLimitRejectsLimitPlusOne(t *testing.T) {
	var dst bytes.Buffer
	n, err := copyWithLimit(&dst, bytes.NewReader(make([]byte, 33)), 32)
	if !errors.Is(err, ErrMediaTooLarge) || n != 33 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestCopyWithLimitAcceptsExactLimit(t *testing.T) {
	var dst bytes.Buffer
	src := bytes.NewReader(make([]byte, 32))
	n, err := copyWithLimit(&dst, src, 32)
	if err != nil || n != 32 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestCopyMediaWithLimit(t *testing.T) {
	data := []byte("hello, media limit test!")
	src := bytes.NewReader(data)
	var dst bytes.Buffer
	n, err := CopyMediaWithLimit(&dst, src)
	if err != nil {
		t.Fatalf("CopyMediaWithLimit failed: %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("n=%d, want %d", n, len(data))
	}
	if dst.String() != string(data) {
		t.Fatalf("copied data mismatch: got %q, want %q", dst.String(), string(data))
	}
}

func TestValidateMediaContentLength(t *testing.T) {
	if err := ValidateMediaContentLength(0); err != nil {
		t.Fatalf("ValidateMediaContentLength(0) = %v, want nil", err)
	}
	if err := ValidateMediaContentLength(MaxMediaSourceBytes); err != nil {
		t.Fatalf("ValidateMediaContentLength(MaxMediaSourceBytes) = %v, want nil", err)
	}
	if err := ValidateMediaContentLength(MaxMediaSourceBytes + 1); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("ValidateMediaContentLength(MaxMediaSourceBytes+1) = %v, want ErrMediaTooLarge", err)
	}
}

func TestCopyMediaWithLimitCopiesDataCorrectly(t *testing.T) {
	data := []byte("exact boundary test data here")
	src := bytes.NewReader(data)
	var dst bytes.Buffer
	n, err := CopyMediaWithLimit(&dst, src)
	if err != nil {
		t.Fatalf("CopyMediaWithLimit failed: %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("n=%d, want %d", n, len(data))
	}
	if !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("copied data mismatch")
	}
}

// Ensure the ErrMediaTooLarge message contains the exact limit value.
func TestErrMediaTooLargeMessage(t *testing.T) {
	msg := ErrMediaTooLarge.Error()
	if msg != "media exceeds 4294967295 byte limit" {
		t.Fatalf("ErrMediaTooLarge.Error() = %q", msg)
	}
}
