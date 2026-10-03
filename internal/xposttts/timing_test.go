package xposttts

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"imagepadserver/internal/xpostmodel"
)

func TestSynthesizeTimedMergesNaturalChunksAndMapsMeasuredCues(t *testing.T) {
	wav := testWAV(16000, 1, 16000)
	var queryTexts []string
	var synthesisCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/audio_query":
			text := r.URL.Query().Get("text")
			queryTexts = append(queryTexts, text)
			if text == "😀単語です。" {
				fmt.Fprint(w, `{"accent_phrases":[{"moras":[{"text":"a","consonant":"k","consonant_length":0.07,"vowel":"a","vowel_length":0.21,"pitch":5}],"pause_mora":{"text":"、","consonant":null,"consonant_length":null,"vowel":"pau","vowel_length":0.12,"pitch":0},"is_interrogative":false}],"speedScale":1,"prePhonemeLength":0.1,"postPhonemeLength":0.2,"pauseLength":0.08,"pauseLengthScale":1.5,"enable_interrogative_upspeak":true}`)
			} else {
				fmt.Fprint(w, `{"accent_phrases":[{"moras":[{"text":"a","consonant":null,"consonant_length":null,"vowel":"a","vowel_length":0.42,"pitch":5}],"pause_mora":null,"is_interrogative":true}],"speedScale":1,"prePhonemeLength":0.1,"postPhonemeLength":0.2,"pauseLength":0.08,"pauseLengthScale":1.5,"enable_interrogative_upspeak":true}`)
			}
		case "/synthesis":
			synthesisCalls++
			if r.URL.Query().Get("enable_interrogative_upspeak") != "true" {
				t.Errorf("synthesis upspeak option = %q", r.URL.Query().Get("enable_interrogative_upspeak"))
			}
			var q map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Errorf("decode synthesis query: %v", err)
			}
			var speed float64
			_ = json.Unmarshal(q["speedScale"], &speed)
			var phrases []json.RawMessage
			_ = json.Unmarshal(q["accent_phrases"], &phrases)
			if speed != 1.25 || len(phrases) != 2 {
				t.Errorf("merged query speed=%v phrases=%d", speed, len(phrases))
			}
			w.Write(wav)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	text := "😀単語です。 次かな？"
	speech, err := c.SynthesizeTimed(context.Background(), text, xpostmodel.Voice{StyleID: 4, Speed: 1.25}, filepath.Join(t.TempDir(), "timed.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(queryTexts, []string{"😀単語です。", " 次かな？"}) {
		t.Fatalf("query texts = %#v", queryTexts)
	}
	if synthesisCalls != 1 {
		t.Fatalf("synthesis calls = %d", synthesisCalls)
	}
	if len(speech.Cues) != 2 {
		t.Fatalf("cues = %#v", speech.Cues)
	}
	wantRuneBounds := [][2]int{{0, 6}, {6, 11}}
	for i, cue := range speech.Cues {
		if cue.StartRune != wantRuneBounds[i][0] || cue.EndRune != wantRuneBounds[i][1] {
			t.Errorf("cue %d rune bounds = [%d,%d)", i, cue.StartRune, cue.EndRune)
		}
		if cue.StartSeconds < 0 || cue.EndSeconds <= cue.StartSeconds || cue.EndSeconds > speech.Duration {
			t.Errorf("cue %d times = [%f,%f), speech=%f", i, cue.StartSeconds, cue.EndSeconds, speech.Duration)
		}
	}
	if !(speech.Cues[0].EndSeconds < speech.Cues[1].StartSeconds) {
		t.Fatalf("pre/post silence and pause not represented: %#v", speech.Cues)
	}
	firstDuration := speech.Cues[0].EndSeconds - speech.Cues[0].StartSeconds
	secondDuration := speech.Cues[1].EndSeconds - speech.Cues[1].StartSeconds
	if secondDuration < firstDuration*1.7 {
		t.Fatalf("unequal phoneme timing or interrogative upspeak lost: first=%f second=%f", firstDuration, secondDuration)
	}
}

