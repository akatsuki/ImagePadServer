package video

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testDownloadRun records the yt-dlp args and creates dummy sidecar files.
func testDownloadRun(exe string, args ...string) error {
	// Record args to a file in the output directory.
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			outDir := filepath.Dir(args[i+1])
			os.WriteFile(filepath.Join(outDir, "mock-args.txt"), []byte(strings.Join(args, " ")), 0644)
			break
		}
	}

	// Find the output template and manifest path from args.
	var outTemplate string
	var manifestPath string
	p2fWait := 0 // --print-to-file has two following args: <template> <file>
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			outTemplate = args[i+1]
		}
		if a == "--print-to-file" {
			p2fWait = 2 // expect print template + file path
			continue
		}
		if p2fWait > 0 {
			p2fWait--
			if p2fWait == 0 {
				manifestPath = a // second arg after --print-to-file is the file path
			}
			continue
		}
	}

	if outTemplate == "" {
		return nil
	}

	// Derive base path by stripping the .%(ext)s template suffix.
	base := strings.TrimSuffix(outTemplate, ".%(ext)s")

	// Create dummy sidecar files.
	os.WriteFile(base+".mp3", []byte("dummy audio"), 0644)
	os.WriteFile(base+".jpg", []byte("dummy image"), 0644)
	os.WriteFile(base+".info.json", []byte(`{"title":"Test Track","uploader":"Test Uploader","artist":"Test Artist","album":"Test Album"}`), 0644)

	// Write manifest with the audio file path.
	if manifestPath != "" {
		os.WriteFile(manifestPath, []byte(base+".mp3\n"), 0644)
	}

	return nil
}

// ---------------------------------------------------------------------------
// DownloadSoundCloud argument construction tests
// ---------------------------------------------------------------------------

func TestDownloadSoundCloud_Args(t *testing.T) {
	oldRun := runDownloadCmd
	runDownloadCmd = testDownloadRun
	defer func() { runDownloadCmd = oldRun }()

	dir := t.TempDir()

	audio, err := DownloadSoundCloud(context.Background(), "mock-yt-dlp", "https://soundcloud.com/user/track", dir)
	if err != nil {
		t.Fatalf("DownloadSoundCloud failed: %v", err)
	}

	if audio.Kind != SourceSoundCloud {
		t.Errorf("Kind = %q, want %q", audio.Kind, SourceSoundCloud)
	}
	if audio.SourcePath == "" {
		t.Error("SourcePath must not be empty")
	}
	if audio.SoundCloudArtworkPath == "" {
		t.Error("SoundCloudArtworkPath must not be empty")
	}
	if audio.SoundCloudInformationPath == "" {
		t.Error("SoundCloudInformationPath must not be empty")
	}
	if audio.SoundCloudMetadata.Title != "Test Track" {
		t.Errorf("SoundCloudMetadata.Title = %q, want %q", audio.SoundCloudMetadata.Title, "Test Track")
	}
	if audio.SoundCloudMetadata.Artist != "Test Artist" {
		t.Errorf("SoundCloudMetadata.Artist = %q, want %q", audio.SoundCloudMetadata.Artist, "Test Artist")
	}

	// Verify args captured by mock.
	argsData, err := os.ReadFile(filepath.Join(dir, "mock-args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	joined := string(argsData)

	checks := []struct {
		name, substr string
	}{
		{"no-playlist", "--no-playlist"},
		{"no-warnings", "--no-warnings"},
		{"max-filesize MaxMediaSourceBytes", "--max-filesize 4294967295"},
		{"write-thumbnail", "--write-thumbnail"},
		{"write-info-json", "--write-info-json"},
		{"print-to-file", "--print-to-file"},
		{"after_move:filepath", "after_move:filepath"},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(joined, c.substr) {
				t.Errorf("args must contain %q: %s", c.substr, joined)
			}
		})
	}
}

func TestDownloadSoundCloud_UniqueJobPrefixes(t *testing.T) {
	oldRun := runDownloadCmd
	runDownloadCmd = testDownloadRun
	defer func() { runDownloadCmd = oldRun }()

	dir := t.TempDir()

	first, err := DownloadSoundCloud(context.Background(), "mock-yt-dlp", "https://soundcloud.com/user/first", dir)
	if err != nil {
		t.Fatalf("first download failed: %v", err)
	}
	second, err := DownloadSoundCloud(context.Background(), "mock-yt-dlp", "https://soundcloud.com/user/second", dir)
	if err != nil {
		t.Fatalf("second download failed: %v", err)
	}

	if first.SourcePath == second.SourcePath {
		t.Fatal("two successive downloads produced the same path; queued jobs can overwrite each other")
	}
}

