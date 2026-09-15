package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/airplay/iphonemodel"
	"imagepadserver/internal/airplay/sourceclock"
)

type frame struct {
	payload []byte
	delta   time.Duration
}

type audioMaterial struct {
	frames          []frame
	sampleRate      int
	channels        int
	samplesPerFrame int
}

type options struct {
	videoEndpoint           string
	audioEndpoint           string
	tokenHex                string
	videoPath               string
	audioPath               string
	audioFormatChangePath   string
	rotatedVideoPath        string
	rotateAfterReconnect    bool
	scenario                string
	duration                time.Duration
	fps                     int
	seed                    uint64
	mutate                  bool
	scenarioJSON            string
	reportPath              string
	videoTracePath          string
	splitInitialVideoConfig bool
	initialConfigOnly       bool
	noAudio                 bool
}

type streamReport struct {
	Sent                                int    `json:"sent"`
	Dropped                             int    `json:"dropped"`
	Reconnects                          int    `json:"reconnects"`
	Events                              int    `json:"events"`
	ConfigSent                          int    `json:"configSent"`
	KeyframesSent                       int    `json:"keyframesSent"`
	Connections                         int    `json:"connections"`
	InputFrames                         int    `json:"inputFrames"`
	InputConfigFrames                   int    `json:"inputConfigFrames"`
	CacheConfigReplays                  int    `json:"cacheConfigReplays"`
	PostWatermarkIDRs                   int    `json:"postWatermarkIDRs"`
	ReconnectWatermarkSourceSequence    uint64 `json:"reconnectWatermarkSourceSequence,omitempty"`
	FirstPostWatermarkIDRSourceSequence uint64 `json:"firstPostWatermarkIDRSourceSequence,omitempty"`
	TraceComplete                       bool   `json:"traceComplete"`
	TraceEventsDropped                  uint64 `json:"traceEventsDropped"`
}

type videoSendOptions struct {
	initialConfigOnly    bool
	rotateAfterReconnect bool
	tracePath            string
}

type videoTraceEvent struct {
	Event          string `json:"event"`
	Connection     int    `json:"connection"`
	Sequence       uint32 `json:"sequence,omitempty"`
	SourceSequence uint64 `json:"sourceSequence,omitempty"`
	Watermark      uint64 `json:"connectionWatermarkSourceSequence,omitempty"`
	RemoteNTPNS    uint64 `json:"remoteNtpNs,omitempty"`
	ConfigSource   string `json:"configSource,omitempty"`
	CompleteIDR    bool   `json:"completeIDR,omitempty"`
	Config         bool   `json:"config,omitempty"`
	Keyframe       bool   `json:"keyframe,omitempty"`
	PayloadSize    int    `json:"payloadSize,omitempty"`
	Sent           int    `json:"sent"`
	Dropped        int    `json:"dropped"`
	Reconnects     int    `json:"reconnects"`
	AtUTC          string `json:"atUtc"`
}

type h264AccessUnit struct {
	payload        []byte
	sourceSequence uint64
	remoteNTPNS    uint64
}

type h264BootstrapOutput struct {
	payload        []byte
	sourceSequence uint64
	remoteNTPNS    uint64
	configOnly     bool
	configSource   string
	completeIDR    bool
}

type h264BootstrapDecision struct {
	drop           bool
	bootstrap      bool
	outputs        []h264BootstrapOutput
	sourceSequence uint64
	watermark      uint64
}

type h264BootstrapState struct {
	sps                              []byte
	pps                              []byte
	watermark                        uint64
	armed                            bool
	inputFrames                      int
	inputConfigFrames                int
	cacheConfigReplays               int
	postWatermarkIDRs                int
	firstPostWatermarkSourceSequence uint64
}

func newH264BootstrapState() *h264BootstrapState {
	return &h264BootstrapState{}
}

func (state *h264BootstrapState) cacheConfig(payload []byte) {
	for _, start := range annexBStarts(payload) {
		pos := start + startCodeSize(payload[start:])
		if pos >= len(payload) {
			continue
		}
		typ := payload[pos] & 0x1f
		end := len(payload)
		for _, next := range annexBStarts(payload[pos+1:]) {
			end = pos + 1 + next
			break
		}
		if typ == 7 {
			state.sps = append([]byte(nil), payload[start:end]...)
		} else if typ == 8 {
			state.pps = append([]byte(nil), payload[start:end]...)
		}
	}
}

func (state *h264BootstrapState) configPayload() []byte {
	if len(state.sps) == 0 || len(state.pps) == 0 {
		return nil
	}
	return append(append([]byte(nil), state.sps...), state.pps...)
}

func (state *h264BootstrapState) observeInput(au h264AccessUnit) {
	state.inputFrames++
	if containsConfigNAL(au.payload) {
		state.inputConfigFrames++
		state.cacheConfig(au.payload)
	}
}

func (state *h264BootstrapState) beginConnection(watermark uint64) {
	state.watermark = watermark
	state.armed = true
}

func (state *h264BootstrapState) decide(au h264AccessUnit) h264BootstrapDecision {
	state.observeInput(au)
	decision := h264BootstrapDecision{sourceSequence: au.sourceSequence, watermark: state.watermark}
	if !state.armed {
		decision.outputs = []h264BootstrapOutput{{payload: append([]byte(nil), au.payload...), sourceSequence: au.sourceSequence, remoteNTPNS: au.remoteNTPNS, completeIDR: containsKeyNAL(au.payload)}}
		return decision
	}
	if au.sourceSequence <= state.watermark || !containsKeyNAL(au.payload) {
		decision.drop = true
		return decision
	}
	config := state.configPayload()
	if len(config) == 0 {
		decision.drop = true
		return decision
	}
	decision.bootstrap = true
	decision.outputs = []h264BootstrapOutput{
		{payload: config, sourceSequence: au.sourceSequence, remoteNTPNS: au.remoteNTPNS, configOnly: true, configSource: "session-cache"},
		{payload: append([]byte(nil), au.payload...), sourceSequence: au.sourceSequence, remoteNTPNS: au.remoteNTPNS, completeIDR: true},
	}
	return decision
}

func (state *h264BootstrapState) commit(decision h264BootstrapDecision, sent bool) {
	if !decision.bootstrap || !sent {
		return
	}
	state.armed = false
	state.cacheConfigReplays++
	state.postWatermarkIDRs++
	if state.firstPostWatermarkSourceSequence == 0 {
		state.firstPostWatermarkSourceSequence = decision.sourceSequence
	}
}

type videoTraceSink struct {
	writer    io.WriteCloser
	events    chan videoTraceEvent
	done      chan struct{}
	mu        sync.Mutex
	err       error
	dropped   uint64
	closeOnce sync.Once
}

func newVideoTraceSink(path string) (*videoTraceSink, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open video trace: %w", err)
	}
	return newVideoTraceSinkWithWriter(file, 256), nil
}

