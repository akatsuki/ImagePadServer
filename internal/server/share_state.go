package server

import (
	"strings"

	"imagepadserver/internal/library"
	"imagepadserver/internal/obsrtmp"
)

// SourceKind は公開メディアの種別（image/video/music/obs）を表す型付き enum。
// 現状は Kind / SourceKind の文字列マッチが url_engine.go の
// urlSourceFromFields に散在しているため、判定をこの型へ集約する（原因 5）。
type SourceKind string

const (
	SourceKindUnknown SourceKind = ""
	SourceKindImage   SourceKind = "image"
	SourceKindVideo   SourceKind = "video"
	SourceKindMusic   SourceKind = "music"
	SourceKindOBS     SourceKind = "obs"
)

// ShareMode は共有 URL の導出モード（file/link/obs/obs_rtsp/obs_hls）を表す。
type ShareMode string

const (
	ShareModeNone    ShareMode = ""
	ShareModeFile    ShareMode = "file"
	ShareModeLink    ShareMode = "link"
	ShareModeOBS     ShareMode = "obs"
	ShareModeOBSRTSP ShareMode = "obs_rtsp"
	ShareModeOBSHLS  ShareMode = "obs_hls"
)

// NormalizeShareMode は文字列を正規化し、未知の値は ShareModeNone を返す。
// 現行 normalizeShareMode（server.go）と同じ許容値セットを維持する。
func NormalizeShareMode(s string) ShareMode {
	switch ShareMode(s) {
	case ShareModeFile, ShareModeLink, ShareModeOBS, ShareModeOBSRTSP, ShareModeOBSHLS:
		return ShareMode(s)
	default:
		return ShareModeNone
	}
}

// Valid はモードが既知の値かを返す。
func (m ShareMode) Valid() bool { return m != ShareModeNone }

// ShareURL は解決済みの共有 URL と表示ラベル。
type ShareURL struct {
	URL   string `json:"shareURL"`
	Label string `json:"shareURLLabel"`
}

// ShareContext は公開状態の型付きスナップショット。URL 導出に必要な情報を
// 一元化する（現在 url_engine.go が map[string]interface{} から読んでいる
// キー群を型へ置換する）。
type ShareContext struct {
	Current            *library.CurrentImage
	ImageURL           string
	PublicImageURL     string
	LocalImageURL      string
	HLSURL             string
	VideoPlayerEnabled bool
	OBS                obsrtmp.Status
	OBSLatency         obsrtmp.LatencyProfile
}

// Source は Current からメディア種別を一意に導出する。
// 文字列→enum の parse をここ一箇所に集約する（原因 5）。
// Current が無い場合は URL フィールドから推定する（現行 urlSourceFromState と同等）。
func (c ShareContext) Source() SourceKind {
	if s := sourceFromImage(c.Current); s != SourceKindUnknown {
		return s
	}
	if strings.HasPrefix(c.ImageURL, "http") || strings.HasPrefix(c.PublicImageURL, "http") || strings.HasPrefix(c.LocalImageURL, "http") {
		return SourceKindImage
	}
	if strings.HasPrefix(c.HLSURL, "http") {
		return SourceKindVideo
	}
	return SourceKindUnknown
}

func sourceFromImage(img *library.CurrentImage) SourceKind {
	if img == nil {
		return SourceKindUnknown
	}
	return sourceFromFields(img.Kind, img.SourceKind)
}

func sourceFromFields(kind, sourceKind string) SourceKind {
	switch {
	case sourceKind == "obs":
		return SourceKindOBS
	case sourceKind == "soundcloud" || sourceKind == "local_audio" || sourceKind == "remote_audio":
		return SourceKindMusic
	case kind == "video":
		return SourceKindVideo
	case kind == "image" || kind == "":
		return SourceKindImage
	default:
		return SourceKindUnknown
	}
}

// effectiveOBSLatency は、選択された OBS レイテンシ（Mode が空なら status の
// 値へフォールバック）を返す。現行 activeOBSLatency の型付き版。
func (c ShareContext) effectiveOBSLatency() obsrtmp.LatencyProfile {
	if c.OBSLatency.Mode != "" {
		return c.OBSLatency
	}
	return c.OBS.Latency
}

// Resolve は mode に対して単一の正しい URL を返す。
// 現行 urlRequestForShareMode + urlEngineRegistry.Issue を一本化したもの。
// OBS の RTSP/HLS 判定は effectiveOBSLatency（Mode ベース）に統一する
// （現行 url_engine.go の urlRequestForShareMode は生の Transport を、
// obsRTSPURLEngine.Issue は activeOBSLatency を見ており不整合だった。原因 8）。
func (c ShareContext) Resolve(mode ShareMode) ShareURL {
	switch mode {
	case ShareModeOBSRTSP:
		return c.resolveOBSRTSP()
	case ShareModeOBSHLS:
		return c.resolveHLS()
	case ShareModeOBS:
		if c.effectiveOBSLatency().Transport == obsrtmp.LatencyModeRTSPT {
			return c.resolveOBSRTSP()
		}
		return c.resolveHLS()
	case ShareModeFile, ShareModeLink:
		return c.resolveForSource(c.Source())
	default:
		return ShareURL{Label: "URL"}
	}
}

