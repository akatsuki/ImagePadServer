package obsrtmp

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Bootstrap only a new reader, never the publisher or the AirPlay connection.
const rtspJoinMaxBytes = 64 << 20

func h264JoinPayload(sdp []byte) (byte, error) {
	bad := errors.New("RTSP join requires unambiguous H264 with SDP SPS/PPS")
	if len(sdp) > 64<<10 {
		return 0, bad
	}
	counts := map[string]int{}
	video := false
	pt := ""
	fmtp := map[string]string{}
	for _, line := range strings.Split(string(sdp), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=") {
			fields := strings.Fields(line)
			if len(fields) < 4 {
				return 0, bad
			}
			video = fields[0] == "m=video"
			for _, p := range fields[3:] {
				counts[p]++
			}
		}
		if video && strings.HasPrefix(line, "a=rtpmap:") {
			fields := strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))
			if len(fields) == 2 && strings.EqualFold(fields[1], "H264/90000") {
				if pt != "" {
					return 0, bad
				}
				pt = fields[0]
			}
		}
		if video && strings.HasPrefix(line, "a=fmtp:") {
			key, value, ok := strings.Cut(strings.TrimPrefix(line, "a=fmtp:"), " ")
			if ok {
				fmtp[key] = value
			}
		}
	}
	value, err := strconv.ParseUint(pt, 10, 7)
	if err != nil || counts[pt] != 1 {
		return 0, bad
	}
	mode, sps, pps := false, false, false
	for _, part := range strings.Split(fmtp[pt], ";") {
		key, val, _ := strings.Cut(strings.TrimSpace(part), "=")
		if key == "packetization-mode" {
			mode = val == "1"
		}
		if key == "sprop-parameter-sets" {
			for _, encoded := range strings.Split(val, ",") {
				nal, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
				if err != nil || len(nal) < 2 || nal[0]&128 != 0 {
					return 0, bad
				}
				sps = sps || nal[0]&31 == 7
				pps = pps || nal[0]&31 == 8
			}
		}
	}
	if !mode || !sps || !pps {
		return 0, bad
	}
	return byte(value), nil
}

func rtspJoinChannel(response []byte) (byte, bool) {
	for _, line := range strings.Split(string(response), "\r\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(key, "Transport") {
			continue
		}
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "RTP/AVP/TCP;") {
			return 0, false
		}
		for _, part := range strings.Split(value, ";") {
			key, pair, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok || !strings.EqualFold(key, "interleaved") {
				continue
			}
			a, b, ok := strings.Cut(pair, "-")
			x, e1 := strconv.ParseUint(a, 10, 8)
			y, e2 := strconv.ParseUint(b, 10, 8)
			return byte(x), ok && e1 == nil && e2 == nil && x != y
		}
	}
	return 0, false
}

// Control replies can be interleaved with RTP. Bound their headers and bodies.
func readRTSPJoinPacket(src *bufio.Reader) ([]byte, error) {
	first, err := src.Peek(1)
	if err != nil {
		return nil, err
	}
	if first[0] == '$' {
		header := make([]byte, 4)
		if _, err = io.ReadFull(src, header); err != nil {
			return nil, err
		}
		packet := make([]byte, 4+int(binary.BigEndian.Uint16(header[2:])))
		copy(packet, header)
		_, err = io.ReadFull(src, packet[4:])
		return packet, err
	}
	var header []byte
	for len(header) < 64<<10 {
		b, err := src.ReadByte()
		if err != nil {
			return nil, err
		}
		header = append(header, b)
		if bytes.HasSuffix(header, []byte("\r\n\r\n")) {
			length := 0
			for _, line := range strings.Split(string(header), "\r\n") {
				key, value, ok := strings.Cut(line, ":")
				if ok && strings.EqualFold(key, "Content-Length") {
					length, err = strconv.Atoi(strings.TrimSpace(value))
					if err != nil || length < 0 || length > 64<<10 {
						return nil, errors.New("invalid RTSP control length")
					}
				}
			}
			body := make([]byte, length)
			_, err = io.ReadFull(src, body)
			return append(header, body...), err
		}
	}
	return nil, errors.New("RTSP control header exceeds join limit")
}

