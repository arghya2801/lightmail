package main

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"golang.org/x/net/html"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBlockedImagePolicy(t *testing.T) {
	src := `<html><head><style>body{background-image:url('/img?u=https://tracker.example/css')}</style></head><body><img src="/img?u=https://tracker.example/relative"><img src="https://tracker.example/direct"><img src="data:image/png;base64,aW1hZ2U="></body></html>`
	out := rewrite(src, func(string) string { return "" }, false)
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	var policy string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Data == "meta" && attr(n, "http-equiv") == "Content-Security-Policy" {
			policy = attr(n, "content")
		}
		if n.Data == "img" {
			if u := attr(n, "src"); u != "" && !strings.HasPrefix(u, "data:image/") {
				t.Errorf("untrusted image URL retained: %s", u)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if !strings.Contains(policy, "img-src data:") || strings.Contains(policy, "img-src 'self'") {
		t.Fatalf("CSS images can bypass blocking: %s", policy)
	}
	if !strings.Contains(out, "data:image/png") {
		t.Fatal("blocking hid inline image")
	}
}

func TestImageSecurity(t *testing.T) {
	for _, address := range []string{"127.0.0.1:80", "192.168.1.1:80", "10.0.0.1:80", "172.16.1.1:80", "169.254.169.254:80", "100.64.0.1:80", "[::1]:80", "[::ffff:127.0.0.1]:80", "[fe80::1%eth0]:80", "[fc00::1]:80", "0.0.0.0:80", "224.0.0.1:80"} {
		if err := imageAddress("tcp", address, nil); err == nil {
			t.Errorf("accepted private address %s", address)
		}
	}
	for _, address := range []string{"8.8.8.8:443", "[2606:4700:4700::1111]:443"} {
		if err := imageAddress("tcp", address, nil); err != nil {
			t.Errorf("public address %s: %v", address, err)
		}
	}
	if _, _, err := fetchImage(t.TempDir(), "http://127.0.0.1:1/test"); err == nil || !strings.Contains(err.Error(), "private network") {
		t.Fatalf("local fetch: %v", err)
	}
	for _, body := range []string{"<html>active content</html>", "<svg xmlns=\"http://www.w3.org/2000/svg\"><script/></svg>"} {
		if _, err := imageType([]byte(body)); err == nil {
			t.Errorf("accepted active content %q", body)
		}
	}
	// A cache populated by an older version must not serve HTML under an image MIME type.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "img"), 0700)
	u := "https://example.com/cached"
	sum := sha256.Sum256([]byte(u))
	os.WriteFile(filepath.Join(dir, "img", hex.EncodeToString(sum[:])), []byte("image/png\n<html>unsafe</html>"), 0600)
	r := httptest.NewRecorder()
	imageHandler(dir).ServeHTTP(r, httptest.NewRequest("GET", "/img?u="+url.QueryEscape(u), nil))
	if r.Code != http.StatusBadGateway || r.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unsafe cache response: %d %v", r.Code, r.Header())
	}
}

func TestImageRedirectAndContent(t *testing.T) {
	old := httpc
	t.Cleanup(func() { httpc = old })
	for _, body := range []string{"<html>not an image</html>", "\x89PNG\r\n\x1a\nfake"} {
		httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}
		_, _, err := fetchImage(t.TempDir(), "https://example.com/image")
		if (err != nil) != strings.HasPrefix(body, "<html>") {
			t.Fatalf("content validation %q: %v", body, err)
		}
	}
	httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "example.com" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://127.0.0.1:1/private"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return old.Transport.RoundTrip(r)
	})}
	if _, _, err := fetchImage(t.TempDir(), "https://example.com/redirect"); err == nil || !strings.Contains(err.Error(), "private network") {
		t.Fatalf("redirect reached private destination: %v", err)
	}
}