func TestDownloadSoundCloud_InvalidURL(t *testing.T) {
	oldRun := runDownloadCmd
	runDownloadCmd = testDownloadRun
	defer func() { runDownloadCmd = oldRun }()

	dir := t.TempDir()

	_, err := DownloadSoundCloud(context.Background(), "mock-yt-dlp", "not-a-valid-url", dir)
	if err == nil {
		t.Fatal("expected error for non-SoundCloud URL")
	}
}

// ---------------------------------------------------------------------------
// ReadSinglePathManifest tests
// ---------------------------------------------------------------------------

func TestReadSinglePathManifest_ValidAudio(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "track.mp3")
	os.WriteFile(audioPath, []byte("dummy"), 0644)
	manifest := filepath.Join(dir, "manifest.txt")
	os.WriteFile(manifest, []byte(audioPath+"\n"), 0644)

	got, err := ReadSinglePathManifest(manifest, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != audioPath {
		t.Errorf("got %q, want %q", got, audioPath)
	}
}

func TestReadSinglePathManifest_ZeroLines(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "empty.txt")
	os.WriteFile(manifest, []byte{}, 0644)

	_, err := ReadSinglePathManifest(manifest, dir)
	if err == nil {
		t.Fatal("expected error for empty manifest")
	}
}

func TestReadSinglePathManifest_TwoLines(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "two.txt")
	os.WriteFile(manifest, []byte("path1\npath2\n"), 0644)

	_, err := ReadSinglePathManifest(manifest, dir)
	if err == nil {
		t.Fatal("expected error for manifest with two lines")
	}
}

func TestReadSinglePathManifest_MissingFile(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.txt")
	os.WriteFile(manifest, []byte(filepath.Join(dir, "nonexistent.mp3")+"\n"), 0644)

	_, err := ReadSinglePathManifest(manifest, dir)
	if err == nil {
		t.Fatal("expected error for manifest pointing to nonexistent file")
	}
}

func TestReadSinglePathManifest_OutsideRoot(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "..", "outside.mp3")
	os.WriteFile(outside, []byte("dummy"), 0644)
	manifest := filepath.Join(dir, "manifest.txt")
	os.WriteFile(manifest, []byte(outside+"\n"), 0644)

	_, err := ReadSinglePathManifest(manifest, dir)
	if err == nil {
		t.Fatal("expected error for path outside root")
	}
}

func TestReadSinglePathManifest_CarriageReturnLineEndings(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "track.mp3")
	os.WriteFile(audioPath, []byte("dummy"), 0644)
	manifest := filepath.Join(dir, "manifest.txt")
	os.WriteFile(manifest, []byte(audioPath+"\r\n"), 0644)

	got, err := ReadSinglePathManifest(manifest, dir)
	if err != nil {
		t.Fatalf("unexpected error with CRLF: %v", err)
	}
	if got != audioPath {
		t.Errorf("got %q, want %q", got, audioPath)
	}
}

func TestReadSinglePathManifest_TrailingWhitespace(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "track.mp3")
	os.WriteFile(audioPath, []byte("dummy"), 0644)
	manifest := filepath.Join(dir, "manifest.txt")
	os.WriteFile(manifest, []byte(audioPath+"  \n"), 0644)

	got, err := ReadSinglePathManifest(manifest, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != audioPath {
		t.Errorf("got %q, want %q", got, audioPath)
	}
}

func TestReadSinglePathManifest_NotFound(t *testing.T) {
	_, err := ReadSinglePathManifest("nonexistent-manifest.txt", ".")
	if err == nil {
		t.Fatal("expected error for missing manifest file")
	}
}

// ---------------------------------------------------------------------------
// ParseSoundCloudInfoJSON tests
// ---------------------------------------------------------------------------

func TestParseSoundCloudInfoJSON_TitleUploader(t *testing.T) {
	data := []byte(`{"title":"My Track","uploader":"Some User"}`)
	meta, err := ParseSoundCloudInfoJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Title != "My Track" {
		t.Errorf("Title = %q, want %q", meta.Title, "My Track")
	}
	if meta.Uploader != "Some User" {
		t.Errorf("Uploader = %q, want %q", meta.Uploader, "Some User")
	}
	if meta.Artist != "Some User" {
		t.Errorf("Artist (fallback) = %q, want %q", meta.Artist, "Some User")
	}
	if meta.Album != "" {
		t.Errorf("Album = %q, want empty", meta.Album)
	}
}

func TestParseSoundCloudInfoJSON_AlbumPresent(t *testing.T) {
	data := []byte(`{"title":"Track","uploader":"User","artist":"Real Artist","album":"Real Album"}`)
	meta, err := ParseSoundCloudInfoJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Title != "Track" {
		t.Errorf("Title = %q", meta.Title)
	}
	if meta.Artist != "Real Artist" {
		t.Errorf("Artist = %q, want %q", meta.Artist, "Real Artist")
	}
	if meta.Album != "Real Album" {
		t.Errorf("Album = %q, want %q", meta.Album, "Real Album")
	}
	if meta.Uploader != "User" {
		t.Errorf("Uploader = %q", meta.Uploader)
	}
}

