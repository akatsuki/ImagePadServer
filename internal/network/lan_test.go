package network

import (
	"net"
	"testing"
)

func TestIsTailscaleIP(t *testing.T) {
	valid := []string{"100.64.0.1", "100.100.100.100", "100.127.255.254"}
	for _, raw := range valid {
		if !isTailscaleIP(net.ParseIP(raw)) {
			t.Fatalf("expected %s to be detected as a Tailscale IPv4 address", raw)
		}
	}

	invalid := []string{"100.63.255.255", "100.128.0.1", "192.168.1.20", "fd7a:115c:a1e0::1"}
	for _, raw := range invalid {
		if isTailscaleIP(net.ParseIP(raw)) {
			t.Fatalf("expected %s not to be detected as a Tailscale IPv4 address", raw)
		}
	}
}

func TestIsTailscaleCandidate(t *testing.T) {
	if !isTailscaleCandidate("Wi-Fi", net.ParseIP("100.64.12.34")) {
		t.Fatal("expected Tailscale CGNAT address to be a candidate")
	}
	if !isTailscaleCandidate("Tailscale", net.ParseIP("10.0.0.5")) {
		t.Fatal("expected Tailscale interface name to be a candidate")
	}
	if isTailscaleCandidate("Wi-Fi", net.ParseIP("192.168.1.20")) {
		t.Fatal("expected normal LAN address not to be a Tailscale candidate")
	}
}