func TestSaveEmailImagePermissions(t *testing.T) {
	_, s, a := mailbox(t)
	a.cfg.SaveDir = t.TempDir()
	a.cfg.BlockImages = true
	id := idOf(t, s, "m0@x")
	old := httpc
	t.Cleanup(func() { httpc = old })
	requests := 0
	httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\nfake")), Request: r}, nil
	})}
	for _, tc := range []struct {
		name              string
		once, all, sender bool
		want              int
	}{
		{"blocked", false, false, false, 0}, {"once", true, false, false, 1}, {"all", false, true, false, 1}, {"sender", false, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests = 0
			a.cfg.BlockImages = !tc.all
			a.cfg.ImageSenders = nil
			if tc.sender {
				a.cfg.ImageSenders = []string{"crew@morningbrew.com"}
			}
			path, err := a.SaveEmail(id, tc.once)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if requests != tc.want {
				t.Errorf("remote requests %d, want %d", requests, tc.want)
			}
			if strings.Contains(string(b), "https://cdn.x") {
				t.Error("export retained a remote image URL")
			}
			if strings.Contains(string(b), "data:image/png") != (tc.want > 0) {
				t.Error("export image permission ignored")
			}
		})
	}
}

func TestPasswordMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	plain := `{"user":"me@example.com","password":"secret app password","labels":["INBOX"]}`
	if err := os.WriteFile(path, []byte(plain), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(dir, false)
	if err != nil || c.Password != "secret app password" {
		t.Fatalf("migration: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]json.RawMessage
	if err := json.Unmarshal(b, &disk); err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["password"]; ok || strings.Contains(string(b), "secret app password") || len(disk["protectedPassword"]) == 0 {
		t.Fatal("password was not removed from settings")
	}
	c, err = loadConfig(dir, false)
	if err != nil || c.Password != "secret app password" {
		t.Fatalf("decrypt: %v", err)
	}
	if err := saveConfig(dir, c); err != nil {
		t.Fatalf("replace existing config: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"user":"me@example.com","protectedPassword":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = loadConfig(dir, true)
	if err != nil || c.Password != "" || c.PasswordError == "" {
		t.Fatal("unreadable portable secret did not ask for a password")
	}
	if err := saveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), `"protectedPassword": "invalid"`) {
		t.Fatal("appearance save destroyed an unreadable secret")
	}
	if err := os.WriteFile(path, []byte("broken json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(dir, false); err == nil {
		t.Fatal("malformed settings were silently replaced")
	}
}

func TestAllowedSendersAndFailedSettingsWrite(t *testing.T) {
	a := &App{dir: t.TempDir(), cfg: Config{User: "me@example.com", Password: "fixture-password", BlockImages: true}}
	if err := a.AllowImages("sender@example.com"); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(a.dir, false)
	if err != nil || len(c.ImageSenders) != 1 {
		t.Fatalf("allowed sender was not persisted: %v", err)
	}
	c.ImageSenders = nil
	if err := a.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	c, err = loadConfig(a.dir, false)
	if err != nil || len(c.ImageSenders) != 0 {
		t.Fatalf("sender removal was not persisted: %v", err)
	}
	a.dir = filepath.Join(t.TempDir(), "missing-directory")
	before := a.config()
	c = before
	c.User = "another@example.com"
	if err := a.SaveConfig(c); err == nil || a.config().User != before.User {
		t.Fatal("failed save changed the active account")
	}
	if err := a.SetAppearance("compact", "table", 125); err == nil || a.config().Scale != before.Scale {
		t.Fatal("failed appearance save changed active settings")
	}
	if err := a.AllowImages("other@example.com"); err == nil || len(a.config().ImageSenders) != 0 {
		t.Fatal("failed allow-list save changed active permissions")
	}
}

func TestExternalURL(t *testing.T) {
	for _, u := range []string{"https://example.com/a(b);c?q=x!y&next=1", "https://example.com/a%20b", "mailto:me@example.com?subject=Hello%20there"} {
		if _, err := externalURL(u); err != nil {
			t.Errorf("valid link %q: %v", u, err)
		}
	}
	for _, u := range []string{"javascript:alert(1)", "file:///C:/test.exe", "data:text/html,hello", "https:///missing-host", "mailto:", "https://example.com/\x00"} {
		if _, err := externalURL(u); err == nil {
			t.Errorf("unsafe link accepted: %q", u)
		}
	}
}

func TestMetadataMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE messages(id INTEGER PRIMARY KEY,message_id TEXT UNIQUE NOT NULL,from_name TEXT NOT NULL,from_addr TEXT NOT NULL,to_addrs TEXT NOT NULL,subject TEXT NOT NULL,date INTEGER NOT NULL,size INTEGER NOT NULL,read INTEGER NOT NULL DEFAULT 0,flagged INTEGER NOT NULL DEFAULT 0);
		CREATE TABLE bodies(id INTEGER PRIMARY KEY,raw BLOB NOT NULL);
		CREATE TABLE message_labels(message_id INTEGER NOT NULL,label TEXT NOT NULL,uid INTEGER NOT NULL,PRIMARY KEY(message_id,label));
		CREATE TABLE label_state(label TEXT PRIMARY KEY,uidvalidity INTEGER NOT NULL,last_uid INTEGER NOT NULL);
		CREATE TABLE folders(name TEXT PRIMARY KEY,role TEXT NOT NULL);
		CREATE TABLE pending_flags(message_id INTEGER NOT NULL,flag TEXT NOT NULL,value INTEGER NOT NULL,PRIMARY KEY(message_id,flag));
		CREATE VIRTUAL TABLE fts USING fts5(subject,from_name,text,content='',contentless_delete=1);
		INSERT INTO messages VALUES(1,'abc@mb','Morning Brew','crew@morningbrew.com','old recipients','Café markets',1,1,0,0);
		INSERT INTO messages VALUES(2,'unopened@x','Other','other@example.com','Legacy recipient','Unopened',2,1,0,0);
		INSERT INTO message_labels VALUES(1,'INBOX',1),(2,'INBOX',2);
		INSERT INTO pending_flags VALUES(1,'\Seen',1);
		PRAGMA user_version=2;`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bodies VALUES(1,?)`, pack([]byte(fixture))); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	for _, search := range []string{"crew@morningbrew.com", "prices", "other@example.com"} {
		rows, err := s.List(Query{Search: search})
		if err != nil || len(rows) != 1 {
			t.Errorf("backfilled search %q: %d, %v", search, len(rows), err)
		}
	}
	if count(s, `SELECT count(*) FROM pending_flags`) != 1 || count(s, `SELECT count(*) FROM bodies`) != 1 {
		t.Fatal("migration lost pending flags or bodies")
	}
	m, err := s.Get(1)
	if err != nil || len(m.ToAddrs) != 2 || !strings.Contains(m.ToAddrs[0], "priya@x.com") {
		t.Fatalf("recipient migration: %+v %v", m, err)
	}
	m, err = s.Get(2)
	if err != nil || m.To != "Legacy recipient" {
		t.Fatalf("unopened legacy message lost: %+v %v", m, err)
	}
}

func TestAttachmentAndRecipientSync(t *testing.T) {
	f := newFakeIMAP(t)
	raw := "From: Sender <sender@example.com>\r\nTo: Person <person@example.com>\r\nCc: Copy <copy@example.com>\r\nMessage-ID: <attachment@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=A\r\n\r\n--A\r\nContent-Type: text/plain\r\n\r\nhello\r\n--A\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=report.pdf\r\n\r\n%PDF\r\n--A--\r\n"
	cmd := f.cl.Append("INBOX", int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}})
	cmd.Write([]byte(raw))
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	s := testStore(t)
	ss := &session{cl: f.dial()}
	if err := s.syncLabel(ss, "INBOX", func(int) {}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Get(1)
	if err != nil || !m.HasAttachment || len(m.ToAddrs) != 1 || len(m.CcAddrs) != 1 || !strings.Contains(m.CcAddrs[0], "copy@example.com") {
		t.Fatalf("sync metadata: %+v %v", m, err)
	}
	if count(s, `SELECT count(*) FROM bodies`) != 0 {
		t.Fatal("attachment indicator downloaded the body")
	}
	// An existing UID must also receive metadata after migration, without a body download.
	s.db.Exec(`UPDATE messages SET has_attachment=-1,to_recipients='[]',cc_recipients='[]'`)
	if err := s.syncLabel(ss, "INBOX", func(int) {}); err != nil {
		t.Fatal(err)
	}
	m, err = s.Get(1)
	if err != nil || !m.HasAttachment || len(m.CcAddrs) != 1 {
		t.Fatalf("metadata backfill: %+v %v", m, err)
	}
	a := &App{store: s, dir: t.TempDir(), dial: func() (*imapclient.Client, error) { return f.dial(), nil }}
	m, err = a.Open(1, false)
	if err != nil || len(m.ToAddrs) != 1 || len(m.CcAddrs) != 1 {
		t.Fatalf("reader recipients: %+v %v", m, err)
	}
}

