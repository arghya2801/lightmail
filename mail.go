package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func dial(c Config) (*imapclient.Client, error) {
	if c.User == "" || c.Password == "" {
		return nil, errors.New("add your Gmail address and app password in Settings")
	}
	cl, err := imapclient.DialTLS(c.Host, nil)
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

func serverLabels(c Config) ([]string, error) {
	cl, err := dial(c)
	if err != nil {
		return nil, err
	}
	defer cl.Logout()
	list, err := cl.List("", "*", nil).Collect()
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, l := range list {
		if !slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) {
			out = append(out, l.Mailbox)
		}
	}
	slices.Sort(out)
	return out, nil
}

const batch = 50

// syncLabel pulls every message newer than the stored last UID. Read-only:
// EXAMINE + BODY.PEEK, so nothing on the server changes. Commits every
// `batch` messages so memory stays flat and an interrupted sync resumes.
func (s *Store) syncLabel(cl *imapclient.Client, label string, progress func(int)) error {
	sel, err := cl.Select(label, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return fmt.Errorf("open label %q: %w", label, err)
	}
	validity, last := s.labelState(label)
	if validity != sel.UIDValidity {
		last = 0
	}
	if sel.NumMessages == 0 || (sel.UIDNext != 0 && uint32(sel.UIDNext) <= last+1) {
		return nil
	}
	sec := &imap.FetchItemBodySection{Peek: true}
	cmd := cl.Fetch(imap.UIDSet{{Start: imap.UID(last + 1), Stop: 0}}, &imap.FetchOptions{
		UID: true, Flags: true, BodySection: []*imap.FetchItemBodySection{sec},
	})
	defer cmd.Close()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { tx.Rollback() }()
	n := 0
	for fm := cmd.Next(); fm != nil; fm = cmd.Next() {
		buf, err := fm.Collect()
		if err != nil {
			return err
		}
		if uint32(buf.UID) <= last { // "n:*" always returns the newest message
			continue
		}
		last = uint32(buf.UID)
		m, err := parseMsg(buf.FindBodySection(sec))
		if err != nil {
			log.Printf("skip %s uid %d: %v", label, buf.UID, err)
			continue
		}
		m.Read = slices.Contains(buf.Flags, imap.FlagSeen)
		if err := insertMsg(tx, m, label); err != nil {
			return err
		}
		if n++; n%batch == 0 {
			if err := setLabelState(tx, label, sel.UIDValidity, last); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			progress(n)
			if tx, err = s.db.Begin(); err != nil {
				return err
			}
		}
	}
	if err := setLabelState(tx, label, sel.UIDValidity, last); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	progress(n)
	return cmd.Close()
}

// pushSeen marks queued issues read in Gmail. Gmail keeps flags per message,
// not per label, so finding it by Message-ID in any one of its labels is enough.
// Rows stay queued on error, so the next call retries them.
// ponytail: a label deleted in Gmail keeps its rows queued and retried each sync.
func (s *Store) pushSeen(cl *imapclient.Client) error {
	type item struct {
		id           int64
		msgID, label string
	}
	rows, err := s.db.Query(`SELECT p.message_id, m.message_id, min(l.label) FROM pending_seen p
		JOIN messages m ON m.id=p.message_id JOIN message_labels l ON l.message_id=m.id
		GROUP BY p.message_id ORDER BY 3`)
	if err != nil {
		return err
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.msgID, &it.label); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	selected := ""
	for _, it := range items {
		if !strings.HasPrefix(it.msgID, "nomsgid-") { // synthetic id: nothing to search for
			if it.label != selected {
				if _, err := cl.Select(it.label, nil).Wait(); err != nil {
					return fmt.Errorf("open label %q: %w", it.label, err)
				}
				selected = it.label
			}
			found, err := cl.UIDSearch(&imap.SearchCriteria{
				Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: it.msgID}},
			}, nil).Wait()
			if err != nil {
				return err
			}
			if uids := found.AllUIDs(); len(uids) > 0 { // none: gone from Gmail, drop it
				store := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}
				if err := cl.Store(imap.UIDSetNum(uids...), store, nil).Close(); err != nil {
					return err
				}
			}
		}
		if _, err := s.db.Exec(`DELETE FROM pending_seen WHERE message_id=?`, it.id); err != nil {
			return err
		}
	}
	return nil
}

// ponytail: cid: inline images and attachments are ignored; newsletters link remote images.
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
	if m.MessageID == "" {
		sum := sha256.Sum256([]byte(m.FromAddr + m.Subject + fmt.Sprint(m.Date)))
		m.MessageID = "nomsgid-" + hex.EncodeToString(sum[:8])
	}

	var htmlBody, plain string
	for {
		p, err := mr.NextPart()
		if err == io.EOF || (err != nil && p == nil) {
			break
		}
		ih, ok := p.Header.(*mail.InlineHeader)
		if !ok {
			continue
		}
		ct, _, _ := ih.ContentType()
		b, _ := io.ReadAll(p.Body)
		switch {
		case ct == "text/html" && htmlBody == "":
			htmlBody = string(b)
		case ct == "text/plain" && plain == "":
			plain = string(b)
		}
	}
	if htmlBody == "" {
		htmlBody = `<pre style="white-space:pre-wrap;font:inherit">` + html.EscapeString(plain) + `</pre>`
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
func rewrite(src string, imgSrc func(u string) string) string {
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
					}
				}
				walk(c)
			}
			c = next
		}
	}
	walk(doc)
	if head != nil {
		meta := &html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta, Attr: []html.Attribute{
			{Key: "http-equiv", Val: "Content-Security-Policy"}, {Key: "content", Val: csp}}}
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

func proxied(u string) string { return "/img?u=" + url.QueryEscape(u) }

var httpc = &http.Client{Timeout: 20 * time.Second}

// fetchImage returns an image from the disk cache, downloading it once.
// ponytail: cache never evicts; add an LRU sweep if img/ gets big.
func fetchImage(dir, u string) (body []byte, ctype string, err error) {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, "", errors.New("bad image url")
	}
	sum := sha256.Sum256([]byte(u))
	path := filepath.Join(dir, "img", hex.EncodeToString(sum[:]))
	if b, err := os.ReadFile(path); err == nil {
		if ct, body, ok := bytes.Cut(b, []byte{'\n'}); ok {
			return body, string(ct), nil
		}
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("image %s: %s", u, resp.Status)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, "", err
	}
	ctype = resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ctype, "image/") {
		ctype = http.DetectContentType(body)
	}
	os.WriteFile(path, append([]byte(ctype+"\n"), body...), 0o644)
	return body, ctype, nil
}
