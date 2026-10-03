package xposttts

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"imagepadserver/internal/xpostmodel"
)

var (
	urlPattern     = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s<>"'{}|\\^\x60]+|(?:[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?\.)+[\p{L}]{2,}(?::[0-9]{1,5})?(?:/[^^\s<>"'{}|\\^\x60]*)?`)
	mentionPattern = regexp.MustCompile(`@[\p{L}\p{N}_]{1,30}`)
)

// BuildSpeechText removes URL and mention entities using their UTF-16 ranges,
// then removes recognizable URLs and handles that may not have entity metadata.
func BuildSpeechText(post xpostmodel.Post) (string, error) {
	text := post.RawText
	if text == "" {
		text = post.Text
	}
	units := utf16UnitOffsets(text)
	var spans []span
	for _, entity := range post.Entities {
		kind := strings.ToLower(strings.ReplaceAll(entity.Kind, "-", "_"))
		if kind != "url" && kind != "mention" && kind != "user_mention" && kind != "user_mentions" {
			continue
		}
		if entity.Start < 0 || entity.End < entity.Start || entity.End >= len(units) || units[entity.Start] < 0 || units[entity.End] < 0 {
			return "", fmt.Errorf("invalid UTF-16 entity range %d:%d for text length %d", entity.Start, entity.End, len(units)-1)
		}
		spans = append(spans, span{units[entity.Start], units[entity.End]})
	}
	text = removeSpans(text, spans)
	text = removeMatchedTokens(text, urlPattern, true)
	text = removeMatchedTokens(text, mentionPattern, false)
	return strings.Join(strings.Fields(text), " "), nil
}

type span struct{ start, end int }

func utf16UnitOffsets(s string) []int {
	result := []int{0}
	units := 0
	for byteOffset, r := range s {
		size := utf8RuneSize(s[byteOffset:])
		if r > 0xffff {
			units++
			result = append(result, -1) // No byte offset exists between a surrogate pair.
			units++
		} else {
			units++
		}
		result = append(result, byteOffset+size)
	}
	return result
}

func removeMatchedTokens(text string, pattern *regexp.Regexp, url bool) string {
	matches := pattern.FindAllStringIndex(text, -1)
	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if start > 0 {
			prev := []rune(text[:start])
			p := prev[len(prev)-1]
			explicitURL := url && (strings.HasPrefix(strings.ToLower(text[start:]), "http://") || strings.HasPrefix(strings.ToLower(text[start:]), "https://") || strings.HasPrefix(strings.ToLower(text[start:]), "www."))
			if !explicitURL && (p < 128 && (unicode.IsLetter(p) || unicode.IsDigit(p)) || p == '_' || url && p == '@' || !url && p == '.') {
				continue
			}
		}
		if url {
			for end > start && strings.ContainsRune(".,!?;:)]}", rune(text[end-1])) {
				end--
			}
			if end == start {
				continue
			}
		}
		b.WriteString(text[last:start])
		b.WriteByte(' ')
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

func utf8RuneSize(s string) int {
	_, size := utf8.DecodeRuneInString(s)
	return size
}

func removeSpans(text string, spans []span) string {
	if len(spans) == 0 {
		return text
	}
	// Merge ranges while preserving the source order.
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].start < spans[j-1].start; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	var b strings.Builder
	last := 0
	for _, s := range spans {
		if s.start < last {
			if s.end <= last {
				continue
			}
			s.start = last
		}
		b.WriteString(text[last:s.start])
		b.WriteByte(' ')
		last = s.end
	}
	b.WriteString(text[last:])
	return b.String()
}