func TestSynthesizeTimedWhitespaceAndEmojiOnlyDoNotUseNetwork(t *testing.T) {
	c, _ := NewClient("http://127.0.0.1:1")
	for _, text := range []string{" \n\t", "😀✨"} {
		got, err := c.SynthesizeTimed(context.Background(), text, xpostmodel.Voice{}, filepath.Join(t.TempDir(), "none.wav"))
		if err != nil || !reflect.DeepEqual(got, xpostmodel.Speech{}) {
			t.Fatalf("SynthesizeTimed(%q) = %#v, %v", text, got, err)
		}
	}
}

func TestSynthesizeTimedRejectsInvalidRealTiming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio_query" {
			fmt.Fprint(w, `{"accent_phrases":[{"moras":[{"vowel":"a","vowel_length":-1,"pitch":5}]}],"speedScale":1,"prePhonemeLength":0.1,"postPhonemeLength":0.1,"pauseLengthScale":1}`)
			return
		}
		t.Error("synthesis must not run for invalid timing")
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	if _, err := c.SynthesizeTimed(context.Background(), "invalid", xpostmodel.Voice{StyleID: 1}, filepath.Join(t.TempDir(), "bad.wav")); err == nil || !strings.Contains(strings.ToLower(err.Error()), "timing") {
		t.Fatalf("error = %v", err)
	}
}

func TestSynthesizeTimedFallsBackForLegacyQueryWithoutAccentPhrases(t *testing.T) {
	wav := testWAV(16000, 1, 800)
	var synthCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/audio_query":
			fmt.Fprint(w, `{"speedScale":1}`)
		case "/synthesis":
			synthCalls++
			w.Write(wav)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	speech, err := c.SynthesizeTimed(context.Background(), "legacy", xpostmodel.Voice{StyleID: 2}, filepath.Join(t.TempDir(), "legacy.wav"))
	if err != nil || speech.Duration != .05 || len(speech.Cues) != 0 || synthCalls != 1 {
		t.Fatalf("legacy fallback = %#v, synth calls=%d, err=%v", speech, synthCalls, err)
	}
}

func TestSplitTimedTextPreservesExactNaturalBoundaries(t *testing.T) {
	text := "長いcompound語は分けない、 次の文。😀！？"
	chunks := splitTimedText(text)
	var joined strings.Builder
	for _, chunk := range chunks {
		joined.WriteString(chunk.text)
	}
	if joined.String() != text {
		t.Fatalf("chunks lost or duplicated text: %#v => %q", chunks, joined.String())
	}
	if len(chunks) != 3 || chunks[0].text != "長いcompound語は分けない、" || chunks[1].text != " 次の文。" || chunks[2].text != "😀！？" {
		t.Fatalf("chunks = %#v", chunks)
	}
	for _, chunk := range chunks {
		if chunk.startRune < 0 || chunk.endRune <= chunk.startRune {
			t.Fatalf("invalid chunk rune bounds: %#v", chunk)
		}
	}
}

func TestSplitTimedTextKeepsNumericClockTimeTogether(t *testing.T) {
	text := "配信は18:00に始まる。"
	chunks := splitTimedText(text)
	if len(chunks) != 1 || chunks[0].text != text {
		t.Fatalf("clock time split into %#v", chunks)
	}
}

func TestTimedQueryDefaultsInterrogativeUpspeakToEngineDefault(t *testing.T) {
	raw := map[string]json.RawMessage{
		"accent_phrases":   []byte(`[{"moras":[{"vowel":"a","vowel_length":0.2,"pitch":5}],"is_interrogative":true}]`),
		"prePhonemeLength": []byte(`0`), "postPhonemeLength": []byte(`0.2`), "pauseLengthScale": []byte(`1`), "speedScale": []byte(`1`),
	}
	query, err := parseTimedQuery(raw, 1)
	if err != nil {
		t.Fatal(err)
	}
	cues, err := measuredCues([]timedTextChunk{{text: "かな？", startRune: 0, endRune: 3}}, []timedQuery{query}, 10000, 16000)
	if err != nil || len(cues) != 1 {
		t.Fatalf("measured cues = %#v, %v", cues, err)
	}
	wantFrames := durationFrames(.2) + durationFrames(.15)
	wantSeconds := float64(wantFrames) / float64(wantFrames+durationFrames(.2)) * (float64(10000) / 16000)
	gotSeconds := cues[0].EndSeconds - cues[0].StartSeconds
	if math.Abs(gotSeconds-wantSeconds) > .000001 {
		t.Fatalf("default upspeak duration = %f, want %f", gotSeconds, wantSeconds)
	}
}

