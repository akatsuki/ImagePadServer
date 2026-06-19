package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"

	"gonum.org/v1/gonum/dsp/fourier"
)

const (
	fftWindowSize   = 2048
	frameAdvance    = 1600
	sampleRate      = 48000
	envelopeSamples = 1000
	onsetHopSamples = 480  // 10 ms at 48 kHz
	onsetRate       = sampleRate / onsetHopSamples // 100 Hz
)

func SelectMoodPalette(features AudioFeatures) (startHex, endHex string) {
	switch {
	case features.BPM >= 120:
		return "#FF4500", "#8B0000"
	case features.LowFrequencyRatio >= 0.4:
		return "#1E90FF", "#00008B"
	case features.SpectralCentroid >= 3000:
		return "#FFD700", "#FF8C00"
	case features.IntegratedLUFS >= -14:
		return "#98FB98", "#006400"
	default:
		return "#9370DB", "#4B0082"
	}
}

func AnalyzeAudio(ctx context.Context, ffmpeg, sourcePath string) (AudioAnalysis, error) {
	pcm, err := decodeToPCM(ctx, ffmpeg, sourcePath)
	if err != nil {
		return AudioAnalysis{}, fmt.Errorf("decode: %w", err)
	}

	duration := float64(len(pcm)) / float64(sampleRate*2)
	if duration <= 0 {
		return AudioAnalysis{}, fmt.Errorf("zero duration after decode")
	}

	totalFrames := int(math.Ceil(duration * 30))
	if totalFrames < 1 {
		totalFrames = 1
	}

	frames := make([]AudioFrame, totalFrames)
	fft := fourier.NewFFT(fftWindowSize)

	for i := 0; i < totalFrames; i++ {
		center := i * frameAdvance
		window := make([]float64, fftWindowSize)
		for j := 0; j < fftWindowSize; j++ {
			s := center + j - fftWindowSize/2
			if s >= 0 && s < len(pcm) {
				hann := 0.5 * (1 - math.Cos(2*math.Pi*float64(j)/float64(fftWindowSize-1)))
				window[j] = float64(pcm[s]) * hann
			}
		}
		spectrum := fft.Coefficients(nil, window)
		frames[i].Spectrum24 = mapBands(spectrum)
	}

	frames = smoothBands(frames)

	features := computeFeatures(pcm, sampleRate)

	lufs, err := extractLUFS(ctx, ffmpeg, sourcePath)
	if err == nil {
		features.IntegratedLUFS = lufs
	} else if features.IntegratedLUFS == 0 {
		features.IntegratedLUFS = -70
	}

	analysis := AudioAnalysis{
		FPS:      30,
		Duration: duration,
		Frames:   frames,
		Features: features,
	}

	return analysis, nil
}

func decodeToPCM(ctx context.Context, ffmpeg, sourcePath string) ([]int16, error) {
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-v", "error",
		"-i", sourcePath,
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-ac", "2",
		"-",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode: %w\n%s", err, stderr.String())
	}
	pcm := make([]int16, out.Len()/2)
	if err := binary.Read(&out, binary.LittleEndian, &pcm); err != nil {
		return nil, fmt.Errorf("read pcm: %w", err)
	}
	return pcm, nil
}

func mapBands(spectrum []complex128) [24]float64 {
	var bands [24]float64
	nyquist := float64(sampleRate) / 2
	magnitudes := make([]float64, len(spectrum))
	for i := range spectrum {
		magnitudes[i] = cmag(spectrum[i])
	}
	for b := 0; b < 24; b++ {
		lo := 20.0 * math.Pow(1000.0, float64(b)/24.0)
		hi := 20.0 * math.Pow(1000.0, float64(b+1)/24.0)
		loBin := int(lo / nyquist * float64(len(spectrum)))
		hiBin := int(hi / nyquist * float64(len(spectrum)))
		if loBin < 1 {
			loBin = 1
		}
		if hiBin > len(magnitudes) {
			hiBin = len(magnitudes)
		}
		if loBin >= hiBin {
			continue
		}
		var sum float64
		for j := loBin; j < hiBin; j++ {
			sum += magnitudes[j]
		}
		bands[b] = sum / float64(hiBin-loBin)
	}
	maxBand := 0.0
	for _, v := range bands {
		if v > maxBand {
			maxBand = v
		}
	}
	if maxBand > 0 {
		for i := range bands {
			bands[i] /= maxBand
		}
	}
	return bands
}

