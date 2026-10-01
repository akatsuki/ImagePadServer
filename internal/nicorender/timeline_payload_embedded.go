//go:build nico_timeline_embedded

package nicorender

import _ "embed"

//go:embed timeline_payload/nico-compositord.bin
var embeddedTimelineExecutable []byte

//go:embed timeline_payload/manifest.json
var embeddedTimelineManifest []byte

func timelinePayload() ([]byte, []byte) {
	return embeddedTimelineExecutable, embeddedTimelineManifest
}
