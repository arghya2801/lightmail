package main

import (
	"embed"
	"log"
	"net/http"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	dir, portable := dataDir()
	store, err := openStore(filepath.Join(dir, "cache.db"))
	if err != nil {
		log.Fatal(err)
	}
	app := NewApp(dir, portable, store)
	win := &windows.Options{}
	// No GPU process: about 70 MB less memory (246 to 176 MB private, measured) for
	// roughly 20% more renderer CPU while moving between messages. Text mail doesn't need it.
	win.WebviewGpuIsDisabled = true
	if portable { // keep WebView2's cache beside the exe too
		win.WebviewUserDataPath = filepath.Join(dir, "webview")
	}

	// /img?u=<remote url> serves newsletter images from the disk cache.
	imgs := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/img" {
			http.NotFound(w, r)
			return
		}
		b, ct, err := fetchImage(dir, r.URL.Query().Get("u"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "max-age=31536000")
		w.Write(b)
	})

	err = wails.Run(&options.App{
		Title:            "Lightmail",
		Width:            1320,
		Height:           860,
		MinWidth:         900,
		MinHeight:        560,
		AssetServer:      &assetserver.Options{Assets: assets, Handler: imgs},
		BackgroundColour: &options.RGBA{R: 230, G: 233, B: 237, A: 1},
		OnStartup:        app.startup,
		Windows:          win,
		Bind:             []interface{}{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
