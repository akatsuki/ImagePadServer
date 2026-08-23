package airplay

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeZipPathRejectsTraversalAndAbsoluteNames(t *testing.T) {
	for _, name := range []string{
		"../outside.txt",
		"nested/../../outside.txt",
		`..\outside.txt`,
		`C:\outside.txt`,
		`\outside.txt`,
		"/outside.txt",
		"nested/..",
		"nested/\x00file.txt",
	} {
		if _, err := safeZipPath(name); err == nil {
			t.Errorf("safeZipPath(%q) accepted an unsafe name", name)
		}
	}
	for _, test := range []struct{ name, want string }{
		{name: `nested\file.dll`, want: "nested/file.dll"},
		{name: "nested//file.dll", want: "nested/file.dll"},
	} {
		got, err := safeZipPath(test.name)
		if err != nil || got != test.want {
			t.Errorf("safeZipPath(%q) = %q, %v; want %q", test.name, got, err, test.want)
		}
	}
}

func TestExtractUxPlayArchiveRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "malicious.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("must not be extracted")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "bundle")
	if _, err := extractUxPlayArchive(archivePath, destination); err == nil {
		t.Fatal("extractUxPlayArchive accepted a traversal entry")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(destination), "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive traversal created an outside file: %v", err)
	}
}

func TestExtractUxPlayArchiveRejectsSymlink(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "symlink.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(os.ModeSymlink | 0600)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("target")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := extractUxPlayArchive(archivePath, filepath.Join(t.TempDir(), "bundle")); err == nil {
		t.Fatal("extractUxPlayArchive accepted a symlink entry")
	}
}
