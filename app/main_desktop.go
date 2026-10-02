//go:build desktop || bindings

package main

import (
	"embed"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	dataDir := os.Getenv("HARNESS_DATA_DIR")
	if dataDir == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			log.Fatal(err)
		}
		dataDir = filepath.Join(config, "harness")
	}
	app, err := NewDesktop(filepath.Join(dataDir, "harness.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer app.store.Close()
	frontend, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	if err := wails.Run(&options.App{
		Title: "harness", Width: 1280, Height: 900, MinWidth: 980, MinHeight: 700,
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 244, A: 255},
		AssetServer:      &assetserver.Options{Assets: frontend},
		OnStartup:        app.startup, OnShutdown: app.shutdown, Bind: []interface{}{app},
	}); err != nil {
		log.Fatal(err)
	}
}
