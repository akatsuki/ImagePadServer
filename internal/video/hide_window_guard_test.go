package video

import (
	"os"
	"strings"
	"testing"
)

// TestEveryExecSpawnHidesWindow guards against console-window flicker on
// Windows: every subprocess spawn in this package must call hideWindow on the
// command so the child console is suppressed. Forgetting the call (as happened
// for the audio analysis and visualizer ffmpeg invocations) makes a command
// prompt flash on every conversion.
func TestEveryExecSpawnHidesWindow(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, "hide_") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "exec.Command(") && !strings.Contains(line, "exec.CommandContext(") {
				continue
			}
			// Scan the spawn site and the following lines (the command may be
			// built across multiple lines) for the hideWindow call.
			found := false
			for j := i; j < len(lines) && j < i+25; j++ {
				if strings.Contains(lines[j], "hideWindow(") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s:%d spawns a subprocess without a following hideWindow(cmd); console window will flash on Windows\n    %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
