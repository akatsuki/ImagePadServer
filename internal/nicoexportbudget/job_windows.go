//go:build windows

package nicoexportbudget

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	defaultSampleInterval = 250 * time.Millisecond
	maxCapturedLine       = 64 * 1024
	jobCPUControlEnable   = 0x00000001
	jobCPUControlHardCap  = 0x00000004
)

type cpuRateControlInformation struct {
	ControlFlags uint32
	CpuRate      uint32
}

type basicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

type processIDListHeader struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIDsInList  uint32
}

type childPipe struct {
	child  *os.File
	parent *os.File
}

func Run(ctx context.Context, spec ProcessSpec, options Options) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateOptions(options); err != nil {
		return Report{Reason: err.Error()}, err
	}
	if strings.TrimSpace(spec.Exe) == "" {
		return Report{Reason: "executable is required"}, errors.New("nico export budget: executable is required")
	}
	if err := ctx.Err(); err != nil {
		return Report{Reason: err.Error()}, err
	}

	sampleInterval := options.SampleInterval
	if sampleInterval <= 0 {
		sampleInterval = defaultSampleInterval
	}
	job, err := newBudgetJob(options.Percent)
	if err != nil {
		return Report{Reason: err.Error()}, err
	}
	defer windows.CloseHandle(job)

	stdin, err := newChildPipe(true)
	if err != nil {
		return Report{Reason: err.Error()}, err
	}
	stdout, err := newChildPipe(false)
	if err != nil {
		closeChildPipe(stdin)
		return Report{Reason: err.Error()}, err
	}
	stderr, err := newChildPipe(false)
	if err != nil {
		closeChildPipe(stdin)
		closeChildPipe(stdout)
		return Report{Reason: err.Error()}, err
	}

	pi, err := createSuspendedProcess(spec, stdin.child, stdout.child, stderr.child)
	closeFile(stdin.child)
	closeFile(stdout.child)
	closeFile(stderr.child)
	if err != nil {
		closeChildPipe(stdin)
		closeChildPipe(stdout)
		closeChildPipe(stderr)
		return Report{Reason: err.Error()}, err
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		return Report{Reason: fmt.Sprintf("assign process %d to CPU budget job: %v", pi.ProcessId, err)}, fmt.Errorf("assign process to CPU budget job: %w", err)
	}

	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		return Report{Reason: fmt.Sprintf("resume process %d: %v", pi.ProcessId, err)}, fmt.Errorf("resume budgeted process: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	copyErrs := make(chan error, 3)
	var copyWG sync.WaitGroup
	startCopy := func(src *os.File, dst io.Writer, label string) {
		copyWG.Add(1)
		go func() {
			defer copyWG.Done()
			if dst == nil {
				dst = io.Discard
			}
			_, copyErr := io.Copy(dst, src)
			if copyErr != nil {
				select {
				case copyErrs <- fmt.Errorf("%s pipe: %w", label, copyErr):
				default:
				}
				cancel()
			}
		}()
	}
	startCopy(stdout.parent, spec.Stdout, "stdout")
	startCopy(stderr.parent, spec.Stderr, "stderr")

	var stdinWG sync.WaitGroup
	stdinWG.Add(1)
	go func() {
		defer stdinWG.Done()
		defer stdin.parent.Close()
		if spec.Stdin == nil {
			return
		}
		if _, copyErr := io.Copy(stdin.parent, spec.Stdin); copyErr != nil {
			select {
			case copyErrs <- fmt.Errorf("stdin pipe: %w", copyErr):
			default:
			}
			cancel()
		}
	}()

	start := time.Now()
	lastSampleAt := start
	lastCPUSeconds := 0.0
	processEndedAt := time.Time{}
	report := Report{
		Verified:    true,
		LogicalCPUs: windows.GetActiveProcessorCount(windows.ALL_PROCESSOR_GROUPS),
		JobCPURate:  options.Percent * 100,
		PIDs:        []uint32{pi.ProcessId},
		Reason:      fmt.Sprintf("windows_job_cpu_hard_cap=%d", options.Percent*100),
	}
	for {
		if runCtx.Err() != nil {
			_ = windows.TerminateJobObject(job, 1)
			waitForJob(job, 10*time.Second)
			break
		}
		processStatus, waitErr := windows.WaitForSingleObject(pi.Process, uint32((50*time.Millisecond)/time.Millisecond))
		if waitErr != nil {
			_ = windows.TerminateJobObject(job, 1)
			waitForJob(job, 10*time.Second)
			return report, fmt.Errorf("wait for CPU budget job: %w", waitErr)
		}
		now := time.Now()
		if now.Sub(lastSampleAt) >= sampleInterval || processStatus == windows.WAIT_OBJECT_0 {
			cpuSeconds, accountErr := queryCPUSeconds(job)
			if accountErr != nil {
				_ = windows.TerminateJobObject(job, 1)
				waitForJob(job, 10*time.Second)
				return report, fmt.Errorf("query CPU accounting: %w", accountErr)
			}
			pids := queryJobPIDs(job, pi.ProcessId)
			elapsed := now.Sub(lastSampleAt).Seconds()
			cpuPercent := 0.0
			if elapsed > 0 {
				cpuPercent = (cpuSeconds - lastCPUSeconds) / elapsed * 100
				if report.LogicalCPUs > 0 {
					cpuPercent /= float64(report.LogicalCPUs)
				}
			}
			report.Samples = append(report.Samples, Sample{At: now.Sub(start), CPUPercent: cpuPercent, PIDs: pids})
			report.PIDs = mergePIDs(report.PIDs, pids)
			lastCPUSeconds = cpuSeconds
			lastSampleAt = now
			report.CPUSeconds = cpuSeconds
		}
		if processStatus == windows.WAIT_OBJECT_0 {
			if processEndedAt.IsZero() {
				processEndedAt = now
			}
			remaining := queryJobPIDs(job, 0)
			if len(remaining) <= 1 || now.Sub(processEndedAt) >= 5*time.Second {
				if len(remaining) > 1 {
					_ = windows.TerminateJobObject(job, 1)
					waitForJob(job, 10*time.Second)
				}
				break
			}
		}
	}

	if runCtx.Err() != nil {
		closeFile(stdin.parent)
		closeFile(stdout.parent)
		closeFile(stderr.parent)
	} else {
		stdinWG.Wait()
	}
	closeFile(stdout.parent)
	closeFile(stderr.parent)
	copyDone := make(chan struct{})
	go func() {
		copyWG.Wait()
		close(copyDone)
	}()
	select {
	case <-copyDone:
	case <-time.After(5 * time.Second):
		closeFile(stdout.parent)
		closeFile(stderr.parent)
		<-copyDone
	}

	var exitCode uint32
	if err := windows.GetExitCodeProcess(pi.Process, &exitCode); err != nil {
		return report, fmt.Errorf("get process exit code: %w", err)
	}
	report.ExitCode = int(exitCode)
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	select {
	case copyErr := <-copyErrs:
		return report, copyErr
	default:
	}
	if exitCode != 0 {
		return report, fmt.Errorf("budgeted process exited with code %d", exitCode)
	}
	return report, nil
}

