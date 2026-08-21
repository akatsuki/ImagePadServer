package playlist

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"imagepadserver/internal/video"
)

type StoreOptions struct {
	ActiveRecipe func() video.RadioRenderRecipe
	ObserveAsset func(context.Context, string) (video.RadioAssetSpec, error)
	HashFile     func(string) (string, error)
	Rename       func(string, string) error
	Now          func() time.Time
	ProbeTimeout time.Duration
}

type Store struct {
	mu           sync.Mutex
	path         string
	mediaDir     string
	activeRecipe func() video.RadioRenderRecipe
	observeAsset func(context.Context, string) (video.RadioAssetSpec, error)
	hashFile     func(string) (string, error)
	rename       func(string, string) error
	now          func() time.Time
	probeTimeout time.Duration
}

var linkMediaFile = os.Link

var (
	storageIDRandomRead      = rand.Read
	storageIDFallbackNow     = time.Now
	storageIDFallbackCounter atomic.Uint64
)

type storedPlaylist struct {
	ID             string  `json:"id,omitempty"`
	Name           string  `json:"name"`
	ActiveVersion  string  `json:"activeVersion,omitempty"`
	ManifestDigest string  `json:"manifestDigest,omitempty"`
	Tracks         []Track `json:"tracks,omitempty"` // v0 migration evidence only
}

type PlaylistEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var versionNamePattern = regexp.MustCompile(`^\d{8}T\d{6}\.\d{9}Z-[0-9a-f]{24}$`)

type storeFile struct {
	Playlists []storedPlaylist `json:"playlists"`
}

func NewStore(path string) *Store {
	return NewStoreWithOptions(path, StoreOptions{})
}

func NewStoreWithOptions(path string, options StoreOptions) *Store {
	if options.ActiveRecipe == nil {
		options.ActiveRecipe = func() video.RadioRenderRecipe {
			preset := video.MusicRadioQualityPreset("auto", 0, 0)
			preset.Height = 720
			preset.RadioLatency = "rtsp-ultra"
			return video.MusicRadioRenderRecipe(preset, 180)
		}
	}
	if options.ObserveAsset == nil {
		options.ObserveAsset = func(ctx context.Context, path string) (video.RadioAssetSpec, error) {
			ffprobe, err := video.EnsureFFprobe()
			if err != nil {
				return video.RadioAssetSpec{}, err
			}
			return video.ObserveRadioAsset(ctx, ffprobe, path)
		}
	}
	if options.HashFile == nil {
		options.HashFile = SHA256File
	}
	if options.Rename == nil {
		options.Rename = os.Rename
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ProbeTimeout <= 0 {
		options.ProbeTimeout = 30 * time.Second
	}
	return &Store{
		path: path, mediaDir: filepath.Join(filepath.Dir(path), "playlist-media"),
		activeRecipe: options.ActiveRecipe, observeAsset: options.ObserveAsset,
		hashFile: options.HashFile, rename: options.Rename, now: options.Now,
		probeTimeout: options.ProbeTimeout,
	}
}

func (s *Store) load() (storeFile, error) {
	var f storeFile
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return storeFile{}, fmt.Errorf("parse %s: %w", filepath.Base(s.path), err)
	}
	if err := validateStoreFile(f); err != nil {
		return storeFile{}, err
	}
	return f, nil
}

func (s *Store) write(f storeFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return s.atomicWriteFile(s.path, append(data, '\n'), 0600)
}

func (s *Store) atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := s.rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (s *Store) Save(name string, tracks []Track) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("プレイリスト名を入力してください")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return err
	}
	matches := findStoredPlaylistsByName(index, name)
	if len(matches) > 1 {
		return fmt.Errorf("playlist name %q is ambiguous", name)
	}
	if len(matches) == 1 {
		if matches[0].ID == "" {
			id, err := newUniqueStorageID(index)
			if err != nil {
				return err
			}
			return s.saveIDLockedMigratingLegacy(id, name, tracks)
		}
		return s.saveIDLocked(matches[0].ID, name, tracks, false)
	}
	id, err := newUniqueStorageID(index)
	if err != nil {
		return err
	}
	return s.saveIDLocked(id, name, tracks, true)
}

