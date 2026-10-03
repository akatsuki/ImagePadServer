// Package xpostgpu hosts the persistent common-WGPU X video compositor.
package xpostgpu

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

const (
	maxControlBytes   = 8 << 20
	maxAssets         = 4096
	maxAssetDimension = 16384
	maxAssetBytes     = 128 << 20
	maxFrameBytes     = 256 << 20
	maxLayers         = 4096
	maxStderrBytes    = 64 << 10
)

type Asset struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Layer struct {
	AssetID string      `json:"assetId"`
	X       float32     `json:"x"`
	Y       float32     `json:"y"`
	Width   float32     `json:"width"`
	Height  float32     `json:"height"`
	Tilt    float32     `json:"tilt"`
	Opacity float32     `json:"opacity"`
	UV      *[4]float32 `json:"uv,omitempty"`
}

type Frame struct {
	Layers []Layer `json:"layers"`
}

type Update struct {
	AssetID string
	Pixels  []byte
}

type Client struct {
	Adapter string
	Backend string

	width  int
	height int
	assets map[string]int
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *lockedBuffer
	mu     sync.Mutex
	closed bool
}

type initMessage struct {
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	Assets          []Asset `json:"assets"`
	LightBackground bool    `json:"lightBackground,omitempty"`
}

type updateMetadata struct {
	UpdateID string `json:"updateID"`
	ByteLen  int    `json:"byteLen"`
}

type requestMessage struct {
	Frame  Frame           `json:"frame"`
	Update *updateMetadata `json:"update"`
}

type readyMessage struct {
	Adapter string `json:"adapter"`
	Backend string `json:"backend"`
}

type lockedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	length := len(p)
	if length >= maxStderrBytes {
		b.data = make([]byte, maxStderrBytes)
		copy(b.data, p[length-maxStderrBytes:])
		return length, nil
	}
	if overflow := len(b.data) + length - maxStderrBytes; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, p...)
	if cap(b.data) > maxStderrBytes {
		bounded := make([]byte, len(b.data), maxStderrBytes)
		copy(bounded, b.data)
		b.data = bounded
	}
	return length, nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// Start launches one sidecar and retains its WGPU device and asset textures
// until Close. Asset paths must name exact-size, tightly packed raw RGBA files.
func Start(ctx context.Context, exe string, width, height int, assets []Asset) (*Client, error) {
	return StartWithTheme(ctx, exe, width, height, assets, "dark")
}

// StartWithTheme uses the same background as the post cards, including uncovered
// media margins and CoverFlow frames. Empty theme preserves the legacy black.
func StartWithTheme(ctx context.Context, exe string, width, height int, assets []Asset, theme string) (*Client, error) {
	if theme != "" && theme != "light" && theme != "dark" {
		return nil, errors.New("invalid X compositor background theme")
	}
	if exe == "" {
		return nil, errors.New("xpost compositor executable path is empty")
	}
	if err := validateAssets(width, height, assets); err != nil {
		return nil, err
	}
	if assets == nil {
		assets = []Asset{}
	}
	cmd := exec.CommandContext(ctx, exe)
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening compositor stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening compositor stdout: %w", err)
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting xpost compositor: %w", err)
	}
	assetBytes := make(map[string]int, len(assets))
	for _, asset := range assets {
		assetBytes[asset.ID] = asset.Width * asset.Height * 4
	}
	client := &Client{width: width, height: height, assets: assetBytes, cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: stderr}
	if err := writeControl(stdin, initMessage{Width: width, Height: height, Assets: assets, LightBackground: theme == "light"}); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return nil, fmt.Errorf("sending compositor init: %w%s", err, stderrSuffix(stderr))
	}
	var ready readyMessage
	if err := readControl(client.stdout, &ready); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return nil, fmt.Errorf("waiting for compositor ready: %w%s", err, stderrSuffix(stderr))
	}
	client.Adapter, client.Backend = ready.Adapter, ready.Backend
	return client, nil
}

func (c *Client) Render(frame Frame, update *Update) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("xpost compositor client is closed")
	}
	if err := validateFrame(frame, c.assets); err != nil {
		return nil, err
	}
	if update != nil {
		expected, ok := c.assets[update.AssetID]
		if !ok || len(update.Pixels) != expected || len(update.Pixels) > maxAssetBytes {
			return nil, fmt.Errorf("update for asset %q has an unknown ID or invalid RGBA byte length", update.AssetID)
		}
	}
	encoded, err := encodeRequest(frame, update)
	if err != nil {
		return nil, err
	}
	if err := writeFramed(c.stdin, encoded); err != nil {
		return nil, fmt.Errorf("sending compositor frame: %w%s", err, stderrSuffix(c.stderr))
	}
	if update != nil {
		if err := writeAll(c.stdin, update.Pixels); err != nil {
			return nil, fmt.Errorf("sending compositor asset update: %w%s", err, stderrSuffix(c.stderr))
		}
	}
	frameBytes := make([]byte, c.width*c.height*4)
	if _, err := io.ReadFull(c.stdout, frameBytes); err != nil {
		return nil, fmt.Errorf("reading compositor RGBA frame: %w%s", err, stderrSuffix(c.stderr))
	}
	return frameBytes, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	_ = c.stdin.Close()
	if err := c.cmd.Wait(); err != nil {
		return fmt.Errorf("stopping xpost compositor: %w%s", err, stderrSuffix(c.stderr))
	}
	return nil
}

