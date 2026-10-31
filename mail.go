package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	netmail "net/mail"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var clientOpts = &imapclient.Options{WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}}

func dial(c Config, opts *imapclient.Options) (*imapclient.Client, error) {
	if c.User == "" || c.Password == "" {
		return nil, errors.New("add your Gmail address and app password in Settings")
	}
	cl, err := imapclient.DialTLS(c.Host, opts)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", c.Host, err)
	}
	// Google shows app passwords with spaces; IMAP wants them without.
	if err := cl.Login(c.User, strings.ReplaceAll(c.Password, " ", "")).Wait(); err != nil {
		cl.Close()
		return nil, fmt.Errorf("login failed (check the app password): %w", err)
	}
	return cl, nil
}

// session is an IMAP connection that remembers which folder is selected.
type session struct {
	cl  *imapclient.Client
	sel string
}

func (s *session) open(box string) error {
	if s.sel == box {
		return nil
	}
	s.sel = ""
	if _, err := s.cl.Select(box, nil).Wait(); err != nil {
		return fmt.Errorf("open folder %q: %w", box, err)
	}
	s.sel = box
	return nil
}

var roles = map[imap.MailboxAttr]string{
	imap.MailboxAttrAll: "all", imap.MailboxAttrArchive: "archive", imap.MailboxAttrDrafts: "drafts",
	imap.MailboxAttrFlagged: "flagged", imap.MailboxAttrJunk: "junk", imap.MailboxAttrSent: "sent",
	imap.MailboxAttrTrash: "trash", imap.MailboxAttrImportant: "important",
}

