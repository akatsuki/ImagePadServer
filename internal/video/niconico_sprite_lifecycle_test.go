//go:build nico_sprite_spike

package video

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSpritePipeChild(t *testing.T) {
	role := os.Args[len(os.Args)-1]
	if !strings.HasPrefix(role, "spike-child-") {
		return
	}
	switch role {
	case "spike-child-fail":
		os.Exit(17)
	case "spike-child-read":
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "spike-child-write":
		b := make([]byte, 65536)
		for {
			if _, err := os.Stdout.Write(b); err != nil {
				os.Exit(18)
			}
		}
	case "spike-child-relay":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	}
	os.Exit(0)
}

func TestSpritePipeFailureCleanup(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := func(role string) []string {
		return []string{"-test.run=^TestSpritePipeChild$", "--", "spike-child-" + role}
	}
	for _, name := range []string{"encoder-fail", "native-fail", "capture-fail", "cancel", "native-start", "encoder-start"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			native, encoder := exe, exe
			na, ea := args("write"), args("read")
			var produce func(context.Context, io.Writer) error
			switch name {
			case "encoder-fail":
				ea = args("fail")
			case "native-fail":
				na = args("fail")
			case "capture-fail":
				na = args("relay")
				produce = func(context.Context, io.Writer) error { return fmt.Errorf("intentional capture failure") }
			case "cancel":
				time.AfterFunc(150*time.Millisecond, cancel)
			case "native-start":
				native = exe + ".missing"
			case "encoder-start":
				encoder = exe + ".missing"
			}
			started := time.Now()
			err := spritePipePair(ctx, native, na, encoder, ea, produce)
			if err == nil {
				t.Fatal("accepted failed pipeline")
			}
			if time.Since(started) > 2*time.Second {
				t.Fatalf("cleanup did not return promptly: %v", err)
			}
		})
	}
}
