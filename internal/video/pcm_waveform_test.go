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
	if len(got) != 3 || got[0] != 0 || got[1] != 32768 || got[2] != 65535 {
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
