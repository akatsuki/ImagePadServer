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

	"imagepadserver/internal/video"
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
	SourcePath      string      `json:"sourcePath"`
	Status          TrackStatus `json:"status"`
	// Progress is the preparation progress (0-100) while Status is
	// TrackPreparing: download, analysis, and render phases combined.
	Progress             int                              `json:"progress"`
	Error                string                           `json:"error,omitempty"`
	AddedAt              time.Time                        `json:"addedAt"`
	EncodingContract     *video.RadioEncodingContract     `json:"encodingContract,omitempty"`
	RenderRecipeContract *video.AssetRenderRecipeContract `json:"renderRecipeContract,omitempty"`
	RenderContentValues  video.AssetRenderContentValues   `json:"renderContentValues,omitempty"`
	NeedsRegeneration    bool                             `json:"needsRegeneration,omitempty"`
	Incompatible         bool                             `json:"incompatible,omitempty"`
}

type Queue struct {
	mu                   sync.Mutex
	tracks               []*Track
	currentID            string
	shuffle              bool
	loop                 bool
	played               map[string]bool
	sequentialLastID     string
	sequentialGeneration uint64
	sequentialPlayed     map[string]uint64
	previewID            string
	previewGeneration    uint64
}

func NewQueue() *Queue {
	return &Queue{
		played:               map[string]bool{},
		sequentialGeneration: 1,
		sequentialPlayed:     map[string]uint64{},
	}
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
			delete(q.sequentialPlayed, id)
			if q.currentID == id {
				q.currentID = ""
			}
			q.clearPreview()
			if q.sequentialLastID == id {
				q.sequentialLastID = ""
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
	q.clearPreview()
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

// ReplaceAll swaps the entire queue contents (e.g. loading a saved playlist),
// resets playback state, and returns the displaced tracks to their owner for
// runtime cleanup. Track IDs are kept as provided.
func (q *Queue) ReplaceAll(tracks []Track) (previous []Track) {
	q.mu.Lock()
	defer q.mu.Unlock()
	previous = make([]Track, 0, len(q.tracks))
	for _, track := range q.tracks {
		previous = append(previous, *track)
	}
	q.tracks = q.tracks[:0]
	for i := range tracks {
		stored := tracks[i]
		q.tracks = append(q.tracks, &stored)
	}
	q.currentID = ""
	q.resetPlaybackCycle()
	q.sequentialPlayed = map[string]uint64{}
	q.clearPreview()
	return previous
}

func (q *Queue) CurrentID() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.currentID
}

// SetCurrent marks the track as now playing (割り込み再生). It also advances the
// sequential cursor and counts the track as played for shuffle selection.
func (q *Queue) SetCurrent(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.find(id) == nil {
		return false
	}
	q.markPlayed(id)
	q.clearPreview()
	return true
}

func (q *Queue) ClearCurrent() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.currentID = ""
	q.clearPreview()
}

// ResetPlaybackCycle starts a fresh playback generation. Automatic wakes do
// not call this method: they retain the sequential cursor after idle.
func (q *Queue) ResetPlaybackCycle() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resetPlaybackCycle()
	q.clearPreview()
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
	q.clearPreview()
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
	q.clearPreview()
}

func (q *Queue) Loop() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.loop
}

// Next advances to the next playable track and marks it current. It returns
// ok=false when the playlist is exhausted (and loop is off) or when no track
// is ready; in that case the current track is cleared. A prior PreviewNext
// reservation is committed when it is still valid.
func (q *Queue) Next() (Track, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var next *Track
	if q.previewID != "" && q.previewGeneration == q.sequentialGeneration {
		next = q.find(q.previewID)
		if next == nil || next.Status != TrackReady || next.ID == q.currentID {
			next = nil
		}
	}
	q.clearPreview()
	if next == nil {
		if q.shuffle {
			next = q.nextShuffled()
		} else {
			next = q.nextSequential()
		}
	}
	if next == nil {
		q.currentID = ""
		return Track{}, false
	}
	q.markPlayed(next.ID)
	return *next, true
}

