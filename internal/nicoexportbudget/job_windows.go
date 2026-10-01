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
	defaultSampleInterval                 = 250 * time.Millisecond
	postTerminationPipeDrainGrace         = 500 * time.Millisecond
	maxCapturedLine                       = 64 * 1024
	jobCPUControlEnable                   = 0x00000001
	jobCPUControlHardCap                  = 0x00000004
	procThreadAttributeJobList    uintptr = 0x0002000d
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

var isProcessInJobProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

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
	jobOwned := true
	defer func() {
		if jobOwned {
			_ = windows.CloseHandle(job)
		}
	}()

	proc, err := createSuspendedProcessInJob(job, spec)
	if err != nil {
		return Report{Reason: err.Error()}, err
	}
	defer proc.close()
	stdin, stdout, stderr := proc.stdin, proc.stdout, proc.stderr
	pi := proc.info
	if err := proc.resume(job); err != nil {
		if proc.jobClosed {
			jobOwned = false
		}
		return Report{Reason: fmt.Sprintf("resume process %d: %v", pi.ProcessId, err)}, err
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
	copyDone := make(chan struct{})
	go func() {
		copyWG.Wait()
		close(copyDone)
	}()
	select {
	case <-copyDone:
		closeFile(stdout.parent)
		closeFile(stderr.parent)
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

// StartSession starts one Windows budgeted process and returns its live lifecycle handle.
func StartSession(ctx context.Context, spec ProcessSpec, options Options) (*SessionProcess, error) {
	p, _, err := startSessionWithOps(ctx, spec, options, defaultWindowsSessionOps())
	return p, err
}

type windowsSessionOps struct {
	terminateJob     func(windows.Handle, uint32) error
	closeJob         func(windows.Handle) error
	terminateProcess func(windows.Handle, uint32) error
	waitProcess      func(windows.Handle, time.Duration) (uint32, error)
	queryCPU         func(windows.Handle) (float64, error)
	queryPIDs        func(windows.Handle) ([]uint32, error)
	teardownTimeout  time.Duration
}

func defaultWindowsSessionOps() windowsSessionOps {
	return windowsSessionOps{
		terminateJob:     windows.TerminateJobObject,
		closeJob:         windows.CloseHandle,
		terminateProcess: windows.TerminateProcess,
		waitProcess: func(h windows.Handle, timeout time.Duration) (uint32, error) {
			return windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
		},
		queryCPU:        queryCPUSeconds,
		queryPIDs:       queryJobPIDsStrict,
		teardownTimeout: 10 * time.Second,
	}
}

func startSessionWithOps(ctx context.Context, spec ProcessSpec, options Options, ops windowsSessionOps) (*SessionProcess, *windowsSessionBackend, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateOptions(options); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(spec.Exe) == "" {
		return nil, nil, errors.New("nico export budget: executable is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	job, err := newBudgetJob(options.Percent)
	if err != nil {
		return nil, nil, err
	}
	proc, err := createSuspendedProcessInJob(job, spec)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, nil, err
	}
	if err := proc.resume(job); err != nil {
		proc.close()
		if !proc.jobClosed {
			_ = windows.CloseHandle(job)
		}
		return nil, nil, err
	}
	backend := newWindowsSessionBackend(ctx, job, proc, spec, options, ops)
	p, err := newSessionProcess(ctx, backend)
	return p, backend, err
}

type windowsSessionBackend struct {
	job             windows.Handle
	proc            *budgetProcess
	started         time.Time
	interval        time.Duration
	mu              sync.RWMutex
	handleMu        sync.RWMutex // lock order: handleMu before mu
	report          Report
	done            chan struct{}
	closeOnce       sync.Once
	enforceOnce     sync.Once
	enforceErr      error
	waitErr         error
	closeErr        error
	transportSignal chan struct{}
	transportErr    error
	stopping        bool
	intentionalStop bool
	deadline        time.Time
	jobRetired      bool // guarded by handleMu; no Job API calls after retirement
	pumpWG          sync.WaitGroup
	pumpDone        chan struct{}
	ops             windowsSessionOps
}

func newWindowsSessionBackend(ctx context.Context, job windows.Handle, proc *budgetProcess, spec ProcessSpec, options Options, ops windowsSessionOps) *windowsSessionBackend {
	interval := options.SampleInterval
	if interval <= 0 {
		interval = defaultSampleInterval
	}
	if ops.teardownTimeout <= 0 {
		ops.teardownTimeout = 10 * time.Second
	}
	b := &windowsSessionBackend{job: job, proc: proc, started: time.Now(), interval: interval, done: make(chan struct{}), transportSignal: make(chan struct{}, 1), pumpDone: make(chan struct{}), ops: ops, report: Report{Verified: true, LogicalCPUs: windows.GetActiveProcessorCount(windows.ALL_PROCESSOR_GROUPS), JobCPURate: options.Percent * 100, PIDs: []uint32{proc.info.ProcessId}, Reason: fmt.Sprintf("windows_job_cpu_hard_cap=%d", options.Percent*100)}}
	b.pumpWG.Add(3)
	go b.pump(proc.stdout.parent, spec.Stdout, "stdout")
	go b.pump(proc.stderr.parent, spec.Stderr, "stderr")
	go func() {
		defer b.pumpWG.Done()
		defer closeFile(proc.stdin.parent)
		if spec.Stdin != nil {
			if _, err := io.Copy(proc.stdin.parent, spec.Stdin); err != nil {
				b.recordTransportError("stdin", err)
			}
		}
	}()
	go func() { b.pumpWG.Wait(); close(b.pumpDone) }()
	go b.monitor(ctx)
	return b
}

func (b *windowsSessionBackend) pump(src *os.File, dst io.Writer, label string) {
	defer b.pumpWG.Done()
	if dst == nil {
		dst = io.Discard
	}
	if _, err := io.Copy(dst, src); err != nil {
		b.recordTransportError(label, err)
	}
}

func (b *windowsSessionBackend) recordTransportError(label string, err error) {
	b.mu.Lock()
	if b.intentionalStop {
		b.mu.Unlock()
		return
	}
	if b.transportErr == nil {
		b.transportErr = fmt.Errorf("%s pipe: %w", label, err)
		select {
		case b.transportSignal <- struct{}{}:
		default:
		}
	}
	b.mu.Unlock()
	_ = b.Close()
}

func (b *windowsSessionBackend) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.stopping = true
		b.intentionalStop = true
		if b.deadline.IsZero() {
			b.deadline = time.Now().Add(b.ops.teardownTimeout)
		}
		b.mu.Unlock()
		b.closeErr = b.enforceJob()
		closeFile(b.proc.stdin.parent)
	})
	return b.closeErr
}