func TestSynthesizeTimedAddsPauseWhenMergedChunkHasNoFinalPause(t *testing.T) {
	wav := testWAV(16000, 1, 16000)
	var gotMergedPause json.RawMessage
	var gotUpspeak string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/audio_query":
			fmt.Fprint(w, `{"accent_phrases":[{"moras":[{"vowel":"a","vowel_length":0.2,"pitch":5}],"pause_mora":null,"is_interrogative":false}],"speedScale":1,"prePhonemeLength":0,"postPhonemeLength":0,"pauseLengthScale":1}`)
		case "/synthesis":
			gotUpspeak = r.URL.Query().Get("enable_interrogative_upspeak")
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode synthesis query: %v", err)
			}
			var phrases []struct {
				PauseMora json.RawMessage `json:"pause_mora"`
			}
			_ = json.Unmarshal(body["accent_phrases"], &phrases)
			if len(phrases) > 0 {
				gotMergedPause = phrases[0].PauseMora
			}
			w.Write(wav)
		}
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	speech, err := c.SynthesizeTimed(context.Background(), "一文。 次？", xpostmodel.Voice{StyleID: 3}, filepath.Join(t.TempDir(), "pause.wav"))
	if err != nil {
		t.Fatal(err)
	}
	var pause timingMora
	if gotUpspeak != "true" {
		t.Errorf("default upspeak query = %q", gotUpspeak)
	}
	if err := json.Unmarshal(gotMergedPause, &pause); err != nil || pause.Vowel != "pau" || pause.VowelLength == nil || *pause.VowelLength != .3 || pause.Pitch == nil || *pause.Pitch != 0 || pause.Consonant != nil {
		t.Fatalf("merged boundary pause = %s, parsed=%#v, err=%v", gotMergedPause, pause, err)
	}
	if len(speech.Cues) != 2 || speech.Cues[1].StartSeconds-speech.Cues[0].EndSeconds < .25 {
		t.Fatalf("default chunk pause missing from cue timeline: %#v", speech.Cues)
	}
}

func TestSynthesizeTimedExplicitlyDisablesUpspeakForEngine(t *testing.T) {
	wav := testWAV(16000, 1, 8000)
	var gotUpspeak string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio_query" {
			fmt.Fprint(w, `{"accent_phrases":[{"moras":[{"vowel":"a","vowel_length":0.2,"pitch":5}],"is_interrogative":true}],"speedScale":1,"prePhonemeLength":0,"postPhonemeLength":0,"pauseLengthScale":1,"enable_interrogative_upspeak":false}`)
			return
		}
		gotUpspeak = r.URL.Query().Get("enable_interrogative_upspeak")
		w.Write(wav)
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	if _, err := c.SynthesizeTimed(context.Background(), "かな？", xpostmodel.Voice{StyleID: 2}, filepath.Join(t.TempDir(), "no-upspeak.wav")); err != nil {
		t.Fatal(err)
	}
	if gotUpspeak != "false" {
		t.Fatalf("explicit upspeak query = %q", gotUpspeak)
	}
}

func TestDurationFramesUsesRoundToEven(t *testing.T) {
	if got := durationFrames(0.5 / voicevoxFramesPerSecond); got != 0 {
		t.Fatalf("half frame rounded to %d, want even 0", got)
	}
	if got := durationFrames(1.5 / voicevoxFramesPerSecond); got != 2 {
		t.Fatalf("one and a half frames rounded to %d, want even 2", got)
	}
}

func TestMeasuredCuesHandlesZeroDurationWithoutDivision(t *testing.T) {
	chunk := timedTextChunk{text: "音", startRune: 0, endRune: 1}
	raw := map[string]json.RawMessage{
		"accent_phrases":   []byte(`[{"moras":[{"vowel":"a","vowel_length":0,"pitch":5}],"is_interrogative":false}]`),
		"prePhonemeLength": []byte(`0`), "postPhonemeLength": []byte(`0`), "pauseLengthScale": []byte(`1`), "speedScale": []byte(`1`),
	}
	query, err := parseTimedQuery(raw, 1)
	if err != nil {
		t.Fatal(err)
	}
	cues, err := measuredCues([]timedTextChunk{chunk}, []timedQuery{query}, 0, 16000)
	if err != nil || len(cues) != 0 {
		t.Fatalf("zero duration cues = %#v, %v", cues, err)
	}
}

