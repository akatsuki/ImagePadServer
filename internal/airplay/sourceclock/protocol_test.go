package sourceclock

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

func TestHeaderGoldenVector(t *testing.T) {
	want, err := hex.DecodeString("4950414600010028010100010000000400000007000000003b9aca00000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	h := Header{
		Version:      1,
		StreamKind:   StreamVideo,
		Codec:        CodecH264AnnexBAU,
		Flags:        FlagKeyframe,
		PayloadBytes: 4,
		Sequence:     7,
		RemoteNTPNS:  1_000_000_000,
	}
	if got := EncodeHeader(h); !bytes.Equal(got, want) {
		t.Fatalf("header=%x want=%x", got, want)
	}
}

func TestHeaderRoundTripAndLimits(t *testing.T) {
	h := Header{
		Version:      ProtocolVersion,
		HeaderBytes:  HeaderSize,
		StreamKind:   StreamAudio,
		Codec:        CodecAACELDRAW,
		Flags:        FlagDiscontinuity,
		PayloadBytes: 1024,
		Sequence:     99,
		RemoteNTPNS:  123456789,
		SampleCount:  480,
		SampleRate:   44100,
		Channels:     2,
	}
	decoded, err := DecodeHeader(EncodeHeader(h))
	if err != nil {
		t.Fatal(err)
	}
	if decoded != h {
		t.Fatalf("decoded=%+v want=%+v", decoded, h)
	}
	for _, tc := range []struct {
		name string
		h    Header
	}{
		{"video-too-large", Header{Version: ProtocolVersion, HeaderBytes: HeaderSize, StreamKind: StreamVideo, Codec: CodecH264AnnexBAU, PayloadBytes: MaxVideoPayload + 1}},
		{"audio-too-large", Header{Version: ProtocolVersion, HeaderBytes: HeaderSize, StreamKind: StreamAudio, Codec: CodecAACELDRAW, PayloadBytes: MaxAudioPayload + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeHeader(EncodeHeader(tc.h)); err == nil {
				t.Fatal("oversized payload was accepted")
			}
		})
	}
}

func TestHeaderDecodeRejectsReservedAndKindMismatch(t *testing.T) {
	h := Header{Version: ProtocolVersion, HeaderBytes: HeaderSize, StreamKind: StreamVideo, Codec: CodecH264AnnexBAU, PayloadBytes: 1}
	encoded := EncodeHeader(h)
	encoded[38] = 1
	if _, err := DecodeHeader(encoded); err == nil {
		t.Fatal("nonzero reserved field was accepted")
	}
	if _, err := DecodeHeaderForStream(encoded, StreamAudio); err == nil {
		t.Fatal("video frame was accepted on audio stream")
	}
}

func TestReadFrameHandlesFragmentedAndConcatenatedInput(t *testing.T) {
	first := Header{Version: ProtocolVersion, HeaderBytes: HeaderSize, StreamKind: StreamVideo, Codec: CodecH264AnnexBAU, PayloadBytes: 3, Sequence: 1}
	second := Header{Version: ProtocolVersion, HeaderBytes: HeaderSize, StreamKind: StreamVideo, Codec: CodecH264AnnexBAU, PayloadBytes: 2, Sequence: 2}
	input := append(EncodeHeader(first), []byte("abc")...)
	input = append(input, EncodeHeader(second)...)
	input = append(input, []byte("de")...)
	frameReader := oneByteReader{Reader: bytes.NewReader(input)}
	gotHeader, gotPayload, err := ReadFrame(&frameReader, StreamVideo)
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader.Sequence != 1 || string(gotPayload) != "abc" {
		t.Fatalf("first frame=%+v payload=%q", gotHeader, gotPayload)
	}
	gotHeader, gotPayload, err = ReadFrame(&frameReader, StreamVideo)
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader.Sequence != 2 || string(gotPayload) != "de" {
		t.Fatalf("second frame=%+v payload=%q", gotHeader, gotPayload)
	}
}

func TestReadFrameRejectsPayloadEOF(t *testing.T) {
	h := Header{Version: ProtocolVersion, HeaderBytes: HeaderSize, StreamKind: StreamAudio, Codec: CodecAACELDRAW, PayloadBytes: 4}
	input := append(EncodeHeader(h), []byte{1, 2}...)
	_, _, err := ReadFrame(bytes.NewReader(input), StreamAudio)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v want unexpected EOF", err)
	}
}

func TestValidateHelloRejectsInvalidTokenSizedPayload(t *testing.T) {
	h := Header{Version: ProtocolVersion, HeaderBytes: HeaderSize, StreamKind: StreamControl, Codec: ControlHello, PayloadBytes: SessionTokenBytes - 1}
	if err := ValidateHello(h, make([]byte, h.PayloadBytes)); err == nil {
		t.Fatal("short HELLO token was accepted")
	}
}

type oneByteReader struct{ io.Reader }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return r.Reader.Read(p[:1])
}
