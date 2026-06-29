package obsrtmp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

var (
	mediaMTXProcessRegistryMu sync.Mutex
	killOwnedMediaMTX         = video.KillOwnedProcesses
)

func CleanupStaleMediaMTX() (int, error) {
	pids, readErr := readMediaMTXProcessIDs()
	killed, killErr := killOwnedMediaMTX(mediaMTXExecutableName(), "imagepad-mediamtx-", pids)
	removeErr := removeMediaMTXProcessRegistry()

	var errs []string
	if readErr != nil {
		errs = append(errs, readErr.Error())
	}
	if killErr != nil {
		errs = append(errs, killErr.Error())
	}
	if removeErr != nil {
		errs = append(errs, removeErr.Error())
	}
	if len(errs) > 0 {
		return killed, errors.New(strings.Join(errs, "; "))
	}
	return killed, nil
}

func registerMediaMTXProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	mediaMTXProcessRegistryMu.Lock()
	defer mediaMTXProcessRegistryMu.Unlock()
	pids, err := readMediaMTXProcessIDsLocked()
	if err != nil {
		return err
	}
	pids = append(pids, pid)
	return writeMediaMTXProcessIDsLocked(normalizeMediaMTXPIDs(pids))
}

func unregisterMediaMTXProcess(pid int) error {
	mediaMTXProcessRegistryMu.Lock()
	defer mediaMTXProcessRegistryMu.Unlock()
	pids, err := readMediaMTXProcessIDsLocked()
	if err != nil {
		return err
	}
	filtered := pids[:0]
	for _, existing := range pids {
		if existing != pid {
			filtered = append(filtered, existing)
		}
	}
	return writeMediaMTXProcessIDsLocked(normalizeMediaMTXPIDs(filtered))
}

func readMediaMTXProcessIDs() ([]int, error) {
	mediaMTXProcessRegistryMu.Lock()
	defer mediaMTXProcessRegistryMu.Unlock()
	return readMediaMTXProcessIDsLocked()
}

func readMediaMTXProcessIDsLocked() ([]int, error) {
	data, err := os.ReadFile(mediaMTXProcessRegistryPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read MediaMTX process registry: %w", err)
	}
	var pids []int
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}), &pids); err != nil {
		return nil, fmt.Errorf("decode MediaMTX process registry: %w", err)
	}
	return normalizeMediaMTXPIDs(pids), nil
}

func writeMediaMTXProcessIDsLocked(pids []int) error {
	path := mediaMTXProcessRegistryPath()
	if len(pids) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove MediaMTX process registry: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("prepare MediaMTX process registry: %w", err)
	}
	data, err := json.Marshal(pids)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mediamtx-processes-*.tmp")
	if err != nil {
		return fmt.Errorf("create MediaMTX process registry: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write MediaMTX process registry: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync MediaMTX process registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish MediaMTX process registry: %w", err)
	}
	return nil
}

func removeMediaMTXProcessRegistry() error {
	mediaMTXProcessRegistryMu.Lock()
	defer mediaMTXProcessRegistryMu.Unlock()
	if err := os.Remove(mediaMTXProcessRegistryPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove MediaMTX process registry: %w", err)
	}
	return nil
}

func normalizeMediaMTXPIDs(pids []int) []int {
	seen := make(map[int]struct{}, len(pids))
	result := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		result = append(result, pid)
	}
	sort.Ints(result)
	return result
}

func mediaMTXProcessRegistryPath() string {
	return filepath.Join(settings.Dir(), "mediamtx-processes.json")
}
