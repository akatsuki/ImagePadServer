package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/airplay/iphonemodel"
	"imagepadserver/internal/airplay/sourceclock"
)

func TestReceiverAdapterArgsTranslateSourceClockEndpointsAndFixtureEnvironment(t *testing.T) {
	env := map[string]string{
		"IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_FILE": `C:\fixture\video.h264`,
		"IMAGEPAD_AIRPLAY_FIXTURE_AUDIO_FILE": `C:\fixture\audio.aac`,
		"IMAGEPAD_AIRPLAY_FIXTURE_SCENARIO":   "audio-start-after-silence",
		"IMAGEPAD_AIRPLAY_FIXTURE_DURATION":   "24s",
		"IMAGEPAD_AIRPLAY_FIXTURE_REPORT":     `C:\evidence\fixture.json`,
	}
	getenv := func(key string) string { return env[key] }
	got, err := receiverAdapterArgs([]string{
		"-n", "ImagePadServer-AirPlay", "-vs", "0", "-as", "0", "-fps", "60", "-d", "1",
		"-ipscv", "127.0.0.1:41001", "-ipsca", "127.0.0.1:41002",
		"-ipsct", "00112233445566778899aabbccddeeff", "-ipscid", "receiver-001",
	}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-video", "127.0.0.1:41001", "-audio", "127.0.0.1:41002",
		"-token", "00112233445566778899aabbccddeeff",
		"-video-file", `C:\fixture\video.h264`, "-audio-file", `C:\fixture\audio.aac`,
		"-scenario", "audio-start-after-silence", "-duration", "24s",
		"-fps", "60", "-report", `C:\evidence\fixture.json`,
	}
	if len(got) != len(want) {
		t.Fatalf("args=%q want=%q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d]=%q want %q; all=%q", i, got[i], want[i], got)
		}
	}
}

func TestReceiverAdapterArgsSupportsVideoOnlyWithoutAudioMaterial(t *testing.T) {
	env := map[string]string{
		"IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_FILE": `C:\fixture\video.h264`,
		"IMAGEPAD_AIRPLAY_FIXTURE_SCENARIO":   "video-only",
		"IMAGEPAD_AIRPLAY_FIXTURE_DURATION":   "12s",
	}
	got, err := receiverAdapterArgs([]string{
		"-ipscv", "127.0.0.1:42001", "-ipsca", "127.0.0.1:42002",
		"-ipsct", "00112233445566778899aabbccddeeff",
	}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-no-audio") || strings.Contains(joined, "-audio-file") {
		t.Fatalf("video-only adapter args=%q", got)
	}
}

func TestReceiverAdapterArgsUsesVideoOnlyForPublisherRestartScenario(t *testing.T) {
	env := map[string]string{
		"IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_FILE": `C:\fixture\video.h264`,
		"IMAGEPAD_AIRPLAY_FIXTURE_SCENARIO":   "publisher-restart-once",
		"IMAGEPAD_AIRPLAY_FIXTURE_DURATION":   "24s",
	}
	got, err := receiverAdapterArgs([]string{
		"-ipscv", "127.0.0.1:42001", "-ipsca", "127.0.0.1:42002",
		"-ipsct", "00112233445566778899aabbccddeeff",
	}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-no-audio") || strings.Contains(joined, "-audio-file") {
		t.Fatalf("publisher restart adapter args=%q", got)
	}
}

