package xposttts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"imagepadserver/internal/xpostmodel"
)

const (
	maxJSONBytes = 4 << 20
	maxWAVBytes  = 128 << 20
)

type Client struct {
	base *url.URL
	http *http.Client
}

type Speaker struct {
	UUID   string  `json:"speaker_uuid"`
	Name   string  `json:"name"`
	Styles []Style `json:"styles"`
}

type Style struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// NewClient creates a client bound to a loopback VOICEVOX-compatible engine.
func NewClient(engineURL string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(engineURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("engine URL must be an HTTP(S) loopback origin")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("engine URL host must be loopback")
	}
	u.Path = ""
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	hc := &http.Client{
		Transport: transport,
		Timeout:   45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || !sameLoopbackOrigin(u, req.URL) {
				return errors.New("engine redirect rejected")
			}
			return nil
		},
	}
	return &Client{base: u, http: hc}, nil
}

func sameLoopbackOrigin(base, target *url.URL) bool {
	return target.Scheme == base.Scheme && strings.EqualFold(target.Host, base.Host) && (strings.EqualFold(target.Hostname(), "localhost") || net.ParseIP(target.Hostname()) != nil && net.ParseIP(target.Hostname()).IsLoopback())
}

func (c *Client) endpoint(path string) string { return strings.TrimRight(c.base.String(), "/") + path }

func (c *Client) Speakers(ctx context.Context) ([]Speaker, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/speakers"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list engine speakers: %w", err)
	}
	defer resp.Body.Close()
	data, err := readResponse(resp, maxJSONBytes)
	if err != nil {
		return nil, err
	}
	var speakers []Speaker
	if err := json.Unmarshal(data, &speakers); err != nil {
		return nil, fmt.Errorf("decode engine speakers: %w", err)
	}
	return speakers, nil
}

func (c *Client) Synthesize(ctx context.Context, text string, voice xpostmodel.Voice, outPath string) (xpostmodel.Speech, error) {
	if strings.TrimSpace(text) == "" {
		return xpostmodel.Speech{}, nil
	}
	if voice.StyleID < 0 {
		return xpostmodel.Speech{}, errors.New("voice style ID must be non-negative")
	}
	speed := voice.Speed
	if speed == 0 {
		speed = 1
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < .5 || speed > 2 {
		return xpostmodel.Speech{}, errors.New("voice speed must be between 0.5 and 2")
	}
	if strings.TrimSpace(outPath) == "" {
		return xpostmodel.Speech{}, errors.New("output path is required")
	}

	queryURL := c.endpoint("/audio_query") + "?speaker=" + strconv.Itoa(voice.StyleID) + "&text=" + url.QueryEscape(text)
	queryReq, err := http.NewRequestWithContext(ctx, http.MethodPost, queryURL, nil)
	if err != nil {
		return xpostmodel.Speech{}, err
	}
	queryResp, err := c.http.Do(queryReq)
	if err != nil {
		return xpostmodel.Speech{}, fmt.Errorf("create audio query: %w", err)
	}
	queryData, readErr := readResponse(queryResp, maxJSONBytes)
	queryResp.Body.Close()
	if readErr != nil {
		return xpostmodel.Speech{}, readErr
	}
	var query map[string]json.RawMessage
	if err := json.Unmarshal(queryData, &query); err != nil {
		return xpostmodel.Speech{}, fmt.Errorf("decode audio query: %w", err)
	}
	query["speedScale"], _ = json.Marshal(speed)
	body, err := json.Marshal(query)
	if err != nil {
		return xpostmodel.Speech{}, err
	}
	synthURL := c.endpoint("/synthesis") + "?speaker=" + strconv.Itoa(voice.StyleID)
	synthReq, err := http.NewRequestWithContext(ctx, http.MethodPost, synthURL, bytes.NewReader(body))
	if err != nil {
		return xpostmodel.Speech{}, err
	}
	synthReq.Header.Set("Content-Type", "application/json")
	synthResp, err := c.http.Do(synthReq)
	if err != nil {
		return xpostmodel.Speech{}, fmt.Errorf("synthesize speech: %w", err)
	}
	wav, readErr := readResponse(synthResp, maxWAVBytes)
	synthResp.Body.Close()
	if readErr != nil {
		return xpostmodel.Speech{}, readErr
	}
	info, err := inspectWAV(wav)
	if err != nil {
		return xpostmodel.Speech{}, err
	}

	if err := writeAtomic(outPath, wav); err != nil {
		return xpostmodel.Speech{}, err
	}
	return xpostmodel.Speech{Path: outPath, Duration: float64(info.samples) / float64(info.sampleRate), SampleRate: info.sampleRate, Channels: info.channels, Samples: info.samples}, nil
}

func readResponse(resp *http.Response, limit int64) ([]byte, error) {
	defer io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("engine returned HTTP %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read engine response: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("engine response exceeds size limit")
	}
	return data, nil
}

type wavInfo struct {
	sampleRate, channels int
	samples              int64
}

func inspectWAV(b []byte) (wavInfo, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" || uint64(binary.LittleEndian.Uint32(b[4:8]))+8 != uint64(len(b)) {
		return wavInfo{}, errors.New("engine returned invalid WAV container")
	}
	var rate uint32
	var channels, bits, format, blockAlign uint16
	var dataSize uint32
	haveFmt, haveData := false, false
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := binary.LittleEndian.Uint32(b[off+4 : off+8])
		start, end := uint64(off+8), uint64(off+8)+uint64(size)
		if end > uint64(len(b)) {
			return wavInfo{}, errors.New("engine returned truncated WAV chunk")
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return wavInfo{}, errors.New("engine returned invalid WAV format chunk")
			}
			format = binary.LittleEndian.Uint16(b[start:])
			channels = binary.LittleEndian.Uint16(b[start+2:])
			rate = binary.LittleEndian.Uint32(b[start+4:])
			blockAlign = binary.LittleEndian.Uint16(b[start+12:])
			bits = binary.LittleEndian.Uint16(b[start+14:])
			haveFmt = true
		case "data":
			dataSize, haveData = size, true
		}
		off = int(end + uint64(size&1))
	}
	if !haveFmt || !haveData || format != 1 || bits != 16 || channels == 0 || rate == 0 || blockAlign == 0 || uint64(blockAlign) != uint64(channels)*2 || dataSize%uint32(blockAlign) != 0 {
		return wavInfo{}, errors.New("engine WAV must contain 16-bit PCM audio")
	}
	return wavInfo{sampleRate: int(rate), channels: int(channels), samples: int64(dataSize / uint32(blockAlign))}, nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".speech-*.tmp")
	if err != nil {
		return fmt.Errorf("create speech output: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write speech output: %w", err)
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync speech output: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close speech output: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publish speech output: %w", err)
	}
	return nil
}