func (s *Store) Create(name string, tracks []Track) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("プレイリスト名を入力してください")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return "", err
	}
	if len(findStoredPlaylistsByName(index, name)) != 0 {
		return "", fmt.Errorf("playlist name %q already exists", name)
	}
	id, err := newUniqueStorageID(index)
	if err != nil {
		return "", err
	}
	if err := s.saveIDLocked(id, name, tracks, true); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) SaveID(id, name string, tracks []Track) error {
	name = strings.TrimSpace(name)
	if !validStorageID(id) {
		return errors.New("invalid playlist ID")
	}
	if name == "" {
		return errors.New("プレイリスト名を入力してください")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveIDLocked(id, name, tracks, false)
}

func (s *Store) Rename(id, name string) error {
	name = strings.TrimSpace(name)
	if !validStorageID(id) {
		return errors.New("invalid playlist ID")
	}
	if name == "" {
		return errors.New("プレイリスト名を入力してください")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return err
	}
	found := false
	for i := range index.Playlists {
		if index.Playlists[i].Name == name && index.Playlists[i].ID != id {
			return fmt.Errorf("playlist name %q already exists", name)
		}
		if index.Playlists[i].ID == id {
			index.Playlists[i].Name = name
			found = true
		}
	}
	if !found {
		return fmt.Errorf("playlist ID %q not found", id)
	}
	return s.write(index)
}

func (s *Store) saveIDLocked(id, name string, tracks []Track, create bool) error {
	return s.saveIDLockedInternal(id, name, tracks, create, "")
}

func (s *Store) saveIDLockedMigratingLegacy(id, name string, tracks []Track) error {
	return s.saveIDLockedInternal(id, name, tracks, true, name)
}

func (s *Store) saveIDLockedInternal(id, name string, tracks []Track, create bool, legacyName string) error {
	if !validStorageID(id) {
		return errors.New("invalid playlist ID")
	}
	index, err := s.load()
	if err != nil {
		return err
	}
	if err := validateTrackIDs(tracks); err != nil {
		return err
	}
	entryIndex := -1
	entry := storedPlaylist{ID: id, Name: name}
	for i := range index.Playlists {
		if index.Playlists[i].ID == id {
			entryIndex, entry = i, index.Playlists[i]
			break
		}
	}
	if entryIndex < 0 && legacyName != "" {
		for i := range index.Playlists {
			if index.Playlists[i].ID == "" && index.Playlists[i].Name == legacyName {
				entryIndex = i
				entry = index.Playlists[i]
				break
			}
		}
		if entryIndex < 0 {
			return fmt.Errorf("legacy playlist %q not found", legacyName)
		}
	}
	if entryIndex < 0 && !create {
		return fmt.Errorf("playlist ID %q not found", id)
	}
	for i := range index.Playlists {
		if i != entryIndex && index.Playlists[i].Name == name && index.Playlists[i].ID != id {
			return fmt.Errorf("playlist name %q already exists", name)
		}
	}
	entry.ID = id
	entry.Name = name
	version := s.now().UTC().Format("20060102T150405.000000000Z") + "-" + newStorageID()
	root, err := s.playlistRoot(entry.ID, true)
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(root, "."+version+".tmp-")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	recipe := s.activeRecipe()
	active := recipe.NormalizedEncodingContract()
	now := s.now().UTC()
	diagnostics := currentBuildDiagnostics()
	createdAt, createdWith := now, diagnostics
	if old, oldErr := s.readManifest(entry); oldErr == nil {
		createdAt, createdWith = old.CreatedAt, old.CreatedWith
	}
	manifest := PlaylistManifest{
		SchemaVersion: PlaylistManifestSchemaVersion, PlaylistID: entry.ID,
		CreatedAt: createdAt, UpdatedAt: now, CreatedWith: createdWith, UpdatedWith: diagnostics,
		GeneratedBy:      GeneratorDiagnostics{Component: "playlist-save"},
		EncodingContract: active, EncodingFingerprint: active.EncodingFingerprint(), RegenerationState: "ready",
		RenderRecipeContract: recipe.AssetRenderRecipeContract(), RenderRecipeFingerprint: recipe.AssetRenderFingerprint(),
		Tracks: make([]ManifestTrack, 0, len(tracks)),
	}
	for i := range tracks {
		mt, buildErr := s.buildManifestTrack(tmpDir, tracks[i], recipe)
		if buildErr != nil {
			return buildErr
		}
		if mt.RegenerationState != "ready" {
			manifest.RegenerationState = "regeneration-required"
		}
		manifest.Tracks = append(manifest.Tracks, mt)
	}
	manifest.Diagnostics = manifestRegenerationDiagnostics(manifest.Tracks)
	switch {
	case manifest.Diagnostics.NeedsRegenerationCount > 0 && manifest.Diagnostics.IncompatibleCount > 0:
		manifest.RegenerationState = "mixed"
	case manifest.Diagnostics.NeedsRegenerationCount > 0:
		manifest.RegenerationState = "regeneration-required"
	case manifest.Diagnostics.IncompatibleCount > 0:
		manifest.RegenerationState = "incompatible"
	default:
		manifest.RegenerationState = "ready"
	}
	manifest.Digest, err = manifest.computedDigest()
	if err != nil {
		return err
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeSyncedFile(filepath.Join(tmpDir, PlaylistManifestFileName), append(manifestData, '\n'), 0600); err != nil {
		return err
	}
	if err := syncDirectory(tmpDir); err != nil {
		return err
	}
	finalDir, err := containedChild(root, version, false)
	if err != nil {
		return err
	}
	if err := s.rename(tmpDir, finalDir); err != nil {
		return err
	}
	committed = true
	if err := syncDirectory(root); err != nil {
		return err
	}
	entry.ActiveVersion = version
	entry.ManifestDigest = manifest.Digest
	entry.Tracks = make([]Track, len(manifest.Tracks))
	for i := range manifest.Tracks {
		entry.Tracks[i] = manifest.Tracks[i].Track
	}
	if entryIndex >= 0 {
		index.Playlists[entryIndex] = entry
	} else {
		index.Playlists = append(index.Playlists, entry)
	}
	if err := s.write(index); err != nil {
		_ = s.removeVersion(entry.ID, version)
		return err
	}
	return nil
}

func (s *Store) buildManifestTrack(dir string, track Track, activeRecipe video.RadioRenderRecipe) (ManifestTrack, error) {
	track.NeedsRegeneration, track.Incompatible = false, false
	active := activeRecipe.StreamEncodingContract()
	actual := active.Clone()
	if track.EncodingContract != nil {
		actual = track.EncodingContract.Clone()
	}
	track.EncodingContract = &actual
	actualRender := activeRecipe.AssetRenderRecipeContract()
	if track.RenderRecipeContract != nil {
		actualRender = *track.RenderRecipeContract
	}
	track.RenderRecipeContract = &actualRender
	if track.RenderContentValues.DurationSeconds == 0 {
		track.RenderContentValues = activeRecipe.AssetRenderContentValues()
	}
	mt := ManifestTrack{Track: track, EncodingContract: actual, EncodingFingerprint: actual.EncodingFingerprint(), RenderRecipeContract: actualRender, RenderRecipeFingerprint: actualRender.Fingerprint(), RenderContentValues: track.RenderContentValues, RegenerationState: "ready"}
	var err error
	if track.MediaPath != "" {
		mt.Track.MediaPath, err = copyVersionAsset(dir, "media", track.ID, track.MediaPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				mt.Track.MediaPath = ""
				mt.Track.Status = TrackFailed
				mt.Track.Error = "メディアファイルを保存できませんでした（再追加が必要）"
			} else {
				return ManifestTrack{}, err
			}
		} else {
			mediaPath := filepath.Join(dir, mt.Track.MediaPath)
			if mt.MediaSHA256, err = s.hashFile(mediaPath); err != nil {
				return ManifestTrack{}, fmt.Errorf("hash media: %w", err)
			}
			probeCtx, cancel := context.WithTimeout(context.Background(), s.probeTimeout)
			mt.ObservedEncoding, err = s.observeAsset(probeCtx, mediaPath)
			cancel()
			if err != nil {
				return ManifestTrack{}, fmt.Errorf("probe media: %w", err)
			}
			if mt.ObservedEncoding.Video.GOPFrames <= 0 {
				return ManifestTrack{}, errors.New("probe media: GOP evidence missing")
			}
		}
	}
	if track.SourcePath != "" {
		if mt.Track.SourcePath, err = copyVersionAsset(dir, "source", track.ID, track.SourcePath); err != nil {
			return ManifestTrack{}, fmt.Errorf("copy source: %w", err)
		}
		if mt.SourceSHA256, err = s.hashFile(filepath.Join(dir, mt.Track.SourcePath)); err != nil {
			return ManifestTrack{}, fmt.Errorf("hash source: %w", err)
		}
	}
	if track.ThumbnailPath != "" {
		if mt.Track.ThumbnailPath, err = copyVersionAsset(dir, "artwork", track.ID, track.ThumbnailPath); err != nil {
			return ManifestTrack{}, fmt.Errorf("copy artwork: %w", err)
		}
	}
	streamMatch := actual.EncodingFingerprint() == active.EncodingFingerprint()
	renderMatch := actualRender.Fingerprint() == activeRecipe.AssetRenderFingerprint()
	observedMatch, observedReason := observedMatchesContract(mt.ObservedEncoding, actual)
	if mt.Track.MediaPath == "" || !streamMatch || !renderMatch || !observedMatch {
		switch {
		case mt.Track.MediaPath == "":
			mt.RegenerationReason = "saved media missing"
		case !streamMatch:
			mt.RegenerationReason = "stream encoding contract mismatch"
		case !renderMatch:
			mt.RegenerationReason = "asset render recipe mismatch"
		default:
			mt.RegenerationReason = observedReason
		}
		if mt.Track.SourcePath != "" {
			mt.RegenerationState = "regeneration-required"
		} else {
			mt.RegenerationState = "incompatible"
		}
	}
	return mt, nil
}

func (s *Store) readManifest(entry storedPlaylist) (PlaylistManifest, error) {
	if entry.ID == "" || entry.ActiveVersion == "" {
		return PlaylistManifest{}, errors.New("legacy playlist has no manifest")
	}
	dir, err := s.versionDir(entry)
	if err != nil {
		return PlaylistManifest{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, PlaylistManifestFileName))
	if err != nil {
		return PlaylistManifest{}, err
	}
	var manifest PlaylistManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PlaylistManifest{}, err
	}
	if manifest.SchemaVersion != PlaylistManifestSchemaVersion {
		return manifest, fmt.Errorf("unsupported playlist manifest schema %d", manifest.SchemaVersion)
	}
	if manifest.PlaylistID != entry.ID {
		return manifest, errors.New("playlist manifest identity mismatch")
	}
	computed, err := manifest.computedDigest()
	if err != nil {
		return manifest, err
	}
	if entry.ManifestDigest == "" || manifest.Digest == "" || entry.ManifestDigest != manifest.Digest || computed != manifest.Digest {
		return manifest, errors.New("playlist manifest digest mismatch")
	}
	return manifest, nil
}

