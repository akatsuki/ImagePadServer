package ytdlplab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	ClassOK                     = "ok"
	ClassBotCheck               = "youtube_bot_check"
	ClassFormatUnavailable      = "youtube_format_unavailable"
	ClassStoryboardOnly         = "youtube_storyboard_only"
	ClassPOTokenMissing         = "youtube_po_token_missing"
	ClassSABR                   = "youtube_sabr"
	ClassNChallenge             = "youtube_n_challenge"
	ClassImpersonateUnavailable = "yt_dlp_impersonate_unavailable"
	ClassHTTP403                = "http_403"
	ClassRateLimited            = "rate_limited"
	ClassNetwork                = "network"
	ClassUnknown                = "unknown"

	AppFormatSelector = "bv*[height<=1080]+ba/b[height<=1080]/best[height<=1080]/best"
)

var resolutionRe = regexp.MustCompile(`(?i)(\d{3,5})x(\d{3,5})`)

type Options struct {
	YTDLPPath        string `json:"ytDLPPath,omitempty"`
	URL              string `json:"url,omitempty"`
	UseCookies       bool   `json:"useCookies,omitempty"`
	CookiePath       string `json:"cookiePath,omitempty"`
	OutputDir        string `json:"outputDir,omitempty"`
	DownieAppPath    string `json:"downieAppPath,omitempty"`
	DownieRunnerPath string `json:"downieRunnerPath,omitempty"`
	TimeoutSeconds   int    `json:"timeoutSeconds,omitempty"`
}

type Strategy struct {
	Name  string   `json:"name"`
	Probe string   `json:"probe"`
	Args  []string `json:"args"`
}

type RunResult struct {
	Strategy   string       `json:"strategy"`
	Probe      string       `json:"probe"`
	Args       []string     `json:"args"`
	ExitCode   int          `json:"exitCode"`
	Class      string       `json:"class"`
	Format     *FormatStats `json:"format,omitempty"`
	DurationMS int64        `json:"durationMs"`
	Stdout     string       `json:"stdout"`
	Stderr     string       `json:"stderr"`
}

type FormatStats struct {
	TotalNonStoryboard int `json:"totalNonStoryboard"`
	Storyboard         int `json:"storyboard"`
	Video              int `json:"video"`
	Audio              int `json:"audio"`
	Combined           int `json:"combined"`
	MaxHeight          int `json:"maxHeight"`
}

type DownieResource struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind"`
}

