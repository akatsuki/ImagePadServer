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

	"imagepadserver/internal/about"
	"imagepadserver/internal/appwindow"
	"imagepadserver/internal/browser"
	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/network"
	"imagepadserver/internal/server"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/tray"
	"imagepadserver/internal/tunnel"
	"imagepadserver/internal/video"
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

	storeDir := filepath.Join(settings.Dir(), "media")
	store, err := library.NewStore(storeDir)
	if err != nil {
		return err
	}
	defer resetMediaWorkspace(store)

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
	localURL = cfg.URLForHost("127.0.0.1")

	advertisedHost := cfg.AdvertisedHost(network.BestReachableIP(cfg.PreferTailscale))
	mux := http.NewServeMux()
	srv := server.New(cfg, store, "")
	srv.Register(mux)
	httpServer.Handler = mux
	go measureNetworkOnce()

	publicURL := cfg.URLForHost(advertisedHost)

	log.Printf("%s %s listening on %s", about.AppName, about.Version, publicURL)

	// SteamVR integration is intentionally frozen. Keep the implementation
	// under internal/steamvr as an archived asset, but do not register or start it.

	var tunnelMu sync.Mutex
	var tunnelHandle *tunnel.Tunnel

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	trayExit := make(chan struct{})
	var trayExitOnce sync.Once
	reconnect := make(chan struct{}, 1)
	startTray := func() (*tray.Tray, error) {
		return tray.Start(localURL, func() {
			trayExitOnce.Do(func() {
				close(trayExit)
			})
		}, func() {
			requestReconnect(reconnect)
		}, func() {
			requestReconnect(reconnect)
		})
	}

	trayExitRequested := func() {
		trayExitOnce.Do(func() {
			close(trayExit)
		})
	}

	srv.SetPublicNetworkMessage("UPnP auto port mapping is disabled for safety.")
	srv.SetTunnelReconnect(reconnect)

	go func() {
		time.Sleep(300 * time.Millisecond)
		if useNativeWindow {
			_ = appwindow.Show(localURL)
			return
		}
		browser.Open(localURL)
	}()

	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
		}
	}()

	go func() {
		originURL := cfg.URLForHost("127.0.0.1")
		manageCloudflareTunnel(originURL, srv, &tunnelMu, &tunnelHandle, reconnect, stop)
	}()

	if tray.MustRunOnMainThread() {
		go func() {
			select {
			case <-stop:
				tray.StopCurrent()
			case <-trayExit:
				tray.StopCurrent()
			}
		}()
		if _, err := startTray(); err != nil {
			log.Printf("tray icon unavailable: %v", err)
			select {
			case <-stop:
			case <-trayExit:
			}
		}
	} else {
		trayIcon, err := startTray()
		if err != nil {
			log.Printf("tray icon unavailable: %v", err)
		} else {
			defer trayIcon.Stop()
		}
		select {
		case <-stop:
		case <-trayExit:
		}
	}
	trayExitRequested()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tunnelMu.Lock()
	if tunnelHandle != nil {
		tunnelHandle.Stop()
	}
	tunnelMu.Unlock()
	return httpServer.Shutdown(ctx)
}

func resetMediaWorkspace(store *library.Store) {
	if store == nil {
		return
	}
	video.RemoveGenerated(store.Dir())
	if err := store.Reset(); err != nil {
		log.Printf("failed to reset media workspace: %v", err)
	}
}

func measureNetworkOnce() {
	appSettings, err := settings.Load()
	if err != nil || appSettings.NetworkUploadMbps > 0 {
		return
	}
	measurement := video.MeasureNetwork()
	if measurement.UploadMbps <= 0 {
		return
	}
	_ = settings.Update(func(appSettings *settings.Settings) error {
		appSettings.NetworkUploadMbps = measurement.UploadMbps
		return nil
	})
}

func manageCloudflareTunnel(originURL string, srv *server.Server, tunnelMu *sync.Mutex, tunnelHandle **tunnel.Tunnel, reconnect <-chan struct{}, stop <-chan os.Signal) {
	const retryDelay = 5 * time.Second

	for {
		handle, status := tunnel.Start(originURL)
		srv.SetTunnelStatus(status.OK, status.URL, status.Message)
		if status.OK {
			log.Printf("Cloudflare Tunnel available at %s", status.URL)
			tunnelMu.Lock()
			*tunnelHandle = handle
			tunnelMu.Unlock()

		waitLoop:
			for {
				select {
				case <-stop:
					handle.Stop()
					return
				case <-reconnect:
					if !handle.IsRunning() {
						log.Printf("Cloudflare Tunnel reconnect requested and process is not running; restarting")
						handle.Stop()
						break waitLoop
					}
					log.Printf("Cloudflare Tunnel reconnect requested; restarting")
					handle.Stop()
					break waitLoop
				case err := <-handle.Done():
					log.Printf("Cloudflare Tunnel stopped unexpectedly: %v", err)
					srv.SetTunnelStatus(false, "", "Cloudflare Tunnel disconnected; retrying...")
					break waitLoop
				}
			}

			tunnelMu.Lock()
			*tunnelHandle = nil
			tunnelMu.Unlock()
		} else {
			log.Printf("Cloudflare Tunnel unavailable: %s", status.Message)
		}

		select {
		case <-stop:
			return
		case <-reconnect:
			log.Printf("Cloudflare Tunnel reconnect requested; retrying immediately")
		case <-time.After(retryDelay):
		}
	}
}

func requestReconnect(reconnect chan<- struct{}) {
	select {
	case reconnect <- struct{}{}:
	default:
	}
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
