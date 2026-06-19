package video

import (
	"math"
	"testing"

	"gonum.org/v1/gonum/dsp/fourier"
)

func TestFractionalLogBands(t *testing.T) {
	fftSize := 8192
	sampleRate := 48000

	for b := 0; b < 24; b++ {
		loFreq := 20.0 * math.Pow(1000.0, float64(b)/24.0)
		hiFreq := 20.0 * math.Pow(1000.0, float64(b+1)/24.0)
		freq := math.Sqrt(loFreq * hiFreq)

		window := make([]float64, fftSize)
		for i := 0; i < fftSize; i++ {
			sine := math.Sin(2 * math.Pi * freq * float64(i) / float64(sampleRate))
			hann := 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(fftSize-1)))
			window[i] = sine * hann
		}

		fft := fourier.NewFFT(fftSize)
		coeff := fft.Coefficients(nil, window)

		energies := fractionalLogBandEnergies(coeff, sampleRate)

		if math.IsNaN(energies[b]) || math.IsInf(energies[b], 0) || energies[b] <= 0 {
			t.Errorf("band %d (%.1f-%.1f Hz, test freq %.1f Hz): expected finite positive energy, got %e",
				b, loFreq, hiFreq, freq, energies[b])
		}
	}
}