func newVideoTraceSinkWithWriter(writer io.WriteCloser, capacity int) *videoTraceSink {
	if writer == nil {
		return nil
	}
	if capacity < 1 {
		capacity = 1
	}
	sink := &videoTraceSink{writer: writer, events: make(chan videoTraceEvent, capacity), done: make(chan struct{})}
	go sink.run()
	return sink
}

func (sink *videoTraceSink) run() {
	defer close(sink.done)
	encoder := json.NewEncoder(sink.writer)
	for event := range sink.events {
		sink.mu.Lock()
		failed := sink.err != nil
		sink.mu.Unlock()
		if failed {
			continue
		}
		if err := encoder.Encode(event); err != nil {
			sink.mu.Lock()
			sink.err = fmt.Errorf("write video trace: %w", err)
			sink.mu.Unlock()
		}
	}
	if syncer, ok := sink.writer.(interface{ Sync() error }); ok {
		if err := syncer.Sync(); err != nil {
			sink.mu.Lock()
			if sink.err == nil {
				sink.err = fmt.Errorf("flush video trace: %w", err)
			}
			sink.mu.Unlock()
		}
	}
	if err := sink.writer.Close(); err != nil {
		sink.mu.Lock()
		if sink.err == nil {
			sink.err = fmt.Errorf("close video trace: %w", err)
		}
		sink.mu.Unlock()
	}
}

func (sink *videoTraceSink) record(event videoTraceEvent) {
	if sink == nil {
		return
	}
	event.AtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	select {
	case sink.events <- event:
	default:
		sink.mu.Lock()
		sink.dropped++
		sink.mu.Unlock()
	}
}

func (sink *videoTraceSink) close() error {
	if sink == nil {
		return nil
	}
	sink.closeOnce.Do(func() {
		close(sink.events)
		<-sink.done
	})
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.err
}

type audioReport struct {
	FormatChanges       int  `json:"formatChanges"`
	InitialSampleRate   int  `json:"initialSampleRate,omitempty"`
	InitialChannels     int  `json:"initialChannels,omitempty"`
	FinalSampleRate     int  `json:"finalSampleRate,omitempty"`
	FinalChannels       int  `json:"finalChannels,omitempty"`
	SecondaryFileOpened bool `json:"secondaryFileOpened"`
	Enabled             bool `json:"enabled"`
	FileOpened          bool `json:"fileOpened"`
	Connections         int  `json:"connections"`
	Sent                int  `json:"sent"`
	Dropped             int  `json:"dropped"`
	Reconnects          int  `json:"reconnects"`
	Events              int  `json:"events"`
}

type streamResult struct {
	report streamReport
	err    error
}

type audioStreamResult struct {
	report audioReport
	err    error
}

type fixtureReport struct {
	Scenario iphonemodel.Scenario `json:"scenario"`
	Video    streamReport         `json:"video"`
	Audio    audioReport          `json:"audio"`
}

func audioReportFor(enabled bool) audioReport {
	return audioReport{Enabled: enabled}
}

func runFixtureStreams(noAudio bool, videoFn func() (streamReport, error), audioFn func() (audioReport, error)) (streamReport, error, audioReport, error) {
	videoResult := make(chan streamResult, 1)
	go func() {
		report, err := videoFn()
		videoResult <- streamResult{report: report, err: err}
	}()

	audio := audioReportFor(false)
	var audioResult chan audioStreamResult
	if !noAudio {
		audioResult = make(chan audioStreamResult, 1)
		go func() {
			report, err := audioFn()
			audioResult <- audioStreamResult{report: report, err: err}
		}()
	}

	video := <-videoResult
	if noAudio {
		return video.report, video.err, audio, nil
	}
	gotAudio := <-audioResult
	return video.report, video.err, gotAudio.report, gotAudio.err
}

func parseOptions(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("airplay-source-clock-fixture", flag.ContinueOnError)
	flags.StringVar(&opts.videoEndpoint, "video", "", "source-clock video endpoint host:port")
	flags.StringVar(&opts.audioEndpoint, "audio", "", "source-clock audio endpoint host:port")
	flags.StringVar(&opts.tokenHex, "token", "", "16-byte source-clock session token in hex")
	flags.StringVar(&opts.videoPath, "video-file", "", "Annex-B H.264 file with AUD NALs")
	flags.StringVar(&opts.audioPath, "audio-file", "", "AAC ADTS file")
	flags.StringVar(&opts.audioFormatChangePath, "audio-format-change-file", "", "second AAC ADTS file for audio-format-change")
	flags.StringVar(&opts.scenario, "scenario", "steady-60", "fixture scenario")
	flags.DurationVar(&opts.duration, "duration", 5*time.Second, "scenario duration")
	flags.IntVar(&opts.fps, "fps", 60, "video cadence")
	flags.StringVar(&opts.rotatedVideoPath, "rotated-video-file", "", "optional second H.264 fixture for rotation scenarios")
	flags.BoolVar(&opts.rotateAfterReconnect, "rotate-after-reconnect", false, "switch to the rotated H.264 fixture after reconnect")
	flags.Uint64Var(&opts.seed, "seed", 1, "deterministic scenario seed")
	flags.BoolVar(&opts.mutate, "mutate", false, "apply one deterministic transport mutation")
	flags.StringVar(&opts.scenarioJSON, "scenario-json", "", "load scenario JSON instead of a built-in")
	flags.StringVar(&opts.reportPath, "report", "", "write fixture report JSON")
	flags.StringVar(&opts.videoTracePath, "video-trace", "", "write connection and H.264 bootstrap trace JSONL")
	flags.BoolVar(&opts.splitInitialVideoConfig, "split-initial-video-config", false, "send initial H.264 parameter sets separately from the first IDR")
	flags.BoolVar(&opts.initialConfigOnly, "initial-config-only", false, "remove H.264 SPS/PPS after the initial bootstrap")
	flags.BoolVar(&opts.noAudio, "no-audio", false, "do not read or send audio")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	return opts, nil
}

