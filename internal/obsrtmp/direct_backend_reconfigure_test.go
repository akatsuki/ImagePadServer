package obsrtmp

import (
	"errors"
	"testing"
)

func mustNewDirectBackendRouter(t *testing.T, initial directBackendRoute) *directBackendRouter {
	t.Helper()
	router, err := newDirectBackendRouter(initial)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	return router
}

func TestNewDirectBackendRouterRejectsInvalidInitialRoute(t *testing.T) {
	base := directBackendRoute{
		sessionID:       "session-1",
		sessionEpoch:    7,
		generation:      1,
		requestID:       "request-1",
		privateRTSPPort: 50001,
	}
	cases := []struct {
		name string
		edit func(*directBackendRoute)
	}{
		{name: "empty session", edit: func(route *directBackendRoute) { route.sessionID = "" }},
		{name: "zero epoch", edit: func(route *directBackendRoute) { route.sessionEpoch = 0 }},
		{name: "zero generation", edit: func(route *directBackendRoute) { route.generation = 0 }},
		{name: "empty request", edit: func(route *directBackendRoute) { route.requestID = "" }},
		{name: "zero port", edit: func(route *directBackendRoute) { route.privateRTSPPort = 0 }},
		{name: "port too large", edit: func(route *directBackendRoute) { route.privateRTSPPort = 65536 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initial := base
			tc.edit(&initial)
			if router, err := newDirectBackendRouter(initial); !errors.Is(err, errDirectBackendRouterInvalidRoute) || router != nil {
				t.Fatalf("constructor result = router %v, error %v; want nil and %v", router, err, errDirectBackendRouterInvalidRoute)
			}
		})
	}
}

func TestDirectBackendRouterPrepareDoesNotChangeActive(t *testing.T) {
	activeRuntime := &mediaMTXRuntime{}
	router := mustNewDirectBackendRouter(t, directBackendRoute{
		sessionID:       "session-1",
		sessionEpoch:    7,
		generation:      1,
		requestID:       "request-1",
		privateRTSPPort: 50001,
		runtime:         activeRuntime,
	})

	candidate := directBackendRoute{
		sessionID:       "session-1",
		sessionEpoch:    7,
		generation:      2,
		requestID:       "request-2",
		privateRTSPPort: 50002,
		runtime:         &mediaMTXRuntime{},
	}
	prepared, err := router.prepare(candidate)
	if err != nil {
		t.Fatalf("prepare candidate: %v", err)
	}
	if prepared != candidate {
		t.Fatalf("prepared candidate = %+v, want %+v", prepared, candidate)
	}
	if got := router.snapshot(); got != (directBackendRoute{
		sessionID:       "session-1",
		sessionEpoch:    7,
		generation:      1,
		requestID:       "request-1",
		privateRTSPPort: 50001,
		runtime:         activeRuntime,
	}) {
		t.Fatalf("prepare changed active route = %+v", got)
	}
}

func TestDirectBackendRouterCommitRequiresExactExpectedIdentity(t *testing.T) {
	newRouter := func() (*directBackendRouter, directBackendRoute) {
		active := directBackendRoute{
			sessionID:       "session-1",
			sessionEpoch:    7,
			generation:      1,
			requestID:       "request-1",
			privateRTSPPort: 50001,
		}
		return mustNewDirectBackendRouter(t, active), active
	}

	t.Run("stale expected identity", func(t *testing.T) {
		router, active := newRouter()
		candidate := active
		candidate.generation = 2
		candidate.requestID = "request-2"
		candidate.privateRTSPPort = 50002
		if err := router.commit(active, candidate); err != nil {
			t.Fatal(err)
		}
		staleExpected := active
		if err := router.commit(staleExpected, candidate); !errors.Is(err, errDirectBackendRouterStaleCommit) {
			t.Fatalf("stale commit error = %v, want %v", err, errDirectBackendRouterStaleCommit)
		}
	})

	t.Run("foreign session", func(t *testing.T) {
		router, active := newRouter()
		candidate := active
		candidate.sessionID = "session-2"
		candidate.generation = 2
		candidate.requestID = "request-2"
		candidate.privateRTSPPort = 50002
		if err := router.commit(active, candidate); !errors.Is(err, errDirectBackendRouterForeignSession) {
			t.Fatalf("foreign-session commit error = %v, want %v", err, errDirectBackendRouterForeignSession)
		}
	})

	t.Run("expected runtime identity", func(t *testing.T) {
		activeRuntime := &mediaMTXRuntime{}
		active := directBackendRoute{
			sessionID:       "session-1",
			sessionEpoch:    7,
			generation:      1,
			requestID:       "request-1",
			privateRTSPPort: 50001,
			runtime:         activeRuntime,
		}
		router := mustNewDirectBackendRouter(t, active)
		expected := active
		expected.runtime = &mediaMTXRuntime{}
		candidate := active
		candidate.generation = 2
		candidate.requestID = "request-2"
		candidate.privateRTSPPort = 50002
		if err := router.commit(expected, candidate); !errors.Is(err, errDirectBackendRouterStaleCommit) {
			t.Fatalf("runtime-mismatch commit error = %v, want %v", err, errDirectBackendRouterStaleCommit)
		}
	})
}

func TestDirectBackendRouterCommitRejectsInvalidCandidateAndTerminalState(t *testing.T) {
	active := directBackendRoute{
		sessionID:       "session-1",
		sessionEpoch:    7,
		generation:      2,
		requestID:       "request-2",
		privateRTSPPort: 50002,
	}

	cases := []struct {
		name string
		edit func(*directBackendRoute)
		want error
	}{
		{name: "generation zero", edit: func(route *directBackendRoute) { route.generation = 0 }, want: errDirectBackendRouterGenerationNotMonotonic},
		{name: "generation not increasing", edit: func(route *directBackendRoute) { route.generation = 2 }, want: errDirectBackendRouterGenerationNotMonotonic},
		{name: "request id empty", edit: func(route *directBackendRoute) { route.requestID = "" }, want: errDirectBackendRouterInvalidRoute},
		{name: "port zero", edit: func(route *directBackendRoute) { route.privateRTSPPort = 0 }, want: errDirectBackendRouterInvalidRoute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := mustNewDirectBackendRouter(t, active)
			candidate := active
			candidate.generation = 3
			candidate.requestID = "request-3"
			candidate.privateRTSPPort = 50003
			tc.edit(&candidate)
			if err := router.commit(active, candidate); !errors.Is(err, tc.want) {
				t.Fatalf("commit error = %v, want %v", err, tc.want)
			}
		})
	}

	router := mustNewDirectBackendRouter(t, active)
	router.markTerminal()
	candidate := active
	candidate.generation = 3
	candidate.requestID = "request-3"
	candidate.privateRTSPPort = 50003
	if err := router.commit(active, candidate); !errors.Is(err, errDirectBackendRouterTerminal) {
		t.Fatalf("terminal commit error = %v, want %v", err, errDirectBackendRouterTerminal)
	}
}
