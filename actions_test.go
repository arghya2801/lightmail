package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// mailbox sets up the fake server with m0..m2 in INBOX, a synced cache, and an
// App wired to it. Roles are set by hand (see fakeIMAP).
func mailbox(t *testing.T) (*fakeIMAP, *Store, *App) {
	f := newFakeIMAP(t)
	for i := 0; i < 3; i++ {
		f.add("INBOX", i)
	}
	s := testStore(t)
	a := &App{store: s, dir: t.TempDir(), dial: func() (*imapclient.Client, error) { return f.dial(), nil }}
	a.cfg.Labels = []string{"INBOX", "News", "Trash"}
	s.setFolders([]Folder{{Name: "INBOX", Role: "inbox"}, {Name: "News"}, {Name: "Fin"},
		{Name: "All Mail", Role: "all"}, {Name: "Trash", Role: "trash"}})
	ss := &session{cl: f.dial()}
	for _, l := range a.cfg.Labels {
		if err := s.syncLabel(ss, l, func(int) {}); err != nil {
			t.Fatal(err)
		}
	}
	return f, s, a
}

func (f *fakeIMAP) size(box string) uint32 {
	d, err := f.cl.Select(box, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		f.t.Fatal(err)
	}
	return d.NumMessages
}

func (f *fakeIMAP) sizes(boxes ...string) string {
	out := ""
	for _, b := range boxes {
		out += fmt.Sprintf("%s=%d ", b, f.size(b))
	}
	return out
}

func listed(s *Store, label string) (n int) {
	l, _ := s.List(Query{Label: label})
	return len(l)
}