func TestReceiverAdapterArgsMapsPublisherRestartFeasibilityControls(t *testing.T) {
	env := map[string]string{
		"IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_FILE":          `C:\fixture\video.h264`,
		"IMAGEPAD_AIRPLAY_FIXTURE_SCENARIO":            "publisher-restart-initial-config-only",
		"IMAGEPAD_AIRPLAY_FIXTURE_DURATION":            "24s",
		"IMAGEPAD_AIRPLAY_FIXTURE_INITIAL_CONFIG_ONLY": "1",
		"IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_TRACE":         `C:\evidence\video-trace.jsonl`,
	}
	got, err := receiverAdapterArgs([]string{
		"-ipscv", "127.0.0.1:42001", "-ipsca", "127.0.0.1:42002",
		"-ipsct", "00112233445566778899aabbccddeeff",
	}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-no-audio") || !strings.Contains(joined, "-initial-config-only") ||
		!strings.Contains(joined, `-video-trace C:\evidence\video-trace.jsonl`) {
		t.Fatalf("publisher restart feasibility args=%q", got)
	}
}

func TestReceiverAdapterArgsMapsRotationAfterReconnectFixtureControls(t *testing.T) {
	env := map[string]string{
		"IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_FILE":             `C:\fixture\video.h264`,
		"IMAGEPAD_AIRPLAY_FIXTURE_ROTATED_VIDEO_FILE":     `C:\fixture\rotated.h264`,
		"IMAGEPAD_AIRPLAY_FIXTURE_ROTATE_AFTER_RECONNECT": "1",
		"IMAGEPAD_AIRPLAY_FIXTURE_SCENARIO":               "publisher-restart-initial-config-only",
		"IMAGEPAD_AIRPLAY_FIXTURE_DURATION":               "24s",
	}
	got, err := receiverAdapterArgs([]string{
		"-ipscv", "127.0.0.1:42001", "-ipsca", "127.0.0.1:42002",
		"-ipsct", "00112233445566778899aabbccddeeff",
	}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, `-rotated-video-file C:\fixture\rotated.h264`) ||
		!strings.Contains(joined, "-rotate-after-reconnect") {
		t.Fatalf("rotation-after-reconnect adapter args=%q", got)
	}
}

func TestPublisherRestartScenarioClassificationCoversFeasibilityCases(t *testing.T) {
	for _, name := range []string{
		"publisher-restart-once",
		"publisher-restart-initial-config-only",
		"publisher-restart-static",
		"publisher-restart-static-initial-config-only",
	} {
		if !isPublisherRestartScenario(name) {
			t.Fatalf("%q was not classified as a publisher restart scenario", name)
		}
	}
	if isPublisherRestartScenario("video-only") {
		t.Fatal("video-only was classified as a publisher restart scenario")
	}
}

func TestBuildScenarioUsesPublisherRestartTemplateForFeasibilityAliases(t *testing.T) {
	for _, name := range []string{
		"publisher-restart-initial-config-only",
		"publisher-restart-static",
		"publisher-restart-static-initial-config-only",
	} {
		got, err := buildScenario(options{scenario: name, duration: 5 * time.Second, seed: 7, noAudio: true})
		if err != nil {
			t.Fatalf("buildScenario(%q): %v", name, err)
		}
		if got.Name != name || got.Duration != 5*time.Second {
			t.Fatalf("buildScenario(%q)=%#v", name, got)
		}
	}
}

func TestReconnectStreamUntilPublisherReturnsRetriesWithoutChangingGeneration(t *testing.T) {
	now := time.Unix(100, 0)
	deadline := now.Add(time.Second)
	dials := 0
	var captured *deterministicAudioConn
	dial := func(string, string) (net.Conn, error) {
		dials++
		if dials < 3 {
			return nil, io.ErrClosedPipe
		}
		captured = &deterministicAudioConn{}
		return captured, nil
	}
	connection := streamConnection{endpoint: "ignored", token: make([]byte, sourceclock.SessionTokenBytes)}
	err := reconnectStreamUntilPublisherReturns(&connection, 7, deadline, func() time.Time { return now }, func(d time.Duration) {
		now = now.Add(d)
	}, dial)
	connection.close()
	if err != nil {
		t.Fatal(err)
	}
	if dials != 3 {
		t.Fatalf("dial attempts=%d, want 3", dials)
	}
	reader := bytes.NewReader(captured.Bytes())
	if _, _, err := sourceclock.ReadFrame(reader, sourceclock.StreamControl); err != nil {
		t.Fatal(err)
	}
	header, _, err := sourceclock.ReadFrame(reader, sourceclock.StreamControl)
	if err != nil || header.Codec != sourceclock.ControlSessionStart || header.Sequence != 7 {
		t.Fatalf("session start=%#v err=%v, want generation 7", header, err)
	}
}

func TestReceiverAdapterArgsRejectsMissingRequiredFixtureMaterial(t *testing.T) {
	_, err := receiverAdapterArgs([]string{
		"-ipscv", "127.0.0.1:42001", "-ipsca", "127.0.0.1:42002",
		"-ipsct", "00112233445566778899aabbccddeeff",
	}, func(string) string { return "" })
	if err == nil {
		t.Fatal("adapter accepted missing video material")
	}
}

func TestParseADTSMaterialReadsFormatFromHeader(t *testing.T) {
	primary := append(testADTSFrame(2, 4, 2, 0), testADTSFrame(2, 4, 2, 0)...)
	path := filepath.Join(t.TempDir(), "primary.aac")
	if err := os.WriteFile(path, primary, 0o600); err != nil {
		t.Fatal(err)
	}
	material, err := readADTSMaterial(path)
	if err != nil {
		t.Fatal(err)
	}
	if material.sampleRate != 44100 || material.channels != 2 || material.samplesPerFrame != 1024 || len(material.frames) != 2 {
		t.Fatalf("material=%#v", material)
	}
}

func TestParseADTSMaterialReads48KMono(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mono.aac")
	if err := os.WriteFile(path, testADTSFrame(2, 3, 1, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	material, err := readADTSMaterial(path)
	if err != nil {
		t.Fatal(err)
	}
	if material.sampleRate != 48000 || material.channels != 1 || material.samplesPerFrame != 1024 {
		t.Fatalf("material=%#v", material)
	}
}

func TestParseADTSMaterialAcceptsCRCProtectedFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crc.aac")
	if err := os.WriteFile(path, testADTSFrameWithHeaderLength(2, 4, 2, 0, 9, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readADTSMaterial(path); err != nil {
		t.Fatalf("valid CRC-protected ADTS was rejected: %v", err)
	}
}

func TestParseADTSMaterialRejectsCRCFrameShorterThanHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short-crc.aac")
	if err := os.WriteFile(path, testADTSFrameWithHeaderLength(2, 4, 2, 0, 9, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readADTSMaterial(path); err == nil {
		t.Fatal("CRC-protected ADTS shorter than its header was accepted")
	}
}

func TestParseADTSMaterialRejectsInvalidMetadata(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
	}{
		{"unsupported profile", testADTSFrame(1, 4, 2, 0)},
		{"reserved sample rate 13", testADTSFrame(2, 13, 2, 0)},
		{"reserved sample rate 14", testADTSFrame(2, 14, 2, 0)},
		{"reserved sample rate", testADTSFrame(2, 15, 2, 0)},
		{"zero channels", testADTSFrame(2, 4, 0, 0)},
		{"three channels", testADTSFrame(2, 4, 3, 0)},
		{"multiple raw blocks", testADTSFrame(2, 4, 2, 2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.aac")
			if err := os.WriteFile(path, tt.frame, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readADTSMaterial(path); err == nil {
				t.Fatal("invalid ADTS metadata was accepted")
			}
		})
	}
}

func TestParseADTSMaterialRejectsMixedFormat(t *testing.T) {
	data := append(testADTSFrame(2, 4, 2, 0), testADTSFrame(2, 3, 1, 0)...)
	path := filepath.Join(t.TempDir(), "mixed.aac")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readADTSMaterial(path); err == nil {
		t.Fatal("mixed ADTS format was accepted")
	}
}

func TestBuildScenarioRequiresSecondaryAudioForFormatChange(t *testing.T) {
	_, err := buildScenario(options{scenario: "audio-format-change", duration: 5 * time.Second})
	if err == nil {
		t.Fatal("format-change scenario did not require secondary audio")
	}
}

func TestBuildScenarioRejectsNoAudioFormatChange(t *testing.T) {
	_, err := buildScenario(options{scenario: "audio-format-change", duration: 5 * time.Second,
		noAudio: true, audioFormatChangePath: "unused.aac"})
	if err == nil {
		t.Fatal("format-change scenario was accepted with -no-audio")
	}
}

func TestScenarioHasAudioFormatChangeDetectsMalformedStreamByKind(t *testing.T) {
	scenario := iphonemodel.Scenario{Name: "malformed", Duration: time.Second, Events: []iphonemodel.Event{{
		At: 500 * time.Millisecond, Stream: iphonemodel.StreamVideo, Kind: iphonemodel.EventAudioFormatChange, Generation: 1,
	}}}
	if !scenarioHasAudioFormatChange(scenario) {
		t.Fatal("audio format change was hidden by its malformed stream")
	}
}

func TestSendAudioRejectsIdenticalFormatChangeMaterialsBeforeDial(t *testing.T) {
	now := time.Unix(0, 0)
	dialed := false
	dial := func(string, string) (net.Conn, error) {
		dialed = true
		return &deterministicAudioConn{now: func() time.Time { return now }, audioHeaderTimes: new([]time.Duration)}, nil
	}
	sleep := func(duration time.Duration) { now = now.Add(duration) }
	material := audioMaterial{sampleRate: 44100, channels: 2, samplesPerFrame: 1024,
		frames: []frame{{payload: []byte{1}, delta: time.Second * 1024 / 44100}}}
	scenario := iphonemodel.Scenario{Name: "identical-format", Seed: 1, Duration: 50 * time.Millisecond, Events: []iphonemodel.Event{{
		At: 20 * time.Millisecond, Stream: iphonemodel.StreamAudio, Kind: iphonemodel.EventAudioFormatChange, Generation: 1,
	}}}
	_, err := sendAudioMaterialsWithDeps("ignored", make([]byte, sourceclock.SessionTokenBytes),
		[]audioMaterial{material, material}, scenario, func() time.Time { return now }, sleep, dial)
	if err == nil {
		t.Fatal("identical audio materials were accepted as a format change")
	}
	if dialed {
		t.Fatal("identical format change dialed before validation")
	}
}

func TestSendAudioFormatChangeKeepsSessionAndUsesSecondaryMaterial(t *testing.T) {
	now := time.Unix(0, 0)
	conn := &deterministicAudioConn{now: func() time.Time { return now }, audioHeaderTimes: new([]time.Duration)}
	dial := func(string, string) (net.Conn, error) { return conn, nil }
	sleep := func(duration time.Duration) { now = now.Add(duration) }
	material := func(rate, channels int, payload byte) audioMaterial {
		return audioMaterial{sampleRate: rate, channels: channels, samplesPerFrame: 1024,
			frames: []frame{{payload: []byte{payload}, delta: time.Second * 1024 / time.Duration(rate)}}}
	}
	scenario := iphonemodel.Scenario{Name: "format-change", Seed: 1, Duration: 50 * time.Millisecond, Events: []iphonemodel.Event{{
		At: 20 * time.Millisecond, Stream: iphonemodel.StreamAudio, Kind: iphonemodel.EventAudioFormatChange, Generation: 1,
	}}}
	report, err := sendAudioMaterialsWithDeps("ignored", make([]byte, sourceclock.SessionTokenBytes),
		[]audioMaterial{material(44100, 2, 0x11), material(48000, 1, 0x22)}, scenario,
		func() time.Time { return now }, sleep, dial)
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(conn.Bytes())
	if _, _, err := sourceclock.ReadFrame(reader, sourceclock.StreamControl); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sourceclock.ReadFrame(reader, sourceclock.StreamControl); err != nil {
		t.Fatal(err)
	}
	first, firstPayload, err := sourceclock.ReadFrame(reader, sourceclock.StreamAudio)
	if err != nil {
		t.Fatal(err)
	}
	if first.SampleRate != 44100 || first.Channels != 2 || !bytes.Equal(firstPayload, []byte{0x11}) {
		t.Fatalf("first audio header=%#v payload=%x", first, firstPayload)
	}
	foundSecondary := false
	previousSequence := first.Sequence
	previousNTP := first.RemoteNTPNS
	for {
		header, payload, readErr := sourceclock.ReadFrame(reader, sourceclock.StreamAudio)
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if header.Sequence <= previousSequence || header.RemoteNTPNS <= previousNTP {
			t.Fatalf("audio clock regressed: previous=(%d,%d) current=%#v", previousSequence, previousNTP, header)
		}
		previousSequence, previousNTP = header.Sequence, header.RemoteNTPNS
		if header.SampleRate == 48000 && header.Channels == 1 && bytes.Equal(payload, []byte{0x22}) {
			foundSecondary = true
		}
		if foundSecondary && bytes.Equal(payload, []byte{0x11}) {
			t.Fatal("primary audio returned after secondary format became active")
		}
	}
	if !foundSecondary || report.FormatChanges != 1 || report.InitialSampleRate != 44100 || report.InitialChannels != 2 ||
		report.FinalSampleRate != 48000 || report.FinalChannels != 1 || report.SecondaryFileOpened {
		t.Fatalf("report=%#v secondary=%v", report, foundSecondary)
	}
}

func testADTSFrame(profile, sampleRateIndex, channels, rawBlocks byte) []byte {
	return testADTSFrameWithHeaderLength(profile, sampleRateIndex, channels, rawBlocks, 7, 0)
}

func testADTSFrameWithHeaderLength(profile, sampleRateIndex, channels, rawBlocks byte, headerLength, declaredLength int) []byte {
	payload := []byte{0x11, 0x22, 0x33, 0x44}
	length := headerLength + len(payload)
	if declaredLength > 0 {
		length = declaredLength
	}
	header := make([]byte, length)
	header[0] = 0xff
	header[1] = 0xf0
	if headerLength == 7 {
		header[1] = 0xf1
	}
	header[2] = (profile-1)<<6 | (sampleRateIndex << 2) | (channels >> 2)
	header[3] = (channels&3)<<6 | byte(length>>11)
	header[4] = byte(length >> 3)
	header[5] = byte(length&7)<<5 | 0x1f
	header[6] = 0xfc | (rawBlocks & 3)
	for index := headerLength; index < len(header); index++ {
		header[index] = payload[(index-headerLength)%len(payload)]
	}
	return header
}

func TestBuildScenarioPreservesLegacyDefault(t *testing.T) {
	got, err := buildScenario(options{scenario: "steady-60", duration: 5 * time.Second, seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "steady-60" || got.Duration != 5*time.Second {
		t.Fatalf("%#v", got)
	}
}

func TestParseOptionsSupportsNoAudio(t *testing.T) {
	opts, err := parseOptions([]string{"-no-audio"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.noAudio {
		t.Fatal("-no-audio was not enabled")
	}
}

func TestNoAudioReportHasNoAudioActivity(t *testing.T) {
	report := audioReportFor(false)
	if report.Enabled || report.FileOpened || report.Connections != 0 || report.Sent != 0 {
		t.Fatalf("no-audio report=%#v", report)
	}
}

func TestRunFixtureStreamsStartsAudioBeforeVideoCompletes(t *testing.T) {
	videoStarted := make(chan struct{})
	releaseVideo := make(chan struct{})
	audioStarted := make(chan struct{})
	videoFn := func() (streamReport, error) {
		close(videoStarted)
		<-releaseVideo
		return streamReport{}, nil
	}
	audioFn := func() (audioReport, error) {
		close(audioStarted)
		return audioReport{Enabled: true}, nil
	}
	done := make(chan struct{})
	go func() {
		_, _, _, _ = runFixtureStreams(false, videoFn, audioFn)
		close(done)
	}()

	select {
	case <-videoStarted:
	case <-time.After(time.Second):
		t.Fatal("video did not start")
	}
	select {
	case <-audioStarted:
	case <-time.After(time.Second):
		close(releaseVideo)
		t.Fatal("audio did not start before video completed")
	}
	close(releaseVideo)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream orchestration did not complete")
	}
}

func TestRunFixtureStreamsDoesNotCallAudioWhenDisabled(t *testing.T) {
	audioCalled := false
	videoFn := func() (streamReport, error) { return streamReport{}, nil }
	audioFn := func() (audioReport, error) {
		audioCalled = true
		return audioReport{}, nil
	}
	_, _, audio, audioErr := runFixtureStreams(true, videoFn, audioFn)
	if audioErr != nil || audioCalled || audio.Enabled || audio.FileOpened || audio.Connections != 0 || audio.Sent != 0 {
		t.Fatalf("no-audio orchestration: called=%v report=%#v err=%v", audioCalled, audio, audioErr)
	}
}

func TestSendAudioDeterministicLateAudioTimeline(t *testing.T) {
	base := time.Unix(0, 0)
	now := base
	dialTimes := make([]time.Duration, 0, 1)
	sleepTimes := make([]time.Duration, 0)
	audioHeaderTimes := make([]time.Duration, 0, 1)
	sleepAfterDialBeforeFirstAudio := false
	var captured *deterministicAudioConn
	dial := func(string, string) (net.Conn, error) {
		dialTimes = append(dialTimes, now.Sub(base))
		captured = &deterministicAudioConn{now: func() time.Time { return now }, audioHeaderTimes: &audioHeaderTimes}
		return captured, nil
	}
	sleep := func(duration time.Duration) {
		sleepTimes = append(sleepTimes, now.Sub(base))
		if len(dialTimes) > 0 && len(audioHeaderTimes) == 0 {
			sleepAfterDialBeforeFirstAudio = true
		}
		now = now.Add(duration)
	}
	scenario := iphonemodel.Scenario{
		Name: "deterministic-late-audio", Seed: 1, Duration: 100 * time.Millisecond,
		Events: []iphonemodel.Event{{
			At: 0, Stream: iphonemodel.StreamAudio, Kind: iphonemodel.EventGapStart,
			Duration: 40 * time.Millisecond, Generation: 1,
		}, {
			At: 40 * time.Millisecond, Stream: iphonemodel.StreamAudio, Kind: iphonemodel.EventGapEnd,
			Generation: 1,
		}},
	}
	token := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	item := frame{payload: []byte{1, 2, 3}, delta: 10 * time.Millisecond}
	report, err := sendAudioWithDeps("ignored", token, []frame{item}, scenario,
		func() time.Time { return now }, sleep, dial)
	if err != nil {
		t.Fatal(err)
	}
	if len(dialTimes) != 1 || dialTimes[0] != 40*time.Millisecond {
		t.Fatalf("dial times=%v", dialTimes)
	}
	if len(audioHeaderTimes) == 0 || audioHeaderTimes[0] != dialTimes[0] {
		t.Fatalf("audio header times=%v dial times=%v", audioHeaderTimes, dialTimes)
	}
	if sleepAfterDialBeforeFirstAudio {
		t.Fatalf("sleep occurred after dial before first audio frame: %v", sleepTimes)
	}
	if report.Connections != 1 || report.Reconnects != 0 || report.Sent == 0 {
		t.Fatalf("report=%#v", report)
	}
	if captured == nil {
		t.Fatal("audio connection was not captured")
	}
	reader := bytes.NewReader(captured.Bytes())
	hello, helloPayload, err := sourceclock.ReadFrame(reader, sourceclock.StreamControl)
	if err != nil {
		t.Fatal(err)
	}
	if hello.Codec != sourceclock.ControlHello || !bytes.Equal(helloPayload, token) {
		t.Fatalf("hello header=%#v payload=%v", hello, helloPayload)
	}
	sessionStart, sessionPayload, err := sourceclock.ReadFrame(reader, sourceclock.StreamControl)
	if err != nil {
		t.Fatal(err)
	}
	if sessionStart.Codec != sourceclock.ControlSessionStart || sessionStart.Sequence != 1 || len(sessionPayload) != 0 {
		t.Fatalf("session-start header=%#v payload=%v", sessionStart, sessionPayload)
	}
	_, payload, err := sourceclock.ReadFrame(reader, sourceclock.StreamAudio)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, item.payload) {
		t.Fatalf("first audio payload=%v want=%v", payload, item.payload)
	}
}

func TestSendAudioReconnectUsesInjectedDialAndGeneration(t *testing.T) {
	base := time.Unix(0, 0)
	now := base
	connections := make([]*deterministicAudioConn, 0, 2)
	dial := func(string, string) (net.Conn, error) {
		connection := &deterministicAudioConn{now: func() time.Time { return now }, audioHeaderTimes: new([]time.Duration)}
		connections = append(connections, connection)
		return connection, nil
	}
	sleep := func(duration time.Duration) { now = now.Add(duration) }
	scenario := iphonemodel.Scenario{Name: "deterministic-reconnect", Seed: 1, Duration: 70 * time.Millisecond, Events: []iphonemodel.Event{
		{At: 20 * time.Millisecond, Stream: iphonemodel.StreamAudio, Kind: iphonemodel.EventDisconnect, Generation: 1},
		{At: 40 * time.Millisecond, Stream: iphonemodel.StreamAudio, Kind: iphonemodel.EventReconnect, Generation: 2},
	}}
	item := frame{payload: []byte{4, 5, 6}, delta: 10 * time.Millisecond}
	report, err := sendAudioWithDeps("ignored", make([]byte, sourceclock.SessionTokenBytes), []frame{item}, scenario,
		func() time.Time { return now }, sleep, dial)
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 2 || report.Connections != 2 || report.Reconnects != 1 {
		t.Fatalf("connections=%d report=%#v", len(connections), report)
	}
	for index, connection := range connections {
		reader := bytes.NewReader(connection.Bytes())
		if _, _, err := sourceclock.ReadFrame(reader, sourceclock.StreamControl); err != nil {
			t.Fatal(err)
		}
		header, _, err := sourceclock.ReadFrame(reader, sourceclock.StreamControl)
		if err != nil {
			t.Fatal(err)
		}
		wantGeneration := uint32(1)
		if index == 1 {
			wantGeneration = 2
		}
		if header.Codec != sourceclock.ControlSessionStart || header.Sequence != wantGeneration {
			t.Fatalf("connection %d session-start=%#v want generation %d", index, header, wantGeneration)
		}
	}
}

type deterministicAudioConn struct {
	bytes.Buffer
	now              func() time.Time
	audioHeaderTimes *[]time.Duration
}

func (conn *deterministicAudioConn) Write(data []byte) (int, error) {
	if len(data) == int(sourceclock.HeaderSize) {
		if header, err := sourceclock.DecodeHeader(data); err == nil && header.StreamKind == sourceclock.StreamAudio {
			*conn.audioHeaderTimes = append(*conn.audioHeaderTimes, conn.now().Sub(time.Unix(0, 0)))
		}
	}
	return conn.Buffer.Write(data)
}

func (*deterministicAudioConn) Close() error                     { return nil }
func (*deterministicAudioConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*deterministicAudioConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*deterministicAudioConn) SetDeadline(time.Time) error      { return nil }
func (*deterministicAudioConn) SetReadDeadline(time.Time) error  { return nil }
func (*deterministicAudioConn) SetWriteDeadline(time.Time) error { return nil }

func TestSendAudioReconnectsSameGeneration(t *testing.T) {
	generations, report := runAudioReconnectScenario(t, false)
	if len(generations) != 2 || generations[0] != 1 || generations[1] != 1 {
		t.Fatalf("generations=%v report=%#v", generations, report)
	}
	if report.Connections != 2 || report.Reconnects != 1 {
		t.Fatalf("report=%#v", report)
	}
}

func TestSendAudioReconnectsNewGeneration(t *testing.T) {
	generations, report := runAudioReconnectScenario(t, true)
	if len(generations) != 2 || generations[0] != 1 || generations[1] != 2 {
		t.Fatalf("generations=%v report=%#v", generations, report)
	}
	if report.Connections != 2 || report.Reconnects != 1 {
		t.Fatalf("report=%#v", report)
	}
}

func runAudioReconnectScenario(t *testing.T, newGeneration bool) ([]uint32, audioReport) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	generations := make(chan []uint32, 1)
	go func() {
		got := make([]uint32, 0, 2)
		for len(got) < 2 {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				generations <- got
				return
			}
			if _, _, readErr := sourceclock.ReadFrame(conn, sourceclock.StreamControl); readErr != nil {
				_ = conn.Close()
				generations <- got
				return
			}
			header, _, readErr := sourceclock.ReadFrame(conn, sourceclock.StreamControl)
			if readErr != nil {
				_ = conn.Close()
				generations <- got
				return
			}
			got = append(got, header.Sequence)
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}
		generations <- got
	}()

	events := []iphonemodel.Event{
		{At: 20 * time.Millisecond, Stream: iphonemodel.StreamAudio, Kind: iphonemodel.EventDisconnect, Generation: 1},
		{At: 40 * time.Millisecond, Stream: iphonemodel.StreamAudio, Kind: iphonemodel.EventReconnect, Generation: 1},
	}
	if newGeneration {
		events[1].Generation = 2
	}
	scenario := iphonemodel.Scenario{Name: "audio-reconnect-test", Seed: 1, Duration: 80 * time.Millisecond, Events: events}
	item := frame{payload: []byte{1, 2, 3}, delta: 10 * time.Millisecond}
	report, sendErr := sendAudio(listener.Addr().String(), make([]byte, sourceclock.SessionTokenBytes), []frame{item}, scenario)
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	return <-generations, report
}

func TestBuildScenarioRejectsScenarioJSONWithBuiltInMutationConflict(t *testing.T) {
	_, err := buildScenario(options{scenarioJSON: "scenario.json", mutate: true})
	if err == nil {
		t.Fatal("conflicting input was accepted")
	}
}

func TestBuildScenarioLoadsValidatedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.json")
	data := []byte(`{"name":"json","seed":4,"duration":1000000000,"events":[]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := buildScenario(options{scenarioJSON: path})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "json" || got.Seed != 4 || got.Duration != time.Second {
		t.Fatalf("%#v", got)
	}
}

func TestScenarioForCoversAllQualificationScenarios(t *testing.T) {
	for _, name := range scenarioNames {
		spec, err := scenarioFor(name, 5*time.Second)
		if err != nil {
			t.Fatalf("scenarioFor(%q): %v", name, err)
		}
		if spec.name != name || spec.duration != 5*time.Second {
			t.Fatalf("scenarioFor(%q) = %#v", name, spec)
		}
	}
}

func TestScenarioForRejectsUnknownAndNonPositiveDuration(t *testing.T) {
	if _, err := scenarioFor("unknown", time.Second); err == nil {
		t.Fatal("unknown scenario was accepted")
	}
	if _, err := scenarioFor("steady-60", 0); err == nil {
		t.Fatal("zero duration was accepted")
	}
}

func TestScenarioFaultWindows(t *testing.T) {
	spec, err := scenarioFor("video-gap-500ms", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if spec.videoPauseAt != time.Second || spec.videoPauseFor != 500*time.Millisecond {
		t.Fatalf("video gap = %#v", spec)
	}
	spec, err = scenarioFor("both-gap-5s", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if spec.bothPauseAt != time.Second || spec.bothPauseFor != 5*time.Second {
		t.Fatalf("both gap = %#v", spec)
	}
}

type shortWriteConn struct {
	bytes.Buffer
	maxWrite int
	writes   int
}

func (conn *shortWriteConn) Write(data []byte) (int, error) {
	conn.writes++
	if len(data) > conn.maxWrite {
		data = data[:conn.maxWrite]
	}
	return conn.Buffer.Write(data)
}
func (*shortWriteConn) Close() error                     { return nil }
func (*shortWriteConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*shortWriteConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*shortWriteConn) SetDeadline(time.Time) error      { return nil }
func (*shortWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (*shortWriteConn) SetWriteDeadline(time.Time) error { return nil }

func TestWriteFrameCompletesFragmentedShortWrites(t *testing.T) {
	conn := &shortWriteConn{maxWrite: 1}
	payload := []byte{1, 2, 3, 4}
	header := sourceclock.Header{StreamKind: sourceclock.StreamVideo, Codec: sourceclock.CodecH264AnnexBAU, PayloadBytes: uint32(len(payload)), Sequence: 7}
	if err := writeFrame(conn, header, payload, 2); err != nil {
		t.Fatal(err)
	}
	gotHeader, gotPayload, err := sourceclock.ReadFrame(bytes.NewReader(conn.Bytes()), sourceclock.StreamVideo)
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader.Sequence != 7 || !bytes.Equal(gotPayload, payload) || conn.writes < int(sourceclock.HeaderSize)+len(payload) {
		t.Fatalf("header=%#v payload=%v writes=%d", gotHeader, gotPayload, conn.writes)
	}
}

func TestSplitInitialH264ConfigSeparatesParameterSetsFromIDR(t *testing.T) {
	payload := []byte{
		0, 0, 0, 1, 0x09, 0xf0,
		0, 0, 0, 1, 0x67, 0x42,
		0, 0, 0, 1, 0x68, 0xce,
		0, 0, 0, 1, 0x65, 0x88,
	}
	got, err := splitInitialH264Config([]frame{{payload: payload, delta: time.Second / 60}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("split frames=%d, want 3", len(got))
	}
	if want := []byte{0, 0, 0, 1, 0x67, 0x42}; !bytes.Equal(got[0].payload, want) {
		t.Fatalf("first frame=%x, want SPS %x", got[0].payload, want)
	}
	if want := []byte{0, 0, 0, 1, 0x68, 0xce}; !bytes.Equal(got[1].payload, want) {
		t.Fatalf("second frame=%x, want PPS %x", got[1].payload, want)
	}
	if containsConfigNAL(got[2].payload) || !containsKeyNAL(got[2].payload) {
		t.Fatalf("third frame is not IDR-only: %x", got[2].payload)
	}
}

func TestRetainInitialH264ConfigOnlyRemovesLaterParameterSets(t *testing.T) {
	first := frame{payload: []byte{
		0, 0, 0, 1, 0x09, 0xf0,
		0, 0, 0, 1, 0x67, 0x42,
		0, 0, 0, 1, 0x68, 0xce,
		0, 0, 0, 1, 0x65, 0x88,
	}, delta: time.Second / 60}
	laterIDR := frame{payload: []byte{
		0, 0, 0, 1, 0x09, 0xf0,
		0, 0, 0, 1, 0x67, 0x42,
		0, 0, 0, 1, 0x68, 0xce,
		0, 0, 0, 1, 0x65, 0x99,
	}, delta: time.Second / 60}
	laterP := frame{payload: []byte{
		0, 0, 0, 1, 0x09, 0xf0,
		0, 0, 0, 1, 0x41, 0xaa,
	}, delta: time.Second / 60}

	got, err := retainInitialH264ConfigOnly([]frame{first, laterIDR, laterP})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !containsConfigNAL(got[0].payload) {
		t.Fatalf("initial configuration was not retained: %#v", got)
	}
	if containsConfigNAL(got[1].payload) || !containsKeyNAL(got[1].payload) {
		t.Fatalf("later IDR did not retain IDR while removing SPS/PPS: %x", got[1].payload)
	}
	if containsConfigNAL(got[2].payload) || got[1].delta != laterIDR.delta || got[2].delta != laterP.delta {
		t.Fatalf("later frames were not preserved correctly: %#v", got)
	}
}

func TestRetainInitialH264ConfigOnlyRejectsMissingBootstrap(t *testing.T) {
	frames := []frame{{payload: []byte{0, 0, 0, 1, 0x41, 0xaa}, delta: time.Second / 60}}
	if _, err := retainInitialH264ConfigOnly(frames); err == nil {
		t.Fatal("fixture without initial SPS/PPS was accepted")
	}
}

func TestRetainInitialH264ConfigOnlyKeepsSplitSPSAndPPSExactlyOnce(t *testing.T) {
	sps := frame{payload: []byte{0, 0, 0, 1, 0x67, 0x42}, delta: time.Second / 60}
	pps := frame{payload: []byte{0, 0, 0, 1, 0x68, 0xce}, delta: time.Second / 60}
	idr := frame{payload: []byte{0, 0, 0, 1, 0x65, 0x88}, delta: time.Second / 60}
	got, err := retainInitialH264ConfigOnly([]frame{sps, pps, idr, sps, pps, idr})
	if err != nil {
		t.Fatal(err)
	}
	configFrames := 0
	for _, item := range got {
		if containsConfigNAL(item.payload) {
			configFrames++
		}
	}
	if configFrames != 2 {
		t.Fatalf("config frames=%d, want one SPS frame and one PPS frame", configFrames)
	}
	first, hasSPS, hasPPS := retainUnsentH264ConfigNALs(sps.payload, false, false)
	if len(first) == 0 || !hasSPS || hasPPS {
		t.Fatalf("first SPS was not retained: payload=%x sps=%v pps=%v", first, hasSPS, hasPPS)
	}
	second, hasSPS, hasPPS := retainUnsentH264ConfigNALs(pps.payload, true, false)
	if len(second) == 0 || hasSPS || !hasPPS {
		t.Fatalf("split PPS was not retained after SPS: payload=%x sps=%v pps=%v", second, hasSPS, hasPPS)
	}
	duplicate, _, _ := retainUnsentH264ConfigNALs(sps.payload, true, true)
	if len(duplicate) != 0 {
		t.Fatalf("duplicate SPS was retained: %x", duplicate)
	}
}

func TestH264BootstrapRejectsSourceSequencesAtOrBeforeReconnectWatermark(t *testing.T) {
	state := newH264BootstrapState()
	state.beginConnection(10)

	for _, sourceSequence := range []uint64{9, 10} {
		decision := state.decide(h264AccessUnit{sourceSequence: sourceSequence, payload: h264PFrame(0xaa)})
		if !decision.drop {
			t.Fatalf("source sequence %d was accepted at watermark %d: %#v", sourceSequence, state.watermark, decision)
		}
	}
	decision := state.decide(h264AccessUnit{sourceSequence: 11, payload: h264PFrame(0xbb)})
	if !decision.drop {
		t.Fatalf("post-watermark P frame was not held until a complete IDR: %#v", decision)
	}
}

func TestH264BootstrapDropsPostWatermarkPFramesUntilCompleteIDR(t *testing.T) {
	state := newH264BootstrapState()
	state.cacheConfig(h264Config())
	state.beginConnection(3)

	for sourceSequence, payload := range map[uint64][]byte{
		4: h264PFrame(0x41),
		5: h264PFrame(0x42),
	} {
		decision := state.decide(h264AccessUnit{sourceSequence: sourceSequence, payload: payload})
		if !decision.drop {
			t.Fatalf("P frame at source sequence %d escaped bootstrap gate: %#v", sourceSequence, decision)
		}
	}
	decision := state.decide(h264AccessUnit{sourceSequence: 6, remoteNTPNS: 600, payload: h264IDR(0x66)})
	if decision.drop || !decision.bootstrap || len(decision.outputs) != 2 {
		t.Fatalf("complete IDR did not produce a bootstrap pair: %#v", decision)
	}
}

func TestH264BootstrapFirstAcceptedIDRReplaysSessionConfigThenSameFreshIDR(t *testing.T) {
	state := newH264BootstrapState()
	config := h264Config()
	state.cacheConfig(config)
	state.beginConnection(20)
	idr := h264AccessUnit{sourceSequence: 21, remoteNTPNS: 2_100, payload: h264IDR(0x77)}

	decision := state.decide(idr)
	if decision.drop || !decision.bootstrap || len(decision.outputs) != 2 {
		t.Fatalf("decision=%#v", decision)
	}
	if decision.outputs[0].configSource != "session-cache" || !decision.outputs[0].configOnly {
		t.Fatalf("first output was not session-cache CONFIG: %#v", decision.outputs[0])
	}
	if decision.outputs[1].sourceSequence != idr.sourceSequence || !containsKeyNAL(decision.outputs[1].payload) {
		t.Fatalf("second output was not the same fresh IDR: %#v", decision.outputs[1])
	}
	if !bytes.Equal(decision.outputs[0].payload, config) || !bytes.Equal(decision.outputs[1].payload, idr.payload) {
		t.Fatalf("bootstrap payloads changed: %#v", decision.outputs)
	}
}

func TestH264BootstrapConfigSendFailureRemainsPendingForNextConnection(t *testing.T) {
	state := newH264BootstrapState()
	state.cacheConfig(h264Config())
	state.beginConnection(30)

	first := state.decide(h264AccessUnit{sourceSequence: 31, remoteNTPNS: 3_100, payload: h264IDR(0x31)})
	if !first.bootstrap || len(first.outputs) != 2 {
		t.Fatalf("first decision=%#v", first)
	}
	state.commit(first, false)
	state.beginConnection(31)
	second := state.decide(h264AccessUnit{sourceSequence: 32, remoteNTPNS: 3_200, payload: h264IDR(0x32)})
	if !second.bootstrap || len(second.outputs) != 2 {
		t.Fatalf("bootstrap was not retried after CONFIG failure: %#v", second)
	}
	if second.outputs[0].configSource != "session-cache" || second.outputs[1].sourceSequence != 32 {
		t.Fatalf("retry pair=%#v", second.outputs)
	}
}

func TestH264BootstrapReplayDoesNotIncrementInputCounters(t *testing.T) {
	state := newH264BootstrapState()
	state.cacheConfig(h264Config())
	state.beginConnection(40)
	decision := state.decide(h264AccessUnit{sourceSequence: 41, payload: h264IDR(0x41)})
	state.commit(decision, true)
	if state.inputFrames != 1 || state.inputConfigFrames != 0 || state.cacheConfigReplays != 1 || state.postWatermarkIDRs != 1 {
		t.Fatalf("state counters=%#v", state)
	}
}

func h264Config() []byte {
	return []byte{0, 0, 0, 1, 0x67, 0x42, 0, 0, 0, 1, 0x68, 0xce}
}

func h264PFrame(value byte) []byte {
	return []byte{0, 0, 0, 1, 0x41, value}
}

func h264IDR(value byte) []byte {
	return []byte{0, 0, 0, 1, 0x65, value}
}

type blockingFailingTraceWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (writer *blockingFailingTraceWriter) Write(_ []byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	<-writer.release
	return 0, io.ErrClosedPipe
}

func (*blockingFailingTraceWriter) Close() error { return nil }

func TestVideoTraceRecordingNeverBlocksTheMediaSender(t *testing.T) {
	writer := &blockingFailingTraceWriter{started: make(chan struct{}), release: make(chan struct{})}
	sink := newVideoTraceSinkWithWriter(writer, 1)
	sink.record(videoTraceEvent{Event: "connection-open", Connection: 1})
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("trace writer did not start")
	}
	recorded := make(chan struct{})
	go func() {
		for sequence := 0; sequence < 1000; sequence++ {
			sink.record(videoTraceEvent{Event: "video-frame", Sequence: uint32(sequence + 1)})
		}
		close(recorded)
	}()
	select {
	case <-recorded:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("trace backpressure blocked the media sender")
	}
	close(writer.release)
	if err := sink.close(); err == nil {
		t.Fatal("trace writer failure was not retained for diagnostics")
	}
}

func TestSendVideoPersistsConnectionAndBootstrapTrace(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		_, _ = io.Copy(io.Discard, conn)
		done <- conn.Close()
	}()

	tracePath := filepath.Join(t.TempDir(), "video-trace.jsonl")
	item := frame{payload: []byte{
		0, 0, 0, 1, 0x09, 0xf0,
		0, 0, 0, 1, 0x67, 0x42,
		0, 0, 0, 1, 0x68, 0xce,
		0, 0, 0, 1, 0x65, 0x88,
	}, delta: 10 * time.Millisecond}
	scenario := iphonemodel.Scenario{Name: "video-trace-test", Seed: 1, Duration: 40 * time.Millisecond}
	report, err := sendVideo(listener.Addr().String(), make([]byte, sourceclock.SessionTokenBytes),
		[]frame{item}, []frame{item}, 100, scenario, videoSendOptions{initialConfigOnly: true, tracePath: tracePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"event":"connection-open"`) ||
		!strings.Contains(text, `"event":"video-frame"`) ||
		!strings.Contains(text, `"config":true`) || !strings.Contains(text, `"keyframe":true`) {
		t.Fatalf("trace=%s", text)
	}
	if report.ConfigSent != 1 || report.KeyframesSent < 1 {
		t.Fatalf("report=%#v", report)
	}
}

func TestSourceVideoFlagsDoNotMarkConfigurationOnlyPacketAsKeyframe(t *testing.T) {
	configuration := []byte{0, 0, 0, 1, 0x67, 0x42, 0, 0, 0, 1, 0x68, 0xce}
	if got := sourceVideoFlags(configuration); got != sourceclock.FlagConfig {
		t.Fatalf("configuration flags=%d, want %d", got, sourceclock.FlagConfig)
	}
	keyframe := []byte{0, 0, 0, 1, 0x65, 0x88}
	if got := sourceVideoFlags(keyframe); got != sourceclock.FlagKeyframe {
		t.Fatalf("keyframe flags=%d, want %d", got, sourceclock.FlagKeyframe)
	}
}

func TestSendVideoMarksFirstRotatedFrame(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	flags := make(chan uint16, 32)
	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer conn.Close()
		if _, _, readErr := sourceclock.ReadFrame(conn, sourceclock.StreamControl); readErr != nil {
			done <- readErr
			return
		}
		if _, _, readErr := sourceclock.ReadFrame(conn, sourceclock.StreamControl); readErr != nil {
			done <- readErr
			return
		}
		for {
			header, _, readErr := sourceclock.ReadFrame(conn, sourceclock.StreamVideo)
			if readErr != nil {
				if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
					done <- nil
				} else {
					done <- readErr
				}
				return
			}
			flags <- header.Flags
		}
	}()

	scenario := iphonemodel.Scenario{Name: "rotation", Seed: 1, Duration: 100 * time.Millisecond, Events: []iphonemodel.Event{{
		At: 25 * time.Millisecond, Stream: iphonemodel.StreamVideo, Kind: iphonemodel.EventRotation, Generation: 1,
	}}}
	item := frame{payload: []byte{0, 0, 0, 1, 0x65}, delta: 10 * time.Millisecond}
	report, err := sendVideo(listener.Addr().String(), make([]byte, sourceclock.SessionTokenBytes), []frame{item}, []frame{item}, 100, scenario)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(flags)
	want := sourceclock.FlagConfig | sourceclock.FlagDiscontinuity | sourceclock.FlagKeyframe
	found := false
	for got := range flags {
		if got&want == want {
			found = true
		}
	}
	if !found || report.Events != 1 || report.Sent == 0 {
		t.Fatalf("rotated flags missing; report=%#v", report)
	}
}

func TestSendVideoReconnectsSameGeneration(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan int, 1)
	go func() {
		count := 0
		for count < 2 {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				accepted <- count
				return
			}
			count++
			go func() { _, _ = io.Copy(io.Discard, conn); _ = conn.Close() }()
		}
		accepted <- count
	}()
	scenario := iphonemodel.Scenario{Name: "reconnect", Seed: 1, Duration: 100 * time.Millisecond, Events: []iphonemodel.Event{
		{At: 20 * time.Millisecond, Stream: iphonemodel.StreamVideo, Kind: iphonemodel.EventDisconnect, Generation: 1},
		{At: 40 * time.Millisecond, Stream: iphonemodel.StreamVideo, Kind: iphonemodel.EventReconnect, Generation: 1},
	}}
	item := frame{payload: []byte{0, 0, 0, 1, 0x65}, delta: 10 * time.Millisecond}
	report, err := sendVideo(listener.Addr().String(), make([]byte, sourceclock.SessionTokenBytes), []frame{item}, []frame{item}, 100, scenario)
	if err != nil {
		t.Fatal(err)
	}
	if count := <-accepted; count != 2 || report.Reconnects != 1 {
		t.Fatalf("accepted=%d report=%#v", count, report)
	}
}

