package video

import (
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/encoding/japanese"
)

// NormalizeEmbeddedTag normalizes a single embedded metadata string.
//
// It implements the strict acceptance algorithm from spec section 4.8:
//  1. Trim leading and trailing Unicode whitespace and NUL characters.
//  2. If the value contains no C1 control characters (U+0080..U+009F),
//     has no replacement character U+FFFD, and at least 90% of
//     non-whitespace characters are printable, return it unchanged.
//  3. Otherwise, attempt legacy Japanese (CP932) repair only when
//     every code point is in U+0000..U+00FF.
//  4. Convert those code points back to bytes via ISO-8859-1 mapping.
//  5. Decode the bytes as strict Windows-31J/CP932.
//  6. Accept the repaired candidate only when it contains no U+FFFD,
//     has fewer control characters than the original, and at least 90%
//     of its non-whitespace characters are printable.
//  7. If the repaired candidate is not strictly better, treat the tag
//     as empty.
func NormalizeEmbeddedTag(raw string) string {
	// Step 1: trim Unicode whitespace and NUL
	s := strings.TrimFunc(raw, func(r rune) bool {
		return r == 0 || unicode.IsSpace(r)
	})
	if s == "" {
		return ""
	}

	// Analyse original string
	hasC1 := false
	hasFFFD := false
	allIn00FF := true
	printCount := 0
	nonSpaceCount := 0
	origControlCount := 0

	for _, r := range s {
		if r >= 0x0080 && r <= 0x009F {
			hasC1 = true
			origControlCount++
		}
		if r == 0xFFFD {
			hasFFFD = true
		}
		if !unicode.IsSpace(r) {
			nonSpaceCount++
			if unicode.IsPrint(r) {
				printCount++
			}
		}
		if r > 0x00FF {
			allIn00FF = false
		}
		if r < 0x20 || (r >= 0x0080 && r <= 0x009F) {
			origControlCount++
		}
	}

	// Step 2: check if value is clean
	clean := !hasC1 && !hasFFFD
	if nonSpaceCount > 0 {
		clean = clean && (printCount*10 >= nonSpaceCount*9)
	}
	if clean {
		return s
	}

	// Step 3: attempt CP932 repair only if all code points are in U+0000..U+00FF
	if !allIn00FF {
		return ""
	}

	// Step 4: convert code points back to bytes via ISO-8859-1 mapping
	b := make([]byte, 0, len(s))
	for _, r := range s {
		b = append(b, byte(r))
	}

	// Step 5: decode as strict CP932 (Windows-31J)
	decoder := japanese.ShiftJIS.NewDecoder()
	decoded, err := decoder.Bytes(b)
	if err != nil {
		return ""
	}

	decodedStr := string(decoded)

	// Step 6: check if repaired candidate is strictly better
	repFFFD := false
	repPrint := 0
	repNonSpace := 0
	repControl := 0

	for _, r := range decodedStr {
		if r == 0xFFFD {
			repFFFD = true
		}
		if !unicode.IsSpace(r) {
			repNonSpace++
			if unicode.IsPrint(r) {
				repPrint++
			}
		}
		if r < 0x20 || (r >= 0x0080 && r <= 0x009F) {
			repControl++
		}
	}

	better := !repFFFD && repControl < origControlCount
	if repNonSpace > 0 {
		better = better && (repPrint*10 >= repNonSpace*9)
	}

	if better {
		return decodedStr
	}

	// Step 7: not strictly better — treat as empty
	return ""
}

// ResolveAudioMetadata resolves title, artist, and album according to
// the source-specific precedence rules in spec section 4.4.
//
// All embedded string fields are normalized with NormalizeEmbeddedTag
// before comparison. SoundCloud metadata is assumed to be clean UTF-8.
//
// For SourceSoundCloud:
//  1. Non-empty embedded audio tags win.
//  2. SoundCloud metadata fills remaining empty fields.
//  3. Artist falls back to Uploader.
//  4. Title falls back to sourceName without its final extension.
//
// For SourceLocalAudio and SourceRemoteAudio:
//  1. Only embedded audio tags are used.
//  2. Title falls back to sourceName without its final extension.
//  3. Artist and album remain empty if not set.
func ResolveAudioMetadata(kind SourceKind, sourceName string, embedded, soundCloud AudioMetadata) AudioMetadata {
	// Normalize embedded fields
	norm := AudioMetadata{
		Title:  NormalizeEmbeddedTag(embedded.Title),
		Artist: NormalizeEmbeddedTag(embedded.Artist),
		Album:  NormalizeEmbeddedTag(embedded.Album),
	}

	result := AudioMetadata{}

	switch kind {
	case SourceSoundCloud, SourceMusic:
		result.Title = norm.Title
		result.Artist = norm.Artist
		result.Album = norm.Album

		if result.Title == "" {
			result.Title = soundCloud.Title
		}
		if result.Artist == "" {
			result.Artist = soundCloud.Artist
		}
		if result.Album == "" {
			result.Album = soundCloud.Album
		}
		if result.Artist == "" {
			result.Artist = soundCloud.Uploader
		}
		if result.Title == "" {
			result.Title = stripExt(sourceName)
		}

	default: // SourceLocalAudio, SourceRemoteAudio
		result.Title = norm.Title
		result.Artist = norm.Artist
		result.Album = norm.Album

		if result.Title == "" {
			result.Title = stripExt(sourceName)
		}
	}

	return result
}

// stripExt returns the filename without its final extension.
func stripExt(name string) string {
	ext := filepath.Ext(name)
	if ext == "" {
		return name
	}
	return strings.TrimSuffix(name, ext)
}
