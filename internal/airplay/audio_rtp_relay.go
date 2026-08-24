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

type l16RTPRelay struct {
	inputConn      *net.UDPConn
	outputAddr     *net.UDPAddr
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	videoStarted   atomic.Bool
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
	r := &l16RTPRelay{inputConn: inputConn, outputAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: outputPort}, cancel: cancel}
	r.wg.Add(1)
	go r.run(ctx)
	if os.Getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG") == "1" {
		go r.logStats(ctx)
	}
	return r, nil
}

func (r *l16RTPRelay) NotifyVideoActivity() { r.videoStarted.Store(true) }

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

func (r *l16RTPRelay) run(ctx context.Context) {
	defer r.wg.Done()
	packets := make(chan []byte, 32)
	go func() {
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
			payload := append([]byte(nil), buf[offset:n]...)
			select {
			case packets <- payload:
			case <-ctx.Done():
				return
			}
		}
	}()

	interval := time.Second * l16FramesPerPacket / l16ClockRate
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var sequence uint16
	var timestamp uint32
	marker := true
	silence := make([]byte, l16FramesPerPacket*l16Channels*l16BytesPerSample)
	queue := make([]byte, 0, len(silence)*2)
	for {
		select {
		case <-ctx.Done():
			return
		case payload := <-packets:
			r.inputPackets.Add(1)
			queue = append(queue, payload...)
			if len(queue) > len(silence)*16 {
				queue = queue[len(queue)-len(silence)*16:]
			}
		case <-ticker.C:
			if !r.videoStarted.Load() {
				continue
			}
			payload := silence
			isSilence := true
			if len(queue) >= len(silence) {
				payload = queue[:len(silence)]
				queue = queue[len(silence):]
				isSilence = false
			}
			packet := make([]byte, 12+len(payload))
			packet[0] = 0x80
			packet[1] = l16PayloadType
			if marker {
				packet[1] |= 0x80
				marker = false
			}
			binary.BigEndian.PutUint16(packet[2:4], sequence)
			binary.BigEndian.PutUint32(packet[4:8], timestamp)
			binary.BigEndian.PutUint32(packet[8:12], l16SSRC)
			copy(packet[12:], payload)
			if _, err := r.inputConn.WriteToUDP(packet, r.outputAddr); err != nil {
				return
			}
			sequence++
			timestamp += l16FramesPerPacket
			r.outputPackets.Add(1)
			if isSilence {
				r.silencePackets.Add(1)
			}
		}
	}
}
