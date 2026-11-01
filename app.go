package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
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

	idleMu       sync.Mutex
	idle         *imapclient.Client // the Inbox watch connection, closed to restart it
	dialIdle     func(*imapclient.Options) (*imapclient.Client, error)
	inboxChanged func() // what a change in the Inbox triggers; Sync, or a counter in tests
}

func NewApp(dir string, portable bool, store *Store) (*App, error) {
	c, err := loadConfig(dir, portable)
	if err != nil {
		return nil, err
	}
	a := &App{dir: dir, store: store, cfg: c}
	a.dial = func() (*imapclient.Client, error) { return dial(a.config(), clientOpts) }
	a.dialIdle = func(o *imapclient.Options) (*imapclient.Client, error) { return dial(a.config(), o) }
	a.inboxChanged = a.Sync
	return a, nil
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go func() {
		sweepImages(a.dir, imageTTL)
		os.RemoveAll(openDir()) // files still open in a viewer stay until next time
	}()
	go a.watchInbox()
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
	c.PasswordError = ""
	if err := saveConfig(a.dir, c); err != nil {
		a.mu.Unlock()
		return err
	}
	a.cfg = c
	a.mu.Unlock()
	a.dropConn() // the account may have changed
	a.dropIdle()
	return nil
}

// SetAppearance saves only the display settings, so unsaved account edits stay unsaved.
func (a *App) SetAppearance(density, layout string, scale int) error {
	if density != "compact" {
		density = "comfortable"
	}
	if layout != "table" {
		layout = "cards"
	}
	scale = min(max(scale, 80), 150)
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.cfg
	c.Density, c.Layout, c.Scale = density, layout, scale
	if err := saveConfig(a.dir, c); err != nil {
		return err
	}
	a.cfg = c
	return nil
}

func (a *App) SetReaderZoom(zoom int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.cfg
	c.ReaderZoom = min(max(zoom, 50), 200)
	if err := saveConfig(a.dir, c); err != nil {
		return err
	}
	a.cfg = c
	return nil
}

type ServerFolder struct {
	Name    string `json:"name"`
	Default bool   `json:"default"` // ticked unless the user says otherwise
}

// ServerFolders lists the account's folders for the Settings picker. It signs in
// with the address and password being edited, so nothing is saved until Save.
func (a *App) ServerFolders(user, password string) ([]ServerFolder, error) {
	c := a.config()
	c.User, c.Password = user, password
	if c.Host == "" {
		c.Host = "imap.gmail.com:993"
	}
	cl, err := dial(c, clientOpts)
	if err != nil {
		return nil, err
	}
	defer cl.Logout()
	fs, err := listFolders(cl)
	if err != nil {
		return nil, err
	}
	var out []ServerFolder
	for _, f := range fs {
		out = append(out, ServerFolder{f.Name, defaultSynced(f)})
	}
	return out, nil
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

// watchInbox holds an IDLE connection on the Inbox and syncs as soon as the
// server reports a change, so new mail arrives in seconds rather than at the
// next poll. Other folders still sync on the timer.
func (a *App) watchInbox() {
	for {
		if err := a.idleInbox(); err != nil {
			log.Printf("inbox watch: %v", err)
		}
		time.Sleep(time.Minute) // offline, no account yet, or settings changed: try again soon
	}
}

// idleInbox runs one IDLE session until the connection ends.
func (a *App) idleInbox() error {
	c := a.config()
	inbox := a.store.roleFolder("inbox")
	if c.Password == "" || inbox == "" || !slices.Contains(c.Labels, inbox) {
		return nil
	}
	changed := make(chan struct{}, 1)
	poke := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	opts := *clientOpts
	opts.UnilateralDataHandler = &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { poke() },
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				poke()
			}
		},
		Fetch: func(m *imapclient.FetchMessageData) { m.Collect(); poke() }, // read/star changed elsewhere
	}
	cl, err := a.dialIdle(&opts)
	if err != nil {
		return err
	}
	a.idleMu.Lock()
	a.idle = cl
	a.idleMu.Unlock()
	defer cl.Close()
	if _, err := cl.Select(inbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return err
	}
	for {
		idle, err := cl.Idle()
		if err != nil {
			return err
		}
		ended := make(chan error, 1)
		go func() { ended <- idle.Wait() }()
		select {
		case err := <-ended: // connection dropped or closed by dropIdle
			return err
		case <-changed:
		}
		if err := idle.Close(); err != nil {
			return err
		}
		if err := <-ended; err != nil {
			return err
		}
		a.inboxChanged()
	}
}