func TestArchiveAndUndo(t *testing.T) {
	f, s, a := mailbox(t)
	id := idOf(t, s, "m0@x")
	moved, err := a.Archive([]int64{id}, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("INBOX", "All Mail"); got != "INBOX=2 All Mail=1 " {
		t.Errorf("server after archive: %s", got)
	}
	if listed(s, "INBOX") != 2 || listed(s, "") != 2 {
		t.Errorf("archived message still listed: inbox %d, all %d", listed(s, "INBOX"), listed(s, ""))
	}
	if len(moved) != 1 || moved[0].UID == 0 {
		t.Fatalf("moved %+v", moved)
	}
	if err := a.Undo(moved); err != nil {
		t.Fatal(err)
	}
	// Undo copies back out of All Mail rather than moving: in Gmail a move
	// out of All Mail deletes the message.
	if got := f.sizes("INBOX", "All Mail"); got != "INBOX=3 All Mail=1 " {
		t.Errorf("server after undo: %s", got)
	}
	if listed(s, "INBOX") != 3 {
		t.Errorf("undo not reflected locally: inbox %d", listed(s, "INBOX"))
	}
	if err := s.syncLabel(&session{cl: f.dial()}, "INBOX", func(int) {}); err != nil { // the UID undo recorded is the real one
		t.Fatal(err)
	}
	if listed(s, "INBOX") != 3 {
		t.Errorf("reconcile disagreed with undo: inbox %d", listed(s, "INBOX"))
	}
}

func TestTrashThenDeleteForever(t *testing.T) {
	f, s, a := mailbox(t)
	id := idOf(t, s, "m1@x")
	if _, err := a.Trash([]int64{id}, "INBOX"); err != nil {
		t.Fatal(err)
	}
	if listed(s, "Trash") != 1 || listed(s, "") != 2 {
		t.Errorf("after trash: Trash lists %d, All mail %d", listed(s, "Trash"), listed(s, ""))
	}
	if err := a.DeleteForever([]int64{id}, "Trash"); err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("INBOX", "Trash"); got != "INBOX=2 Trash=0 " {
		t.Errorf("server after delete forever: %s", got)
	}
	if listed(s, "Trash") != 0 {
		t.Error("still listed in Trash")
	}
}

func TestMoveAndAddLabel(t *testing.T) {
	f, s, a := mailbox(t)
	m1, m2 := idOf(t, s, "m1@x"), idOf(t, s, "m2@x")

	moved, err := a.Move([]int64{m1, m2}, "INBOX", "News") // drag two rows onto News
	if err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("INBOX", "News"); got != "INBOX=1 News=2 " || listed(s, "News") != 2 || listed(s, "INBOX") != 1 {
		t.Errorf("after move: server %s, local News %d INBOX %d", got, listed(s, "News"), listed(s, "INBOX"))
	}
	if err := a.Undo(moved); err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("INBOX", "News"); got != "INBOX=3 News=0 " || listed(s, "INBOX") != 3 {
		t.Errorf("after undo move: server %s, local INBOX %d", got, listed(s, "INBOX"))
	}

	added, err := a.AddLabel([]int64{m1}, "News") // Ctrl+drag
	if err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("INBOX", "News"); got != "INBOX=3 News=1 " || listed(s, "News") != 1 {
		t.Errorf("after add label: server %s", got)
	}
	if labels, _ := s.labelsOf(m1); fmt.Sprint(labels) != "[INBOX News]" {
		t.Errorf("labels %v", labels)
	}
	if err := a.Undo(added); err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("INBOX", "News"); got != "INBOX=3 News=0 " || listed(s, "News") != 0 {
		t.Errorf("after undo add label: server %s", got)
	}

	if _, err := a.Move([]int64{m1}, "News", "Fin"); err == nil {
		t.Error("moved a message out of a folder it isn't in")
	}
	if _, err := a.Move([]int64{m1}, "INBOX", "Fin"); err != nil { // unsynced destination
		t.Fatal(err)
	}
	if listed(s, "") != 2 {
		t.Errorf("message moved to an unsynced folder still listed")
	}
}

func TestSetFlags(t *testing.T) {
	f, s, a := mailbox(t)
	if err := a.SetFlags([]int64{idOf(t, s, "m0@x"), idOf(t, s, "m2@x")}, `\Flagged`, true); err != nil {
		t.Fatal(err)
	}
	a.pushFlags()
	if got := fmt.Sprint(f.flags("INBOX")); got != `[[\Flagged] [] [\Flagged]]` {
		t.Errorf("server flags %s", got)
	}
}

// TestMoveOutOfAllMailCopies: filing from All Mail adds a label instead of
// moving, because in Gmail a move out of All Mail deletes the message.
func TestMoveOutOfAllMailCopies(t *testing.T) {
	f, s, a := mailbox(t)
	f.add("All Mail", 9)
	a.cfg.Labels = append(a.cfg.Labels, "All Mail")
	s.syncLabel(&session{cl: f.dial()}, "All Mail", func(int) {})
	moved, err := a.Move([]int64{idOf(t, s, "m9@x")}, "All Mail", "News")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("All Mail", "News"); got != "All Mail=1 News=1 " || moved[0].From != "" {
		t.Errorf("server %s, moved %+v", got, moved)
	}
	if listed(s, "All Mail") != 1 || listed(s, "News") != 1 {
		t.Errorf("local All Mail %d, News %d", listed(s, "All Mail"), listed(s, "News"))
	}
}

// TestWatchInbox: mail arriving in the Inbox (from another connection)
// triggers a sync over IDLE; dropIdle ends the watch.
func TestWatchInbox(t *testing.T) {
	f, _, a := mailbox(t)
	a.cfg.Password = "x"
	a.dialIdle = func(o *imapclient.Options) (*imapclient.Client, error) {
		cl, err := imapclient.DialInsecure(f.addr, o)
		if err == nil {
			err = cl.Login("me", "pw").Wait()
		}
		return cl, err
	}
	synced := make(chan struct{}, 10)
	a.inboxChanged = func() { synced <- struct{}{} }
	done := make(chan error, 1)
	go func() { done <- a.idleInbox() }()

	time.Sleep(300 * time.Millisecond) // let IDLE start
	f.add("INBOX", 7)
	select {
	case <-synced:
	case <-time.After(5 * time.Second):
		t.Fatal("new Inbox mail didn't trigger a sync")
	}
	a.dropIdle()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dropIdle didn't end the watch")
	}
}

func TestLabels(t *testing.T) {
	f, s, a := mailbox(t)
	for _, l := range []string{"Work", "Work/Acme"} {
		if err := a.CreateLabel(l); err != nil {
			t.Fatal(err)
		}
	}
	m0 := idOf(t, s, "m0@x")
	if _, err := a.AddLabel([]int64{m0}, "Work/Acme"); err != nil {
		t.Fatal(err)
	}
	if err := a.RenameLabel("Work", "Job"); err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("Job", "Job/Acme"); got != "Job=0 Job/Acme=1 " {
		t.Errorf("server after rename: %s", got)
	}
	if labels, _ := s.labelsOf(m0); fmt.Sprint(labels) != "[INBOX Job/Acme]" {
		t.Errorf("cached labels after rename: %v", labels)
	}
	if fmt.Sprint(a.cfg.Labels) != "[INBOX News Trash Job Job/Acme]" {
		t.Errorf("synced folders after rename: %v", a.cfg.Labels)
	}
	if err := s.syncLabel(&session{cl: f.dial()}, "Job/Acme", func(int) {}); err != nil || listed(s, "Job/Acme") != 1 {
		t.Errorf("sync after rename: %v, %d listed", err, listed(s, "Job/Acme"))
	}

	if err := a.DeleteLabel("Job/Acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cl.Select("Job/Acme", nil).Wait(); err == nil {
		t.Error("label still on server")
	}
	if labels, _ := s.labelsOf(m0); fmt.Sprint(labels) != "[INBOX]" {
		t.Errorf("cached labels after delete: %v", labels)
	}
	for _, bad := range []string{"INBOX", "Trash", ""} {
		if err := a.DeleteLabel(bad); err == nil {
			t.Errorf("deleted system folder %q", bad)
		}
	}
}

