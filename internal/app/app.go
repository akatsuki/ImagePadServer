package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"imagepadserver/internal/browser"
	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/network"
	"imagepadserver/internal/server"
	"imagepadserver/internal/upnp"
)

func Run() error {
	cfg := config.FromEnv()

	storeDir := filepath.Join(os.TempDir(), "imagepadserver")
	store, err := library.NewStore(storeDir)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	srv := server.New(cfg, store)
	srv.Register(mux)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:      mux,
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
	publicURL := cfg.URLForHost(lanIP)

	log.Printf("ImagePadServer listening on %s", publicURL)

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

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(ctx)
}
