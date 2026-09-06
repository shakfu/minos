package timeline

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// open is Open with the arguments the server passes, which no test varies.
func open(t *testing.T, path string) (*Timeline, error) {
	t.Helper()
	return Open(path, 200, time.Minute)
}

// raw is a connection that does not go through Open, for arranging a database
// the way another writer would have left it.
func raw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("cannot open %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	var version int
	if err := raw(t, path).QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("cannot read the version: %v", err)
	}
	return version
}

func TestNewDatabaseIsStampedAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.db")

	store, err := open(t, path)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	store.Close()

	if got := userVersion(t, path); got != SchemaVersion {
		t.Fatalf("user_version is %d, want %d", got, SchemaVersion)
	}
	store, err = open(t, path)
	if err != nil {
		t.Fatalf("cannot reopen: %v", err)
	}
	store.Close()
}

func TestDatabaseWithoutTheMarkerIsRefused(t *testing.T) {
	// The case the marker exists for: a file from before it was written, whose
	// tables this server would otherwise read straight through.
	path := filepath.Join(t.TempDir(), "timeline.db")
	if _, err := raw(t, path).Exec("CREATE TABLE rooms (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("cannot arrange the database: %v", err)
	}

	if _, err := open(t, path); err == nil {
		t.Fatal("an unmarked database was accepted")
	}
	if got := userVersion(t, path); got != 0 {
		t.Fatalf("a refused database was stamped %d", got)
	}
}

func TestOlderDatabaseIsUpgradedInPlace(t *testing.T) {
	// The point of the version: an upgrade, not a refusal, where one exists.
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := open(t, path)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	group, err := store.CreateGroup("Ops", "", []string{"alice"})
	if err != nil {
		t.Fatalf("cannot arrange a group: %v", err)
	}
	store.Close()

	// Wind it back to version 1, tables and all.
	for _, statement := range []string{
		"DROP TABLE IF EXISTS channel_audience", "PRAGMA user_version = 1",
	} {
		if _, err := raw(t, path).Exec(statement); err != nil {
			t.Fatalf("cannot arrange the database: %v", err)
		}
	}

	store, err = open(t, path)
	if err != nil {
		t.Fatalf("cannot upgrade: %v", err)
	}
	defer store.Close()

	if got := userVersion(t, path); got != SchemaVersion {
		t.Fatalf("user_version is %d, want %d", got, SchemaVersion)
	}
	kept, err := store.Group(group.ID)
	if err != nil || kept == nil || len(kept.Members) != 1 {
		t.Fatalf("the upgrade lost the group: %v, %v", kept, err)
	}
	if rule, err := store.AudienceRule("anything"); err != nil || len(rule) != 0 {
		t.Fatalf("channel_audience was not restored: %v, %v", rule, err)
	}
}

func TestVersionWithNoUpgradePathIsRefused(t *testing.T) {
	// A step nobody wrote must not be applied by stamping over it.
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := open(t, path)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	store.Close()
	if _, err := raw(t, path).Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("cannot arrange the database: %v", err)
	}

	steps := migrations
	migrations = map[int][]string{}
	defer func() { migrations = steps }()

	if _, err := open(t, path); err == nil {
		t.Fatal("a version with no upgrade path was accepted")
	}
	if got := userVersion(t, path); got != 1 {
		t.Fatalf("a refused database was stamped %d", got)
	}
}

func TestDatabaseFromALaterSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := open(t, path)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	store.Close()
	if _, err := raw(t, path).Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion+1)); err != nil {
		t.Fatalf("cannot arrange the database: %v", err)
	}

	if _, err := open(t, path); err == nil {
		t.Fatal("a later schema was accepted")
	}
}

func TestADatabaseWrittenByTheOtherServerIsAccepted(t *testing.T) {
	// The Python server creates presence and occupants for its own use. They are
	// not part of the shared schema, and their absence here is not a difference
	// the version marker is claiming anything about.
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := open(t, path)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	store.Close()
	if _, err := raw(t, path).Exec(
		"CREATE TABLE presence (id TEXT PRIMARY KEY, username TEXT NOT NULL, worker TEXT NOT NULL)",
	); err != nil {
		t.Fatalf("cannot arrange the database: %v", err)
	}

	store, err = open(t, path)
	if err != nil {
		t.Fatalf("cannot open a database the other server wrote: %v", err)
	}
	store.Close()
}
