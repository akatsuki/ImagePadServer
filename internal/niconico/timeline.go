package niconico

import "fmt"

// FrameClock is the exact rational output frame rate used by the renderer.
// Processing speed never participates in any of these calculations.
type FrameClock struct {
	Num int64 // frames per second numerator
	Den int64 // frames per second denominator
}

func NewFrameClock(num, den int64) (FrameClock, error) {
	if num <= 0 || den <= 0 {
		return FrameClock{}, fmt.Errorf("niconico: frame rate must be positive")
	}
	if num > 60*den {
		return FrameClock{}, fmt.Errorf("niconico: frame rate exceeds 60fps")
	}
	return FrameClock{Num: num, Den: den}, nil
}

// MustFrameClock is intended for package-level constants and tests.
func MustFrameClock(num, den int64) FrameClock {
	clock, err := NewFrameClock(num, den)
	if err != nil {
		panic(err)
	}
	return clock
}

// CommentTimeMs returns floor(frame * Den / Num * 1000). Floor is used for
// negative timestamps too, so a comment just before t=0 is not rounded into
// the first frame accidentally.
func (c FrameClock) CommentTimeMs(frame int64) int64 {
	numerator := frame * c.Den * 1000
	if numerator >= 0 {
		return numerator / c.Num
	}
	return -((-numerator + c.Num - 1) / c.Num)
}

// FrameCountForDurationMs returns ceil(durationMs * fps). A non-positive
// duration contains no output frames.
func (c FrameClock) FrameCountForDurationMs(durationMs int64) int64 {
	if durationMs <= 0 {
		return 0
	}
	numerator := durationMs * c.Num
	denominator := c.Den * 1000
	return (numerator + denominator - 1) / denominator
}