func TestParseSoundCloudInfoJSON_InvalidJSON(t *testing.T) {
	_, err := ParseSoundCloudInfoJSON([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseSoundCloudInfoJSON_EmptyData(t *testing.T) {
	_, err := ParseSoundCloudInfoJSON([]byte{})
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestParseSoundCloudInfoJSON_UploaderAsArtistFallback(t *testing.T) {
	data := []byte(`{"title":"Fallback Track","uploader":"Fallback Uploader"}`)
	meta, err := ParseSoundCloudInfoJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Title != "Fallback Track" {
		t.Errorf("Title = %q, want %q", meta.Title, "Fallback Track")
	}
	if meta.Uploader != "Fallback Uploader" {
		t.Errorf("Uploader = %q, want %q", meta.Uploader, "Fallback Uploader")
	}
	if meta.Artist != "Fallback Uploader" {
		t.Errorf("Artist = %q, want uploader fallback %q", meta.Artist, "Fallback Uploader")
	}
}

func TestParseSoundCloudInfoJSON_AlbumOnlyWhenPresent(t *testing.T) {
	data := []byte(`{"title":"Track","uploader":"User"}`)
	meta, err := ParseSoundCloudInfoJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Album != "" {
		t.Errorf("Album = %q, want empty when absent", meta.Album)
	}

	data2 := []byte(`{"title":"Track","uploader":"User","album":"Real Album"}`)
	meta2, err := ParseSoundCloudInfoJSON(data2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta2.Album != "Real Album" {
		t.Errorf("Album = %q, want %q", meta2.Album, "Real Album")
	}
}

func TestParseSoundCloudInfoJSON_FromFile(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "track.info.json")
	infoData := `{
		"title": "Real Track",
		"uploader": "Real Uploader",
		"artist": "Real Artist",
		"album": "Real Album",
		"duration": 123456
	}`
	if err := os.WriteFile(infoPath, []byte(infoData), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseSoundCloudInfoJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Title != "Real Track" {
		t.Errorf("Title = %q", meta.Title)
	}
	if meta.Artist != "Real Artist" {
		t.Errorf("Artist = %q", meta.Artist)
	}
	if meta.Album != "Real Album" {
		t.Errorf("Album = %q", meta.Album)
	}
	if meta.Uploader != "Real Uploader" {
		t.Errorf("Uploader = %q", meta.Uploader)
	}
}

// ---------------------------------------------------------------------------
// Regression: .info.json larger than audio, manifest-listed audio still wins
// ---------------------------------------------------------------------------

func TestDownloadSoundCloud_ManifestWinsOverLargestFile(t *testing.T) {
	oldRun := runDownloadCmd
	runDownloadCmd = testDownloadRun
	defer func() { runDownloadCmd = oldRun }()

	dir := t.TempDir()

	audio, err := DownloadSoundCloud(context.Background(), "mock-yt-dlp", "https://soundcloud.com/user/track", dir)
	if err != nil {
		t.Fatalf("DownloadSoundCloud failed: %v", err)
	}

	if !strings.HasSuffix(audio.SourcePath, ".mp3") {
		t.Errorf("SourcePath = %q, want .mp3 file from manifest", audio.SourcePath)
	}
	if audio.SoundCloudMetadata.Title != "Test Track" {
		t.Errorf("SoundCloudMetadata.Title = %q, want %q", audio.SoundCloudMetadata.Title, "Test Track")
	}
}

func TestDownloadSoundCloud_InfoJSONSidecar(t *testing.T) {
	oldRun := runDownloadCmd
	runDownloadCmd = testDownloadRun
	defer func() { runDownloadCmd = oldRun }()

	dir := t.TempDir()

	audio, err := DownloadSoundCloud(context.Background(), "mock-yt-dlp", "https://soundcloud.com/user/track", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if audio.SoundCloudInformationPath == "" {
		t.Fatal("SoundCloudInformationPath must not be empty")
	}
	if !strings.HasSuffix(audio.SoundCloudInformationPath, ".info.json") {
		t.Errorf("SoundCloudInformationPath = %q, want .info.json", audio.SoundCloudInformationPath)
	}
	if audio.SoundCloudMetadata.Title != "Test Track" {
		t.Errorf("SoundCloudMetadata.Title = %q, want %q", audio.SoundCloudMetadata.Title, "Test Track")
	}
	if audio.SoundCloudMetadata.Uploader != "Test Uploader" {
		t.Errorf("SoundCloudMetadata.Uploader = %q, want %q", audio.SoundCloudMetadata.Uploader, "Test Uploader")
	}
}