func (a *App) dropIdle() {
	a.idleMu.Lock()
	defer a.idleMu.Unlock()
	if a.idle != nil {
		a.idle.Close()
		a.idle = nil
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
	ss := &session{cl: cl}
	defer func() { ss.cl.Logout() }()
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
		recovered := map[imap.UID]bool{}
		for {
			err := a.store.syncLabel(ss, l, func(n int) {
				done = n
				a.emit(SyncStatus{Running: true, Label: l, Count: fetched + n})
			})
			fetched += done
			done = 0
			var malformed *bodyStructureError
			if !errors.As(err, &malformed) || recovered[malformed.uid] {
				if err != nil {
					return err
				}
				break
			}
			recovered[malformed.uid] = true
			// A decode error closes the connection. Read this message as raw MIME instead.
			ss.cl.Close()
			cl, err := a.dial()
			if err != nil {
				return err
			}
			ss.cl, ss.sel = cl, ""
			if err := a.store.fetchBodies(ss, l, []imap.UID{malformed.uid}); err != nil {
				return err
			}
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

// Open returns a message ready to render and marks it read, here and on the
// server. Remote images load unless Settings blocks them; then only when images
// is true or the sender is on the allow list, and m.Blocked counts the rest.
func (a *App) Open(id int64, images bool) (Msg, error) {
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
	c := a.config()
	src := proxied
	m.ImagesAllowed = images || !c.BlockImages || slices.Contains(c.ImageSenders, m.FromAddr)
	if !m.ImagesAllowed {
		src = func(string) string { m.Blocked++; return "" }
	}
	m.HTML = rewrite(p.HTML, src, m.ImagesAllowed)
	m.Attachments = p.Attachments
	m.ToAddrs, m.CcAddrs = p.ToAddrs, p.CcAddrs
	if m.Labels, err = a.store.labelsOf(id); err != nil {
		return m, err
	}
	if !m.Read {
		m.Read = true
		a.SetFlag(id, `\Seen`, true)
	}
	return m, nil
}

// AllowImages always loads remote images in mail from sender.
func (a *App) AllowImages(sender string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.cfg
	if !slices.Contains(c.ImageSenders, sender) {
		c.ImageSenders = append(slices.Clone(c.ImageSenders), sender)
	}
	if err := saveConfig(a.dir, c); err != nil {
		return err
	}
	a.cfg = c
	return nil
}

func externalURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Hostname() == "" {
			return "", errors.New("link has no host")
		}
	case "mailto":
		if u.Opaque == "" {
			return "", errors.New("mail link has no address")
		}
	default:
		return "", errors.New("only web and mail links can be opened")
	}
	return u.String(), nil
}

// ShellExecute passes the URL directly to its handler without a command shell.
func (a *App) OpenLink(raw string) error {
	u, err := externalURL(raw)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, nil, p, nil, nil, windows.SW_SHOWNORMAL)
}

func (a *App) attachment(id int64, i int) (Attachment, error) {
	raw, err := a.raw(id)
	if err != nil {
		return Attachment{}, err
	}
	p, err := parseMsg(raw)
	if err != nil {
		return Attachment{}, err
	}
	if i < 0 || i >= len(p.Attachments) {
		return Attachment{}, errors.New("no such attachment")
	}
	return p.Attachments[i], nil
}

// SaveAttachment asks where to save attachment i of message id and writes it
// there. It returns the path, or "" if the user cancelled.
func (a *App) SaveAttachment(id int64, i int) (string, error) {
	at, err := a.attachment(id, i)
	if err != nil {
		return "", err
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title: "Save attachment", DefaultDirectory: a.saveDir(), DefaultFilename: safeName(at.Name),
	})
	if err != nil || path == "" {
		return "", err
	}
	return path, os.WriteFile(path, at.data, 0o644)
}

// viewable lists the attachment types that may open in their default app:
// documents, images and media. Programs, scripts, shortcuts, HTML and
// archives can carry code that runs on open, so those can only be saved.
var viewable = map[string]bool{
	".pdf": true, ".txt": true, ".csv": true, ".ics": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".heic": true,
	".docx": true, ".xlsx": true, ".pptx": true, ".odt": true, ".ods": true, ".odp": true,
	".mp3": true, ".m4a": true, ".wav": true, ".mp4": true, ".mov": true,
}

// canOpen goes by the name the file would be written under, so tricks like a
// trailing dot or a stream name can't turn a program into a "document".
func canOpen(name string) bool { return viewable[strings.ToLower(filepath.Ext(safeName(name)))] }

// openDir holds attachments opened in place; emptied at startup, since the
// apps viewing them keep the files locked while open.
func openDir() string { return filepath.Join(os.TempDir(), "lightmail-open") }

// OpenAttachment writes attachment i to a temporary folder and opens it in its
// default app. The file is marked as downloaded from the internet (Mark of the
// Web), so Office opens it in Protected View and SmartScreen still applies.
func (a *App) OpenAttachment(id int64, i int) error {
	at, err := a.attachment(id, i)
	if err != nil {
		return err
	}
	if !at.Open {
		return fmt.Errorf("%s could run code when opened, so save it instead", at.Name)
	}
	os.MkdirAll(openDir(), 0o700)
	dir, err := os.MkdirTemp(openDir(), "")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, safeName(at.Name))
	if err := os.WriteFile(path, at.data, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o600); err != nil {
		return fmt.Errorf("couldn't mark the file as downloaded, so it wasn't opened: %w", err)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, nil, p, nil, nil, windows.SW_SHOWNORMAL)
}

func (a *App) SaveEmail(id int64, images bool) (string, error) {
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
	c := a.config()
	return saveEmail(a.dir, a.saveDir(), m, images || !c.BlockImages || slices.Contains(c.ImageSenders, m.FromAddr))
}
