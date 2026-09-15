package airplaycontract

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

type publisherObserverContractStub struct{}

func (publisherObserverContractStub) PreparePublisher(context.Context, PublisherArtifacts) error {
	return nil
}
func (publisherObserverContractStub) ObservePublisher(Event)                {}
func (publisherObserverContractStub) FinishRecording(RecordingOutcome)      {}
func (publisherObserverContractStub) CompletePublisher(PublisherCompletion) {}
func (publisherObserverContractStub) SealPublishers()                       {}

var _ PublisherObserver = publisherObserverContractStub{}

func TestNewPublisherArtifactsBuildsGenerationScopedPaths(t *testing.T) {
	root := filepath.Join("media-root", "nested")
	got := NewPublisherArtifacts(root, "session-abc", 1)
	stem := filepath.Join(root, "airplay", "session-abc", "publisher-0001")
	want := PublisherArtifacts{
		SessionID:   "session-abc",
		Generation:  1,
		Recording:   stem + ".mp4",
		Ready:       stem + ".ready.json",
		MediaReady:  stem + ".media-ready.json",
		EventLog:    stem + ".events.jsonl",
		ProcessLog:  stem + ".log",
		StopRequest: stem + ".stop",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("artifacts=%+v, want %+v", got, want)
	}
}

func TestPublisherCompletionKeepsGenerationExitEvidenceSeparate(t *testing.T) {
	artifacts := NewPublisherArtifacts(t.TempDir(), "0123456789abcdef", 4)
	completion := PublisherCompletion{
		Artifacts: artifacts, Started: true, ExitConfirmed: true,
		EventLogError: "line 3 malformed",
	}
	if completion.Artifacts != artifacts || !completion.Started || !completion.ExitConfirmed || completion.EventLogError != "line 3 malformed" {
		t.Fatalf("completion=%+v", completion)
	}
}

func TestNewPublisherArtifactsSeparatesEveryPathAcrossGenerations(t *testing.T) {
	first := NewPublisherArtifacts("media-root", "session-abc", 1)
	second := NewPublisherArtifacts("media-root", "session-abc", 2)
	firstPaths := []string{first.Recording, first.Ready, first.MediaReady, first.EventLog, first.ProcessLog, first.StopRequest}
	secondPaths := []string{second.Recording, second.Ready, second.MediaReady, second.EventLog, second.ProcessLog, second.StopRequest}
	seen := make(map[string]string, len(firstPaths)+len(secondPaths))
	for generation, paths := range map[string][]string{"generation-1": firstPaths, "generation-2": secondPaths} {
		for _, path := range paths {
			if path == "" {
				t.Fatalf("%s contains an empty path", generation)
			}
			if previous, exists := seen[path]; exists {
				t.Fatalf("artifact path %q reused by %s and %s", path, previous, generation)
			}
			seen[path] = generation
		}
	}
}
