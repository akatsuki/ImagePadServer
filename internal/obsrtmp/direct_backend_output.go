package obsrtmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"imagepadserver/internal/airplaycontract"
)

var errDirectBackendOutputInvalid = errors.New("direct backend decoded video evidence is invalid")

// This is downstream decode evidence, not source-AU identity, a full GOP audit,
// audio readiness, backend liveness, or permission to promote a generation.
// The coordinator must combine it with the T7 proof for the exact owned route.
type directBackendDecodedOutput struct {
	Width, Height, Frames, KeyFrames int
	FirstPTS, LastPTS                float64
}

type directBackendOutputProof struct {
	descriptor airplaycontract.DeliveryGeneration
	route      directBackendRoute
	decoded    directBackendDecodedOutput
	observedAt time.Time
}

func (p directBackendOutputProof) matches(descriptor airplaycontract.DeliveryGeneration, route directBackendRoute) bool {
	return !p.observedAt.IsZero() && p.descriptor == descriptor && p.route == route &&
		p.decoded.Frames >= 2 && p.decoded.KeyFrames > 0 && p.decoded.Width == descriptor.Output.Width &&
		p.decoded.Height == descriptor.Output.Height && p.decoded.LastPTS > p.decoded.FirstPTS
}

// Route and descriptor are immutable snapshots. The session coordinator must
// still validate its ledger, stop/deadline state and both processes at commit.
func probeDirectBackendGeneration(ctx context.Context, ffprobe string, descriptor airplaycontract.DeliveryGeneration, route directBackendRoute, command directRecordingProbeCommandFunc) (directBackendOutputProof, error) {
	empty := directBackendOutputProof{}
	if validateDirectBackendRoute(route) != nil || route.runtime == nil ||
		descriptor.SessionID != route.sessionID || descriptor.SessionEpoch != route.sessionEpoch ||
		descriptor.Generation != route.generation || descriptor.RequestID != route.requestID ||
		descriptor.ArtifactPaths.SessionID != descriptor.SessionID || descriptor.ArtifactPaths.Generation != descriptor.Generation ||
		descriptor.ExpectedRecordingWidth != descriptor.Output.Width || descriptor.ExpectedRecordingHeight != descriptor.Output.Height ||
		route.runtime.cfg.Path != mediaMTXPathName(descriptor.SessionID) ||
		route.runtime.cfg.Ports.BackendRTSP != route.privateRTSPPort ||
		route.runtime.cfg.Ports.RTSP == route.privateRTSPPort {
		return empty, errDirectBackendOutputInvalid
	}
	decoded, err := runDirectBackendFFprobe(ctx, ffprobe, route.runtime.backendRTSPURL(), descriptor.Output.Width, descriptor.Output.Height, command)
	if err != nil {
		return empty, err
	}
	if ctx != nil && ctx.Err() != nil {
		return empty, ctx.Err()
	}
	return directBackendOutputProof{descriptor: descriptor, route: route, decoded: decoded, observedAt: time.Now()}, nil
}

func decodeDirectBackendOutputJSON(data []byte, width, height int) (directBackendDecodedOutput, error) {
	var document struct {
		Streams []struct {
			Codec         string `json:"codec_name"`
			Width, Height int
		}
		Frames []struct {
			MediaType     string `json:"media_type"`
			KeyFrame      int    `json:"key_frame"`
			PictureType   string `json:"pict_type"`
			Width, Height int
			PTS           string `json:"best_effort_timestamp_time"`
		}
	}
	invalid := func() (directBackendDecodedOutput, error) {
		return directBackendDecodedOutput{}, errDirectBackendOutputInvalid
	}
	if width <= 0 || height <= 0 || len(data) > directRecordingProbeOutputLimit || json.Unmarshal(data, &document) != nil {
		return invalid()
	}
	if len(document.Streams) != 1 || document.Streams[0].Codec != "h264" || document.Streams[0].Width != width || document.Streams[0].Height != height || len(document.Frames) < 2 {
		return invalid()
	}
	proof := directBackendDecodedOutput{Width: width, Height: height}
	var previousPTS float64
	hasPTS := false
	for i, frame := range document.Frames {
		if frame.MediaType != "video" || frame.Width != width || frame.Height != height ||
			(frame.PictureType != "I" && frame.PictureType != "P") || (frame.KeyFrame != 0 && frame.KeyFrame != 1) ||
			(frame.KeyFrame == 1 && frame.PictureType != "I") {
			return invalid()
		}
		if i == 0 && (frame.KeyFrame != 1 || frame.PictureType != "I") {
			return invalid()
		}
		// A real RTSP join can produce one untimed initial decoded I-frame.
		// Do not invent its time or use its P-frames as an anchor. Observe the
		// next fully timed keyframe instead; missing times anywhere else fail.
		if i == 0 && frame.PTS == "" {
			continue
		}
		pts, err := strconv.ParseFloat(frame.PTS, 64)
		if err != nil || math.IsNaN(pts) || math.IsInf(pts, 0) || (hasPTS && pts <= previousPTS) {
			return invalid()
		}
		previousPTS, hasPTS = pts, true
		if proof.Frames == 0 {
			if frame.KeyFrame != 1 {
				continue
			}
			proof.FirstPTS = pts
		}
		if frame.KeyFrame == 1 {
			proof.KeyFrames++
		}
		proof.Frames++
		proof.LastPTS = pts
	}
	if proof.Frames < 2 {
		return invalid()
	}
	return proof, nil
}

// Use only the candidate's credential-free loopback read URL. A public gate or
// old recording file must never satisfy candidate output readiness. The native
// source proof is separate; ffprobe actually decodes frames instead of merely
// reading SDP/stream metadata. The caller supplies the generation-owned URL.
func runDirectBackendFFprobe(parent context.Context, ffprobe, endpoint string, width, height int, command directRecordingProbeCommandFunc) (directBackendDecodedOutput, error) {
	empty := directBackendDecodedOutput{}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "rtsp" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || u.Path == "/" {
		return empty, errDirectBackendOutputInvalid
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 || width <= 0 || height <= 0 || strings.TrimSpace(ffprobe) == "" || command == nil {
		return empty, errDirectBackendOutputInvalid
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	cmd := command(ctx, ffprobe, "-v", "error", "-rtsp_transport", "tcp",
		// The longest supported profile GOP is four seconds. Five seconds
		// allows a timed keyframe and its successor after a join-time prefix;
		// the whole subprocess still has the independent ten-second deadline.
		"-select_streams", "v:0", "-read_intervals", "%+5", "-show_frames", "-show_streams",
		"-show_entries", "stream=codec_name,width,height:frame=media_type,key_frame,pict_type,width,height,best_effort_timestamp_time",
		"-of", "json", endpoint)
	if cmd == nil {
		return empty, errDirectBackendOutputInvalid
	}
	stdout := &directRecordingProbeBuffer{limit: directRecordingProbeOutputLimit}
	stderr := &directRecordingProbeBuffer{limit: directRecordingProbeErrorLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = directRecordingProbeWaitDelay
	hideWindow(cmd)
	runErr := cmd.Run()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if runErr != nil {
		return empty, fmt.Errorf("direct backend ffprobe failed: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	if stdout.overflow || stderr.overflow || strings.TrimSpace(stderr.String()) != "" {
		// Decoder errors may accompany exit zero. Never accept partial output.
		return empty, errDirectBackendOutputInvalid
	}
	proof, err := decodeDirectBackendOutputJSON(stdout.Bytes(), width, height)
	if contextErr := ctx.Err(); contextErr != nil {
		return empty, contextErr
	}
	return proof, err
}
