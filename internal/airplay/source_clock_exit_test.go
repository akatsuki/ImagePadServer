package airplay

import "testing"

func TestClassifySourceClockExit(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		crashed bool
		want    PublisherExitDecision
	}{
		{name: "stopped", code: 0, want: PublisherExitDecision{Restart: false, Reason: "stopped"}},
		{name: "configuration error", code: 2, want: PublisherExitDecision{Restart: false, Reason: "configuration-error"}},
		{name: "native no signal", code: 20, want: PublisherExitDecision{Restart: false, Reason: "no-signal"}},
		{name: "protocol error", code: 21, want: PublisherExitDecision{Restart: false, Reason: "protocol-error"}},
		{name: "pipeline error", code: 22, want: PublisherExitDecision{Restart: true, Reason: "pipeline-error"}},
		{name: "unknown positive", code: 37, want: PublisherExitDecision{Restart: false, Reason: "unclassified-exit"}},
		{name: "unknown negative", code: -1073740940, want: PublisherExitDecision{Restart: false, Reason: "unclassified-exit"}},
		{name: "crash overrides stopped", code: 0, crashed: true, want: PublisherExitDecision{Restart: true, Reason: "process-crash"}},
		{name: "crash overrides configuration", code: 2, crashed: true, want: PublisherExitDecision{Restart: true, Reason: "process-crash"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifySourceClockExit(test.code, test.crashed); got != test.want {
				t.Fatalf("classifySourceClockExit(%d, %t) = %+v, want %+v", test.code, test.crashed, got, test.want)
			}
		})
	}
}
