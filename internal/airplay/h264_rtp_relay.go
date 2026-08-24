package airplay

import (
	"bytes"
	"context"
	"encoding/binary"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const h264RTPRelaySocketBufferSize = 4 * 1024 * 1024

// h264RTPRelay keeps H.264 parameter sets across FFmpeg bridge restarts and
// repeats them immediately before each IDR access unit. UxPlay sends SPS/PPS
// only at the beginning of a mirroring session or after a format change, so a
// direct UDP consumer cannot recover if it misses those packets.
type h264RTPRelay struct {
	conn             *net.UDPConn
	output           *net.UDPAddr
	done             chan struct{}
	once             sync.Once
	replayGeneration atomic.Uint64
	onPacket         func()
}

func startH264RTPRelay(ctx context.Context, inputPort, outputPort int, onPacket ...func()) (*h264RTPRelay, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: inputPort})
	if err != nil {
		return nil, err
	}
	if err := conn.SetReadBuffer(h264RTPRelaySocketBufferSize); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetWriteBuffer(h264RTPRelaySocketBufferSize); err != nil {
		_ = conn.Close()
		return nil, err
	}
	relay := &h264RTPRelay{
		conn:   conn,
		output: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: outputPort},
		done:   make(chan struct{}),
	}
	if len(onPacket) > 0 {
		relay.onPacket = onPacket[0]
	}
	go relay.run()
	go func() {
		select {
		case <-ctx.Done():
			relay.closeSocket()
		case <-relay.done:
		}
	}()
	return relay, nil
}

func (r *h264RTPRelay) closeSocket() {
	r.once.Do(func() { _ = r.conn.Close() })
}

func (r *h264RTPRelay) Close() {
	r.closeSocket()
	<-r.done
}

func (r *h264RTPRelay) ReplayParameterSets() {
	r.replayGeneration.Add(1)
	// Wake a blocked read so a newly started bridge receives the cached random
	// access point immediately, even while iOS is not sending changed frames.
	_ = r.conn.SetReadDeadline(time.Now())
}

func (r *h264RTPRelay) run() {
	defer close(r.done)
	buffer := make([]byte, 65535)
	var state h264RTPRelayState
	debug := os.Getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG") == "1"
	packetDebug := os.Getenv("IMAGEPAD_AIRPLAY_RTP_PACKET_DEBUG") == "1"
	var inputPackets uint64
	var outputPackets uint64
	var handledReplay uint64
	writePackets := func(packets [][]byte) {
		for _, packet := range packets {
			outputPackets++
			if packetDebug {
				logH264RTPMetadata("output", outputPackets, packet)
			}
			_, _ = r.conn.WriteToUDP(packet, r.output)
		}
	}
	for {
		// Clear an old wake-up deadline before checking the generation. If a
		// request races with this clear, its generation is visible below; if it
		// races after the check, SetReadDeadline wakes ReadFromUDP.
		_ = r.conn.SetReadDeadline(time.Time{})
		requestedReplay := r.replayGeneration.Load()
		if requestedReplay != handledReplay {
			if packets, replayed := state.replayCachedDecoderRefresh(); replayed {
				handledReplay = requestedReplay
				if debug {
					log.Printf("AirPlay H264 RTP immediately supplied complete decoder refresh generation=%d", requestedReplay)
				}
				writePackets(packets)
				if r.onPacket != nil {
					r.onPacket()
				}
				continue
			}
		}

		n, _, err := r.conn.ReadFromUDP(buffer)
		if err != nil {
			if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
				continue
			}
			return
		}
		inputPackets++
		if packetDebug {
			logH264RTPMetadata("input", inputPackets, buffer[:n])
		}
		requestedReplay = r.replayGeneration.Load()
		replay := requestedReplay != handledReplay
		previousDiscontinuities := state.inputDiscontinuities
		previousFormatChanges := state.formatChanges
		packets, replayed := state.forwardWithReplay(buffer[:n], replay)
		if debug && state.inputDiscontinuities != previousDiscontinuities {
			log.Printf("AirPlay H264 RTP input discontinuity total=%d expected_seq=%d received_seq=%d; dropping until a complete IDR", state.inputDiscontinuities, state.lastExpectedSequence, state.lastReceivedSequence)
		}
		if debug && state.formatChanges != previousFormatChanges {
			log.Printf("AirPlay H264 RTP parameter-set change total=%d; invalidated cached IDR and waiting for a new IDR", state.formatChanges)
		}
		if replayed {
			handledReplay = requestedReplay
			if debug {
				log.Printf("AirPlay H264 RTP supplied complete decoder refresh generation=%d", requestedReplay)
			}
		}
		writePackets(packets)
		if replayed && r.onPacket != nil {
			r.onPacket()
		}
	}
}

