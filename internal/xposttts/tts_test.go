package xposttts

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/xpostmodel"
)

func TestBuildSpeechTextRemovesEntitiesByUTF16RangesAndFallbacks(t *testing.T) {
	post := xpostmodel.Post{
		RawText: "😀こんにちは @alice 123 https://example.com/path @bob! www.test.jp",
		Entities: []xpostmodel.Entity{
			{Start: 8, End: 14, Kind: "mention"}, // UTF-16 positions after emoji and Japanese text
			{Start: 19, End: 43, Kind: "url"},
		},
	}
	got, err := BuildSpeechText(post)
	if err != nil {
		t.Fatal(err)
	}
	if got != "😀こんにちは 123 !" {
		t.Fatalf("BuildSpeechText() = %q", got)
	}
}

func TestBuildSpeechTextRejectsInvalidEntityRange(t *testing.T) {
	_, err := BuildSpeechText(xpostmodel.Post{RawText: "text", Entities: []xpostmodel.Entity{{Start: 9, End: 12, Kind: "url"}}})
	if err == nil {
		t.Fatal("expected invalid range error")
	}
}

func TestBuildSpeechTextKeepsURLPunctuationNumbersAndEmail(t *testing.T) {
	got, err := BuildSpeechText(xpostmodel.Post{RawText: "Read https://x.test/path! @one and 42 3.14 mail me@example.com @second"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Read ! and 42 3.14 mail me@example.com" {
		t.Fatalf("BuildSpeechText() = %q", got)
	}
}

func TestBuildSpeechTextRejectsRangeInsideSurrogatePair(t *testing.T) {
	_, err := BuildSpeechText(xpostmodel.Post{RawText: "😀 hi", Entities: []xpostmodel.Entity{{Start: 1, End: 2, Kind: "url"}}})
	if err == nil {
		t.Fatal("expected range inside surrogate pair to fail")
	}
}

func TestSpeakersReadsUUIDAndStyles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/speakers" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `[{"speaker_uuid":"uuid-a","name":"Voice A","styles":[{"id":4,"name":"Normal"}]}]`)
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Speakers(context.Background())
	if err != nil || len(got) != 1 || got[0].UUID != "uuid-a" || got[0].Styles[0].ID != 4 {
		t.Fatalf("Speakers() = %#v, %v", got, err)
	}
}

func TestSynthesizeWritesValidatedPCMAndReportsDuration(t *testing.T) {
	wav := testWAV(16000, 1, 1600)
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/audio_query":
			if r.URL.Query().Get("speaker") != "4" || r.URL.Query().Get("text") != "hello" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"speedScale":1}`)
		case "/synthesis":
			if r.URL.Query().Get("speaker") != "4" {
				t.Errorf("synthesis query = %s", r.URL.RawQuery)
			}
			var query map[string]float64
			if err := json.NewDecoder(r.Body).Decode(&query); err != nil || query["speedScale"] != 1.25 {
				t.Errorf("synthesis speedScale = %v, err=%v", query["speedScale"], err)
			}
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(wav)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "speech.wav")
	speech, err := c.Synthesize(context.Background(), "hello", xpostmodel.Voice{StyleID: 4, Speed: 1.25}, path)
	if err != nil {
		t.Fatal(err)
	}
	if speech.Duration != .1 || speech.SampleRate != 16000 || speech.Channels != 1 || speech.Samples != 1600 {
		t.Fatalf("speech = %#v", speech)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != string(wav) {
		t.Fatalf("output mismatch, err=%v", err)
	}
}

func TestSynthesizeWhitespaceSkipsNetwork(t *testing.T) {
	c, err := NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	speech, err := c.Synthesize(context.Background(), " \n\t", xpostmodel.Voice{}, filepath.Join(t.TempDir(), "none.wav"))
	if err != nil || !reflect.DeepEqual(speech, xpostmodel.Speech{}) {
		t.Fatalf("got %#v, %v", speech, err)
	}
}

func TestNewClientRejectsNonLoopbackEndpoint(t *testing.T) {
	for _, endpoint := range []string{"http://example.com", "https://192.0.2.1", "file:///tmp/x"} {
		if _, err := NewClient(endpoint); err == nil {
			t.Errorf("NewClient(%q) succeeded", endpoint)
		}
	}
}

func TestSynthesizeDoesNotLeavePartialFileOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "engine failed", http.StatusBadGateway) }))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	path := filepath.Join(t.TempDir(), "speech.wav")
	if _, err := c.Synthesize(context.Background(), "hello", xpostmodel.Voice{StyleID: 4}, path); err == nil {
		t.Fatal("expected engine error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial output exists or stat failed: %v", err)
	}
}

func TestSynthesizeHonorsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(200 * time.Millisecond); fmt.Fprint(w, `{}`) }))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Synthesize(ctx, "hello", xpostmodel.Voice{StyleID: 1}, filepath.Join(t.TempDir(), "speech.wav"))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "context") {
		t.Fatalf("error = %v", err)
	}
}

func TestSynthesizeRejectsMalformedWAV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio_query" {
			fmt.Fprint(w, `{}`)
			return
		}
		_, _ = w.Write([]byte("not a wave"))
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL)
	_, err := c.Synthesize(context.Background(), "x", xpostmodel.Voice{StyleID: 1}, filepath.Join(t.TempDir(), "speech.wav"))
	if err == nil {
		t.Fatal("expected invalid WAV error")
	}
}

func testWAV(sampleRate, channels, samples int) []byte {
	dataBytes := samples * channels * 2
	b := make([]byte, 44+dataBytes)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], uint16(channels))
	binary.LittleEndian.PutUint32(b[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(b[28:], uint32(sampleRate*channels*2))
	binary.LittleEndian.PutUint16(b[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(dataBytes))
	return b
}
