package main

import (
	"log"
	"os"

	"github.com/barishamil/kde-connect-fyne/internal/core"
	"github.com/barishamil/kde-connect-fyne/internal/ui"
)

func main() {
	deviceName, _ := os.Hostname()
	if deviceName == "" {
		deviceName = "Fyne Client"
	}

	engine, err := core.NewEngine(deviceName)
	if err != nil {
		log.Fatalf("Failed to initialize engine: %v", err)
	}

	app := ui.NewApp(engine)

	engine.Start()

	log.Printf("KDE Connect client started with ID %s\n", engine.Identity.DeviceId)
	app.Run()
}
