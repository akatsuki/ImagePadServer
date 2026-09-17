package nicorender

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	frameHeaderSize           = 40
	frameProtocolVersion      = 1
	frameFormatRGBA8          = 1
	frameKindFull        byte = 1
	frameKindRepeat      byte = 2
	frameKindSparseRuns  byte = 3
)

var frameMagic = [4]byte{'N', 'C', 'R', '1'}

// frameHeader is the fixed metadata exchanged by the binary render transport.
// Width and height describe the reconstructed tightly packed RGBA8 image.
type frameHeader struct {
	Kind          byte
	Sequence      uint64
	TimeMs        uint64
	Width, Height uint32
}

func encodeFramePacket(h frameHeader, pixels []byte) ([]byte, error) {
	if h.Kind == frameKindSparseRuns {
		return nil, errors.New("niconico: compressed frames are encoded by the browser")
	}
	if err := validateFrameHeader(h, false); err != nil {
		return nil, err
	}
	want, err := framePayloadSize(h)
	if err != nil {
		return nil, err
	}
	if h.Kind == frameKindRepeat {
		if len(pixels) != 0 {
			return nil, errors.New("niconico: repeat frame must not contain pixels")
		}
	} else if len(pixels) != want {
		return nil, fmt.Errorf("niconico: frame payload %d bytes, want %d", len(pixels), want)
	}
	packet := make([]byte, frameHeaderSize+len(pixels))
	copy(packet[:4], frameMagic[:])
	packet[4] = frameProtocolVersion
	packet[5] = h.Kind
	packet[6] = frameFormatRGBA8
	// packet[7] is reserved flags and remains zero.
	binary.LittleEndian.PutUint64(packet[8:16], h.Sequence)
	binary.LittleEndian.PutUint64(packet[16:24], h.TimeMs)
	binary.LittleEndian.PutUint32(packet[24:28], uint32(len(pixels)))
	binary.LittleEndian.PutUint32(packet[28:32], h.Width)
	binary.LittleEndian.PutUint32(packet[32:36], h.Height)
	copy(packet[frameHeaderSize:], pixels)
	return packet, nil
}

// decodeFramePacket validates the wire header against want and returns the
// payload owned by packet. The caller must not modify or reuse the packet
// while a sink or lastFull retains it. Sparse frames own a new reconstructed
// buffer. A want.Kind of zero accepts any supported kind.
func decodeFramePacket(packet []byte, want frameHeader) (frameHeader, []byte, error) {
	if len(packet) < frameHeaderSize {
		return frameHeader{}, nil, errors.New("niconico: frame packet is shorter than header")
	}
	if string(packet[:4]) != string(frameMagic[:]) {
		return frameHeader{}, nil, errors.New("niconico: invalid frame packet magic")
	}
	if packet[4] != frameProtocolVersion || packet[6] != frameFormatRGBA8 || packet[7] != 0 {
		return frameHeader{}, nil, errors.New("niconico: unsupported frame packet format")
	}
	h := frameHeader{
		Kind:     packet[5],
		Sequence: binary.LittleEndian.Uint64(packet[8:16]),
		TimeMs:   binary.LittleEndian.Uint64(packet[16:24]),
		Width:    binary.LittleEndian.Uint32(packet[28:32]),
		Height:   binary.LittleEndian.Uint32(packet[32:36]),
	}
	if binary.LittleEndian.Uint32(packet[36:40]) != 0 {
		return frameHeader{}, nil, errors.New("niconico: non-zero frame packet reserved field")
	}
	if err := validateFrameHeader(h, false); err != nil {
		return frameHeader{}, nil, err
	}
	if want.Kind != 0 && h.Kind != want.Kind {
		return frameHeader{}, nil, fmt.Errorf("niconico: frame kind %d, want %d", h.Kind, want.Kind)
	}
	if h.Sequence != want.Sequence || h.TimeMs != want.TimeMs || h.Width != want.Width || h.Height != want.Height {
		return frameHeader{}, nil, fmt.Errorf("niconico: frame metadata mismatch: got seq=%d time=%d size=%dx%d", h.Sequence, h.TimeMs, h.Width, h.Height)
	}
	payloadBytes := int(binary.LittleEndian.Uint32(packet[24:28]))
	if payloadBytes != len(packet)-frameHeaderSize {
		return frameHeader{}, nil, fmt.Errorf("niconico: frame payload header=%d actual=%d", payloadBytes, len(packet)-frameHeaderSize)
	}
	wantBytes, err := framePayloadSize(h)
	if err != nil {
		return frameHeader{}, nil, err
	}
	if h.Kind == frameKindRepeat && payloadBytes != 0 {
		return frameHeader{}, nil, errors.New("niconico: repeat frame has a payload")
	}
	if h.Kind == frameKindFull && payloadBytes != wantBytes {
		return frameHeader{}, nil, fmt.Errorf("niconico: full frame payload %d bytes, want %d", payloadBytes, wantBytes)
	}
	if h.Kind == frameKindSparseRuns {
		pixels, err := decodeSparseRuns(packet[frameHeaderSize:], wantBytes)
		return h, pixels, err
	}
	return h, packet[frameHeaderSize:], nil
}

// Every span has two little-endian uint32 values (absolute pixel offset,
// literal pixel count), followed by literal RGBA. Omitted pixels are all-zero bytes,
// not merely transparent alpha. Each frame owns its reconstructed buffer.
func decodeSparseRuns(payload []byte, size int) ([]byte, error) {
	if len(payload) >= size {
		return nil, errors.New("niconico: invalid sparse frame size")
	}
	pixels := make([]byte, size)
	var previousEnd uint64
	for len(payload) > 0 {
		if len(payload) < 8 {
			return nil, errors.New("niconico: truncated sparse span")
		}
		start, count := uint64(binary.LittleEndian.Uint32(payload[:4])), uint64(binary.LittleEndian.Uint32(payload[4:8]))
		payload = payload[8:]
		if start < previousEnd || count == 0 || start+count > uint64(size/4) || count*4 > uint64(len(payload)) {
			return nil, errors.New("niconico: invalid sparse span bounds")
		}
		n := int(count) * 4
		offset := int(start) * 4
		copy(pixels[offset:offset+n], payload[:n])
		payload = payload[n:]
		previousEnd = start + count
	}
	return pixels, nil
}

func validateFrameHeader(h frameHeader, allowWildcardKind bool) error {
	if h.Kind != frameKindFull && h.Kind != frameKindRepeat && h.Kind != frameKindSparseRuns && !(allowWildcardKind && h.Kind == 0) {
		return fmt.Errorf("niconico: invalid frame kind %d", h.Kind)
	}
	if h.Width == 0 || h.Height == 0 || h.Width > 3840 || h.Height > 2160 {
		return fmt.Errorf("niconico: invalid frame dimensions %dx%d", h.Width, h.Height)
	}
	return nil
}

func framePayloadSize(h frameHeader) (int, error) {
	if err := validateFrameHeader(h, false); err != nil {
		return 0, err
	}
	size := uint64(h.Width) * uint64(h.Height) * 4
	if size > uint64(^uint(0)>>1) {
		return 0, errors.New("niconico: frame payload is too large")
	}
	return int(size), nil
}
