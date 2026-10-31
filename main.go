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
	app, err := NewApp(dir, portable, store)
	if err != nil {
		log.Fatal(err)
	}
	win := &windows.Options{}
	// No GPU process: about 70 MB less memory (246 to 176 MB private, measured) for
	// roughly 20% more renderer CPU while moving between messages. Text mail doesn't need it.
	win.WebviewGpuIsDisabled = true
	if portable { // keep WebView2's cache beside the exe too
		win.WebviewUserDataPath = filepath.Join(dir, "webview")
	}

	err = wails.Run(&options.App{
		Title:            "Lightmail",
		Width:            1320,
		Height:           860,
		MinWidth:         900,
		MinHeight:        560,
		AssetServer:      &assetserver.Options{Assets: assets, Handler: imageHandler(dir)},
		BackgroundColour: &options.RGBA{R: 230, G: 233, B: 237, A: 1},
		OnStartup:        app.startup,
		Windows:          win,
		Bind:             []interface{}{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// /img?u=<remote url> serves validated images from the disk cache.
func imageHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
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
	}
}