func logH264RTPMetadata(direction string, ordinal uint64, packet []byte) {
	payload, _, ok := rtpPayload(packet)
	if !ok {
		if ordinal <= 20 {
			log.Printf("AirPlay H264 RTP %s packet=%d invalid", direction, ordinal)
		}
		return
	}
	nalType := payload[0] & 0x1f
	fuType := byte(0)
	start := false
	end := false
	if nalType == 28 && len(payload) >= 2 {
		fuType = payload[1] & 0x1f
		start = payload[1]&0x80 != 0
		end = payload[1]&0x40 != 0
	}
	special := nalType == 5 || nalType == 7 || nalType == 8 || fuType == 5 || fuType == 7 || fuType == 8
	if ordinal > 20 && !special {
		return
	}
	log.Printf("AirPlay H264 RTP %s packet=%d seq=%d ts=%d pt=%d marker=%t nal=%d fu=%d start=%t end=%t bytes=%d", direction, ordinal, binary.BigEndian.Uint16(packet[2:4]), binary.BigEndian.Uint32(packet[4:8]), packet[1]&0x7f, packet[1]&0x80 != 0, nalType, fuType, start, end, len(payload))
}

const defaultH264RTPFrameTimestampStep uint32 = 90000 / 30

type h264RTPRelayState struct {
	initialized           bool
	nextSequence          uint16
	timestampInitialized  bool
	outputTimestamp       uint32
	lastInputTimestamp    uint32
	previousMarker        bool
	inputSequenceReady    bool
	expectedInputSequence uint16
	inputSSRCReady        bool
	inputSSRC             uint32
	inputDiscontinuities  uint64
	lastExpectedSequence  uint16
	lastReceivedSequence  uint16
	formatChanges         uint64
	accessUnitPackets     [][]byte
	accessUnitHasIDR      bool
	cachedIDRPackets      [][]byte
	dropUntilMarker       bool
	waitingForIDR         bool
	sps                   []byte
	pps                   []byte
	spsTimestamp          uint32
	ppsTimestamp          uint32
	fuParameterSet        []byte
	fuParameterType       byte
	fuTimestamp           uint32
}

func (s *h264RTPRelayState) forward(packet []byte) [][]byte {
	packets, _ := s.forwardWithReplay(packet, false)
	return packets
}

func (s *h264RTPRelayState) forwardWithReplay(packet []byte, replay bool) ([][]byte, bool) {
	payload, headerSize, ok := rtpPayload(packet)
	if !ok {
		return nil, false
	}
	sequence := binary.BigEndian.Uint16(packet[2:4])
	if !s.initialized {
		s.initialized = true
		s.nextSequence = sequence
	}
	s.observeInputStream(packet, sequence)
	timestamp := binary.BigEndian.Uint32(packet[4:8])
	isIDR := false
	for _, nal := range h264NALUnits(payload) {
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 0x1f {
		case 5:
			isIDR = true
		case 7:
			s.updateParameterSet(7, nal, timestamp)
		case 8:
			s.updateParameterSet(8, nal, timestamp)
		}
	}
	s.consumeFUParameterSet(payload, timestamp)
	if h264PayloadStartsIDR(payload) {
		isIDR = true
	}
	if s.dropUntilMarker {
		if packet[1]&0x80 != 0 {
			s.dropUntilMarker = false
			s.previousMarker = true
		}
		return nil, false
	}

	parameterSetsAvailable := len(s.sps) > 0 && len(s.pps) > 0
	if isIDR && parameterSetsAvailable {
		s.waitingForIDR = false
	}
	if s.waitingForIDR && h264PayloadHasVCL(payload) {
		if packet[1]&0x80 != 0 {
			s.accessUnitPackets = nil
			s.accessUnitHasIDR = false
		}
		return nil, false
	}

	forwarded := append([]byte(nil), packet...)
	s.normalizeTimestamp(forwarded)
	if !h264PayloadOnlyParameterSets(payload) {
		s.cacheAccessUnit(packet, isIDR)
	}

	out := make([][]byte, 0, 3+len(s.cachedIDRPackets))
	if replay {
		if replayedPackets, replayed := s.replayCachedDecoderRefresh(); replayed {
			s.dropUntilMarker = packet[1]&0x80 == 0
			return replayedPackets, true
		}
	}
	replayed := replay && isIDR && parameterSetsAvailable
	if isIDR && parameterSetsAvailable {
		out = append(out, s.emit(injectedRTPPacket(forwarded, headerSize, s.sps)))
		out = append(out, s.emit(injectedRTPPacket(forwarded, headerSize, s.pps)))
	}
	out = append(out, s.emit(forwarded))
	return out, replayed
}

