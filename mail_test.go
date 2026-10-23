package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const fixture = "From: Morning Brew <crew@MorningBrew.com>\r\n" +
	"To: Priya Nair <priya@x.com>, bob@x.com\r\n" +
	"Subject: =?utf-8?q?Caf=C3=A9_markets?=\r\n" +
	"Date: Mon, 21 Sep 2026 07:00:00 +0000\r\n" +
	"Message-ID: <abc@mb>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=XX\r\n\r\n" +
	"--XX\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain version\r\n" +
	"--XX\r\nContent-Type: text/html; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
	"<html><head><meta http-equiv=3D\"refresh\" content=3D\"0;url=3Dx\"><style>.a{}</style></head><body>" +
	"<p>Caf=E9 prices rose.</p><img src=3D\"https://cdn.x/a.png\"><img src=3D\"https://t.x/p.gif\" width=3D\"1\" height=3D\"1\">" +
	"<script>alert(1)</script></body></html>\r\n--XX--\r\n"

func fixtureN(i int) []byte {
	return []byte(strings.Replace(fixture, "<abc@mb>", fmt.Sprintf("<m%d@x>", i), 1))
}

func testStore(t *testing.T) *Store {
	s, err := openStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

func TestPipeline(t *testing.T) {
	m, err := parseMsg([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Café markets" || m.FromAddr != "crew@morningbrew.com" || m.FromName != "Morning Brew" || m.MessageID != "abc@mb" {
		t.Fatalf("headers: %+v", m)
	}
	if m.Text != "Café prices rose." {
		t.Fatalf("text %q", m.Text)
	}

	out := rewrite(m.HTML, proxied)
	for _, want := range []string{`src="/img?u=https%3A%2F%2Fcdn.x%2Fa.png"`, "Content-Security-Policy"} {
		if !strings.Contains(out, want) {
			t.Errorf("rewrite missing %s:\n%s", want, out)
		}
	}
	for _, bad := range []string{"p.gif", "<script", "refresh"} {
		if strings.Contains(out, bad) {
			t.Errorf("rewrite kept %s:\n%s", bad, out)
		}
	}

	s := testStore(t)
	s.setFolders([]Folder{{Name: "News"}, {Name: "Finance"}, {Name: "Trash", Role: "trash"}})
	tx, _ := s.db.Begin()
	for _, l := range []string{"News", "Finance"} { // same message, second label: dedupe
		if err := insertHeader(tx, m, l, 7); err != nil {
			t.Fatal(err)
		}
	}
	insertHeader(tx, &Msg{MessageID: "gone@x", Subject: "binned"}, "Trash", 1)
	tx.Commit()
	for _, q := range []Query{{Search: `caf"`}, {Label: "Finance"}, {Sender: "crew@morningbrew.com"}, {}} {
		if l, err := s.List(q); err != nil || len(l) != 1 {
			t.Errorf("List(%+v) = %d, %v", q, len(l), err)
		}
	}
	if l, _ := s.List(Query{Search: "pric"}); len(l) != 0 {
		t.Errorf("body text searchable before the body was downloaded")
	}
	if _, err := s.setBody("News", 7, []byte(fixture)); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(Query{Search: "pric"}); len(l) != 1 {
		t.Errorf("body search after setBody: %d", len(l))
	}
	if l, _ := s.List(Query{Search: "bitcoin"}); len(l) != 0 {
		t.Errorf("search false positive")
	}
	if fs, _ := s.Folders(); len(fs) != 3 || fs[0].Name != "Finance" || fs[0].Unread != 1 {
		t.Errorf("folders %+v", fs)
	}
}

func TestFetchImageCaches(t *testing.T) {
	png := "\x89PNG\r\n\x1a\nfake"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(png)) }))
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "img"), 0o755)
	for i := 0; i < 2; i++ { // second call must come from disk
		b, ct, err := fetchImage(dir, srv.URL+"/a.png")
		if err != nil || string(b) != png || ct != "image/png" {
			t.Fatalf("call %d: %q %q %v", i, b, ct, err)
		}
		srv.Close()
	}
	if _, _, err := fetchImage(dir, "file:///c:/windows/win.ini"); err == nil {
		t.Error("non-http url accepted")
	}
}

// fakeIMAP is go-imap's in-memory server with INBOX, News, Fin, All Mail and
// Trash, speaking MOVE and UIDPLUS like Gmail. It drops special-use roles
// (go-imap beta bug), so tests that need them set the folders table directly.
type fakeIMAP struct {
	t    *testing.T
	addr string
	cl   *imapclient.Client // for arranging and inspecting server state
}

