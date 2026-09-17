//go:build !windows

package airplay

import "os"

func atomicReplaceVideoViewFile(tmp, dst string) error { return os.Rename(tmp, dst) }
