package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"imagepadserver/internal/ytdlplab"
)

func main() {
	var opts ytdlplab.Options
	flag.StringVar(&opts.URL, "url", "", "URL to inspect")
	flag.StringVar(&opts.YTDLPPath, "yt-dlp", "", "path to yt-dlp executable")
	flag.BoolVar(&opts.UseCookies, "cookies", false, "pass --cookies to yt-dlp")
	flag.StringVar(&opts.CookiePath, "cookie-path", defaultCookiePath(), "Netscape cookie file path")
	flag.StringVar(&opts.OutputDir, "out", filepath.Join(".dev", "ytdlplab"), "output directory")
	flag.StringVar(&opts.DownieAppPath, "downie-app", "", "optional Downie 4.app path to scan")
	flag.StringVar(&opts.DownieRunnerPath, "downie-runner", "", "optional external runner script for Downie black-box checks")
	flag.IntVar(&opts.TimeoutSeconds, "timeout", 90, "timeout per strategy in seconds")
	flag.Parse()

	if opts.URL == "" && flag.NArg() > 0 {
		opts.URL = flag.Arg(0)
	}
	if opts.UseCookies && opts.CookiePath == "" {
		fmt.Fprintln(os.Stderr, "--cookies requires --cookie-path")
		os.Exit(2)
	}

	report, err := ytdlplab.Run(context.Background(), opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := "report-" + time.Now().UTC().Format("20060102T150405Z") + ".json"
	outPath := filepath.Join(opts.OutputDir, name)
	if err := ytdlplab.WriteReport(outPath, report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	summary := map[string]interface{}{
		"report":        outPath,
		"ytDLPVersion":  report.YTDLPVersion,
		"results":       len(report.Results),
		"downieScanned": len(report.DownieResources),
	}
	_ = json.NewEncoder(os.Stdout).Encode(summary)
}

func defaultCookiePath() string {
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "ImagePadServer", "cookies", "yt-dlp-cookies.txt")
	}
	return ""
}
