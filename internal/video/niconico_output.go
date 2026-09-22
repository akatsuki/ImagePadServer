package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

// NicoOutputMode selects how one Nico export produces its media artifacts.
// An empty value is the legacy separate-output behavior.
type NicoOutputMode string

const (
	NicoOutputSeparate NicoOutputMode = "separate"
	NicoOutputTee      NicoOutputMode = "tee"
)

// NicoExportOutput is the validated result contract shared by separate and
// tee exporters. T2 is responsible for populating it after both artifacts
// have completed validation.
type NicoExportOutput struct {
	Encode NicoEncodeReport
	HLS    NicoHLSReport
	Mode   NicoOutputMode
}

// ExportNicoCommented is the common output contract for browser and native
// renderers. The encoder owns MP4/HLS completion; callers only publish after
// this function returns both validated artifacts.
func ExportNicoCommented(ctx context.Context, ffmpeg, sourcePath, outputPath, hlsStagingDir string, snapshot niconico.Snapshot, renderOptions nicorender.RenderOptions, encodeOptions NicoEncodeOptions, mode NicoOutputMode) (NicoExportOutput, nicorender.RenderReport, error) {
	normalized, err := NormalizeNicoOutputMode(mode)
	if err != nil {
		return NicoExportOutput{}, nicorender.RenderReport{}, err
	}
	if err := ValidateNicoOutputStaging(sourcePath, outputPath, hlsStagingDir); err != nil {
		return NicoExportOutput{}, nicorender.RenderReport{}, err
	}
	encodeOptions.OutputMode = normalized
	encodeOptions.HLSOutputDir = hlsStagingDir
	encoded, renderReport, err := EncodeNicoCommentedWithRenderer(ctx, ffmpeg, sourcePath, outputPath, snapshot, renderOptions, encodeOptions)
	if err != nil {
		return NicoExportOutput{}, renderReport, err
	}
	var hls NicoHLSReport
	if normalized == NicoOutputSeparate {
		hls, err = CreateNicoHLS(ctx, ffmpeg, outputPath, hlsStagingDir)
	} else {
		segments, validateErr := validateNicoPlaylist(filepath.Join(hlsStagingDir, "playlist.m3u8"), hlsStagingDir)
		if validateErr != nil {
			err = validateErr
		} else {
			hls = NicoHLSReport{PlaylistPath: filepath.Join(hlsStagingDir, "playlist.m3u8"), Segments: segments}
		}
	}
	if err != nil {
		return NicoExportOutput{}, renderReport, err
	}
	return NicoExportOutput{Encode: encoded, HLS: hls, Mode: normalized}, renderReport, nil
}

// NormalizeNicoOutputMode applies the legacy empty default and rejects every
// value that could select an unimplemented or unsafe output path.
func NormalizeNicoOutputMode(mode NicoOutputMode) (NicoOutputMode, error) {
	switch mode {
	case "", NicoOutputSeparate:
		return NicoOutputSeparate, nil
	case NicoOutputTee:
		return NicoOutputTee, nil
	default:
		return "", fmt.Errorf("niconico: invalid output mode %q", mode)
	}
}

// ValidateNicoOutputStaging checks the caller-provided paths without creating,
// replacing, or deleting anything. The worker may create the staging parent
// after validation; the MP4 and HLS paths must still be new when validation
// runs.
func ValidateNicoOutputStaging(sourcePath, outputPath, hlsStagingDir string) error {
	if strings.TrimSpace(sourcePath) == "" {
		return errors.New("niconico: source path is required")
	}
	if strings.TrimSpace(outputPath) == "" {
		return errors.New("niconico: output path is required")
	}
	if strings.TrimSpace(hlsStagingDir) == "" {
		return errors.New("niconico: HLS staging path is required")
	}

	source, err := absoluteNicoOutputPath(sourcePath)
	if err != nil {
		return fmt.Errorf("niconico: source path: %w", err)
	}
	output, err := absoluteNicoOutputPath(outputPath)
	if err != nil {
		return fmt.Errorf("niconico: output path: %w", err)
	}
	hls, err := absoluteNicoOutputPath(hlsStagingDir)
	if err != nil {
		return fmt.Errorf("niconico: HLS staging path: %w", err)
	}
	if sameNicoOutputPath(source, output) {
		return errors.New("niconico: source and output paths must differ")
	}
	if sameNicoOutputPath(source, hls) {
		return errors.New("niconico: source and HLS staging paths must differ")
	}
	if sameNicoOutputPath(output, hls) {
		return errors.New("niconico: output and HLS staging paths must differ")
	}
	if filepath.Dir(output) != filepath.Dir(hls) {
		return errors.New("niconico: output and HLS staging must share a staging parent")
	}
	if _, err := os.Lstat(output); err == nil {
		return errors.New("niconico: output MP4 already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("niconico: inspect output MP4: %w", err)
	}
	if _, err := os.Lstat(hls); err == nil {
		return errors.New("niconico: HLS staging already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("niconico: inspect HLS staging: %w", err)
	}
	return nil
}

func absoluteNicoOutputPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func sameNicoOutputPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
