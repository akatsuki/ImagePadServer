package airplay

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	l16ClockRate       = 44100
	l16Channels        = 2
	l16BytesPerSample  = 2
	l16FramesPerPacket = 352
	l16PayloadType     = 96
	l16SSRC            = 0x49504144
)

const (
	// l16FrameBytes is the byte width of one L16 stereo sample frame.
	l16FrameBytes = l16Channels * l16BytesPerSample
	// l16PacketBytes is the payload width of one output RTP packet.
	l16PacketBytes = l16FramesPerPacket * l16FrameBytes
	// l16MaxQueueBytes bounds the steady-state queue (~128 ms) so a source
	// clock that runs faster than the consumer cannot grow memory without
	// limit. Trimming always happens on a whole-frame boundary.
	l16MaxQueueBytes = l16PacketBytes * 16
	// l16LeadInMaxBytes bounds the audio buffered before the video stream
	// starts (~2 s). This preserves the leading audio (replayed from the head
	// once video begins) without risking unbounded growth if video never
	// arrives.
	l16LeadInMaxBytes = 2 * l16ClockRate * l16FrameBytes
)

const (
	// l16PacketInterval is the nominal wall-clock duration of one L16 packet.
	l16PacketInterval = time.Second * l16FramesPerPacket / l16ClockRate
	// l16StallTimeout is how long the relay waits for input after video has
	// started before emitting silence to keep the downstream stream continuous.
	l16StallTimeout = 4 * l16PacketInterval
)

// l16RTPRelayState encapsulates the L16 re-packetization logic: a frame-aligned
// queue, a normalized sequence/timestamp clock, talk-spurt marker placement,
// and silence fill. It is decoupled from UDP I/O so the drift and alignment
// behaviour can be unit-tested deterministically.
//
// The output timestamp is normalized to a zero origin (consistent with the
// normalized video timeline) and advances by l16FramesPerPacket per packet.
// Drift compensation is achieved in the relay run loop by emitting packets in
// response to input arrival (input-clock driven) rather than on a fixed
// wall-clock ticker, so the output rate tracks the source clock.
type l16RTPRelayState struct {
	queue       []byte
	sequence    uint16
	outputTS    uint32
	baseTS      uint32
	initialized bool
	wasSilence  bool
}

func newL16RTPRelayState() *l16RTPRelayState {
	// wasSilence starts true so the first real audio packet (the first
	// talk-spurt) carries the RTP marker bit.
	return &l16RTPRelayState{wasSilence: true}
}

// ingest appends an input L16 payload to the queue, recording the input RTP
// timestamp on first use. The payload is truncated to a whole-frame boundary
// and the queue is trimmed to maxQueueBytes on a frame boundary, so a partial
// stereo sample can never be split across output packets.
func (s *l16RTPRelayState) ingest(timestamp uint32, payload []byte, maxQueueBytes int) {
	if !s.initialized {
		s.initialized = true
		s.baseTS = timestamp
	}
	if rem := len(payload) % l16FrameBytes; rem != 0 {
		payload = payload[:len(payload)-rem]
	}
	s.queue = append(s.queue, payload...)
	if maxQueueBytes > 0 && len(s.queue) > maxQueueBytes {
		drop := len(s.queue) - maxQueueBytes
		drop -= drop % l16FrameBytes
		s.queue = s.queue[drop:]
	}
}

// hasFullPacket reports whether at least one full output packet is buffered.
func (s *l16RTPRelayState) hasFullPacket() bool {
	return len(s.queue) >= l16PacketBytes
}

// emit returns the next output RTP packet, consuming a full packet from the
// queue or emitting silence when the queue has underflowed. It advances the
// sequence and normalized output timestamp and sets the marker bit at
// talk-spurt boundaries (silence-to-audio transitions).
func (s *l16RTPRelayState) emit() (packet []byte, isSilence bool) {
	isSilence = len(s.queue) < l16PacketBytes
	var payload []byte
	if isSilence {
		payload = make([]byte, l16PacketBytes)
	} else {
		payload = s.queue[:l16PacketBytes]
		s.queue = s.queue[l16PacketBytes:]
	}
	packet = make([]byte, 12+len(payload))
	packet[0] = 0x80
	packet[1] = l16PayloadType
	if s.wasSilence && !isSilence {
		packet[1] |= 0x80
	}
	binary.BigEndian.PutUint16(packet[2:4], s.sequence)
	binary.BigEndian.PutUint32(packet[4:8], s.outputTS)
	binary.BigEndian.PutUint32(packet[8:12], l16SSRC)
	copy(packet[12:], payload)
	s.sequence++
	s.outputTS += l16FramesPerPacket
	s.wasSilence = isSilence
	return packet, isSilence
}

type l16RTPRelay struct {
	inputConn      *net.UDPConn
	outputAddr     *net.UDPAddr
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	videoStarted   atomic.Bool
	videoStartedCh chan struct{}
	inputPackets   atomic.Uint64
	outputPackets  atomic.Uint64
	silencePackets atomic.Uint64
}

