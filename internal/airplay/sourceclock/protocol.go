package sourceclock

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	ProtocolVersion uint16 = 1
	HeaderSize      uint16 = 40
	MaxVideoPayload uint32 = 64 << 20
	MaxAudioPayload uint32 = 1 << 20
)

type StreamKind uint8

const (
	StreamControl StreamKind = 0
	StreamVideo   StreamKind = 1
	StreamAudio   StreamKind = 2
)

type Codec uint8

const (
	CodecH264AnnexBAU Codec = 1
	CodecH265AnnexBAU Codec = 2
	CodecAACELDRAW    Codec = 16
	CodecALACRAW      Codec = 17
	CodecAACLCADTS    Codec = 18
)

const (
	ControlHello        Codec = 1
	ControlSessionStart Codec = 2
	ControlStreamGap    Codec = 3
	ControlSessionEnd   Codec = 4
	ControlHeartbeat    Codec = 5
	SessionTokenBytes         = 16
)

const (
	FlagKeyframe      uint16 = 1 << 0
	FlagConfig        uint16 = 1 << 1
	FlagDiscontinuity uint16 = 1 << 2
)

var (
	ErrNeedMore        = errors.New("source-clock header needs more bytes")
	ErrBadMagic        = errors.New("source-clock header has bad magic")
	ErrBadVersion      = errors.New("source-clock header has unsupported version")
	ErrBadHeader       = errors.New("source-clock header has invalid size or reserved field")
	ErrBadKind         = errors.New("source-clock header has invalid stream kind")
	ErrPayloadTooLarge = errors.New("source-clock payload is too large")
)

type Header struct {
	Version      uint16
	HeaderBytes  uint16
	StreamKind   StreamKind
	Codec        Codec
	Flags        uint16
	PayloadBytes uint32
	Sequence     uint32
	RemoteNTPNS  uint64
	SampleCount  uint32
	SampleRate   uint32
	Channels     uint16
	Reserved     uint16
}

func EncodeHeader(h Header) []byte {
	if h.Version == 0 {
		h.Version = ProtocolVersion
	}
	if h.HeaderBytes == 0 {
		h.HeaderBytes = HeaderSize
	}
	b := make([]byte, HeaderSize)
	copy(b[0:4], []byte{'I', 'P', 'A', 'F'})
	binary.BigEndian.PutUint16(b[4:6], h.Version)
	binary.BigEndian.PutUint16(b[6:8], h.HeaderBytes)
	b[8] = byte(h.StreamKind)
	b[9] = byte(h.Codec)
	binary.BigEndian.PutUint16(b[10:12], h.Flags)
	binary.BigEndian.PutUint32(b[12:16], h.PayloadBytes)
	binary.BigEndian.PutUint32(b[16:20], h.Sequence)
	binary.BigEndian.PutUint64(b[20:28], h.RemoteNTPNS)
	binary.BigEndian.PutUint32(b[28:32], h.SampleCount)
	binary.BigEndian.PutUint32(b[32:36], h.SampleRate)
	binary.BigEndian.PutUint16(b[36:38], h.Channels)
	binary.BigEndian.PutUint16(b[38:40], h.Reserved)
	return b
}

func DecodeHeader(b []byte) (Header, error) {
	if len(b) < int(HeaderSize) {
		return Header{}, ErrNeedMore
	}
	if string(b[0:4]) != "IPAF" {
		return Header{}, ErrBadMagic
	}
	h := Header{
		Version:      binary.BigEndian.Uint16(b[4:6]),
		HeaderBytes:  binary.BigEndian.Uint16(b[6:8]),
		StreamKind:   StreamKind(b[8]),
		Codec:        Codec(b[9]),
		Flags:        binary.BigEndian.Uint16(b[10:12]),
		PayloadBytes: binary.BigEndian.Uint32(b[12:16]),
		Sequence:     binary.BigEndian.Uint32(b[16:20]),
		RemoteNTPNS:  binary.BigEndian.Uint64(b[20:28]),
		SampleCount:  binary.BigEndian.Uint32(b[28:32]),
		SampleRate:   binary.BigEndian.Uint32(b[32:36]),
		Channels:     binary.BigEndian.Uint16(b[36:38]),
		Reserved:     binary.BigEndian.Uint16(b[38:40]),
	}
	if h.Version != ProtocolVersion {
		return Header{}, ErrBadVersion
	}
	if h.HeaderBytes != HeaderSize || h.Reserved != 0 {
		return Header{}, ErrBadHeader
	}
	if h.StreamKind != StreamControl && h.StreamKind != StreamVideo && h.StreamKind != StreamAudio {
		return Header{}, ErrBadKind
	}
	if h.StreamKind == StreamVideo && h.PayloadBytes > MaxVideoPayload {
		return Header{}, ErrPayloadTooLarge
	}
	if h.StreamKind == StreamAudio && h.PayloadBytes > MaxAudioPayload {
		return Header{}, ErrPayloadTooLarge
	}
	return h, nil
}

func DecodeHeaderForStream(b []byte, expected StreamKind) (Header, error) {
	h, err := DecodeHeader(b)
	if err != nil {
		return Header{}, err
	}
	if h.StreamKind != expected {
		return Header{}, fmt.Errorf("%w: got %d want %d", ErrBadKind, h.StreamKind, expected)
	}
	return h, nil
}

func ReadFrame(r io.Reader, expected StreamKind) (Header, []byte, error) {
	if r == nil {
		return Header{}, nil, errors.New("source-clock frame reader is nil")
	}
	headerBytes := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, headerBytes); err != nil {
		return Header{}, nil, err
	}
	h, err := DecodeHeaderForStream(headerBytes, expected)
	if err != nil {
		return Header{}, nil, err
	}
	payload := make([]byte, int(h.PayloadBytes))
	if _, err := io.ReadFull(r, payload); err != nil {
		return Header{}, nil, err
	}
	return h, payload, nil
}

func ValidateHello(h Header, payload []byte) error {
	if h.StreamKind != StreamControl || h.Codec != ControlHello {
		return fmt.Errorf("invalid HELLO header")
	}
	if len(payload) != SessionTokenBytes {
		return fmt.Errorf("invalid HELLO token length: got %d want %d", len(payload), SessionTokenBytes)
	}
	return nil
}