func newFakeIMAP(t *testing.T) *fakeIMAP {
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("me", "pw")
	mem.AddUser(u)
	for _, n := range []string{"INBOX", "News", "Fin"} {
		u.Create(n, nil)
	}
	u.Create("All Mail", nil)
	u.Create("Trash", nil)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapMove: {}, imap.CapUIDPlus: {},
			imap.CapSpecialUse: {}, imap.CapListExtended: {}},
		InsecureAuth: true,
	})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	f := &fakeIMAP{t: t, addr: ln.Addr().String()}
	f.cl = f.dial()
	return f
}

func (f *fakeIMAP) dial() *imapclient.Client {
	cl, err := imapclient.DialInsecure(f.addr, clientOpts)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { cl.Close() })
	if err := cl.Login("me", "pw").Wait(); err != nil {
		f.t.Fatal(err)
	}
	return cl
}

// add files fixture copy i under box.
func (f *fakeIMAP) add(box string, i int, flags ...imap.Flag) {
	raw := fixtureN(i)
	cmd := f.cl.Append(box, int64(len(raw)), &imap.AppendOptions{Flags: flags})
	cmd.Write(raw)
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		f.t.Fatal(err)
	}
}

// flags returns each message's flags in box, in UID order.
func (f *fakeIMAP) flags(box string) (out [][]imap.Flag) {
	if _, err := f.cl.Select(box, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		f.t.Fatal(err)
	}
	msgs, _ := f.cl.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{Flags: true}).Collect()
	for _, m := range msgs {
		slices.Sort(m.Flags)
		out = append(out, m.Flags)
	}
	return
}

func idOf(t *testing.T, s *Store, msgID string) (id int64) {
	if err := s.db.QueryRow(`SELECT id FROM messages WHERE message_id=?`, msgID).Scan(&id); err != nil {
		t.Fatalf("%s: %v", msgID, err)
	}
	return
}

func count(s *Store, q string, args ...any) (n int) {
	s.db.QueryRow(q, args...).Scan(&n)
	return
}

// TestSync: header-only sync with batching, resume from the last UID, dedupe
// across folders, and that syncing changes nothing on the server.
func TestSync(t *testing.T) {
	defer func(b int) { batch = b }(batch)
	batch = 50
	f := newFakeIMAP(t)
	for i := 0; i < 120; i++ {
		f.add("News", i)
	}
	f.add("Fin", 7) // same message filed under two labels

	s := testStore(t)
	ss := &session{cl: f.dial()}
	var calls []int
	if err := s.syncLabel(ss, "News", func(n int) { calls = append(calls, n) }); err != nil {
		t.Fatal(err)
	}
	if n := count(s, `SELECT count(*) FROM messages`); n != 120 || fmt.Sprint(calls) != "[50 100 120]" {
		t.Fatalf("first sync: %d rows, progress %v", n, calls)
	}
	if n := count(s, `SELECT count(*) FROM bodies`); n != 0 {
		t.Errorf("sync downloaded %d bodies", n)
	}
	m, _ := s.Get(idOf(t, s, "m3@x"))
	if m.Subject != "Café markets" || m.FromName != "Morning Brew" || m.To != "Priya Nair, bob@x.com" || m.Date != 1789974000 {
		t.Errorf("header row: %+v", m)
	}
	f.add("News", 500)
	calls = nil
	s.syncLabel(ss, "News", func(n int) { calls = append(calls, n) })
	if n := count(s, `SELECT count(*) FROM messages`); n != 121 || fmt.Sprint(calls) != "[1]" {
		t.Fatalf("resume: %d rows, progress %v", n, calls)
	}
	s.syncLabel(ss, "Fin", func(int) {})
	if l, _ := s.List(Query{Label: "Fin"}); count(s, `SELECT count(*) FROM messages`) != 121 || len(l) != 1 {
		t.Fatalf("dedupe: Fin has %d", len(l))
	}
	if fl := f.flags("News"); len(fl[0]) != 0 {
		t.Errorf("sync changed server flags: %v", fl[0])
	}
}

// TestReconcile: changes made by another client (delete, read, star) reach
// the cache, except where a local change is still waiting to be pushed.
func TestReconcile(t *testing.T) {
	f := newFakeIMAP(t)
	for i := 0; i < 4; i++ {
		f.add("INBOX", i)
	}
	s := testStore(t)
	ss := &session{cl: f.dial()}
	s.syncLabel(ss, "INBOX", func(int) {})
	s.setFlag(idOf(t, s, "m3@x"), `\Seen`, true) // local, not pushed yet

	f.cl.Select("INBOX", nil).Wait()
	f.cl.Store(imap.UIDSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
	f.cl.Expunge().Close()
	f.cl.Store(imap.UIDSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close()
	f.cl.Store(imap.UIDSetNum(3), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged}}, nil).Close()

	if err := s.syncLabel(ss, "INBOX", func(int) {}); err != nil {
		t.Fatal(err)
	}
	s.prune([]string{"INBOX"})
	got := ""
	l, _ := s.List(Query{Label: "INBOX", Sort: "old"})
	for _, m := range l {
		got += fmt.Sprintf("%d read=%v star=%v; ", m.ID, m.Read, m.Flagged)
	}
	if want := "2 read=true star=false; 3 read=false star=true; 4 read=true star=false; "; got != want {
		t.Errorf("after reconcile:\n%s\nwant\n%s", got, want)
	}
	if n := count(s, `SELECT count(*) FROM messages`); n != 3 {
		t.Errorf("deleted message still cached: %d rows", n)
	}
}

