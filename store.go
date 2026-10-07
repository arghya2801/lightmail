package main

import (
	"bytes"
	"compress/flate"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Config struct {
	Host        string   `json:"host"`
	User        string   `json:"user"`
	Password    string   `json:"password"`
	Labels      []string `json:"labels"`
	SaveDir     string   `json:"saveDir"`
	SyncMinutes int      `json:"syncMinutes"`
	Density     string   `json:"density"`    // "comfortable" or "compact"
	Scale       int      `json:"scale"`      // interface size in percent
	ReaderZoom  int      `json:"readerZoom"` // newsletter body zoom in percent
}

// dataDir picks where settings and the cache live: $LIGHTMAIL_DATA if set; a "data"
// folder beside the exe when a file named "portable" sits next to it; else %APPDATA%.
func dataDir() (dir string, portable bool) {
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "portable")); err == nil {
			dir, portable = filepath.Join(filepath.Dir(exe), "data"), true
		}
	}
	if !portable {
		d, err := os.UserConfigDir()
		if err != nil {
			d = "."
		}
		dir = filepath.Join(d, "lightmail")
	}
	if env := os.Getenv("LIGHTMAIL_DATA"); env != "" { // separate profile, e.g. for testing
		dir = env
	}
	os.MkdirAll(filepath.Join(dir, "img"), 0o755)
	return dir, portable
}

func loadConfig(dir string, portable bool) Config {
	home, _ := os.UserHomeDir()
	save := filepath.Join(home, "Documents", "Lightmail")
	if portable {
		save = "Saved" // relative to the data folder, so it survives a new drive letter
	}
	c := Config{Host: "imap.gmail.com:993", SaveDir: save, SyncMinutes: 15, Density: "comfortable", Scale: 100, ReaderZoom: 100}
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

func saveConfig(dir string, c Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
}

// messages holds only small metadata so sorting, counting and filtering scan
// a few hundred bytes per row; bodies are compressed and read one at a time.
// fts is contentless: it indexes the text without storing a second copy.
const slimMessages = `(
  id INTEGER PRIMARY KEY, message_id TEXT UNIQUE NOT NULL,
  from_name TEXT NOT NULL, from_addr TEXT NOT NULL, subject TEXT NOT NULL, date INTEGER NOT NULL,
  words INTEGER NOT NULL, read INTEGER NOT NULL DEFAULT 0, hidden INTEGER NOT NULL DEFAULT 0)`

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS messages` + slimMessages + `;
CREATE INDEX IF NOT EXISTS messages_date ON messages(date);
CREATE TABLE IF NOT EXISTS bodies(id INTEGER PRIMARY KEY REFERENCES messages(id), html BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS message_labels(
  message_id INTEGER NOT NULL REFERENCES messages(id), label TEXT NOT NULL, PRIMARY KEY(message_id, label));
CREATE TABLE IF NOT EXISTS label_state(label TEXT PRIMARY KEY, uidvalidity INTEGER NOT NULL, last_uid INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS highlights(
  id INTEGER PRIMARY KEY, message_id INTEGER NOT NULL REFERENCES messages(id),
  text TEXT NOT NULL, prefix TEXT NOT NULL, suffix TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL);
CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(subject, from_name, text, content='');
CREATE TABLE IF NOT EXISTS pending_seen(message_id INTEGER PRIMARY KEY REFERENCES messages(id));
`

type Store struct{ db *sql.DB }

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // ponytail: single conn serialises everything; fine for one user
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if err := migrateV1(db); err != nil {
		return nil, fmt.Errorf("upgrade cache: %w", err)
	}
	return &Store{db}, nil
}

// migrateV1 upgrades the first schema (html + text inline in messages) in
// place, keeping ids so read state, labels and highlights survive.
func migrateV1(db *sql.DB) error {
	var old int
	db.QueryRow(`SELECT count(*) FROM pragma_table_info('messages') WHERE name='html'`).Scan(&old)
	if old == 0 {
		return nil
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer db.Exec(`PRAGMA foreign_keys=ON`)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE messages_new` + slimMessages,
		`INSERT INTO messages_new SELECT id,message_id,from_name,from_addr,subject,date,words,read,hidden FROM messages`,
		`DROP TABLE fts`,
		`CREATE VIRTUAL TABLE fts USING fts5(subject, from_name, text, content='')`,
		`INSERT INTO fts(rowid,subject,from_name,text) SELECT id,subject,from_name,text FROM messages`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	for last := int64(0); ; {
		type body struct {
			id   int64
			html string
		}
		var page []body
		rows, err := tx.Query(`SELECT id,html FROM messages WHERE id>? ORDER BY id LIMIT 200`, last)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b body
			rows.Scan(&b.id, &b.html)
			page = append(page, b)
		}
		rows.Close()
		if len(page) == 0 {
			break
		}
		for _, b := range page {
			if _, err := tx.Exec(`INSERT INTO bodies VALUES(?,?)`, b.id, pack(b.html)); err != nil {
				return err
			}
		}
		last = page[len(page)-1].id
	}
	for _, q := range []string{
		`DROP TABLE messages`,
		`ALTER TABLE messages_new RENAME TO messages`,
		`CREATE INDEX messages_date ON messages(date)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.Exec(`VACUUM`) // give the freed space back to the disk
	return err
}

var packers = sync.Pool{New: func() any { w, _ := flate.NewWriter(nil, flate.DefaultCompression); return w }}

func pack(s string) []byte {
	var b bytes.Buffer
	w := packers.Get().(*flate.Writer)
	w.Reset(&b)
	io.WriteString(w, s)
	w.Close()
	packers.Put(w)
	return b.Bytes()
}

func unpack(b []byte) (string, error) {
	r := flate.NewReader(bytes.NewReader(b))
	defer r.Close()
	out, err := io.ReadAll(r)
	return string(out), err
}

type Msg struct {
	ID        int64  `json:"id"`
	MessageID string `json:"-"`
	FromName  string `json:"fromName"`
	FromAddr  string `json:"fromAddr"`
	Subject   string `json:"subject"`
	Date      int64  `json:"date"`
	HTML      string `json:"html,omitempty"`
	Text      string `json:"-"`
	Words     int    `json:"words"`
	Read      bool   `json:"read"`
}

// insert adds m (deduped by Message-ID) and tags it with label.
func insertMsg(tx *sql.Tx, m *Msg, label string) error {
	res, err := tx.Exec(`INSERT INTO messages(message_id,from_name,from_addr,subject,date,words,read)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(message_id) DO NOTHING`,
		m.MessageID, m.FromName, m.FromAddr, m.Subject, m.Date, m.Words, m.Read)
	if err != nil {
		return err
	}
	var id int64
	if n, _ := res.RowsAffected(); n == 1 {
		id, _ = res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO bodies VALUES(?,?)`, id, pack(m.HTML)); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO fts(rowid,subject,from_name,text) VALUES(?,?,?,?)`, id, m.Subject, m.FromName, m.Text); err != nil {
			return err
		}
	} else if err := tx.QueryRow(`SELECT id FROM messages WHERE message_id=?`, m.MessageID).Scan(&id); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO message_labels VALUES(?,?)`, id, label)
	return err
}

