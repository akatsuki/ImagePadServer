//go:build !windows || !amd64 || !nico_native_embedded

package nicorender

func nativePayload() ([]byte, []byte) { return nil, nil }