func cmag(c complex128) float64 {
	r := real(c)
	im := imag(c)
	return math.Sqrt(r*r + im*im)
}

func smoothBands(frames []AudioFrame) []AudioFrame {
	if len(frames) == 0 {
		return frames
	}
	smoothed := make([]AudioFrame, len(frames))
	smoothed[0] = frames[0]
	for i := 1; i < len(frames); i++ {
		for j := 0; j < 24; j++ {
			prev := smoothed[i-1].Spectrum24[j]
			curr := frames[i].Spectrum24[j]
			if curr > prev {
				smoothed[i].Spectrum24[j] = prev + 0.65*(curr-prev)
			} else {
				smoothed[i].Spectrum24[j] = prev + 0.18*(curr-prev)
			}
		}
	}
	return smoothed
}

func computeFeatures(pcm []int16, rate int) AudioFeatures {
	features := AudioFeatures{}
	totalSamples := len(pcm)

	if totalSamples < rate {
		return features
	}

	features.SpectralCentroid = computeSpectralCentroid(pcm, rate)
	features.LowFrequencyRatio = computeLowFrequencyRatio(pcm, rate)
	features.BPM = computeBPM(pcm, rate)
	features.LoudnessEnvelope = computeLoudnessEnvelope(pcm, envelopeSamples)
	features.Fingerprint64 = computeFingerprint(pcm, rate)
	clampFeatures(&features)
	return features
}

func computeSpectralCentroid(pcm []int16, rate int) float64 {
	fft := fourier.NewFFT(fftWindowSize)
	window := make([]float64, fftWindowSize)
	for i := 0; i < fftWindowSize && i < len(pcm); i++ {
		hann := 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(fftWindowSize-1)))
		window[i] = float64(pcm[i]) * hann
	}
	spectrum := fft.Coefficients(nil, window)
	var weightedSum, totalMag float64
	nyquist := float64(rate) / 2
	for i := 1; i < len(spectrum); i++ {
		mag := cmag(spectrum[i])
		freq := float64(i) / float64(len(spectrum)) * nyquist
		weightedSum += mag * freq
		totalMag += mag
	}
	if totalMag > 0 {
		return weightedSum / totalMag
	}
	return 0
}

func computeLowFrequencyRatio(pcm []int16, rate int) float64 {
	fft := fourier.NewFFT(fftWindowSize)
	window := make([]float64, fftWindowSize)
	for i := 0; i < fftWindowSize && i < len(pcm); i++ {
		hann := 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(fftWindowSize-1)))
		window[i] = float64(pcm[i]) * hann
	}
	spectrum := fft.Coefficients(nil, window)
	nyquist := float64(rate) / 2
	var lowMag, totalMag float64
	for i := 1; i < len(spectrum); i++ {
		mag := cmag(spectrum[i])
		freq := float64(i) / float64(len(spectrum)) * nyquist
		if freq <= 250 {
			lowMag += mag
		}
		totalMag += mag
	}
	if totalMag > 0 {
		return lowMag / totalMag
	}
	return 0
}

func computeBPM(pcm []int16, rate int) float64 {
	mono := make([]float64, len(pcm)/2)
	for i := range mono {
		mono[i] = float64(pcm[i*2]+pcm[i*2+1]) / 2
	}

	// Build onset flux: one value per onsetHopSamples (10 ms at 48 kHz)
	nOnset := len(mono) / onsetHopSamples
	if nOnset < 2 {
		return 0
	}
	flux := make([]float64, nOnset)
	for i := range flux {
		var sum float64
		for j := 0; j < onsetHopSamples; j++ {
			idx := i*onsetHopSamples + j
			if idx < len(mono)-1 {
				diff := mono[idx+1] - mono[idx]
				if diff > 0 {
					sum += diff
				}
			}
		}
		flux[i] = sum
	}

	// Lag bounds in onset-frame units (not PCM samples)
	minLag := onsetRate * 60 / 200 // 30 for BPM 200
	maxLag := onsetRate * 60 / 60  // 100 for BPM 60

	bestCorr := -1.0
	bestLag := 0
	for lag := minLag; lag <= maxLag && lag < len(flux); lag++ {
		var sum, sumA, sumB float64
		n := len(flux) - lag
		if n <= 0 {
			continue
		}
		for i := 0; i < n; i++ {
			sum += flux[i] * flux[i+lag]
			sumA += flux[i] * flux[i]
			sumB += flux[i+lag] * flux[i+lag]
		}
		denom := math.Sqrt(sumA * sumB)
		if denom > 0 {
			corr := sum / denom
			// Weight toward shorter lags to prefer higher BPM
			// (avoids picking 60 BPM when 180 BPM is the true tempo)
			weighted := corr * (1.0 - 0.4*float64(lag-minLag)/float64(maxLag-minLag))
			if weighted > bestCorr {
				bestCorr = weighted
				bestLag = lag
			}
		}
	}

	if bestCorr <= 0 {
		return 0
	}
	// BPM = 60 * onsetRate / bestLag
	return 60.0 * float64(onsetRate) / float64(bestLag)
}

