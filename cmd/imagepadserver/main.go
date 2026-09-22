package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"

	"imagepadserver/internal/app"
	"imagepadserver/internal/nicoexportworker"
)

func main() {
	// SteamVR launch handling is frozen indefinitely. The old overlay assets and
	// code remain in the repository, but the app no longer exposes that entry.

	if len(os.Args) > 1 && os.Args[1] == "obs-relay-config" {
		if err := app.PrintOBSRelayConfig(os.Args[2:], os.Stdout); err != nil {
			log.Println(err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "reset-data" {
		if err := app.ResetData(os.Args[2:], os.Stdout); err != nil {
			log.Println(err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "nico-export-worker" {
		if err := runNicoExportWorker(os.Stdin, os.Stdout, os.Stderr); err != nil {
			log.Println(err)
			os.Exit(1)
		}
		return
	}

	if err := app.Run(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}

func runNicoExportWorker(input io.Reader, output, diagnostics io.Writer) error {
	request, err := nicoexportworker.ReadRequest(input)
	if err != nil {
		eventErr := nicoexportworker.WriteEvent(output, nicoexportworker.Event{Version: nicoexportworker.ProtocolVersion, Type: "result", Error: err.Error()})
		if eventErr != nil {
			return errors.Join(err, eventErr)
		}
		return err
	}
	return nicoexportworker.Run(context.Background(), request, output, diagnostics)
}
