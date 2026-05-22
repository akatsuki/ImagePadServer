package config

import "testing"

func TestURLForHostFormatsIPv4AndIPv6(t *testing.T) {
	cfg := Config{Port: 8080}

	if got := cfg.URLForHost("100.64.0.10"); got != "http://100.64.0.10:8080/" {
		t.Fatalf("unexpected IPv4 URL: %s", got)
	}
	if got := cfg.URLForHost("fd7a:115c:a1e0::1"); got != "http://[fd7a:115c:a1e0::1]:8080/" {
		t.Fatalf("unexpected IPv6 URL: %s", got)
	}
}

func TestAdvertisedHostPrefersExplicitValue(t *testing.T) {
	cfg := Config{AdvertiseHost: "imagepad.tailnet-name.ts.net"}

	if got := cfg.AdvertisedHost("192.168.1.20"); got != "imagepad.tailnet-name.ts.net" {
		t.Fatalf("unexpected advertised host: %s", got)
	}
}

func TestTruthy(t *testing.T) {
	if !truthy("1") || !truthy("true") || !truthy("ON") {
		t.Fatal("expected common truthy values to be accepted")
	}
	if truthy("") || truthy("0") || truthy("false") {
		t.Fatal("expected non-truthy values to be rejected")
	}
}
