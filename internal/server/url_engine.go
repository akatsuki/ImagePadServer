package server

import (
	"net/url"
	"strings"

	"imagepadserver/internal/library"
	"imagepadserver/internal/obsrtmp"
)

type urlSourceKind string

const (
	urlSourceUnknown urlSourceKind = ""
	urlSourceImage   urlSourceKind = "image"
	urlSourceVideo   urlSourceKind = "video"
	urlSourceMusic   urlSourceKind = "music"
	urlSourceOBS     urlSourceKind = "obs"
)

type outputProtocol string

const (
	outputImagePad outputProtocol = "imagepad"
	outputHLS      outputProtocol = "hls"
	outputRTSP     outputProtocol = "rtsp"
)

type urlIssueRequest struct {
	State    map[string]interface{}
	Source   urlSourceKind
	Protocol outputProtocol
}

type urlIssueResult struct {
	URL   string
	Label string
}

type urlEngine interface {
	Issue(urlIssueRequest) urlIssueResult
}

type imagePadURLEngine struct{}

func (imagePadURLEngine) Issue(req urlIssueRequest) urlIssueResult {
	state := req.State
	if imageURL, ok := state["imageURL"].(string); ok && strings.HasPrefix(imageURL, "http") {
		return urlIssueResult{URL: imageURL, Label: "ImagePad URL"}
	}
	if publicURL, ok := state["publicImageURL"].(string); ok && strings.HasPrefix(publicURL, "http") {
		return urlIssueResult{URL: publicURL, Label: "ImagePad URL"}
	}
	if localURL, ok := state["localImageURL"].(string); ok && strings.HasPrefix(localURL, "http") {
		return urlIssueResult{URL: localURL, Label: "Local URL"}
	}
	return urlIssueResult{Label: "URL"}
}

type hlsURLEngine struct{}

func (hlsURLEngine) Issue(req urlIssueRequest) urlIssueResult {
	state := req.State
	if videoPlayer, ok := state["videoPlayer"].(map[string]interface{}); ok {
		if enabled, _ := videoPlayer["enabled"].(bool); enabled {
			if hlsURL, ok := state["hlsURL"].(string); ok && strings.HasPrefix(hlsURL, "http") {
				return urlIssueResult{URL: hlsURL, Label: "HLS URL"}
			}
		}
	}
	return urlIssueResult{Label: "URL"}
}

type obsRTSPURLEngine struct{}

func (obsRTSPURLEngine) Issue(req urlIssueRequest) urlIssueResult {
	state := req.State
	obsLatency, _ := state["obsLatency"].(obsrtmp.LatencyProfile)
	if obsStatus, ok := state["obs"].(obsrtmp.Status); ok &&
		activeOBSLatency(obsLatency, obsStatus).Transport == obsrtmp.LatencyModeRTSPT &&
		(obsStatus.Connected || obsStatus.Publishing) {
		if strings.HasPrefix(obsStatus.RTSPTURL, "rtsp://") {
			return urlIssueResult{URL: obsStatus.RTSPTURL, Label: "RTSP TCP URL"}
		}
	}
	return urlIssueResult{Label: "URL"}
}

type obsURLEngine struct {
	rtsp obsRTSPURLEngine
}

func (e obsURLEngine) Issue(req urlIssueRequest) urlIssueResult {
	return e.rtsp.Issue(req)
}

type urlEngineRegistry struct {
	image imagePadURLEngine
	hls   hlsURLEngine
	obs   obsURLEngine
}

var defaultURLEngines = urlEngineRegistry{
	image: imagePadURLEngine{},
	hls:   hlsURLEngine{},
	obs: obsURLEngine{
		rtsp: obsRTSPURLEngine{},
	},
}

func (r urlEngineRegistry) Issue(req urlIssueRequest) urlIssueResult {
	switch req.Protocol {
	case outputImagePad:
		return r.image.Issue(req)
	case outputRTSP:
		if req.Source == urlSourceOBS {
			return r.obs.Issue(req)
		}
		return urlIssueResult{Label: "URL"}
	case outputHLS:
		return r.hls.Issue(req)
	default:
		return urlIssueResult{Label: "URL"}
	}
}

