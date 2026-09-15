package obsrtmp

import (
	"context"
	"testing"

	"imagepadserver/internal/airplaycontract"
)

func TestDirectBackendRecordingClaimRequiresCurrentRoute(t *testing.T) {
	for _, scenario := range []string{"active", "replaced", "stopping", "failed", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			const id = "0123456789abcdef"
			root := t.TempDir()
			observer, err := newDirectPublisherObserver(root, id)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = observer.Close() })
			first, second := airplaycontract.NewPublisherArtifacts(root, id, 1), airplaycontract.NewPublisherArtifacts(root, id, 2)
			if err := observer.PreparePublisher(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			if !observer.claimCurrentForPublication(first) {
				t.Fatal("initial publication missing")
			}
			if err := observer.PreparePublisher(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			route := directBackendRoute{sessionID: id, sessionEpoch: 4, generation: 2, requestID: "candidate", privateRTSPPort: 18554, runtime: &mediaMTXRuntime{}}
			router := mustNewDirectBackendRouter(t, route)
			m := &Manager{running: true, directPublishing: true, listenerGeneration: 4, directHandle: DirectSessionHandle{ID: id, Generation: 4}, current: &Session{ID: id, Recording: first.Recording}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "replaced":
				next := route
				next.generation, next.requestID = 3, "newer"
				if err := router.commit(route, next); err != nil {
					t.Fatal(err)
				}
			case "stopping":
				router.markTerminal()
			case "failed":
				router.failed = true
			case "canceled":
				cancel()
			}
			got := m.claimDirectRecordingForBackend(ctx, router, route, observer, second)
			if scenario == "active" {
				if !got || m.current.Recording != second.Recording {
					t.Fatal("current recording not adopted")
				}
			} else if got || m.current.Recording != first.Recording || observer.candidateGeneration != 1 {
				t.Fatal("stale recording claim changed publication or consumed its generation")
			}
		})
	}
}