func receiverAdapterArgs(args []string, getenv func(string) string) ([]string, error) {
	if getenv == nil {
		return nil, fmt.Errorf("receiver adapter environment reader is nil")
	}
	values := make(map[string]string)
	known := map[string]bool{
		"-n": true, "-vs": true, "-as": true, "-fps": true, "-d": true,
		"-ipscv": true, "-ipsca": true, "-ipsct": true, "-ipscid": true,
	}
	for index := 0; index < len(args); index++ {
		flagName := args[index]
		if !known[flagName] {
			return nil, fmt.Errorf("unsupported receiver adapter argument %q", flagName)
		}
		if index+1 >= len(args) {
			return nil, fmt.Errorf("receiver adapter argument %q has no value", flagName)
		}
		index++
		values[flagName] = args[index]
	}
	for _, name := range []string{"-ipscv", "-ipsca", "-ipsct"} {
		if strings.TrimSpace(values[name]) == "" {
			return nil, fmt.Errorf("receiver adapter is missing %s", name)
		}
	}
	videoPath := strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_FILE"))
	if videoPath == "" {
		return nil, fmt.Errorf("IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_FILE is required")
	}
	scenario := strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_SCENARIO"))
	if scenario == "" {
		scenario = "video-only"
	}
	duration := strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_DURATION"))
	if duration == "" {
		duration = "24s"
	}
	fps := strings.TrimSpace(values["-fps"])
	if fps == "" {
		fps = "60"
	}
	normalized := []string{
		"-video", values["-ipscv"], "-audio", values["-ipsca"],
		"-token", values["-ipsct"], "-video-file", videoPath,
	}
	noAudio := scenario == "video-only" || scenario == "video-only-forever" || isPublisherRestartScenario(scenario) || scenario == "no-signal-after-media" || scenario == "no-initial-media" ||
		strings.EqualFold(strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO")), "true") ||
		strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO")) == "1"
	if noAudio {
		normalized = append(normalized, "-no-audio")
	} else {
		audioPath := strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_AUDIO_FILE"))
		if audioPath == "" {
			return nil, fmt.Errorf("IMAGEPAD_AIRPLAY_FIXTURE_AUDIO_FILE is required for %s", scenario)
		}
		normalized = append(normalized, "-audio-file", audioPath)
	}
	normalized = append(normalized, "-scenario", scenario, "-duration", duration, "-fps", fps)
	if environmentSwitchEnabled(getenv("IMAGEPAD_AIRPLAY_FIXTURE_INITIAL_CONFIG_ONLY")) {
		normalized = append(normalized, "-initial-config-only")
	}
	if rotated := strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_ROTATED_VIDEO_FILE")); rotated != "" {
		normalized = append(normalized, "-rotated-video-file", rotated)
	}
	if environmentSwitchEnabled(getenv("IMAGEPAD_AIRPLAY_FIXTURE_ROTATE_AFTER_RECONNECT")) {
		normalized = append(normalized, "-rotate-after-reconnect")
	}
	if secondary := strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_AUDIO_FORMAT_CHANGE_FILE")); secondary != "" {
		normalized = append(normalized, "-audio-format-change-file", secondary)
	}
	if report := strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_REPORT")); report != "" {
		normalized = append(normalized, "-report", report)
	}
	if trace := strings.TrimSpace(getenv("IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_TRACE")); trace != "" {
		normalized = append(normalized, "-video-trace", trace)
	}
	return normalized, nil
}

func environmentSwitchEnabled(value string) bool {
	return strings.TrimSpace(value) == "1" || strings.EqualFold(strings.TrimSpace(value), "true")
}

func isPublisherRestartScenario(name string) bool {
	return name == "publisher-restart-once" || strings.HasPrefix(name, "publisher-restart-")
}

func buildScenario(opts options) (iphonemodel.Scenario, error) {
	if opts.scenarioJSON != "" {
		if opts.mutate {
			return iphonemodel.Scenario{}, fmt.Errorf("-scenario-json cannot be combined with -mutate")
		}
		data, err := os.ReadFile(opts.scenarioJSON)
		if err != nil {
			return iphonemodel.Scenario{}, fmt.Errorf("read scenario JSON: %w", err)
		}
		var scenario iphonemodel.Scenario
		if err := json.Unmarshal(data, &scenario); err != nil {
			return iphonemodel.Scenario{}, fmt.Errorf("decode scenario JSON: %w", err)
		}
		if err := iphonemodel.ValidateScenario(scenario); err != nil {
			return iphonemodel.Scenario{}, err
		}
		if scenarioHasAudioFormatChange(scenario) {
			if opts.noAudio {
				return iphonemodel.Scenario{}, fmt.Errorf("audio-format-change scenario cannot be combined with -no-audio")
			}
			if opts.audioFormatChangePath == "" {
				return iphonemodel.Scenario{}, fmt.Errorf("audio-format-change scenario requires -audio-format-change-file")
			}
		}
		return scenario, nil
	}
	name := opts.scenario
	if name == "" {
		name = "steady-60"
	}
	builtInName := name
	if isPublisherRestartScenario(name) {
		builtInName = "publisher-restart-once"
	}
	scenario, err := iphonemodel.BuiltInScenario(builtInName, opts.duration, opts.seed)
	if err != nil {
		return iphonemodel.Scenario{}, err
	}
	scenario.Name = name
	if scenarioHasAudioFormatChange(scenario) {
		if opts.noAudio {
			return iphonemodel.Scenario{}, fmt.Errorf("audio-format-change scenario cannot be combined with -no-audio")
		}
		if opts.audioFormatChangePath == "" {
			return iphonemodel.Scenario{}, fmt.Errorf("audio-format-change scenario requires -audio-format-change-file")
		}
	}
	if opts.mutate {
		scenario, _, err = iphonemodel.MutateScenario(scenario, opts.seed)
		if err != nil {
			return iphonemodel.Scenario{}, err
		}
	}
	return scenario, nil
}

func scenarioHasAudioFormatChange(scenario iphonemodel.Scenario) bool {
	for _, event := range scenario.Events {
		if event.Kind == iphonemodel.EventAudioFormatChange {
			return true
		}
	}
	return false
}

type scenarioSpec struct {
	name           string
	duration       time.Duration
	videoPauseAt   time.Duration
	videoPauseFor  time.Duration
	bothPauseAt    time.Duration
	bothPauseFor   time.Duration
	rotationAt     time.Duration
	burst          bool
	sessionResetAt time.Duration
	audioLateAt    time.Duration
}

var scenarioNames = []string{
	"steady-60", "video-gap-500ms", "both-gap-5s", "video-burst",
	"rotation-format-change", "session-reset", "audio-late-drop", "video-only", "video-only-forever",
	"publisher-restart-once", "publisher-restart-initial-config-only", "publisher-restart-static",
	"publisher-restart-static-initial-config-only", "no-signal-after-media", "audio-format-change",
}

func scenarioFor(name string, duration time.Duration) (scenarioSpec, error) {
	if duration <= 0 {
		return scenarioSpec{}, fmt.Errorf("duration must be positive")
	}
	spec := scenarioSpec{name: name, duration: duration}
	switch name {
	case "steady-60":
	case "video-only", "video-only-forever", "publisher-restart-once", "publisher-restart-initial-config-only",
		"publisher-restart-static", "publisher-restart-static-initial-config-only", "no-signal-after-media":
	case "video-gap-500ms":
		spec.videoPauseAt, spec.videoPauseFor = time.Second, 500*time.Millisecond
	case "both-gap-5s":
		spec.bothPauseAt, spec.bothPauseFor = time.Second, 5*time.Second
	case "video-burst":
		spec.burst = true
	case "rotation-format-change":
		spec.rotationAt = time.Second
	case "session-reset":
		spec.sessionResetAt = 2 * time.Second
	case "audio-late-drop":
		spec.audioLateAt = 2 * time.Second
	case "audio-format-change":
	default:
		return scenarioSpec{}, fmt.Errorf("unknown scenario %q (want one of %v)", name, scenarioNames)
	}
	return spec, nil
}

