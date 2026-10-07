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

	_ "modernc.org/sqlite"
)

type Config struct {
	Host        string   `json:"host"`
	User        string   `json:"user"`
	Password    string   `json:"password"`
	Labels      []string `json:"labels"` // folders to sync
	SaveDir     string   `json:"saveDir"`
	SyncMinutes int      `json:"syncMinutes"`
	Density     string   `json:"density"`    // "comfortable" or "compact"
	Scale       int      `json:"scale"`      // interface size in percent
	ReaderZoom  int      `json:"readerZoom"` // message body zoom in percent
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

// messages holds only header metadata, so listing a 50k-message folder scans
// a few hundred bytes per row. One row per Message-ID: Gmail shows a message
// with two labels in two folders, so message_labels has a row (with that
// folder's UID) per label. Bodies arrive on demand, compressed raw RFC 822.
// fts is contentless: it indexes the text without storing a second copy.
const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS messages(
  id INTEGER PRIMARY KEY, message_id TEXT UNIQUE NOT NULL,
  from_name TEXT NOT NULL, from_addr TEXT NOT NULL, to_addrs TEXT NOT NULL, subject TEXT NOT NULL,
  date INTEGER NOT NULL, size INTEGER NOT NULL, read INTEGER NOT NULL DEFAULT 0, flagged INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS messages_date ON messages(date);
CREATE TABLE IF NOT EXISTS bodies(id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE, raw BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS message_labels(
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE, label TEXT NOT NULL, uid INTEGER NOT NULL,
  PRIMARY KEY(message_id, label));
CREATE INDEX IF NOT EXISTS message_labels_uid ON message_labels(label, uid);
CREATE TABLE IF NOT EXISTS label_state(label TEXT PRIMARY KEY, uidvalidity INTEGER NOT NULL, last_uid INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS folders(name TEXT PRIMARY KEY, role TEXT NOT NULL);
CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(subject, from_name, text, content='', contentless_delete=1);
CREATE TABLE IF NOT EXISTS pending_flags(
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE, flag TEXT NOT NULL, value INTEGER NOT NULL,
  PRIMARY KEY(message_id, flag));
`

type Store struct{ db *sql.DB }

// schemaVersion goes up whenever schema changes shape. The cache holds
// nothing the server doesn't, so an old one is deleted and synced again.
const schemaVersion = 2

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // ponytail: single conn serialises everything; fine for one user
	var v, tables int
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&tables)
	if v != schemaVersion && tables > 0 {
		db.Close()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("replace old cache: %w", err)
			}
		}
		return openStore(path)
	}
	if _, err := db.Exec(schema + fmt.Sprintf("PRAGMA user_version=%d;", schemaVersion)); err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

var packers = sync.Pool{New: func() any { w, _ := flate.NewWriter(nil, flate.DefaultCompression); return w }}

func pack(b []byte) []byte {
	var out bytes.Buffer
	w := packers.Get().(*flate.Writer)
	w.Reset(&out)
	w.Write(b)
	w.Close()
	packers.Put(w)
	return out.Bytes()
}

func unpack(b []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(b))
	defer r.Close()
	return io.ReadAll(r)
}

type Msg struct {
	ID        int64    `json:"id"`
	MessageID string   `json:"-"`
	FromName  string   `json:"fromName"`
	FromAddr  string   `json:"fromAddr"`
	To        string   `json:"to"` // display names of To and Cc, comma-separated
	Subject   string   `json:"subject"`
	Date      int64    `json:"date"`
	Size      int64    `json:"-"`
	Read      bool     `json:"read"`
	Flagged   bool     `json:"flagged"`
	HTML      string   `json:"html,omitempty"`
	Text      string   `json:"-"`
	Labels    []string `json:"labels,omitempty"` // filled by Open
}

type Folder struct {
	Name   string `json:"name"`
	Role   string `json:"role"` // inbox, sent, drafts, trash, junk, all, archive, flagged, important or ""
	Total  int    `json:"total"`
	Unread int    `json:"unread"`
	Synced bool   `json:"synced"`
}

// insertHeader adds m (deduped by Message-ID) and records it at uid in label.
func insertHeader(tx *sql.Tx, m *Msg, label string, uid uint32) error {
	res, err := tx.Exec(`INSERT INTO messages(message_id,from_name,from_addr,to_addrs,subject,date,size,read,flagged)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(message_id) DO NOTHING`,
		m.MessageID, m.FromName, m.FromAddr, m.To, m.Subject, m.Date, m.Size, m.Read, m.Flagged)
	if err != nil {
		return err
	}
	var id int64
	if n, _ := res.RowsAffected(); n == 1 {
		id, _ = res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO fts(rowid,subject,from_name,text) VALUES(?,?,?,'')`, id, m.Subject, m.FromName); err != nil {
			return err
		}
	} else if err := tx.QueryRow(`SELECT id FROM messages WHERE message_id=?`, m.MessageID).Scan(&id); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO message_labels VALUES(?,?,?) ON CONFLICT(message_id,label) DO UPDATE SET uid=excluded.uid`, id, label, uid)
	return err
}

// setBody caches the raw message found at uid in label and indexes its text.
func (s *Store) setBody(label string, uid uint32, raw []byte) (int64, error) {
	var id int64
	var subject, from string
	err := s.db.QueryRow(`SELECT m.id,m.subject,m.from_name FROM message_labels l JOIN messages m ON m.id=l.message_id
		WHERE l.label=? AND l.uid=?`, label, uid).Scan(&id, &subject, &from)
	if err != nil {
		return 0, err
	}
	text := ""
	if m, err := parseMsg(raw); err == nil {
		text = m.Text
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT OR REPLACE INTO bodies VALUES(?,?)`, []any{id, pack(raw)}},
		{`DELETE FROM fts WHERE rowid=?`, []any{id}},
		{`INSERT INTO fts(rowid,subject,from_name,text) VALUES(?,?,?,?)`, []any{id, subject, from, text}},
	} {
		if _, err := tx.Exec(q.sql, q.args...); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// body returns the cached raw message, or nil if it hasn't been downloaded.
func (s *Store) body(id int64) ([]byte, error) {
	var b []byte
	err := s.db.QueryRow(`SELECT raw FROM bodies WHERE id=?`, id).Scan(&b)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return unpack(b)
}

// location returns one folder and UID where message id lives on the server.
func (s *Store) location(id int64) (label string, uid uint32, err error) {
	err = s.db.QueryRow(`SELECT label,uid FROM message_labels WHERE message_id=? ORDER BY label LIMIT 1`, id).Scan(&label, &uid)
	if err == sql.ErrNoRows {
		err = fmt.Errorf("message %d is no longer on the server", id)
	}
	return
}

func (s *Store) labelsOf(id int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT label FROM message_labels WHERE message_id=? ORDER BY label`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var l string
		rows.Scan(&l)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) labelState(label string) (validity, lastUID uint32) {
	s.db.QueryRow(`SELECT uidvalidity,last_uid FROM label_state WHERE label=?`, label).Scan(&validity, &lastUID)
	return
}

func setLabelState(tx *sql.Tx, label string, validity, lastUID uint32) error {
	_, err := tx.Exec(`INSERT INTO label_state VALUES(?,?,?) ON CONFLICT(label) DO UPDATE SET uidvalidity=excluded.uidvalidity, last_uid=excluded.last_uid`, label, validity, lastUID)
	return err
}

// setFolders replaces the folder list with what the server reported.
func (s *Store) setFolders(fs []Folder) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM folders`); err != nil {
		return err
	}
	for _, f := range fs {
		if _, err := tx.Exec(`INSERT INTO folders VALUES(?,?)`, f.Name, f.Role); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// roleFolder returns the server folder with a special-use role, or "".
func (s *Store) roleFolder(role string) string {
	var name string
	s.db.QueryRow(`SELECT name FROM folders WHERE role=? ORDER BY name LIMIT 1`, role).Scan(&name)
	return name
}

// prune drops everything about folders no longer synced (unticked, or gone
// from the server), then messages left in no folder at all.
func (s *Store) prune(live []string) error {
	keep, _ := json.Marshal(live)
	for _, q := range []string{
		`DELETE FROM message_labels WHERE label NOT IN (SELECT value FROM json_each(?1))`,
		`DELETE FROM label_state WHERE label NOT IN (SELECT value FROM json_each(?1))`,
		`DELETE FROM fts WHERE rowid IN (SELECT id FROM messages m WHERE NOT EXISTS(SELECT 1 FROM message_labels l WHERE l.message_id=m.id))`,
		`DELETE FROM messages WHERE NOT EXISTS(SELECT 1 FROM message_labels l WHERE l.message_id=messages.id)`,
	} {
		if _, err := s.db.Exec(q, string(keep)); err != nil {
			return err
		}
	}
	return nil
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
}

// ftsQuery turns free text into an FTS5 prefix-AND query that can't syntax-error.
func ftsQuery(s string) string {
	var parts []string
	for _, t := range strings.Fields(s) {
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " ")
}

// where builds the shared filter for List and Senders. No label means all
// mail: everything outside Trash and Spam.
func where(label, sender, search string) (string, []any) {
	w, args := "", []any{}
	if label != "" {
		w = "EXISTS(SELECT 1 FROM message_labels l WHERE l.message_id=m.id AND l.label=?)"
		args = append(args, label)
	} else {
		w = `EXISTS(SELECT 1 FROM message_labels l LEFT JOIN folders f ON f.name=l.label
			WHERE l.message_id=m.id AND coalesce(f.role,'') NOT IN ('trash','junk'))`
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

const msgCols = `m.id,m.from_name,m.from_addr,m.to_addrs,m.subject,m.date,m.read,m.flagged`

func scanMsg(row interface{ Scan(...any) error }, m *Msg) error {
	return row.Scan(&m.ID, &m.FromName, &m.FromAddr, &m.To, &m.Subject, &m.Date, &m.Read, &m.Flagged)
}

func (s *Store) List(q Query) ([]Msg, error) {
	order, ok := sorts[q.Sort]
	if !ok {
		order = sorts["new"]
	}
	w, args := where(q.Label, q.Sender, q.Search)
	rows, err := s.db.Query(`SELECT `+msgCols+` FROM messages m WHERE `+w+
		` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, pageSize, q.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Msg{}
	for rows.Next() {
		var m Msg
		if err := scanMsg(rows, &m); err != nil {
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

func (s *Store) Senders(label string) ([]Count, error) {
	w, args := where(label, "", "")
	rows, err := s.db.Query(`SELECT from_addr, max(from_name), count(*), sum(read=0) FROM messages m WHERE `+w+
		` GROUP BY from_addr ORDER BY 2 COLLATE NOCASE`, args...)
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

// Folders returns every server folder with local message counts.
func (s *Store) Folders() ([]Folder, error) {
	rows, err := s.db.Query(`SELECT f.name, f.role, count(m.id), coalesce(sum(m.read=0),0) FROM folders f
		LEFT JOIN message_labels l ON l.label=f.name LEFT JOIN messages m ON m.id=l.message_id
		GROUP BY f.name ORDER BY f.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Folder{}
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.Name, &f.Role, &f.Total, &f.Unread); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) Get(id int64) (Msg, error) {
	var m Msg
	err := scanMsg(s.db.QueryRow(`SELECT `+msgCols+` FROM messages m WHERE m.id=?`, id), &m)
	return m, err
}

var flagCols = map[string]string{`\Seen`: "read", `\Flagged`: "flagged"}

// setFlag changes read or starred here and queues the change for the server.
func (s *Store) setFlag(id int64, flag string, value bool) error {
	col, ok := flagCols[flag]
	if !ok {
		return fmt.Errorf("unknown flag %s", flag)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE messages SET `+col+`=? WHERE id=?`, value, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO pending_flags VALUES(?,?,?) ON CONFLICT DO UPDATE SET value=excluded.value`, id, flag, value); err != nil {
		return err
	}
	return tx.Commit()
}