// listFolders returns every selectable folder with its special-use role, which
// is how the app finds Trash or All Mail without hardcoding "[Gmail]/...".
func listFolders(cl *imapclient.Client) ([]Folder, error) {
	var opts *imap.ListOptions
	if caps := cl.Caps(); caps.Has(imap.CapListExtended) && caps.Has(imap.CapSpecialUse) {
		opts = &imap.ListOptions{ReturnSpecialUse: true}
	}
	list, err := cl.List("", "*", opts).Collect()
	if err != nil {
		return nil, err
	}
	out := []Folder{}
	for _, l := range list {
		if !slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) && !slices.Contains(l.Attrs, imap.MailboxAttrNonExistent) {
			out = append(out, folderOf(l))
		}
	}
	slices.SortFunc(out, func(a, b Folder) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func folderOf(l *imap.ListData) Folder {
	f := Folder{Name: l.Mailbox}
	if strings.EqualFold(l.Mailbox, "INBOX") {
		f.Role = "inbox"
	}
	for _, a := range l.Attrs {
		if r, ok := roles[a]; ok {
			f.Role = r
		}
	}
	return f
}

// defaultSynced says whether a folder is ticked before the user picks: All Mail
// and Important only repeat messages that live in other folders.
func defaultSynced(f Folder) bool { return f.Role != "all" && f.Role != "important" }

var batch = 500 // var so tests can shrink it

// syncLabel brings one folder up to date: headers of new messages, then
// reconcile. Bodies are not fetched here. Commits every `batch` messages so
// memory stays flat and an interrupted sync resumes.
func (s *Store) syncLabel(ss *session, label string, progress func(int)) error {
	ss.sel = ""
	sel, err := ss.cl.Select(label, nil).Wait()
	if err != nil {
		return fmt.Errorf("open folder %q: %w", label, err)
	}
	ss.sel = label
	validity, last := s.labelState(label)
	if validity != sel.UIDValidity { // UIDs were renumbered: start the folder over
		if _, err := s.db.Exec(`DELETE FROM message_labels WHERE label=?`, label); err != nil {
			return err
		}
		last = 0
	}
	type hdr struct {
		m   *Msg
		uid uint32
	}
	var got []hdr
	// save writes the headers received so far in one short transaction, so the
	// cache is never locked while waiting on the network.
	save := func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, h := range got {
			if err := insertHeader(tx, h.m, label, h.uid); err != nil {
				return err
			}
		}
		if err := setLabelState(tx, label, sel.UIDValidity, last); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		got = got[:0]
		return nil
	}
	if sel.NumMessages > 0 && (sel.UIDNext == 0 || uint32(sel.UIDNext) > last+1) {
		cmd := ss.cl.Fetch(imap.UIDSet{{Start: imap.UID(last + 1), Stop: 0}}, &imap.FetchOptions{
			UID: true, Flags: true, Envelope: true, RFC822Size: true, InternalDate: true,
			BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		})
		defer cmd.Close()
		n := 0
		for fm := cmd.Next(); fm != nil; fm = cmd.Next() {
			buf, err := fm.Collect()
			if err != nil {
				return err
			}
			if uint32(buf.UID) <= last || buf.Envelope == nil { // "n:*" always returns the newest message
				continue
			}
			last = uint32(buf.UID)
			got = append(got, hdr{envMsg(buf), last})
			if n++; n%batch == 0 {
				if err := save(); err != nil {
					return err
				}
				progress(n)
			}
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		progress(n)
	}
	if err := save(); err != nil {
		return err
	}
	// Older caches already know the UIDs, but not the attachment or recipient metadata.
	rows, err := s.db.Query(`SELECT l.uid FROM message_labels l JOIN messages m ON m.id=l.message_id WHERE l.label=? AND m.has_attachment=-1`, label)
	if err != nil {
		return err
	}
	var missing []imap.UID
	for rows.Next() {
		var uid imap.UID
		if err := rows.Scan(&uid); err != nil {
			rows.Close()
			return err
		}
		missing = append(missing, uid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for start := 0; start < len(missing); start += batch {
		cmd := ss.cl.Fetch(imap.UIDSetNum(missing[start:min(start+batch, len(missing))]...), &imap.FetchOptions{
			UID: true, Flags: true, Envelope: true, RFC822Size: true, InternalDate: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		})
		for fm := cmd.Next(); fm != nil; fm = cmd.Next() {
			buf, err := fm.Collect()
			if err != nil {
				cmd.Close()
				return err
			}
			if buf.Envelope != nil {
				got = append(got, hdr{envMsg(buf), uint32(buf.UID)})
			}
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		if err := save(); err != nil {
			return err
		}
	}
	return s.reconcile(ss.cl, label)
}

func envMsg(buf *imapclient.FetchMessageBuffer) *Msg {
	e := buf.Envelope
	m := &Msg{Subject: e.Subject, MessageID: e.MessageID, Size: buf.RFC822Size,
		Read: slices.Contains(buf.Flags, imap.FlagSeen), Flagged: slices.Contains(buf.Flags, imap.FlagFlagged)}
	if buf.BodyStructure != nil {
		buf.BodyStructure.Walk(func(_ []int, part imap.BodyStructure) bool {
			if p, ok := part.(*imap.BodyStructureSinglePart); ok {
				if p.ID != "" && strings.HasPrefix(p.MediaType(), "image/") {
					return true
				}
				d := p.Disposition()
				if (d != nil && strings.EqualFold(d.Value, "attachment")) || p.Filename() != "" || !strings.HasPrefix(p.MediaType(), "text/") {
					m.HasAttachment = true
				}
			}
			return true
		})
	}
	m.Date = e.Date.Unix()
	if e.Date.IsZero() {
		m.Date = buf.InternalDate.Unix()
	}
	if len(e.From) > 0 {
		m.FromName, m.FromAddr = e.From[0].Name, strings.ToLower(e.From[0].Addr())
	}
	if m.FromName == "" {
		m.FromName = m.FromAddr
	}
	var to []string
	for _, a := range append(slices.Clone(e.To), e.Cc...) {
		if a.Name != "" {
			to = append(to, a.Name)
		} else if a.Mailbox != "" {
			to = append(to, a.Addr())
		}
	}
	m.To = strings.Join(to, ", ")
	for _, a := range e.To {
		m.ToAddrs = append(m.ToAddrs, (&netmail.Address{Name: a.Name, Address: a.Addr()}).String())
	}
	for _, a := range e.Cc {
		m.CcAddrs = append(m.CcAddrs, (&netmail.Address{Name: a.Name, Address: a.Addr()}).String())
	}
	if m.MessageID == "" {
		m.MessageID = synthID(m)
	}
	return m
}

// synthID stands in for a missing Message-ID, stable across folders.
func synthID(m *Msg) string {
	sum := sha256.Sum256([]byte(m.FromAddr + m.Subject + fmt.Sprint(m.Date)))
	return "nomsgid-" + hex.EncodeToString(sum[:8])
}

// reconcile makes the folder's local rows match the server: rows whose UID
// is gone (archived, deleted, moved elsewhere) are dropped, and read/starred
// follow the server unless a local change is still waiting to be pushed.
// Three SEARCHes return bare UID lists, cheap even for 50k messages.
func (s *Store) reconcile(cl *imapclient.Client, label string) error {
	since := s.mark() // before asking, so anything done here meanwhile is newer than the answer
	search := func(c *imap.SearchCriteria) (map[uint32]bool, error) {
		d, err := cl.UIDSearch(c, nil).Wait()
		if err != nil {
			return nil, err
		}
		set := map[uint32]bool{}
		for _, u := range d.AllUIDs() {
			set[uint32(u)] = true
		}
		return set, nil
	}
	all, err := search(&imap.SearchCriteria{})
	if err != nil {
		return err
	}
	unseen, err := search(&imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}})
	if err != nil {
		return err
	}
	flagged, err := search(&imap.SearchCriteria{Flag: []imap.Flag{imap.FlagFlagged}})
	if err != nil {
		return err
	}
	return s.match(label, all, unseen, flagged, since)
}

// match applies what the server said about label's UIDs to the cache. A
// message changed here after since (moved, undone, read, starred, pushed) is
// left alone: the server answered before that change, and the next sync sees it.
func (s *Store) match(label string, all, unseen, flagged map[uint32]bool, since uint64) error {
	type row struct {
		uid                    uint32
		id                     int64
		read, flagged, pending bool
	}
	rows, err := s.db.Query(`SELECT l.uid, m.id, m.read, m.flagged, EXISTS(SELECT 1 FROM pending_flags p WHERE p.message_id=m.id)
		FROM message_labels l JOIN messages m ON m.id=l.message_id WHERE l.label=?`, label)
	if err != nil {
		return err
	}
	var local []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.uid, &r.id, &r.read, &r.flagged, &r.pending); err != nil {
			rows.Close()
			return err
		}
		local = append(local, r)
	}
	rows.Close()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Checked inside the transaction: a change touches before it writes, and
	// can't write until this commits, so none slips between check and update.
	for _, r := range local {
		if s.changedSince(r.id, since) {
			continue
		}
		switch {
		case !all[r.uid]:
			_, err = tx.Exec(`DELETE FROM message_labels WHERE label=? AND uid=?`, label, r.uid)
		case !r.pending && (r.read == unseen[r.uid] || r.flagged != flagged[r.uid]):
			_, err = tx.Exec(`UPDATE messages SET read=?, flagged=? WHERE id=?`, !unseen[r.uid], flagged[r.uid], r.id)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// fetchBodies downloads whole messages by UID in label and caches them.
func (s *Store) fetchBodies(ss *session, label string, uids []imap.UID) error {
	if len(uids) == 0 {
		return nil
	}
	if err := ss.open(label); err != nil {
		return err
	}
	sec := &imap.FetchItemBodySection{Peek: true}
	cmd := ss.cl.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}})
	defer cmd.Close()
	for fm := cmd.Next(); fm != nil; fm = cmd.Next() {
		buf, err := fm.Collect()
		if err != nil {
			return err
		}
		if raw := buf.FindBodySection(sec); raw != nil {
			if _, err := s.setBody(label, uint32(buf.UID), raw); err != nil {
				return err
			}
		}
	}
	return cmd.Close()
}

