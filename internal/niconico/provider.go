package niconico

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultWatchBaseURL    = "https://www.nicovideo.jp"
	defaultCommentHost     = "public.nvcomment.nicovideo.jp"
	maxProviderJSON        = 32 << 20
	maxRequestAttempts     = 3
	providerRequestTimeout = 20 * time.Second
	providerFetchTimeout   = 60 * time.Second
)

// Client retrieves a NicoNico watch response and its nvComment threads.
// CommentServerHosts is intentionally allowlisted; values from the watch
// response are never used as arbitrary outbound destinations.
type Client struct {
	HTTPClient         *http.Client
	WatchBaseURL       string
	CommentServerHosts map[string]struct{}
}

func NewClient(httpClient *http.Client, commentServerHosts []string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	hosts := make(map[string]struct{}, len(commentServerHosts))
	if len(commentServerHosts) == 0 {
		hosts[defaultCommentHost] = struct{}{}
	} else {
		for _, host := range commentServerHosts {
			if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
				hosts[host] = struct{}{}
			}
		}
	}
	return &Client{HTTPClient: httpClient, WatchBaseURL: defaultWatchBaseURL, CommentServerHosts: hosts}
}

func (c *Client) Fetch(ctx context.Context, videoID string) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, providerFetchTimeout)
		defer cancel()
	}
	if videoID == "" {
		return Snapshot{}, fmt.Errorf("niconico: empty video ID")
	}
	watchBase, err := url.Parse(strings.TrimRight(c.WatchBaseURL, "/"))
	if err != nil || watchBase.Scheme != "https" || watchBase.Hostname() == "" {
		return Snapshot{}, fmt.Errorf("niconico: invalid watch base URL")
	}
	trackID := "AAAAAAAAAA_" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	watchURL := *watchBase
	watchURL.Path = "/api/watch/v3_guest/" + url.PathEscape(videoID)
	query := watchURL.Query()
	query.Set("actionTrackId", trackID)
	watchURL.RawQuery = query.Encode()
	watchData, err := c.getJSON(ctx, watchURL.String(), map[string]string{
		"X-Frontend-Id":      "6",
		"X-Frontend-Version": "0",
		"Referer":            "https://www.nicovideo.jp/watch/" + url.PathEscape(videoID),
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("niconico: watch request: %w", err)
	}
	if status := responseStatus(watchData); status != 0 && status != http.StatusOK {
		return Snapshot{}, fmt.Errorf("niconico: watch API status %d", status)
	}
	nvComment, ok := findObjectByKey(watchData, "nvComment")
	if !ok {
		return Snapshot{}, fmt.Errorf("niconico: watch response has no nvComment")
	}
	server, _ := nvComment["server"].(string)
	params, ok := nvComment["params"]
	if !ok {
		return Snapshot{}, fmt.Errorf("niconico: nvComment params missing")
	}
	threadKey, _ := nvComment["threadKey"].(string)
	if threadKey == "" {
		return Snapshot{}, fmt.Errorf("niconico: nvComment threadKey missing")
	}
	commentURL, err := c.validateCommentServer(server)
	if err != nil {
		return Snapshot{}, err
	}
	payload, err := json.Marshal(map[string]any{"additionals": map[string]any{}, "params": params, "threadKey": threadKey})
	if err != nil {
		return Snapshot{}, fmt.Errorf("niconico: encode threads request: %w", err)
	}
	threadsData, err := c.postJSON(ctx, strings.TrimRight(commentURL.String(), "/")+"/v1/threads", payload, map[string]string{
		"Content-Type":       "text/plain;charset=UTF-8",
		"Origin":             "https://www.nicovideo.jp",
		"Referer":            "https://www.nicovideo.jp/",
		"X-Client-Os-Type":   "others",
		"X-Frontend-Id":      "6",
		"X-Frontend-Version": "0",
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("niconico: comments request: %w", err)
	}
	if status := responseStatus(threadsData); status != 0 && status != http.StatusOK {
		return Snapshot{}, fmt.Errorf("niconico: comments API status %d", status)
	}
	threads, err := parseThreads(threadsData)
	if err != nil {
		return Snapshot{}, err
	}
	selected := make([]string, 0, len(threads))
	seenFork := make(map[string]struct{})
	for _, thread := range threads {
		if thread.Fork != "main" && thread.Fork != "owner" {
			continue
		}
		if _, ok := seenFork[thread.Fork]; !ok {
			selected = append(selected, thread.Fork)
			seenFork[thread.Fork] = struct{}{}
		}
	}
	normalized, err := NormalizeSnapshot(Snapshot{
		VideoID:         videoID,
		AcquiredAt:      time.Now().UTC().Format(time.RFC3339Nano),
		ProviderVersion: "nvcomment-v1",
		SelectedForks:   selected,
		Threads:         threads,
	})
	if err != nil {
		return Snapshot{}, err
	}
	return normalized, nil
}

func (c *Client) validateCommentServer(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("niconico: invalid comment host")
	}
	host := strings.ToLower(u.Hostname())
	if _, ok := c.CommentServerHosts[host]; !ok {
		return nil, fmt.Errorf("niconico: unapproved comment host %q", host)
	}
	if u.Port() != "" && u.Port() != "443" && host == defaultCommentHost {
		return nil, fmt.Errorf("niconico: invalid comment host port")
	}
	return u, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, headers map[string]string) (map[string]any, error) {
	return c.doJSON(ctx, http.MethodGet, endpoint, nil, headers)
}

