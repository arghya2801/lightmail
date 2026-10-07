package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
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
	dial    func() (*imapclient.Client, error) // swapped in tests

	connMu sync.Mutex // guards conn, the connection for clicks (open, move, flags)
	conn   *session

	statusMu sync.Mutex
	status   SyncStatus // last one emitted
}

func NewApp(dir string, portable bool, store *Store) *App {
	a := &App{dir: dir, store: store, cfg: loadConfig(dir, portable)}
	a.dial = func() (*imapclient.Client, error) { return dial(a.config()) }
	return a
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
	a.dropConn() // the account may have changed
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

type ServerFolder struct {
	Name    string `json:"name"`
	Default bool   `json:"default"` // ticked unless the user says otherwise
}

// ServerFolders lists the account's folders for the Settings picker.
func (a *App) ServerFolders() ([]ServerFolder, error) {
	var out []ServerFolder
	err := a.imap(func(s *session) error {
		fs, err := listFolders(s.cl)
		if err != nil {
			return err
		}
		a.store.setFolders(fs)
		for _, f := range fs {
			out = append(out, ServerFolder{f.Name, defaultSynced(f)})
		}
		return nil
	})
	return out, err
}

func (a *App) ChooseSaveDir() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Folder for saved emails", DefaultDirectory: a.saveDir()})
}

// imap runs f on the shared connection for user actions, dialing when there is
// none. A dropped connection (Gmail closes idle ones) is redialed once; a
// server refusal (NO/BAD) is returned as is.
func (a *App) imap(f func(*session) error) error {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	for try := 0; ; try++ {
		if a.conn == nil {
			cl, err := a.dial()
			if err != nil {
				return err
			}
			a.conn = &session{cl: cl}
		}
		err := f(a.conn)
		var refused *imap.Error
		if err == nil || errors.As(err, &refused) || try == 1 {
			return err
		}
		a.conn.cl.Close()
		a.conn = nil
	}
}

func (a *App) dropConn() {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.conn != nil {
		a.conn.cl.Close()
		a.conn = nil
	}
}

type SyncStatus struct {
	Running bool   `json:"running"`
	Label   string `json:"label"`
	Count   int    `json:"count"`
	Error   string `json:"error"`
}

func (a *App) emit(s SyncStatus) {
	a.statusMu.Lock()
	a.status = s
	a.statusMu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "sync", s)
	}
}

// GetSyncStatus returns the last sync status, for a page that missed the event.
func (a *App) GetSyncStatus() SyncStatus {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	return a.status
}

// Sync brings every picked folder up to date. Concurrent calls are no-ops.
func (a *App) Sync() {
	if !a.syncing.TryLock() {
		return
	}
	defer a.syncing.Unlock()
	c := a.config()
	if len(c.Labels) == 0 || c.Password == "" {
		return
	}
	a.emit(SyncStatus{Running: true})
	a.pushFlags() // first, so the reconcile below sees local changes on the server
	if err := a.syncAll(c.Labels); err != nil {
		a.emit(SyncStatus{Error: err.Error()})
	}
}

// syncAll runs one sync on its own connection, so clicks never wait behind it.
func (a *App) syncAll(labels []string) error {
	cl, err := a.dial()
	if err != nil {
		return err
	}
	defer cl.Logout()
	ss := &session{cl: cl}
	folders, err := listFolders(cl)
	if err != nil {
		return err
	}
	if err := a.store.setFolders(folders); err != nil {
		return err
	}
	var live []string // picked folders that still exist on the server
	for _, f := range folders {
		if slices.Contains(labels, f.Name) {
			live = append(live, f.Name)
		}
	}
	fetched := 0 // new messages across all folders
	for _, l := range live {
		done := 0
		err := a.store.syncLabel(ss, l, func(n int) {
			done = n
			a.emit(SyncStatus{Running: true, Label: l, Count: fetched + n})
		})
		fetched += done
		if err != nil {
			return err
		}
	}
	if err := a.store.prune(live); err != nil {
		return err
	}
	a.emit(SyncStatus{Count: fetched})

	if inbox := a.store.roleFolder("inbox"); slices.Contains(live, inbox) {
		uids, err := a.store.missingBodies(inbox)
		if err == nil {
			err = a.store.fetchBodies(ss, inbox, uids)
		}
		if err != nil {
			log.Printf("prefetch: %v", err)
		}
	}
	return nil
}