func computeLoudnessEnvelope(pcm []int16, n int) [1000]float64 {
	var envelope [1000]float64
	chunkSize := len(pcm) / n
	if chunkSize < 1 {
		return envelope
	}
	for i := 0; i < n && i < 1000; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(pcm) {
			end = len(pcm)
		}
		if start >= end {
			continue
		}
		var sumSq float64
		for j := start; j < end; j++ {
			f := float64(pcm[j]) / 32768.0
			sumSq += f * f
		}
		rms := math.Sqrt(sumSq / float64(end-start))
		envelope[i] = rms
	}
	return envelope
}

func computeFingerprint(pcm []int16, rate int) [64]float64 {
	var fingerprint [64]float64
	if len(pcm) < rate {
		return fingerprint
	}

	fft := fourier.NewFFT(fftWindowSize)
	step := len(pcm) / 64
	if step < fftWindowSize {
		step = fftWindowSize
	}

	for b := 0; b < 64; b++ {
		center := b * step
		window := make([]float64, fftWindowSize)
		for j := 0; j < fftWindowSize && center+j < len(pcm); j++ {
			hann := 0.5 * (1 - math.Cos(2*math.Pi*float64(j)/float64(fftWindowSize-1)))
			window[j] = float64(pcm[center+j]) * hann
		}
		spectrum := fft.Coefficients(nil, window)

		nyquist := float64(rate) / 2
		lo := 20.0 * math.Pow(1000.0, float64(b)/64.0)
		hi := 20.0 * math.Pow(1000.0, float64(b+1)/64.0)
		loBin := int(lo / nyquist * float64(len(spectrum)/2+1))
		hiBin := int(hi / nyquist * float64(len(spectrum)/2+1))
		if loBin < 1 {
			loBin = 1
		}
		if hiBin > len(spectrum)/2 {
			hiBin = len(spectrum) / 2
		}
		var sum float64
		if loBin < hiBin && loBin <= len(spectrum)/2 {
			for j := loBin; j < hiBin && j < len(spectrum)/2; j++ {
				idx := j
				if idx >= len(spectrum) {
					break
				}
				sum += cmag(spectrum[idx])
			}
			fingerprint[b] = sum / float64(hiBin-loBin)
		}
	}

	maxVal := 0.0
	for _, v := range fingerprint {
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal > 0 {
		for i := range fingerprint {
			fingerprint[i] /= maxVal
		}
	}
	return fingerprint
}

var lufsPattern = regexp.MustCompile(`I:\s*(-?\d+\.?\d*)\s*LUFS`)

func extractLUFS(ctx context.Context, ffmpeg, sourcePath string) (float64, error) {
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-v", "info",
		"-i", sourcePath,
		"-af", "ebur128=metadata=1",
		"-f", "null",
		"NUL",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ebur128: %w", err)
	}
	matches := lufsPattern.FindStringSubmatch(stderr.String())
	if len(matches) < 2 {
		return 0, fmt.Errorf("no LUFS in output")
	}
	val, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("parse LUFS: %w", err)
	}
	return val, nil
}

func clampFeatures(f *AudioFeatures) {
	if f.BPM < 0 {
		f.BPM = 0
	}
	if f.BPM > 300 {
		f.BPM = 300
	}
	if f.IntegratedLUFS < -70 {
		f.IntegratedLUFS = -70
	}
	if f.IntegratedLUFS > 0 {
		f.IntegratedLUFS = 0
	}
	if f.LowFrequencyRatio < 0 {
		f.LowFrequencyRatio = 0
	}
	if f.LowFrequencyRatio > 1 {
		f.LowFrequencyRatio = 1
	}
	if f.SpectralCentroid < 0 {
		f.SpectralCentroid = 0
	}
}