// prefetch is how many of the newest Inbox messages get their bodies
// downloaded after a sync, so the common case opens without a round trip.
const prefetch, prefetchMax = 200, 2 << 20

func (s *Store) missingBodies(label string) ([]imap.UID, error) {
	rows, err := s.db.Query(`SELECT l.uid FROM message_labels l JOIN messages m ON m.id=l.message_id
		WHERE l.label=? AND m.size<? AND NOT EXISTS(SELECT 1 FROM bodies b WHERE b.id=m.id)
		ORDER BY m.date DESC LIMIT ?`, label, prefetchMax, prefetch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []imap.UID
	for rows.Next() {
		var u uint32
		rows.Scan(&u)
		out = append(out, imap.UID(u))
	}
	return out, rows.Err()
}

// pushFlags sends queued read/starred changes to the server. Gmail keeps flags
// per message, not per label, so storing them in any one folder is enough.
// Rows stay queued on error, so the next call retries them.
func (s *Store) pushFlags(ss *session) error {
	type item struct {
		id          int64
		flag, label string
		value       bool
		uid         uint32
	}
	rows, err := s.db.Query(`SELECT p.message_id, p.flag, p.value, l.label, l.uid FROM pending_flags p
		JOIN message_labels l ON l.message_id=p.message_id
		WHERE l.label=(SELECT min(label) FROM message_labels WHERE message_id=p.message_id) ORDER BY l.label`)
	if err != nil {
		return err
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.flag, &it.value, &it.label, &it.uid); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	for _, it := range items {
		if err := ss.open(it.label); err != nil {
			return err
		}
		op := imap.StoreFlagsDel
		if it.value {
			op = imap.StoreFlagsAdd
		}
		store := &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.Flag(it.flag)}}
		if err := ss.cl.Store(imap.UIDSetNum(imap.UID(it.uid)), store, nil).Close(); err != nil {
			return err
		}
		s.touch(it.id) // the server changed after any reconcile already asking it
		// Only if unchanged: a toggle made during the push stays queued.
		if _, err := s.db.Exec(`DELETE FROM pending_flags WHERE message_id=? AND flag=? AND value=?`, it.id, it.flag, it.value); err != nil {
			return err
		}
	}
	return nil
}

