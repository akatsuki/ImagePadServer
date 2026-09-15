package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestResetDirClearsWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leftover.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ResetDir(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty workspace, got %d entries", len(entries))
	}
}

func TestStoreResetClearsCurrent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentInfo(CurrentImage{
		FileName:    "current.jpg",
		PublicName:  "current.jpg",
		ContentType: "image/jpeg",
	}); err != nil {
		t.Fatal(err)
	}
	if store.Current() == nil {
		t.Fatal("expected current image before reset")
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if store.Current() != nil {
		t.Fatal("expected current image to be cleared after reset")
	}
}

func TestPendingCurrentDoesNotCreateHistoryUntilCommitted(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	recording := filepath.Join(dir, "publisher-0001.mp4")
	if err := os.WriteFile(recording, []byte("verified-later"), 0600); err != nil {
		t.Fatal(err)
	}
	pending := CurrentImage{
		ID: "0123456789abcdef", Kind: "video", SourceKind: "airplay",
		FileName: filepath.Base(recording), PublicName: "airplay.mp4",
		ContentType: "video/mp4", OriginalName: "AirPlay",
	}
	if err := store.SetPendingCurrentInfoWithIDInMemory(pending); err != nil {
		t.Fatal(err)
	}
	if current := store.Current(); current == nil || current.ID != pending.ID || current.FileName != pending.FileName {
		t.Fatalf("pending current=%+v", current)
	}
	if history := store.History(); len(history) != 0 {
		t.Fatalf("pending recording entered history: %+v", history)
	}
	if err := store.CommitHistoryInfoWithIDInMemory(pending); err != nil {
		t.Fatal(err)
	}
	if history := store.History(); len(history) != 1 || history[0].ID != pending.ID {
		t.Fatalf("committed history=%+v", history)
	}
}

func TestCommitHistoryFromVerifiedNestedPathMakesPlayableCurrent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(dir, "airplay", "0123456789abcdef")
	if err := os.MkdirAll(sourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "publisher-0002.mp4")
	if err := os.WriteFile(source, []byte("verified-generation"), 0600); err != nil {
		t.Fatal(err)
	}
	info := CurrentImage{
		ID: "0123456789abcdef", Kind: "video", SourceKind: "airplay",
		FileName: filepath.Base(source), PublicName: "airplay.mp4", ContentType: "video/mp4",
	}
	if err := store.SetPendingCurrentInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryFromPathWithIDInMemory(source, info); err != nil {
		t.Fatal(err)
	}
	history := store.History()
	if len(history) != 1 || history[0].ID != info.ID {
		t.Fatalf("committed history=%+v", history)
	}
	currentPath, current, ok := store.CurrentPath()
	if !ok || current == nil || current.FileName != history[0].HistoryFileName {
		t.Fatalf("playable current path=%q current=%+v history=%+v", currentPath, current, history)
	}
	data, err := os.ReadFile(currentPath)
	if err != nil || string(data) != "verified-generation" {
		t.Fatalf("playable current artifact=%q err=%v", data, err)
	}
}

func TestCommitHistoryFromPathRejectsSourceOutsideStore(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.mp4")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	info := CurrentImage{ID: "0123456789abcdef", Kind: "video", FileName: "publisher-0001.mp4", PublicName: "airplay.mp4"}
	if err := store.SetPendingCurrentInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	revision := store.PublishedRevision()
	if err := store.CommitHistoryFromPathWithIDInMemory(outside, info); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("outside source error=%v", err)
	}
	if len(store.History()) != 0 || store.PublishedRevision() != revision {
		t.Fatal("rejected outside source mutated library state")
	}
}

func TestCommitHistoryFromPathAcceptsStoreRelativeSource(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("airplay", "0123456789abcdef", "publisher-0001.mp4")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, relative)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, relative), []byte("relative-source"), 0600); err != nil {
		t.Fatal(err)
	}
	info := CurrentImage{ID: "0123456789abcdef", Kind: "video", FileName: "publisher-0001.mp4", PublicName: "airplay.mp4"}
	if err := store.SetPendingCurrentInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryFromPathWithIDInMemory(relative, info); err != nil {
		t.Fatal(err)
	}
	currentPath, _, ok := store.CurrentPath()
	if !ok {
		t.Fatal("relative source did not produce current")
	}
	data, err := os.ReadFile(currentPath)
	if err != nil || string(data) != "relative-source" {
		t.Fatalf("relative current=%q err=%v", data, err)
	}
}

