//go:build windows

package video

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// assignStartedFFmpegToKillOnCloseJob gives one app-started FFmpeg process its
// own unnamed kill-on-close Job Object. The returned release function owns the
// Job handle and must be called only after cmd.Wait has completed. If the Go
// parent is force-terminated first, Windows closes the handle and kills every
// process still in this Job.
func assignStartedFFmpegToKillOnCloseJob(process *os.Process) (func(), error) {
	if process == nil {
		return nil, errors.New("assign FFmpeg job: nil process")
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create FFmpeg kill-on-close job: %w", err)
	}
	owned := true
	defer func() {
		if owned {
			_ = windows.CloseHandle(job)
		}
	}()

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	ret, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	runtime.KeepAlive(&info)
	if err != nil {
		return nil, fmt.Errorf("configure FFmpeg kill-on-close job: %w", err)
	}
	if ret == 0 {
		return nil, errors.New("configure FFmpeg kill-on-close job: SetInformationJobObject returned zero")
	}

	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(process.Pid),
	)
	if err != nil {
		return nil, fmt.Errorf("open FFmpeg process %d: %w", process.Pid, err)
	}
	defer windows.CloseHandle(processHandle)

	if err := windows.AssignProcessToJobObject(job, processHandle); err != nil {
		return nil, fmt.Errorf("assign FFmpeg process %d to kill-on-close job: %w", process.Pid, err)
	}

	owned = false
	var once sync.Once
	return func() {
		once.Do(func() {
			// cmd.Wait only waits for the process directly started by os/exec.
			// A .cmd/.bat wrapper can leave a real FFmpeg child (or a helper such
			// as ping.exe) alive after that wait returns. Terminate the owned job
			// and wait for the job to become signaled before releasing the handle;
			// otherwise Windows may still hold the wrapper directory when a caller
			// immediately removes its temporary working tree.
			_ = windows.TerminateJobObject(job, 1)
			_, _ = windows.WaitForSingleObject(job, uint32((10 * time.Second) / time.Millisecond))
			_ = windows.CloseHandle(job)
		})
	}, nil
}
