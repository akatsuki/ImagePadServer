package video

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type DownloadProgress struct {
	Percent int
	Text    string
}

var (
	ytdlpProgressMu sync.RWMutex
	ytdlpProgressCB func(DownloadProgress)
	ytdlpPercentRe  = regexp.MustCompile(`(?i)\[download\]\s+([0-9]+(?:\.[0-9]+)?)%`)
)

func WithYTDLPProgress(cb func(DownloadProgress), fn func() error) error {
	ytdlpProgressMu.Lock()
	prev := ytdlpProgressCB
	ytdlpProgressCB = cb
	ytdlpProgressMu.Unlock()
	defer func() {
		ytdlpProgressMu.Lock()
		ytdlpProgressCB = prev
		ytdlpProgressMu.Unlock()
	}()
	return fn()
}

func runYTDLPCommand(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var output bytes.Buffer
	var outputMu sync.Mutex
	var wg sync.WaitGroup
	consume := func(r io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			outputMu.Lock()
			output.WriteString(line)
			output.WriteByte('\n')
			outputMu.Unlock()
			if progress, ok := parseYTDLPProgress(line); ok {
				reportYTDLPProgress(progress)
			}
		}
	}
	wg.Add(2)
	go consume(stdout)
	go consume(stderr)
	waitErr := cmd.Wait()
	wg.Wait()
	if waitErr != nil {
		outputMu.Lock()
		text := trimOutput(output.Bytes())
		outputMu.Unlock()
		return fmt.Errorf("%w: %s", waitErr, text)
	}
	return nil
}

func parseYTDLPProgress(line string) (DownloadProgress, bool) {
	match := ytdlpPercentRe.FindStringSubmatch(line)
	if len(match) != 2 {
		return DownloadProgress{}, false
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return DownloadProgress{}, false
	}
	percent := int(math.Round(value))
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return DownloadProgress{
		Percent: percent,
		Text:    strings.TrimSpace(line),
	}, true
}

func reportYTDLPProgress(progress DownloadProgress) {
	ytdlpProgressMu.RLock()
	cb := ytdlpProgressCB
	ytdlpProgressMu.RUnlock()
	if cb != nil {
		cb(progress)
	}
}