type Report struct {
	GeneratedAt     time.Time         `json:"generatedAt"`
	URL             string            `json:"url,omitempty"`
	YTDLPPath       string            `json:"ytDLPPath,omitempty"`
	YTDLPVersion    string            `json:"ytDLPVersion,omitempty"`
	Results         []RunResult       `json:"results,omitempty"`
	DownieResources []DownieResource  `json:"downieResources,omitempty"`
	DownieRun       *RunResult        `json:"downieRun,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

func Classify(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "sign in to confirm") && strings.Contains(lower, "not a bot"):
		return ClassBotCheck
	case strings.Contains(lower, "requested format is not available") || strings.Contains(lower, "only images are available"):
		return ClassFormatUnavailable
	case looksStoryboardOnly(lower):
		return ClassStoryboardOnly
	case strings.Contains(lower, "po token") || strings.Contains(lower, "potoken"):
		return ClassPOTokenMissing
	case strings.Contains(lower, "sabr"):
		return ClassSABR
	case strings.Contains(lower, "n challenge"):
		return ClassNChallenge
	case strings.Contains(lower, "impersonate target") && strings.Contains(lower, "is not available"):
		return ClassImpersonateUnavailable
	case strings.Contains(lower, "http error 403") || strings.Contains(lower, " forbidden"):
		return ClassHTTP403
	case strings.Contains(lower, "http error 429") || strings.Contains(lower, "too many requests"):
		return ClassRateLimited
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "tls handshake") || strings.Contains(lower, "no such host"):
		return ClassNetwork
	case strings.TrimSpace(text) == "":
		return ClassOK
	default:
		return ClassUnknown
	}
}

func looksStoryboardOnly(lower string) bool {
	if !strings.Contains(lower, "available formats") || !strings.Contains(lower, "storyboard") {
		return false
	}
	hasStoryboard := false
	for _, line := range strings.Split(lower, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "sb") && strings.Contains(line, "storyboard") {
			hasStoryboard = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "id ") {
			continue
		}
		if strings.Contains(line, " | ") && !strings.Contains(line, "storyboard") {
			return false
		}
	}
	return hasStoryboard
}

func BuildStrategies(opts Options) []Strategy {
	common := []string{"--no-playlist", "--no-warnings"}
	if opts.UseCookies && opts.CookiePath != "" {
		common = append([]string{"--cookies", opts.CookiePath}, common...)
	}
	with := func(probe, name string, base []string, extra ...string) Strategy {
		args := append([]string{}, base...)
		args = append(args, extra...)
		return Strategy{Name: name, Probe: probe, Args: args}
	}
	listBase := append(append([]string{}, common...), "--list-formats")
	selectBase := append(append([]string{}, common...),
		"--simulate",
		"--no-progress",
		"--max-filesize", "2G",
		"-f", AppFormatSelector,
		"--merge-output-format", "mp4",
		"--concurrent-fragments", "4",
		"--print", "%(id)s\t%(title)s\t%(format_id)s\t%(format)s",
	)
	clients := []struct {
		name string
		args []string
	}{
		{name: "default"},
		{name: "web-safari-android-vr", args: []string{"--extractor-args", "youtube:player_client=web,web_safari,android_vr"}},
		{name: "mweb", args: []string{"--extractor-args", "youtube:player_client=mweb"}},
		{name: "tv-default", args: []string{"--extractor-args", "youtube:player_client=default,tv"}},
		{name: "ios-android-web", args: []string{"--extractor-args", "youtube:player_client=ios,android,web"}},
	}
	strategies := make([]Strategy, 0, len(clients)*2)
	for _, client := range clients {
		strategies = append(strategies, with("list-formats", client.name, listBase, client.args...))
		strategies = append(strategies, with("app-selector-simulate", client.name, selectBase, client.args...))
	}
	for _, target := range []string{"safari", "chrome", "firefox"} {
		strategies = append(strategies, with("app-exact-simulate", "impersonate-"+target, selectBase,
			"--impersonate", target,
			"--extractor-args", "youtube:player_client=web,web_safari,android_vr",
		))
	}
	return strategies
}

func Run(ctx context.Context, opts Options) (Report, error) {
	if opts.URL == "" {
		return Report{}, errors.New("url is required")
	}
	if _, err := url.ParseRequestURI(opts.URL); err != nil {
		return Report{}, fmt.Errorf("invalid url: %w", err)
	}
	ytdlp := opts.YTDLPPath
	if ytdlp == "" {
		var err error
		ytdlp, err = exec.LookPath("yt-dlp")
		if err != nil {
			return Report{}, errors.New("yt-dlp not found; pass --yt-dlp")
		}
	}
	report := Report{
		GeneratedAt:  time.Now().UTC(),
		URL:          opts.URL,
		YTDLPPath:    ytdlp,
		YTDLPVersion: ytdlpVersion(ctx, ytdlp),
		Metadata: map[string]string{
			"os":   runtime.GOOS,
			"arch": runtime.GOARCH,
		},
	}
	timeout := time.Duration(opts.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	for _, strategy := range BuildStrategies(opts) {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		result := runYTDLP(runCtx, ytdlp, strategy, opts.URL)
		cancel()
		report.Results = append(report.Results, result)
	}
	if opts.DownieAppPath != "" {
		resources, err := ScanDownieResources(opts.DownieAppPath)
		if err == nil {
			report.DownieResources = resources
		} else {
			report.Metadata["downieScanError"] = err.Error()
		}
	}
	if opts.DownieRunnerPath != "" {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		result := runDownieRunner(runCtx, opts.DownieRunnerPath, opts.URL)
		cancel()
		report.DownieRun = &result
	}
	return report, nil
}

func runYTDLP(ctx context.Context, ytdlp string, strategy Strategy, rawURL string) RunResult {
	args := append([]string{}, strategy.Args...)
	args = append(args, rawURL)
	start := time.Now()
	cmd := exec.CommandContext(ctx, ytdlp, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	combined := stderr.String()
	if combined == "" {
		combined = stdout.String()
	}
	class := Classify(combined)
	if err == nil && class == ClassUnknown {
		class = ClassOK
	}
	return RunResult{
		Strategy:   strategy.Name,
		Probe:      strategy.Probe,
		Args:       args,
		ExitCode:   exitCode,
		Class:      class,
		Format:     parseFormatStats(stdout.String()),
		DurationMS: time.Since(start).Milliseconds(),
		Stdout:     trimForReport(stdout.String()),
		Stderr:     trimForReport(stderr.String()),
	}
}

func ytdlpVersion(ctx context.Context, ytdlp string) string {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, ytdlp, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func ScanDownieResources(appPath string) ([]DownieResource, error) {
	base := filepath.Join(appPath, "Contents", "Frameworks", "DownieCore.framework", "Versions", "A", "Resources")
	targets := map[string]string{
		"YouTubeJSChallenge.html":                       "youtube_js_challenge",
		"POTokenGenerator.html":                         "po_token_generator",
		"POTokenGeneratorJavaScriptBridge.js":           "po_token_bridge",
		"JavaScriptExecutorBridge.html":                 "js_executor",
		"JavaScriptExecutorBridge.js":                   "js_executor_bridge",
		"CustomIntegrationExecutionJavaScriptBridge.js": "custom_integration_bridge",
	}
	var resources []DownieResource
	for name, kind := range targets {
		path := filepath.Join(base, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		sum, err := fileSHA256(path)
		if err != nil {
			continue
		}
		resources = append(resources, DownieResource{
			Path:   path,
			Size:   info.Size(),
			SHA256: sum,
			Kind:   kind,
		})
	}
	return resources, nil
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func runDownieRunner(ctx context.Context, runnerPath, rawURL string) RunResult {
	start := time.Now()
	cmd := exec.CommandContext(ctx, runnerPath, rawURL)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	combined := stderr.String()
	if combined == "" {
		combined = stdout.String()
	}
	return RunResult{
		Strategy:   "downie-runner",
		Probe:      "external",
		Args:       []string{runnerPath, rawURL},
		ExitCode:   exitCode,
		Class:      Classify(combined),
		DurationMS: time.Since(start).Milliseconds(),
		Stdout:     trimForReport(stdout.String()),
		Stderr:     trimForReport(stderr.String()),
	}
}

func parseFormatStats(text string) *FormatStats {
	if !strings.Contains(strings.ToLower(text), "available formats") {
		return nil
	}
	stats := &FormatStats{}
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" || strings.HasPrefix(lower, "[") || strings.HasPrefix(lower, "-") || strings.HasPrefix(lower, "id ") {
			continue
		}
		if strings.Contains(lower, "storyboard") {
			stats.Storyboard++
			continue
		}
		if !strings.Contains(lower, " | ") {
			continue
		}
		stats.TotalNonStoryboard++
		if strings.Contains(lower, "audio only") {
			stats.Audio++
		} else if strings.Contains(lower, "video only") {
			stats.Video++
		} else {
			stats.Combined++
		}
		if matches := resolutionRe.FindStringSubmatch(lower); len(matches) == 3 {
			if height, err := strconv.Atoi(matches[2]); err == nil && height > stats.MaxHeight {
				stats.MaxHeight = height
			}
		}
	}
	return stats
}

func trimForReport(text string) string {
	const max = 12000
	if len(text) <= max {
		return text
	}
	return text[:max] + "\n...[truncated]"
}

func WriteReport(path string, report Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0600)
}