func (s *Store) manifestPath(entry storedPlaylist) (string, error) {
	dir, err := s.versionDir(entry)
	if err != nil {
		return "", err
	}
	return containedChild(dir, PlaylistManifestFileName, false)
}

func (s *Store) versionDir(entry storedPlaylist) (string, error) {
	if entry.ID == "" || entry.ActiveVersion == "" {
		return "", errors.New("missing playlist version pointer")
	}
	if !versionNamePattern.MatchString(entry.ActiveVersion) {
		return "", errors.New("invalid playlist version")
	}
	root, err := s.playlistRoot(entry.ID, false)
	if err != nil {
		return "", err
	}
	return containedChild(root, entry.ActiveVersion, true)
}

func (s *Store) LoadID(id string) ([]Track, error) {
	if !validStorageID(id) {
		return nil, errors.New("invalid playlist ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return nil, err
	}
	entry, ok := findStoredPlaylistByID(index, id)
	if !ok {
		return nil, fmt.Errorf("playlist ID %q not found", id)
	}
	return s.loadTracks(entry, s.activeRecipe().NormalizedEncodingContract())
}

func (s *Store) Load(name string) ([]Track, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return nil, err
	}
	entry, err := resolveStoredPlaylistByName(index, name)
	if err != nil {
		return nil, err
	}
	return s.loadTracks(entry, s.activeRecipe().NormalizedEncodingContract())
}

