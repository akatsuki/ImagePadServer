package iphonemodel

import "fmt"

type Status string

const (
	StatusPass    Status = "PASS"
	StatusFail    Status = "FAIL"
	StatusBlocked Status = "BLOCKED"
	StatusNotRun  Status = "NOT_RUN"
)

type StageResult struct {
	Stage    string `json:"stage"`
	Status   Status `json:"status"`
	Error    string `json:"error,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

func FirstFailure(stages []StageResult) *StageResult {
	for index := range stages {
		if stages[index].Status == StatusFail {
			return &stages[index]
		}
	}
	return nil
}

func ValidateStageOrder(stages []StageResult) error {
	knownStatus := map[Status]bool{
		StatusPass: true, StatusFail: true, StatusBlocked: true, StatusNotRun: true,
	}
	seenStages := make(map[string]bool, len(stages))
	terminal := false
	failureSeen := false
	for index, stage := range stages {
		if stage.Stage == "" {
			return fmt.Errorf("stage %d has an empty name", index)
		}
		if seenStages[stage.Stage] {
			return fmt.Errorf("stage %q is duplicated", stage.Stage)
		}
		seenStages[stage.Stage] = true
		if !knownStatus[stage.Status] {
			return fmt.Errorf("stage %q has unknown status %q", stage.Stage, stage.Status)
		}
		switch stage.Status {
		case StatusPass:
			if terminal {
				return fmt.Errorf("stage %q passes after a terminal stage", stage.Stage)
			}
		case StatusFail:
			if terminal || failureSeen {
				return fmt.Errorf("stage %q fails after an earlier terminal stage", stage.Stage)
			}
			failureSeen = true
			terminal = true
		case StatusBlocked, StatusNotRun:
			terminal = true
		}
	}
	return nil
}
