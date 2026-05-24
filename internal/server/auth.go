package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

func (s *Server) adminAllowed(r *http.Request) bool {
	if isLoopbackRequest(r) && isLocalAdminHost(r.Host) {
		return true
	}
	if !isPrivateRemoteRequest(r) || !isAllowedAdminHost(r.Host) {
		return false
	}
	return s.validAdminToken(r)
}

func (s *Server) validAdminToken(r *http.Request) bool {
	if s.adminToken == "" {
		return false
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-ImagePad-Token")
	}
	if token == "" {
		if cookie, err := r.Cookie("imagepad_admin"); err == nil {
			token = cookie.Value
		}
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.adminToken)) == 1
}

func (s *Server) rememberAdminToken(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" || r.URL.Query().Get("token") == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "imagepad_admin",
		Value:    s.adminToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 365,
	})
}

func isLoopbackRequest(r *http.Request) bool {
	ip := remoteIP(r)
	return ip != nil && ip.IsLoopback()
}

func isLocalAdminHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || host == "0.0.0.0" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isAllowedAdminHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return !strings.Contains(host, ".")
	}
	return isAllowedAdminIP(ip)
}

func isPrivateRemoteRequest(r *http.Request) bool {
	ip := remoteIP(r)
	return isAllowedAdminIP(ip)
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func isAllowedAdminIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

func publicReadAllowed(r *http.Request) bool {
	return isAllowedAdminIP(remoteIP(r))
}