// TestThreads: replies join their parent's conversation whichever arrives
// first, the grouped list shows one row per conversation, and actions widen
// to the conversation's messages in the folder.
func TestThreads(t *testing.T) {
	s := testStore(t)
	s.setFolders([]Folder{{Name: "INBOX", Role: "inbox"}, {Name: "Sent", Role: "sent"}, {Name: "Trash", Role: "trash"}})
	tx, _ := s.db.Begin()
	for i, h := range []struct {
		mid, parent, label string
		date               int64
	}{
		{"c@x", "b@x", "INBOX", 3}, // the reply to b arrives before b
		{"a@x", "", "INBOX", 1},
		{"b@x", "a@x", "Sent", 2},
		{"d@x", "", "INBOX", 4},
		{"e@x", "a@x", "Trash", 5},
	} {
		if err := insertHeader(tx, &Msg{MessageID: h.mid, InReplyTo: h.parent, Subject: h.mid, Date: h.date, Read: h.mid != "a@x"}, h.label, uint32(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
	a, c, d := idOf(t, s, "a@x"), idOf(t, s, "c@x"), idOf(t, s, "d@x")
	if n := count(s, `SELECT count(DISTINCT thread) FROM messages`); n != 2 {
		t.Fatalf("%d threads, want 2", n)
	}
	rows, err := s.List(Query{Label: "INBOX", Threads: true})
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, m := range rows {
		got += fmt.Sprintf("%s count=%d read=%v; ", m.Subject, m.Count, m.Read)
	}
	// a, b, c outside Trash; the newest Inbox message (d, then c) stands for each.
	if want := "d@x count=1 read=true; c@x count=3 read=false; "; got != want {
		t.Errorf("grouped Inbox:\n%s\nwant\n%s", got, want)
	}
	if l, _ := s.List(Query{Label: "INBOX"}); len(l) != 3 {
		t.Errorf("ungrouped Inbox has %d rows, want 3", len(l))
	}
	th, _ := s.Thread(c)
	if len(th) != 3 || th[0].ID != a || th[2].ID != c {
		t.Errorf("thread of c: %+v", th)
	}
	if ids, _ := s.ThreadMembers([]int64{c}, "INBOX"); fmt.Sprint(ids) != fmt.Sprint([]int64{c, a}) {
		t.Errorf("Inbox members of c's thread: %v", ids)
	}
	if ids, _ := s.ThreadMembers([]int64{d}, ""); fmt.Sprint(ids) != fmt.Sprint([]int64{d}) {
		t.Errorf("members of d's thread: %v", ids)
	}
	b := idOf(t, s, "b@x") // opened from the conversation, but in Sent
	if ids, _ := s.ThreadMembers([]int64{b}, "INBOX"); fmt.Sprint(ids) != fmt.Sprint([]int64{c, a}) {
		t.Errorf("Inbox members of b's thread: %v", ids)
	}
}

// TestSnooze: a snoozed message leaves the Inbox, stays recorded until its
// time, then comes back; Undo's Unsnooze forgets it.
func TestSnooze(t *testing.T) {
	f, s, a := mailbox(t)
	m0, m1 := idOf(t, s, "m0@x"), idOf(t, s, "m1@x")
	if _, err := a.Snooze([]int64{m0}, time.Now().Unix()-1); err == nil {
		t.Error("snoozed into the past")
	}
	until := time.Now().Add(time.Hour).Unix()
	if _, err := a.Snooze([]int64{m0, m1}, until); err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("INBOX", "All Mail"); got != "INBOX=1 All Mail=2 " {
		t.Errorf("server after snooze: %s", got)
	}
	if c, _ := loadConfig(a.dir, false); c.Snoozed["m0@x"] != until {
		t.Errorf("snooze not saved: %v", c.Snoozed)
	}
	a.Unsnooze([]int64{m1})
	if err := a.wake(until - 1); err != nil || f.size("INBOX") != 1 {
		t.Fatalf("woke early: %v, %s", err, f.sizes("INBOX"))
	}
	if err := a.wake(until); err != nil {
		t.Fatal(err)
	}
	if got := f.sizes("INBOX", "All Mail"); got != "INBOX=2 All Mail=2 " {
		t.Errorf("server after wake: %s", got)
	}
	if len(a.config().Snoozed) != 0 {
		t.Errorf("snoozes left: %v", a.config().Snoozed)
	}
}
