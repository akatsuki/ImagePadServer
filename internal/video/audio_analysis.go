package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"sync"

	"gonum.org/v1/gonum/dsp/fourier"
)

var ErrIncompleteSample = errors.New("incomplete audio sample")

type pcmAnalyzer interface {
	ConsumeStereo(samples []int16) error
	Finish() (AudioAnalysis, error)
}

const (
	fftWindowSize   = 2048
	frameAdvance    = 1600
	sampleRate      = 48000
	envelopeSamples = 1000
	onsetHopSamples = 480                          // 10 ms at 48 kHz
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
	az := newStreamAnalyzer()

	cmd := exec.CommandContext(ctx, ffmpeg,
		"-v", "error",
		"-i", sourcePath,
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-ac", "2",
		"-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return AudioAnalysis{}, fmt.Errorf("stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return AudioAnalysis{}, fmt.Errorf("ffmpeg start: %w", err)
	}

	readBuf := make([]byte, 65536)
	halfBuf := make([]byte, 0)
	done := make(chan error, 1)

	go func() {
		defer stdout.Close()
		for {
			n, rerr := stdout.Read(readBuf)
			if n > 0 {
				data := append(halfBuf, readBuf[:n]...)
				halfBuf = nil
				remainder := len(data) % 2
				samples := len(data) / 2
				if samples > 0 {
					end := len(data) - remainder
					pcm := make([]int16, samples)
					if err := binary.Read(bytes.NewReader(data[:end]), binary.LittleEndian, &pcm); err != nil {
						done <- fmt.Errorf("read pcm: %w", err)
						return
					}
					if err := az.ConsumeStereo(pcm); err != nil {
						done <- err
						return
					}
				}
				if remainder > 0 {
					halfBuf = append(halfBuf, data[len(data)-1])
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				done <- fmt.Errorf("read: %w", rerr)
				return
			}
		}

		if len(halfBuf) > 0 {
			done <- ErrIncompleteSample
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			return AudioAnalysis{}, fmt.Errorf("decode: %w", err)
		}
	case <-ctx.Done():
		cmd.Process.Kill()
		cmd.Wait()
		return AudioAnalysis{}, fmt.Errorf("decode: %w", ctx.Err())
	}

	if err := cmd.Wait(); err != nil {
		return AudioAnalysis{}, fmt.Errorf("ffmpeg: %w\n%s", err, stderrBuf.String())
	}

	analysis, err := az.Finish()
	if err != nil {
		return AudioAnalysis{}, err
	}

	lufs, err := extractLUFS(ctx, ffmpeg, sourcePath)
	if err == nil {
		analysis.Features.IntegratedLUFS = lufs
	} else if analysis.Features.IntegratedLUFS == 0 {
		analysis.Features.IntegratedLUFS = -70
	}

	return analysis, nil
}

type streamAnalyzer struct {
	pcm               []float64
	totalMonoSamples  int64
	fft               *fourier.FFT
	frames            []AudioFrame
	monoBuf           []float64
	onsetFlux         []float64
	onsetPositiveDiff float64
	previousMono      float64
	havePreviousMono  bool
	loudnessSumSq     float64
	loudnessCount     int
	loudnessBlocks    []float64
	fingerprintSum    [64]float64
	fingerprintFrames int
	centroidSum       float64
	lowRatioSum       float64
	spectralFrames    int
	mu                sync.Mutex
}

func newStreamAnalyzer() *streamAnalyzer {
	return &streamAnalyzer{
		pcm:            make([]float64, 0, fftWindowSize+frameAdvance),
		fft:            fourier.NewFFT(fftWindowSize),
		frames:         make([]AudioFrame, 0, 3000),
		monoBuf:        make([]float64, 0, onsetHopSamples),
		onsetFlux:      make([]float64, 0, 6000),
		loudnessBlocks: make([]float64, 0, 6000),
	}
}

func (a *streamAnalyzer) ConsumeStereo(samples []int16) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(samples)%2 != 0 {
		return ErrIncompleteSample
	}
	for i := 0; i < len(samples); i += 2 {
		mono := (float64(samples[i]) + float64(samples[i+1])) / 2
		a.totalMonoSamples++
		a.pcm = append(a.pcm, mono)
		if a.havePreviousMono {
			if diff := mono - a.previousMono; diff > 0 {
				a.onsetPositiveDiff += diff
			}
		}
		a.previousMono, a.havePreviousMono = mono, true
		a.monoBuf = append(a.monoBuf, mono)
		normalized := mono / 32768.0
		a.loudnessSumSq += normalized * normalized
		a.loudnessCount++
		if len(a.monoBuf) == onsetHopSamples {
			a.onsetFlux = append(a.onsetFlux, a.onsetPositiveDiff)
			a.onsetPositiveDiff = 0
			a.monoBuf = a.monoBuf[:0]
			a.loudnessBlocks = append(a.loudnessBlocks, math.Sqrt(a.loudnessSumSq/float64(a.loudnessCount)))
			a.loudnessSumSq, a.loudnessCount = 0, 0
		}
		for len(a.pcm) >= fftWindowSize {
			a.consumeSpectrumWindow(a.pcm[:fftWindowSize])
			copy(a.pcm, a.pcm[frameAdvance:])
			a.pcm = a.pcm[:len(a.pcm)-frameAdvance]
		}
	}
	return nil
}