func (b *windowsSessionBackend) Wait() (Report, error) {
	<-b.done
	b.mu.RLock()
	report, err := cloneReport(b.report), b.waitErr
	if b.transportErr != nil {
		err = errors.Join(err, b.transportErr)
	}
	b.mu.RUnlock()
	return report, err
}

func (b *windowsSessionBackend) Snapshot() (Report, error) {
	b.handleMu.RLock()
	defer b.handleMu.RUnlock()
	b.mu.RLock()
	report := cloneReport(b.report)
	b.mu.RUnlock()
	if b.jobRetired || b.job == 0 {
		return report, nil
	}
	cpu, err := b.ops.queryCPU(b.job)
	if err != nil {
		return report, err
	}
	report.CPUSeconds = cpu
	return report, nil
}

func (b *windowsSessionBackend) monitor(ctx context.Context) {
	defer close(b.done)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	lastAt, lastCPU := time.Now(), 0.0
	rootExited := false
	for {
		select {
		case <-ticker.C:
		case <-b.transportSignal:
		case <-ctx.Done():
		}
		if ctx.Err() != nil || b.hasTransportError() {
			_ = b.Close()
		}
		status, err := b.ops.waitProcess(b.proc.info.Process, 10*time.Millisecond)
		if err != nil {
			b.setWaitError(fmt.Errorf("wait for session process: %w", err))
			_ = b.Close()
		} else if status == windows.WAIT_OBJECT_0 {
			rootExited = true
		}
		if rootExited && !b.isStopping() {
			b.beginStopping()
		}
		if !rootExited {
			b.recordSample(&lastAt, &lastCPU)
		}
		if rootExited {
			if b.jobEmpty() {
				break
			}
			if b.expired() {
				_ = b.Close()
				break
			}
		}
		if b.isStopping() && b.expired() {
			_ = b.Close()
			if err := b.ops.terminateProcess(b.proc.info.Process, 1); err != nil {
				b.setWaitError(fmt.Errorf("best-effort root termination at teardown deadline: %w", err))
			}
			status, waitErr := b.ops.waitProcess(b.proc.info.Process, 0)
			if waitErr == nil && status == windows.WAIT_OBJECT_0 {
				rootExited = true
			} else {
				b.setWaitError(fmt.Errorf("root process exit unverified at teardown deadline: status=%#x wait=%v", status, waitErr))
			}
			break
		}
	}
	if !rootExited {
		_ = b.Close()
	}
	if b.jobHasMembers() {
		_ = b.enforceJob()
	}
	b.closeOwnedJob()
	select {
	case <-b.pumpDone:
	default:
		drainTimer := time.NewTimer(postTerminationPipeDrainGrace)
		defer drainTimer.Stop()
		select {
		case <-b.pumpDone:
		case <-drainTimer.C:
			select {
			case <-b.pumpDone:
			default:
				b.setWaitError(errors.New("pipe drain incomplete: caller-provided IO did not finish within post-termination drain grace"))
			}
		}
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(b.proc.info.Process, &exitCode); err != nil {
		b.setWaitError(err)
	}
	b.mu.Lock()
	b.report.ExitCode = int(exitCode)
	b.mu.Unlock()
	b.mu.RLock()
	intentionalStop := b.intentionalStop
	b.mu.RUnlock()
	if exitCode != 0 && !intentionalStop {
		b.setWaitError(fmt.Errorf("budgeted session exited with code %d", exitCode))
	}
	b.proc.close()
}

func (b *windowsSessionBackend) beginStopping() {
	b.mu.Lock()
	b.stopping = true
	if b.deadline.IsZero() {
		b.deadline = time.Now().Add(b.ops.teardownTimeout)
	}
	b.mu.Unlock()
}
func (b *windowsSessionBackend) isStopping() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.stopping
}
func (b *windowsSessionBackend) getDeadline() time.Time {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.deadline
}
func (b *windowsSessionBackend) expired() bool {
	d := b.getDeadline()
	return !d.IsZero() && !time.Now().Before(d)
}
func (b *windowsSessionBackend) hasTransportError() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.transportErr != nil
}
func (b *windowsSessionBackend) jobHasMembers() bool {
	b.handleMu.RLock()
	defer b.handleMu.RUnlock()
	if b.jobRetired || b.job == 0 {
		return false
	}
	pids, err := b.ops.queryPIDs(b.job)
	if err != nil {
		b.setWaitError(fmt.Errorf("query Job membership: %w", err))
		return true
	}
	return len(pids) > 0
}
func (b *windowsSessionBackend) jobEmpty() bool { return !b.jobHasMembers() }
func (b *windowsSessionBackend) enforceJob() error {
	b.enforceOnce.Do(func() { b.enforceJobOnce() })
	return b.enforceErr
}
func (b *windowsSessionBackend) enforceJobOnce() {
	b.handleMu.Lock()
	defer b.handleMu.Unlock()
	if b.jobRetired || b.job == 0 {
		return
	}
	termErr := b.ops.terminateJob(b.job, 1)
	if termErr == nil {
		return
	}
	closeErr := b.ops.closeJob(b.job)
	b.jobRetired = true
	if closeErr == nil {
		b.job = 0
	} else {
		b.setWaitError(errors.New("tree termination unverified: TerminateJobObject and close-on-job-close both failed"))
	}
	joined := errors.Join(fmt.Errorf("terminate Job: %w", termErr), wrapIf("close Job fallback", closeErr))
	if closeErr != nil {
		if err := b.ops.terminateProcess(b.proc.info.Process, 1); err != nil {
			joined = errors.Join(joined, fmt.Errorf("best-effort root termination: %w", err))
		}
	}
	b.mu.Lock()
	b.waitErr = errors.Join(b.waitErr, joined)
	b.mu.Unlock()
	b.enforceErr = joined
}
func (b *windowsSessionBackend) closeOwnedJob() {
	b.handleMu.Lock()
	defer b.handleMu.Unlock()
	if b.jobRetired || b.job == 0 {
		return
	}
	err := b.ops.closeJob(b.job)
	b.jobRetired = true
	if err == nil {
		b.job = 0
	} else {
		b.setWaitError(fmt.Errorf("close Job: %w; tree termination unverified", err))
	}
}
func wrapIf(label string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", label, err)
}

