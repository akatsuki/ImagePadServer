package xposttts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"imagepadserver/internal/xpostmodel"
)

const voicevoxFramesPerSecond = 93.75
const maxTimingDurationSeconds = 3600.0
const maxTimingTotalFrames int64 = 8_100_000 // 24 hours at VOICEVOX's 93.75 Hz timing rate.

type timedTextChunk struct {
	text               string
	startRune, endRune int
}

type timingMora struct {
	Text            string   `json:"text,omitempty"`
	Consonant       *string  `json:"consonant"`
	ConsonantLength *float64 `json:"consonant_length"`
	Vowel           string   `json:"vowel"`
	VowelLength     *float64 `json:"vowel_length"`
	Pitch           *float64 `json:"pitch"`
}

type timingAccentPhrase struct {
	Moras           []timingMora `json:"moras"`
	PauseMora       *timingMora  `json:"pause_mora"`
	IsInterrogative bool         `json:"is_interrogative"`
}

type timedQuery struct {
	raw         map[string]json.RawMessage
	phrases     []timingAccentPhrase
	preFrames   int64
	postFrames  int64
	pauseScale  float64
	pauseLength *float64
	speed       float64
	upspeak     bool
}

// SynthesizeTimed creates one engine synthesis from naturally divided query
// chunks and reports cue positions from the engine's phoneme timing metadata.
func (c *Client) SynthesizeTimed(ctx context.Context, text string, voice xpostmodel.Voice, outPath string) (xpostmodel.Speech, error) {
	if strings.TrimSpace(text) == "" || !hasSpokenText(text) {
		return xpostmodel.Speech{}, nil
	}
	if voice.StyleID < 0 {
		return xpostmodel.Speech{}, errors.New("voice style ID must be non-negative")
	}
	speed := voice.Speed
	if speed == 0 {
		speed = 1
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < .5 || speed > 2 {
		return xpostmodel.Speech{}, errors.New("voice speed must be between 0.5 and 2")
	}
	if strings.TrimSpace(outPath) == "" {
		return xpostmodel.Speech{}, errors.New("output path is required")
	}

	allChunks := splitTimedText(text)
	chunks := make([]timedTextChunk, 0, len(allChunks))
	queries := make([]timedQuery, 0, len(allChunks))
	for _, chunk := range allChunks {
		if !hasSpokenText(chunk.text) {
			continue
		}
		query, err := c.audioQuery(ctx, chunk.text, voice.StyleID)
		if err != nil {
			return xpostmodel.Speech{}, err
		}
		if _, ok := query["accent_phrases"]; !ok {
			// Some older engine-compatible mocks return only query settings. Keep
			// their established synthesis behavior while withholding unknown cues.
			return c.Synthesize(ctx, text, voice, outPath)
		}
		parsed, err := parseTimedQuery(query, speed)
		if err != nil {
			return xpostmodel.Speech{}, err
		}
		chunks = append(chunks, chunk)
		queries = append(queries, parsed)
	}
	if len(chunks) == 0 {
		return xpostmodel.Speech{}, nil
	}
	for i := 0; i+1 < len(queries); i++ {
		if err := addDefaultBoundaryPause(&queries[i]); err != nil {
			return xpostmodel.Speech{}, err
		}
	}

	merged := make(map[string]json.RawMessage, len(queries[0].raw))
	for key, value := range queries[0].raw {
		merged[key] = value
	}
	allPhrases := make([]json.RawMessage, 0)
	for _, query := range queries {
		var phrases []json.RawMessage
		if err := json.Unmarshal(query.raw["accent_phrases"], &phrases); err != nil {
			return xpostmodel.Speech{}, fmt.Errorf("invalid timing accent_phrases: %w", err)
		}
		allPhrases = append(allPhrases, phrases...)
	}
	merged["accent_phrases"], _ = json.Marshal(allPhrases)
	merged["speedScale"], _ = json.Marshal(speed)
	body, err := json.Marshal(merged)
	if err != nil {
		return xpostmodel.Speech{}, err
	}

	synthURL := c.endpoint("/synthesis") + "?speaker=" + strconv.Itoa(voice.StyleID) + "&enable_interrogative_upspeak=" + strconv.FormatBool(queries[0].upspeak)
	synthReq, err := http.NewRequestWithContext(ctx, http.MethodPost, synthURL, bytes.NewReader(body))
	if err != nil {
		return xpostmodel.Speech{}, err
	}
	synthReq.Header.Set("Content-Type", "application/json")
	synthResp, err := c.http.Do(synthReq)
	if err != nil {
		return xpostmodel.Speech{}, fmt.Errorf("synthesize speech: %w", err)
	}
	wav, readErr := readResponse(synthResp, maxWAVBytes)
	synthResp.Body.Close()
	if readErr != nil {
		return xpostmodel.Speech{}, readErr
	}
	info, err := inspectWAV(wav)
	if err != nil {
		return xpostmodel.Speech{}, err
	}
	cues, err := measuredCues(chunks, queries, info.samples, info.sampleRate)
	if err != nil {
		return xpostmodel.Speech{}, err
	}
	if err := writeAtomic(outPath, wav); err != nil {
		return xpostmodel.Speech{}, err
	}
	return xpostmodel.Speech{
		Path: outPath, Duration: float64(info.samples) / float64(info.sampleRate),
		SampleRate: info.sampleRate, Channels: info.channels, Samples: info.samples, Cues: cues,
	}, nil
}

func (c *Client) audioQuery(ctx context.Context, text string, styleID int) (map[string]json.RawMessage, error) {
	queryURL := c.endpoint("/audio_query") + "?speaker=" + strconv.Itoa(styleID) + "&text=" + url.QueryEscape(text)
	queryReq, err := http.NewRequestWithContext(ctx, http.MethodPost, queryURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(queryReq)
	if err != nil {
		return nil, fmt.Errorf("create audio query: %w", err)
	}
	data, readErr := readResponse(resp, maxJSONBytes)
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	var query map[string]json.RawMessage
	if err := json.Unmarshal(data, &query); err != nil || query == nil {
		return nil, fmt.Errorf("decode audio query: invalid JSON object: %v", err)
	}
	return query, nil
}

func splitTimedText(text string) []timedTextChunk {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	var chunks []timedTextChunk
	start := 0
	for i := 0; i < len(runes); i++ {
		if !isTimedBoundary(runes[i], runes, i) {
			continue
		}
		end := i + 1
		for end < len(runes) && isPunctuation(runes[end]) {
			end++
		}
		chunks = append(chunks, timedTextChunk{text: string(runes[start:end]), startRune: start, endRune: end})
		start = end
		i = end - 1
	}
	if start < len(runes) {
		chunks = append(chunks, timedTextChunk{text: string(runes[start:]), startRune: start, endRune: len(runes)})
	}
	return chunks
}

func isTimedBoundary(r rune, text []rune, i int) bool {
	switch r {
	case '。', '！', '？', '!', '?', '、', ',', '，', ';', '；', '\n', '\r':
		return true
	case ':', '：':
		return !(i > 0 && i+1 < len(text) && unicode.IsDigit(text[i-1]) && unicode.IsDigit(text[i+1]))
	case '.':
		return !(i > 0 && i+1 < len(text) && unicode.IsDigit(text[i-1]) && unicode.IsDigit(text[i+1]))
	default:
		return false
	}
}

func isPunctuation(r rune) bool { return unicode.IsPunct(r) }

func addDefaultBoundaryPause(query *timedQuery) error {
	if len(query.phrases) == 0 {
		return nil
	}
	last := len(query.phrases) - 1
	if query.phrases[last].PauseMora != nil {
		return nil
	}
	length, pitch := .3, 0.0
	pause := timingMora{Text: "、", Vowel: "pau", VowelLength: &length, Pitch: &pitch}
	query.phrases[last].PauseMora = &pause
	var rawPhrases []map[string]json.RawMessage
	if err := json.Unmarshal(query.raw["accent_phrases"], &rawPhrases); err != nil || len(rawPhrases) != len(query.phrases) {
		return fmt.Errorf("invalid timing accent_phrases while adding chunk pause: %v", err)
	}
	pauseJSON, err := json.Marshal(pause)
	if err != nil {
		return err
	}
	rawPhrases[last]["pause_mora"] = pauseJSON
	query.raw["accent_phrases"], err = json.Marshal(rawPhrases)
	if err != nil {
		return fmt.Errorf("encode timing accent_phrases: %w", err)
	}
	return nil
}

func hasSpokenText(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func parseTimedQuery(raw map[string]json.RawMessage, speed float64) (timedQuery, error) {
	query := timedQuery{raw: raw, speed: speed, upspeak: true}
	if err := json.Unmarshal(raw["accent_phrases"], &query.phrases); err != nil || query.phrases == nil {
		return query, fmt.Errorf("invalid timing accent_phrases: %v", err)
	}
	pre, err := requiredFinite(raw, "prePhonemeLength")
	if err != nil || pre < 0 || pre/speed > maxTimingDurationSeconds {
		return query, errors.New("invalid timing prePhonemeLength")
	}
	post, err := requiredFinite(raw, "postPhonemeLength")
	if err != nil || post < 0 || post/speed > maxTimingDurationSeconds {
		return query, errors.New("invalid timing postPhonemeLength")
	}
	querySpeed, err := requiredFinite(raw, "speedScale")
	if err != nil || querySpeed <= 0 {
		return query, errors.New("invalid timing speedScale")
	}
	query.preFrames = durationFrames(pre / speed)
	query.postFrames = durationFrames(post / speed)
	query.pauseScale, err = requiredFinite(raw, "pauseLengthScale")
	if err != nil || query.pauseScale < 0 {
		return query, errors.New("invalid timing pauseLengthScale")
	}
	if pauseRaw, ok := raw["pauseLength"]; ok && string(bytes.TrimSpace(pauseRaw)) != "null" {
		pause, e := finiteJSONNumber(pauseRaw)
		if e != nil || pause < 0 || pause > maxTimingDurationSeconds {
			return query, errors.New("invalid timing pauseLength")
		}
		query.pauseLength = &pause
	}
	if enabledRaw, ok := raw["enable_interrogative_upspeak"]; ok {
		if err := json.Unmarshal(enabledRaw, &query.upspeak); err != nil {
			return query, errors.New("invalid timing enable_interrogative_upspeak")
		}
	}
	for _, phrase := range query.phrases {
		for _, mora := range phrase.Moras {
			if _, err := timingMoraFrames(mora, query); err != nil {
				return query, err
			}
		}
		if phrase.PauseMora != nil {
			if _, err := timingMoraFrames(*phrase.PauseMora, query); err != nil {
				return query, err
			}
		}
	}
	return query, nil
}

func requiredFinite(raw map[string]json.RawMessage, key string) (float64, error) {
	value, ok := raw[key]
	if !ok {
		return 0, errors.New("missing " + key)
	}
	return finiteJSONNumber(value)
}

func finiteJSONNumber(raw json.RawMessage) (float64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, errors.New("not a finite number")
	}
	var value float64
	if err := json.Unmarshal(trimmed, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("not a finite number")
	}
	return value, nil
}

func durationFrames(seconds float64) int64 {
	return int64(math.RoundToEven(seconds * voicevoxFramesPerSecond))
}

func timingMoraFrames(mora timingMora, query timedQuery) (int64, error) {
	if mora.Vowel == "" || mora.VowelLength == nil || mora.Pitch == nil || math.IsNaN(*mora.Pitch) || math.IsInf(*mora.Pitch, 0) || *mora.VowelLength < 0 || math.IsNaN(*mora.VowelLength) || math.IsInf(*mora.VowelLength, 0) {
		return 0, errors.New("invalid timing mora fields")
	}
	vowelLength := *mora.VowelLength
	if mora.Vowel == "pau" {
		if query.pauseLength != nil {
			vowelLength = *query.pauseLength
		}
		vowelLength *= query.pauseScale
	}
	vowelSeconds := vowelLength / query.speed
	if vowelLength > maxTimingDurationSeconds || math.IsNaN(vowelSeconds) || math.IsInf(vowelSeconds, 0) || vowelSeconds > maxTimingDurationSeconds {
		return 0, errors.New("timing vowel duration exceeds limit")
	}
	frames := durationFrames(vowelSeconds)
	if mora.Consonant != nil {
		if *mora.Consonant == "" || mora.ConsonantLength == nil || *mora.ConsonantLength < 0 || *mora.ConsonantLength > maxTimingDurationSeconds || math.IsNaN(*mora.ConsonantLength) || math.IsInf(*mora.ConsonantLength, 0) {
			return 0, errors.New("invalid timing consonant fields")
		}
		consonantSeconds := *mora.ConsonantLength / query.speed
		if math.IsNaN(consonantSeconds) || math.IsInf(consonantSeconds, 0) || consonantSeconds > maxTimingDurationSeconds {
			return 0, errors.New("timing consonant duration exceeds limit")
		}
		frames += durationFrames(consonantSeconds)
	}
	return frames, nil
}

func measuredCues(chunks []timedTextChunk, queries []timedQuery, samples int64, sampleRate int) ([]xpostmodel.SpeechCue, error) {
	if len(chunks) != len(queries) || sampleRate <= 0 || samples < 0 {
		return nil, errors.New("invalid timing output dimensions")
	}
	query := queries[0]
	totalFrames := query.preFrames + query.postFrames
	chunkFrames := make([]int64, len(queries))
	spokenFrames := make([]int64, len(queries))
	for i, part := range queries {
		var lastPause int64
		for phraseIndex, phrase := range part.phrases {
			for _, mora := range phrase.Moras {
				frames, err := timingMoraFrames(mora, query)
				if err != nil {
					return nil, err
				}
				chunkFrames[i] += frames
			}
			if query.upspeak && phrase.IsInterrogative && len(phrase.Moras) > 0 && phrase.Moras[len(phrase.Moras)-1].Pitch != nil && *phrase.Moras[len(phrase.Moras)-1].Pitch > 0 {
				chunkFrames[i] += durationFrames(.15 / query.speed)
			}
			if phrase.PauseMora != nil {
				frames, err := timingMoraFrames(*phrase.PauseMora, query)
				if err != nil {
					return nil, err
				}
				chunkFrames[i] += frames
				if phraseIndex == len(part.phrases)-1 {
					lastPause = frames
				}
			}
		}
		spokenFrames[i] = chunkFrames[i] - lastPause
		if chunkFrames[i] < 0 || totalFrames > maxTimingTotalFrames-chunkFrames[i] {
			return nil, errors.New("predicted timing duration exceeds 24 hours")
		}
		totalFrames += chunkFrames[i]
	}
	if totalFrames == 0 || samples == 0 {
		return nil, nil
	}
	predictedSeconds := float64(totalFrames) / voicevoxFramesPerSecond
	actualSeconds := float64(samples) / float64(sampleRate)
	if predictedSeconds <= 0 || math.IsNaN(actualSeconds) || math.IsInf(actualSeconds, 0) {
		return nil, errors.New("invalid timing duration")
	}
	scale := actualSeconds / predictedSeconds
	positionFrames := query.preFrames
	cues := make([]xpostmodel.SpeechCue, 0, len(chunks))
	for i, chunk := range chunks {
		if spokenFrames[i] > 0 {
			start := float64(positionFrames) / voicevoxFramesPerSecond * scale
			end := float64(positionFrames+spokenFrames[i]) / voicevoxFramesPerSecond * scale
			cues = append(cues, xpostmodel.SpeechCue{StartRune: chunk.startRune, EndRune: chunk.endRune, StartSeconds: start, EndSeconds: end})
		}
		positionFrames += chunkFrames[i]
	}
	return cues, nil
}
