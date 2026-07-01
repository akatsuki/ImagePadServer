package video

import (
	"io"
	"math"
	"net/http"
	"strconv"
	"time"
)

type NetworkMeasurement struct {
	DownloadMbps int `json:"downloadMbps"`
	UploadMbps   int `json:"uploadMbps"`
}

func MeasureNetwork() NetworkMeasurement {
	return NetworkMeasurement{
		UploadMbps: MeasureNetworkUploadMbps(),
	}
}

func MeasureNetworkMbps() int {
	return MeasureNetworkDownloadMbps()
}

func MeasureNetworkDownloadMbps() int {
	const bytesToMeasure = 10_000_000
	rawURL := "https://speed.cloudflare.com/__down?bytes=" + strconv.Itoa(bytesToMeasure)
	client := http.Client{Timeout: 30 * time.Second}
	start := time.Now()
	resp, err := client.Get(rawURL)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0
	}
	written, err := io.Copy(io.Discard, io.LimitReader(resp.Body, bytesToMeasure))
	return mbpsFromBytes(written, start, err)
}

func MeasureNetworkUploadMbps() int {
	const bytesToMeasure = 40_000_000
	client := http.Client{Timeout: 90 * time.Second}
	reader := io.LimitReader(zeroReader{}, bytesToMeasure)
	req, err := http.NewRequest(http.MethodPost, "https://speed.cloudflare.com/__up", reader)
	if err != nil {
		return 0
	}
	req.ContentLength = bytesToMeasure
	req.Header.Set("Content-Type", "application/octet-stream")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0
	}
	return mbpsFromBytes(bytesToMeasure, start, nil)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func mbpsFromBytes(written int64, start time.Time, err error) int {
	if err != nil || written <= 0 {
		return 0
	}
	seconds := time.Since(start).Seconds()
	if seconds <= 0 {
		return 0
	}
	mbps := int(math.Round(float64(written*8) / seconds / 1000 / 1000))
	if mbps < 1 {
		return 1
	}
	return mbps
}