func (b *windowsSessionBackend) setWaitError(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	b.waitErr = errors.Join(b.waitErr, err)
	b.mu.Unlock()
}

func (b *windowsSessionBackend) recordSample(lastAt *time.Time, lastCPU *float64) {
	now := time.Now()
	b.handleMu.RLock()
	defer b.handleMu.RUnlock()
	if b.jobRetired || b.job == 0 {
		return
	}
	cpu, err := b.ops.queryCPU(b.job)
	if err != nil {
		return
	}
	pids, err := b.ops.queryPIDs(b.job)
	if err != nil {
		return
	}
	elapsed := now.Sub(*lastAt).Seconds()
	pct := 0.0
	if elapsed > 0 {
		pct = (cpu - *lastCPU) / elapsed * 100
		if b.report.LogicalCPUs > 0 {
			pct /= float64(b.report.LogicalCPUs)
		}
	}
	b.mu.Lock()
	b.report.Samples = append(b.report.Samples, Sample{At: now.Sub(b.started), CPUPercent: pct, PIDs: append([]uint32(nil), pids...)})
	b.report.PIDs = mergePIDs(b.report.PIDs, pids)
	b.report.CPUSeconds = cpu
	b.mu.Unlock()
	*lastCPU, *lastAt = cpu, now
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

type budgetProcess struct {
	info                  windows.ProcessInformation
	stdin, stdout, stderr childPipe
	jobClosed             bool
}

func (p *budgetProcess) close() {
	if p == nil {
		return
	}
	closeChildPipe(p.stdin)
	closeChildPipe(p.stdout)
	closeChildPipe(p.stderr)
	if p.info.Thread != 0 {
		_ = windows.CloseHandle(p.info.Thread)
		p.info.Thread = 0
	}
	if p.info.Process != 0 {
		_ = windows.CloseHandle(p.info.Process)
		p.info.Process = 0
	}
}

func (p *budgetProcess) resume(job windows.Handle) error {
	return p.resumeWithHooks(job, defaultWindowsProcessLaunchHooks())
}

func (p *budgetProcess) resumeWithHooks(job windows.Handle, hooks windowsProcessLaunchHooks) error {
	if hooks.resumeThread == nil {
		hooks.resumeThread = windows.ResumeThread
	}
	if hooks.terminateJob == nil {
		hooks.terminateJob = windows.TerminateJobObject
	}
	if hooks.closeJob == nil {
		hooks.closeJob = windows.CloseHandle
	}
	if _, err := hooks.resumeThread(p.info.Thread); err != nil {
		var cleanupErr error
		terminateErr := hooks.terminateJob(job, 1)
		if terminateErr != nil {
			closeErr := hooks.closeJob(job)
			if closeErr == nil {
				p.jobClosed = true
			}
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("terminate Job after resume failure: %w", terminateErr), closeErr)
		}
		status, waitErr := windows.WaitForSingleObject(p.info.Process, 10_000)
		if waitErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("wait after resume failure: %w", waitErr))
		}
		if status != windows.WAIT_OBJECT_0 {
			killErr := windows.TerminateProcess(p.info.Process, 1)
			if killErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("terminate suspended process after resume failure: %w", killErr))
			}
			status, waitErr = windows.WaitForSingleObject(p.info.Process, 10_000)
			if waitErr != nil || status != windows.WAIT_OBJECT_0 {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("prove process reaped after resume failure: status=%#x wait=%v", status, waitErr))
			}
		}
		return errors.Join(fmt.Errorf("resume budgeted process: %w", err), cleanupErr)
	}
	return nil
}