func (s *Store) loadTracks(entry storedPlaylist, active video.RadioEncodingContract) ([]Track, error) {
	manifest, err := s.readManifest(entry)
	if err != nil {
		// Canonical playlist paths must not fall back to index tracks when
		// containment checks reject an unsafe version directory.
		if entry.ID != "" && (strings.Contains(err.Error(), "symlink") || strings.Contains(err.Error(), "escapes playlist") || strings.Contains(err.Error(), "contained path")) {
			return nil, err
		}
		if strings.Contains(err.Error(), "digest mismatch") {
			return nil, err
		}
		tracks := append([]Track(nil), entry.Tracks...)
		versionDir, versionErr := s.versionDir(entry)
		for i := range tracks {
			if versionErr == nil {
				for _, field := range []*string{&tracks[i].MediaPath, &tracks[i].SourcePath, &tracks[i].ThumbnailPath} {
					if *field == "" {
						continue
					}
					resolved, resolveErr := existingContainedAsset(versionDir, *field)
					if resolveErr != nil {
						*field = ""
					} else {
						*field = resolved
					}
				}
			} else if entry.ID == "" && entry.ActiveVersion == "" {
				for _, field := range []*string{&tracks[i].MediaPath, &tracks[i].SourcePath, &tracks[i].ThumbnailPath} {
					if *field == "" {
						continue
					}
					resolved, resolveErr := existingLegacyAsset(s.mediaDir, *field)
					if resolveErr != nil {
						*field = ""
					} else {
						*field = resolved
					}
				}
			} else {
				tracks[i].MediaPath, tracks[i].SourcePath, tracks[i].ThumbnailPath = "", "", ""
			}
			blockTrack(&tracks[i], tracks[i].SourcePath != "", "保存形式の移行が必要です")
		}
		return tracks, nil
	}
	dir, err := s.versionDir(entry)
	if err != nil {
		return nil, err
	}
	tracks := make([]Track, len(manifest.Tracks))
	for i := range manifest.Tracks {
		mt := manifest.Tracks[i]
		track := mt.Track
		track.EncodingContract = ptrContract(mt.EncodingContract.Clone())
		renderContract := mt.RenderRecipeContract
		track.RenderRecipeContract = &renderContract
		track.RenderContentValues = mt.RenderContentValues
		pathsOK := true
		for _, field := range []*string{&track.MediaPath, &track.SourcePath, &track.ThumbnailPath} {
			if *field == "" {
				continue
			}
			resolved, resolveErr := existingContainedAsset(dir, *field)
			if resolveErr != nil {
				pathsOK = false
				*field = ""
			} else {
				*field = resolved
			}
		}
		contractMatch := manifest.EncodingFingerprint == manifest.EncodingContract.EncodingFingerprint() &&
			manifest.EncodingFingerprint == active.EncodingFingerprint() &&
			mt.EncodingFingerprint == mt.EncodingContract.EncodingFingerprint() && mt.EncodingFingerprint == active.EncodingFingerprint() &&
			manifest.RenderRecipeFingerprint == manifest.RenderRecipeContract.Fingerprint() && manifest.RenderRecipeFingerprint == s.activeRecipe().AssetRenderFingerprint() &&
			mt.RenderRecipeFingerprint == mt.RenderRecipeContract.Fingerprint() && mt.RenderRecipeFingerprint == s.activeRecipe().AssetRenderFingerprint()
		observedMatch, reason := observedMatchesContract(mt.ObservedEncoding, mt.EncodingContract)
		sourceAvailable := track.SourcePath != ""
		if !pathsOK || !contractMatch || !observedMatch || mt.RegenerationState != "ready" {
			if reason == "" {
				reason = "encoding contract mismatch"
			}
			blockTrack(&track, sourceAvailable, reason)
		}
		if track.Status == TrackReady {
			if _, statErr := os.Stat(track.MediaPath); statErr != nil {
				blockTrack(&track, sourceAvailable, "メディアファイルが見つかりません")
			}
		}
		tracks[i] = track
	}
	return tracks, nil
}

