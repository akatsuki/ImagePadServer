package video

import (
	"encoding/binary"
	"errors"
)

const (
	GPUSharedRingMagic uint32 = 0x47505531 // GPU1
	GPUSharedRingHeaderSize = 64
)

var ErrInvalidSharedRingHeader = errors.New("invalid gpu shared ring header")

// SharedRingHeader is the cross-process ABI header. All integer fields are
// little-endian and the sequence counters are 8-byte aligned for atomic
// access by Go, Rust, and native mapping implementations.
type SharedRingHeader struct {
	Magic uint32
	Version uint16
	Capacity uint16
	SlotBytes uint32
	WriteSeq uint64
	ReadSeq uint64
	Closed uint32
}

func (h SharedRingHeader) Encode(dst []byte) error {
	if len(dst) < GPUSharedRingHeaderSize || h.Magic != GPUSharedRingMagic || h.Version == 0 || h.Capacity == 0 || h.SlotBytes == 0 { return ErrInvalidSharedRingHeader }
	clear := dst[:GPUSharedRingHeaderSize]; for i := range clear { clear[i] = 0 }
	binary.LittleEndian.PutUint32(clear[0:4], h.Magic)
	binary.LittleEndian.PutUint16(clear[4:6], h.Version)
	binary.LittleEndian.PutUint16(clear[6:8], h.Capacity)
	binary.LittleEndian.PutUint32(clear[8:12], h.SlotBytes)
	binary.LittleEndian.PutUint64(clear[16:24], h.WriteSeq)
	binary.LittleEndian.PutUint64(clear[24:32], h.ReadSeq)
	binary.LittleEndian.PutUint32(clear[32:36], h.Closed)
	return nil
}

func DecodeSharedRingHeader(src []byte) (SharedRingHeader, error) {
	if len(src) < GPUSharedRingHeaderSize { return SharedRingHeader{}, ErrInvalidSharedRingHeader }
	h := SharedRingHeader{Magic: binary.LittleEndian.Uint32(src[0:4]), Version: binary.LittleEndian.Uint16(src[4:6]), Capacity: binary.LittleEndian.Uint16(src[6:8]), SlotBytes: binary.LittleEndian.Uint32(src[8:12]), WriteSeq: binary.LittleEndian.Uint64(src[16:24]), ReadSeq: binary.LittleEndian.Uint64(src[24:32]), Closed: binary.LittleEndian.Uint32(src[32:36])}
	if h.Magic != GPUSharedRingMagic || h.Version == 0 || h.Capacity == 0 || h.SlotBytes == 0 || h.ReadSeq > h.WriteSeq || h.WriteSeq-h.ReadSeq > uint64(h.Capacity) { return SharedRingHeader{}, ErrInvalidSharedRingHeader }
	return h, nil
}
