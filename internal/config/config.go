package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

type Config struct {
	Host            string
	Port            int
	AdvertiseHost   string
	PreferTailscale bool
}

func FromEnv() Config {
	cfg := Config{
		Host: "0.0.0.0",
		Port: 8080,
	}
	if v := os.Getenv("IMAGEPAD_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("IMAGEPAD_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil && port > 0 {
			cfg.Port = port
		}
	}
	if v := os.Getenv("IMAGEPAD_ADVERTISE_HOST"); v != "" {
		cfg.AdvertiseHost = v
	}
	if v := os.Getenv("IMAGEPAD_PREFER_TAILSCALE"); truthy(v) {
		cfg.PreferTailscale = true
	}
	return cfg
}

func (c Config) URLForHost(host string) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s/", net.JoinHostPort(host, strconv.Itoa(c.Port)))
}

func (c Config) AdvertisedHost(defaultHost string) string {
	if c.AdvertiseHost != "" {
		return c.AdvertiseHost
	}
	return defaultHost
}

func truthy(value string) bool {
	switch value {
	case "1", "true", "TRUE", "True", "yes", "YES", "on", "ON":
		return true
	default:
		return false
	}
}