func TestSyncEmptyMultipart(t *testing.T) {
	f := newFakeIMAP(t)
	broken := "From: Sender <sender@example.com>\r\nTo: Person <person@example.com>\r\nMessage-ID: <empty@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=A\r\n\r\n--A\r\nContent-Type: multipart/mixed; boundary=EMPTY\r\n\r\n--EMPTY--\r\n--A\r\nContent-Type: text/html\r\n\r\n<p>Still readable</p>\r\n--A--\r\n"
	attachment := strings.Replace(broken, "<empty@example.com>", "<pdf@example.com>", 1)
	attachment = strings.Replace(attachment, "Content-Type: multipart/mixed; boundary=EMPTY\r\n\r\n--EMPTY--", "Content-Type: application/pdf\r\nContent-Disposition: attachment; filename=report.pdf\r\n\r\n%PDF", 1)
	if m, err := parseMsg([]byte(broken)); err != nil || !strings.Contains(m.HTML, "Still readable") {
		t.Fatalf("raw empty multipart: %+v %v", m, err)
	}
	// The fake server refuses to serialize empty multiparts. Inject Gmail's exact wire shape instead.
	serverMessage := strings.Replace(broken, "multipart/mixed; boundary=EMPTY", "application/x-empty", 1)
	for _, raw := range []string{fixture, serverMessage, attachment} {
		cmd := f.cl.Append("News", int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}})
		cmd.Write([]byte(raw))
		cmd.Close()
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	s := testStore(t)
	dials := 0
	a := &App{store: s, dial: func() (*imapclient.Client, error) {
		dials++
		conn, err := net.Dial("tcp", f.addr)
		if err != nil {
			return nil, err
		}
		cl := imapclient.New(&emptyMultipartConn{Conn: conn, reader: bufio.NewReader(conn)}, clientOpts)
		t.Cleanup(func() { cl.Close() })
		return cl, cl.Login("me", "pw").Wait()
	}}
	cl, err := a.dial()
	if err != nil {
		t.Fatal(err)
	}
	cl.Select("News", nil).Wait()
	_, err = cl.Fetch(imap.UIDSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{BodyStructure: &imap.FetchItemBodyStructure{Extended: true}}).Collect()
	cl.Close()
	if err == nil || !strings.Contains(err.Error(), `in body-type-mpart: in body-type-1part: imapwire: expected string, got "("`) {
		t.Fatalf("fixture did not reproduce Gmail's parser error: %v", err)
	}
	for pass := 0; pass < 3; pass++ {
		if pass == 1 {
			// Exercise metadata backfill for an existing UID as well as a fresh sync.
			s.db.Exec(`UPDATE messages SET has_attachment=-1 WHERE message_id='empty@example.com'`)
			if err := s.setFlag(idOf(t, s, "pdf@example.com"), `\Flagged`, true); err != nil {
				t.Fatal(err)
			}
		}
		if err := a.syncAll([]string{"News"}); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if count(s, `SELECT count(*) FROM messages`) != 3 || count(s, `SELECT count(*) FROM messages WHERE has_attachment=-1`) != 0 {
			t.Fatal("sync lost a message or left metadata unfinished")
		}
		if count(s, `SELECT count(*) FROM bodies`) != 1 {
			t.Fatal("recovery must download only the malformed message")
		}
		m, err := s.Get(idOf(t, s, "pdf@example.com"))
		if err != nil || !m.HasAttachment || len(m.ToAddrs) != 1 {
			t.Fatalf("valid attachment metadata lost: %+v %v", m, err)
		}
		m, err = a.Open(idOf(t, s, "empty@example.com"), false)
		if err != nil || !strings.Contains(m.HTML, "Still readable") {
			t.Fatalf("recovered message is unreadable: %+v %v", m, err)
		}
	}
	if dials != 6 { // One reproduction, two recovering syncs, and one ordinary sync.
		t.Fatalf("unexpected repeated recovery: %d connections", dials)
	}
	if count(s, `SELECT count(*) FROM pending_flags`) != 1 || count(s, `SELECT count(*) FROM messages WHERE flagged=1`) != 1 {
		t.Fatal("recovery lost a pending flag change")
	}
	for _, flags := range f.flags("News") {
		if !slices.Contains(flags, imap.FlagSeen) {
			t.Fatal("sync changed server flags")
		}
	}
}

