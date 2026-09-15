package iphonemodel

import "testing"

func TestFirstFailureKeepsLaterStagesBlocked(t *testing.T) {
	stages := []StageResult{{Stage: "uxplay", Status: StatusFail}, {Stage: "bridge", Status: StatusBlocked}}
	got := FirstFailure(stages)
	if got == nil || got.Stage != "uxplay" {
		t.Fatalf("got=%#v", got)
	}
}

func TestFirstFailureReturnsNilWithoutFailure(t *testing.T) {
	if got := FirstFailure([]StageResult{{Stage: "uxplay", Status: StatusPass}}); got != nil {
		t.Fatalf("got=%#v", got)
	}
}

func TestValidateStageOrderRejectsImpossibleEvidence(t *testing.T) {
	tests := []struct {
		name   string
		stages []StageResult
	}{
		{"not-run before pass", []StageResult{{Stage: "uxplay", Status: StatusNotRun}, {Stage: "bridge", Status: StatusPass}}},
		{"fail before pass", []StageResult{{Stage: "uxplay", Status: StatusFail}, {Stage: "bridge", Status: StatusPass}}},
		{"blocked before fail", []StageResult{{Stage: "uxplay", Status: StatusBlocked}, {Stage: "bridge", Status: StatusFail}}},
		{"unknown status", []StageResult{{Stage: "uxplay", Status: "MAYBE"}}},
		{"empty stage", []StageResult{{Stage: "", Status: StatusPass}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateStageOrder(tt.stages); err == nil {
				t.Fatalf("invalid stages accepted: %#v", tt.stages)
			}
		})
	}
}

func TestValidateStageOrderAcceptsFailureThenBlocked(t *testing.T) {
	stages := []StageResult{
		{Stage: "fixture", Status: StatusPass},
		{Stage: "bridge", Status: StatusFail},
		{Stage: "mediamtx", Status: StatusBlocked},
		{Stage: "hls", Status: StatusNotRun},
	}
	if err := ValidateStageOrder(stages); err != nil {
		t.Fatal(err)
	}
}
