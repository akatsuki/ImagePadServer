package niconico

import "testing"

func TestParseVideoURLCanonicalizesSupportedForms(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantID  string
		wantURL string
	}{
		{name: "watch", input: "https://www.nicovideo.jp/watch/sm9?from=1", wantID: "sm9", wantURL: "https://www.nicovideo.jp/watch/sm9"},
		{name: "short", input: "https://nico.ms/sm9", wantID: "sm9", wantURL: "https://www.nicovideo.jp/watch/sm9"},
		{name: "numeric", input: "https://www.nicovideo.jp/watch/1173108780", wantID: "1173108780", wantURL: "https://www.nicovideo.jp/watch/1173108780"},
		{name: "shorts", input: "https://www.nicovideo.jp/shorts/ss123", wantID: "ss123", wantURL: "https://www.nicovideo.jp/shorts/ss123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVideoURL(tt.input)
			if err != nil {
				t.Fatalf("ParseVideoURL() error = %v", err)
			}
			if got.ID != tt.wantID || got.CanonicalURL != tt.wantURL {
				t.Fatalf("ParseVideoURL() = %#v, want id=%q url=%q", got, tt.wantID, tt.wantURL)
			}
		})
	}
}

func TestParseVideoURLRejectsLookalikesAndCredentials(t *testing.T) {
	for _, input := range []string{
		"https://evilnicovideo.jp/watch/sm9",
		"https://www.nicovideo.jp.evil.example/watch/sm9",
		"https://user:pass@www.nicovideo.jp/watch/sm9",
		"https://www.nicovideo.jp:8443/watch/sm9",
		"https://www.nicovideo.jp/mylist/1",
		"https://www.nicovideo.jp/live/1",
		"javascript:alert(1)",
	} {
		if _, err := ParseVideoURL(input); err == nil {
			t.Errorf("ParseVideoURL(%q) succeeded, want error", input)
		}
	}
}