func blockTrack(track *Track, canRegenerate bool, reason string) {
	track.MediaPath = ""
	track.NeedsRegeneration = canRegenerate
	track.Incompatible = !canRegenerate
	if canRegenerate {
		track.Status = TrackPreparing
		track.Error = "再生成が必要です: " + reason
	} else {
		track.Status = TrackFailed
		track.Error = "互換性がありません（再追加が必要）: " + reason
	}
}

func (s *Store) AuditCanonical(active video.RadioEncodingContract) ([]PlaylistAudit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return nil, err
	}
	audits := make([]PlaylistAudit, 0, len(index.Playlists))
	for _, entry := range index.Playlists {
		audit := PlaylistAudit{PlaylistID: entry.ID, Name: entry.Name}
		manifest, manifestErr := s.readManifest(entry)
		if manifestErr != nil {
			audit.Incompatible = entry.ID != ""
			audit.RegenerationReason = manifestErr.Error()
			versionDir, versionErr := s.versionDir(entry)
			for _, track := range entry.Tracks {
				canRegenerate := false
				if track.SourcePath != "" {
					if versionErr == nil {
						_, resolveErr := existingContainedAsset(versionDir, track.SourcePath)
						canRegenerate = resolveErr == nil
					} else if entry.ID == "" && entry.ActiveVersion == "" {
						_, resolveErr := existingLegacyAsset(s.mediaDir, track.SourcePath)
						canRegenerate = resolveErr == nil
					}
				}
				audit.Tracks = append(audit.Tracks, TrackAudit{TrackID: track.ID, NeedsRegeneration: canRegenerate, Incompatible: !canRegenerate, RegenerationReason: manifestErr.Error()})
				audit.Incompatible = audit.Incompatible || !canRegenerate
			}
			audit.NeedsRegeneration = len(audit.Tracks) > 0 && !allTracksIncompatible(audit.Tracks)
			finalizePlaylistAudit(&audit)
			audits = append(audits, audit)
			continue
		}
		activeRenderFingerprint := s.activeRecipe().AssetRenderFingerprint()
		playlistContractMismatch := manifest.EncodingFingerprint != manifest.EncodingContract.EncodingFingerprint() || manifest.EncodingFingerprint != active.EncodingFingerprint() || manifest.RenderRecipeFingerprint != manifest.RenderRecipeContract.Fingerprint() || manifest.RenderRecipeFingerprint != activeRenderFingerprint
		if playlistContractMismatch {
			audit.NeedsRegeneration = true
			audit.RegenerationReason = "playlist encoding contract mismatch"
		}
		versionDir, versionErr := s.versionDir(entry)
		for _, mt := range manifest.Tracks {
			trackAudit := TrackAudit{TrackID: mt.Track.ID}
			match, reason := observedMatchesContract(mt.ObservedEncoding, mt.EncodingContract)
			pathsOK := versionErr == nil
			sourceAvailable := false
			if pathsOK {
				for _, path := range []string{mt.Track.MediaPath, mt.Track.ThumbnailPath} {
					if path == "" {
						continue
					}
					if _, pathErr := existingContainedAsset(versionDir, path); pathErr != nil {
						pathsOK = false
					}
				}
				if mt.Track.SourcePath != "" {
					_, sourceErr := existingContainedAsset(versionDir, mt.Track.SourcePath)
					sourceAvailable = sourceErr == nil
					if sourceErr != nil {
						pathsOK = false
					}
				}
			}
			if mt.Track.Status == TrackReady && mt.Track.MediaPath == "" {
				pathsOK = false
			}
			if playlistContractMismatch || mt.EncodingFingerprint != mt.EncodingContract.EncodingFingerprint() || mt.EncodingFingerprint != active.EncodingFingerprint() || mt.RenderRecipeFingerprint != mt.RenderRecipeContract.Fingerprint() || mt.RenderRecipeFingerprint != activeRenderFingerprint || !match || !pathsOK || mt.RegenerationState != "ready" {
				if mt.RegenerationReason != "" {
					reason = mt.RegenerationReason
				}
				if reason == "" {
					if !pathsOK {
						reason = "saved asset path is invalid"
					} else {
						reason = "encoding contract mismatch"
					}
				}
				trackAudit.RegenerationReason = reason
				trackAudit.NeedsRegeneration = sourceAvailable
				trackAudit.Incompatible = !sourceAvailable
				audit.NeedsRegeneration = audit.NeedsRegeneration || trackAudit.NeedsRegeneration
				audit.Incompatible = audit.Incompatible || trackAudit.Incompatible
			}
			audit.Tracks = append(audit.Tracks, trackAudit)
		}
		finalizePlaylistAudit(&audit)
		audits = append(audits, audit)
	}
	return audits, nil
}

