package video

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStreamShowwavesPCMHistoryPreservesLoudnormLatency(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not available")
	}
	fixture := filepath.Join("..", "..", "artifacts", "strict-fixtures", "no-artwork-fallback-1s.wav")
	var histories [][]uint16
	err = StreamShowwavesPCMHistoryFrames(context.Background(), ffmpeg, AudioRenderInput{
		SourcePath: fixture,
		Kind:       SourceMusic,
	}, 376, 5, func(_ int, samples []uint16) error {
		histories = append(histories, append([]uint16(nil), samples...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(histories) != 5 || len(histories[0]) != 376*18*2 {
		t.Fatalf("history dimensions = %d x %d", len(histories), len(histories[0]))
	}
	for i, sample := range histories[0][:showwavesPCMSamplesPerTick*2] {
		if sample != 32768 {
			t.Fatalf("prior tick sample %d = %d, want centre-biased silence", i, sample)
		}
	}
	nonSilent := false
	for _, sample := range histories[1][showwavesPCMSamplesPerTick*2:] {
		if sample != 32768 {
			nonSilent = true
			break
		}
	}
	if !nonSilent {
		t.Fatal("current-tick prefix is unexpectedly silent")
	}
	for frame := 1; frame <= showwavesPCMLoudnormDelayTicks; frame++ {
		for i := range histories[0] {
			if histories[frame][i] != histories[0][i] {
				t.Fatalf("delayed frame %d differs at sample %d", frame, i)
			}
		}
	}
	different := false
	for i := range histories[0] {
		if histories[showwavesPCMLoudnormDelayTicks+1][i] != histories[0][i] {
			different = true
			break
		}
	}
	if !different {
		t.Fatal("first post-delay waveform did not advance")
	}
}

func TestStreamShowwavesPCMHistoryPadsAudioEOF(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not available")
	}
	fixture := filepath.Join("..", "..", "artifacts", "strict-fixtures", "no-artwork-fallback-1s.wav")
	frames := 0
	err = StreamWaveformPCMHistoryFrames(context.Background(), ffmpeg, AudioRenderInput{
		SourcePath: fixture,
		Kind:       SourceMusic,
	}, 376, 31, func(_ int, samples []uint16) error {
		frames++
		if len(samples) != 376*18*2 {
			t.Fatalf("padded history values = %d", len(samples))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("audio EOF must be padded through the requested video duration: %v", err)
	}
	if frames != 31 {
		t.Fatalf("padded callback count = %d, want 31", frames)
	}
}

func TestGPUShowwavesFirstHistoryOnlyDrawsCurrentPrefix(t *testing.T) {
	exe := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if exe == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not configured")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not available")
	}
	fixture := filepath.Join("..", "..", "artifacts", "strict-fixtures", "no-artwork-fallback-1s.wav")
	var history []uint16
	err = StreamShowwavesPCMHistoryFrames(context.Background(), ffmpeg, AudioRenderInput{SourcePath: fixture, Kind: SourceMusic}, 376, 1, func(_ int, samples []uint16) error {
		history = append([]uint16(nil), samples...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scene := CanonicalMusicScene(AudioRenderInput{Analysis: AudioAnalysis{Duration: 1, FPS: 30}}, 0, 0)
	scene.Feature.WaveformQ16 = history
	frame, err := ProbeGPUSceneRawWaveform(context.Background(), exe, 640, 360, &scene)
	if err != nil {
		t.Fatal(err)
	}
	minX, maxX := 640, -1
	for y := 0; y < 360; y++ {
		for x := 0; x < 640; x++ {
			if frame.Payload[(y*640+x)*4+3] != 0 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
			}
		}
	}
	if maxX < 0 {
		t.Fatal("native waveform probe rendered no current-tick prefix")
	}
	if minX < 216+350 || maxX >= 216+376 {
		t.Fatalf("native first-frame waveform bounds = [%d,%d], want within [%d,%d)", minX, maxX, 216+350, 216+376)
	}
}

func TestGPUShowwavesMatchesFFmpegReference(t *testing.T) {
	exe := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if exe == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not configured")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not available")
	}
	fixture := filepath.Join("..", "..", "artifacts", "strict-fixtures", "no-artwork-fallback-1s.wav")
	layout, err := LayoutForSize(640, 360)
	if err != nil {
		t.Fatal(err)
	}
	const frames = 30
	reference := make([][]byte, frames)
	err = StreamAudioWaveFrames(context.Background(), ffmpeg, fixture, layout.Spectrum.W, layout.Spectrum.H, frames, "#7AF4FF@0.55", musicLoudnormFilter, func(index int, rgba []byte) error {
		reference[index] = append([]byte(nil), rgba...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	histories := make([][]uint16, frames)
	err = StreamShowwavesPCMHistoryFrames(context.Background(), ffmpeg, AudioRenderInput{SourcePath: fixture, Kind: SourceMusic}, layout.Spectrum.W, frames, func(index int, samples []uint16) error {
		histories[index] = append([]uint16(nil), samples...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for frameIndex := 0; frameIndex < frames; frameIndex++ {
		scene := CanonicalMusicScene(AudioRenderInput{Analysis: AudioAnalysis{Duration: 1, FPS: 30}}, uint64(frameIndex), int64(frameIndex)*(1_000_000_000/30))
		scene.Palette.Accent = [4]uint8{122, 244, 255, 255}
		scene.Feature.WaveformQ16 = histories[frameIndex]
		frame, err := ProbeGPUSceneRawWaveform(context.Background(), exe, 640, 360, &scene)
		if err != nil {
			t.Fatal(err)
		}
		var absolute uint64
		var cpuAlpha, gpuAlpha uint64
		cpuPixels, gpuPixels := 0, 0
		for y := 0; y < layout.Spectrum.H; y++ {
			for x := 0; x < layout.Spectrum.W; x++ {
				cpuOffset := (y*layout.Spectrum.W + x) * 4
				gpuOffset := (layout.Spectrum.Y+y)*int(frame.RowStride) + (layout.Spectrum.X+x)*4
				cpuA := reference[frameIndex][cpuOffset+3]
				gpuA := frame.Payload[gpuOffset+3]
				cpuAlpha += uint64(cpuA)
				gpuAlpha += uint64(gpuA)
				if cpuA != 0 {
					cpuPixels++
				}
				if gpuA != 0 {
					gpuPixels++
				}
				for channel := 0; channel < 4; channel++ {
					delta := int(reference[frameIndex][cpuOffset+channel]) - int(frame.Payload[gpuOffset+channel])
					if delta < 0 {
						delta = -delta
					}
					absolute += uint64(delta)
				}
			}
		}
		mae := float64(absolute) / float64(layout.Spectrum.W*layout.Spectrum.H*4)
		t.Logf("frame %d waveform MAE %.6f alpha cpu=%d gpu=%d pixels cpu=%d gpu=%d", frameIndex, mae, cpuAlpha, gpuAlpha, cpuPixels, gpuPixels)
		if mae > 0.5 {
			t.Errorf("frame %d native waveform MAE = %.6f, want <= 0.5", frameIndex, mae)
		}
	}
}
