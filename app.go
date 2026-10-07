package main

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the whole frontend API; every exported method is bound to JS.
type App struct {
	ctx     context.Context
	dir     string
	store   *Store
	mu      sync.Mutex // guards cfg
	cfg     Config
	syncing sync.Mutex
	pushing sync.Mutex // one pushSeen at a time
}

func NewApp(dir string, portable bool, store *Store) *App {
	return &App{dir: dir, store: store, cfg: loadConfig(dir, portable)}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go func() {
		for {
			a.Sync()
			m := max(a.config().SyncMinutes, 1)
			time.Sleep(time.Duration(m) * time.Minute)
		}
	}()
}

func (a *App) config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

func (a *App) GetConfig() Config { return a.config() }

// saveDir resolves a relative save folder (portable mode) against the data folder.
func (a *App) saveDir() string {
	d := a.config().SaveDir
	if !filepath.IsAbs(d) {
		d = filepath.Join(a.dir, d)
	}
	return d
}

func (a *App) SaveConfig(c Config) error {
	if c.Host == "" {
		c.Host = "imap.gmail.com:993"
	}
	a.mu.Lock()
	a.cfg = c
	a.mu.Unlock()
	return saveConfig(a.dir, c)
}

// SetAppearance saves only the display settings, so unsaved account edits stay unsaved.
func (a *App) SetAppearance(density string, scale int) error {
	if density != "compact" {
		density = "comfortable"
	}
	scale = min(max(scale, 80), 150)
	a.mu.Lock()
	a.cfg.Density, a.cfg.Scale = density, scale
	c := a.cfg
	a.mu.Unlock()
	return saveConfig(a.dir, c)
}

func (a *App) SetReaderZoom(zoom int) error {
	a.mu.Lock()
	a.cfg.ReaderZoom = min(max(zoom, 50), 200)
	c := a.cfg
	a.mu.Unlock()
	return saveConfig(a.dir, c)
}

func (a *App) ServerLabels() ([]string, error) { return serverLabels(a.config()) }

func (a *App) ChooseSaveDir() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Folder for saved emails", DefaultDirectory: a.saveDir()})
}

type SyncStatus struct {
	Running bool   `json:"running"`
	Label   string `json:"label"`
	Count   int    `json:"count"`
	Error   string `json:"error"`
}

// Sync pulls new mail from every picked label. Concurrent calls are no-ops.
func (a *App) Sync() {
	if !a.syncing.TryLock() {
		return
	}
	defer a.syncing.Unlock()
	c := a.config()
	if len(c.Labels) == 0 || c.Password == "" {
		return
	}
	emit := func(s SyncStatus) { runtime.EventsEmit(a.ctx, "sync", s) }
	emit(SyncStatus{Running: true})
	cl, err := dial(c)
	if err != nil {
		emit(SyncStatus{Error: err.Error()})
		return
	}
	defer cl.Logout()
	fetched := 0 // new messages across all labels
	for _, l := range c.Labels {
		done := 0
		err := a.store.syncLabel(cl, l, func(n int) {
			done = n
			emit(SyncStatus{Running: true, Label: l, Count: fetched + n})
		})
		fetched += done
		if err != nil {
			emit(SyncStatus{Error: err.Error(), Count: fetched})
			return
		}
	}
	emit(SyncStatus{Count: fetched})
	a.pushSeen() // retry anything that failed to reach Gmail earlier
}

// pushSeen sends queued "opened in the app" marks to Gmail. Failures stay queued.
func (a *App) pushSeen() {
	a.pushing.Lock()
	defer a.pushing.Unlock()
	var n int
	if a.store.db.QueryRow(`SELECT count(*) FROM pending_seen`).Scan(&n); n == 0 {
		return
	}
	cl, err := dial(a.config())
	if err != nil {
		log.Printf("mark read in Gmail: %v", err)
		return
	}
	defer cl.Logout()
	if err := a.store.pushSeen(cl); err != nil {
		log.Printf("mark read in Gmail: %v", err)
	}
}

func (a *App) Labels() ([]Count, error)              { return a.store.Labels() }
func (a *App) Senders(label string) ([]Count, error) { return a.store.Senders(label) }
func (a *App) List(q Query) ([]Msg, error)           { return a.store.List(q) }
func (a *App) SetRead(id int64, read bool) error     { return a.store.SetRead(id, read) }

// Open returns a message ready to render and marks it read, here and in Gmail.
func (a *App) Open(id int64) (Msg, error) {
	m, err := a.store.Get(id)
	if err != nil {
		return Msg{}, err
	}
	m.HTML = rewrite(m.HTML, proxied)
	if !m.Read {
		a.store.SetRead(id, true)
		if a.store.queueSeen(id) == nil {
			go a.pushSeen()
		}
	}
	return m, nil
}

func (a *App) SaveEmail(id int64) (string, error) {
	m, err := a.store.Get(id)
	if err != nil {
		return "", err
	}
	return saveEmail(a.dir, a.saveDir(), m)
}
