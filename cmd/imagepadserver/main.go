package main

import (
	"log"
	"os"

	"imagepadserver/internal/app"
)

func main() {
	// SteamVR launch handling is frozen indefinitely. The old overlay assets and
	// code remain in the repository, but the app no longer exposes that entry.

	if err := app.Run(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
