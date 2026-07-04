package server

import "testing"

func TestLocalVideoUploadLimitAllowsLargeLANUploads(t *testing.T) {
	if maxLocalVideoUploadBytes < 3<<30 {
		t.Fatalf("maxLocalVideoUploadBytes = %d, want at least 3 GiB", maxLocalVideoUploadBytes)
	}
	if uploadMemoryLimit() != maxMultipartMemory {
		t.Fatalf("uploadMemoryLimit() = %d, want %d", uploadMemoryLimit(), maxMultipartMemory)
	}
}
