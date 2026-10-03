package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os"

	"imagepadserver/internal/app"
	"imagepadserver/internal/nicoexportworker"
	"imagepadserver/internal/xpostexport"
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

	if len(os.Args) > 1 && os.Args[1] == "nico-export-session" {
		if err := runNicoExportSession(os.Args[2:], os.Stdin, os.Stdout, os.Stderr); err != nil {
			log.Println(err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "xpost-export-worker" {
		request, err := xpostexport.ReadRequest(os.Stdin)
		if err == nil {
			err = xpostexport.Run(context.Background(), request, os.Stdout, os.Stderr)
		}
		if err != nil {
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

func runNicoExportSession(args []string, input io.Reader, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("nico-export-session", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var sessionID string
	flags.StringVar(&sessionID, "session-id", "", "parent-supplied worker session identifier")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("nico-export-session: unexpected positional arguments")
	}
	if sessionID == "" {
		return errors.New("nico-export-session: --session-id is required")
	}
	return nicoexportworker.RunSession(context.Background(), sessionID, input, output, diagnostics)
}
