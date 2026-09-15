package airplaycontract

import "testing"

func TestFixedPathsForRecordingDerivesSchema2Sidecars(t *testing.T) {
	recording := `C:\media\AirPlay session.mp4`

	paths, err := FixedPathsForRecording(recording)
	if err != nil {
		t.Fatal(err)
	}

	if paths.Ready != `C:\media\AirPlay session.mp4.publisher-ready` {
		t.Fatalf("Ready = %q", paths.Ready)
	}
	if paths.MediaReady != `C:\media\AirPlay session.mp4.media-ready` {
		t.Fatalf("MediaReady = %q", paths.MediaReady)
	}
	if paths.EventLog != `C:\media\AirPlay session.mp4.events.jsonl` {
		t.Fatalf("EventLog = %q", paths.EventLog)
	}
}

func TestFixedPathsForRecordingRejectsEmptyRecording(t *testing.T) {
	for _, recording := range []string{"", " ", "\t\r\n"} {
		t.Run(recording, func(t *testing.T) {
			if _, err := FixedPathsForRecording(recording); err == nil {
				t.Fatalf("FixedPathsForRecording(%q) unexpectedly succeeded", recording)
			}
		})
	}
}

func TestFixedPathsForRecordingProducesDistinctPaths(t *testing.T) {
	paths, err := FixedPathsForRecording("recording.mp4")
	if err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]string, 3)
	for name, path := range map[string]string{
		"Ready":      paths.Ready,
		"MediaReady": paths.MediaReady,
		"EventLog":   paths.EventLog,
	} {
		if previous, exists := seen[path]; exists {
			t.Fatalf("%s and %s collide at %q", previous, name, path)
		}
		seen[path] = name
	}
}