func (a *streamAnalyzer) consumeSpectrumWindow(window []float64) {
	weighted := make([]float64, fftWindowSize)
	for i := range weighted {
		weighted[i] = window[i] * 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(fftWindowSize-1)))
	}
	spectrum := a.fft.Coefficients(nil, weighted)
	a.frames = append(a.frames, AudioFrame{Spectrum24: mapBands(spectrum)})
	centroid, lowRatio := spectrumFeatures(spectrum, sampleRate)
	a.centroidSum += centroid
	a.lowRatioSum += lowRatio
	a.spectralFrames++
	bands64 := mapLogBands(spectrum, 64)
	for i := range a.fingerprintSum {
		a.fingerprintSum[i] += bands64[i]
	}
	a.fingerprintFrames++
}

func (a *streamAnalyzer) Finish() (AudioAnalysis, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	duration := float64(a.totalMonoSamples) / sampleRate
	if duration <= 0 {
		return AudioAnalysis{}, fmt.Errorf("zero duration")
	}
	if a.loudnessCount > 0 {
		a.loudnessBlocks = append(a.loudnessBlocks, math.Sqrt(a.loudnessSumSq/float64(a.loudnessCount)))
	}
	expectedFrames := int(math.Ceil(duration * 30))
	for len(a.frames) < expectedFrames {
		window := make([]float64, fftWindowSize)
		copy(window, a.pcm)
		a.consumeSpectrumWindow(window)
		if len(a.pcm) > frameAdvance {
			a.pcm = a.pcm[frameAdvance:]
		} else {
			a.pcm = nil
		}
	}
	if len(a.frames) > expectedFrames {
		a.frames = a.frames[:expectedFrames]
	}
	features := AudioFeatures{
		BPM:              computeBPMFromFlux(a.onsetFlux),
		LoudnessEnvelope: resampleEnvelope(a.loudnessBlocks),
	}
	if a.spectralFrames > 0 {
		features.SpectralCentroid = a.centroidSum / float64(a.spectralFrames)
		features.LowFrequencyRatio = a.lowRatioSum / float64(a.spectralFrames)
	}
	if a.fingerprintFrames > 0 {
		var peak float64
		for i := range features.Fingerprint64 {
			features.Fingerprint64[i] = a.fingerprintSum[i] / float64(a.fingerprintFrames)
			peak = math.Max(peak, features.Fingerprint64[i])
		}
		if peak > 0 {
			for i := range features.Fingerprint64 {
				features.Fingerprint64[i] /= peak
			}
		}
	}
	clampFeatures(&features)
	return AudioAnalysis{FPS: 30, Duration: duration, Frames: smoothBands(a.frames), Features: features}, nil
}

func spectrumFeatures(spectrum []complex128, rate int) (centroid, lowRatio float64) {
	var weighted, low, total float64
	nyquist := float64(rate) / 2
	for i := 1; i < len(spectrum); i++ {
		magnitude := cmag(spectrum[i])
		frequency := float64(i) / float64(len(spectrum)) * nyquist
		weighted += magnitude * frequency
		total += magnitude
		if frequency <= 250 {
			low += magnitude
		}
	}
	if total == 0 {
		return 0, 0
	}
	return weighted / total, low / total
}

func mapLogBands(spectrum []complex128, count int) []float64 {
	out := make([]float64, count)
	nyquist := float64(sampleRate) / 2
	for band := range out {
		lo := 20 * math.Pow(1000, float64(band)/float64(count))
		hi := 20 * math.Pow(1000, float64(band+1)/float64(count))
		loBin := max(1, int(lo/nyquist*float64(len(spectrum))))
		hiBin := min(len(spectrum), int(hi/nyquist*float64(len(spectrum))))
		for i := loBin; i < hiBin; i++ {
			out[band] += cmag(spectrum[i])
		}
		if hiBin > loBin {
			out[band] /= float64(hiBin - loBin)
		}
	}
	return out
}

func resampleEnvelope(blocks []float64) [1000]float64 {
	var out [1000]float64
	if len(blocks) == 0 {
		return out
	}
	for i := range out {
		position := float64(i) * float64(len(blocks)-1) / float64(len(out)-1)
		left := int(math.Floor(position))
		right := min(left+1, len(blocks)-1)
		fraction := position - float64(left)
		out[i] = blocks[left]*(1-fraction) + blocks[right]*fraction
	}
	return out
}

func computeBPM(pcm []int16, rate int) float64 {
	mono := make([]float64, len(pcm)/2)
	for i := range mono {
		mono[i] = float64(pcm[i*2]+pcm[i*2+1]) / 2
	}
	return computeBPMFromMono(mono, rate)
}

func computeBPMFromMono(mono []float64, rate int) float64 {
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
	return computeBPMFromFlux(flux)
}

func computeBPMFromFlux(flux []float64) float64 {
	minLag := onsetRate * 60 / 200
	maxLag := onsetRate * 60 / 60

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
	return 60.0 * float64(onsetRate) / float64(bestLag)
}

func computeLoudnessEnvelopeFast(pcm []int16, n int) [1000]float64 {
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
