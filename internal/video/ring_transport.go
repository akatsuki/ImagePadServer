package video

import (
	"encoding/binary"
	"fmt"
)

// Binary protocol for the zero-copy shared-ring transport. It mirrors
// gpu/playlist-compositord/src/ring_transport.rs: the JSONL control plane
// carries prepare/transition/flush, while the per-batch bulk path (frame
// indices in, raw H.264 access units out) uses this fixed little-endian
// format so no JSON or base64 round-trip is paid on the hot path.
const (
	ringRenderRequestTag = 0x01
	ringEncodedBatchTag  = 0x02
)

type ringRenderRequest struct {
	batchID         uint64
	epoch           uint64
	timelineVersion uint64
	frameIndices    []uint64
}

type ringEncodedFrame struct {
	sequence           uint64
	ptsNs              int64
	pixelReadbackBytes uint64
	payload            []byte
}

type ringEncodedBatch struct {
	batchID         uint64
	epoch           uint64
	timelineVersion uint64
	frames          []ringEncodedFrame
}

func encodeRingRenderRequest(req *ringRenderRequest) []byte {
	dst := make([]byte, 1+8+8+8+4+len(req.frameIndices)*8)
	dst[0] = ringRenderRequestTag
	binary.LittleEndian.PutUint64(dst[1:9], req.batchID)
	binary.LittleEndian.PutUint64(dst[9:17], req.epoch)
	binary.LittleEndian.PutUint64(dst[17:25], req.timelineVersion)
	binary.LittleEndian.PutUint32(dst[25:29], uint32(len(req.frameIndices)))
	off := 29
	for _, idx := range req.frameIndices {
		binary.LittleEndian.PutUint64(dst[off:off+8], idx)
		off += 8
	}
	return dst
}

func decodeRingEncodedBatch(src []byte) (*ringEncodedBatch, error) {
	if len(src) < 1 || src[0] != ringEncodedBatchTag {
		return nil, fmt.Errorf("encoded batch tag mismatch")
	}
	if len(src) < 1+8+8+8+4 {
		return nil, fmt.Errorf("encoded batch header truncated")
	}
	batch := &ringEncodedBatch{
		batchID:         binary.LittleEndian.Uint64(src[1:9]),
		epoch:           binary.LittleEndian.Uint64(src[9:17]),
		timelineVersion: binary.LittleEndian.Uint64(src[17:25]),
	}
	off := 25
	count := int(binary.LittleEndian.Uint32(src[off : off+4]))
	off += 4
	batch.frames = make([]ringEncodedFrame, 0, count)
	for i := 0; i < count; i++ {
		if len(src) < off+8+8+8+4 {
			return nil, fmt.Errorf("encoded frame %d truncated", i)
		}
		frame := ringEncodedFrame{
			sequence:           binary.LittleEndian.Uint64(src[off : off+8]),
			ptsNs:              int64(binary.LittleEndian.Uint64(src[off+8 : off+16])),
			pixelReadbackBytes: binary.LittleEndian.Uint64(src[off+16 : off+24]),
		}
		off += 24
		n := int(binary.LittleEndian.Uint32(src[off : off+4]))
		off += 4
		if len(src) < off+n {
			return nil, fmt.Errorf("encoded frame %d payload truncated", i)
		}
		frame.payload = append([]byte(nil), src[off:off+n]...)
		off += n
		batch.frames = append(batch.frames, frame)
	}
	return batch, nil
}
