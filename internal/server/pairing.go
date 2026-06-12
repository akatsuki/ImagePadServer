package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"imagepadserver/internal/settings"
)

const (
	relayScope       = "obs-relay"
	pairingTTL       = 120 * time.Second
	relayNonceTTL    = 5 * time.Minute
	relayClockWindow = 5 * time.Minute
)

type pairingRequest struct {
	ID         string
	Nonce      string
	PIN        string
	ClientName string
	DeviceName string
	ExpiresAt  time.Time
	Attempts   int
}

func (s *Server) handlePairingRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isPrivateRemoteRequest(r) || !isAllowedAdminHost(r.Host) {
		http.Error(w, "pairing requires a local network request", http.StatusForbidden)
		return
	}
	var req struct {
		ClientName string `json:"clientName"`
		DeviceName string `json:"deviceName"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	req.ClientName = cleanLabel(req.ClientName, "BrowserRelayStreamer")
	req.DeviceName = cleanLabel(req.DeviceName, "Relay device")

	pairingID := "pair_" + randomToken(18)
	nonce := randomToken(24)
	pin := randomPIN()
	expiresAt := time.Now().Add(pairingTTL)
	s.mu.Lock()
	s.prunePairingsLocked(time.Now())
	s.pairings[pairingID] = pairingRequest{
		ID:         pairingID,
		Nonce:      nonce,
		PIN:        pin,
		ClientName: req.ClientName,
		DeviceName: req.DeviceName,
		ExpiresAt:  expiresAt,
	}
	s.mu.Unlock()

	writeJSON(w, map[string]interface{}{
		"pairingId": pairingID,
		"nonce":     nonce,
		"expiresAt": expiresAt.UTC().Format(time.RFC3339),
		"pinDigits": 4,
	})
}

func (s *Server) handlePairingConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isPrivateRemoteRequest(r) || !isAllowedAdminHost(r.Host) {
		http.Error(w, "pairing requires a local network request", http.StatusForbidden)
		return
	}
	var req struct {
		PairingID  string `json:"pairingId"`
		ClientName string `json:"clientName"`
		DeviceName string `json:"deviceName"`
		Proof      string `json:"proof"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid pairing confirmation", http.StatusBadRequest)
		return
	}
	now := time.Now()
	s.mu.Lock()
	s.prunePairingsLocked(now)
	pairing, ok := s.pairings[req.PairingID]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "pairing request expired or not found", http.StatusForbidden)
		return
	}
	if pairing.Attempts >= 5 {
		delete(s.pairings, req.PairingID)
		s.mu.Unlock()
		http.Error(w, "pairing attempts exceeded", http.StatusForbidden)
		return
	}
	pairing.Attempts++
	s.pairings[req.PairingID] = pairing
	s.mu.Unlock()

	clientName := cleanLabel(req.ClientName, pairing.ClientName)
	deviceName := cleanLabel(req.DeviceName, pairing.DeviceName)
	if !validPairingProof(pairing, clientName, deviceName, req.Proof) {
		http.Error(w, "invalid pairing proof", http.StatusForbidden)
		return
	}

	clientID := "brs_" + randomToken(18)
	clientSecret := randomToken(32)
	createdAt := now.UTC().Format(time.RFC3339)
	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.RelayDevices = append(appSettings.RelayDevices, settings.RelayDevice{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			DeviceName:   deviceName,
			Scope:        relayScope,
			CreatedAt:    createdAt,
		})
		return nil
	}); err != nil {
		http.Error(w, "failed to save relay device", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	delete(s.pairings, req.PairingID)
	s.mu.Unlock()

	writeJSON(w, map[string]interface{}{
		"clientId":      clientID,
		"clientSecret":  clientSecret,
		"serverBaseURL": s.lanURL,
		"scope":         relayScope,
		"createdAt":     createdAt,
	})
}

func (s *Server) relayDeviceAllowed(r *http.Request) bool {
	if !isPrivateRemoteRequest(r) || !isAllowedAdminHost(r.Host) {
		return false
	}
	clientID := strings.TrimSpace(r.Header.Get("X-ImagePad-Client-Id"))
	timestamp := strings.TrimSpace(r.Header.Get("X-ImagePad-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-ImagePad-Nonce"))
	signature := strings.TrimSpace(r.Header.Get("X-ImagePad-Signature"))
	if clientID == "" || timestamp == "" || nonce == "" || signature == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return false
	}
	now := time.Now()
	if at.Before(now.Add(-relayClockWindow)) || at.After(now.Add(relayClockWindow)) {
		return false
	}
	appSettings, err := settings.Load()
	if err != nil {
		return false
	}
	var device settings.RelayDevice
	for _, candidate := range appSettings.RelayDevices {
		if candidate.ClientID == clientID {
			device = candidate
			break
		}
	}
	if device.ClientID == "" || device.ClientSecret == "" || device.Scope != relayScope || device.RevokedAt != "" {
		return false
	}
	if !s.acceptRelayNonce(clientID, nonce, now) {
		return false
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	bodyHash := sha256.Sum256(body)
	message := strings.Join([]string{
		r.Method,
		r.URL.RequestURI(),
		timestamp,
		nonce,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
	expected := hmacSHA256Base64URL(device.ClientSecret, message)
	if subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) != 1 {
		return false
	}
	_ = settings.Update(func(appSettings *settings.Settings) error {
		for i := range appSettings.RelayDevices {
			if appSettings.RelayDevices[i].ClientID == clientID {
				appSettings.RelayDevices[i].LastSeenAt = now.UTC().Format(time.RFC3339)
				break
			}
		}
		return nil
	})
	return true
}

func (s *Server) acceptRelayNonce(clientID, nonce string, now time.Time) bool {
	key := clientID + ":" + nonce
	s.mu.Lock()
	defer s.mu.Unlock()
	for existing, expiresAt := range s.relayNonces {
		if expiresAt.Before(now) {
			delete(s.relayNonces, existing)
		}
	}
	if _, ok := s.relayNonces[key]; ok {
		return false
	}
	s.relayNonces[key] = now.Add(relayNonceTTL)
	return true
}

func (s *Server) pairingState() map[string]interface{} {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prunePairingsLocked(now)
	for _, pairing := range s.pairings {
		return map[string]interface{}{
			"active":     true,
			"pin":        pairing.PIN,
			"clientName": pairing.ClientName,
			"deviceName": pairing.DeviceName,
			"expiresAt":  pairing.ExpiresAt.UTC().Format(time.RFC3339),
		}
	}
	return map[string]interface{}{"active": false}
}

func (s *Server) prunePairingsLocked(now time.Time) {
	for id, pairing := range s.pairings {
		if pairing.ExpiresAt.Before(now) {
			delete(s.pairings, id)
		}
	}
}

func validPairingProof(pairing pairingRequest, clientName, deviceName, proof string) bool {
	message := strings.Join([]string{pairing.ID, pairing.Nonce, clientName, deviceName}, "\n")
	expected := hmacSHA256Hex(pairing.PIN, message)
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(strings.TrimSpace(proof))), []byte(expected)) == 1
}

func hmacSHA256Hex(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func hmacSHA256Base64URL(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func randomPIN() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000"
	}
	value := (int(b[0])<<8 | int(b[1])) % 10000
	return fmt.Sprintf("%04d", value)
}

func randomToken(bytes int) string {
	if bytes <= 0 {
		bytes = 16
	}
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func cleanLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if len(value) > 80 {
		value = value[:80]
	}
	return value
}
