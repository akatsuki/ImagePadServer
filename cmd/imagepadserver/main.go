package main

import (
	"log"
	"os"

	"imagepadserver/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
