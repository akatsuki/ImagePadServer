package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"imagepadserver/internal/appwindow"
	"imagepadserver/internal/browser"
	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/network"
	"imagepadserver/internal/server"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/steamvr"
	"imagepadserver/internal/tray"
	"imagepadserver/internal/tunnel"
	"imagepadserver/internal/upnp"
)

// OpenOrRun opens the existing local server when it is already running;
// otherwise it starts a normal ImagePadServer instance.
func OpenOrRun() error {
	cfg := config.FromEnv()
	localURL := cfg.URLForHost("127.0.0.1")
	if serverIsHealthy(localURL + "healthz") {
		browser.Open(localURL)
		return nil
	}
	return Run()
}

// OpenWindowOrRun shows the built-in desktop window when an instance is
// already running; otherwise it starts the server and opens that window.
func OpenWindowOrRun() error {
	cfg := config.FromEnv()
	localURL := cfg.URLForHost("127.0.0.1")
	if serverIsHealthy(localURL + "healthz") {
		return appwindow.Show(localURL)
	}
	return run(true)
}

func Run() error {
	return run(false)
}

func run(useNativeWindow bool) error {
	cfg := config.FromEnv()
	localURL := cfg.URLForHost("127.0.0.1")
	if serverIsHealthy(localURL + "healthz") {
		if useNativeWindow {
			return appwindow.Show(localURL)
		}
		browser.Open(localURL)
		return nil
	}

	storeDir := filepath.Join(os.TempDir(), "imagepadserver")
	store, err := library.NewStore(storeDir)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	listener, err := net.Listen("tcp", httpServer.Addr)
	if err != nil {
		return err
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	cfg.Port = actualPort

	lanIP := network.BestLANIP()
	mux := http.NewServeMux()
	srv := server.New(cfg, store, "")
	srv.Register(mux)
	httpServer.Handler = mux

	publicURL := cfg.URLForHost(lanIP)

	log.Printf("ImagePadServer listening on %s", publicURL)

	ensureSteamVRRegistration()

	var tunnelMu sync.Mutex
	var tunnelHandle *tunnel.Tunnel

	trayIcon, err := tray.Start(publicURL)
	if err != nil {
		log.Printf("tray icon unavailable: %v", err)
	} else {
		defer trayIcon.Stop()
	}

	go func() {
		result := upnp.TryMapTCP(actualPort, "ImagePadServer")
		srv.SetUPnPResult(result)
		if result.OK {
			log.Printf("UPnP mapped TCP port %d", actualPort)
		} else {
			log.Printf("UPnP unavailable: %s", result.Message)
		}
	}()

	go func() {
		time.Sleep(300 * time.Millisecond)
		if useNativeWindow {
			_ = appwindow.Show(localURL)
			return
		}
		browser.Open(publicURL)
	}()

	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
		}
	}()

	go func() {
		originURL := cfg.URLForHost("127.0.0.1")
		handle, status := tunnel.Start(originURL)
		srv.SetTunnelStatus(status.OK, status.URL, status.Message)
		if status.OK {
			log.Printf("Cloudflare Tunnel available at %s", status.URL)
			tunnelMu.Lock()
			tunnelHandle = handle
			tunnelMu.Unlock()
		} else {
			log.Printf("Cloudflare Tunnel unavailable: %s", status.Message)
		}
	}()

	go func() {
		if err := steamvr.Start(steamvr.Config{ServerURL: publicURL}); err != nil {
			log.Printf("SteamVR integration unavailable: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tunnelMu.Lock()
	if tunnelHandle != nil {
		tunnelHandle.Stop()
	}
	tunnelMu.Unlock()
	return httpServer.Shutdown(ctx)
}

func serverIsHealthy(url string) bool {
	client := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
	return resp.StatusCode == http.StatusOK && string(body) == "ok"
}

func ensureSteamVRRegistration() {
	appSettings, err := settings.Load()
	if err == nil && appSettings.SteamVRExplicitlyDisabled {
		return
	}
	status := steamvr.Registration()
	if !status.Available {
		return
	}
	if _, err := steamvr.SetRegistration(true); err != nil {
		log.Printf("SteamVR registration unavailable: %v", err)
	}
}