func (s *h264RTPRelayState) observeInputStream(packet []byte, sequence uint16) {
	ssrc := binary.BigEndian.Uint32(packet[8:12])
	if !s.inputSSRCReady {
		s.inputSSRCReady = true
		s.inputSSRC = ssrc
		s.waitingForIDR = true
	} else if s.inputSSRC != ssrc {
		s.inputSSRC = ssrc
		s.inputSequenceReady = false
		s.inputDiscontinuities++
		s.lastExpectedSequence = s.expectedInputSequence
		s.lastReceivedSequence = sequence
		s.resetDecoderInput(true)
		s.dropUntilMarker = false
	}
	if !s.inputSequenceReady {
		s.inputSequenceReady = true
		s.expectedInputSequence = sequence + 1
		return
	}
	if sequence != s.expectedInputSequence {
		s.inputDiscontinuities++
		s.lastExpectedSequence = s.expectedInputSequence
		s.lastReceivedSequence = sequence
		s.resetDecoderInput(false)
	}
	s.expectedInputSequence = sequence + 1
}

func (s *h264RTPRelayState) resetDecoderInput(clearParameterSets bool) {
	s.accessUnitPackets = nil
	s.accessUnitHasIDR = false
	s.fuParameterSet = nil
	s.fuParameterType = 0
	s.cachedIDRPackets = nil
	s.dropUntilMarker = true
	s.waitingForIDR = true
	if clearParameterSets {
		s.sps = nil
		s.pps = nil
	}
}

func (s *h264RTPRelayState) updateParameterSet(nalType byte, nal []byte, timestamp uint32) {
	var current []byte
	if nalType == 7 {
		current = s.sps
	} else {
		current = s.pps
	}
	changed := len(current) == 0 || !bytes.Equal(current, nal)
	if changed {
		if len(current) > 0 {
			s.formatChanges++
		}
		s.cachedIDRPackets = nil
		s.accessUnitPackets = nil
		s.accessUnitHasIDR = false
		s.waitingForIDR = true
	}
	if nalType == 7 {
		s.sps = append(s.sps[:0], nal...)
		s.spsTimestamp = timestamp
		if changed && len(current) > 0 {
			s.pps = nil
		}
	} else {
		s.pps = append(s.pps[:0], nal...)
		s.ppsTimestamp = timestamp
	}
}

func (s *h264RTPRelayState) cacheAccessUnit(packet []byte, isIDR bool) {
	s.accessUnitPackets = append(s.accessUnitPackets, append([]byte(nil), packet...))
	s.accessUnitHasIDR = s.accessUnitHasIDR || isIDR
	if packet[1]&0x80 == 0 {
		return
	}
	if s.accessUnitHasIDR {
		s.cachedIDRPackets = s.accessUnitPackets
	}
	s.accessUnitPackets = nil
	s.accessUnitHasIDR = false
}

func (s *h264RTPRelayState) hasCachedIDR() bool {
	return len(s.cachedIDRPackets) > 0
}

func (s *h264RTPRelayState) replayCachedDecoderRefresh() ([][]byte, bool) {
	if len(s.sps) == 0 || len(s.pps) == 0 || len(s.cachedIDRPackets) == 0 {
		return nil, false
	}
	_, headerSize, ok := rtpPayload(s.cachedIDRPackets[0])
	if !ok {
		return nil, false
	}
	template := append([]byte(nil), s.cachedIDRPackets[0]...)
	binary.BigEndian.PutUint32(template[4:8], s.outputTimestamp)
	out := make([][]byte, 0, 2+len(s.cachedIDRPackets))
	out = append(out, s.emit(injectedRTPPacket(template, headerSize, s.sps)))
	out = append(out, s.emit(injectedRTPPacket(template, headerSize, s.pps)))
	for _, cached := range s.cachedIDRPackets {
		replayedPacket := append([]byte(nil), cached...)
		binary.BigEndian.PutUint32(replayedPacket[4:8], s.outputTimestamp)
		out = append(out, s.emit(replayedPacket))
	}
	s.accessUnitPackets = nil
	s.accessUnitHasIDR = false
	return out, true
}

// normalizeTimestamp rewrites each packet's RTP timestamp into the zero-based
// output timeline shared with the audio relay. The first input timestamp (an
// arbitrary iOS RTP origin) is discarded: the first packet is emitted at zero
// and later access units advance by the observed input deltas. A broken
// constant input timestamp is advanced by a fixed 90 kHz step at each
// access-unit boundary (UxPlay on Windows can emit every mirrored frame with
// the same timestamp, which would otherwise leave FFmpeg's output PTS frozen).
func (s *h264RTPRelayState) normalizeTimestamp(packet []byte) {
	inputTimestamp := binary.BigEndian.Uint32(packet[4:8])
	if !s.timestampInitialized {
		s.timestampInitialized = true
		s.outputTimestamp = 0
	} else if s.previousMarker {
		delta := inputTimestamp - s.lastInputTimestamp
		if delta == 0 {
			delta = defaultH264RTPFrameTimestampStep
		}
		s.outputTimestamp += delta
	}
	s.lastInputTimestamp = inputTimestamp
	s.previousMarker = packet[1]&0x80 != 0
	binary.BigEndian.PutUint32(packet[4:8], s.outputTimestamp)
}

