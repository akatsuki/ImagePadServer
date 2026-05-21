package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Host string
	Port int
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
	return cfg
}

func (c Config) URLForHost(host string) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/", host, c.Port)
}
