package video

import "testing"

func TestValidateGPUVideoColorMetadata(t *testing.T) {
	probe := "pix_fmt=yuv420p\ncolor_range=tv\ncolor_space=bt709\ncolor_transfer=bt709\ncolor_primaries=bt709\n"
	if err := ValidateGPUVideoColorMetadata(probe); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGPUVideoColorMetadata("pix_fmt=yuv420p\ncolor_range=tv\n"); err == nil {
		t.Fatal("incomplete color metadata accepted")
	}
}
