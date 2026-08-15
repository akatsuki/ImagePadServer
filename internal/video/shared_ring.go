package video

import (
	"encoding/binary"
	"errors"
	"sync/atomic"
	"unsafe"
)

// Cross-process single-producer/single-consumer ring shared with the
// playlist-compositord sidecar through a file-backed memory mapping. The
// layout mirrors gpu/playlist-compositord/src/shared_ring.rs:
//
//	[64-byte header][slot 0][slot 1]... where each slot is
//	[4-byte little-endian length][payload].
//
// Producers and consumers coordinate through write_seq/read_seq in the header,
// so no locks are required.
const (
	gpuSharedRingMagic      = uint32(0x4750_5531)
	gpuSharedRingHeaderSize = 64
	gpuRingWriteSeqOffset   = 16
	gpuRingReadSeqOffset    = 24
)

type sharedRing struct {
	bytes     []byte
	release   func() error
	capacity  int
	slotBytes int
}

func createSharedRing(path string, capacity, slotBytes int) (*sharedRing, error) {
	if capacity <= 0 || capacity > 65535 || slotBytes < 4 {
		return nil, errors.New("invalid shared ring geometry")
	}
	total := gpuSharedRingHeaderSize + capacity*slotBytes
	b, release, err := openFileBackedMapping(path, total)
	if err != nil {
		return nil, err
	}
	r := &sharedRing{bytes: b, release: release, capacity: capacity, slotBytes: slotBytes}
	binary.LittleEndian.PutUint32(b[0:4], gpuSharedRingMagic)
	binary.LittleEndian.PutUint16(b[4:6], 1) // version
	binary.LittleEndian.PutUint16(b[6:8], uint16(capacity))
	binary.LittleEndian.PutUint32(b[8:12], uint32(slotBytes))
	binary.LittleEndian.PutUint64(b[16:24], 0) // write_seq
	binary.LittleEndian.PutUint64(b[24:32], 0) // read_seq
	binary.LittleEndian.PutUint32(b[32:36], 0) // closed
	return r, nil
}

func openSharedRing(path string, capacity, slotBytes int) (*sharedRing, error) {
	total := gpuSharedRingHeaderSize + capacity*slotBytes
	b, release, err := openFileBackedMapping(path, total)
	if err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(b[0:4]) != gpuSharedRingMagic {
		_ = release()
		return nil, errors.New("shared ring magic mismatch")
	}
	if int(binary.LittleEndian.Uint16(b[6:8])) != capacity ||
		int(binary.LittleEndian.Uint32(b[8:12])) != slotBytes {
		_ = release()
		return nil, errors.New("shared ring geometry mismatch")
	}
	return &sharedRing{bytes: b, release: release, capacity: capacity, slotBytes: slotBytes}, nil
}

func (r *sharedRing) writeSeq() uint64 {
	return atomic.LoadUint64((*uint64)(unsafe.Pointer(&r.bytes[gpuRingWriteSeqOffset])))
}

func (r *sharedRing) readSeq() uint64 {
	return atomic.LoadUint64((*uint64)(unsafe.Pointer(&r.bytes[gpuRingReadSeqOffset])))
}

func (r *sharedRing) setWriteSeq(v uint64) {
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&r.bytes[gpuRingWriteSeqOffset])), v)
}

func (r *sharedRing) setReadSeq(v uint64) {
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&r.bytes[gpuRingReadSeqOffset])), v)
}

func (r *sharedRing) closed() bool {
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(&r.bytes[32]))) != 0
}

func (r *sharedRing) slotOffset(seq uint64) int {
	return gpuSharedRingHeaderSize + int(seq%uint64(r.capacity))*r.slotBytes
}

// tryPush writes one message. Returns false when the ring is full or the
// message exceeds slotBytes-4.
func (r *sharedRing) tryPush(data []byte) bool {
	if len(data)+4 > r.slotBytes {
		return false
	}
	write := r.writeSeq()
	read := r.readSeq()
	if write-read >= uint64(r.capacity) {
		return false
	}
	off := r.slotOffset(write)
	binary.LittleEndian.PutUint32(r.bytes[off:off+4], uint32(len(data)))
	copy(r.bytes[off+4:off+4+len(data)], data)
	r.setWriteSeq(write + 1) // atomic store acts as the release barrier
	return true
}

// tryPop reads one message. Returns nil when the ring is empty.
func (r *sharedRing) tryPop() []byte {
	write := r.writeSeq() // atomic load acts as the acquire barrier
	read := r.readSeq()
	if read >= write {
		return nil
	}
	off := r.slotOffset(read)
	length := int(binary.LittleEndian.Uint32(r.bytes[off : off+4]))
	data := make([]byte, length)
	copy(data, r.bytes[off+4:off+4+length])
	r.setReadSeq(read + 1)
	return data
}

func (r *sharedRing) close() error {
	if r.release == nil {
		return nil
	}
	err := r.release()
	r.release = nil
	r.bytes = nil
	return err
}
