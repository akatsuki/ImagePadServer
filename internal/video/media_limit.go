package video

import (
	"errors"
	"io"
)

// ErrMediaTooLarge is returned when media data exceeds the 4 GiB - 1 byte limit.
var ErrMediaTooLarge = errors.New("media exceeds 4294967295 byte limit")

// CopyMediaWithLimit copies from src to dst, enforcing MaxMediaSourceBytes as
// the upper bound. It returns the number of bytes read from src and any error
// encountered. If the source exceeds the limit, ErrMediaTooLarge is returned
// along with the number of bytes actually read.
func CopyMediaWithLimit(dst io.Writer, src io.Reader) (int64, error) {
	return copyWithLimit(dst, src, MaxMediaSourceBytes)
}

// copyWithLimit copies from src to dst, enforcing the supplied limit (in bytes).
// It reads up to limit+1 bytes from src via io.LimitReader. If exactly limit+1
// bytes are consumed, the source exceeds the limit and ErrMediaTooLarge is
// returned along with the full count.
func copyWithLimit(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	limited := io.LimitReader(src, limit+1)
	n, err := io.Copy(dst, limited)
	if n > limit {
		return n, ErrMediaTooLarge
	}
	return n, err
}

// ValidateMediaContentLength returns ErrMediaTooLarge when length exceeds
// MaxMediaSourceBytes. Values at or below the limit (including zero) are
// accepted with a nil return.
func ValidateMediaContentLength(length int64) error {
	if length > MaxMediaSourceBytes {
		return ErrMediaTooLarge
	}
	return nil
}
