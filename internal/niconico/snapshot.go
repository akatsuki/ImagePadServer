package niconico

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	CommentStatusEmpty = "empty"
	CommentStatusReady = "ready"
)

type Snapshot struct {
	SchemaVersion        int      `json:"schemaVersion"`
	VideoID              string   `json:"videoId"`
	AcquiredAt           string   `json:"acquiredAt,omitempty"`
	ProviderVersion      string   `json:"providerVersion,omitempty"`
	NormalizationVersion string   `json:"normalizationVersion,omitempty"`
	SelectedForks        []string `json:"selectedForks,omitempty"`
	Threads              []Thread `json:"threads,omitempty"`
	CommentStatus        string   `json:"commentStatus"`
	CommentCount         int      `json:"commentCount"`
	InvalidCount         int      `json:"invalidCount,omitempty"`
}

type Thread struct {
	ID       string    `json:"id,omitempty"`
	Fork     string    `json:"fork"`
	Comments []Comment `json:"comments,omitempty"`
}

type Comment struct {
	ID          string   `json:"id"`
	No          int64    `json:"no,omitempty"`
	VposMs      int64    `json:"vposMs"`
	Body        string   `json:"body"`
	Commands    []string `json:"commands,omitempty"`
	UserID      string   `json:"userId,omitempty"`
	IsPremium   bool     `json:"isPremium,omitempty"`
	Score       int64    `json:"score,omitempty"`
	PostedAt    string   `json:"postedAt,omitempty"`
	NicoruCount int64    `json:"nicoruCount,omitempty"`
	NicoruID    *string  `json:"nicoruId,omitempty"`
	Source      string   `json:"source,omitempty"`
	IsMyPost    bool     `json:"isMyPost,omitempty"`
}

// RendererThread and RendererComment are the stable v1 input contract passed
// to the Canvas2D comment renderer. Keeping this contract separate from the
// provider snapshot prevents API-specific fields from leaking into rendering.
type RendererThread struct {
	ID           string            `json:"id"`
	Fork         string            `json:"fork"`
	CommentCount int               `json:"commentCount"`
	Comments     []RendererComment `json:"comments"`
}

type RendererComment struct {
	ID          string   `json:"id"`
	No          int64    `json:"no"`
	VposMs      int64    `json:"vposMs"`
	Body        string   `json:"body"`
	Commands    []string `json:"commands"`
	UserID      string   `json:"userId"`
	IsPremium   bool     `json:"isPremium"`
	Score       int64    `json:"score"`
	PostedAt    string   `json:"postedAt"`
	NicoruCount int64    `json:"nicoruCount"`
	NicoruID    *string  `json:"nicoruId"`
	Source      string   `json:"source"`
	IsMyPost    bool     `json:"isMyPost"`
}

// RendererThreads selects the configured forks and maps the normalized
// provider snapshot to the renderer's v1 shape. Easy-comment data remains in
// the snapshot for audit/export, while the default selection is main+owner.
func (s Snapshot) RendererThreads() []RendererThread {
	selected := make(map[string]struct{}, len(s.SelectedForks))
	for _, fork := range s.SelectedForks {
		if fork != "" {
			selected[fork] = struct{}{}
		}
	}
	if len(selected) == 0 {
		selected["main"] = struct{}{}
		selected["owner"] = struct{}{}
	}

	out := make([]RendererThread, 0, len(s.Threads))
	for _, thread := range s.Threads {
		if _, ok := selected[thread.Fork]; !ok {
			continue
		}
		rendererThread := RendererThread{
			ID:           thread.ID,
			Fork:         thread.Fork,
			CommentCount: len(thread.Comments),
			Comments:     make([]RendererComment, 0, len(thread.Comments)),
		}
		for _, comment := range thread.Comments {
			commands := append([]string(nil), comment.Commands...)
			if commands == nil {
				commands = []string{}
			}
			rendererThread.Comments = append(rendererThread.Comments, RendererComment{
				ID:          comment.ID,
				No:          comment.No,
				VposMs:      comment.VposMs,
				Body:        comment.Body,
				Commands:    commands,
				UserID:      comment.UserID,
				IsPremium:   comment.IsPremium,
				Score:       comment.Score,
				PostedAt:    comment.PostedAt,
				NicoruCount: comment.NicoruCount,
				NicoruID:    comment.NicoruID,
				Source:      comment.Source,
				IsMyPost:    comment.IsMyPost,
			})
		}
		out = append(out, rendererThread)
	}
	return out
}

