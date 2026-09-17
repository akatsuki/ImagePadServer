//go:build windows && amd64 && nico_native_embedded

package nicorender

import _ "embed"

//go:embed payload/nico-compositor.exe
var nativeExecutable []byte

//go:embed payload/nico-compositor.json
var nativeManifest []byte

func nativePayload() ([]byte, []byte) { return nativeExecutable, nativeManifest }