// PreviewNext reserves the next playable track without changing currentID,
// played state, playback-cycle generation, or queue order. The following Next
// call commits the same reserved track, including shuffle/loop selection. Any
// queue mutation invalidates the reservation.
func (q *Queue) PreviewNext() (Track, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.previewID != "" && q.previewGeneration == q.sequentialGeneration {
		if next := q.find(q.previewID); next != nil && next.Status == TrackReady && next.ID != q.currentID {
			return *next, true
		}
		q.clearPreview()
	}
	var next *Track
	if q.shuffle {
		next = q.nextShuffledPreview()
	} else {
		next = q.nextSequentialPreview()
	}
	if next == nil {
		return Track{}, false
	}
	q.previewID = next.ID
	q.previewGeneration = q.sequentialGeneration
	return *next, true
}

// CommitPreviewNext consumes the current PreviewNext reservation and marks
// that exact track current. It does not perform a new shuffle/sequential
// selection, so callers can validate an external contract before advancing
// queue playback state.
func (q *Queue) CommitPreviewNext() (Track, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.previewID == "" || q.previewGeneration != q.sequentialGeneration {
		return Track{}, false
	}
	next := q.find(q.previewID)
	if next == nil || next.Status != TrackReady || next.ID == q.currentID {
		q.clearPreview()
		return Track{}, false
	}
	q.clearPreview()
	q.markPlayed(next.ID)
	return *next, true
}

func (q *Queue) nextSequential() *Track {
	start := q.sequentialStart()
	if next := q.nextUnplayedSequential(start); next != nil {
		return next
	}
	if !q.loop {
		return nil
	}
	q.resetPlaybackCycle()
	return q.nextUnplayedSequential(start)
}

func (q *Queue) nextSequentialPreview() *Track {
	start := q.sequentialStart()
	if next := q.nextUnplayedSequential(start); next != nil {
		return next
	}
	if !q.loop || len(q.tracks) == 0 {
		return nil
	}
	for offset := range q.tracks {
		i := (start + offset) % len(q.tracks)
		t := q.tracks[i]
		if t.Status == TrackReady {
			return t
		}
	}
	return nil
}

func (q *Queue) sequentialStart() int {
	for i, t := range q.tracks {
		if t.ID == q.sequentialLastID {
			return (i + 1) % len(q.tracks)
		}
	}
	return 0
}

func (q *Queue) nextUnplayedSequential(start int) *Track {
	for offset := range q.tracks {
		i := (start + offset) % len(q.tracks)
		t := q.tracks[i]
		if t.Status == TrackReady && q.sequentialPlayed[t.ID] != q.sequentialGeneration {
			return t
		}
	}
	return nil
}

func (q *Queue) nextShuffled() *Track {
	candidates := q.shuffleCandidates()
	if len(candidates) == 0 && q.loop {
		q.resetPlaybackCycle()
		candidates = q.shuffleCandidates()
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[randIntn(len(candidates))]
}

func (q *Queue) nextShuffledPreview() *Track {
	candidates := q.shuffleCandidates()
	if len(candidates) == 0 && q.loop {
		candidates = make([]*Track, 0, len(q.tracks))
		for _, t := range q.tracks {
			if t.Status == TrackReady && t.ID != q.currentID {
				candidates = append(candidates, t)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[randIntn(len(candidates))]
}

func (q *Queue) markPlayed(id string) {
	q.currentID = id
	q.played[id] = true
	q.sequentialLastID = id
	q.sequentialPlayed[id] = q.sequentialGeneration
}

func (q *Queue) resetPlaybackCycle() {
	q.played = map[string]bool{}
	q.sequentialLastID = ""
	q.sequentialGeneration++
	if q.sequentialGeneration == 0 {
		q.sequentialGeneration = 1
		q.sequentialPlayed = map[string]uint64{}
	}
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

func (q *Queue) clearPreview() {
	q.previewID = ""
	q.previewGeneration = 0
}