func urlRequestForShareMode(state map[string]interface{}, mode string) urlIssueRequest {
	source := urlSourceFromState(state)
	switch mode {
	case "obs_rtsp":
		return urlIssueRequest{State: state, Source: urlSourceOBS, Protocol: outputRTSP}
	case "obs_hls":
		return urlIssueRequest{State: state, Source: urlSourceOBS, Protocol: outputHLS}
	case "obs":
		if obsLatency, _ := state["obsLatency"].(obsrtmp.LatencyProfile); obsLatency.Transport == obsrtmp.LatencyModeRTSPT {
			return urlIssueRequest{State: state, Source: urlSourceOBS, Protocol: outputRTSP}
		}
		return urlIssueRequest{State: state, Source: urlSourceOBS, Protocol: outputHLS}
	case "file", "link":
		return urlIssueRequest{State: state, Source: source, Protocol: defaultProtocolForSource(source)}
	default:
		return urlIssueRequest{State: state, Source: source, Protocol: defaultProtocolForSource(source)}
	}
}

func defaultProtocolForSource(source urlSourceKind) outputProtocol {
	switch source {
	case urlSourceImage:
		return outputImagePad
	case urlSourceVideo, urlSourceMusic, urlSourceOBS:
		return outputHLS
	default:
		return outputHLS
	}
}

func urlSourceFromState(state map[string]interface{}) urlSourceKind {
	if current, _ := state["current"].(*library.CurrentImage); current != nil {
		return urlSourceFromCurrent(*current)
	}
	if current, _ := state["current"].(library.CurrentImage); current.ID != "" {
		return urlSourceFromCurrent(current)
	}
	if current, _ := state["current"].(map[string]interface{}); current != nil {
		kind, _ := current["kind"].(string)
		sourceKind, _ := current["sourceKind"].(string)
		return urlSourceFromFields(kind, sourceKind)
	}
	if imageURL, _ := state["imageURL"].(string); strings.HasPrefix(imageURL, "http") {
		return urlSourceImage
	}
	if publicURL, _ := state["publicImageURL"].(string); strings.HasPrefix(publicURL, "http") {
		return urlSourceImage
	}
	if localURL, _ := state["localImageURL"].(string); strings.HasPrefix(localURL, "http") {
		return urlSourceImage
	}
	if hlsURL, _ := state["hlsURL"].(string); strings.HasPrefix(hlsURL, "http") {
		return urlSourceVideo
	}
	return urlSourceUnknown
}

func urlSourceFromCurrent(current library.CurrentImage) urlSourceKind {
	return urlSourceFromFields(current.Kind, current.SourceKind)
}

func urlSourceFromFields(kind, sourceKind string) urlSourceKind {
	switch {
	case sourceKind == "obs":
		return urlSourceOBS
	case sourceKind == "soundcloud" || sourceKind == "local_audio" || sourceKind == "remote_audio":
		return urlSourceMusic
	case kind == "video":
		return urlSourceVideo
	case kind == "image" || kind == "":
		return urlSourceImage
	default:
		return urlSourceUnknown
	}
}

func urlForClipboard(state map[string]interface{}) string {
	shareURL, _ := primaryShareURL(state)
	if strings.HasPrefix(shareURL, "http") {
		return shareURL
	}
	if shareURL, _ := state["shareURL"].(string); strings.HasPrefix(shareURL, "http") {
		return shareURL
	}
	return urlForCopyTarget(state, "imageURL")
}

func primaryShareURL(state map[string]interface{}) (string, string) {
	return shareURLForMode(state, shareModeFromState(state))
}

func shareURLForMode(state map[string]interface{}, mode string) (string, string) {
	result := defaultURLEngines.Issue(urlRequestForShareMode(state, mode))
	if result.URL != "" || result.Label != "" {
		return result.URL, result.Label
	}
	return "", "URL"
}

func withResolvedShareURLs(state map[string]interface{}) map[string]interface{} {
	if state == nil {
		return nil
	}
	targets := map[string]interface{}{}
	for _, mode := range []string{"file", "link", "obs", "obs_rtsp", "obs_hls"} {
		shareURL, shareURLLabel := shareURLForMode(state, mode)
		targets[mode] = map[string]interface{}{
			"shareURL":      shareURL,
			"shareURLLabel": shareURLLabel,
		}
	}
	state["shareTargets"] = targets
	shareURL, shareURLLabel := primaryShareURL(state)
	state["shareURL"] = shareURL
	state["shareURLLabel"] = shareURLLabel
	return state
}

