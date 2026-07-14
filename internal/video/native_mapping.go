package video

import "errors"

var ErrNativeMappingUnavailable = errors.New("native shared mapping unavailable")

type NativeMapping interface {
	Bytes() []byte
	Close() error
}

// OpenNativeMapping creates or opens an OS-backed shared byte region. The
// frame-ring header and ownership protocol are layered above this primitive.
func OpenNativeMapping(name string, size int) (NativeMapping, error) {
	return openNativeMapping(name, size)
}