func startL16RTPRelay(parent context.Context, inputPort, outputPort int) (*l16RTPRelay, error) {
	inputConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: inputPort})
	if err != nil {
		return nil, fmt.Errorf("listen L16 RTP input: %w", err)
	}
	_ = inputConn.SetReadBuffer(4 << 20)
	ctx, cancel := context.WithCancel(parent)
	r := &l16RTPRelay{
		inputConn:      inputConn,
		outputAddr:     &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: outputPort},
		cancel:         cancel,
		videoStartedCh: make(chan struct{}, 1),
	}
	r.wg.Add(1)
	go r.run(ctx)
	if os.Getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG") == "1" {
		go r.logStats(ctx)
	}
	return r, nil
}

// NotifyVideoActivity is called by the video relay when it forwards its first
// packet. It flips the videoStarted flag exactly once and wakes the run loop so
// it can immediately replay the audio buffered during the video lead-in.
func (r *l16RTPRelay) NotifyVideoActivity() {
	if r.videoStarted.CompareAndSwap(false, true) {
		select {
		case r.videoStartedCh <- struct{}{}:
		default:
		}
	}
}

func (r *l16RTPRelay) Close() {
	if r == nil {
		return
	}
	r.cancel()
	_ = r.inputConn.Close()
	r.wg.Wait()
}

func (r *l16RTPRelay) Stats() (input, output, silence uint64) {
	return r.inputPackets.Load(), r.outputPackets.Load(), r.silencePackets.Load()
}

func (r *l16RTPRelay) logStats(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			input, output, silence := r.Stats()
			log.Printf("AirPlay L16 RTP relay input=%d output=%d silence=%d video_active=%t", input, output, silence, r.videoStarted.Load())
		}
	}
}

type l16AudioInput struct {
	timestamp uint32
	payload   []byte
}

func (r *l16RTPRelay) readLoop(ctx context.Context, packets chan<- l16AudioInput) {
	buf := make([]byte, 65536)
	for {
		n, err := r.inputConn.Read(buf)
		if err != nil {
			return
		}
		if n < 12 || buf[0]>>6 != 2 || buf[1]&0x7f != l16PayloadType {
			continue
		}
		cc := int(buf[0] & 0x0f)
		offset := 12 + cc*4
		if buf[0]&0x10 != 0 {
			if n < offset+4 {
				continue
			}
			offset += 4 + int(binary.BigEndian.Uint16(buf[offset+2:offset+4]))*4
		}
		if offset >= n {
			continue
		}
		timestamp := binary.BigEndian.Uint32(buf[4:8])
		payload := append([]byte(nil), buf[offset:n]...)
		select {
		case packets <- l16AudioInput{timestamp: timestamp, payload: payload}:
		case <-ctx.Done():
			return
		}
	}
}

func (r *l16RTPRelay) run(ctx context.Context) {
	defer r.wg.Done()
	packets := make(chan l16AudioInput, 32)
	go r.readLoop(ctx, packets)

	state := newL16RTPRelayState()
	stall := time.NewTimer(l16StallTimeout)
	defer stall.Stop()
	stall.Stop() // disarm until video starts

	armStall := func(d time.Duration) {
		if !stall.Stop() {
			select {
			case <-stall.C:
			default:
			}
		}
		stall.Reset(d)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-packets:
			r.inputPackets.Add(1)
			maxQueue := l16LeadInMaxBytes
			if r.videoStarted.Load() {
				maxQueue = l16MaxQueueBytes
			}
			state.ingest(p.timestamp, p.payload, maxQueue)
			if r.videoStarted.Load() {
				// Input-clock driven emission: drain whatever full packets
				// the source has produced so the output rate follows the
				// input clock rather than a fixed ticker.
				for state.hasFullPacket() {
					packet, isSilence := state.emit()
					if !r.writePacket(packet, isSilence) {
						return
					}
				}
				armStall(l16StallTimeout)
			}
		case <-r.videoStartedCh:
			// Replay the audio buffered during the video lead-in from the head.
			for state.hasFullPacket() {
				packet, isSilence := state.emit()
				if !r.writePacket(packet, isSilence) {
					return
				}
			}
			armStall(l16StallTimeout)
		case <-stall.C:
			if !r.videoStarted.Load() {
				continue
			}
			// The source stalled: emit silence to keep the stream continuous.
			packet, isSilence := state.emit()
			if !r.writePacket(packet, isSilence) {
				return
			}
			armStall(l16PacketInterval)
		}
	}
}

// writePacket counts the packet before writing so a reader that already
// observed the datagram sees a consistent Stats() view (closes a read-then-check
// race in the clock-forwarding test).
func (r *l16RTPRelay) writePacket(packet []byte, isSilence bool) bool {
	r.outputPackets.Add(1)
	if isSilence {
		r.silencePackets.Add(1)
	}
	_, err := r.inputConn.WriteToUDP(packet, r.outputAddr)
	return err == nil
}
