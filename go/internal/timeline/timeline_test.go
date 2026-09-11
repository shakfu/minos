package timeline

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSubjectFallsBackToTheFirstLine(t *testing.T) {
	for _, case_ := range []struct{ given, body, want string }{
		{"  Given  ", "the body", "Given"},
		{"", "\n  first  \nsecond", "first"},
		{"", strings.Repeat("é", 250), strings.Repeat("é", SubjectLimit)},
		{" ", " ", ""},
	} {
		if got := Subject(case_.given, case_.body); got != case_.want {
			t.Errorf("Subject(%q, %q) = %q, want %q", case_.given, case_.body, got, case_.want)
		}
	}
}

func TestAVersion3DatabaseGainsSubjectsAndArchival(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := open(t, path)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	channel, _ := store.CreateRoom("News", "demo", ChannelKind, Admin, Persisted, "", nil)
	room, _ := store.CreateRoom("Chat", "demo", RoomKind, User, Persisted, "", nil)
	for _, target := range []string{channel.ID, room.ID} {
		if _, err := store.Append(target, "demo", "", "  Deploy at four\nDetails", Text); err != nil {
			t.Fatalf("cannot arrange a message: %v", err)
		}
	}
	store.Close()

	// As version 3 left it, with a submission written before subjects existed.
	for _, statement := range append(slices.Clone(sinceVersion3),
		"ALTER TABLE submissions DROP COLUMN subject", "PRAGMA user_version = 3",
		"INSERT INTO submissions (id, channel_id, author, body, at, state)"+
			" VALUES ('old', '"+channel.ID+"', 'bob', 'A tip\nmore', 0, 'pending')",
	) {
		if _, err := raw(t, path).Exec(statement); err != nil {
			t.Fatalf("cannot arrange the database: %v", err)
		}
	}

	store, err = open(t, path)
	if err != nil {
		t.Fatalf("cannot upgrade a version 3 database: %v", err)
	}
	defer store.Close()

	news, _ := store.History(channel.ID, 0)
	chat, _ := store.History(room.ID, 0)
	if len(news) != 1 || news[0].Subject == nil || *news[0].Subject != "Deploy at four" {
		t.Fatalf("a channel message was not given its first line: %+v", news)
	}
	if len(chat) != 1 || chat[0].Subject != nil {
		t.Fatalf("a room message was given a subject: %+v", chat)
	}
	if old, err := store.Submission("old"); err != nil || old == nil || old.Subject != "A tip" {
		t.Fatalf("the submission was not given its first line: %+v, %v", old, err)
	}
	if kept, _ := store.Room(channel.ID); kept.Archive.Period != nil || kept.Archive.Searchable {
		t.Fatalf("an upgraded channel archives: %+v", kept.Archive)
	}
	if _, err := store.SearchArchive(channel.ID, "x", 10); err != nil {
		t.Fatalf("archived_messages was not created: %v", err)
	}
}

// A read cursor is evidence of a visit, so the upgrade counts it as one.
func TestAVersion4DatabaseGainsVisits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := open(t, path)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	read, _ := store.CreateRoom("Read", "demo", RoomKind, User, Persisted, "", nil)
	if err := store.MarkRead(read.ID, "alice", 1); err != nil {
		t.Fatalf("cannot arrange a read cursor: %v", err)
	}
	store.Close()
	for _, statement := range []string{"DROP TABLE visits", "PRAGMA user_version = 4"} {
		if _, err := raw(t, path).Exec(statement); err != nil {
			t.Fatalf("cannot arrange the database: %v", err)
		}
	}

	store, err = open(t, path)
	if err != nil {
		t.Fatalf("cannot upgrade a version 4 database: %v", err)
	}
	defer store.Close()
	if visited, err := store.Visited("alice"); err != nil || !slices.Equal(visited, []string{read.ID}) {
		t.Fatalf("after the upgrade visited is %v, %v", visited, err)
	}
	room, _ := store.CreateRoom("Chat", "demo", RoomKind, User, Persisted, "", nil)
	if _, err := store.Enter(room.ID, "alice"); err != nil {
		t.Fatalf("cannot enter: %v", err)
	}
	if visited, _ := store.Visited("alice"); !slices.Contains(visited, room.ID) || len(visited) != 2 {
		t.Fatalf("after entering visited is %v", visited)
	}
}