type windowsProcessLaunchHooks struct {
	updateAttribute func(*windows.ProcThreadAttributeListContainer, uintptr, unsafe.Pointer, uintptr) error
	createProcess   func(*uint16, *uint16, *windows.SecurityAttributes, *windows.SecurityAttributes, bool, uint32, *uint16, *uint16, *windows.StartupInfo, *windows.ProcessInformation) error
	resumeThread    func(windows.Handle) (uint32, error)
	terminateJob    func(windows.Handle, uint32) error
	closeJob        func(windows.Handle) error
}

func defaultWindowsProcessLaunchHooks() windowsProcessLaunchHooks {
	return windowsProcessLaunchHooks{
		updateAttribute: func(attrs *windows.ProcThreadAttributeListContainer, attribute uintptr, value unsafe.Pointer, size uintptr) error {
			return attrs.Update(attribute, value, size)
		},
		createProcess: windows.CreateProcess,
		resumeThread:  windows.ResumeThread,
	}
}

func createSuspendedProcessInJob(job windows.Handle, spec ProcessSpec) (*budgetProcess, error) {
	return createSuspendedProcessInJobWithHooks(job, spec, defaultWindowsProcessLaunchHooks())
}

func createSuspendedProcessInJobWithHooks(job windows.Handle, spec ProcessSpec, hooks windowsProcessLaunchHooks) (*budgetProcess, error) {
	if hooks.updateAttribute == nil {
		hooks.updateAttribute = defaultWindowsProcessLaunchHooks().updateAttribute
	}
	if hooks.createProcess == nil {
		hooks.createProcess = windows.CreateProcess
	}
	proc := &budgetProcess{}
	var err error
	proc.stdin, err = newChildPipe(true)
	if err != nil {
		return nil, err
	}
	proc.stdout, err = newChildPipe(false)
	if err != nil {
		proc.close()
		return nil, err
	}
	proc.stderr, err = newChildPipe(false)
	if err != nil {
		proc.close()
		return nil, err
	}
	childHandles := []windows.Handle{windows.Handle(proc.stdin.child.Fd()), windows.Handle(proc.stdout.child.Fd()), windows.Handle(proc.stderr.child.Fd())}
	jobHandles := []windows.Handle{job}
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		proc.close()
		return nil, fmt.Errorf("allocate process attribute list: %w", err)
	}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&childHandles[0]), uintptr(len(childHandles))*unsafe.Sizeof(childHandles[0])); err != nil {
		proc.close()
		return nil, fmt.Errorf("configure inherited handle list: %w", err)
	}
	defer attrs.Delete()
	if err := hooks.updateAttribute(attrs, procThreadAttributeJobList, unsafe.Pointer(&jobHandles[0]), unsafe.Sizeof(jobHandles[0])); err != nil {
		proc.close()
		return nil, fmt.Errorf("configure creation-time CPU budget Job list: %w", err)
	}
	args := append([]string{spec.Exe}, spec.Args...)
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		proc.close()
		return nil, fmt.Errorf("encode process command line: %w", err)
	}
	applicationName, err := windows.UTF16PtrFromString(spec.Exe)
	if err != nil {
		proc.close()
		return nil, fmt.Errorf("encode process executable: %w", err)
	}
	var currentDir *uint16
	if spec.Dir != "" {
		currentDir, err = windows.UTF16PtrFromString(spec.Dir)
		if err != nil {
			proc.close()
			return nil, fmt.Errorf("encode process directory: %w", err)
		}
	}
	var environment *uint16
	var environmentBlock []uint16
	if spec.Env != nil {
		block := strings.Join(spec.Env, "\x00") + "\x00\x00"
		environmentBlock = utf16.Encode([]rune(block))
		environment = &environmentBlock[0]
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES, StdInput: childHandles[0], StdOutput: childHandles[1], StdErr: childHandles[2]}}
	startup.ProcThreadAttributeList = attrs.List()
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := hooks.createProcess(applicationName, commandLine, nil, nil, true, flags, environment, currentDir, &startup.StartupInfo, &proc.info); err != nil {
		proc.close()
		return nil, fmt.Errorf("create suspended budgeted process in Job: %w", err)
	}
	for _, file := range []*os.File{proc.stdin.child, proc.stdout.child, proc.stderr.child} {
		closeFile(file)
	}
	proc.stdin.child, proc.stdout.child, proc.stderr.child = nil, nil, nil
	inJob, proofErr := processIsInJob(proc.info.Process, job)
	if proofErr != nil || !inJob {
		var observedExit uint32
		_ = windows.GetExitCodeProcess(proc.info.Process, &observedExit)
		_ = windows.TerminateJobObject(job, 1)
		_, _ = windows.WaitForSingleObject(proc.info.Process, 10_000)
		proc.close()
		if proofErr != nil {
			return nil, fmt.Errorf("prove creation-time Job membership: %w", proofErr)
		}
		return nil, fmt.Errorf("prove creation-time Job membership: PID %d is not in Job (exit=%d)", proc.info.ProcessId, observedExit)
	}
	return proc, nil
}