func TestSendVideoPublisherRestartInitialConfigOnlyBootstrapsSecondConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	secondConnection := make(chan []sourceclock.Header, 1)
	serverErr := make(chan error, 1)
	go func() {
		first, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		if _, _, err := sourceclock.ReadFrame(first, sourceclock.StreamControl); err != nil {
			serverErr <- err
			return
		}
		if _, _, err := sourceclock.ReadFrame(first, sourceclock.StreamControl); err != nil {
			serverErr <- err
			return
		}
		if _, _, err := sourceclock.ReadFrame(first, sourceclock.StreamVideo); err != nil {
			serverErr <- err
			return
		}
		if tcp, ok := first.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = first.Close()

		second, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer second.Close()
		if _, _, err := sourceclock.ReadFrame(second, sourceclock.StreamControl); err != nil {
			serverErr <- err
			return
		}
		if _, _, err := sourceclock.ReadFrame(second, sourceclock.StreamControl); err != nil {
			serverErr <- err
			return
		}
		headers := make([]sourceclock.Header, 0, 2)
		for len(headers) < 2 {
			header, _, readErr := sourceclock.ReadFrame(second, sourceclock.StreamVideo)
			if readErr != nil {
				serverErr <- readErr
				return
			}
			headers = append(headers, header)
		}
		secondConnection <- headers
		_, _ = io.Copy(io.Discard, second)
		serverErr <- nil
	}()

	configAndIDR := frame{payload: append(h264Config(), h264IDR(0x88)...), delta: 10 * time.Millisecond}
	pFrame := frame{payload: h264PFrame(0x99), delta: 10 * time.Millisecond}
	scenario := iphonemodel.Scenario{Name: "publisher-restart-initial-config-only", Duration: 120 * time.Millisecond}
	report, err := sendVideo(listener.Addr().String(), make([]byte, sourceclock.SessionTokenBytes),
		[]frame{configAndIDR, pFrame}, []frame{configAndIDR, pFrame}, 100, scenario,
		videoSendOptions{initialConfigOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	headers := <-secondConnection
	if len(headers) != 2 {
		t.Fatalf("second connection headers=%#v", headers)
	}
	if headers[0].Flags&sourceclock.FlagConfig == 0 || headers[0].Flags&sourceclock.FlagKeyframe != 0 {
		t.Fatalf("second connection did not start with CONFIG-only: %#v", headers[0])
	}
	if headers[1].Flags&sourceclock.FlagKeyframe == 0 || headers[1].Flags&sourceclock.FlagConfig != 0 {
		t.Fatalf("second connection did not continue with fresh IDR: %#v", headers[1])
	}
	if report.Connections != 2 || report.Reconnects != 1 || report.ConfigSent != 2 || report.CacheConfigReplays != 1 || report.PostWatermarkIDRs != 1 {
		t.Fatalf("report=%#v", report)
	}
	if report.ReconnectWatermarkSourceSequence == 0 || report.FirstPostWatermarkIDRSourceSequence <= report.ReconnectWatermarkSourceSequence {
		t.Fatalf("watermark report=%#v", report)
	}
}

func TestSendVideoRotatesAfterReconnectBeforeBootstrapTrace(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	type capture struct {
		headers  []sourceclock.Header
		payloads [][]byte
	}
	secondConnection := make(chan capture, 1)
	serverErr := make(chan error, 1)
	go func() {
		first, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		if _, _, err := sourceclock.ReadFrame(first, sourceclock.StreamControl); err != nil {
			serverErr <- err
			return
		}
		if _, _, err := sourceclock.ReadFrame(first, sourceclock.StreamControl); err != nil {
			serverErr <- err
			return
		}
		if _, _, err := sourceclock.ReadFrame(first, sourceclock.StreamVideo); err != nil {
			serverErr <- err
			return
		}
		if tcp, ok := first.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = first.Close()

		second, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer second.Close()
		if _, _, err := sourceclock.ReadFrame(second, sourceclock.StreamControl); err != nil {
			serverErr <- err
			return
		}
		if _, _, err := sourceclock.ReadFrame(second, sourceclock.StreamControl); err != nil {
			serverErr <- err
			return
		}
		got := capture{headers: make([]sourceclock.Header, 0, 2), payloads: make([][]byte, 0, 2)}
		for len(got.headers) < 2 {
			header, payload, readErr := sourceclock.ReadFrame(second, sourceclock.StreamVideo)
			if readErr != nil {
				serverErr <- readErr
				return
			}
			got.headers = append(got.headers, header)
			got.payloads = append(got.payloads, payload)
		}
		secondConnection <- got
		_, _ = io.Copy(io.Discard, second)
		serverErr <- nil
	}()

	initial := frame{payload: append(h264Config(), h264IDR(0x88)...), delta: 10 * time.Millisecond}
	rotated := frame{payload: append(h264Config(), h264IDR(0x77)...), delta: 10 * time.Millisecond}
	pFrame := frame{payload: h264PFrame(0x99), delta: 10 * time.Millisecond}
	tracePath := filepath.Join(t.TempDir(), "video-trace.jsonl")
	scenario := iphonemodel.Scenario{Name: "publisher-restart-initial-config-only", Duration: 120 * time.Millisecond}
	report, err := sendVideo(listener.Addr().String(), make([]byte, sourceclock.SessionTokenBytes),
		[]frame{initial, pFrame}, []frame{rotated}, 100, scenario,
		videoSendOptions{initialConfigOnly: true, rotateAfterReconnect: true, tracePath: tracePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	got := <-secondConnection
	if len(got.payloads) != 2 || !bytes.Equal(got.payloads[0], h264Config()) || !bytes.Equal(got.payloads[1], h264IDR(0x77)) {
		t.Fatalf("second connection payloads=%x", got.payloads)
	}
	if got.headers[0].Flags&sourceclock.FlagConfig == 0 || got.headers[1].Flags&sourceclock.FlagKeyframe == 0 {
		t.Fatalf("second connection headers=%#v", got.headers)
	}
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if strings.Count(trace, `"event":"rotation-after-reconnect","connection":2`) != 1 {
		t.Fatalf("rotation trace count=%d trace=%s", strings.Count(trace, `"event":"rotation-after-reconnect","connection":2`), trace)
	}
	openIndex := strings.Index(trace, `"event":"connection-open","connection":2`)
	rotationIndex := strings.Index(trace, `"event":"rotation-after-reconnect","connection":2`)
	configIndex := strings.Index(trace, `"event":"bootstrap_config","connection":2`)
	idrIndex := strings.Index(trace, `"event":"post_watermark_idr","connection":2`)
	if openIndex < 0 || rotationIndex <= openIndex || configIndex <= rotationIndex || idrIndex <= configIndex {
		t.Fatalf("trace order is invalid: %s", trace)
	}
	if report.Connections != 2 || report.Reconnects != 1 || report.CacheConfigReplays != 1 || report.PostWatermarkIDRs != 1 {
		t.Fatalf("report=%#v", report)
	}
}
