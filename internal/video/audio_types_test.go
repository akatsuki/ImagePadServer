package video

import "testing"

func TestAudioContracts(t *testing.T) {
	if MaxMediaSourceBytes != 4294967295 {
		t.Fatalf("MaxMediaSourceBytes = %d", MaxMediaSourceBytes)
	}
	if SourceSoundCloud != "soundcloud" || SourceLocalAudio != "local_audio" || SourceRemoteAudio != "remote_audio" {
		t.Fatalf("unexpected source kinds")
	}
	var frame AudioFrame
	var features AudioFeatures
	if len(frame.Spectrum24) != 24 || len(features.Fingerprint64) != 64 || len(features.LoudnessEnvelope) != 1000 {
		t.Fatalf("fixed analysis dimensions changed")
	}
}