func (s *Store) labelState(label string) (validity, lastUID uint32) {
	s.db.QueryRow(`SELECT uidvalidity,last_uid FROM label_state WHERE label=?`, label).Scan(&validity, &lastUID)
	return
}

func setLabelState(tx *sql.Tx, label string, validity, lastUID uint32) error {
	_, err := tx.Exec(`INSERT INTO label_state VALUES(?,?,?) ON CONFLICT(label) DO UPDATE SET uidvalidity=excluded.uidvalidity, last_uid=excluded.last_uid`, label, validity, lastUID)
	return err
}

type Query struct {
	Label  string `json:"label"`
	Sender string `json:"sender"`
	Search string `json:"search"`
	Sort   string `json:"sort"`
	Offset int    `json:"offset"`
}

var sorts = map[string]string{
	"new":    "date DESC",
	"old":    "date ASC",
	"sender": "from_name COLLATE NOCASE, date DESC",
	"unread": "read, date DESC",
	"short":  "words, date DESC",
	"long":   "words DESC, date DESC",
}

// ftsQuery turns free text into an FTS5 prefix-AND query that can't syntax-error.
func ftsQuery(s string) string {
	var parts []string
	for _, t := range strings.Fields(s) {
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " ")
}

// where builds the shared filter for List and Senders.
func where(label, sender, search string) (string, []any) {
	w, args := "m.hidden=0", []any{}
	if label != "" {
		w += " AND EXISTS(SELECT 1 FROM message_labels l WHERE l.message_id=m.id AND l.label=?)"
		args = append(args, label)
	}
	if sender != "" {
		w += " AND m.from_addr=?"
		args = append(args, sender)
	}
	if q := ftsQuery(search); q != "" {
		w += " AND m.id IN (SELECT rowid FROM fts WHERE fts MATCH ?)"
		args = append(args, q)
	}
	return w, args
}

const pageSize = 100

