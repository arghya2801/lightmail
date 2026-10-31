package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// Moved records one message that changed folders, enough to undo it.
// From is "" when a label was added (a copy) rather than moved.
type Moved struct {
	ID   int64  `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	UID  uint32 `json:"uid"` // in To; 0 if the server didn't say (no UIDPLUS), which rules out undo
}

type loc struct {
	id  int64
	uid uint32
}

// locate groups ids by the folder they'll leave: from, or wherever each one
// lives when from is "" (the All mail view).
func (s *Store) locate(ids []int64, from string) (map[string][]loc, error) {
	out := map[string][]loc{}
	for _, id := range ids {
		var l loc
		label := from
		var err error
		if from == "" {
			label, l.uid, err = s.location(id)
		} else {
			err = s.db.QueryRow(`SELECT uid FROM message_labels WHERE message_id=? AND label=?`, id, from).Scan(&l.uid)
		}
		if err != nil {
			return nil, fmt.Errorf("message %d is not in %s any more; sync and try again", id, from)
		}
		l.id = id
		out[label] = append(out[label], l)
	}
	return out, nil
}

// transfer moves (or copies) uids from the folder to another and returns the
// UIDs they got there, keyed by old UID, when the server reports them.
func transfer(ss *session, from string, uids []imap.UID, to string, copy bool) (map[imap.UID]imap.UID, error) {
	if err := ss.open(from); err != nil {
		return nil, err
	}
	set := imap.UIDSetNum(uids...)
	var src, dst imap.NumSet
	if copy {
		d, err := ss.cl.Copy(set, to).Wait()
		if err != nil {
			return nil, err
		}
		src, dst = d.SourceUIDs, d.DestUIDs
	} else {
		d, err := ss.cl.Move(set, to).Wait()
		if err != nil {
			return nil, err
		}
		src, dst = d.SourceUIDs, d.DestUIDs
	}
	out := map[imap.UID]imap.UID{}
	s, _ := src.(imap.UIDSet)
	d, _ := dst.(imap.UIDSet)
	if ss, ok := s.Nums(); ok {
		if dd, ok := d.Nums(); ok && len(ss) == len(dd) {
			for i := range ss {
				out[ss[i]] = dd[i]
			}
		}
	}
	return out, nil
}

// expunge removes uids from a folder. In Gmail that drops the folder's label;
// in Trash or Spam it deletes the message for good.
func expunge(ss *session, folder string, uids []imap.UID) error {
	if err := ss.open(folder); err != nil {
		return err
	}
	set := imap.UIDSetNum(uids...)
	del := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
	if err := ss.cl.Store(set, del, nil).Close(); err != nil {
		return err
	}
	return ss.cl.UIDExpunge(set).Close()
}

// relabel applies one finished server change to the cache: id left from (if
// any) and, when to is synced and its UID known, arrived there.
func (s *Store) relabel(id int64, from, to string, uid uint32, synced bool) error {
	s.touch(id)
	if from != "" {
		if _, err := s.db.Exec(`DELETE FROM message_labels WHERE message_id=? AND label=?`, id, from); err != nil {
			return err
		}
	}
	if to != "" && synced && uid != 0 { // the row may be gone if a sync pruned it; that sync's successor refetches it
		_, err := s.db.Exec(`INSERT INTO message_labels SELECT ?1,?2,?3 WHERE EXISTS(SELECT 1 FROM messages WHERE id=?1)
			ON CONFLICT(message_id,label) DO UPDATE SET uid=excluded.uid`, id, to, uid)
		return err
	}
	return nil
}

// move is the one primitive behind archive, delete, spam, drag-and-drop and
// add-label: UID MOVE (or COPY) on the server, then the cache. A message left
// in no synced folder drops out of every view and is pruned on the next
// sync, which keeps its row around for Undo until then.
func (a *App) move(ids []int64, from, to string, copy bool) ([]Moved, error) {
	if to == "" {
		return nil, errors.New("no destination folder")
	}
	groups, err := a.store.locate(ids, from)
	if err != nil {
		return nil, err
	}
	synced := a.config().Labels
	all, trash, junk := a.store.roleFolder("all"), a.store.roleFolder("trash"), a.store.roleFolder("junk")
	var out []Moved
	err = a.imap(func(ss *session) error {
		for src, locs := range groups {
			if src == to {
				continue
			}
			// Out of All Mail, Gmail treats a move as delete. Filing elsewhere
			// means adding a label, so copy; Trash and Spam really are moves.
			cp := copy || (src != "" && src == all && to != trash && to != junk)
			uids := make([]imap.UID, len(locs))
			for i, l := range locs {
				uids[i] = imap.UID(l.uid)
			}
			dest, err := transfer(ss, src, uids, to, cp)
			if err != nil {
				return err
			}
			for _, l := range locs {
				m := Moved{ID: l.id, From: src, To: to, UID: uint32(dest[imap.UID(l.uid)])}
				left := src
				if cp {
					m.From, left = "", ""
				}
				if err := a.store.relabel(l.id, left, to, m.UID, slices.Contains(synced, to)); err != nil {
					return err
				}
				out = append(out, m)
			}
		}
		return nil
	})
	return out, err
}

// Move takes messages out of from (the folder being viewed, or "" for each
// message's own folder) and puts them in to.
func (a *App) Move(ids []int64, from, to string) ([]Moved, error) {
	return a.move(ids, from, to, false)
}

// AddLabel files messages under to as well, leaving them where they are.
func (a *App) AddLabel(ids []int64, to string) ([]Moved, error) { return a.move(ids, "", to, true) }

// Archive takes messages out of from (normally the Inbox). In Gmail that is a
// move to All Mail, which keeps the message and drops the label.
func (a *App) Archive(ids []int64, from string) ([]Moved, error) {
	if from == "" {
		from = a.store.roleFolder("inbox")
	}
	to := a.store.roleFolder("all")
	if to == "" {
		to = a.store.roleFolder("archive")
	}
	if to == "" {
		return nil, errors.New("this account has no All Mail or Archive folder")
	}
	return a.move(ids, from, to, false)
}

// Trash moves messages to Trash; Gmail empties it after 30 days.
func (a *App) Trash(ids []int64, from string) ([]Moved, error) {
	to := a.store.roleFolder("trash")
	if to == "" {
		return nil, errors.New("this account has no Trash folder")
	}
	return a.move(ids, from, to, false)
}

// Spam moves messages to Spam.
func (a *App) Spam(ids []int64, from string) ([]Moved, error) {
	to := a.store.roleFolder("junk")
	if to == "" {
		return nil, errors.New("this account has no Spam folder")
	}
	return a.move(ids, from, to, false)
}

// DeleteForever expunges messages from folder (meant for Trash and Spam). No undo.
func (a *App) DeleteForever(ids []int64, folder string) error {
	groups, err := a.store.locate(ids, folder)
	if err != nil {
		return err
	}
	return a.imap(func(ss *session) error {
		locs := groups[folder]
		uids := make([]imap.UID, len(locs))
		for i, l := range locs {
			uids[i] = imap.UID(l.uid)
		}
		if err := expunge(ss, folder, uids); err != nil {
			return err
		}
		for _, l := range locs {
			if err := a.store.relabel(l.id, folder, "", 0, false); err != nil {
				return err
			}
		}
		return nil
	})
}

// Undo reverses moves and added labels. Moving back out of All Mail would
// delete the message in Gmail, so that direction copies instead (re-adds the label).
func (a *App) Undo(moves []Moved) error {
	all := a.store.roleFolder("all")
	synced := a.config().Labels
	return a.imap(func(ss *session) error {
		for _, m := range moves {
			if m.UID == 0 {
				return errors.New("the server didn't report where the message went, so it can't be undone")
			}
			uid := imap.UID(m.UID)
			if m.From == "" { // an added label: take it off again
				if err := expunge(ss, m.To, []imap.UID{uid}); err != nil {
					return err
				}
				if err := a.store.relabel(m.ID, m.To, "", 0, false); err != nil {
					return err
				}
				continue
			}
			copy := m.To == all
			dest, err := transfer(ss, m.To, []imap.UID{uid}, m.From, copy)
			if err != nil {
				return err
			}
			left := m.To
			if copy {
				left = ""
			}
			if err := a.store.relabel(m.ID, left, m.From, uint32(dest[uid]), slices.Contains(synced, m.From)); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetFlags sets read (`\Seen`) or starred (`\Flagged`) on several messages.
func (a *App) SetFlags(ids []int64, flag string, value bool) error {
	for _, id := range ids {
		if err := a.store.setFlag(id, flag, value); err != nil {
			return err
		}
	}
	go a.pushFlags()
	return nil
}

// userLabel refuses system folders (Inbox, Trash...) for create/rename/delete.
func (a *App) userLabel(name string) error {
	if name == "" {
		return errors.New("label name is empty")
	}
	if a.store.roleFolder("inbox") == name || a.store.folderRole(name) != "" {
		return fmt.Errorf("%s is a system folder", name)
	}
	return nil
}

// CreateLabel makes a label on the server and syncs it from now on.
func (a *App) CreateLabel(name string) error {
	if err := a.userLabel(name); err != nil {
		return err
	}
	if err := a.imap(func(ss *session) error { return ss.cl.Create(name, nil).Wait() }); err != nil {
		return err
	}
	if _, err := a.store.db.Exec(`INSERT OR IGNORE INTO folders VALUES(?, '')`, name); err != nil {
		return err
	}
	return a.editLabels(func(ls []string) []string { return append(ls, name) })
}

// RenameLabel renames a label and, as Gmail does, the labels nested under it.
func (a *App) RenameLabel(old, name string) error {
	if err := a.userLabel(old); err != nil {
		return err
	}
	if err := a.userLabel(name); err != nil {
		return err
	}
	err := a.imap(func(ss *session) error {
		if ss.sel == old || strings.HasPrefix(ss.sel, old+"/") {
			ss.sel = "" // renaming the selected folder leaves the session unsure where it is
		}
		if err := ss.cl.Rename(old, name, nil).Wait(); err != nil {
			return err
		}
		// Gmail renames nested labels too; a server that doesn't leaves them behind.
		left, err := ss.cl.List("", old+"/*", nil).Collect()
		if err != nil {
			return err
		}
		for _, l := range left {
			if err := ss.cl.Rename(l.Mailbox, name+strings.TrimPrefix(l.Mailbox, old), nil).Wait(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := a.store.renameLabel(old, name); err != nil {
		return err
	}
	return a.editLabels(func(ls []string) []string {
		for i, l := range ls {
			if l == old || strings.HasPrefix(l, old+"/") {
				ls[i] = name + strings.TrimPrefix(l, old)
			}
		}
		return ls
	})
}

// DeleteLabel deletes a label. In Gmail the messages stay, in All Mail and their other labels.
func (a *App) DeleteLabel(name string) error {
	if err := a.userLabel(name); err != nil {
		return err
	}
	err := a.imap(func(ss *session) error {
		if ss.sel == name {
			if err := ss.cl.Unselect().Wait(); err != nil {
				return err
			}
			ss.sel = ""
		}
		return ss.cl.Delete(name).Wait()
	})
	if err != nil {
		return err
	}
	if _, err := a.store.db.Exec(`DELETE FROM folders WHERE name=?`, name); err != nil {
		return err
	}
	if err := a.editLabels(func(ls []string) []string { return slices.DeleteFunc(ls, func(l string) bool { return l == name }) }); err != nil {
		return err
	}
	return a.store.prune(a.config().Labels)
}

// editLabels changes the synced folder list and saves it.
func (a *App) editLabels(f func([]string) []string) error {
	a.mu.Lock()
	a.cfg.Labels = f(slices.Clone(a.cfg.Labels))
	c := a.cfg
	a.mu.Unlock()
	if a.dir == "" { // tests without a data folder
		return nil
	}
	return saveConfig(a.dir, c)
}
