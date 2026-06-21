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
	"runtime"
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
	fftWindowSize   = 8192
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

// musicLoudnormFilter is the single-pass EBU R128 loudnorm applied inline to
// music-mode sources during both analysis and render, so every track is
// analyzed and played back at the same -14 LUFS anchor without a separate
// normalization pass or intermediate file.
const musicLoudnormFilter = "loudnorm=I=-14.0:TP=-1.0:LRA=11.0"

// audioLoudnormFilter returns the inline loudnorm filter for music sources, or
// an empty string for sources that should not be loudness-normalized.
func audioLoudnormFilter(kind SourceKind) string {
	if kind == SourceMusic {
		return musicLoudnormFilter
	}
	return ""
}

// AnalyzeAudio analyzes a source with no audio filtering.
func AnalyzeAudio(ctx context.Context, ffmpeg, sourcePath string) (AudioAnalysis, error) {
	return AnalyzeAudioForKind(ctx, ffmpeg, sourcePath, "")
}

// AnalyzeAudioForKind analyzes a source, applying the kind's inline audio
// filter (e.g. loudnorm for music) so the spectrum reflects the same signal
// that will be rendered.
func AnalyzeAudioForKind(ctx context.Context, ffmpeg, sourcePath string, kind SourceKind) (AudioAnalysis, error) {
	az := newStreamAnalyzer()

	args := []string{"-v", "error", "-i", sourcePath}
	if filter := audioLoudnormFilter(kind); filter != "" {
		args = append(args, "-af", filter)
	}
	args = append(args,
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-ac", "2",
		"-",
	)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)

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

type spectrumJob struct {
	idx    int
	window []float64
}

type spectrumResult struct {
	idx      int
	spectrum [24]float64
	centroid float64
	lowRatio float64
	bands64  []float64
}

type streamAnalyzer struct {
	pcm               []float64
	totalMonoSamples  int64
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

	// Parallel spectrum pipeline. Per-window FFT work is the dominant analysis
	// cost; windows are independent, so they are dispatched to a worker pool
	// (each worker owns its fourier.FFT, which is not concurrency-safe) and
	// gathered by a single collector goroutine. Results carry a frame index so
	// out-of-order completion is fine, and the per-track sums are
	// order-independent. The collector never blocks, so the pipeline cannot
	// deadlock.
	dispatched   int
	jobs         chan spectrumJob
	results      chan spectrumResult
	workersWG    sync.WaitGroup
	collectorWG  sync.WaitGroup
	frameResults map[int]spectrumResult
}

func newStreamAnalyzer() *streamAnalyzer {
	a := &streamAnalyzer{
		pcm:            make([]float64, 0, fftWindowSize+frameAdvance),
		frames:         make([]AudioFrame, 0, 3000),
		monoBuf:        make([]float64, 0, onsetHopSamples),
		onsetFlux:      make([]float64, 0, 6000),
		loudnessBlocks: make([]float64, 0, 6000),
		frameResults:   make(map[int]spectrumResult),
	}
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	a.jobs = make(chan spectrumJob, workers*2)
	a.results = make(chan spectrumResult, workers*2)
	for w := 0; w < workers; w++ {
		a.workersWG.Add(1)
		go a.spectrumWorker()
	}
	a.collectorWG.Add(1)
	go a.collector()
	return a
}

// spectrumWorker computes the windowed FFT and band features for each job.
func (a *streamAnalyzer) spectrumWorker() {
	defer a.workersWG.Done()
	fft := fourier.NewFFT(fftWindowSize)
	weighted := make([]float64, fftWindowSize)
	for job := range a.jobs {
		for i, v := range job.window {
			weighted[i] = v * 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(fftWindowSize-1)))
		}
		spectrum := fft.Coefficients(nil, weighted)
		centroid, lowRatio := spectrumFeatures(spectrum, sampleRate)
		a.results <- spectrumResult{
			idx:      job.idx,
			spectrum: fractionalLogBandEnergies(spectrum, sampleRate),
			centroid: centroid,
			lowRatio: lowRatio,
			bands64:  mapLogBands(spectrum, 64),
		}
	}
}

