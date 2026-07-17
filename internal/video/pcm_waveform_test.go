package video

import "testing"

func TestPCMToWaveformQ16Silence(t *testing.T) {
	got := PCMToWaveformQ16(make([]int16, 48000*2), 2, 48000, 30, 0, 64)
	if len(got) != 30 {
		t.Fatalf("len=%d, want 30", len(got))
	}
	for i, v := range got {
		if v != 0 {
			t.Fatalf("sample %d=%d, want 0", i, v)
		}
	}
}

func TestPCMToWaveformQ16PeakAndChannels(t *testing.T) {
	pcm := []int16{0, 0, 16384, -16384, 32767, -32768}
	got := PCMToWaveformQ16(pcm, 2, 6, 3, 0, 8)
	if len(got) != 2 || got[0] != 32768 || got[1] != 65535 {
		t.Fatalf("got %#v", got)
	}
}

func TestPCMToWaveformQ16BoundAndFrameIndex(t *testing.T) {
	pcm := make([]int16, 100*2)
	for i := range pcm {
		pcm[i] = 32767
	}
	if got := PCMToWaveformQ16(pcm, 2, 100, 10, 0, 3); len(got) != 3 {
		t.Fatalf("bounded len=%d", len(got))
	}
	got := PCMToWaveformQ16(pcm, 2, 100, 10, 4, 99)
	if len(got) != 6 {
		t.Fatalf("suffix len=%d, want 6", len(got))
	}
}

func TestPCMToWaveformQ16Invalid(t *testing.T) {
	if PCMToWaveformQ16(nil, 2, 48000, 30, 0, 10) != nil {
		t.Fatal("nil input should be nil")
	}
	if PCMToWaveformQ16([]int16{1}, 2, 48000, 30, 0, 10) != nil {
		t.Fatal("partial channel should be nil")
	}
}

func TestSignedPCMToWaveformQ16PreservesCentreAndPolarity(t *testing.T) {
	pcm := []int16{-20000, -10000, 10000, 20000}
	got := SignedPCMToWaveformQ16(pcm, 1, 4, 2, 0, 8)
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	// First window midpoint is -15000, second is +15000.
	if got[0] != uint16(-15000+32768) || got[1] != uint16(15000+32768) {
		t.Fatalf("got %#v", got)
	}
}

func TestSignedPCMToWaveformQ16Invalid(t *testing.T) {
	if SignedPCMToWaveformQ16(nil, 2, 48000, 30, 0, 10) != nil {
		t.Fatal("nil input should be nil")
	}
}

func TestSignedPCMToWaveformMinMaxQ16PreservesRange(t *testing.T) {
	pcm := []int16{-20000, -10000, 10000, 20000}
	got := SignedPCMToWaveformMinMaxQ16(pcm, 1, 4, 2, 0, 8)
	want := []uint16{uint16(-20000 + 32768), uint16(-10000 + 32768), uint16(10000 + 32768), uint16(20000 + 32768)}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] { t.Fatalf("sample %d=%d, want %d", i, got[i], want[i]) }
	}
}

func TestSignedPCMToWaveformMinMaxQ16BoundsColumns(t *testing.T) {
	pcm := make([]int16, 100)
	got := SignedPCMToWaveformMinMaxQ16(pcm, 1, 100, 10, 0, 3)
	if len(got) != 6 { t.Fatalf("len=%d, want 6", len(got)) }
}