func validateOptions(options Options) error {
	if options.Percent == 0 || options.Percent > 100 {
		return fmt.Errorf("CPU percent must be between 1 and 100: %d", options.Percent)
	}
	if options.SampleInterval < 0 {
		return fmt.Errorf("sample interval must not be negative: %s", options.SampleInterval)
	}
	return nil
}

func newBudgetJob(percent uint32) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create CPU budget job: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = windows.CloseHandle(job)
		}
	}()

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	ret, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		return 0, fmt.Errorf("configure CPU budget kill-on-close: %w", err)
	}
	if ret == 0 {
		return 0, errors.New("configure CPU budget kill-on-close: SetInformationJobObject returned zero")
	}
	rate := cpuRateControlInformation{ControlFlags: jobCPUControlEnable | jobCPUControlHardCap, CpuRate: percent * 100}
	ret, err = windows.SetInformationJobObject(job, windows.JobObjectCpuRateControlInformation, uintptr(unsafe.Pointer(&rate)), uint32(unsafe.Sizeof(rate)))
	if err != nil {
		return 0, fmt.Errorf("configure CPU budget hard cap %d: %w", percent*100, err)
	}
	if ret == 0 {
		return 0, fmt.Errorf("configure CPU budget hard cap %d: SetInformationJobObject returned zero", percent*100)
	}
	closeOnError = false
	return job, nil
}

