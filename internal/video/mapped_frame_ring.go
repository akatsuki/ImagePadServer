package video

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MappedFrameRing is the native-mapping transport implementation. It uses the
// shared ABI header plus fixed-size JSON slots; the control plane remains JSONL
// and the default in-process transport remains available for tests.
type MappedFrameRing struct {
	mapping NativeMapping
	capacity uint16
	slotBytes uint32
}

func NewMappedFrameRing(name string, capacity uint16, slotBytes uint32) (*MappedFrameRing, error) {
	if capacity == 0 || slotBytes < 4 { return nil, errors.New("invalid mapped frame ring size") }
	m, err := OpenNativeMapping(name, GPUSharedRingHeaderSize+int(capacity)*int(slotBytes)); if err != nil { return nil, err }
	r := &MappedFrameRing{mapping: m, capacity: capacity, slotBytes: slotBytes}
	h, err := DecodeSharedRingHeader(m.Bytes()[:GPUSharedRingHeaderSize]); if err != nil {
		h = SharedRingHeader{Magic: GPUSharedRingMagic, Version: 1, Capacity: capacity, SlotBytes: slotBytes}
		if err = h.Encode(m.Bytes()[:GPUSharedRingHeaderSize]); err != nil { m.Close(); return nil, err }
	}
	if h.Capacity != capacity || h.SlotBytes != slotBytes { m.Close(); return nil, errors.New("mapped frame ring ABI mismatch") }
	return r, nil
}

func (r *MappedFrameRing) Submit(ctx context.Context, frame GpuFrame) error {
	if err := frame.Validate(); err != nil { return err }
	b, err := json.Marshal(frame); if err != nil { return err }
	if len(b)+4 > int(r.slotBytes) { return errors.New("gpu frame exceeds mapped slot") }
	for {
		h, err := DecodeSharedRingHeader(r.mapping.Bytes()[:GPUSharedRingHeaderSize]); if err != nil { return err }
		if h.Closed != 0 { return ErrGPUUnavailable }
		if h.WriteSeq-h.ReadSeq < uint64(h.Capacity) {
			i := GPUSharedRingHeaderSize + int(h.WriteSeq%uint64(h.Capacity))*int(h.SlotBytes); slot := r.mapping.Bytes()[i:i+int(h.SlotBytes)]
			binary.LittleEndian.PutUint32(slot[:4], uint32(len(b))); copy(slot[4:], b); h.WriteSeq++
			return h.Encode(r.mapping.Bytes()[:GPUSharedRingHeaderSize])
		}
		select { case <-ctx.Done(): return ctx.Err(); case <-time.After(time.Millisecond): }
	}
}

func (r *MappedFrameRing) Receive(ctx context.Context) (GpuFrame, error) {
	for {
		h, err := DecodeSharedRingHeader(r.mapping.Bytes()[:GPUSharedRingHeaderSize]); if err != nil { return GpuFrame{}, err }
		if h.ReadSeq < h.WriteSeq {
			i := GPUSharedRingHeaderSize + int(h.ReadSeq%uint64(h.Capacity))*int(h.SlotBytes); slot := r.mapping.Bytes()[i:i+int(h.SlotBytes)]
			n := int(binary.LittleEndian.Uint32(slot[:4])); if n < 1 || n+4 > len(slot) { return GpuFrame{}, fmt.Errorf("invalid mapped frame length %d", n) }
			var frame GpuFrame; if err := json.Unmarshal(slot[4:4+n], &frame); err != nil { return GpuFrame{}, err }; h.ReadSeq++
			if err := h.Encode(r.mapping.Bytes()[:GPUSharedRingHeaderSize]); err != nil { return GpuFrame{}, err }; return frame, nil
		}
		if h.Closed != 0 { return GpuFrame{}, ErrGPUUnavailable }
		select { case <-ctx.Done(): return GpuFrame{}, ctx.Err(); case <-time.After(time.Millisecond): }
	}
}

func (r *MappedFrameRing) Close(cause error) { h, err := DecodeSharedRingHeader(r.mapping.Bytes()[:GPUSharedRingHeaderSize]); if err == nil { h.Closed = 1; _ = h.Encode(r.mapping.Bytes()[:GPUSharedRingHeaderSize]) }; _ = r.mapping.Close() }