func shareModeFromState(state map[string]interface{}) string {
	if mode, _ := state["shareMode"].(string); mode == "obs" || mode == "obs_rtsp" || mode == "obs_hls" || mode == "link" || mode == "file" {
		return mode
	}
	if mode, _ := state["historyTargetMode"].(string); mode == "link" || mode == "file" {
		return mode
	}
	if current, _ := state["current"].(*library.CurrentImage); current != nil {
		if current.Kind == "video" && currentScopedHLSURL(state, current.ID) {
			return "link"
		}
		mode := historyTargetMode(*current)
		if mode != "obs" {
			return mode
		}
	}
	if current, _ := state["current"].(library.CurrentImage); current.ID != "" {
		if current.Kind == "video" && currentScopedHLSURL(state, current.ID) {
			return "link"
		}
		mode := historyTargetMode(current)
		if mode != "obs" {
			return mode
		}
	}
	if hlsURL, _ := state["hlsURL"].(string); strings.HasPrefix(hlsURL, "http") {
		return "link"
	}
	return ""
}

func currentScopedHLSURLForState(state map[string]interface{}) bool {
	if current, _ := state["current"].(*library.CurrentImage); current != nil {
		return currentScopedHLSURL(state, current.ID)
	}
	if current, _ := state["current"].(library.CurrentImage); current.ID != "" {
		return currentScopedHLSURL(state, current.ID)
	}
	return false
}

func currentScopedHLSURL(state map[string]interface{}, id string) bool {
	if id == "" {
		return false
	}
	hlsURL, _ := state["hlsURL"].(string)
	return strings.HasPrefix(hlsURL, "http") && strings.Contains(hlsURL, "/stream/"+url.PathEscape(id)+"/")
}

func currentMediaKind(state map[string]interface{}) string {
	if current, _ := state["current"].(*library.CurrentImage); current != nil {
		return current.Kind
	}
	if current, _ := state["current"].(library.CurrentImage); current.ID != "" {
		return current.Kind
	}
	if current, _ := state["current"].(map[string]interface{}); current != nil {
		if kind, _ := current["kind"].(string); kind != "" {
			return kind
		}
	}
	return ""
}

func activeOBSLatency(selected obsrtmp.LatencyProfile, status obsrtmp.Status) obsrtmp.LatencyProfile {
	if selected.Mode != "" {
		return selected
	}
	return status.Latency
}

func urlForCopyTarget(state map[string]interface{}, target string) string {
	switch target {
	case "shareURL":
		shareURL, _ := primaryShareURL(state)
		if shareURL != "" {
			return shareURL
		}
		if shareURL, ok := state["shareURL"].(string); ok {
			return shareURL
		}
	case "phoneURL", "phoneURLMobile":
		if phoneURL, ok := state["phoneURL"].(string); ok {
			return phoneURL
		}
	case "localImageURL":
		if localURL, ok := state["localImageURL"].(string); ok {
			return localURL
		}
	case "publicImageURL":
		if publicURL, ok := state["publicImageURL"].(string); ok {
			return publicURL
		}
	case "videoURL":
		if videoURL, ok := state["videoURL"].(string); ok {
			return videoURL
		}
	case "hlsURL":
		if hlsURL, ok := state["hlsURL"].(string); ok {
			return hlsURL
		}
	case "publicVideoURL":
		if publicURL, ok := state["publicVideoURL"].(string); ok {
			return publicURL
		}
	case "publicHLSURL":
		if publicURL, ok := state["publicHLSURL"].(string); ok {
			return publicURL
		}
	case "obsServerAddress":
		if obs, ok := state["obs"].(obsrtmp.Status); ok {
			return obs.ServerAddress
		}
	case "obsStreamKey":
		if obs, ok := state["obs"].(obsrtmp.Status); ok {
			return obs.StreamKey
		}
	default:
		if imageURL, ok := state["imageURL"].(string); ok && strings.HasPrefix(imageURL, "http") {
			return imageURL
		}
		if publicURL, ok := state["publicImageURL"].(string); ok && publicURL != "" {
			return publicURL
		}
		if localURL, ok := state["localImageURL"].(string); ok {
			return localURL
		}
	}
	return ""
}