func TestCommitHistoryFromPathKeepsFavoriteReplacementPlayable(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "publisher-0001.mp4")
	secondDir := filepath.Join(dir, "airplay", "0123456789abcdef")
	second := filepath.Join(secondDir, "publisher-0002.mp4")
	if err := os.WriteFile(first, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	info := CurrentImage{ID: "0123456789abcdef", Kind: "video", FileName: filepath.Base(first), PublicName: "airplay.mp4"}
	if err := store.SetPendingCurrentInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite(info.ID, true); err != nil {
		t.Fatal(err)
	}
	info.FileName = filepath.Base(second)
	if err := store.CommitHistoryFromPathWithIDInMemory(second, info); err != nil {
		t.Fatal(err)
	}
	currentPath, _, ok := store.CurrentPath()
	if !ok {
		t.Fatal("favorite replacement did not produce current")
	}
	currentData, currentErr := os.ReadFile(currentPath)
	historyPath, _, historyOK := store.HistoryPath(info.ID)
	historyData, historyErr := os.ReadFile(historyPath)
	if !historyOK || currentErr != nil || historyErr != nil || string(currentData) != "second" || string(historyData) != "second" {
		t.Fatalf("favorite replacement current=%q/%v history=%q/%v ok=%t", currentData, currentErr, historyData, historyErr, historyOK)
	}
}

func TestCommitHistoryFromPathSupportsCommittedSourceAsReplacementInput(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "publisher-0001.mp4")
	if err := os.WriteFile(source, []byte("stable"), 0600); err != nil {
		t.Fatal(err)
	}
	info := CurrentImage{ID: "0123456789abcdef", Kind: "video", FileName: filepath.Base(source), PublicName: "airplay.mp4"}
	if err := store.SetPendingCurrentInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryFromPathWithIDInMemory(source, info); err != nil {
		t.Fatal(err)
	}
	currentPath, current, ok := store.CurrentPath()
	if !ok {
		t.Fatal("first commit did not produce current")
	}
	info.FileName = current.FileName
	if err := store.CommitHistoryFromPathWithIDInMemory(currentPath, info); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(currentPath)
	if err != nil || string(data) != "stable" {
		t.Fatalf("source-equals-target replacement=%q err=%v", data, err)
	}
}

func TestPruneHistoryKeepsArtifactReferencedByCurrent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "publisher-0001.mp4")
	if err := os.WriteFile(source, []byte("selected-recording"), 0600); err != nil {
		t.Fatal(err)
	}
	selected := CurrentImage{ID: "selected", Kind: "video", FileName: filepath.Base(source), PublicName: "selected.mp4"}
	if err := store.SetPendingCurrentInfoWithIDInMemory(selected); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryFromPathWithIDInMemory(source, selected); err != nil {
		t.Fatal(err)
	}
	currentPath, _, ok := store.CurrentPath()
	if !ok {
		t.Fatal("selected recording did not become current")
	}
	for index := 0; index < 41; index++ {
		path := filepath.Join(dir, fmt.Sprintf("new-%02d.mp4", index))
		if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AddHistory(path, CurrentImage{ID: fmt.Sprintf("new-%02d", index), Kind: "video", PublicName: "new.mp4"}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(currentPath)
	if err != nil || string(data) != "selected-recording" {
		t.Fatalf("prune removed current artifact: data=%q err=%v", data, err)
	}
}

func TestPendingAndHistoryCommitRejectUnsafeOrCaseVariantIDs(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "..\\escape", "AirPlayID"} {
		if err := store.SetPendingCurrentInfoWithIDInMemory(CurrentImage{ID: id, FileName: "publisher.mp4"}); !errors.Is(err, os.ErrInvalid) {
			t.Fatalf("unsafe ID %q error=%v", id, err)
		}
	}
	if store.Current() != nil || len(store.History()) != 0 {
		t.Fatal("unsafe IDs mutated library state")
	}
}