// collector gathers worker results into the frame map keyed by index. Summation
// is deferred to Finish so it happens in deterministic index order (float
// addition is not associative), which also reproduces the sequential result.
func (a *streamAnalyzer) collector() {
	defer a.collectorWG.Done()
	for r := range a.results {
		a.frameResults[r.idx] = r
	}
}

// drainPipeline closes the job queue and waits for workers and the collector to
// finish. Safe to call exactly once, from Finish.
func (a *streamAnalyzer) drainPipeline() {
	close(a.jobs)
	a.workersWG.Wait()
	close(a.results)
	a.collectorWG.Wait()
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

// consumeSpectrumWindow dispatches one FFT window (copied, since the backing
// pcm buffer is reused) to the worker pool. The actual FFT/band work happens on
// a worker; results are gathered by the collector. Blocking on a full job queue
// provides backpressure that bounds in-flight memory.
func (a *streamAnalyzer) consumeSpectrumWindow(window []float64) {
	buf := make([]float64, fftWindowSize)
	copy(buf, window)
	a.jobs <- spectrumJob{idx: a.dispatched, window: buf}
	a.dispatched++
}

func (a *streamAnalyzer) Finish() (AudioAnalysis, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	duration := float64(a.totalMonoSamples) / sampleRate
	if duration <= 0 {
		a.drainPipeline()
		return AudioAnalysis{}, fmt.Errorf("zero duration")
	}
	if a.loudnessCount > 0 {
		a.loudnessBlocks = append(a.loudnessBlocks, math.Sqrt(a.loudnessSumSq/float64(a.loudnessCount)))
	}
	expectedFrames := int(math.Ceil(duration * 30))
	for a.dispatched < expectedFrames {
		window := make([]float64, fftWindowSize)
		copy(window, a.pcm)
		a.consumeSpectrumWindow(window)
		if len(a.pcm) > frameAdvance {
			a.pcm = a.pcm[frameAdvance:]
		} else {
			a.pcm = nil
		}
	}

	// All windows dispatched: drain the pool, then assemble frames and the
	// per-track sums in index order (deterministic, matches the sequential
	// version exactly since float addition is order-dependent).
	a.drainPipeline()
	a.frames = make([]AudioFrame, a.dispatched)
	for i := 0; i < a.dispatched; i++ {
		r := a.frameResults[i]
		a.frames[i] = AudioFrame{Spectrum24: r.spectrum}
		a.centroidSum += r.centroid
		a.lowRatioSum += r.lowRatio
		a.spectralFrames++
		for j := range a.fingerprintSum {
			a.fingerprintSum[j] += r.bands64[j]
		}
		a.fingerprintFrames++
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
	return AudioAnalysis{FPS: 30, Duration: duration, Frames: finalizeSpectrumFrames(a.frames, 30), Features: features}, nil
}

// finalizeSpectrumFrames converts the raw per-frame band magnitudes collected
// during analysis into display-ready frames: global track normalization
// (normalizeSpectrumTrack) followed by attack/release motion smoothing
// (applySpectrumMotion). This replaces the legacy per-frame mapBands +
// smoothBands path so all 24 bands, including the narrow low-frequency ones,
// animate.
func finalizeSpectrumFrames(frames []AudioFrame, fps float64) []AudioFrame {
	if len(frames) == 0 {
		return frames
	}
	raw := make([][24]float64, len(frames))
	for i, f := range frames {
		raw[i] = f.Spectrum24
	}
	motion := applySpectrumMotion(normalizeSpectrumTrack(raw), fps)
	out := make([]AudioFrame, len(motion))
	for i := range motion {
		out[i] = AudioFrame{Spectrum24: motion[i]}
	}
	return out
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
	hideWindow(cmd)
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