func main() {
	args := os.Args[1:]
	if strings.TrimSpace(os.Getenv("IMAGEPAD_AIRPLAY_FIXTURE_ADAPTER")) == "1" {
		var adapterErr error
		args, adapterErr = receiverAdapterArgs(args, os.Getenv)
		if adapterErr != nil {
			fatalf("receiver adapter: %v", adapterErr)
		}
	}
	opts, err := parseOptions(args)
	if err != nil {
		fatalf("options: %v", err)
	}
	scenario, err := buildScenario(opts)
	if err != nil || opts.videoEndpoint == "" || opts.tokenHex == "" || opts.videoPath == "" || opts.fps < 1 ||
		(!opts.noAudio && (opts.audioEndpoint == "" || opts.audioPath == "")) {
		fatalf("invalid fixture options or scenario: %v", err)
	}
	token, err := hex.DecodeString(opts.tokenHex)
	if err != nil || len(token) != sourceclock.SessionTokenBytes {
		fatalf("invalid session token")
	}
	videoFrames, err := readH264Frames(opts.videoPath, opts.fps)
	if err != nil {
		fatalf("read video fixture: %v", err)
	}
	if opts.splitInitialVideoConfig {
		videoFrames, err = splitInitialH264Config(videoFrames)
		if err != nil {
			fatalf("split initial video configuration: %v", err)
		}
	}
	if opts.initialConfigOnly {
		videoFrames, err = retainInitialH264ConfigOnly(videoFrames)
		if err != nil {
			fatalf("retain initial H.264 configuration only: %v", err)
		}
	}
	rotatedFrames := videoFrames
	if opts.rotatedVideoPath != "" {
		rotatedFrames, err = readH264Frames(opts.rotatedVideoPath, opts.fps)
		if err != nil {
			fatalf("read rotated video fixture: %v", err)
		}
	}
	var audioMaterials []audioMaterial
	audio := audioReportFor(!opts.noAudio)
	if !opts.noAudio {
		primary, readErr := readADTSMaterial(opts.audioPath)
		err = readErr
		if err != nil {
			fatalf("read audio fixture: %v", err)
		}
		audioMaterials = append(audioMaterials, primary)
		audio.FileOpened = true
		if scenarioHasAudioFormatChange(scenario) {
			secondary, secondaryErr := readADTSMaterial(opts.audioFormatChangePath)
			if secondaryErr != nil {
				fatalf("read secondary audio fixture: %v", secondaryErr)
			}
			audioMaterials = append(audioMaterials, secondary)
			audio.SecondaryFileOpened = true
		}
	}
	secondaryFileOpened := audio.SecondaryFileOpened
	video, videoErr, sentAudio, audioErr := runFixtureStreams(opts.noAudio,
		func() (streamReport, error) {
			return sendVideo(opts.videoEndpoint, token, videoFrames, rotatedFrames, opts.fps, scenario, videoSendOptions{
				initialConfigOnly:    opts.initialConfigOnly,
				rotateAfterReconnect: opts.rotateAfterReconnect,
				tracePath:            opts.videoTracePath,
			})
		}, func() (audioReport, error) {
			return sendAudioMaterials(opts.audioEndpoint, token, audioMaterials, scenario)
		})
	if !opts.noAudio {
		audio = audioReport{Enabled: audio.Enabled, FileOpened: audio.FileOpened,
			Connections: sentAudio.Connections, Sent: sentAudio.Sent,
			Dropped: sentAudio.Dropped, Reconnects: sentAudio.Reconnects,
			Events: sentAudio.Events, FormatChanges: sentAudio.FormatChanges,
			InitialSampleRate: sentAudio.InitialSampleRate, InitialChannels: sentAudio.InitialChannels,
			FinalSampleRate: sentAudio.FinalSampleRate, FinalChannels: sentAudio.FinalChannels,
			SecondaryFileOpened: secondaryFileOpened}
	}
	report := fixtureReport{Scenario: scenario, Video: video, Audio: audio}
	if opts.reportPath != "" {
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			fatalf("encode report: %v", marshalErr)
		}
		if writeErr := os.WriteFile(opts.reportPath, append(data, '\n'), 0o600); writeErr != nil {
			fatalf("write report: %v", writeErr)
		}
	}
	if videoErr != nil {
		fatalf("send video: %v", videoErr)
	}
	if audioErr != nil {
		fatalf("send audio: %v", audioErr)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func sendControl(conn net.Conn, token []byte, opcode sourceclock.Codec) error {
	return sendControlSequence(conn, token, opcode, 0)
}

func sendControlSequence(conn net.Conn, token []byte, opcode sourceclock.Codec, sequence uint32) error {
	payload := token
	if opcode != sourceclock.ControlHello {
		payload = nil
	}
	h := sourceclock.Header{StreamKind: sourceclock.StreamControl, Codec: opcode, PayloadBytes: uint32(len(payload)), Sequence: sequence}
	if _, err := conn.Write(sourceclock.EncodeHeader(h)); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

type streamConnection struct {
	endpoint string
	token    []byte
	conn     net.Conn
}

type dialFunc func(network, address string) (net.Conn, error)

func (connection *streamConnection) connect(generation uint32, dialers ...dialFunc) error {
	dial := dialFunc(net.Dial)
	if len(dialers) > 0 && dialers[0] != nil {
		dial = dialers[0]
	}
	conn, err := dial("tcp", connection.endpoint)
	if err != nil {
		return err
	}
	connection.conn = conn
	if err := sendControl(conn, connection.token, sourceclock.ControlHello); err != nil {
		connection.close()
		return err
	}
	if err := sendControlSequence(conn, nil, sourceclock.ControlSessionStart, generation); err != nil {
		connection.close()
		return err
	}
	return nil
}

func (connection *streamConnection) close() {
	if connection.conn != nil {
		_ = connection.conn.Close()
		connection.conn = nil
	}
}

func reconnectStreamUntilPublisherReturns(connection *streamConnection, generation uint32, deadline time.Time,
	now func() time.Time, sleep func(time.Duration), dial dialFunc) error {
	if connection == nil || now == nil || sleep == nil || dial == nil {
		return fmt.Errorf("publisher reconnect dependencies are incomplete")
	}
	connection.close()
	var lastErr error
	for now().Before(deadline) {
		if err := connection.connect(generation, dial); err == nil {
			return nil
		} else {
			lastErr = err
		}
		sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("publisher did not return before scenario deadline: %w", lastErr)
}

func eventsFor(scenario iphonemodel.Scenario, stream iphonemodel.Stream) []iphonemodel.Event {
	events := make([]iphonemodel.Event, 0, len(scenario.Events))
	for _, event := range scenario.Events {
		if event.Stream == stream || event.Stream == iphonemodel.StreamControl {
			events = append(events, event)
		}
	}
	return events
}

func ntpWithDelta(ntp uint64, delta time.Duration) uint64 {
	if delta >= 0 {
		return ntp + uint64(delta)
	}
	backward := uint64(-delta)
	if backward > ntp {
		return 0
	}
	return ntp - backward
}

func sendVideo(endpoint string, token []byte, frames, rotatedFrames []frame, fps int, scenario iphonemodel.Scenario, sendOptions ...videoSendOptions) (report streamReport, returnErr error) {
	var option videoSendOptions
	if len(sendOptions) > 0 {
		option = sendOptions[0]
	}
	trace, err := newVideoTraceSink(option.tracePath)
	if err != nil {
		return report, err
	}
	report.TraceComplete = true
	defer func() {
		if traceErr := trace.close(); traceErr != nil {
			fmt.Fprintf(os.Stderr, "video trace unavailable: %v\n", traceErr)
			report.TraceComplete = false
		}
		if trace != nil {
			trace.mu.Lock()
			report.TraceEventsDropped = trace.dropped
			if trace.dropped > 0 {
				report.TraceComplete = false
			}
			trace.mu.Unlock()
		}
	}()
	started := time.Now()
	deadline := started.Add(scenario.Duration)
	const sourceStart = uint64(1_000_000_000_000_000_000)
	ntp := sourceStart
	period := time.Second / time.Duration(fps)
	seq := uint32(0)
	var sourceSequence uint64
	rotated := false
	rotationAfterReconnectRecorded := false
	rotationFramePending := false
	frameIndex := 0
	initialSPSSent := false
	initialPPSSent := false
	fragmentBytes := 0
	paused := false
	var pauseUntil time.Duration
	burst := false
	truncateNext := false
	oversizeNext := false
	var nextNTPDelta time.Duration
	events := eventsFor(scenario, iphonemodel.StreamVideo)
	eventIndex := 0
	bootstrap := newH264BootstrapState()
	connection := streamConnection{endpoint: endpoint, token: token}
	if err := connection.connect(1); err != nil {
		return report, fmt.Errorf("connect: %w", err)
	}
	connectionIndex := 1
	report.Connections = 1
	trace.record(videoTraceEvent{Event: "connection-open", Connection: connectionIndex})
	defer connection.close()
	syncBootstrapReport := func() {
		report.InputFrames = bootstrap.inputFrames
		report.InputConfigFrames = bootstrap.inputConfigFrames
		report.CacheConfigReplays = bootstrap.cacheConfigReplays
		report.PostWatermarkIDRs = bootstrap.postWatermarkIDRs
		report.FirstPostWatermarkIDRSourceSequence = bootstrap.firstPostWatermarkSourceSequence
	}
	advanceClock := func() {
		if burst {
			if now := sourceStart + uint64(time.Since(started)); now > ntp {
				ntp = now
			}
			return
		}
		ntp += uint64(period)
		time.Sleep(period)
	}
	markRotationAfterReconnect := func() {
		if !option.rotateAfterReconnect || rotationAfterReconnectRecorded || connectionIndex != 2 || len(rotatedFrames) == 0 {
			return
		}
		rotated = true
		frameIndex = 0
		rotationAfterReconnectRecorded = true
		trace.record(videoTraceEvent{Event: "rotation-after-reconnect", Connection: connectionIndex,
			Watermark: bootstrap.watermark, Sent: report.Sent, Dropped: report.Dropped, Reconnects: report.Reconnects})
	}

	for time.Now().Before(deadline) {
		elapsed := time.Since(started)
		for eventIndex < len(events) && events[eventIndex].At <= elapsed {
			event := events[eventIndex]
			eventIndex++
			report.Events++
			switch event.Kind {
			case iphonemodel.EventGapStart:
				paused = true
				pauseUntil = event.At + event.Duration
			case iphonemodel.EventGapEnd:
				paused = false
			case iphonemodel.EventRotation:
				rotated = !rotated
				rotationFramePending = true
				frameIndex = 0
			case iphonemodel.EventSessionEnd:
				if connection.conn != nil {
					if err := sendControlSequence(connection.conn, nil, sourceclock.ControlSessionEnd, event.Generation); err != nil {
						return report, err
					}
				}
			case iphonemodel.EventSessionStart:
				if !(event.At == 0 && event.Generation == 1) && connection.conn != nil {
					if err := sendControlSequence(connection.conn, nil, sourceclock.ControlSessionStart, event.Generation); err != nil {
						return report, err
					}
				}
			case iphonemodel.EventNTPJump:
				nextNTPDelta = event.NTPDelta
			case iphonemodel.EventBurstStart:
				burst = true
			case iphonemodel.EventBurstEnd:
				burst = false
			case iphonemodel.EventDisconnect:
				connection.close()
			case iphonemodel.EventReconnect:
				connection.close()
				if err := connection.connect(event.Generation); err != nil {
					return report, fmt.Errorf("reconnect: %w", err)
				}
				report.Reconnects++
				report.Connections++
				bootstrap.beginConnection(sourceSequence)
				report.ReconnectWatermarkSourceSequence = sourceSequence
				connectionIndex++
				trace.record(videoTraceEvent{Event: "connection-open", Connection: connectionIndex,
					Watermark: sourceSequence, Sent: report.Sent, Dropped: report.Dropped, Reconnects: report.Reconnects})
				markRotationAfterReconnect()
			case iphonemodel.EventFragmentSize:
				fragmentBytes = event.FragmentBytes
			case iphonemodel.EventTruncateNextFrame:
				truncateNext = true
			case iphonemodel.EventOversizeNextFrame:
				oversizeNext = true
			}
		}
		if paused && pauseUntil > 0 && elapsed >= pauseUntil {
			paused = false
		}
		if paused {
			report.Dropped++
			advanceClock()
			continue
		}
		if connection.conn == nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		activeFrames := frames
		if rotated {
			activeFrames = rotatedFrames
		}
		if len(activeFrames) == 0 {
			return report, fmt.Errorf("video fixture has no frames")
		}
		if frameIndex >= len(activeFrames) {
			frameIndex = 0
		}
		item := activeFrames[frameIndex]
		frameIndex++
		sourceSequence++
		if now := sourceStart + uint64(elapsed); now > ntp {
			ntp = now
		}
		frameNTP := ntpWithDelta(ntp, nextNTPDelta)
		nextNTPDelta = 0
		rawPayload := append([]byte(nil), item.payload...)
		pendingSPS := false
		pendingPPS := false
		if option.initialConfigOnly && containsConfigNAL(rawPayload) {
			item.payload, pendingSPS, pendingPPS = retainUnsentH264ConfigNALs(rawPayload, initialSPSSent, initialPPSSent)
			if len(item.payload) == 0 {
				bootstrap.observeInput(h264AccessUnit{payload: rawPayload, sourceSequence: sourceSequence, remoteNTPNS: frameNTP})
				report.Dropped++
				advanceClock()
				continue
			}
		}
		decision := bootstrap.decide(h264AccessUnit{payload: item.payload, sourceSequence: sourceSequence, remoteNTPNS: frameNTP})
		syncBootstrapReport()
		if decision.drop {
			report.Dropped++
			advanceClock()
			continue
		}
		faultInjected := false
		reconnected := false
		for outputIndex, output := range decision.outputs {
			seq++
			flags := sourceVideoFlags(output.payload)
			if rotated && rotationFramePending && outputIndex == len(decision.outputs)-1 {
				flags |= sourceclock.FlagConfig | sourceclock.FlagDiscontinuity | sourceclock.FlagKeyframe
				rotationFramePending = false
			}
			h := sourceclock.Header{StreamKind: sourceclock.StreamVideo, Codec: sourceclock.CodecH264AnnexBAU,
				Flags: flags, PayloadBytes: uint32(len(output.payload)), Sequence: seq, RemoteNTPNS: output.remoteNTPNS}
			if !decision.bootstrap && oversizeNext {
				h.PayloadBytes = sourceclock.MaxVideoPayload + 1
				if err := writeAll(connection.conn, sourceclock.EncodeHeader(h), fragmentBytes); err != nil {
					return report, err
				}
				connection.close()
				oversizeNext = false
				faultInjected = true
				report.Dropped++
				break
			}
			if !decision.bootstrap && truncateNext {
				if err := writeAll(connection.conn, sourceclock.EncodeHeader(h), fragmentBytes); err != nil {
					return report, err
				}
				if err := writeAll(connection.conn, output.payload[:len(output.payload)/2], fragmentBytes); err != nil {
					return report, err
				}
				connection.close()
				truncateNext = false
				faultInjected = true
				report.Dropped++
				break
			}
			if err := writeFrame(connection.conn, h, output.payload, fragmentBytes); err != nil {
				trace.record(videoTraceEvent{Event: "send-failed", Connection: connectionIndex, Sequence: seq,
					SourceSequence: output.sourceSequence, Watermark: bootstrap.watermark, RemoteNTPNS: output.remoteNTPNS,
					Config: h.Flags&sourceclock.FlagConfig != 0, Keyframe: h.Flags&sourceclock.FlagKeyframe != 0,
					PayloadSize: len(output.payload), Sent: report.Sent, Dropped: report.Dropped, Reconnects: report.Reconnects})
				if !isPublisherRestartScenario(scenario.Name) {
					return report, err
				}
				report.Dropped++
				if reconnectErr := reconnectStreamUntilPublisherReturns(&connection, 1, deadline, time.Now, time.Sleep, dialFunc(net.Dial)); reconnectErr != nil {
					return report, fmt.Errorf("reconnect after publisher restart: %w", reconnectErr)
				}
				report.Reconnects++
				report.Connections++
				connectionIndex++
				bootstrap.beginConnection(sourceSequence)
				report.ReconnectWatermarkSourceSequence = sourceSequence
				trace.record(videoTraceEvent{Event: "connection-open", Connection: connectionIndex, Watermark: sourceSequence,
					Sent: report.Sent, Dropped: report.Dropped, Reconnects: report.Reconnects})
				markRotationAfterReconnect()
				reconnected = true
				break
			}
			report.Sent++
			if h.Flags&sourceclock.FlagConfig != 0 {
				report.ConfigSent++
			}
			if h.Flags&sourceclock.FlagKeyframe != 0 {
				report.KeyframesSent++
			}
			if decision.bootstrap && outputIndex == 0 {
				trace.record(videoTraceEvent{Event: "bootstrap_config", Connection: connectionIndex, Sequence: seq,
					SourceSequence: decision.sourceSequence, Watermark: decision.watermark, RemoteNTPNS: output.remoteNTPNS,
					ConfigSource: output.configSource, Config: true, PayloadSize: len(output.payload),
					Sent: report.Sent, Dropped: report.Dropped, Reconnects: report.Reconnects})
			} else if decision.bootstrap && outputIndex == 1 {
				trace.record(videoTraceEvent{Event: "post_watermark_idr", Connection: connectionIndex, Sequence: seq,
					SourceSequence: decision.sourceSequence, Watermark: decision.watermark, RemoteNTPNS: output.remoteNTPNS,
					CompleteIDR: true, Keyframe: true, PayloadSize: len(output.payload),
					Sent: report.Sent, Dropped: report.Dropped, Reconnects: report.Reconnects})
			} else if h.Flags&(sourceclock.FlagConfig|sourceclock.FlagKeyframe) != 0 {
				trace.record(videoTraceEvent{Event: "video-frame", Connection: connectionIndex, Sequence: seq,
					SourceSequence: output.sourceSequence, RemoteNTPNS: output.remoteNTPNS,
					Config: h.Flags&sourceclock.FlagConfig != 0, Keyframe: h.Flags&sourceclock.FlagKeyframe != 0,
					PayloadSize: len(output.payload), Sent: report.Sent, Dropped: report.Dropped, Reconnects: report.Reconnects})
			}
		}
		if reconnected || faultInjected {
			advanceClock()
			continue
		}
		if decision.bootstrap {
			bootstrap.commit(decision, true)
		}
		initialSPSSent = initialSPSSent || pendingSPS
		initialPPSSent = initialPPSSent || pendingPPS
		syncBootstrapReport()
		advanceClock()
	}
	syncBootstrapReport()
	trace.record(videoTraceEvent{Event: "complete", Connection: connectionIndex,
		Sent: report.Sent, Dropped: report.Dropped, Reconnects: report.Reconnects})
	return report, nil
}

func sendAudio(endpoint string, token []byte, frames []frame, scenario iphonemodel.Scenario) (audioReport, error) {
	return sendAudioWithDeps(endpoint, token, frames, scenario, time.Now, time.Sleep, dialFunc(net.Dial))
}

func sendAudioMaterials(endpoint string, token []byte, materials []audioMaterial, scenario iphonemodel.Scenario) (audioReport, error) {
	return sendAudioMaterialsWithDeps(endpoint, token, materials, scenario, time.Now, time.Sleep, dialFunc(net.Dial))
}

func sendAudioWithDeps(endpoint string, token []byte, frames []frame, scenario iphonemodel.Scenario,
	now func() time.Time, sleep func(time.Duration), dial dialFunc) (audioReport, error) {
	return sendAudioMaterialsWithDeps(endpoint, token, []audioMaterial{{frames: frames, sampleRate: 44100, channels: 2, samplesPerFrame: 1024}}, scenario, now, sleep, dial)
}

func sendAudioMaterialsWithDeps(endpoint string, token []byte, materials []audioMaterial, scenario iphonemodel.Scenario,
	now func() time.Time, sleep func(time.Duration), dial dialFunc) (audioReport, error) {
	report := audioReport{Enabled: true, FileOpened: true}
	if len(materials) == 0 || len(materials[0].frames) == 0 {
		return report, fmt.Errorf("audio fixture has no materials")
	}
	if scenarioHasAudioFormatChange(scenario) {
		if len(materials) < 2 || len(materials[1].frames) == 0 {
			return report, fmt.Errorf("audio-format-change requires a second audio material")
		}
		if materials[0].sampleRate == materials[1].sampleRate && materials[0].channels == materials[1].channels {
			return report, fmt.Errorf("audio-format-change requires different sample rate or channel count")
		}
	}
	report.InitialSampleRate = materials[0].sampleRate
	report.InitialChannels = materials[0].channels
	report.FinalSampleRate = materials[0].sampleRate
	report.FinalChannels = materials[0].channels
	started := now()
	deadline := started.Add(scenario.Duration)
	const sourceStart = uint64(1_000_000_000_000_000_000)
	ntp := sourceStart
	seq := uint32(0)
	frameIndex := 0
	materialIndex := 0
	formatChanged := false
	fragmentBytes := 0
	paused := false
	var pauseUntil time.Duration
	burst := false
	var nextNTPDelta time.Duration
	events := eventsFor(scenario, iphonemodel.StreamAudio)
	eventIndex := 0
	connection := streamConnection{endpoint: endpoint, token: token}
	awaitingReconnect := false
	defer connection.close()

	for now().Before(deadline) {
		elapsed := now().Sub(started)
		for eventIndex < len(events) && events[eventIndex].At <= elapsed {
			event := events[eventIndex]
			eventIndex++
			report.Events++
			switch event.Kind {
			case iphonemodel.EventGapStart:
				paused = true
				pauseUntil = event.At + event.Duration
			case iphonemodel.EventGapEnd:
				paused = false
			case iphonemodel.EventSessionEnd:
				if connection.conn != nil {
					if err := sendControlSequence(connection.conn, nil, sourceclock.ControlSessionEnd, event.Generation); err != nil {
						return report, err
					}
				}
			case iphonemodel.EventSessionStart:
				if !(event.At == 0 && event.Generation == 1) && connection.conn != nil {
					if err := sendControlSequence(connection.conn, nil, sourceclock.ControlSessionStart, event.Generation); err != nil {
						return report, err
					}
				}
			case iphonemodel.EventAudioFormatChange:
				if !formatChanged {
					if len(materials) < 2 || len(materials[1].frames) == 0 {
						return report, fmt.Errorf("audio-format-change requires a second audio material")
					}
					materialIndex = 1
					frameIndex = 0
					formatChanged = true
					report.FormatChanges++
					report.FinalSampleRate = materials[materialIndex].sampleRate
					report.FinalChannels = materials[materialIndex].channels
				}
			case iphonemodel.EventNTPJump:
				nextNTPDelta = event.NTPDelta
			case iphonemodel.EventBurstStart:
				burst = true
			case iphonemodel.EventBurstEnd:
				burst = false
			case iphonemodel.EventDisconnect:
				awaitingReconnect = connection.conn != nil
				connection.close()
			case iphonemodel.EventReconnect:
				connection.close()
				if awaitingReconnect {
					if err := connection.connect(event.Generation, dial); err != nil {
						return report, fmt.Errorf("reconnect: %w", err)
					}
					report.Connections++
					report.Reconnects++
					awaitingReconnect = false
				}
			case iphonemodel.EventFragmentSize:
				fragmentBytes = event.FragmentBytes
			}
		}
		if paused && pauseUntil > 0 && elapsed >= pauseUntil {
			paused = false
		}
		if paused {
			report.Dropped++
			sleep(10 * time.Millisecond)
			continue
		}
		if connection.conn == nil {
			if awaitingReconnect {
				sleep(5 * time.Millisecond)
				continue
			}
			if err := connection.connect(1, dial); err != nil {
				return report, fmt.Errorf("connect: %w", err)
			}
			report.Connections++
		}
		material := materials[materialIndex]
		if len(material.frames) == 0 {
			return report, fmt.Errorf("audio fixture has no frames")
		}
		item := material.frames[frameIndex%len(material.frames)]
		frameIndex++
		if now := sourceStart + uint64(elapsed); now > ntp {
			ntp = now
		}
		seq++
		frameNTP := ntpWithDelta(ntp, nextNTPDelta)
		nextNTPDelta = 0
		h := sourceclock.Header{StreamKind: sourceclock.StreamAudio, Codec: sourceclock.CodecAACLCADTS, PayloadBytes: uint32(len(item.payload)), Sequence: seq, RemoteNTPNS: frameNTP, SampleCount: uint32(material.samplesPerFrame), SampleRate: uint32(material.sampleRate), Channels: uint16(material.channels)}
		if err := writeFrame(connection.conn, h, item.payload, fragmentBytes); err != nil {
			return report, err
		}
		report.Sent++
		ntp += uint64(item.delta)
		if !burst {
			sleep(item.delta)
		}
	}
	return report, nil
}

func writeAll(conn net.Conn, data []byte, fragmentBytes int) error {
	if conn == nil {
		return fmt.Errorf("write: connection is nil")
	}
	if fragmentBytes <= 0 || fragmentBytes > len(data) {
		fragmentBytes = len(data)
	}
	for offset := 0; offset < len(data); {
		end := offset + fragmentBytes
		if end > len(data) {
			end = len(data)
		}
		for offset < end {
			written, err := conn.Write(data[offset:end])
			if err != nil {
				return fmt.Errorf("write bytes %d..%d: %w", offset, end, err)
			}
			if written == 0 {
				return fmt.Errorf("write bytes %d..%d: %w", offset, end, io.ErrNoProgress)
			}
			offset += written
		}
	}
	return nil
}

func writeFrame(conn net.Conn, h sourceclock.Header, payload []byte, fragmentBytes int) error {
	if err := writeAll(conn, sourceclock.EncodeHeader(h), fragmentBytes); err != nil {
		return fmt.Errorf("header: %w", err)
	}
	if err := writeAll(conn, payload, fragmentBytes); err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	return nil
}

func readH264Frames(path string, fps int) ([]frame, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	starts := annexBStarts(data)
	if len(starts) < 2 {
		return nil, fmt.Errorf("H.264 fixture has no complete NAL sequence")
	}
	frames := make([]frame, 0)
	current := -1
	for i, start := range starts {
		nalStart := start + startCodeSize(data[start:])
		if nalStart >= len(data) {
			continue
		}
		typ := data[nalStart] & 0x1f
		if typ == 9 {
			if current >= 0 {
				frames = append(frames, frame{payload: append([]byte(nil), data[starts[current]:start]...), delta: time.Second / time.Duration(fps)})
			}
			current = i
		}
	}
	if current >= 0 {
		frames = append(frames, frame{payload: append([]byte(nil), data[starts[current]:]...), delta: time.Second / time.Duration(fps)})
	}
	if len(frames) == 0 {
		frames = append(frames, frame{payload: data, delta: time.Second / time.Duration(fps)})
	}
	return frames, nil
}

func annexBStarts(data []byte) []int {
	starts := make([]int, 0)
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			starts = append(starts, i)
			i += 2
		} else if i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			starts = append(starts, i)
			i += 3
		}
	}
	return starts
}