type h264JoinAU struct {
	idr bool
	fu  byte
}

func (a *h264JoinAU) nal(n []byte) bool {
	if len(n) < 1 || n[0]&128 != 0 {
		return false
	}
	typ := n[0] & 31
	if typ == 28 {
		if len(n) < 3 || n[1]&32 != 0 || n[1]&31 != 5 {
			return false
		}
		start, end := n[1]&128 != 0, n[1]&64 != 0
		if start && end {
			return false
		}
		header := n[0]&0xe0 | n[1]&31
		if start {
			if a.fu != 0 {
				return false
			}
			a.fu = header
			a.idr = true
		} else if a.fu != header {
			return false
		}
		if end {
			a.fu = 0
		}
		return true
	}
	if a.fu != 0 {
		return false
	}
	if typ == 24 {
		n = n[1:]
		if len(n) == 0 {
			return false
		}
		for len(n) > 0 {
			if len(n) < 2 {
				return false
			}
			size := int(binary.BigEndian.Uint16(n))
			n = n[2:]
			if size == 0 || size > len(n) || n[0]&31 >= 24 || !a.nal(n[:size]) {
				return false
			}
			n = n[size:]
		}
		return true
	}
	if typ == 5 {
		a.idr = true
		return len(n) > 1
	}
	return typ >= 6 && typ <= 9
}

// Pass other tracks unchanged; retain video only until one complete IDR AU.
// The first timestamp is always discarded: its earlier slices may be missing.
func copyUntilH264IDR(dst io.Writer, src *bufio.Reader, payloadType byte, rtpChannels map[byte]bool, maxBytes int) error {
	var seen, previousMarker, valid bool
	var timestamp, ssrc uint32
	var sequence uint16
	var pending []byte
	var au h264JoinAU
	for {
		packet, err := readRTSPJoinPacket(src)
		if err != nil {
			return err
		}
		if packet[0] != '$' || !rtpChannels[packet[1]] {
			if _, err = dst.Write(packet); err != nil {
				return err
			}
			continue
		}
		rtp := packet[4:]
		if len(rtp) < 12 || rtp[0]>>6 != 2 {
			return errors.New("invalid RTP header during join")
		}
		if rtp[1]&127 != payloadType {
			if _, err = dst.Write(packet); err != nil {
				return err
			}
			continue
		}
		offset := 12 + 4*int(rtp[0]&15)
		if offset > len(rtp) {
			return errors.New("invalid RTP CSRC")
		}
		if rtp[0]&16 != 0 {
			if offset+4 > len(rtp) {
				return errors.New("invalid RTP extension")
			}
			offset += 4 + 4*int(binary.BigEndian.Uint16(rtp[offset+2:offset+4]))
		}
		end := len(rtp)
		if rtp[0]&32 != 0 {
			padding := int(rtp[end-1])
			if padding == 0 {
				return errors.New("invalid RTP padding")
			}
			end -= padding
		}
		if offset >= end {
			return errors.New("empty RTP H264 payload")
		}
		seq, ts, source := binary.BigEndian.Uint16(rtp[2:4]), binary.BigEndian.Uint32(rtp[4:8]), binary.BigEndian.Uint32(rtp[8:12])
		marker := rtp[1]&128 != 0
		if !seen || source != ssrc || ts != timestamp {
			valid = seen && source == ssrc && previousMarker && seq == sequence+1
			pending = pending[:0]
			au = h264JoinAU{}
		} else if seq != sequence+1 || previousMarker {
			valid = false
			pending = pending[:0]
		}
		seen = true
		sequence = seq
		timestamp = ts
		ssrc = source
		previousMarker = marker
		if !valid {
			continue
		}
		if !au.nal(rtp[offset:end]) {
			valid = false
			pending = pending[:0]
			continue
		}
		if len(packet) > maxBytes-len(pending) {
			return errors.New("H264 join AU exceeds memory limit")
		}
		pending = append(pending, packet...)
		if marker {
			if au.idr && au.fu == 0 {
				_, err = io.Copy(dst, bytes.NewReader(pending))
				return err
			}
			valid = false
			pending = pending[:0]
		}
	}
}