func (s *Store) RegenerateTrack(name, trackID string, replacement Track) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return err
	}
	entry, err := resolveStoredPlaylistByName(index, name)
	if err != nil {
		return err
	}
	return s.regenerateTrackLocked(entry, trackID, replacement)
}

func (s *Store) RegenerateTrackID(id, trackID string, replacement Track) error {
	if !validStorageID(id) {
		return errors.New("invalid playlist ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return err
	}
	entry, ok := findStoredPlaylistByID(index, id)
	if !ok {
		return fmt.Errorf("playlist ID %q not found", id)
	}
	return s.regenerateTrackLocked(entry, trackID, replacement)
}

func (s *Store) regenerateTrackLocked(entry storedPlaylist, trackID string, replacement Track) error {
	manifest, err := s.readManifest(entry)
	if err != nil {
		return err
	}
	dir, err := s.versionDir(entry)
	if err != nil {
		return err
	}
	tracks := make([]Track, len(manifest.Tracks))
	found := false
	for i, mt := range manifest.Tracks {
		track := mt.Track
		for _, field := range []*string{&track.MediaPath, &track.SourcePath, &track.ThumbnailPath} {
			if *field != "" {
				*field, err = resolveContainedAsset(dir, *field)
				if err != nil {
					return err
				}
			}
		}
		contract := mt.EncodingContract.Clone()
		track.EncodingContract = &contract
		renderContract := mt.RenderRecipeContract
		track.RenderRecipeContract = &renderContract
		track.RenderContentValues = mt.RenderContentValues
		if track.ID == trackID {
			replacement.ID = trackID
			if replacement.SourcePath == "" {
				replacement.SourcePath = track.SourcePath
			}
			if replacement.ThumbnailPath == "" {
				replacement.ThumbnailPath = track.ThumbnailPath
			}
			active := s.activeRecipe().NormalizedEncodingContract()
			replacement.EncodingContract = &active
			activeRender := s.activeRecipe().AssetRenderRecipeContract()
			replacement.RenderRecipeContract = &activeRender
			replacement.RenderContentValues = s.activeRecipe().AssetRenderContentValues()
			track = replacement
			found = true
		}
		tracks[i] = track
	}
	if !found {
		return fmt.Errorf("track %q not found", trackID)
	}
	return s.saveIDLocked(entry.ID, entry.Name, tracks, false)
}

func (s *Store) Materialize(name, runtimeDir string) ([]Track, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return nil, err
	}
	entry, err := resolveStoredPlaylistByName(index, name)
	if err != nil {
		return nil, err
	}
	return s.materializeLocked(entry, runtimeDir)
}

func (s *Store) MaterializeID(id, runtimeDir string) ([]Track, error) {
	if !validStorageID(id) {
		return nil, errors.New("invalid playlist ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.load()
	if err != nil {
		return nil, err
	}
	entry, ok := findStoredPlaylistByID(index, id)
	if !ok {
		return nil, fmt.Errorf("playlist ID %q not found", id)
	}
	return s.materializeLocked(entry, runtimeDir)
}

func (s *Store) materializeLocked(entry storedPlaylist, runtimeDir string) ([]Track, error) {
	saved, err := s.loadTracks(entry, s.activeRecipe().NormalizedEncodingContract())
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(runtimeDir, ".materialize-")
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	loadID := newStorageID()
	materialized := make([]Track, len(saved))
	for i, track := range saved {
		track.ID = newTrackID()
		track.AddedAt = s.now()
		if track.Status == TrackReady {
			if err := materializeAsset(tmpDir, "media", track.ID, &track.MediaPath); err != nil {
				blockTrack(&track, track.SourcePath != "", "saved media unavailable")
			}
		}
		if err := materializeAsset(tmpDir, "artwork", track.ID, &track.ThumbnailPath); err != nil {
			return nil, err
		}
		if err := materializeAsset(tmpDir, "source", track.ID, &track.SourcePath); err != nil {
			return nil, err
		}
		materialized[i] = track
	}
	finalDir := filepath.Join(runtimeDir, loadID)
	if err := s.rename(tmpDir, finalDir); err != nil {
		return nil, err
	}
	for i := range materialized {
		materialized[i].MediaPath = materializedPath(finalDir, materialized[i].MediaPath)
		materialized[i].ThumbnailPath = materializedPath(finalDir, materialized[i].ThumbnailPath)
		materialized[i].SourcePath = materializedPath(finalDir, materialized[i].SourcePath)
	}
	committed = true
	return materialized, nil
}

func (s *Store) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(f.Playlists))
	for i := range f.Playlists {
		names[i] = f.Playlists[i].Name
	}
	return names, nil
}

func (s *Store) ListEntries() ([]PlaylistEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]PlaylistEntry, len(f.Playlists))
	for i, p := range f.Playlists {
		if p.ID != "" && !validStorageID(p.ID) {
			return nil, errors.New("invalid playlist ID in index")
		}
		out[i] = PlaylistEntry{ID: p.ID, Name: p.Name}
	}
	return out, nil
}

