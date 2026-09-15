package obsrtmp

import (
	"fmt"
	"os"
	"strings"

	"imagepadserver/internal/video"
)

const rtspDiagnosticTuneEnv = "IMAGEPAD_RTSP_DIAGNOSTIC_TUNE"

// Mandatory even with zerolatency, diagnostic tunes or a CPU fallback.
// See docs/RTSP_H264_COMPATIBILITY_CONTRACT.md; preserve these independently
// of the preset, bitrate and GOP. RTP packet size is a separate constraint.
const rtspX264SingleSliceOptions = "aud=1:repeat-headers=1:sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0"

// RTSPEncoderDiagnosticPolicy is an explicit, RTSP-only comparison contract.
// It permits changing the encoder tune while retaining the active contract's
// encoder and every other FFmpeg argument.
type RTSPEncoderDiagnosticPolicy struct {
	Encoder video.VideoEncoderProfile
	Tune    string
}

// FFmpegRTSPArgsForDiagnostic returns the RTSP argv for a tune comparison.
// The normal RTSP path remains on ffmpegRTSPArgsForContract; this opt-in API
// is intended for manifests and diagnostic hooks that need the actual argv.
func (m *Manager) FFmpegRTSPArgsForDiagnostic(contract OBSActiveSessionContract, recording, rtspURL string, policy RTSPEncoderDiagnosticPolicy) ([]string, error) {
	if err := validateRTSPEncoderDiagnosticPolicy(contract, policy); err != nil {
		return nil, err
	}
	args := m.ffmpegRTSPArgsForContract(contract, recording, rtspURL)
	if policy.Tune == "" {
		return args, nil
	}
	return replaceRTSPDiagnosticTune(args, policy.Tune), nil
}

// ffmpegRTSPArgsForActiveContract keeps the normal path unchanged unless an
// explicit diagnostic tune is requested through the process environment. The
// opt-in is deliberately resolved at launch time so a production session does
// not inherit a tune from a previous diagnostic run.
func (m *Manager) ffmpegRTSPArgsForActiveContract(contract OBSActiveSessionContract, recording, rtspURL string) ([]string, error) {
	tune := strings.TrimSpace(os.Getenv(rtspDiagnosticTuneEnv))
	if tune == "" {
		return m.ffmpegRTSPArgsForContract(contract, recording, rtspURL), nil
	}
	return m.FFmpegRTSPArgsForDiagnostic(contract, recording, rtspURL, RTSPEncoderDiagnosticPolicy{
		Encoder: contract.VideoEncoderProfile,
		Tune:    tune,
	})
}

func validateRTSPEncoderDiagnosticPolicy(contract OBSActiveSessionContract, policy RTSPEncoderDiagnosticPolicy) error {
	if policy.Encoder.Name != contract.VideoEncoderProfile.Name || policy.Encoder.Hardware != contract.VideoEncoderProfile.Hardware {
		return fmt.Errorf("RTSP diagnostic policy encoder %q does not match contract encoder %q", policy.Encoder.Name, contract.VideoEncoderProfile.Name)
	}
	if policy.Encoder.Name != "libx264" && policy.Encoder.Name != "h264_nvenc" {
		return fmt.Errorf("RTSP diagnostic policy does not support encoder %q", policy.Encoder.Name)
	}
	if !validRTSPDiagnosticTune(policy.Encoder.Name, policy.Tune) {
		return fmt.Errorf("unsupported RTSP diagnostic tune %q for encoder %q", policy.Tune, policy.Encoder.Name)
	}
	return nil
}

func validRTSPDiagnosticTune(encoder, tune string) bool {
	if tune == "" {
		return true
	}
	switch encoder {
	case "libx264":
		switch tune {
		case "zerolatency", "animation", "film", "grain", "stillimage", "fastdecode":
			return true
		}
	case "h264_nvenc":
		switch tune {
		case "hq", "ll", "ull":
			return true
		}
	}
	return false
}

func replaceRTSPDiagnosticTune(args []string, tune string) []string {
	result := append([]string(nil), args...)
	for i := 0; i+1 < len(result); i++ {
		if result[i] == "-tune" {
			result[i+1] = tune
			return result
		}
	}
	// Keep the diagnostic override in the video-encoder option group. FFmpeg
	// accepts the option after the encoder's existing private options.
	insertAt := len(result)
	for i, arg := range result {
		if arg == "-pix_fmt" {
			insertAt = i
			break
		}
	}
	result = append(result, "", "")
	copy(result[insertAt+2:], result[insertAt:len(result)-2])
	result[insertAt], result[insertAt+1] = "-tune", tune
	return result
}