func startCodeSize(data []byte) int {
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		return 4
	}
	return 3
}

func containsKeyNAL(data []byte) bool {
	for _, start := range annexBStarts(data) {
		pos := start + startCodeSize(data[start:])
		if pos < len(data) && data[pos]&0x1f == 5 {
			return true
		}
	}
	return false
}

func containsConfigNAL(data []byte) bool {
	for _, start := range annexBStarts(data) {
		pos := start + startCodeSize(data[start:])
		if pos < len(data) {
			typ := data[pos] & 0x1f
			if typ == 7 || typ == 8 {
				return true
			}
		}
	}
	return false
}

func sourceVideoFlags(data []byte) uint16 {
	var flags uint16
	if containsConfigNAL(data) {
		flags |= sourceclock.FlagConfig
	}
	if containsKeyNAL(data) {
		flags |= sourceclock.FlagKeyframe
	}
	return flags
}

func splitInitialH264Config(frames []frame) ([]frame, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("video fixture has no frames")
	}
	starts := annexBStarts(frames[0].payload)
	var configuration [][]byte
	var accessUnit []byte
	for i, start := range starts {
		end := len(frames[0].payload)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		pos := start + startCodeSize(frames[0].payload[start:])
		if pos >= end {
			continue
		}
		typ := frames[0].payload[pos] & 0x1f
		if typ == 7 || typ == 8 {
			configuration = append(configuration, append([]byte(nil), frames[0].payload[start:end]...))
		} else {
			accessUnit = append(accessUnit, frames[0].payload[start:end]...)
		}
	}
	if len(configuration) == 0 || !containsKeyNAL(accessUnit) {
		return nil, fmt.Errorf("initial frame does not contain separate H.264 configuration and IDR data")
	}
	result := make([]frame, 0, len(frames)+len(configuration))
	for _, payload := range configuration {
		result = append(result, frame{payload: payload, delta: 0})
	}
	result = append(result, frame{payload: accessUnit, delta: frames[0].delta})
	result = append(result, frames[1:]...)
	return result, nil
}