func TestCommitHistoryFromPathRejectsEscapingRecordingAndThumbnailSymlinks(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	outsideRecording := filepath.Join(root, "outside.mp4")
	outsideThumbnail := filepath.Join(root, "outside.jpg")
	if err := os.WriteFile(outsideRecording, []byte("outside-recording"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideThumbnail, []byte("outside-thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}
	recordingLink := filepath.Join(store.Dir(), "recording-link.mp4")
	thumbnailLink := filepath.Join(store.Dir(), "thumbnail-link.jpg")
	if err := os.Symlink(outsideRecording, recordingLink); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if err := os.Symlink(outsideThumbnail, thumbnailLink); err != nil {
		t.Skipf("thumbnail symlink creation is unavailable: %v", err)
	}
	info := CurrentImage{ID: "0123456789abcdef", Kind: "video", FileName: filepath.Base(recordingLink), PublicName: "airplay.mp4"}
	if err := store.SetPendingCurrentInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	revision := store.PublishedRevision()
	if err := store.CommitHistoryFromPathWithIDInMemory(recordingLink, info); err == nil {
		t.Fatal("escaping recording symlink was accepted")
	}
	insideRecording := filepath.Join(store.Dir(), "inside.mp4")
	if err := os.WriteFile(insideRecording, []byte("inside-recording"), 0600); err != nil {
		t.Fatal(err)
	}
	info.FileName = filepath.Base(insideRecording)
	info.Thumbnail = filepath.Base(thumbnailLink)
	if err := store.CommitHistoryFromPathWithIDInMemory(insideRecording, info); err == nil {
		t.Fatal("escaping thumbnail symlink was accepted")
	}
	if len(store.History()) != 0 || store.PublishedRevision() != revision {
		t.Fatal("rejected symlink source mutated library state")
	}
}

func TestOpenOwnedFileConfinesNestedRecordingToStore(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nestedDir := filepath.Join(store.Dir(), "airplay", "session", "publisher-0001")
	if err := os.MkdirAll(nestedDir, 0700); err != nil {
		t.Fatal(err)
	}
	recording := filepath.Join(nestedDir, "recording.mp4")
	if err := os.WriteFile(recording, []byte("verified-recording"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := store.OpenOwnedFile(recording)
	if err != nil {
		t.Fatalf("open nested owned recording: %v", err)
	}
	buffer := make([]byte, len("verified-recording"))
	if _, err := file.Read(buffer); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "verified-recording" {
		t.Fatalf("owned recording body = %q", buffer)
	}

	outside := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if file, err := store.OpenOwnedFile(outside); !errors.Is(err, os.ErrPermission) {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("outside file error = %v, want permission", err)
	}
	if file, err := store.OpenOwnedFile(nestedDir); !errors.Is(err, os.ErrInvalid) {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("directory error = %v, want invalid", err)
	}

	link := filepath.Join(store.Dir(), "recording-link.mp4")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if file, err := store.OpenOwnedFile(link); err == nil {
		if file != nil {
			_ = file.Close()
		}
		t.Fatal("escaping recording symlink was opened")
	}
}

func TestCommitOldHistoryDoesNotReplaceNewerPendingCurrent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldRecording := filepath.Join(dir, "publisher-old.mp4")
	newRecording := filepath.Join(dir, "publisher-new.mp4")
	if err := os.WriteFile(oldRecording, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newRecording, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	oldInfo := CurrentImage{ID: "0123456789abcdef", Kind: "video", FileName: filepath.Base(oldRecording), PublicName: "old.mp4"}
	newInfo := CurrentImage{ID: "fedcba9876543210", Kind: "video", FileName: filepath.Base(newRecording), PublicName: "new.mp4"}
	if err := store.SetPendingCurrentInfoWithIDInMemory(oldInfo); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPendingCurrentInfoWithIDInMemory(newInfo); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryInfoWithIDInMemory(oldInfo); err != nil {
		t.Fatal(err)
	}
	if current := store.Current(); current == nil || current.ID != newInfo.ID || current.FileName != newInfo.FileName {
		t.Fatalf("old history commit replaced newer current: %+v", current)
	}
	history := store.History()
	if len(history) != 1 || history[0].ID != oldInfo.ID {
		t.Fatalf("old history was not committed independently: %+v", history)
	}
}

func TestCommitHistoryWithSameIDReplacesOneHistoryArtifact(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(dir, "publisher-0001.mp4")
	secondPath := filepath.Join(dir, "publisher-0002.mp4")
	if err := os.WriteFile(firstPath, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second-generation"), 0600); err != nil {
		t.Fatal(err)
	}
	info := CurrentImage{ID: "0123456789abcdef", Kind: "video", FileName: filepath.Base(firstPath), PublicName: "airplay.mp4"}
	if err := store.SetPendingCurrentInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	info.FileName = filepath.Base(secondPath)
	if err := store.CommitHistoryInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	history := store.History()
	if len(history) != 1 || history[0].ID != info.ID || history[0].FileName != info.FileName {
		t.Fatalf("same session created duplicate history: %+v", history)
	}
	historyPath, _, ok := store.HistoryPath(info.ID)
	if !ok {
		t.Fatal("committed history path was not found")
	}
	data, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second-generation" {
		t.Fatalf("history artifact=%q", data)
	}
}

func TestCommitHistoryWithSameIDPreservesUnpublishedState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(dir, "publisher-0001.mp4")
	secondPath := filepath.Join(dir, "publisher-0002.mp4")
	if err := os.WriteFile(firstPath, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	info := CurrentImage{ID: "0123456789abcdef", Kind: "video", FileName: filepath.Base(firstPath), PublicName: "airplay.mp4"}
	if err := store.SetPendingCurrentInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPublished(info.ID, false); err != nil {
		t.Fatal(err)
	}
	info.FileName = filepath.Base(secondPath)
	if err := store.CommitHistoryInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	history := store.History()
	if len(history) != 1 || history[0].Published {
		t.Fatalf("same-ID replacement changed history publication state: %+v", history)
	}
	if current := store.Current(); current == nil || current.ID != info.ID || current.Published {
		t.Fatalf("same-ID replacement changed current publication state: %+v", current)
	}
}

func TestFailedHistoryReplacementPreservesPreviousArtifactAndMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(dir, "publisher-0001.mp4")
	secondPath := filepath.Join(dir, "publisher-0002.mp4")
	thumbnail := filepath.Join(dir, "first-thumbnail.jpg")
	if err := os.WriteFile(firstPath, []byte("first-generation"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second-generation"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbnail, []byte("first-thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}
	info := CurrentImage{
		ID: "0123456789abcdef", Kind: "video",
		FileName: filepath.Base(firstPath), PublicName: "airplay.mp4",
		Thumbnail: filepath.Base(thumbnail),
	}
	if err := store.SetPendingCurrentInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryInfoWithIDInMemory(info); err != nil {
		t.Fatal(err)
	}
	revision := store.PublishedRevision()
	failed := info
	failed.FileName = filepath.Base(secondPath)
	failed.Thumbnail = "missing-thumbnail.jpg"
	if err := store.CommitHistoryInfoWithIDInMemory(failed); err == nil {
		t.Fatal("history replacement with missing thumbnail succeeded")
	}
	if store.PublishedRevision() != revision {
		t.Fatal("failed history replacement changed published revision")
	}
	history := store.History()
	if len(history) != 1 || history[0].FileName != info.FileName || history[0].Thumbnail != thumbnailFileName(info) {
		t.Fatalf("failed replacement changed history metadata: %+v", history)
	}
	historyPath, _, ok := store.HistoryPath(info.ID)
	if !ok {
		t.Fatal("previous history path disappeared")
	}
	data, err := os.ReadFile(historyPath)
	if err != nil || string(data) != "first-generation" {
		t.Fatalf("previous history artifact changed: data=%q err=%v", data, err)
	}
	thumbnailPath, _, ok := store.HistoryThumbnailPath(info.ID)
	if !ok {
		t.Fatal("previous history thumbnail disappeared")
	}
	thumbData, err := os.ReadFile(thumbnailPath)
	if err != nil || string(thumbData) != "first-thumbnail" {
		t.Fatalf("previous thumbnail changed: data=%q err=%v", thumbData, err)
	}
	if current := store.Current(); current == nil || current.FileName != info.FileName || current.Thumbnail != thumbnailFileName(info) {
		t.Fatalf("failed replacement changed current metadata: %+v", current)
	}
}

func TestHistoryCommitRejectsMissingDirectoryAndEscapingNames(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHistoryInfoWithIDInMemory(CurrentImage{ID: "0123456789abcdef", FileName: "missing.mp4"}); !os.IsNotExist(err) {
		t.Fatalf("missing recording error=%v", err)
	}
	if err := store.CommitHistoryInfoWithIDInMemory(CurrentImage{ID: "0123456789abcdef", FileName: "."}); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("directory recording error=%v", err)
	}
	if err := store.CommitHistoryInfoWithIDInMemory(CurrentImage{ID: "0123456789abcdef", FileName: "..\\outside.mp4"}); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("escaping recording error=%v", err)
	}
	if err := store.SetPendingCurrentInfoWithIDInMemory(CurrentImage{}); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("empty pending ID error=%v", err)
	}
	if len(store.History()) != 0 || store.Current() != nil {
		t.Fatal("rejected library operations mutated state")
	}
}

func TestHistoryReplacementRollsBackEarlierFileWhenLaterTargetIsDirectory(t *testing.T) {
	dir := t.TempDir()
	firstTarget := filepath.Join(dir, "history.mp4")
	firstStage := filepath.Join(dir, "history.pending")
	secondTarget := filepath.Join(dir, "thumbnail-directory")
	secondStage := filepath.Join(dir, "thumbnail.pending")
	if err := os.WriteFile(firstTarget, []byte("old-recording"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstStage, []byte("new-recording"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(secondTarget, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondStage, []byte("new-thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}
	replacements := []historyFileReplacement{
		{target: firstTarget, stage: firstStage, backup: filepath.Join(dir, "history.backup")},
		{target: secondTarget, stage: secondStage, backup: filepath.Join(dir, "thumbnail.backup")},
	}
	if err := commitHistoryFileReplacements(replacements); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("commit error=%v, want os.ErrInvalid", err)
	}
	data, err := os.ReadFile(firstTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old-recording" {
		t.Fatalf("earlier replacement was not rolled back: %q", data)
	}
}

func TestHistoryReplacementCleanupPreservesUnrestoredBackup(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "history.backup")
	stage := filepath.Join(dir, "history.pending")
	if err := os.WriteFile(backup, []byte("recoverable-original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, []byte("uninstalled-new-file"), 0600); err != nil {
		t.Fatal(err)
	}
	replacements := []historyFileReplacement{{
		target:      filepath.Join(dir, "missing-parent", "history.mp4"),
		stage:       stage,
		backup:      backup,
		targetMoved: true,
	}}
	if err := rollbackHistoryFileReplacements(replacements); err == nil {
		t.Fatal("rollback unexpectedly restored into a missing parent")
	}
	cleanupHistoryFileReplacements(replacements)
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("unrestored backup was deleted: %v", err)
	}
	if string(data) != "recoverable-original" {
		t.Fatalf("backup content=%q", data)
	}
}

func TestSetCurrentFromHistoryRestoresConvertedFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "obs-recording.mp4")
	if err := os.WriteFile(source, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(source, CurrentImage{
		ID:           "obs1",
		Kind:         "video",
		FileName:     "obs-recording.mp4",
		PublicName:   "obs-obs1.mp4",
		ContentType:  "video/mp4",
		OriginalName: "OBS",
	})
	if err != nil {
		t.Fatal(err)
	}
	playlist := filepath.Join(dir, "current-"+item.ID+".m3u8")
	segment := filepath.Join(dir, "current-"+item.ID+"-0.ts")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n#EXT-X-ENDLIST\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(segment, []byte("ts"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkConverted(item.ID, []string{playlist, segment}); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(playlist)
	_ = os.Remove(segment)

	if err := store.SetCurrentFromHistory(item.ID); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	if current == nil || !current.Converted {
		t.Fatalf("expected converted current item, got %#v", current)
	}
	if _, err := os.Stat(playlist); err != nil {
		t.Fatalf("expected playlist restored: %v", err)
	}
	if _, err := os.Stat(segment); err != nil {
		t.Fatalf("expected segment restored: %v", err)
	}
}

func TestUpdateCurrentSizePersistsConvertedSize(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentInfo(CurrentImage{
		FileName:    "current.mp4",
		PublicName:  "current.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateCurrentSize(500); err != nil {
		t.Fatal(err)
	}
	if got := store.Current().SizeBytes; got != 500 {
		t.Fatalf("SizeBytes = %d, want 500", got)
	}
}

func TestUpdateHistorySizeUpdatesInMemoryItem(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dummy := filepath.Join(store.Dir(), "dummy.mp4")
	if err := os.WriteFile(dummy, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(dummy, CurrentImage{
		PublicName:  "clip.mp4",
		ContentType: "video/mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateHistorySize(item.ID, 2500); err != nil {
		t.Fatal(err)
	}
	for _, h := range store.History() {
		if h.ID == item.ID && h.SizeBytes != 2500 {
			t.Fatalf("history SizeBytes = %d, want 2500", h.SizeBytes)
		}
	}
}

func TestUpdateHistorySizePersistsFavoriteSize(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dummy := filepath.Join(store.Dir(), "dummy.mp4")
	if err := os.WriteFile(dummy, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(dummy, CurrentImage{
		PublicName:  "clip.mp4",
		ContentType: "video/mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite(item.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateHistorySize(item.ID, 800); err != nil {
		t.Fatal(err)
	}

	// Re-create store to verify favorites.json persistence.
	store2, err := NewStore(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range store2.History() {
		if h.ID == item.ID && h.SizeBytes != 800 {
			t.Fatalf("favorite history SizeBytes = %d, want 800", h.SizeBytes)
		}
	}
}

// TestPublishedFlagPersistsForFavorite は favorite 項目の Published フラグが
// favorites.json 経由で再起動をまたいで永続化されることを検証する。
func TestPublishedFlagPersistsForFavorite(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dummy := filepath.Join(store.Dir(), "dummy.png")
	if err := os.WriteFile(dummy, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(dummy, CurrentImage{PublicName: "img.png", ContentType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPublished(item.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite(item.ID, true); err != nil {
		t.Fatal(err)
	}

	// 再起動相当（同一 dir で NewStore → loadFavorites）
	store2, err := NewStore(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range store2.History() {
		if h.ID != item.ID {
			continue
		}
		found = true
		if !h.Published {
			t.Fatalf("favorite history Published = false after restart, want true")
		}
	}
	if !found {
		t.Fatal("favorite item not reloaded after restart")
	}
}

func TestCurrentImageAudioMetadata(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	info := CurrentImage{
		FileName:    "test.mp3",
		PublicName:  "test.mp3",
		ContentType: "audio/mpeg",
		SourceKind:  "soundcloud",
		Title:       "Test Title",
		Artist:      "Test Artist",
		Album:       "Test Album",
	}

	if err := store.SetCurrentInfo(info); err != nil {
		t.Fatal(err)
	}

	current := store.Current()
	if current.SourceKind != "soundcloud" {
		t.Fatalf("SourceKind = %q, want soundcloud", current.SourceKind)
	}
	if current.Title != "Test Title" {
		t.Fatalf("Title = %q, want Test Title", current.Title)
	}
	if current.Artist != "Test Artist" {
		t.Fatalf("Artist = %q, want Test Artist", current.Artist)
	}
	if current.Album != "Test Album" {
		t.Fatalf("Album = %q, want Test Album", current.Album)
	}
}

func TestHistoryPreservesAudioMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(dir, "source.mp3")
	if err := os.WriteFile(source, []byte("audio data"), 0600); err != nil {
		t.Fatal(err)
	}

	item, err := store.AddHistory(source, CurrentImage{
		FileName:    "source.mp3",
		PublicName:  "source.mp3",
		ContentType: "audio/mpeg",
		SourceKind:  "soundcloud",
		Title:       "History Title",
		Artist:      "History Artist",
		Album:       "History Album",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "History Title" {
		t.Fatalf("Title = %q, want History Title", item.Title)
	}
	if item.Artist != "History Artist" {
		t.Fatalf("Artist = %q, want History Artist", item.Artist)
	}
	if item.Album != "History Album" {
		t.Fatalf("Album = %q, want History Album", item.Album)
	}

	history := store.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 history item, got %d", len(history))
	}
	if history[0].Title != "History Title" {
		t.Fatalf("history Title = %q, want History Title", history[0].Title)
	}
}
