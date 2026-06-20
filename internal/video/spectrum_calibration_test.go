package video

import (
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"sort"
	"testing"
)

// TestSpectrumCalibrationProbe measures the per-band dBFS distribution of a
// -14 LUFS reference signal through the real analysis pipeline, to calibrate
// spectrumRefDB. Skipped unless CALIBRATE_SPECTRUM is set.
func TestSpectrumCalibrationProbe(t *testing.T) {
	if os.Getenv("CALIBRATE_SPECTRUM") == "" {
		t.Skip("set CALIBRATE_SPECTRUM=1 to run spectrum calibration")
	}
	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg,
		"-v", "error",
		"-f", "lavfi",
		"-i", "anoisesrc=color=pink:duration=30:sample_rate=48000:amplitude=0.6",
		"-af", "loudnorm=I=-14:TP=-1:LRA=11",
		"-ac", "2",
		"-ar", "48000",
		"-f", "s16le",
		"-",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg pink noise: %v", err)
	}
	count := len(out) / 2
	samples := make([]int16, count)
	for i := 0; i < count; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(out[i*2:]))
	}
	az := newStreamAnalyzer()
	if err := az.ConsumeStereo(samples); err != nil {
		t.Fatal(err)
	}
	full := 32768.0 * float64(fftWindowSize) * 0.5
	var dbs []float64
	for _, f := range az.frames {
		for _, v := range f.Spectrum24 {
			if v > 0 {
				dbs = append(dbs, 20*math.Log10(v/full))
			}
		}
	}
	if len(dbs) == 0 {
		t.Fatal("no band energy collected")
	}
	sort.Float64s(dbs)
	p := func(q float64) float64 {
		idx := int(float64(len(dbs)) * q)
		if idx >= len(dbs) {
			idx = len(dbs) - 1
		}
		return dbs[idx]
	}
	t.Logf("frames=%d values=%d band dBFS: p50=%.1f p75=%.1f p90=%.1f p95=%.1f p99=%.1f max=%.1f",
		len(az.frames), len(dbs), p(0.5), p(0.75), p(0.9), p(0.95), p(0.99), dbs[len(dbs)-1])
}
