// Package playlist implements the music-mode playlist queue: ordered tracks,
// shuffle/loop next-track selection, and JSON persistence. It is pure domain
// logic with no HTTP or ffmpeg dependencies.
package playlist

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"sync"
	"time"
)

type TrackStatus string

const (
	TrackPreparing TrackStatus = "preparing"
	TrackReady     TrackStatus = "ready"
	TrackFailed    TrackStatus = "failed"
)

type Track struct {
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	Artist          string      `json:"artist"`
	Album           string      `json:"album"`
	DurationSeconds int         `json:"durationSeconds"`
	SourceKind      string      `json:"sourceKind"`
	OriginalName    string      `json:"originalName"`
	MediaPath       string      `json:"mediaPath"`
	ThumbnailPath   string      `json:"thumbnailPath"`
	Status          TrackStatus `json:"status"`
	// Progress is the preparation progress (0-100) while Status is
	// TrackPreparing: download, analysis, and render phases combined.
	Progress int       `json:"progress"`
	Error    string    `json:"error,omitempty"`
	AddedAt  time.Time `json:"addedAt"`
}

type Queue struct {
	mu        sync.Mutex
	tracks    []*Track
	currentID string
	shuffle   bool
	loop      bool
	played    map[string]bool
}

func NewQueue() *Queue {
	return &Queue{played: map[string]bool{}}
}

func newTrackID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))[:16]
	}
	return hex.EncodeToString(buf)
}

// Add appends the track to the end of the queue, assigning an ID and AddedAt.
// A zero Status defaults to TrackPreparing. The stored copy is returned.
func (q *Queue) Add(t Track) Track {
	q.mu.Lock()
	defer q.mu.Unlock()
	t.ID = newTrackID()
	t.AddedAt = time.Now()
	if t.Status == "" {
		t.Status = TrackPreparing
	}
	stored := t
	q.tracks = append(q.tracks, &stored)
	return stored
}

func (q *Queue) Remove(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, t := range q.tracks {
		if t.ID == id {
			q.tracks = append(q.tracks[:i], q.tracks[i+1:]...)
			delete(q.played, id)
			if q.currentID == id {
				q.currentID = ""
			}
			return true
		}
	}
	return false
}

// SetOrder reorders the queue. ids must be an exact permutation of the
// current track IDs, otherwise the order is left unchanged.
func (q *Queue) SetOrder(ids []string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(ids) != len(q.tracks) {
		return false
	}
	byID := make(map[string]*Track, len(q.tracks))
	for _, t := range q.tracks {
		byID[t.ID] = t
	}
	next := make([]*Track, 0, len(ids))
	for _, id := range ids {
		t, ok := byID[id]
		if !ok {
			return false
		}
		delete(byID, id)
		next = append(next, t)
	}
	q.tracks = next
	return true
}

func (q *Queue) Snapshot() []Track {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Track, 0, len(q.tracks))
	for _, t := range q.tracks {
		out = append(out, *t)
	}
	return out
}

func (q *Queue) Get(id string) (Track, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if t := q.find(id); t != nil {
		return *t, true
	}
	return Track{}, false
}

func (q *Queue) find(id string) *Track {
	for _, t := range q.tracks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// Mutate applies fn to the stored track under the queue lock.
func (q *Queue) Mutate(id string, fn func(*Track)) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.find(id)
	if t == nil {
		return false
	}
	fn(t)
	return true
}

// ReplaceAll swaps the entire queue contents (e.g. loading a saved playlist)
// and resets playback state. Track IDs are kept as provided.
func (q *Queue) ReplaceAll(tracks []Track) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = q.tracks[:0]
	for i := range tracks {
		stored := tracks[i]
		q.tracks = append(q.tracks, &stored)
	}
	q.currentID = ""
	q.played = map[string]bool{}
}

func (q *Queue) CurrentID() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.currentID
}

// SetCurrent marks the track as now playing (割り込み再生). It also counts the
// track as played so a following shuffle Next does not repeat it.
func (q *Queue) SetCurrent(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.find(id) == nil {
		return false
	}
	q.currentID = id
	q.played[id] = true
	return true
}

func (q *Queue) ClearCurrent() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.currentID = ""
}

func (q *Queue) MarkReady(id, mediaPath string, durationSeconds int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.find(id)
	if t == nil {
		return false
	}
	t.Status = TrackReady
	t.MediaPath = mediaPath
	t.DurationSeconds = durationSeconds
	t.Progress = 100
	t.Error = ""
	return true
}

// SetProgress updates the preparation progress (clamped 0-99 so only
// MarkReady reports completion); it never moves backwards.
func (q *Queue) SetProgress(id string, percent int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.find(id)
	if t == nil || t.Status != TrackPreparing {
		return false
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	if percent > t.Progress {
		t.Progress = percent
	}
	return true
}

func (q *Queue) MarkFailed(id, message string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.find(id)
	if t == nil {
		return false
	}
	t.Status = TrackFailed
	t.Error = message
	return true
}

func (q *Queue) SetShuffle(on bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuffle = on
}

func (q *Queue) Shuffle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.shuffle
}

func (q *Queue) SetLoop(on bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.loop = on
}

func (q *Queue) Loop() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.loop
}

// Next advances to the next playable track and marks it current. It returns
// ok=false when the playlist is exhausted (and loop is off) or when no track
// is ready; in that case the current track is cleared.
func (q *Queue) Next() (Track, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.currentID != "" {
		q.played[q.currentID] = true
	}
	var next *Track
	if q.shuffle {
		next = q.nextShuffled()
	} else {
		next = q.nextSequential()
	}
	if next == nil {
		q.currentID = ""
		return Track{}, false
	}
	q.currentID = next.ID
	q.played[next.ID] = true
	return *next, true
}

func (q *Queue) nextSequential() *Track {
	start := 0
	if q.currentID != "" {
		for i, t := range q.tracks {
			if t.ID == q.currentID {
				start = i + 1
				break
			}
		}
	}
	for i := start; i < len(q.tracks); i++ {
		if q.tracks[i].Status == TrackReady {
			return q.tracks[i]
		}
	}
	if q.loop {
		for i := 0; i < start && i < len(q.tracks); i++ {
			if q.tracks[i].Status == TrackReady {
				return q.tracks[i]
			}
		}
	}
	return nil
}

func (q *Queue) nextShuffled() *Track {
	candidates := q.shuffleCandidates()
	if len(candidates) == 0 && q.loop {
		q.played = map[string]bool{}
		candidates = q.shuffleCandidates()
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[randIntn(len(candidates))]
}

func (q *Queue) shuffleCandidates() []*Track {
	out := make([]*Track, 0, len(q.tracks))
	for _, t := range q.tracks {
		if t.Status == TrackReady && !q.played[t.ID] && t.ID != q.currentID {
			out = append(out, t)
		}
	}
	return out
}

func randIntn(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return int(time.Now().UnixNano()) % n
	}
	return int(v.Int64())
}