// A young message holds back an older one behind it, or the live tail would
// have a hole in it.
func TestArchivalTakesTheAgedRunFromTheOldestOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := open(t, path)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	defer store.Close()
	channel, _ := store.CreateRoom("News", "demo", ChannelKind, Admin, Persisted, "", nil)
	for _, body := range []string{"one", "two", "three"} {
		if _, err := store.Append(channel.ID, "demo", "", body, Text); err != nil {
			t.Fatalf("cannot arrange a message: %v", err)
		}
	}
	if _, err := raw(t, path).Exec("UPDATE messages SET at = at - 3600 WHERE seq IN (1, 3)"); err != nil {
		t.Fatalf("cannot age the messages: %v", err)
	}
	period := 60.0
	if err := store.SetArchive(channel.ID, Archive{Period: &period}); err != nil {
		t.Fatal(err)
	}

	through, err := store.ArchiveAged(channel.ID)
	if err != nil || through != 1 {
		t.Fatalf("archived through %d, %v; want 1", through, err)
	}
	live, _ := store.History(channel.ID, 0)
	archived, more, _ := store.Archived(channel.ID, 0)
	if len(live) != 2 || live[0].Seq != 2 || len(archived) != 1 || archived[0].Seq != 1 || more {
		t.Fatalf("live %+v, archived %+v (more %v)", live, archived, more)
	}
	// Opened for its author when published, and the mark went with the message.
	if opened, _ := store.OpenedBetween(channel.ID, "demo", 1, 3); !slices.Equal(opened, []int64{2, 3}) {
		t.Fatalf("opened marks are %v", opened)
	}
}

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

// sinceVersion3 undoes what versions 4 and 5 added, to wind a database back past them.
var sinceVersion3 = []string{
	"DROP TABLE visits",
	"ALTER TABLE messages DROP COLUMN subject",
	"ALTER TABLE rooms DROP COLUMN archive_period",
	"ALTER TABLE rooms DROP COLUMN archive_searchable",
	"DROP TABLE opened",
	"DROP TABLE archived_messages",
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
	for _, statement := range append(slices.Clone(sinceVersion3),
		"DROP TABLE IF EXISTS channel_audience", "DROP TABLE IF EXISTS channel_moderators",
		"DROP TABLE IF EXISTS submissions", "PRAGMA user_version = 1",
	) {
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
	if found, err := store.SubmissionsBy("anyone"); err != nil || len(found) != 0 {
		t.Fatalf("submissions was not restored: %v, %v", found, err)
	}
}

func TestADatabaseAtTheSharedVersionIsUpgraded(t *testing.T) {
	// The retired Python server stopped at version 2, so a database it wrote must
	// open here and gain what version 3 adds, with nothing it held disturbed.
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := open(t, path)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	channel, err := store.CreateRoom("News", "demo", ChannelKind, Admin, Persisted, "", nil)
	if err != nil {
		t.Fatalf("cannot arrange a channel: %v", err)
	}
	store.Close()

	for _, statement := range append(slices.Clone(sinceVersion3),
		"DROP TABLE channel_moderators", "DROP TABLE submissions", "PRAGMA user_version = 2",
	) {
		if _, err := raw(t, path).Exec(statement); err != nil {
			t.Fatalf("cannot arrange the database: %v", err)
		}
	}

	store, err = open(t, path)
	if err != nil {
		t.Fatalf("cannot upgrade a version 2 database: %v", err)
	}
	defer store.Close()

	if got := userVersion(t, path); got != SchemaVersion {
		t.Fatalf("user_version is %d, want %d", got, SchemaVersion)
	}
	if _, err := store.Appoint(channel.ID, "alice"); err != nil {
		t.Fatalf("channel_moderators was not created: %v", err)
	}
	if kept, err := store.Room(channel.ID); err != nil || kept == nil || kept.Moderators[0] != "alice" {
		t.Fatalf("the upgrade lost the channel: %v, %v", kept, err)
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
	// The retired Python server created presence and occupants for its own use.
	// No version covers them, so a database that has them still opens.
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