func stripH264ConfigNALs(data []byte) []byte {
	starts := annexBStarts(data)
	result := make([]byte, 0, len(data))
	for index, start := range starts {
		end := len(data)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		pos := start + startCodeSize(data[start:])
		if pos >= end {
			continue
		}
		typ := data[pos] & 0x1f
		if typ == 7 || typ == 8 {
			continue
		}
		result = append(result, data[start:end]...)
	}
	return result
}

func retainUnsentH264ConfigNALs(data []byte, spsSent, ppsSent bool) ([]byte, bool, bool) {
	starts := annexBStarts(data)
	if len(starts) == 0 {
		return append([]byte(nil), data...), false, false
	}
	result := make([]byte, 0, len(data))
	pendingSPS := false
	pendingPPS := false
	for index, start := range starts {
		end := len(data)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		pos := start + startCodeSize(data[start:])
		if pos >= end {
			continue
		}
		keep := true
		switch data[pos] & 0x1f {
		case 7:
			keep = !spsSent && !pendingSPS
			pendingSPS = pendingSPS || keep
		case 8:
			keep = !ppsSent && !pendingPPS
			pendingPPS = pendingPPS || keep
		}
		if keep {
			result = append(result, data[start:end]...)
		}
	}
	return result, pendingSPS, pendingPPS
}