// TestPushFlags: queued read/star changes reach the server, through any one
// folder holding the message; the queue empties.
func TestPushFlags(t *testing.T) {
	f := newFakeIMAP(t)
	for i := 0; i < 3; i++ {
		f.add("News", i)
	}
	f.add("Fin", 1, imap.FlagFlagged) // Gmail shows one message under two labels; the fake has two copies
	s := testStore(t)
	ss := &session{cl: f.dial()}
	s.syncLabel(ss, "News", func(int) {})
	s.syncLabel(ss, "Fin", func(int) {})

	s.setFlag(idOf(t, s, "m1@x"), `\Seen`, true) // pushed via Fin, the first of its labels
	s.setFlag(idOf(t, s, "m1@x"), `\Flagged`, false)
	s.setFlag(idOf(t, s, "m2@x"), `\Seen`, true)
	s.setFlag(idOf(t, s, "m2@x"), `\Flagged`, true)
	if err := s.pushFlags(ss); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(f.flags("News"), f.flags("Fin")); got != `[[] [] [\Flagged \Seen]] [[\Seen]]` {
		t.Errorf("flags News, Fin: %s", got)
	}
	if n := count(s, `SELECT count(*) FROM pending_flags`); n != 0 {
		t.Errorf("%d left in queue", n)
	}
}

// TestOpen: the first open downloads and caches the body, marks the message
// read on the server, and makes its text searchable; later opens work offline.
func TestOpen(t *testing.T) {
	f := newFakeIMAP(t)
	f.add("INBOX", 1)
	s := testStore(t)
	s.syncLabel(&session{cl: f.dial()}, "INBOX", func(int) {})
	a := &App{store: s, dir: t.TempDir(), dial: func() (*imapclient.Client, error) { return f.dial(), nil }}

	m, err := a.Open(1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.HTML, "prices rose") || !m.Read || fmt.Sprint(m.Labels) != "[INBOX]" {
		t.Fatalf("open: read=%v labels=%v html=%q", m.Read, m.Labels, m.HTML)
	}
	if strings.Contains(m.HTML, "/img?u=") || m.Blocked != 1 {
		t.Errorf("remote image not held back: blocked=%d html=%q", m.Blocked, m.HTML)
	}
	if m, _ := a.Open(1, true); !strings.Contains(m.HTML, "/img?u=") || m.Blocked != 0 {
		t.Errorf("Load images: blocked=%d", m.Blocked)
	}
	a.AllowImages("crew@morningbrew.com")
	if m, _ := a.Open(1, false); !strings.Contains(m.HTML, "/img?u=") {
		t.Errorf("allowed sender's images still blocked")
	}
	a.pushFlags() // what the background push does
	if fl := f.flags("INBOX"); !slices.Contains(fl[0], imap.FlagSeen) {
		t.Errorf("not marked read on server: %v", fl)
	}
	if l, _ := s.List(Query{Search: "prices"}); len(l) != 1 {
		t.Errorf("body not searchable after open")
	}
	a.dial = func() (*imapclient.Client, error) { return nil, fmt.Errorf("offline") }
	a.dropConn()
	if _, err := a.Open(1, false); err != nil {
		t.Errorf("second open needed the server: %v", err)
	}
}