func TestParseTimedQueryRejectsNullRequiredNumbersButAllowsNullPauseLength(t *testing.T) {
	base := map[string]json.RawMessage{
		"accent_phrases": []byte(`[]`), "prePhonemeLength": []byte(`0.1`), "postPhonemeLength": []byte(`0.1`),
		"speedScale": []byte(`1`), "pauseLengthScale": []byte(`1`),
	}
	for _, key := range []string{"prePhonemeLength", "postPhonemeLength", "speedScale", "pauseLengthScale"} {
		t.Run(key, func(t *testing.T) {
			raw := make(map[string]json.RawMessage, len(base))
			for name, value := range base {
				raw[name] = value
			}
			raw[key] = []byte(`null`)
			if _, err := parseTimedQuery(raw, 1); err == nil {
				t.Fatalf("null %s accepted", key)
			}
		})
	}
	base["pauseLength"] = []byte(`null`)
	if _, err := parseTimedQuery(base, 1); err != nil {
		t.Fatalf("nullable pauseLength rejected: %v", err)
	}
}

func TestParseTimedQueryRejectsOversizedIndividualDurations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]json.RawMessage)
	}{
		{name: "pre", edit: func(raw map[string]json.RawMessage) { raw["prePhonemeLength"] = []byte(`3600.1`) }},
		{name: "post", edit: func(raw map[string]json.RawMessage) { raw["postPhonemeLength"] = []byte(`1e300`) }},
		{name: "vowel", edit: func(raw map[string]json.RawMessage) {
			raw["accent_phrases"] = []byte(`[{"moras":[{"vowel":"a","vowel_length":1e300,"pitch":5}]}]`)
		}},
		{name: "consonant", edit: func(raw map[string]json.RawMessage) {
			raw["accent_phrases"] = []byte(`[{"moras":[{"consonant":"k","consonant_length":1e300,"vowel":"a","vowel_length":0.1,"pitch":5}]}]`)
		}},
		{name: "pause", edit: func(raw map[string]json.RawMessage) {
			raw["pauseLength"] = []byte(`3600.1`)
			raw["accent_phrases"] = []byte(`[{"moras":[{"vowel":"pau","vowel_length":0.1,"pitch":0}]}]`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]json.RawMessage{
				"accent_phrases": []byte(`[]`), "prePhonemeLength": []byte(`0.1`), "postPhonemeLength": []byte(`0.1`),
				"speedScale": []byte(`1`), "pauseLengthScale": []byte(`1`),
			}
			tc.edit(raw)
			if _, err := parseTimedQuery(raw, 1); err == nil {
				t.Fatal("oversized duration accepted")
			}
		})
	}
}

func TestMeasuredCuesRejectsPredictedTimelineOver24Hours(t *testing.T) {
	length, pitch := 3600.0, 5.0
	moras := make([]timingMora, 25)
	for i := range moras {
		moras[i] = timingMora{Vowel: "a", VowelLength: &length, Pitch: &pitch}
	}
	query := timedQuery{speed: 1, upspeak: true, phrases: []timingAccentPhrase{{Moras: moras}}}
	chunks := []timedTextChunk{{text: "x", startRune: 0, endRune: 1}}
	if _, err := measuredCues(chunks, []timedQuery{query}, 16000, 16000); err == nil {
		t.Fatal("timeline above 24 hours accepted")
	}
}

func TestSynthesizeTimedQueryFailureAndCancellation(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "engine unavailable", http.StatusBadGateway)
	}))
	c, _ := NewClient(fail.URL)
	if _, err := c.SynthesizeTimed(context.Background(), "話す", xpostmodel.Voice{StyleID: 1}, filepath.Join(t.TempDir(), "fail.wav")); err == nil {
		t.Fatal("expected audio query failure")
	}
	fail.Close()

	waiting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer waiting.Close()
	c, _ = NewClient(waiting.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.SynthesizeTimed(ctx, "話す", xpostmodel.Voice{StyleID: 1}, filepath.Join(t.TempDir(), "cancel.wav")); err == nil {
		t.Fatal("expected canceled query error")
	}
}
