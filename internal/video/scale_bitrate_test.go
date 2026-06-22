package video

import "testing"

func TestScaleBitrate(t *testing.T) {
	cases := []struct {
		in     string
		factor float64
		want   string
	}{
		{"3000k", 0.83, "2490k"},
		{"5200k", 0.83, "4316k"},
		{"9000k", 0.83, "7470k"},
		{"1100k", 0.83, "913k"},
		{"", 0.83, ""},
		{"2M", 0.5, "1M"},
		{"abc", 0.83, "abc"},
	}
	for _, c := range cases {
		if got := scaleBitrate(c.in, c.factor); got != c.want {
			t.Errorf("scaleBitrate(%q,%v) = %q, want %q", c.in, c.factor, got, c.want)
		}
	}
}
