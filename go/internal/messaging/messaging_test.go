package messaging

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minos/internal/config"
	"minos/internal/timeline"
)

// One room that cannot be deleted must not keep the sweep from the rest, nor
// from the archive pass after them.
func TestSweepCarriesOnPastARoomItCannotDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := timeline.Open(path, config.HistoryLimit, time.Nanosecond, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := New(store, func([]string, any) {}, func() []string { return []string{"demo"} })
	for i := range 3 {
		if _, err := m.OpenRoom("demo", nil, fmt.Sprint("room ", i), timeline.Transient); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(10 * time.Millisecond)
	expired, err := store.ExpiredTransientRooms()
	if err != nil || len(expired) != 3 {
		t.Fatalf("expired %v, %v", expired, err)
	}

	// The first the sweep will reach refuses to go.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(fmt.Sprintf(
		"CREATE TRIGGER stuck BEFORE DELETE ON rooms WHEN old.id = '%s' BEGIN SELECT RAISE(ABORT, 'stuck'); END",
		expired[0])); err != nil {
		t.Fatal(err)
	}

	gone, err := m.Sweep()
	if err == nil {
		t.Fatal("a failed delete was not reported")
	}
	slices.Sort(gone)
	want := slices.Sorted(slices.Values(expired[1:]))
	if !slices.Equal(gone, want) {
		t.Fatalf("swept %v, want %v", gone, want)
	}
}

// A permanent name is checked and then written; concurrent creates of one name
// must not both pass the check.
func TestConcurrentCreatesOfOneNameMakeOneRoom(t *testing.T) {
	store, err := timeline.Open(filepath.Join(t.TempDir(), "timeline.db"), config.HistoryLimit, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := New(store, func([]string, any) {}, func() []string { return []string{"demo"} })

	var wait sync.WaitGroup
	var created atomic.Int32
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := m.CreateRoom("demo", true, "ops", nil, "", "", ""); err == nil {
				created.Add(1)
			}
		}()
	}
	wait.Wait()
	if n := created.Load(); n != 1 {
		t.Fatalf("%d rooms called ops were created", n)
	}
}

// Entering releases the user's place in any other room, and says which, so the
// transport can forget those occupancies rather than hold them to disconnect.
func TestEnteringReportsTheOccupanciesItReleased(t *testing.T) {
	store, err := timeline.Open(filepath.Join(t.TempDir(), "timeline.db"), config.HistoryLimit, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := New(store, func([]string, any) {}, func() []string { return []string{"demo"} })
	first, err := m.CreateRoom("demo", true, "first", nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.CreateRoom("demo", true, "second", nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	there, err := m.Enter("demo", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	here, err := m.Enter("demo", second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(here.Released, []string{there.Occupancy}) {
		t.Fatalf("released %v, want [%s]", here.Released, there.Occupancy)
	}
}
