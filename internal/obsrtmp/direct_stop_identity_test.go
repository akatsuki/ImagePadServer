package obsrtmp

import (
	"context"
	"testing"
	"time"
)

func TestStopDirectRequiresExactOwnedSessionHandle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		handle   DirectSessionHandle
		wantStop bool
	}{
		{"foreign ID with same epoch", DirectSessionHandle{ID: "foreign", Generation: 4}, false},
		{"same ID with stale epoch", DirectSessionHandle{ID: "owned", Generation: 3}, false},
		{"empty ID", DirectSessionHandle{Generation: 4}, false},
		{"exact owner", DirectSessionHandle{ID: "owned", Generation: 4}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			close(done)
			m := &Manager{
				directPublishing:   true,
				directHandle:       DirectSessionHandle{ID: "owned", Generation: 4},
				listenerGeneration: 4,
				stop:               cancel,
				done:               done,
			}
			got := m.StopDirect(tc.handle, time.Second)
			if got != tc.wantStop || (ctx.Err() != nil) != tc.wantStop {
				t.Fatalf("stop=%t, session canceled=%t; want both %t", got, ctx.Err() != nil, tc.wantStop)
			}
		})
	}
}