type Attachment struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int    `json:"size"`
	Open bool   `json:"open"` // safe to hand to its default app; see canOpen
	data []byte
}

// parseMsg reads a raw message: headers, the HTML to show (plain text wrapped
// in <pre> if there is no HTML part, cid: images inlined as data URIs), its
// visible text for search, and its attachments, in a stable order.
func parseMsg(raw []byte) (*Msg, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if mr == nil {
		return nil, err
	}
	h := mr.Header
	m := &Msg{}
	m.Subject, _ = h.Subject()
	if d, err := h.Date(); err == nil {
		m.Date = d.Unix()
	}
	if from, err := h.AddressList("From"); err == nil && len(from) > 0 {
		m.FromName, m.FromAddr = from[0].Name, strings.ToLower(from[0].Address)
	}
	if m.FromName == "" {
		m.FromName = m.FromAddr
	}
	m.MessageID, _ = h.MessageID()
	for _, field := range []struct {
		name      string
		addresses *[]string
	}{{"To", &m.ToAddrs}, {"Cc", &m.CcAddrs}} {
		if addresses, err := h.AddressList(field.name); err == nil {
			for _, a := range addresses {
				*field.addresses = append(*field.addresses, a.String())
			}
		}
	}
	if m.MessageID == "" {
		m.MessageID = synthID(m)
	}

	var htmlBody, plain string
	cids := map[string]string{} // Content-ID -> data: URI
	for {
		p, err := mr.NextPart()
		if err == io.EOF || (err != nil && p == nil) {
			break
		}
		b, _ := io.ReadAll(p.Body)
		var ct, name string
		var ph interface{ Get(string) string }
		switch h := p.Header.(type) {
		case *mail.AttachmentHeader:
			ct, _, _ = h.ContentType()
			name, _ = h.Filename()
			ph = h
		case *mail.InlineHeader:
			var params map[string]string
			ct, params, _ = h.ContentType()
			name = params["name"]
			ph = h
			if ct == "text/html" && htmlBody == "" {
				htmlBody = string(b)
				continue
			}
			if ct == "text/plain" && plain == "" {
				plain = string(b)
				continue
			}
			if strings.HasPrefix(ct, "text/") && name == "" { // a second body part, e.g. a footer
				continue
			}
		default:
			continue
		}
		if id := strings.Trim(ph.Get("Content-Id"), "<> "); id != "" && strings.HasPrefix(ct, "image/") {
			cids[id] = "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(b)
			continue
		}
		if name == "" {
			name = "attachment"
		}
		m.Attachments = append(m.Attachments, Attachment{Name: name, Type: ct, Size: len(b), Open: canOpen(name), data: b})
	}
	if htmlBody == "" {
		htmlBody = `<pre style="white-space:pre-wrap;font:inherit">` + html.EscapeString(plain) + `</pre>`
	}
	for id, uri := range cids {
		htmlBody = strings.ReplaceAll(htmlBody, "cid:"+id, uri)
	}
	m.HTML = htmlBody
	m.Text = htmlText(htmlBody)
	return m, nil
}

// htmlText extracts visible text, used for search.
func htmlText(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(b.String()), " ")
		case html.StartTagToken:
			if n, _ := z.TagName(); string(n) == "style" || string(n) == "script" || string(n) == "title" {
				skip++
			}
		case html.EndTagToken:
			if n, _ := z.TagName(); string(n) == "style" || string(n) == "script" || string(n) == "title" {
				skip--
			}
		case html.TextToken:
			if skip <= 0 {
				b.Write(z.Text())
				b.WriteByte(' ')
			}
		}
	}
}

const csp = `default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; font-src data:`

