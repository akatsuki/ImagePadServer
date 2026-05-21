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

	"imagepadserver/internal/browser"
	"imagepadserver/internal/config"
	"imagepadserver/internal/httpscert"
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

func Run() error {
	cfg := config.FromEnv()
	localURL := cfg.URLForHost("127.0.0.1")
	if serverIsHealthy(localURL + "healthz") {
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
	imageURLBase := ""
	var httpsServer *http.Server
	var httpsListener net.Listener
	certFiles, err := httpscert.Ensure([]string{lanIP, "127.0.0.1", "localhost"})
	if err != nil {
		log.Printf("HTTPS certificate unavailable: %v", err)
	} else {
		httpsServer = &http.Server{
			Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.HTTPSPort),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		}
		httpsListener, err = net.Listen("tcp", httpsServer.Addr)
		if err != nil {
			log.Printf("HTTPS listener unavailable: %v", err)
			httpsServer = nil
		} else {
			cfg.HTTPSPort = httpsListener.Addr().(*net.TCPAddr).Port
			imageURLBase = cfg.HTTPSURLForHost(lanIP)
			if !certFiles.Trusted {
				log.Printf("HTTPS certificate trust could not be confirmed")
			}
		}
	}

	mux := http.NewServeMux()
	srv := server.New(cfg, store, imageURLBase)
	srv.Register(mux)
	httpServer.Handler = mux
	if httpsServer != nil {
		httpsServer.Handler = mux
	}

	publicURL := cfg.URLForHost(lanIP)

	log.Printf("ImagePadServer listening on %s", publicURL)
	if imageURLBase != "" {
		log.Printf("ImagePad HTTPS image URL base is %s", imageURLBase)
	}

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

	if httpsServer != nil && httpsListener != nil {
		go func() {
			if err := httpsServer.ServeTLS(httpsListener, certFiles.CertPath, certFiles.KeyPath); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTPS server error: %v", err)
			}
		}()
	}

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
	if httpsServer != nil {
		_ = httpsServer.Shutdown(ctx)
	}
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
	if !status.Available || status.Enabled {
		return
	}
	if _, err := steamvr.SetRegistration(true); err != nil {
		log.Printf("SteamVR registration unavailable: %v", err)
	}
}
