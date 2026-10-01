//go:build !windows

package nicoexportbudget

import (
	"context"
	"errors"
	"testing"
)

func TestRunRemainsUnsupportedAndIgnoresInputs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := Run(ctx, ProcessSpec{}, Options{})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Run error = %v, want ErrUnsupported", err)
	}
	if got.Reason != ErrUnsupported.Error() {
		t.Fatalf("Run report reason = %q, want %q", got.Reason, ErrUnsupported.Error())
	}
}

func TestStartSessionIsUnsupportedForInvalidSpecAndOptions(t *testing.T) {
	tests := []struct {
		name    string
		spec    ProcessSpec
		options Options
	}{
		{name: "missing executable", spec: ProcessSpec{}, options: Options{Percent: 100}},
		{name: "invalid percent", spec: ProcessSpec{Exe: "definitely-not-launched"}, options: Options{Percent: 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := StartSession(context.Background(), tc.spec, tc.options)
			if p != nil {
				t.Fatalf("StartSession returned process %v, want nil on unsupported platform", p)
			}
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("StartSession error = %v, want ErrUnsupported", err)
			}
		})
	}
}
