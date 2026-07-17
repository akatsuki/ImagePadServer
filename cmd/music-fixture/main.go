package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type manifest struct {
	Fixtures []fixture `json:"fixtures"`
}
type fixture struct {
	ID              string            `json:"id"`
	Seed            int64             `json:"seed"`
	Duration        float64           `json:"duration_s"`
	Metadata        map[string]string `json:"metadata"`
	Artwork         *string           `json:"artwork"`
	FeatureProfile  string            `json:"feature_profile"`
	BoundarySamples []float64         `json:"boundary_samples"`
	Output          *struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"output"`
	CancelAt *float64 `json:"cancel_at_s"`
}

func main() {
	manifestPath := flag.String("manifest", "testdata/music-render/manifest.json", "fixture manifest")
	out := flag.String("out", ".tmp/music-fixtures", "output directory")
	flag.Parse()
	data, err := os.ReadFile(*manifestPath)
	if err != nil {
		panic(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		panic(err)
	}
	files := make([]map[string]any, 0, len(m.Fixtures))
	for _, f := range m.Fixtures {
		if err := writeFixture(*out, f); err != nil {
			panic(err)
		}
		path := filepath.Join(*out, f.ID+".wav")
		files = append(files, fixtureEvidence(path, f))
	}
	sum := sha256.Sum256(data)
	ffmpegVersion := "unavailable"
	if ffprobe, err := exec.LookPath("ffprobe"); err == nil {
		if out, err := exec.Command(ffprobe, "-version").Output(); err == nil {
			line := string(out)
			if i := len(line); i > 0 {
				ffmpegVersion = line[:min(i, 120)]
			}
		}
	}
	report := map[string]any{
		"manifest_sha256": fmt.Sprintf("%x", sum),
		"fixtures":        len(m.Fixtures),
		"generator":       "cmd/music-fixture",
		"go_version":      runtime.Version(),
		"ffprobe_version": ffmpegVersion,
		"files":           files,
	}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	_ = os.WriteFile(filepath.Join(*out, "analysis-report.json"), append(encoded, '\n'), 0644)
}

func fixtureEvidence(path string, f fixture) map[string]any {
	evidence := map[string]any{
		"id": f.ID, "path": filepath.Base(path), "expected_duration_s": f.Duration,
		"expected_frame_count": int(math.Ceil(f.Duration * 30)),
		"metadata":             f.Metadata, "artwork": f.Artwork, "feature_profile": f.FeatureProfile,
		"boundary_samples": f.BoundarySamples, "cancel_at_s": f.CancelAt,
	}
	if f.Output != nil {
		evidence["output"] = f.Output
	}
	if data, err := os.ReadFile(path); err == nil {
		sum := sha256.Sum256(data)
		evidence["sha256"] = fmt.Sprintf("%x", sum)
		evidence["bytes"] = len(data)
	}
	if ffprobe, err := exec.LookPath("ffprobe"); err == nil {
		if out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name,sample_rate,channels,duration", "-of", "json", path).Output(); err == nil {
			var probe any
			if json.Unmarshal(out, &probe) == nil {
				evidence["ffprobe"] = probe
			}
		}
	}
	return evidence
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func writeFixture(dir string, f fixture) error {
	n := int(math.Round(f.Duration * 48000))
	path := filepath.Join(dir, f.ID+".wav")
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	dataBytes := n * 2 * 2
	h := make([]byte, 44)
	copy(h, []byte("RIFF"))
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataBytes))
	copy(h[8:], []byte("WAVEfmt "))
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 2)
	binary.LittleEndian.PutUint32(h[24:], 48000)
	binary.LittleEndian.PutUint32(h[28:], 48000*4)
	binary.LittleEndian.PutUint16(h[32:], 4)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], []byte("data"))
	binary.LittleEndian.PutUint32(h[40:], uint32(dataBytes))
	if _, err := file.Write(h); err != nil {
		return err
	}
	freq := 220.0 + float64(f.Seed%7)*31
	for i := 0; i < n; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*freq*float64(i)/48000))
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(v))
		if _, err := file.Write(b[:]); err != nil {
			return err
		}
		if _, err := file.Write(b[:]); err != nil {
			return err
		}
	}
	return nil
}