func (s *h264RTPRelayState) emit(packet []byte) []byte {
	binary.BigEndian.PutUint16(packet[2:4], s.nextSequence)
	s.nextSequence++
	return packet
}

func (s *h264RTPRelayState) consumeFUParameterSet(payload []byte, timestamp uint32) {
	if len(payload) < 3 || payload[0]&0x1f != 28 {
		return
	}
	nalType := payload[1] & 0x1f
	if nalType != 7 && nalType != 8 {
		return
	}
	start := payload[1]&0x80 != 0
	end := payload[1]&0x40 != 0
	if start {
		s.fuParameterSet = append(s.fuParameterSet[:0], payload[0]&0xe0|nalType)
		s.fuParameterSet = append(s.fuParameterSet, payload[2:]...)
		s.fuParameterType = nalType
		s.fuTimestamp = timestamp
	} else {
		if s.fuParameterType != nalType || s.fuTimestamp != timestamp || len(s.fuParameterSet) == 0 {
			return
		}
		s.fuParameterSet = append(s.fuParameterSet, payload[2:]...)
	}
	if !end || s.fuParameterType != nalType || s.fuTimestamp != timestamp {
		return
	}
	if nalType == 7 {
		s.updateParameterSet(7, s.fuParameterSet, timestamp)
	} else {
		s.updateParameterSet(8, s.fuParameterSet, timestamp)
	}
	s.fuParameterSet = s.fuParameterSet[:0]
	s.fuParameterType = 0
}

func rtpPayload(packet []byte) ([]byte, int, bool) {
	if len(packet) < 12 || packet[0]>>6 != 2 {
		return nil, 0, false
	}
	headerSize := 12 + int(packet[0]&0x0f)*4
	if len(packet) < headerSize {
		return nil, 0, false
	}
	if packet[0]&0x10 != 0 {
		if len(packet) < headerSize+4 {
			return nil, 0, false
		}
		extensionWords := int(binary.BigEndian.Uint16(packet[headerSize+2 : headerSize+4]))
		headerSize += 4 + extensionWords*4
		if len(packet) < headerSize {
			return nil, 0, false
		}
	}
	payloadEnd := len(packet)
	if packet[0]&0x20 != 0 {
		padding := int(packet[len(packet)-1])
		if padding == 0 || padding > payloadEnd-headerSize {
			return nil, 0, false
		}
		payloadEnd -= padding
	}
	if payloadEnd <= headerSize {
		return nil, 0, false
	}
	return packet[headerSize:payloadEnd], headerSize, true
}

func h264NALUnits(payload []byte) [][]byte {
	if len(payload) == 0 || payload[0]&0x1f != 24 {
		return [][]byte{payload}
	}
	var units [][]byte
	for offset := 1; offset+2 <= len(payload); {
		size := int(binary.BigEndian.Uint16(payload[offset : offset+2]))
		offset += 2
		if size == 0 || offset+size > len(payload) {
			return units
		}
		units = append(units, payload[offset:offset+size])
		offset += size
	}
	return units
}

func h264PayloadOnlyParameterSets(payload []byte) bool {
	units := h264NALUnits(payload)
	if len(units) == 0 {
		return false
	}
	for _, nal := range units {
		if len(nal) == 0 {
			return false
		}
		nalType := nal[0] & 0x1f
		if nalType == 28 && len(nal) >= 2 {
			nalType = nal[1] & 0x1f
		}
		if nalType != 7 && nalType != 8 {
			return false
		}
	}
	return true
}

func h264PayloadHasVCL(payload []byte) bool {
	for _, nal := range h264NALUnits(payload) {
		if len(nal) == 0 {
			continue
		}
		nalType := nal[0] & 0x1f
		if nalType >= 1 && nalType <= 5 {
			return true
		}
		if nalType == 28 && len(nal) >= 2 {
			fuType := nal[1] & 0x1f
			if fuType >= 1 && fuType <= 5 {
				return true
			}
		}
	}
	return false
}

func h264PayloadStartsIDR(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	switch payload[0] & 0x1f {
	case 5:
		return true
	case 28:
		return len(payload) >= 2 && payload[1]&0x80 != 0 && payload[1]&0x1f == 5
	default:
		return false
	}
}

func injectedRTPPacket(template []byte, headerSize int, payload []byte) []byte {
	packet := make([]byte, headerSize+len(payload))
	copy(packet, template[:headerSize])
	packet[0] &^= 0x20
	packet[1] &^= 0x80
	copy(packet[headerSize:], payload)
	return packet
}