type RenderOptions struct {
	SourceDigest    string `json:"sourceDigest,omitempty"`
	RendererVersion string `json:"rendererVersion"`
	BrowserVersion  string `json:"browserVersion,omitempty"`
	FontDigest      string `json:"fontDigest,omitempty"`
	Geometry        string `json:"geometry,omitempty"`
	FPS             string `json:"fps,omitempty"`
	EncoderPolicy   string `json:"encoderPolicy,omitempty"`
}

// NormalizeSnapshot validates and copies the provider result. It deliberately
// preserves body whitespace and negative vpos values; those can affect CA and
// comments that are already visible at the start of a video.
func NormalizeSnapshot(in Snapshot) (Snapshot, error) {
	if in.VideoID == "" {
		return Snapshot{}, fmt.Errorf("niconico: video ID is required")
	}
	out := in
	out.SchemaVersion = 1
	out.NormalizationVersion = "1"
	forkSet := make(map[string]struct{}, len(in.SelectedForks))
	for _, fork := range in.SelectedForks {
		if fork != "" {
			forkSet[fork] = struct{}{}
		}
	}
	out.SelectedForks = make([]string, 0, len(forkSet))
	for fork := range forkSet {
		out.SelectedForks = append(out.SelectedForks, fork)
	}
	sort.Strings(out.SelectedForks)
	out.Threads = make([]Thread, 0, len(in.Threads))
	seen := make(map[string][]byte)
	inputComments := 0
	validComments := 0
	invalidComments := 0
	for _, thread := range in.Threads {
		copyThread := Thread{ID: thread.ID, Fork: thread.Fork}
		copyThread.Comments = make([]Comment, 0, len(thread.Comments))
		for _, comment := range thread.Comments {
			inputComments++
			if comment.ID == "" {
				invalidComments++
				continue
			}
			copyComment := comment
			copyComment.Commands = append([]string(nil), comment.Commands...)
			encoded, err := json.Marshal(copyComment)
			if err != nil {
				return Snapshot{}, fmt.Errorf("niconico: encode comment %q: %w", comment.ID, err)
			}
			key := thread.ID + "\x00" + thread.Fork + "\x00" + comment.ID
			if previous, ok := seen[key]; ok {
				if string(previous) != string(encoded) {
					return Snapshot{}, fmt.Errorf("niconico: duplicate comment %q has different content", comment.ID)
				}
				continue
			}
			seen[key] = encoded
			copyThread.Comments = append(copyThread.Comments, copyComment)
			validComments++
		}
		if len(copyThread.Comments) > 0 {
			out.Threads = append(out.Threads, copyThread)
		}
	}
	if inputComments == 0 {
		out.CommentStatus = CommentStatusEmpty
	} else if validComments == 0 {
		return Snapshot{}, fmt.Errorf("niconico: all comments are invalid")
	} else {
		out.CommentStatus = CommentStatusReady
	}
	out.CommentCount = validComments
	out.InvalidCount = invalidComments
	return out, nil
}

// RenderKey returns a stable content key for the exact source, snapshot and
// rendering inputs. Acquisition time is intentionally excluded from the key.
func RenderKey(snapshot Snapshot, options RenderOptions) (string, error) {
	// Acquisition time is audit metadata and must not invalidate a reusable
	// render for the same source, comments, and rendering inputs.
	snapshot.AcquiredAt = ""
	canonical := struct {
		Snapshot Snapshot      `json:"snapshot"`
		Options  RenderOptions `json:"options"`
	}{Snapshot: snapshot, Options: options}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("niconico: render key input: %w", err)
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}
