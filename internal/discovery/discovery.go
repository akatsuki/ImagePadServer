package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	Port  = 45849
	Probe = "IMAGEPADSERVER_DISCOVER_V1"
)

type Info struct {
	App             string `json:"app"`
	Version         string `json:"version"`
	BaseURL         string `json:"baseURL"`
	HealthPath      string `json:"healthPath"`
	OBSRelayPath    string `json:"obsRelayPath"`
	AuthRequired    bool   `json:"authRequired"`
	DiscoveryPort   int    `json:"discoveryPort"`
	ProtocolVersion int    `json:"protocolVersion"`
}

func DefaultInfo(app, version, baseURL string) Info {
	return Info{
		App:             app,
		Version:         version,
		BaseURL:         strings.TrimRight(baseURL, "/") + "/",
		HealthPath:      "/healthz",
		OBSRelayPath:    "/api/obs/relay-config",
		AuthRequired:    true,
		DiscoveryPort:   Port,
		ProtocolVersion: 1,
	}
}

func StartResponder(ctx context.Context, info Info) error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: Port}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go serve(ctx, conn, info)
	return nil
}

func serve(ctx context.Context, conn *net.UDPConn, info Info) {
	buf := make([]byte, 512)
	payload, _ := json.Marshal(info)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			return
		}
		if strings.TrimSpace(string(buf[:n])) != Probe {
			continue
		}
		_, _ = conn.WriteToUDP(payload, remote)
	}
}

func Discover(timeout time.Duration) (Info, error) {
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return Info{}, err
	}
	defer conn.Close()

	target := &net.UDPAddr{IP: net.IPv4bcast, Port: Port}
	if _, err := conn.WriteToUDP([]byte(Probe+"\n"), target); err != nil {
		return Info{}, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return Info{}, fmt.Errorf("ImagePadServer beacon not found on LAN: %w", err)
		}
		var info Info
		if err := json.Unmarshal(buf[:n], &info); err != nil {
			continue
		}
		if info.App == "" || info.BaseURL == "" || info.ProtocolVersion != 1 {
			continue
		}
		return info, nil
	}
}
