//go:build !windows

package video

import (
	"fmt"
	"os"
	"syscall"
)

type unixMapping struct { file *os.File; bytes []byte }
func openNativeMapping(name string, size int) (NativeMapping, error) {
	if name == "" || size <= 0 { return nil, ErrNativeMappingUnavailable }
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600); if err != nil { return nil, err }
	if err = f.Truncate(int64(size)); err != nil { f.Close(); return nil, err }
	b, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED); if err != nil { f.Close(); return nil, fmt.Errorf("mmap: %w", err) }
	return &unixMapping{file: f, bytes: b}, nil
}
func (m *unixMapping) Bytes() []byte { return m.bytes }
func (m *unixMapping) Close() error { err := syscall.Munmap(m.bytes); e2 := m.file.Close(); if err != nil { return err }; return e2 }