func retainInitialH264ConfigOnly(frames []frame) ([]frame, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("video fixture has no frames")
	}
	seenSPS := false
	seenPPS := false
	result := make([]frame, 0, len(frames))
	for _, item := range frames {
		starts := annexBStarts(item.payload)
		payload := make([]byte, 0, len(item.payload))
		for index, start := range starts {
			end := len(item.payload)
			if index+1 < len(starts) {
				end = starts[index+1]
			}
			pos := start + startCodeSize(item.payload[start:])
			if pos >= end {
				continue
			}
			keep := true
			switch item.payload[pos] & 0x1f {
			case 7:
				keep = !seenSPS
				seenSPS = true
			case 8:
				keep = !seenPPS
				seenPPS = true
			}
			if keep {
				payload = append(payload, item.payload[start:end]...)
			}
		}
		if len(starts) == 0 {
			payload = append(payload, item.payload...)
		}
		if len(payload) == 0 {
			continue
		}
		result = append(result, frame{payload: append([]byte(nil), payload...), delta: item.delta})
	}
	if !seenSPS || !seenPPS {
		return nil, fmt.Errorf("video fixture has no initial SPS/PPS bootstrap")
	}
	return result, nil
}

func readADTSMaterial(path string) (audioMaterial, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return audioMaterial{}, err
	}
	frames := make([]frame, 0)
	var sampleRate, channels, samplesPerFrame int
	for offset := 0; offset < len(data); {
		if len(data)-offset < 7 || data[offset] != 0xff || data[offset+1]&0xf6 != 0xf0 {
			return audioMaterial{}, fmt.Errorf("invalid ADTS at offset %d", offset)
		}
		headerLength := 7
		if data[offset+1]&1 == 0 {
			headerLength = 9
		}
		profile := int((data[offset+2]>>6)&3) + 1
		sampleRateIndex := int((data[offset+2] >> 2) & 0xf)
		sampleRates := [...]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
		if profile != 2 || sampleRateIndex >= len(sampleRates) {
			return audioMaterial{}, fmt.Errorf("unsupported ADTS metadata at offset %d", offset)
		}
		currentRate := sampleRates[sampleRateIndex]
		currentChannels := int((data[offset+2]&1)<<2 | (data[offset+3] >> 6))
		if currentChannels != 1 && currentChannels != 2 {
			return audioMaterial{}, fmt.Errorf("unsupported ADTS channel configuration %d at offset %d", currentChannels, offset)
		}
		if data[offset+6]&3 != 0 {
			return audioMaterial{}, fmt.Errorf("unsupported ADTS raw data blocks at offset %d", offset)
		}
		length := int(data[offset+3]&0x03)<<11 | int(data[offset+4])<<3 | int(data[offset+5]>>5)
		if length < headerLength || offset+length > len(data) {
			return audioMaterial{}, io.ErrUnexpectedEOF
		}
		if len(frames) == 0 {
			sampleRate, channels, samplesPerFrame = currentRate, currentChannels, 1024
		} else if currentRate != sampleRate || currentChannels != channels {
			return audioMaterial{}, fmt.Errorf("ADTS format changed within file at offset %d", offset)
		}
		frames = append(frames, frame{payload: append([]byte(nil), data[offset:offset+length]...), delta: time.Duration(samplesPerFrame) * time.Second / time.Duration(sampleRate)})
		offset += length
	}
	if len(frames) == 0 {
		return audioMaterial{}, fmt.Errorf("AAC fixture has no frames")
	}
	return audioMaterial{frames: frames, sampleRate: sampleRate, channels: channels, samplesPerFrame: samplesPerFrame}, nil
}

func readADTSFrames(path string) ([]frame, error) {
	material, err := readADTSMaterial(path)
	if err != nil {
		return nil, err
	}
	return material.frames, nil
}