func processIsInJob(process, job windows.Handle) (bool, error) {
	if err := isProcessInJobProc.Find(); err != nil {
		return false, err
	}
	var inJob int32
	r1, _, callErr := isProcessInJobProc.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&inJob)))
	if r1 == 0 {
		return false, callErr
	}
	return inJob != 0, nil
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

func queryJobPIDsStrict(job windows.Handle) ([]uint32, error) {
	const maxPIDs = 1024
	size := int(unsafe.Sizeof(processIDListHeader{})) + maxPIDs*8
	data := make([]byte, size)
	var returned uint32
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&data[0])), uint32(len(data)), &returned); err != nil {
		return nil, err
	}
	header := (*processIDListHeader)(unsafe.Pointer(&data[0]))
	count := int(header.NumberOfProcessIDsInList)
	if count > maxPIDs {
		return nil, fmt.Errorf("Job process list exceeds supported capacity %d", maxPIDs)
	}
	ids := make([]uint32, 0, count)
	base := unsafe.Sizeof(processIDListHeader{})
	for i := 0; i < count; i++ {
		pid := *(*uint64)(unsafe.Pointer(&data[base+uintptr(i)*8]))
		if pid > 0 && pid <= ^uint64(0)>>32 {
			ids = append(ids, uint32(pid))
		}
	}
	return ids, nil
}

func queryJobPIDs(job windows.Handle, fallback uint32) []uint32 {
	ids, err := queryJobPIDsStrict(job)
	if err != nil || len(ids) == 0 {
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
