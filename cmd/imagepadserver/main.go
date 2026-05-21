package main

import (
	"log"
	"os"

	"imagepadserver/internal/app"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--steamvr-launch" {
		if err := app.OpenWindowOrRun(); err != nil {
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