func (s *Store) List(q Query) ([]Msg, error) {
	order, ok := sorts[q.Sort]
	if !ok {
		order = sorts["new"]
	}
	w, args := where(q.Label, q.Sender, q.Search)
	rows, err := s.db.Query(`SELECT id,from_name,from_addr,subject,date,words,read FROM messages m WHERE `+w+
		` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, pageSize, q.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Msg{}
	for rows.Next() {
		var m Msg
		if err := rows.Scan(&m.ID, &m.FromName, &m.FromAddr, &m.Subject, &m.Date, &m.Words, &m.Read); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type Count struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Total  int    `json:"total"`
	Unread int    `json:"unread"`
}

func (s *Store) counts(sqlStr string, args ...any) ([]Count, error) {
	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Key, &c.Name, &c.Total, &c.Unread); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Senders(label string) ([]Count, error) {
	w, args := where(label, "", "")
	return s.counts(`SELECT from_addr, max(from_name), count(*), sum(read=0) FROM messages m WHERE `+w+
		` GROUP BY from_addr ORDER BY 2 COLLATE NOCASE`, args...)
}

func (s *Store) Labels() ([]Count, error) {
	return s.counts(`SELECT l.label, l.label, count(*), sum(m.read=0) FROM message_labels l
		JOIN messages m ON m.id=l.message_id WHERE m.hidden=0 GROUP BY l.label ORDER BY l.label COLLATE NOCASE`)
}

func (s *Store) Get(id int64) (Msg, error) {
	var m Msg
	var body []byte
	err := s.db.QueryRow(`SELECT m.id,from_name,from_addr,subject,date,b.html,words,read FROM messages m JOIN bodies b ON b.id=m.id WHERE m.id=?`, id).
		Scan(&m.ID, &m.FromName, &m.FromAddr, &m.Subject, &m.Date, &body, &m.Words, &m.Read)
	if err != nil {
		return m, err
	}
	m.HTML, err = unpack(body)
	return m, err
}

func (s *Store) SetRead(id int64, read bool) error {
	_, err := s.db.Exec(`UPDATE messages SET read=? WHERE id=?`, read, id)
	return err
}

// queueSeen records an issue opened in the app, to be marked read in Gmail by pushSeen.
func (s *Store) queueSeen(id int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO pending_seen VALUES(?)`, id)
	return err
}

func (s *Store) SetHidden(id int64, hidden bool) error {
	_, err := s.db.Exec(`UPDATE messages SET hidden=? WHERE id=?`, hidden, id)
	return err
}

type Highlight struct {
	ID        int64  `json:"id"`
	MessageID int64  `json:"messageId"`
	Text      string `json:"text"`
	Prefix    string `json:"prefix"`
	Suffix    string `json:"suffix"`
	Note      string `json:"note"`
	Created   int64  `json:"created"`
	// joined in on ListHighlights
	Subject  string `json:"subject,omitempty"`
	FromName string `json:"fromName,omitempty"`
	Date     int64  `json:"date,omitempty"`
}

func (s *Store) AddHighlight(h Highlight) (Highlight, error) {
	h.Created = time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO highlights(message_id,text,prefix,suffix,note,created) VALUES(?,?,?,?,?,?)`,
		h.MessageID, h.Text, h.Prefix, h.Suffix, h.Note, h.Created)
	if err != nil {
		return h, err
	}
	h.ID, err = res.LastInsertId()
	return h, err
}

func (s *Store) SetNote(id int64, note string) error {
	_, err := s.db.Exec(`UPDATE highlights SET note=? WHERE id=?`, note, id)
	return err
}

func (s *Store) DeleteHighlight(id int64) error {
	_, err := s.db.Exec(`DELETE FROM highlights WHERE id=?`, id)
	return err
}

// Highlights returns highlights for one message (messageID>0) or all, newest issue first.
func (s *Store) Highlights(messageID int64) ([]Highlight, error) {
	q := `SELECT h.id,h.message_id,h.text,h.prefix,h.suffix,h.note,h.created,m.subject,m.from_name,m.date
		FROM highlights h JOIN messages m ON m.id=h.message_id`
	var args []any
	if messageID > 0 {
		q += " WHERE h.message_id=?"
		args = append(args, messageID)
	}
	rows, err := s.db.Query(q+" ORDER BY m.date DESC, h.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Highlight{}
	for rows.Next() {
		var h Highlight
		if err := rows.Scan(&h.ID, &h.MessageID, &h.Text, &h.Prefix, &h.Suffix, &h.Note, &h.Created, &h.Subject, &h.FromName, &h.Date); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
