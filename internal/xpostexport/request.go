// Package xpostexport owns one immutable X slideshow export in a budgeted child.
package xpostexport

import (
	"encoding/json"
	"errors"
	"fmt"
	"imagepadserver/internal/xpostimage"
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xposttts"
	"imagepadserver/internal/xpostvideo"
	"io"
	"math"
	"path/filepath"
	"strings"
)

type Request struct {
	JobID        string             `json:"jobId"`
	URL          string             `json:"url"`
	Theme        string             `json:"theme"`
	FontPath     string             `json:"fontPath"`
	WorkDir      string             `json:"workDir"`
	FFmpeg       string             `json:"ffmpeg"`
	FFprobe      string             `json:"ffprobe"`
	Compositor   string             `json:"compositor"`
	EncoderMode  string             `json:"encoderMode"`
	CRF          int                `json:"crf"`
	AudioBitrate string             `json:"audioBitrate"`
	Voice        *xpostmodel.Voice  `json:"voice,omitempty"`
	Options      xpostvideo.Options `json:"options"`
}

func ReadRequest(r io.Reader) (Request, error) {
	data, err := io.ReadAll(io.LimitReader(r, 128*1024+1))
	if err != nil {
		return Request{}, err
	}
	if len(data) > 128*1024 {
		return Request{}, errors.New("X export request exceeds 128 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Request{}, errors.New("X export request has trailing JSON")
	}
	return request, request.Validate()
}

func (r Request) Validate() error {
	if !xpostimage.IsPostURL(r.URL) {
		return errors.New("Xの投稿URLを入力してください")
	}
	if r.JobID == "" || strings.ContainsAny(r.JobID, "/\\.\x00") {
		return errors.New("invalid X job identity")
	}
	if !filepath.IsAbs(r.WorkDir) || r.FontPath == "" || r.FFmpeg == "" || r.FFprobe == "" || r.Compositor == "" {
		return errors.New("X export tools and absolute work directory are required")
	}
	if r.EncoderMode != "gpu" && r.EncoderMode != "cpu" {
		return errors.New("invalid X encoder mode")
	}
	if r.CRF < 0 || r.CRF > 51 {
		return errors.New("invalid X video quality")
	}
	return nil
}

// ValidateVoice refuses reused style IDs and canonicalizes the engine's current
// names. An unavailable voice is never silently replaced with another voice.
func ValidateVoice(v xpostmodel.Voice, speakers []xposttts.Speaker) (xpostmodel.Voice, error) {
	if _, err := xposttts.NewClient(v.EngineURL); err != nil {
		return v, err
	}
	if math.IsNaN(v.Speed) || math.IsInf(v.Speed, 0) || v.Speed < .5 || v.Speed > 2 {
		return v, errors.New("読み上げ速度は0.5〜2.0にしてください")
	}
	for _, speaker := range speakers {
		if speaker.UUID != v.SpeakerUUID {
			continue
		}
		for _, style := range speaker.Styles {
			if style.ID == v.StyleID {
				v.SpeakerName = speaker.Name
				v.StyleName = style.Name
				return v, nil
			}
		}
	}
	return v, fmt.Errorf("選択したVOICEVOXの声が利用できません。声一覧を更新して選び直してください")
}

type Result struct {
	OutputPath    string  `json:"outputPath"`
	HLSDir        string  `json:"hlsDir"`
	SnapshotPath  string  `json:"snapshotPath"`
	ThumbnailPath string  `json:"thumbnailPath"`
	PostID        string  `json:"postId"`
	Frames        int     `json:"frames"`
	RendererCalls int     `json:"rendererCalls"`
	Duration      float64 `json:"duration"`
	Adapter       string  `json:"adapter"`
	Backend       string  `json:"backend"`
	Encoder       string  `json:"encoder"`
}

type Event struct {
	JobID   string            `json:"jobId"`
	Type    string            `json:"type"`
	Percent int               `json:"percent,omitempty"`
	Message string            `json:"message,omitempty"`
	Voice   *xpostmodel.Voice `json:"voice,omitempty"`
	Result  *Result           `json:"result,omitempty"`
	Error   string            `json:"error,omitempty"`
}

func WriteEvent(w io.Writer, e Event) error { return json.NewEncoder(w).Encode(e) }