func (c *Client) postJSON(ctx context.Context, endpoint string, body []byte, headers map[string]string) (map[string]any, error) {
	return c.doJSON(ctx, http.MethodPost, endpoint, body, headers)
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, body []byte, headers map[string]string) (map[string]any, error) {
	for attempt := 0; attempt < maxRequestAttempts; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, providerRequestTimeout)
		req, err := http.NewRequestWithContext(requestCtx, method, endpoint, bytes.NewReader(body))
		if err != nil {
			cancel()
			return nil, err
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			cancel()
			return nil, err
		}
		limited := io.LimitReader(resp.Body, maxProviderJSON+1)
		data, readErr := io.ReadAll(limited)
		resp.Body.Close()
		cancel()
		if readErr != nil {
			return nil, readErr
		}
		if len(data) > maxProviderJSON {
			return nil, fmt.Errorf("niconico: response exceeds %d bytes", maxProviderJSON)
		}
		if isTransientStatus(resp.StatusCode) && attempt+1 < maxRequestAttempts {
			if err := waitRetry(ctx, retryDelay(resp.Header.Get("Retry-After"), attempt)); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		var decoded map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("decode JSON: %w", err)
		}
		return decoded, nil
	}
	return nil, fmt.Errorf("niconico: request attempts exhausted")
}

func isTransientStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500 && status <= 599
}

func retryDelay(retryAfter string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
		if seconds > 5 {
			seconds = 5
		}
		return time.Duration(seconds) * time.Second
	}
	delay := 100 * time.Millisecond
	for i := 0; i < attempt; i++ {
		delay *= 2
	}
	return delay
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func responseStatus(data map[string]any) int {
	meta, _ := data["meta"].(map[string]any)
	return numberInt(meta["status"])
}

func findObjectByKey(value any, key string) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			if name == key {
				if object, ok := child.(map[string]any); ok {
					return object, true
				}
			}
			if object, ok := findObjectByKey(child, key); ok {
				return object, true
			}
		}
	case []any:
		for _, child := range typed {
			if object, ok := findObjectByKey(child, key); ok {
				return object, true
			}
		}
	}
	return nil, false
}

func parseThreads(data map[string]any) ([]Thread, error) {
	dataObject, _ := data["data"].(map[string]any)
	threadsValue, ok := dataObject["threads"]
	if !ok {
		return nil, fmt.Errorf("niconico: comments response has no threads")
	}
	threadValues, ok := threadsValue.([]any)
	if !ok {
		return nil, fmt.Errorf("niconico: comments threads has invalid type")
	}
	threads := make([]Thread, 0, len(threadValues))
	for _, value := range threadValues {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("niconico: invalid thread")
		}
		thread := Thread{ID: scalarString(object["id"]), Fork: scalarString(object["fork"])}
		commentsValue, _ := object["comments"].([]any)
		thread.Comments = make([]Comment, 0, len(commentsValue))
		for _, commentValue := range commentsValue {
			commentObject, ok := commentValue.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("niconico: invalid comment")
			}
			comment := Comment{
				ID:        scalarString(commentObject["id"]),
				No:        numberInt64(commentObject["no"]),
				VposMs:    numberInt64(commentObject["vposMs"]),
				Body:      scalarString(commentObject["body"]),
				UserID:    scalarString(commentObject["userId"]),
				IsPremium: boolValue(commentObject["isPremium"]),
				Score:     numberInt64(commentObject["score"]),
				PostedAt:  scalarString(commentObject["postedAt"]),
				Source:    scalarString(commentObject["source"]),
				IsMyPost:  boolValue(commentObject["isMyPost"]),
			}
			if nicoru, ok := stringPointer(commentObject["nicoruId"]); ok {
				comment.NicoruID = nicoru
			}
			comment.NicoruCount = numberInt64(commentObject["nicoruCount"])
			if commands, ok := commentObject["commands"].([]any); ok {
				for _, command := range commands {
					comment.Commands = append(comment.Commands, scalarString(command))
				}
			}
			thread.Comments = append(thread.Comments, comment)
		}
		threads = append(threads, thread)
	}
	return threads, nil
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	default:
		return ""
	}
}

func numberInt(value any) int {
	return int(numberInt64(value))
}

func numberInt64(value any) int64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case float64:
		return int64(typed)
	case int64:
		return typed
	default:
		return 0
	}
}

func stringPointer(value any) (*string, bool) {
	if value == nil {
		return nil, false
	}
	parsed := scalarString(value)
	if parsed == "" {
		return nil, false
	}
	return &parsed, true
}

func boolValue(value any) bool {
	parsed, _ := value.(bool)
	return parsed
}
