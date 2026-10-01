package nicorender

import (
	"testing"

	"imagepadserver/internal/niconico"
)

func TestTimelineVPos(t *testing.T) {
	tests := []struct {
		name  string
		clock niconico.FrameClock
		frame int64
		want  int32
		bad   bool
	}{
		{"ntsc-negative", niconico.MustFrameClock(60000, 1001), -1, -2, false},
		{"ntsc-zero", niconico.MustFrameClock(60000, 1001), 0, 0, false},
		{"ntsc-one", niconico.MustFrameClock(60000, 1001), 1, 1, false},
		{"ntsc-two", niconico.MustFrameClock(60000, 1001), 2, 3, false},
		{"ntsc-sixty", niconico.MustFrameClock(60000, 1001), 60, 100, false},
		{"thirty-negative", niconico.MustFrameClock(30, 1), -1, -4, false},
		{"too-large-vpos", niconico.MustFrameClock(1, 1), int64(1<<31)/100 + 1, 0, true},
		{"invalid-clock", niconico.FrameClock{}, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TimelineVPos(tt.clock, tt.frame)
			if tt.bad {
				if err == nil {
					t.Fatalf("TimelineVPos(%+v, %d) = %d, want error", tt.clock, tt.frame, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("TimelineVPos(%+v, %d) = %d, %v; want %d", tt.clock, tt.frame, got, err, tt.want)
			}
		})
	}
}
