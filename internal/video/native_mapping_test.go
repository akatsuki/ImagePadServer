package video

import "testing"

func TestNativeMappingRejectsInvalidInput(t *testing.T) {
	if _, err := OpenNativeMapping("", 0); err == nil { t.Fatal("expected invalid mapping input") }
}
