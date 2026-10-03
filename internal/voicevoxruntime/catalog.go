package voicevoxruntime

import "fmt"

const Version = "0.25.2"
const Endpoint = "http://127.0.0.1:50121"

type Asset struct {
	Name, URL, SHA256 string
	Size              int64
}

// Fixed official release assets; never execute an unverified latest download.
func releaseAsset(goos, arch string) (Asset, error) {
	var name, hash string
	var size int64
	switch goos + "/" + arch {
	case "windows/amd64":
		name = "windows-cpu"
		hash = "cae07cb718866708d8c6148988769966168a2282610f53f002b19778ebea38e9"
		size = 1894411533
	case "linux/amd64":
		name = "linux-cpu-x64"
		hash = "024ce70140d2028638a00014c037b97b82f83f4efd8442cc421bd555e2f122e6"
		size = 1911613161
	case "linux/arm64":
		name = "linux-cpu-arm64"
		hash = "d22f92195baa802d457ecc50287371e87cb590ab90a99fbfeb97f4d0eb968519"
		size = 1907271021
	case "darwin/amd64":
		name = "macos-x64"
		hash = "88cabb15d183bf163df37507e70e88acb897de6f5ad0e14ea6cc5f0ce7b3096b"
		size = 1890344527
	case "darwin/arm64":
		name = "macos-arm64"
		hash = "1ba776700d2afa81382573de52961ebaa33ee26c2aedc8d3ed78782a4e1538fb"
		size = 1887128088
	default:
		return Asset{}, fmt.Errorf("VOICEVOX自動準備は %s/%s に対応していません", goos, arch)
	}
	name = "voicevox_engine-" + name + "-" + Version + ".vvpp"
	return Asset{Name: name, URL: "https://github.com/VOICEVOX/voicevox_engine/releases/download/" + Version + "/" + name, SHA256: hash, Size: size}, nil
}