// rewrite makes newsletter HTML safe to show: tracking pixels dropped, every
// remote image routed through imgSrc, refresh/redirect meta removed, CSP added.
// Scripts are blocked by the iframe sandbox, not here.
func rewrite(src string, imgSrc func(u string) string, images bool) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return src
	}
	var head *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.ElementNode {
				switch c.DataAtom {
				case atom.Head:
					head = c
				case atom.Script, atom.Iframe, atom.Object, atom.Embed, atom.Meta:
					if c.DataAtom != atom.Meta || attr(c, "http-equiv") != "" {
						n.RemoveChild(c)
						c = next
						continue
					}
				case atom.Img:
					if isPixel(c) {
						n.RemoveChild(c)
						c = next
						continue
					}
					setAttr(c, "srcset", "")
					if u := attr(c, "src"); strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
						setAttr(c, "src", imgSrc(u))
					} else if !strings.HasPrefix(u, "data:image/") {
						setAttr(c, "src", "")
					}
				}
				walk(c)
			}
			c = next
		}
	}
	walk(doc)
	if head != nil {
		policy := csp
		if !images {
			policy = strings.Replace(policy, "img-src 'self' data:", "img-src data:", 1)
		}
		meta := &html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta, Attr: []html.Attribute{
			{Key: "http-equiv", Val: "Content-Security-Policy"}, {Key: "content", Val: policy}}}
		head.InsertBefore(meta, head.FirstChild)
	}
	var b strings.Builder
	html.Render(&b, doc)
	return b.String()
}

func isPixel(n *html.Node) bool {
	w, h := attr(n, "width"), attr(n, "height")
	tiny := func(v string) bool { v = strings.TrimSuffix(v, "px"); return v == "0" || v == "1" }
	st := strings.ReplaceAll(strings.ToLower(attr(n, "style")), " ", "")
	return (tiny(w) && tiny(h)) || (tiny(w) && h == "") || (tiny(h) && w == "") ||
		strings.Contains(st, "display:none") || strings.Contains(st, "width:1px;height:1px") || strings.Contains(st, "width:0")
}

func attr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, k) {
			return a.Val
		}
	}
	return ""
}

// setAttr sets (or with "" removes) attribute k.
func setAttr(n *html.Node, k, v string) {
	for i, a := range n.Attr {
		if strings.EqualFold(a.Key, k) {
			if v == "" {
				n.Attr = slices.Delete(n.Attr, i, i+1)
			} else {
				n.Attr[i].Val = v
			}
			return
		}
	}
	if v != "" {
		n.Attr = append(n.Attr, html.Attribute{Key: k, Val: v})
	}
}

// Bypass browser responses cached before image validation was added.
func proxied(u string) string { return "/img?u=" + url.QueryEscape(u) + "&v=2" }

var httpc = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
	// Direct connections let Control validate the actual destination, including redirects.
	DialContext:         (&net.Dialer{Timeout: 10 * time.Second, Control: imageAddress}).DialContext,
	TLSHandshakeTimeout: 10 * time.Second,
}}

func imageAddress(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("240.0.0.0/4").Contains(ip) {
		return errors.New("remote images cannot access local or private network addresses")
	}
	return nil
}

func imageType(body []byte) (string, error) {
	ct := http.DetectContentType(body)
	if !strings.HasPrefix(ct, "image/") {
		return "", errors.New("remote response is not an image")
	}
	return ct, nil
}

// imageTTL is how long a cached image survives without being shown.
const imageTTL = 30 * 24 * time.Hour

// fetchImage returns an image from the disk cache, downloading it once. A hit
// renews the file's time, which is what sweepImages goes by.
func fetchImage(dir, u string) (body []byte, ctype string, err error) {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, "", errors.New("bad image url")
	}
	sum := sha256.Sum256([]byte(u))
	path := filepath.Join(dir, "img", hex.EncodeToString(sum[:]))
	if b, err := os.ReadFile(path); err == nil {
		if _, body, ok := bytes.Cut(b, []byte{'\n'}); ok {
			ctype, err := imageType(body)
			if err != nil {
				os.Remove(path)
				return nil, "", err
			}
			now := time.Now()
			os.Chtimes(path, now, now)
			return body, ctype, nil
		}
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil { // mail is full of malformed URLs
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("image %s: %s", u, resp.Status)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > 10<<20 {
		return nil, "", errors.New("image exceeds 10 MB")
	}
	ctype, err = imageType(body)
	if err != nil {
		return nil, "", err
	}
	os.WriteFile(path, append([]byte(ctype+"\n"), body...), 0o644)
	return body, ctype, nil
}

// sweepImages deletes cached images not shown for longer than ttl. One the
// WebView still holds is simply downloaded again if it's ever asked for.
func sweepImages(dir string, ttl time.Duration) {
	ents, _ := os.ReadDir(filepath.Join(dir, "img"))
	for _, e := range ents {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > ttl {
			os.Remove(filepath.Join(dir, "img", e.Name()))
		}
	}
}