func (s *Store) DeleteID(id string) error {
	if !validStorageID(id) {
		return errors.New("invalid playlist ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	for i, entry := range f.Playlists {
		if entry.ID != id {
			continue
		}
		root, err := s.playlistRoot(id, false)
		if err != nil {
			return err
		}
		f.Playlists = append(f.Playlists[:i], f.Playlists[i+1:]...)
		if err := s.write(f); err != nil {
			return err
		}
		return os.RemoveAll(root)
	}
	return fmt.Errorf("playlist ID %q not found", id)
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	entry, err := resolveStoredPlaylistByName(f, name)
	if err != nil {
		return err
	}
	if entry.ID == "" {
		return errors.New("legacy playlist must be migrated before delete")
	}
	root, err := s.playlistRoot(entry.ID, false)
	if err != nil {
		return err
	}
	for i := range f.Playlists {
		if f.Playlists[i].ID == entry.ID {
			f.Playlists = append(f.Playlists[:i], f.Playlists[i+1:]...)
			break
		}
	}
	if err := s.write(f); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func copyVersionAsset(dir, kind, trackID, source string) (string, error) {
	name := fmt.Sprintf("%s-%s%s", kind, safeComponent(trackID), filepath.Ext(source))
	if err := copyMediaFile(filepath.Join(dir, name), source); err != nil {
		return "", err
	}
	return name, nil
}

func materializeAsset(dir, kind, trackID string, path *string) error {
	if *path == "" {
		return nil
	}
	name := fmt.Sprintf("%s-%s%s", kind, safeComponent(trackID), filepath.Ext(*path))
	if err := linkOrCopyMediaFile(filepath.Join(dir, name), *path); err != nil {
		return err
	}
	*path = name
	return nil
}

func materializedPath(dir, path string) string {
	if path == "" {
		return ""
	}
	return filepath.Join(dir, path)
}

func findStoredPlaylist(f storeFile, name string) (storedPlaylist, bool) {
	matches := findStoredPlaylistsByName(f, name)
	if len(matches) != 1 {
		return storedPlaylist{}, false
	}
	return matches[0], true
}

func findStoredPlaylistsByName(f storeFile, name string) []storedPlaylist {
	var out []storedPlaylist
	for _, entry := range f.Playlists {
		if entry.Name == name {
			out = append(out, entry)
		}
	}
	return out
}
func resolveStoredPlaylistByName(f storeFile, name string) (storedPlaylist, error) {
	matches := findStoredPlaylistsByName(f, name)
	if len(matches) == 0 {
		return storedPlaylist{}, fmt.Errorf("プレイリスト %q が見つかりません", name)
	}
	if len(matches) > 1 {
		return storedPlaylist{}, fmt.Errorf("playlist name %q is ambiguous", name)
	}
	return matches[0], nil
}
func findStoredPlaylistByID(f storeFile, id string) (storedPlaylist, bool) {
	for _, entry := range f.Playlists {
		if entry.ID == id {
			return entry, true
		}
	}
	return storedPlaylist{}, false
}

func resolveContainedAsset(base, relative string) (string, error) {
	if relative == "" {
		return "", nil
	}
	if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" {
		return "", errors.New("absolute manifest asset path")
	}
	path := filepath.Join(base, relative)
	if err := ensureSameVolumeContained(base, path); err != nil {
		return "", err
	}
	return path, nil
}

func resolveLegacyAsset(base, path string) (string, error) {
	candidate := path
	if !filepath.IsAbs(candidate) && filepath.VolumeName(candidate) == "" {
		candidate = filepath.Join(base, candidate)
	}
	if err := ensureSameVolumeContained(base, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

func existingContainedAsset(base, relative string) (string, error) {
	path, err := resolveContainedAsset(base, relative)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if err := ensureSameVolumeContained(base, resolved); err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("saved asset is not a regular file")
	}
	return path, nil
}

func existingLegacyAsset(base, path string) (string, error) {
	resolved, err := resolveLegacyAsset(base, path)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	if err := ensureSameVolumeContained(base, realPath); err != nil {
		return "", err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("legacy asset is not a regular file")
	}
	return path, nil
}

func ensureSameVolumeContained(base, candidate string) error {
	baseAbs, err := canonicalPathForContainment(base)
	if err != nil {
		return err
	}
	candidateAbs, err := canonicalPathForContainment(candidate)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.VolumeName(baseAbs), filepath.VolumeName(candidateAbs)) {
		return errors.New("path crosses filesystem volume")
	}
	rel, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("path escapes playlist version")
	}
	return nil
}

// canonicalPathForContainment resolves existing symlinks and preserves the
// real path of the nearest existing parent for paths that are not created yet.
// macOS commonly exposes the temporary directory through /var while the
// filesystem resolves it to /private/var; comparing the two textual paths
// would incorrectly classify a contained asset as external.
func canonicalPathForContainment(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := canonicalPathForContainment(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func safeComponent(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "asset"
	}
	return b.String()
}

func validStorageID(id string) bool {
	if len(id) != 24 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func containedChild(root, name string, mustExist bool) (string, error) {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return "", errors.New("invalid contained path component")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(rootResolved, name)
	if err := ensureSameVolumeContained(rootResolved, candidate); err != nil {
		return "", err
	}
	if info, lerr := os.Lstat(candidate); lerr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("contained path is a symlink")
		}
		resolved, rerr := filepath.EvalSymlinks(candidate)
		if rerr != nil {
			return "", rerr
		}
		if err := ensureSameVolumeContained(rootResolved, resolved); err != nil {
			return "", err
		}
		return filepath.Join(rootAbs, name), nil
	} else if !errors.Is(lerr, os.ErrNotExist) {
		return "", lerr
	}
	if mustExist {
		return "", os.ErrNotExist
	}
	return filepath.Join(rootAbs, name), nil
}

