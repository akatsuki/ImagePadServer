// Command uxplay-capture records the raw UDP datagrams emitted by UxPlay.
// It deliberately does not decode, reorder, or rewrite RTP packets.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var captureMagic = []byte("IPRTP01\n")

const captureRecordHeaderSize = 12

const captureReadBufferSize = 16 << 20

type captureEvent struct {
	ArrivalUnixNano int64  `json:"arrival_unix_nano"`
	Length          int    `json:"length"`
	Remote          string `json:"remote"`
	PayloadType     uint8  `json:"payload_type,omitempty"`
	Marker          bool   `json:"marker,omitempty"`
	Sequence        uint16 `json:"sequence,omitempty"`
	Timestamp       uint32 `json:"timestamp,omitempty"`
	SSRC            uint32 `json:"ssrc,omitempty"`
}

func main() {
	videoPort := flag.Int("video-port", 0, "UDP port receiving UxPlay video RTP")
	audioPort := flag.Int("audio-port", 0, "UDP port receiving UxPlay audio RTP")
	outDir := flag.String("out", "", "directory for raw RTP files")
	flag.Parse()
	if *videoPort <= 0 || *audioPort <= 0 || *outDir == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := os.MkdirAll(*outDir, 0700); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	for _, stream := range []struct {
		name string
		port int
	}{
		{name: "video", port: *videoPort},
		{name: "audio", port: *audioPort},
	} {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: stream.port})
		if err != nil {
			log.Fatal(err)
		}
		if err := conn.SetReadBuffer(captureReadBufferSize); err != nil {
			log.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			if err := captureStream(ctx, conn, *outDir, stream.name); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Printf("%s capture: %v", stream.name, err)
			}
		}()
		log.Printf("capturing %s RTP on UDP %d", stream.name, stream.port)
	}

	<-ctx.Done()
	wg.Wait()
	log.Printf("capture stopped; files written to %s", *outDir)
}

func captureStream(ctx interface{ Done() <-chan struct{} }, conn *net.UDPConn, outDir, name string) error {
	raw, err := os.OpenFile(filepath.Join(outDir, name+".rtpbin"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer raw.Close()
	if err := writeCaptureHeader(raw); err != nil {
		return err
	}

	meta, err := os.OpenFile(filepath.Join(outDir, name+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer meta.Close()

	buffer := make([]byte, 64*1024)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return err
		}
		n, remote, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if isReadTimeout(err) {
				select {
				case <-ctx.Done():
					return nil
				default:
					continue
				}
			}
			return err
		}
		arrival := time.Now()
		payload := buffer[:n]
		if err := writeCaptureRecord(raw, arrival, payload); err != nil {
			return err
		}
		event := captureEvent{ArrivalUnixNano: arrival.UnixNano(), Length: n, Remote: remote.String()}
		if sequence, timestamp, payloadType, marker, ssrc, ok := parseRTPHeader(payload); ok {
			event.Sequence = sequence
			event.Timestamp = timestamp
			event.PayloadType = payloadType
			event.Marker = marker
			event.SSRC = ssrc
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(meta, "%s\n", encoded); err != nil {
			return err
		}
	}
}

func isReadTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	netErr, ok := err.(net.Error)
	return ok && netErr.Timeout()
}

func writeCaptureHeader(w io.Writer) error {
	_, err := w.Write(captureMagic)
	return err
}

func writeCaptureRecord(w io.Writer, arrival time.Time, payload []byte) error {
	var header [captureRecordHeaderSize]byte
	binary.LittleEndian.PutUint64(header[0:8], uint64(arrival.UnixNano()))
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func parseRTPHeader(packet []byte) (sequence uint16, timestamp uint32, payloadType uint8, marker bool, ssrc uint32, ok bool) {
	if len(packet) < 12 || packet[0]>>6 != 2 {
		return 0, 0, 0, false, 0, false
	}
	cc := int(packet[0] & 0x0f)
	headerSize := 12 + cc*4
	if len(packet) < headerSize {
		return 0, 0, 0, false, 0, false
	}
	if packet[0]&0x10 != 0 {
		if len(packet) < headerSize+4 {
			return 0, 0, 0, false, 0, false
		}
		extensionWords := int(binary.BigEndian.Uint16(packet[headerSize+2 : headerSize+4]))
		headerSize += 4 + extensionWords*4
		if len(packet) < headerSize {
			return 0, 0, 0, false, 0, false
		}
	}
	return binary.BigEndian.Uint16(packet[2:4]), binary.BigEndian.Uint32(packet[4:8]), packet[1] & 0x7f, packet[1]&0x80 != 0, binary.BigEndian.Uint32(packet[8:12]), true
}