func newChildPipe(stdin bool) (childPipe, error) {
	var readHandle, writeHandle windows.Handle
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	if err := windows.CreatePipe(&readHandle, &writeHandle, &security, 0); err != nil {
		return childPipe{}, fmt.Errorf("create process pipe: %w", err)
	}
	childHandle, parentHandle := readHandle, writeHandle
	if !stdin {
		childHandle, parentHandle = writeHandle, readHandle
	}
	if err := windows.SetHandleInformation(parentHandle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = windows.CloseHandle(readHandle)
		_ = windows.CloseHandle(writeHandle)
		return childPipe{}, fmt.Errorf("make process pipe parent end non-inheritable: %w", err)
	}
	return childPipe{child: os.NewFile(uintptr(childHandle), "nico-export-child"), parent: os.NewFile(uintptr(parentHandle), "nico-export-parent")}, nil
}

func closeChildPipe(pipe childPipe) {
	closeFile(pipe.child)
	closeFile(pipe.parent)
}

func closeFile(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}

func createSuspendedProcess(spec ProcessSpec, stdin, stdout, stderr *os.File) (windows.ProcessInformation, error) {
	args := append([]string{spec.Exe}, spec.Args...)
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("encode process command line: %w", err)
	}
	applicationName, err := windows.UTF16PtrFromString(spec.Exe)
	if err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("encode process executable: %w", err)
	}
	var currentDir *uint16
	if spec.Dir != "" {
		currentDir, err = windows.UTF16PtrFromString(spec.Dir)
		if err != nil {
			return windows.ProcessInformation{}, fmt.Errorf("encode process directory: %w", err)
		}
	}
	var environment *uint16
	var environmentBlock []uint16
	if spec.Env != nil {
		block := strings.Join(spec.Env, "\x00") + "\x00\x00"
		environmentBlock = utf16.Encode([]rune(block))
		environment = &environmentBlock[0]
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Flags: windows.STARTF_USESTDHANDLES, StdInput: windows.Handle(stdin.Fd()), StdOutput: windows.Handle(stdout.Fd()), StdErr: windows.Handle(stderr.Fd())}
	var process windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(applicationName, commandLine, nil, nil, true, flags, environment, currentDir, &startup, &process); err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("create suspended budgeted process: %w", err)
	}
	return process, nil
}

func waitForJob(job windows.Handle, timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	_, _ = windows.WaitForSingleObject(job, uint32(timeout/time.Millisecond))
}

func queryCPUSeconds(job windows.Handle) (float64, error) {
	var info basicAccountingInformation
	var returned uint32
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &returned); err != nil {
		return 0, err
	}
	return float64(info.TotalUserTime+info.TotalKernelTime) / 10_000_000, nil
}

func queryJobPIDs(job windows.Handle, fallback uint32) []uint32 {
	const maxPIDs = 1024
	size := int(unsafe.Sizeof(processIDListHeader{})) + maxPIDs*8
	data := make([]byte, size)
	var returned uint32
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&data[0])), uint32(len(data)), &returned); err != nil {
		return []uint32{fallback}
	}
	header := (*processIDListHeader)(unsafe.Pointer(&data[0]))
	count := int(header.NumberOfProcessIDsInList)
	if count > maxPIDs {
		count = maxPIDs
	}
	ids := make([]uint32, 0, count)
	base := unsafe.Sizeof(processIDListHeader{})
	for i := 0; i < count; i++ {
		pid := *(*uint64)(unsafe.Pointer(&data[base+uintptr(i)*8]))
		if pid > 0 && pid <= ^uint64(0)>>32 {
			ids = append(ids, uint32(pid))
		}
	}
	if len(ids) == 0 {
		return []uint32{fallback}
	}
	return ids
}

func mergePIDs(existing, additional []uint32) []uint32 {
	seen := make(map[uint32]struct{}, len(existing)+len(additional))
	out := make([]uint32, 0, len(existing)+len(additional))
	for _, pid := range append(append([]uint32(nil), existing...), additional...) {
		if pid == 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		out = append(out, pid)
	}
	return out
}