func (s *Store) playlistRoot(id string, create bool) (string, error) {
	if !validStorageID(id) {
		return "", errors.New("invalid playlist ID")
	}
	if err := os.MkdirAll(s.mediaDir, 0700); err != nil {
		return "", err
	}
	root, err := containedChild(s.mediaDir, id, false)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) && create {
		if err := os.Mkdir(root, 0700); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return containedChild(s.mediaDir, id, true)
}

func (s *Store) removeVersion(id, version string) error {
	if !versionNamePattern.MatchString(version) {
		return errors.New("invalid playlist version")
	}
	root, err := s.playlistRoot(id, false)
	if err != nil {
		return err
	}
	path, err := containedChild(root, version, true)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func newStorageID() string {
	var b [12]byte
	if _, err := storageIDRandomRead(b[:]); err != nil {
		seed := fmt.Sprintf("%d:%d", storageIDFallbackNow().UnixNano(), storageIDFallbackCounter.Add(1))
		sum := sha256.Sum256([]byte(seed))
		copy(b[:], sum[:12])
	}
	return hex.EncodeToString(b[:])
}

func newUniqueStorageID(index storeFile) (string, error) {
	used := map[string]bool{}
	for _, entry := range index.Playlists {
		if entry.ID != "" {
			used[entry.ID] = true
		}
	}
	for i := 0; i < 128; i++ {
		id := newStorageID()
		if validStorageID(id) && !used[id] {
			return id, nil
		}
	}
	return "", errors.New("could not allocate unique playlist ID")
}
func validateStoreFile(f storeFile) error {
	seen := map[string]bool{}
	for _, entry := range f.Playlists {
		if entry.ID == "" {
			continue
		}
		if !validStorageID(entry.ID) {
			return errors.New("invalid playlist ID in index")
		}
		if seen[entry.ID] {
			return fmt.Errorf("duplicate playlist ID %q", entry.ID)
		}
		seen[entry.ID] = true
	}
	return nil
}
func validateTrackIDs(tracks []Track) error {
	seen := map[string]bool{}
	for _, track := range tracks {
		if track.ID == "" {
			return errors.New("empty track ID")
		}
		if seen[track.ID] {
			return fmt.Errorf("duplicate track ID %q", track.ID)
		}
		seen[track.ID] = true
	}
	return nil
}
func ptrContract(c video.RadioEncodingContract) *video.RadioEncodingContract { return &c }
func allTracksIncompatible(tracks []TrackAudit) bool {
	for _, track := range tracks {
		if !track.Incompatible {
			return false
		}
	}
	return len(tracks) > 0
}

func manifestRegenerationDiagnostics(tracks []ManifestTrack) RegenerationDiagnostics {
	var d RegenerationDiagnostics
	seen := map[string]bool{}
	for _, track := range tracks {
		switch track.RegenerationState {
		case "regeneration-required":
			d.NeedsRegenerationCount++
		case "incompatible":
			d.IncompatibleCount++
		}
		if track.RegenerationReason != "" && !seen[track.RegenerationReason] {
			seen[track.RegenerationReason] = true
			d.Reasons = append(d.Reasons, track.RegenerationReason)
		}
	}
	return d
}
func finalizePlaylistAudit(a *PlaylistAudit) {
	a.NeedsRegeneration = false
	a.Incompatible = false
	a.NeedsRegenerationCount = 0
	a.IncompatibleCount = 0
	seen := map[string]bool{}
	var reasons []string
	for i := range a.Tracks {
		track := &a.Tracks[i]
		if track.NeedsRegeneration {
			track.Incompatible = false
			a.NeedsRegenerationCount++
		}
		if track.Incompatible {
			track.NeedsRegeneration = false
			a.IncompatibleCount++
		}
		if track.RegenerationReason != "" && !seen[track.RegenerationReason] {
			seen[track.RegenerationReason] = true
			reasons = append(reasons, track.RegenerationReason)
		}
	}
	a.NeedsRegeneration = a.NeedsRegenerationCount > 0
	a.Incompatible = a.IncompatibleCount > 0
	if len(reasons) > 0 {
		a.RegenerationReason = strings.Join(reasons, "; ")
	}
}

func writeSyncedFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		// Windows does not support syncing a directory handle. Files are flushed
		// before rename; MoveFileEx-backed rename supplies the atomic pointer swap.
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func copyMediaFile(dst, src string) error {
	if filepath.Clean(dst) == filepath.Clean(src) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func linkOrCopyMediaFile(dst, src string) error {
	if err := linkMediaFile(src, dst); err == nil {
		return nil
	}
	return copyMediaFile(dst, src)
}
