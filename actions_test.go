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
