package nicoexportbudget

import (
	"io"
	"time"
)

type Options struct {
	Percent        uint32
	SampleInterval time.Duration
}

type ProcessSpec struct {
	Exe    string
	Args   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type Report struct {
	Verified    bool     `json:"verified"`
	ExitCode    int      `json:"exit_code"`
	CPUSeconds  float64  `json:"cpu_seconds"`
	LogicalCPUs uint32   `json:"logical_cpus"`
	JobCPURate  uint32   `json:"job_cpu_rate"`
	Samples     []Sample `json:"cpu_samples"`
	PIDs        []uint32 `json:"pids,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

type Sample struct {
	At         time.Duration `json:"at"`
	CPUPercent float64       `json:"cpu_percent"`
	PIDs       []uint32      `json:"pids,omitempty"`
}
