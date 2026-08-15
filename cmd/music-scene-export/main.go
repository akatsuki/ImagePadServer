package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"imagepadserver/internal/video"
)

func main() {
	input := flag.String("input", "", "audio input path")
	output := flag.String("output", "", "shared scene JSON output path")
	width := flag.Uint("width", 640, "scene width")
	height := flag.Uint("height", 360, "scene height")
	fps := flag.Uint("fps", 30, "scene FPS")
	frames := flag.Uint("frames", 0, "frame count; default derives from analyzed duration")
	pcmSamples := flag.Uint("pcm-samples", 4096, "shared PCM window sample count")
	sampleRate := flag.Uint("sample-rate", 44100, "shared PCM sample rate")
	title := flag.String("title", "", "metadata title")
	artist := flag.String("artist", "", "metadata artist")
	album := flag.String("album", "", "metadata album")
	artwork := flag.String("artwork", "", "artwork image path")
	ffmpeg := flag.String("ffmpeg", "", "ffmpeg executable; default uses the managed binary")
	flag.Parse()

	if *input == "" || *output == "" {
		fatal("--input and --output are required")
	}
	if _, err := os.Stat(*input); err != nil {
		fatal(fmt.Sprintf("input: %v", err))
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		fatal(fmt.Sprintf("create output directory: %v", err))
	}

	ff := *ffmpeg
	if ff == "" {
		var err error
		ff, err = video.EnsureFFmpeg()
		if err != nil {
			fatal(fmt.Sprintf("ffmpeg: %v", err))
		}
	}
	analysis, err := video.AnalyzeAudioForKind(context.Background(), ff, *input, video.SourceMusic)
	if err != nil {
		fatal(fmt.Sprintf("analyze audio: %v", err))
	}
	fpsValue := uint32(*fps)
	if fpsValue == 0 {
		fatal("--fps must be non-zero")
	}
	frameCount := uint32(*frames)
	if frameCount == 0 {
		frameCount = uint32(math.Max(1, math.Ceil(analysis.Duration*float64(fpsValue))))
	}
	inputSpec := video.AudioRenderInput{
		SourcePath: *input,
		Kind:       video.SourceMusic,
		Metadata:   video.AudioMetadata{Title: *title, Artist: *artist, Album: *album},
		ArtworkPath: *artwork,
		Analysis:   analysis,
	}
	doc, err := video.NewMusicSceneDocument(
		inputSpec,
		uint32(*width),
		uint32(*height),
		fpsValue,
		frameCount,
		uint32(*pcmSamples),
		uint32(*sampleRate),
	)
	if err != nil {
		fatal(fmt.Sprintf("build scene document: %v", err))
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		fatal(fmt.Sprintf("marshal scene document: %v", err))
	}
	if err := os.WriteFile(*output, append(encoded, '\n'), 0644); err != nil {
		fatal(fmt.Sprintf("write scene document: %v", err))
	}
	hash := sha256.Sum256(encoded)
	fmt.Printf("scene_document_written=%s schema=%d frames=%d size=%dx%d fps=%d sha256=%s\n", *output, doc.Schema, doc.Frames, doc.Width, doc.Height, doc.FPS, hex.EncodeToString(hash[:]))
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}


