package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"imagepadserver/internal/config"
	"imagepadserver/internal/discovery"
)

type obsRelayConfig struct {
	OK                 bool        `json:"ok"`
	ServerAddress      string      `json:"serverAddress"`
	StreamKey          string      `json:"streamKey"`
	RTMPURL            string      `json:"rtmpURL"`
	VideoPlayerEnabled bool        `json:"videoPlayerEnabled"`
	Listening          bool        `json:"listening"`
	Publishing         bool        `json:"publishing"`
	Latency            interface{} `json:"latency,omitempty"`
}

// PrintOBSRelayConfig asks a running local ImagePadServer instance to prepare
// the OBS/RTMP receiver for an external relay tool, then prints connection info.
func PrintOBSRelayConfig(args []string, out io.Writer) error {
	format := "json"
	outputPath := ""
	serverURL := ""
	token := os.Getenv("IMAGEPAD_ADMIN_TOKEN")
	clientID := os.Getenv("IMAGEPAD_RELAY_CLIENT_ID")
	clientSecret := os.Getenv("IMAGEPAD_RELAY_CLIENT_SECRET")
	discover := false
	timeout := 1200 * time.Millisecond
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 >= len(args) {
				return errors.New("--format requires json, env, or rtmp-url")
			}
			format = args[i+1]
			i++
		case "--json":
			format = "json"
		case "--env":
			format = "env"
		case "--rtmp-url":
			format = "rtmp-url"
		case "--output", "-o":
			if i+1 >= len(args) {
				return errors.New("--output requires a file path")
			}
			outputPath = args[i+1]
			i++
		case "--server":
			if i+1 >= len(args) {
				return errors.New("--server requires an ImagePadServer base URL")
			}
			serverURL = args[i+1]
			i++
		case "--token":
			if i+1 >= len(args) {
				return errors.New("--token requires an admin token")
			}
			token = args[i+1]
			i++
		case "--client-id":
			if i+1 >= len(args) {
				return errors.New("--client-id requires a relay client id")
			}
			clientID = args[i+1]
			i++
		case "--client-secret":
			if i+1 >= len(args) {
				return errors.New("--client-secret requires a relay client secret")
			}
			clientSecret = args[i+1]
			i++
		case "--discover":
			discover = true
		case "--timeout-ms":
			if i+1 >= len(args) {
				return errors.New("--timeout-ms requires a number")
			}
			ms, err := strconv.Atoi(args[i+1])
			if err != nil || ms <= 0 {
				return errors.New("--timeout-ms requires a positive number")
			}
			timeout = time.Duration(ms) * time.Millisecond
			i++
		case "--help", "-h":
			fmt.Fprintln(out, "Usage: imagepadserver obs-relay-config [--discover] [--server url] [--token token] [--client-id id --client-secret secret] [--format json|env|rtmp-url] [--output file]")
			return nil
		default:
			return fmt.Errorf("unknown obs-relay-config option: %s", args[i])
		}
	}

	if outputPath != "" {
		file, err := os.Create(outputPath)
		if err != nil {
			return err
		}
		defer file.Close()
		out = file
	}

	baseURL := serverURL
	if discover {
		info, err := discovery.Discover(timeout)
		if err != nil {
			return err
		}
		baseURL = info.BaseURL
	}
	if baseURL == "" {
		cfg := config.FromEnv()
		baseURL = cfg.URLForHost("127.0.0.1")
	}
	baseURL = strings.TrimRight(baseURL, "/") + "/"
	client := http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodPost, baseURL+"api/obs/relay-config", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-ImagePad-Client", "BrowserRelayStreamer")
	if token != "" {
		req.Header.Set("X-ImagePad-Token", token)
	} else if clientID != "" && clientSecret != "" {
		signRelayRequest(req, clientID, clientSecret, nil)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ImagePadServer is not running or did not answer on %s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("OBS relay config request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var relay obsRelayConfig
	if err := json.NewDecoder(resp.Body).Decode(&relay); err != nil {
		return err
	}
	switch format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(relay)
	case "env":
		_, err := fmt.Fprintf(out, "IMAGEPAD_OBS_SERVER=%s\nIMAGEPAD_OBS_STREAM_KEY=%s\nIMAGEPAD_OBS_RTMP_URL=%s\n", relay.ServerAddress, relay.StreamKey, relay.RTMPURL)
		return err
	case "rtmp-url":
		_, err := fmt.Fprintln(out, relay.RTMPURL)
		return err
	default:
		return fmt.Errorf("unsupported format %q; use json, env, or rtmp-url", format)
	}
}

func signRelayRequest(req *http.Request, clientID, clientSecret string, body []byte) {
	timestamp := time.Now().UTC().Format(time.RFC3339)
	nonce := fmt.Sprintf("%d", time.Now().UnixNano())
	bodyHash := sha256.Sum256(body)
	message := strings.Join([]string{
		req.Method,
		req.URL.RequestURI(),
		timestamp,
		nonce,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
	mac := hmac.New(sha256.New, []byte(clientSecret))
	_, _ = mac.Write([]byte(message))
	req.Header.Set("X-ImagePad-Client-Id", clientID)
	req.Header.Set("X-ImagePad-Timestamp", timestamp)
	req.Header.Set("X-ImagePad-Nonce", nonce)
	req.Header.Set("X-ImagePad-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}
