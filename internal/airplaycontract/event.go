package airplaycontract

import "time"

// Event is an AirPlay source-clock publisher notification.
type Event struct {
	Schema                  int       `json:"schema"`
	SessionID               string    `json:"sessionId"`
	PublisherGeneration     uint64    `json:"publisherGeneration"`
	Event                   string    `json:"event"`
	At                      time.Time `json:"at"`
	ProtocolVersion         int       `json:"protocolVersion,omitempty"`
	VideoListenPort         int       `json:"videoListenPort,omitempty"`
	AudioListenPort         int       `json:"audioListenPort,omitempty"`
	PipelineStartAccepted   bool      `json:"pipelineStartAccepted,omitempty"`
	VideoDecoded            bool      `json:"videoDecoded,omitempty"`
	SourceSessionGeneration uint64    `json:"sourceSessionGeneration,omitempty"`
	SourceVideoSequence     *uint64   `json:"sourceVideoSequence,omitempty"`
	RunningTimeNS           *uint64   `json:"runningTimeNs,omitempty"`
	SourceNTPNS             *uint64   `json:"sourceNtpNs,omitempty"`
	RecordingPath           string    `json:"recordingPath,omitempty"`
	RecordingClosed         bool      `json:"recordingClosed,omitempty"`
	ProcessID               int       `json:"processId,omitempty"`
}

// ReadyFor reports whether the event proves that the expected publisher accepted pipeline startup.
func (e Event) ReadyFor(id string, generation uint64) bool {
	return e.matches(id, generation) &&
		e.Event == "publisher-ready" &&
		e.ProtocolVersion == 1 &&
		e.VideoListenPort > 0 && e.VideoListenPort <= 65534 &&
		e.AudioListenPort > 0 && e.AudioListenPort <= 65534 &&
		e.PipelineStartAccepted
}

// MediaReadyFor reports whether the event proves that the expected publisher decoded real video.
func (e Event) MediaReadyFor(id string, generation uint64) bool {
	return e.matches(id, generation) &&
		e.Event == "video-decoded" &&
		e.VideoDecoded &&
		e.RunningTimeNS != nil
}

// RecordingFinalizedFor reports whether the event proves that the registered
// recording path was closed for the expected publisher generation.
func (e Event) RecordingFinalizedFor(id string, generation uint64, recordingPath string) bool {
	return e.matches(id, generation) &&
		e.Event == "recording-finalized" &&
		recordingPath != "" && e.RecordingPath == recordingPath &&
		e.RecordingClosed
}

// RTSPEOFFor reports whether the native publisher observed its RTSP server
// closing the publish connection. The process identity keeps the observation
// tied to the exact publisher instance that emitted the generation log.
func (e Event) RTSPEOFFor(id string, generation uint64) bool {
	return e.matches(id, generation) && e.Event == "rtsp-eof" && e.ProcessID > 0
}

func (e Event) matches(id string, generation uint64) bool {
	return e.Schema == 2 &&
		id != "" && e.SessionID == id &&
		generation != 0 && e.PublisherGeneration == generation &&
		!e.At.IsZero()
}
