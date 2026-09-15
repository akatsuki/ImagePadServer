// Package rtspdiagnostic contains an independent, read-only RTSP/RTP capture
// analyzer. It is deliberately separate from production media parsers.
package rtspdiagnostic

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var ErrMalformedRTP = errors.New("malformed RTP packet")

type RTPPacket struct {
	Sequence    uint16
	Timestamp   uint32
	PayloadType uint8
	Marker      bool
	SSRC        uint32
	Payload     []byte
}

func ParseRTP(b []byte) (RTPPacket, error) {
	var p RTPPacket
	if len(b) < 12 || b[0]>>6 != 2 {
		return p, ErrMalformedRTP
	}
	cc := int(b[0] & 15)
	h := 12 + cc*4
	if len(b) < h {
		return p, ErrMalformedRTP
	}
	if b[0]&0x10 != 0 {
		if len(b) < h+4 {
			return p, ErrMalformedRTP
		}
		words := int(binary.BigEndian.Uint16(b[h+2 : h+4]))
		h += 4 + words*4
		if len(b) < h {
			return p, ErrMalformedRTP
		}
	}
	end := len(b)
	if b[0]&0x20 != 0 {
		n := int(b[len(b)-1])
		if n == 0 || n > end-h {
			return p, ErrMalformedRTP
		}
		end -= n
	}
	if end < h {
		return p, ErrMalformedRTP
	}
	p.Sequence = binary.BigEndian.Uint16(b[2:4])
	p.Timestamp = binary.BigEndian.Uint32(b[4:8])
	p.PayloadType = b[1] & 0x7f
	p.Marker = b[1]&0x80 != 0
	p.SSRC = binary.BigEndian.Uint32(b[8:12])
	p.Payload = append([]byte(nil), b[h:end]...)
	if len(p.Payload) == 0 {
		return p, ErrMalformedRTP
	}
	return p, nil
}

type NALUnit struct {
	Type byte
	Data []byte
}
type AccessUnit struct {
	Timestamp uint32
	NALs      []NALUnit
	Complete  bool
}
type Stats struct{ RTPPackets, AUs, Frames, IncompleteAUs, SequenceGaps, MalformedFragments, TimestampWraps uint64 }
type Option func(*Assembler)

func WithMaxNALSize(n int) Option {
	return func(a *Assembler) {
		if n > 0 {
			a.maxNALSize = n
		}
	}
}

type Assembler struct {
	current      *AccessUnit
	fragment     []byte
	fragmentType byte
	fragmentSeq  uint16
	haveSeq      bool
	lastSeq      uint16
	haveLastSeq  bool
	lastTS       uint32
	haveTS       bool
	maxNALSize   int
	stats        Stats
}

func NewAssembler(opts ...Option) *Assembler {
	a := &Assembler{maxNALSize: 16 << 20}
	for _, o := range opts {
		o(a)
	}
	return a
}
func (a *Assembler) Stats() Stats { return a.stats }
func (a *Assembler) Push(p RTPPacket) []AccessUnit {
	a.stats.RTPPackets++
	gap := a.haveLastSeq && p.Sequence != a.lastSeq+1
	if gap {
		a.stats.SequenceGaps++
		a.abort(false)
	}
	if a.haveTS && p.Timestamp < a.lastTS && a.lastTS-p.Timestamp > 0x80000000 {
		a.stats.TimestampWraps++
	}
	a.lastTS, a.haveTS = p.Timestamp, true
	a.fragmentSeq, a.haveSeq = p.Sequence, true
	a.lastSeq, a.haveLastSeq = p.Sequence, true
	if gap {
		return nil
	}
	if a.current == nil {
		a.current = &AccessUnit{Timestamp: p.Timestamp, Complete: true}
	} else if a.current.Timestamp != p.Timestamp {
		a.abort(false)
		a.current = &AccessUnit{Timestamp: p.Timestamp, Complete: true}
	}
	if len(p.Payload) == 0 {
		a.abort(false)
		return nil
	}
	t := p.Payload[0] & 0x1f
	var nals []NALUnit
	switch {
	case t >= 1 && t <= 23:
		nals = []NALUnit{{Type: t, Data: append([]byte(nil), p.Payload...)}}
	case t == 24:
		for x := p.Payload[1:]; len(x) > 0; {
			if len(x) < 2 {
				a.abort(false)
				return nil
			}
			n := int(binary.BigEndian.Uint16(x))
			x = x[2:]
			if n < 1 || n > len(x) {
				a.abort(false)
				return nil
			}
			nals = append(nals, NALUnit{Type: x[0] & 0x1f, Data: append([]byte(nil), x[:n]...)})
			x = x[n:]
		}
	case t == 28:
		if len(p.Payload) < 2 {
			a.abort(false)
			return nil
		}
		fu := p.Payload[1]
		start, end := fu&0x80 != 0, fu&0x40 != 0
		typ := fu & 0x1f
		if typ == 0 {
			a.abort(false)
			return nil
		}
		if start {
			a.fragment = []byte{(p.Payload[0] & 0xe0) | typ}
			a.fragment = append(a.fragment, p.Payload[2:]...)
			a.fragmentType = typ
		} else {
			if len(a.fragment) == 0 || a.fragmentType != typ {
				a.abort(false)
				return nil
			}
			a.fragment = append(a.fragment, p.Payload[2:]...)
		}
		if len(a.fragment) > a.maxNALSize {
			a.abort(false)
			return nil
		}
		if !end {
			return nil
		}
		nals = []NALUnit{{Type: typ, Data: append([]byte(nil), a.fragment...)}}
		a.fragment = nil
	default:
		a.abort(false)
		return nil
	}
	for _, n := range nals {
		if len(n.Data) > a.maxNALSize {
			a.abort(false)
			return nil
		}
		a.current.NALs = append(a.current.NALs, n)
	}
	if !p.Marker {
		return nil
	}
	return a.finish()
}
func (a *Assembler) finish() []AccessUnit {
	if a.current == nil || len(a.current.NALs) == 0 {
		a.abort(false)
		return nil
	}
	out := []AccessUnit{*a.current}
	a.current = nil
	a.stats.AUs++
	a.stats.Frames++
	return out
}
func (a *Assembler) abort(gap bool) {
	if a.current != nil || len(a.fragment) > 0 {
		a.stats.IncompleteAUs++
		if gap {
			a.stats.SequenceGaps++
		}
	}
	a.current = nil
	a.fragment = nil
	a.fragmentType = 0
}
func (a *Assembler) Flush() []AccessUnit { a.abort(false); return nil }
func (n NALUnit) String() string         { return fmt.Sprintf("NAL(type=%d,len=%d)", n.Type, len(n.Data)) }