// pushFlags sends queued read/starred changes. Failures stay queued.
func (a *App) pushFlags() {
	var n int
	if a.store.db.QueryRow(`SELECT count(*) FROM pending_flags`).Scan(&n); n == 0 {
		return
	}
	if err := a.imap(a.store.pushFlags); err != nil {
		log.Printf("push flags: %v", err)
	}
}

func (a *App) Folders() ([]Folder, error) {
	fs, err := a.store.Folders()
	labels := a.config().Labels
	for i := range fs {
		fs[i].Synced = slices.Contains(labels, fs[i].Name)
	}
	return fs, err
}

func (a *App) Senders(label string) ([]Count, error) { return a.store.Senders(label) }
func (a *App) List(q Query) ([]Msg, error)           { return a.store.List(q) }

// SetFlag sets read (`\Seen`) or starred (`\Flagged`) here, then on the server.
func (a *App) SetFlag(id int64, flag string, value bool) error {
	if err := a.store.setFlag(id, flag, value); err != nil {
		return err
	}
	go a.pushFlags()
	return nil
}

// raw returns the whole message, downloading and caching it on first open.
func (a *App) raw(id int64) ([]byte, error) {
	if b, err := a.store.body(id); b != nil || err != nil {
		return b, err
	}
	label, uid, err := a.store.location(id)
	if err != nil {
		return nil, err
	}
	if err := a.imap(func(s *session) error { return a.store.fetchBodies(s, label, []imap.UID{imap.UID(uid)}) }); err != nil {
		return nil, err
	}
	b, err := a.store.body(id)
	if b == nil && err == nil {
		err = errors.New("the server returned no message; it may have been moved or deleted, try syncing")
	}
	return b, err
}

// Open returns a message ready to render and marks it read, here and on the server.
func (a *App) Open(id int64) (Msg, error) {
	m, err := a.store.Get(id)
	if err != nil {
		return m, err
	}
	raw, err := a.raw(id)
	if err != nil {
		return m, err
	}
	p, err := parseMsg(raw)
	if err != nil {
		return m, err
	}
	m.HTML = rewrite(p.HTML, proxied)
	m.Attachments = p.Attachments
	if m.Labels, err = a.store.labelsOf(id); err != nil {
		return m, err
	}
	if !m.Read {
		m.Read = true
		a.SetFlag(id, `\Seen`, true)
	}
	return m, nil
}

// SaveAttachment asks where to save attachment i of message id and writes it
// there. It returns the path, or "" if the user cancelled.
// ponytail: save only; opening attachments in place would mean running unknown files.
func (a *App) SaveAttachment(id int64, i int) (string, error) {
	raw, err := a.raw(id)
	if err != nil {
		return "", err
	}
	p, err := parseMsg(raw)
	if err != nil {
		return "", err
	}
	if i < 0 || i >= len(p.Attachments) {
		return "", errors.New("no such attachment")
	}
	at := p.Attachments[i]
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title: "Save attachment", DefaultDirectory: a.saveDir(), DefaultFilename: safeName(at.Name),
	})
	if err != nil || path == "" {
		return "", err
	}
	return path, os.WriteFile(path, at.data, 0o644)
}

func (a *App) SaveEmail(id int64) (string, error) {
	m, err := a.store.Get(id)
	if err != nil {
		return "", err
	}
	raw, err := a.raw(id)
	if err != nil {
		return "", err
	}
	p, err := parseMsg(raw)
	if err != nil {
		return "", err
	}
	m.HTML = p.HTML
	return saveEmail(a.dir, a.saveDir(), m)
}