func encodeRequest(frame Frame, update *Update) ([]byte, error) {
	if frame.Layers == nil {
		frame.Layers = []Layer{}
	}
	request := requestMessage{Frame: frame}
	if update != nil {
		request.Update = &updateMetadata{UpdateID: update.AssetID, ByteLen: len(update.Pixels)}
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encoding compositor frame request: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > maxControlBytes {
		return nil, errors.New("compositor control message exceeds protocol limit")
	}
	return encoded, nil
}

func validateAssets(width, height int, assets []Asset) error {
	if width <= 0 || height <= 0 || width > maxAssetDimension || height > maxAssetDimension || int64(width)*int64(height)*4 > maxFrameBytes {
		return errors.New("canvas dimensions or output bytes exceed compositor bounds")
	}
	if len(assets) > maxAssets {
		return errors.New("asset count exceeds compositor bounds")
	}
	seen := make(map[string]struct{}, len(assets))
	var total int64
	for _, asset := range assets {
		if asset.ID == "" || len(asset.ID) > 256 || asset.Path == "" || len(asset.Path) > 32768 {
			return fmt.Errorf("asset %q has an invalid ID or raw RGBA path", asset.ID)
		}
		if _, ok := seen[asset.ID]; ok {
			return fmt.Errorf("duplicate compositor asset ID %q", asset.ID)
		}
		seen[asset.ID] = struct{}{}
		if asset.Width <= 0 || asset.Height <= 0 || asset.Width > maxAssetDimension || asset.Height > maxAssetDimension {
			return fmt.Errorf("asset %q dimensions exceed compositor bounds", asset.ID)
		}
		bytes := int64(asset.Width) * int64(asset.Height) * 4
		if bytes > maxAssetBytes || total > 512<<20-bytes {
			return errors.New("asset pixel bytes exceed compositor bounds")
		}
		total += bytes
	}
	return nil
}

func validateFrame(frame Frame, assets map[string]int) error {
	if len(frame.Layers) > maxLayers {
		return errors.New("layer count exceeds compositor bounds")
	}
	for _, layer := range frame.Layers {
		if _, ok := assets[layer.AssetID]; !ok {
			return fmt.Errorf("layer references unknown compositor asset %q", layer.AssetID)
		}
		if layer.AssetID == "" || !finite(layer.X) || !finite(layer.Y) || !finite(layer.Width) || !finite(layer.Height) || !finite(layer.Tilt) || !finite(layer.Opacity) || layer.Width <= 0 || layer.Height <= 0 || layer.Tilt < -89 || layer.Tilt > 89 || layer.Opacity < 0 || layer.Opacity > 1 {
			return fmt.Errorf("layer for asset %q has invalid geometry, tilt, or opacity", layer.AssetID)
		}
		if layer.UV != nil {
			u0, v0, u1, v1 := layer.UV[0], layer.UV[1], layer.UV[2], layer.UV[3]
			if !finite(u0) || !finite(v0) || !finite(u1) || !finite(v1) || u0 < 0 || v0 < 0 || u1 > 1 || v1 > 1 || u0 >= u1 || v0 >= v1 {
				return fmt.Errorf("layer for asset %q has invalid UV range", layer.AssetID)
			}
		}
	}
	return nil
}

func finite(value float32) bool     { return !float32NaN(value) && !float32Inf(value) }
func float32NaN(value float32) bool { return value != value }
func float32Inf(value float32) bool { return value > 3.402823466e+38 || value < -3.402823466e+38 }

func writeControl(writer io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) == 0 || len(encoded) > maxControlBytes {
		return errors.New("compositor control message exceeds protocol limit")
	}
	return writeFramed(writer, encoded)
}

func writeFramed(writer io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxControlBytes {
		return errors.New("compositor control message exceeds protocol limit")
	}
	var prefix [4]byte
	binary.LittleEndian.PutUint32(prefix[:], uint32(len(payload)))
	if err := writeAll(writer, prefix[:]); err != nil {
		return err
	}
	return writeAll(writer, payload)
}

func readControl(reader io.Reader, value any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return err
	}
	size := binary.LittleEndian.Uint32(prefix[:])
	if size == 0 || size > maxControlBytes {
		return errors.New("compositor control message exceeds protocol limit")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(reader, data); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func stderrSuffix(stderr *lockedBuffer) string {
	if text := stderr.String(); text != "" {
		return ": " + text
	}
	return ""
}
