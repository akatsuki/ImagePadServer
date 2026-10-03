package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"imagepadserver/internal/nicoexportbudget"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/xpostexport"
)

var xpostWorkerExecutable = os.Executable
var xpostRunWorker = runXPostWorker

type xpostEventCollector struct {
	mu         sync.Mutex
	line       []byte
	request    xpostexport.Request
	result     *xpostexport.Result
	seen       bool
	err        error
	onProgress func(int, string)
	onVoice    func(xpostexport.Event) error
	cancel     context.CancelFunc
}

func (c *xpostEventCollector) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}
	fail := func(n int, err error) (int, error) {
		c.err = err
		if c.cancel != nil {
			c.cancel()
		}
		return n, err
	}
	for i, b := range data {
		if b != '\n' {
			c.line = append(c.line, b)
			if len(c.line) > 64*1024 {
				return fail(i+1, errors.New("X worker event exceeds 64 KiB"))
			}
			continue
		}
		var e xpostexport.Event
		err := json.Unmarshal(c.line, &e)
		c.line = c.line[:0]
		if err != nil {
			return fail(i, err)
		}
		if e.JobID != c.request.JobID || c.seen {
			return fail(i, errors.New("X worker event identity or ordering mismatch"))
		}
		switch e.Type {
		case "progress":
			if c.onProgress != nil {
				c.onProgress(e.Percent, e.Message)
			}
		case "voice":
			v := c.request.Voice
			if e.Voice == nil || v == nil || e.Voice.SpeakerUUID != v.SpeakerUUID || e.Voice.StyleID != v.StyleID || e.Voice.Speed != v.Speed || e.Voice.EngineURL != v.EngineURL || e.Voice.Managed != v.Managed {
				return fail(i, errors.New("X worker voice selection mismatch"))
			}
			if c.onVoice != nil {
				if err := c.onVoice(e); err != nil {
					return fail(i, err)
				}
			}
		case "result":
			c.seen = true
			if e.Error != "" {
				return fail(i, errors.New(e.Error))
			}
			if e.Result == nil {
				return fail(i, errors.New("X worker result missing"))
			}
			c.result = e.Result
		default:
			return fail(i, errors.New("unknown X worker event"))
		}
	}
	return len(data), nil
}

func (c *xpostEventCollector) finish() (*xpostexport.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	if len(c.line) != 0 || c.result == nil {
		return nil, errors.New("X worker terminated without a complete result")
	}
	r := c.result
	for _, pair := range [][2]string{{r.OutputPath, "output.mp4"}, {r.SnapshotPath, "snapshot.json"}, {r.ThumbnailPath, "thumbnail.jpg"}, {r.HLSDir, "output.mp4.hls"}} {
		if filepath.Clean(pair[0]) != filepath.Join(c.request.WorkDir, pair[1]) {
			return nil, errors.New("X worker output path mismatch")
		}
	}
	if r.Frames <= 0 || r.Duration <= 0 {
		return nil, errors.New("X worker returned empty video")
	}
	return r, nil
}

func runXPostWorker(ctx context.Context, r xpostexport.Request, progress func(int, string)) (*xpostexport.Result, nicoexportbudget.Report, error) {
	if err := r.Validate(); err != nil {
		return nil, nicoexportbudget.Report{}, err
	}
	exe, err := xpostWorkerExecutable()
	if err != nil {
		return nil, nicoexportbudget.Report{}, err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return nil, nicoexportbudget.Report{}, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &xpostEventCollector{request: r, onProgress: progress, cancel: cancel, onVoice: func(e xpostexport.Event) error {
		return settings.Update(func(s *settings.Settings) error { v := *e.Voice; s.LastXPostVoice = &v; return nil })
	}}
	tail := &nicoWorkerStderrTail{}
	spec := nicoexportbudget.ProcessSpec{Exe: exe, Args: []string{"xpost-export-worker"}, Dir: filepath.Dir(exe), Stdin: bytes.NewReader(append(data, '\n')), Stdout: c, Stderr: tail}
	var report nicoexportbudget.Report
	if runtime.GOOS == "windows" {
		report, err = nicoexportbudget.Run(childCtx, spec, nicoexportbudget.Options{Percent: 20})
	} else {
		err = runXPostPortable(childCtx, spec)
		report.Reason = "CPU上限はWindowsでのみ適用されます"
	}
	if err != nil {
		if c.err != nil {
			return nil, report, c.err
		}
		return nil, report, fmt.Errorf("X動画の生成に失敗しました: %w: %s", err, tail.String())
	}
	if runtime.GOOS == "windows" && !report.Verified {
		return nil, report, fmt.Errorf("X worker CPU budget unverified: %s", report.Reason)
	}
	result, err := c.finish()
	return result, report, err
}
