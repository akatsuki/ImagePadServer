//go:build !nico_timeline_embedded

package nicorender

func timelinePayload() ([]byte, []byte) { return nil, nil }
