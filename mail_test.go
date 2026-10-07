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

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const fixture = "From: Morning Brew <crew@MorningBrew.com>\r\n" +
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

	s, err := openStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	tx, _ := s.db.Begin()
	if err := insertMsg(tx, m, "News"); err != nil {
		t.Fatal(err)
	}
	if err := insertMsg(tx, m, "Finance"); err != nil { // same issue, second label: dedupe
		t.Fatal(err)
	}
	tx.Commit()
	for _, q := range []Query{{Search: `caf"`}, {Search: "pric"}, {Label: "Finance"}, {Sender: "crew@morningbrew.com"}} {
		if l, err := s.List(q); err != nil || len(l) != 1 {
			t.Errorf("List(%+v) = %d, %v", q, len(l), err)
		}
	}
	if l, _ := s.List(Query{Search: "bitcoin"}); len(l) != 0 {
		t.Errorf("search false positive")
	}
	if lbl, _ := s.Labels(); len(lbl) != 2 || lbl[0].Unread != 1 {
		t.Errorf("labels %+v", lbl)
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

// memIMAP starts go-imap's in-memory server with labels News and Fin and
// returns a logged-in client plus a helper that files fixture copy i under box.
func memIMAP(t *testing.T) (*imapclient.Client, func(box string, i int)) {
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("me", "pw")
	mem.AddUser(u)
	u.Create("News", nil)
	u.Create("Fin", nil)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
	})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	cl, err := imapclient.DialInsecure(ln.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	cl.Login("me", "pw").Wait()
	add := func(box string, i int) {
		raw := []byte(strings.Replace(fixture, "<abc@mb>", fmt.Sprintf("<m%d@x>", i), 1))
		cmd := cl.Append(box, int64(len(raw)), nil)
		cmd.Write(raw)
		cmd.Close()
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	return cl, add
}

// TestSync runs the real sync against go-imap's in-memory server: batching,
// resume from last UID, dedupe across labels, and that the mailbox is untouched.
func TestSync(t *testing.T) {
	cl, add := memIMAP(t)
	for i := 0; i < 120; i++ {
		add("News", i)
	}
	add("Fin", 7) // same issue filed under two labels

	s, _ := openStore(filepath.Join(t.TempDir(), "t.db"))
	defer s.db.Close()
	count := func() (n int) { s.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&n); return }
	var calls []int
	if err := s.syncLabel(cl, "News", func(n int) { calls = append(calls, n) }); err != nil {
		t.Fatal(err)
	}
	if count() != 120 || fmt.Sprint(calls) != "[50 100 120]" {
		t.Fatalf("first sync: %d rows, progress %v", count(), calls)
	}
	add("News", 500)
	calls = nil
	s.syncLabel(cl, "News", func(n int) { calls = append(calls, n) })
	if count() != 121 || fmt.Sprint(calls) != "[1]" {
		t.Fatalf("resume: %d rows, progress %v", count(), calls)
	}
	s.syncLabel(cl, "Fin", func(int) {})
	if l, _ := s.List(Query{Label: "Fin"}); count() != 121 || len(l) != 1 {
		t.Fatalf("dedupe: %d rows, Fin has %d", count(), len(l))
	}

	cl.Select("News", nil).Wait()
	msgs, _ := cl.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect()
	if len(msgs[0].Flags) != 0 {
		t.Errorf("sync changed server flags: %v", msgs[0].Flags)
	}
}

// TestPushSeen: issues opened in the app, and only those, get \Seen on the
// server; ids without a Message-ID are dropped; the queue empties.
func TestPushSeen(t *testing.T) {
	cl, add := memIMAP(t)
	for i := 0; i < 3; i++ {
		add("News", i)
	}
	add("Fin", 1) // Gmail shows one message under two labels; the fake server has two copies
	s, _ := openStore(filepath.Join(t.TempDir(), "t.db"))
	defer s.db.Close()
	s.syncLabel(cl, "News", func(int) {})
	s.syncLabel(cl, "Fin", func(int) {})
	tx, _ := s.db.Begin()
	insertMsg(tx, &Msg{MessageID: "nomsgid-1", Subject: "x"}, "News")
	tx.Commit()

	id := func(mid string) (n int64) {
		s.db.QueryRow(`SELECT id FROM messages WHERE message_id=?`, mid).Scan(&n)
		return
	}
	s.queueSeen(id("m1@x")) // pushed via Fin, the first of its labels
	s.queueSeen(id("m2@x"))
	s.queueSeen(id("m2@x")) // opened twice
	s.queueSeen(id("nomsgid-1"))
	if err := s.pushSeen(cl); err != nil {
		t.Fatal(err)
	}
	seen := func(box string) (out []bool) {
		cl.Select(box, &imap.SelectOptions{ReadOnly: true}).Wait()
		msgs, _ := cl.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{Flags: true}).Collect()
		for _, m := range msgs {
			out = append(out, slices.Contains(m.Flags, imap.FlagSeen))
		}
		return
	}
	if got := fmt.Sprint(seen("News"), seen("Fin")); got != "[false false true] [true]" {
		t.Errorf("seen flags News, Fin: %s", got)
	}
	var left int
	s.db.QueryRow(`SELECT count(*) FROM pending_seen`).Scan(&left)
	if left != 0 {
		t.Errorf("%d left in queue", left)
	}
	if err := s.pushSeen(cl); err != nil { // empty queue is a no-op
		t.Fatal(err)
	}
}

// TestOpenQueuesSeen: opening an unread issue queues it for Gmail; opening a
// read one doesn't; a failed push (no account here) leaves it queued.
func TestOpenQueuesSeen(t *testing.T) {
	s, _ := openStore(filepath.Join(t.TempDir(), "t.db"))
	defer s.db.Close()
	tx, _ := s.db.Begin()
	insertMsg(tx, &Msg{MessageID: "a@x", Subject: "unread"}, "News")
	insertMsg(tx, &Msg{MessageID: "b@x", Subject: "read", Read: true}, "News")
	tx.Commit()
	a := &App{store: s}
	for _, id := range []int64{1, 2, 1} {
		if _, err := a.Open(id); err != nil {
			t.Fatal(err)
		}
	}
	a.pushSeen() // what the background push does: fails to dial, keeps the row
	var ids []int64
	rows, _ := s.db.Query(`SELECT message_id FROM pending_seen`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if m, _ := s.Get(1); fmt.Sprint(ids) != "[1]" || !m.Read {
		t.Errorf("queued %v, read %v", ids, m.Read)
	}
}