type emptyMultipartConn struct {
	net.Conn
	reader  *bufio.Reader
	pending []byte
}

func (c *emptyMultipartConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		empty := regexp.MustCompile(`(?i)\("application" "x-empty" [^()]*\)`)
		c.pending = []byte(empty.ReplaceAllString(line, `("MIXED" ("BOUNDARY" "EMPTY") NIL NIL)`))
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func TestAttachmentMetadataParts(t *testing.T) {
	for _, tc := range []struct {
		name string
		part *imap.BodyStructureSinglePart
		want bool
	}{
		{"unnamed PDF", &imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf"}, true},
		{"inline CID image", &imap.BodyStructureSinglePart{Type: "image", Subtype: "png", ID: "logo@x", Params: map[string]string{"name": "logo.png"}}, false},
		{"CID document", &imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf", ID: "report@x", Params: map[string]string{"name": "report.pdf"}}, true},
		{"plain body", &imap.BodyStructureSinglePart{Type: "text", Subtype: "plain"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := envMsg(&imapclient.FetchMessageBuffer{Envelope: &imap.Envelope{}, BodyStructure: tc.part})
			if m.HasAttachment != tc.want {
				t.Errorf("attachment=%v, want %v", m.HasAttachment, tc.want)
			}
		})
	}
}

func TestRecipientDisplay(t *testing.T) {
	want := "Jörg <person@example.com>"
	m := envMsg(&imapclient.FetchMessageBuffer{Envelope: &imap.Envelope{To: []imap.Address{{Name: "Jörg", Mailbox: "person", Host: "example.com"}}}})
	if len(m.ToAddrs) != 1 || m.ToAddrs[0] != want {
		t.Fatalf("IMAP recipient display: %v", m.ToAddrs)
	}
	p, err := parseMsg([]byte("To: =?UTF-8?Q?J=C3=B6rg?= <person@example.com>\r\nCc: copy@example.com\r\n\r\nhello"))
	if err != nil || len(p.ToAddrs) != 1 || p.ToAddrs[0] != want || len(p.CcAddrs) != 1 || p.CcAddrs[0] != "copy@example.com" {
		t.Fatalf("MIME recipient display: %+v %v", p, err)
	}
}