func TestFoldersAndPrune(t *testing.T) {
	f := newFakeIMAP(t)
	f.add("News", 1)
	f.add("Fin", 1)
	f.add("Fin", 2)
	fs, err := listFolders(f.cl)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(fs) != "[{All Mail  0 0 false} {Fin  0 0 false} {INBOX inbox 0 0 false} {News  0 0 false} {Trash  0 0 false}]" {
		t.Errorf("folders: %v", fs)
	}
	for attrs, want := range map[imap.MailboxAttr]string{imap.MailboxAttrAll: "all:false", imap.MailboxAttrTrash: "trash:true", imap.MailboxAttrHasNoChildren: ":true"} {
		f := folderOf(&imap.ListData{Mailbox: "[Gmail]/X", Attrs: []imap.MailboxAttr{imap.MailboxAttrHasNoChildren, attrs}})
		if got := fmt.Sprintf("%s:%v", f.Role, defaultSynced(f)); got != want {
			t.Errorf("%s: %s, want %s", attrs, got, want)
		}
	}

	s := testStore(t)
	ss := &session{cl: f.dial()}
	s.syncLabel(ss, "News", func(int) {})
	s.syncLabel(ss, "Fin", func(int) {})
	s.prune([]string{"News"}) // Fin unticked
	if n := count(s, `SELECT count(*) FROM messages`); n != 1 {
		t.Errorf("%d messages after unticking Fin, want 1 (m1 lives on in News)", n)
	}
	if v, _ := s.labelState("Fin"); v != 0 {
		t.Errorf("Fin sync state kept")
	}
}

// TestOldCacheReplaced: a cache from an older schema is rebuilt, not half-used.
func TestOldCacheReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, _ := openStore(path)
	old.db.Exec(`DROP TABLE message_labels`)
	old.db.Exec(`CREATE TABLE message_labels(message_id INTEGER, label TEXT)`)
	old.db.Exec(`PRAGMA user_version=1`)
	old.db.Close()
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if n := count(s, `SELECT count(*) FROM pragma_table_info('message_labels') WHERE name='uid'`); n != 1 {
		t.Error("old message_labels kept")
	}
}

// TestAttachments: inline cid: images become data URIs and stay out of the
// attachment list; real attachments are listed in order with their bytes.
func TestAttachments(t *testing.T) {
	raw := "From: a@x\r\nSubject: report\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=M\r\n\r\n" +
		"--M\r\nContent-Type: multipart/related; boundary=R\r\n\r\n" +
		"--R\r\nContent-Type: text/html\r\n\r\n<p>Chart:</p><img src=\"cid:chart@x\">\r\n" +
		"--R\r\nContent-Type: image/png\r\nContent-Id: <chart@x>\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw==\r\n" +
		"--R--\r\n" +
		"--M\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"Q3 report.pdf\"\r\n\r\n%PDF-1.4\r\n" +
		"--M--\r\n"
	m, err := parseMsg([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.HTML, `src="data:image/png;base64,iVBORw=="`) {
		t.Errorf("cid image not inlined: %s", m.HTML)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Name != "Q3 report.pdf" || m.Attachments[0].Type != "application/pdf" ||
		string(m.Attachments[0].data) != "%PDF-1.4" {
		t.Errorf("attachments %+v", m.Attachments)
	}
	if out := rewrite(m.HTML, proxied); !strings.Contains(out, "data:image/png") {
		t.Errorf("rewrite dropped the inlined image: %s", out)
	}
}

// TestMatchSkipsNewer: a message changed here after reconcile asked the server
// (a move, an undo, a pushed read) keeps the local state; the rest follow the server.
func TestMatchSkipsNewer(t *testing.T) {
	s := testStore(t)
	tx, _ := s.db.Begin()
	insertHeader(tx, &Msg{MessageID: "old@x"}, "INBOX", 1)
	insertHeader(tx, &Msg{MessageID: "new@x"}, "INBOX", 2)
	tx.Commit()
	since := s.mark()
	id := idOf(t, s, "new@x")
	s.touch(id) // say, undone back into the Inbox and read, after the SEARCH
	s.db.Exec(`UPDATE messages SET read=1 WHERE id=?`, id)

	if err := s.match("INBOX", map[uint32]bool{}, map[uint32]bool{2: true}, map[uint32]bool{}, since); err != nil {
		t.Fatal(err)
	}
	l, _ := s.List(Query{Label: "INBOX"})
	if len(l) != 1 || l[0].ID != id || !l[0].Read {
		t.Errorf("after match: %+v", l)
	}
}

func TestCanOpen(t *testing.T) {
	for name, want := range map[string]bool{
		"report.PDF": true, "photo.jpg": true, "setup.exe": false, "invoice.pdf.exe": false,
		"run.exe.": false, "x.exe:y.pdf": true, "page.html": false, "a.lnk": false, "noext": false,
	} {
		if got := canOpen(name); got != want {
			t.Errorf("canOpen(%q) = %v", name, got)
		}
	}
}

func TestSweepImages(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "img"), 0o755)
	old, fresh := filepath.Join(dir, "img", "old"), filepath.Join(dir, "img", "fresh")
	os.WriteFile(old, nil, 0o644)
	os.WriteFile(fresh, nil, 0o644)
	long := time.Now().Add(-2 * imageTTL)
	os.Chtimes(old, long, long)
	sweepImages(dir, imageTTL)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("stale image kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh image swept")
	}
}