func (c ShareContext) resolveForSource(src SourceKind) ShareURL {
	switch src {
	case SourceKindImage:
		return c.resolveImage()
	default:
		// video / music / obs / unknown → HLS（現行 defaultProtocolForSource と同じ）
		return c.resolveHLS()
	}
}

func (c ShareContext) resolveImage() ShareURL {
	for _, candidate := range []struct{ url, label string }{
		{c.ImageURL, "ImagePad URL"},
		{c.PublicImageURL, "ImagePad URL"},
		{c.LocalImageURL, "Local URL"},
	} {
		if strings.HasPrefix(candidate.url, "http") {
			return ShareURL{URL: candidate.url, Label: candidate.label}
		}
	}
	return ShareURL{Label: "URL"}
}

func (c ShareContext) resolveHLS() ShareURL {
	if c.VideoPlayerEnabled && strings.HasPrefix(c.HLSURL, "http") {
		return ShareURL{URL: c.HLSURL, Label: "HLS URL"}
	}
	return ShareURL{Label: "URL"}
}

func (c ShareContext) resolveOBSRTSP() ShareURL {
	if c.effectiveOBSLatency().Transport == obsrtmp.LatencyModeRTSPT &&
		(c.OBS.Connected || c.OBS.Publishing) &&
		strings.HasPrefix(c.OBS.RTSPTURL, "rtsp://") {
		return ShareURL{URL: c.OBS.RTSPTURL, Label: "RTSP TCP URL"}
	}
	return ShareURL{Label: "URL"}
}

// Targets は全 mode 分の URL を一括で返す（現行 withResolvedShareURLs の置換）。
func (c ShareContext) Targets() map[ShareMode]ShareURL {
	return map[ShareMode]ShareURL{
		ShareModeFile:    c.Resolve(ShareModeFile),
		ShareModeLink:    c.Resolve(ShareModeLink),
		ShareModeOBS:     c.Resolve(ShareModeOBS),
		ShareModeOBSRTSP: c.Resolve(ShareModeOBSRTSP),
		ShareModeOBSHLS:  c.Resolve(ShareModeOBSHLS),
	}
}

// Primary は既定モード（Source から）で解決する。
func (c ShareContext) Primary() (ShareURL, ShareMode) {
	mode := defaultShareModeForSource(c.Source())
	return c.Resolve(mode), mode
}

func defaultShareModeForSource(src SourceKind) ShareMode {
	switch src {
	case SourceKindImage:
		return ShareModeFile
	default:
		return ShareModeLink
	}
}

// shareContextFromState は型なし state map から ShareContext を組み立てる。
// Task 3 の一時ブリッジ。Task 4a で state() から直接型付き構築へ置換し、この
// 関数は削除する。
func shareContextFromState(state map[string]interface{}) ShareContext {
	ctx := ShareContext{}
	switch v := state["current"].(type) {
	case *library.CurrentImage:
		ctx.Current = v
	case library.CurrentImage:
		ctx.Current = &v
	}
	ctx.ImageURL, _ = state["imageURL"].(string)
	ctx.PublicImageURL, _ = state["publicImageURL"].(string)
	ctx.LocalImageURL, _ = state["localImageURL"].(string)
	ctx.HLSURL, _ = state["hlsURL"].(string)
	if vp, ok := state["videoPlayer"].(map[string]interface{}); ok {
		ctx.VideoPlayerEnabled, _ = vp["enabled"].(bool)
	}
	if v, ok := state["obs"].(obsrtmp.Status); ok {
		ctx.OBS = v
	}
	if v, ok := state["obsLatency"].(obsrtmp.LatencyProfile); ok {
		ctx.OBSLatency = v
	}
	return ctx
}

// shareTargetsFromContext は ShareContext から文字列キーの shareTargets を
// 生成する（フロント JS が data.shareTargets[mode] を文字列キーで読むため）。
func shareTargetsFromContext(ctx ShareContext) map[string]interface{} {
	out := make(map[string]interface{}, 5)
	for _, mode := range []ShareMode{ShareModeFile, ShareModeLink, ShareModeOBS, ShareModeOBSRTSP, ShareModeOBSHLS} {
		view := ctx.Resolve(mode)
		out[string(mode)] = map[string]interface{}{
			"shareURL":      view.URL,
			"shareURLLabel": view.Label,
		}
	}
	return out
}
