// Package timeline is the durable half of the messaging layer: groups, rooms,
// channels, and what was said in them.
//
// A room is an append-only sequence of messages, and every message carries a
// `seq` that is monotonic within its room. That number is the whole delivery
// contract: a client that sees a gap between the last sequence it holds and the
// one that just arrived asks for the difference. Nothing else detects a dropped,
// duplicated or out-of-order frame.
//
// Two things in here differ from the Python server this replaces, and both
// follow from one process rather than several:
//
//   - **Presence and occupancy are in memory.** They describe live connections,
//     and the process that holds the connections is the process that answers
//     for them. As rows they needed a worker column, liveness locks and a sweep
//     to reclaim what a dead worker left behind; none of that has anything to
//     answer here.
//   - **`empty_since` is still stored**, because it outlives the connections.
//     A restart empties every room at once, so start-up stamps every transient
//     room that is not already counting down -- which is what makes the promise
//     of deletion survive a restart rather than being forgotten by it.
package timeline

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Message kinds. Text is what a person typed; Event is what happened to the
// room or, in a channel, to the system.
const (
	Text  = "text"
	Event = "event"
)

// What the row is. A channel shares the storage and the sequence; only its door
// differs.
const (
	RoomKind    = "room"
	ChannelKind = "channel"
)

// Who founded it, and therefore who may invite to it.
const (
	Admin = "admin"
	User  = "user"
)

// Whether it is kept.
const (
	Persisted = "persisted"
	Transient = "transient"
)

// Principal kinds a grant can name.
const (
	PrincipalUser  = "user"
	PrincipalGroup = "group"
)

// ErrNoRoom is returned by Append for a room that does not exist.
var ErrNoRoom = errors.New("no such room")

// SchemaVersion is stamped in PRAGMA user_version and checked on open. It
// covers the tables both servers read; presence and occupants belong to the
// Python server alone and are not part of it. Raise it when the shared schema
// changes, in both implementations at once -- SCHEMA_VERSION in
// messaging/timeline.py is the same number.
const SchemaVersion = 2

// migrations is how to reach each version from the one before it. A version
// with no entry has no upgrade path and a database at the version below it is
// refused, which is what keeps a change that cannot be made in place from being
// applied as if it could. An empty slice means the step is additive: schema runs
// after every open and creates whatever is new, so nothing else has to be said.
//
// Statements here run before schema, in one transaction with the stamp.
var migrations = map[int][]string{
	2: {}, // channel_audience, and nothing else to move
}

const schema = `
CREATE TABLE IF NOT EXISTS groups (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS group_members (
    group_id TEXT NOT NULL,
    username TEXT NOT NULL,
    PRIMARY KEY (group_id, username)
);

CREATE TABLE IF NOT EXISTS rooms (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    kind        TEXT NOT NULL,
    authority   TEXT NOT NULL,
    retention   TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  REAL NOT NULL,
    high_seq    INTEGER NOT NULL DEFAULT 0,
    empty_since REAL
);

-- Who was invited. principal_kind is 'user' or 'group'; a group grant is
-- resolved through group_members at the moment access is checked, so it
-- follows the group rather than a snapshot of it.
CREATE TABLE IF NOT EXISTS grants (
    room_id        TEXT NOT NULL,
    principal_kind TEXT NOT NULL,
    principal_id   TEXT NOT NULL,
    granted_at     REAL NOT NULL,
    PRIMARY KEY (room_id, principal_kind, principal_id)
);

-- A channel's audience. Separate from grants because it is a different act:
-- chosen by the subscriber rather than granted to them.
CREATE TABLE IF NOT EXISTS subscriptions (
    channel_id TEXT NOT NULL,
    username   TEXT NOT NULL,
    PRIMARY KEY (channel_id, username)
);

-- A restricted channel's audience: the groups whose members may subscribe. No
-- rows means the channel is open. Groups only -- a channel admitting one named
-- person has misidentified itself and is a room.
CREATE TABLE IF NOT EXISTS channel_audience (
    channel_id  TEXT NOT NULL,
    group_id    TEXT NOT NULL,
    admitted_at REAL NOT NULL,
    PRIMARY KEY (channel_id, group_id)
);

CREATE TABLE IF NOT EXISTS messages (
    room_id TEXT NOT NULL,
    seq     INTEGER NOT NULL,
    author  TEXT NOT NULL,
    kind    TEXT NOT NULL,
    body    TEXT NOT NULL,
    at      REAL NOT NULL,
    PRIMARY KEY (room_id, seq)
);

-- What each user has read, as opposed to what their client has received. The
-- delivery cursor lives in the client and repairs gaps; this one answers what a
-- person has seen, and is the same fact from every device.
CREATE TABLE IF NOT EXISTS read_cursors (
    room_id  TEXT NOT NULL,
    username TEXT NOT NULL,
    seq      INTEGER NOT NULL,
    PRIMARY KEY (room_id, username)
);

CREATE INDEX IF NOT EXISTS grants_by_principal
    ON grants (principal_kind, principal_id);
`

// Principal names who a grant admits: a user, or a whole group.
type Principal struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Room is a room or a channel as the wire carries it.
type Room struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Kind      string      `json:"kind"`
	Authority string      `json:"authority"`
	Retention string      `json:"retention"`
	CreatedBy string      `json:"createdBy"`
	CreatedAt float64     `json:"createdAt"`
	Grants    []Principal `json:"grants"`
	Audience  []string    `json:"audience"`
	// The groups a channel is restricted to; empty is open, and a room has no
	// such rule at all.
	RestrictedTo []string `json:"restrictedTo"`
	Occupants    []string `json:"occupants"`
	LastSeq      int64    `json:"lastSeq"`
}

// Message is one entry in a room's log.
type Message struct {
	Room   string  `json:"room"`
	Seq    int64   `json:"seq"`
	Author string  `json:"author"`
	Kind   string  `json:"kind"`
	Body   string  `json:"body"`
	At     float64 `json:"at"`
}

// Group is a lasting set of users, named where access is decided.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// Timeline owns the database and the live connection state.
type Timeline struct {
	db           *sql.DB
	historyLimit int
	grace        time.Duration

	// Presence and occupancy, both keyed by an opaque id the caller gives back.
	live        sync.Mutex
	presence    map[string]string
	occupancies map[string]seat
}

type seat struct {
	room string
	user string
}

func now() float64 { return float64(time.Now().UnixNano()) / float64(time.Second) }

func Open(path string, historyLimit int, grace time.Duration) (*Timeline, error) {
	// One connection: a single writer cannot contend with itself, which removes
	// every SQLITE_BUSY path the multi-process design had to survive.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	if err := checkVersion(db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	store := &Timeline{
		db:           db,
		historyLimit: historyLimit,
		grace:        grace,
		presence:     map[string]string{},
		occupancies:  map[string]seat{},
	}

	// A restart ends every transient room: occupancy derives from live
	// connections and there are none yet, so any room not already counting down
	// starts now.
	if _, err := db.Exec(
		"UPDATE rooms SET empty_since = ? WHERE retention = ? AND empty_since IS NULL",
		now(), Transient,
	); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (t *Timeline) Close() error { return t.db.Close() }

// checkVersion brings a database to this schema, or refuses the ones that
// cannot be brought.
//
// PRAGMA user_version is zero both in an empty file and in one written before
// the marker existed, so the tables tell them apart: an empty file is stamped
// and then populated, one holding tables cannot be identified and is left
// untouched rather than read or repaired. The stamp commits before any table is
// created, so tables without a marker mean a database from before this check
// and nothing else. One transaction covers the steps and the stamp, so a failed
// upgrade leaves the version it started at.
func checkVersion(db *sql.DB) error {
	transaction, err := db.Begin()
	if err != nil {
		return err
	}
	defer transaction.Rollback()

	var version int
	if err := transaction.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == SchemaVersion {
		return nil
	}
	if version > SchemaVersion {
		return fmt.Errorf(
			"database is schema version %d; this server reads %d, so it was written"+
				" by a later server", version, SchemaVersion,
		)
	}

	if version == 0 {
		var tables int
		if err := transaction.QueryRow(
			"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'",
		).Scan(&tables); err != nil {
			return err
		}
		if tables > 0 {
			return errors.New(
				"database predates the schema version marker, so what it holds cannot be" +
					" identified: move it aside or delete it",
			)
		}
	} else if err := upgrade(transaction, version); err != nil {
		return err
	}

	if _, err := transaction.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return err
	}
	return transaction.Commit()
}

// upgrade runs every step between the version on disk and this one.
func upgrade(transaction *sql.Tx, version int) error {
	for step := version + 1; step <= SchemaVersion; step++ {
		statements, ok := migrations[step]
		if !ok {
			return fmt.Errorf(
				"database is schema version %d and there is no upgrade to %d:"+
					" move it aside or delete it", version, step,
			)
		}
		for _, statement := range statements {
			if _, err := transaction.Exec(statement); err != nil {
				return err
			}
		}
	}
	return nil
}

// -- groups ------------------------------------------------------------------

// CreateGroup creates a group, or returns the existing one with that id.
func (t *Timeline) CreateGroup(name, groupID string, members []string) (*Group, error) {
	if groupID == "" {
		groupID = uuid.NewString()
	}
	transaction, err := t.db.Begin()
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()

	if _, err := transaction.Exec(
		"INSERT OR IGNORE INTO groups (id, name, created_at) VALUES (?, ?, ?)",
		groupID, name, now(),
	); err != nil {
		return nil, err
	}
	for _, member := range members {
		if _, err := transaction.Exec(
			"INSERT OR IGNORE INTO group_members (group_id, username) VALUES (?, ?)",
			groupID, member,
		); err != nil {
			return nil, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return t.Group(groupID)
}

func (t *Timeline) Group(groupID string) (*Group, error) {
	group := Group{ID: groupID, Members: []string{}}
	err := t.db.QueryRow("SELECT name FROM groups WHERE id = ?", groupID).Scan(&group.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	members, err := t.strings(
		"SELECT username FROM group_members WHERE group_id = ? ORDER BY username", groupID)
	if err != nil {
		return nil, err
	}
	group.Members = members
	return &group, nil
}

func (t *Timeline) Groups() ([]Group, error) {
	ids, err := t.strings("SELECT id FROM groups ORDER BY name")
	if err != nil {
		return nil, err
	}
	groups := make([]Group, 0, len(ids))
	for _, id := range ids {
		group, err := t.Group(id)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	return groups, nil
}

func (t *Timeline) AssignGroup(groupID, username string) error {
	_, err := t.db.Exec(
		"INSERT OR IGNORE INTO group_members (group_id, username) VALUES (?, ?)",
		groupID, username)
	return err
}

func (t *Timeline) UnassignGroup(groupID, username string) error {
	_, err := t.db.Exec(
		"DELETE FROM group_members WHERE group_id = ? AND username = ?", groupID, username)
	return err
}

// -- rooms -------------------------------------------------------------------

// CreateRoom creates a room and returns its description. A room whose id
// already exists is left alone and returned as it is, so a well-known channel
// can be declared on every boot without a guard.
func (t *Timeline) CreateRoom(
	title, createdBy, kind, authority, retention, roomID string, grants []Principal,
) (*Room, error) {
	if roomID == "" {
		roomID = uuid.NewString()
	}
	at := now()

	transaction, err := t.db.Begin()
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()

	if _, err := transaction.Exec(
		"INSERT OR IGNORE INTO rooms"+
			" (id, title, kind, authority, retention, created_by, created_at, high_seq)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, 0)",
		roomID, title, kind, authority, retention, createdBy, at,
	); err != nil {
		return nil, err
	}
	for _, grant := range grants {
		if _, err := transaction.Exec(
			"INSERT OR IGNORE INTO grants"+
				" (room_id, principal_kind, principal_id, granted_at) VALUES (?, ?, ?, ?)",
			roomID, grant.Kind, grant.ID, at,
		); err != nil {
			return nil, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return t.Room(roomID)
}

// Room describes one room or channel, or reports nil when there is none.
func (t *Timeline) Room(roomID string) (*Room, error) {
	room := Room{
		ID: roomID, Grants: []Principal{}, Audience: []string{},
		RestrictedTo: []string{}, Occupants: []string{},
	}
	err := t.db.QueryRow(
		"SELECT title, kind, authority, retention, created_by, created_at, high_seq"+
			" FROM rooms WHERE id = ?", roomID,
	).Scan(&room.Title, &room.Kind, &room.Authority, &room.Retention,
		&room.CreatedBy, &room.CreatedAt, &room.LastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if room.Kind == ChannelKind {
		audience, err := t.subscribers(roomID)
		if err != nil {
			return nil, err
		}
		rule, err := t.AudienceRule(roomID)
		if err != nil {
			return nil, err
		}
		room.Audience, room.RestrictedTo = audience, rule
	} else {
		grants, err := t.grantsOf(roomID)
		if err != nil {
			return nil, err
		}
		audience, err := t.participants(roomID)
		if err != nil {
			return nil, err
		}
		room.Grants, room.Audience = grants, audience
	}

	room.Occupants = t.OccupantsOf(roomID)
	return &room, nil
}

// RoomNamed finds an existing room of this authority with this name.
//
// Compared without case, because the name exists to be referred to -- "post it
// in Engineering" has to resolve to one room, and two that differ only in
// capitalisation would not help anybody tell them apart.
func (t *Timeline) RoomNamed(title, authority, kind string) (*Room, error) {
	var id string
	err := t.db.QueryRow(
		"SELECT id FROM rooms WHERE kind = ? AND authority = ? AND lower(title) = lower(?)",
		kind, authority, title,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t.Room(id)
}

func (t *Timeline) grantsOf(roomID string) ([]Principal, error) {
	rows, err := t.db.Query(
		"SELECT principal_kind, principal_id FROM grants WHERE room_id = ?"+
			" ORDER BY principal_kind, principal_id", roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	grants := []Principal{}
	for rows.Next() {
		var grant Principal
		if err := rows.Scan(&grant.Kind, &grant.ID); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// participants is every user a grant admits, named directly or through a group.
func (t *Timeline) participants(roomID string) ([]string, error) {
	return t.strings(`
        SELECT principal_id AS username FROM grants
         WHERE room_id = ? AND principal_kind = 'user'
        UNION
        SELECT gm.username FROM grants g
          JOIN group_members gm ON gm.group_id = g.principal_id
         WHERE g.room_id = ? AND g.principal_kind = 'group'
         ORDER BY username`, roomID, roomID)
}

// subscribers is who a channel reaches: subscribed, and still eligible to be.
//
// Eligibility is re-read here rather than enforced when the subscription was
// stored, so leaving the last group that admitted someone stops their delivery
// at once. The row stays: it is their choice, and it applies again the moment
// they are admitted again.
func (t *Timeline) subscribers(channelID string) ([]string, error) {
	return t.strings(`
        SELECT s.username FROM subscriptions s
         WHERE s.channel_id = ?
           AND (NOT EXISTS (SELECT 1 FROM channel_audience
                             WHERE channel_id = s.channel_id)
                OR EXISTS (SELECT 1 FROM channel_audience ca
                             JOIN group_members gm ON gm.group_id = ca.group_id
                            WHERE ca.channel_id = s.channel_id
                              AND gm.username = s.username))
         ORDER BY s.username`, channelID)
}

// AudienceRule is the groups a channel is restricted to. Empty means open.
func (t *Timeline) AudienceRule(channelID string) ([]string, error) {
	return t.strings(
		"SELECT group_id FROM channel_audience WHERE channel_id = ? ORDER BY group_id",
		channelID)
}

// Eligible reports whether this user may subscribe to a channel, and be
// delivered to once they have.
func (t *Timeline) Eligible(channelID, username string) (bool, error) {
	var found int
	err := t.db.QueryRow(
		"SELECT 1 FROM channel_audience WHERE channel_id = ? LIMIT 1", channelID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	err = t.db.QueryRow(`
        SELECT 1 FROM channel_audience ca
          JOIN group_members gm ON gm.group_id = ca.group_id
         WHERE ca.channel_id = ? AND gm.username = ?
         LIMIT 1`, channelID, username).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AdmitGroup adds a group to a channel's audience, restricting the channel if
// it was open. It reports whether the group was not already admitted.
func (t *Timeline) AdmitGroup(channelID, groupID string) (bool, error) {
	return t.changed(
		"INSERT OR IGNORE INTO channel_audience"+
			" (channel_id, group_id, admitted_at) VALUES (?, ?, ?)",
		channelID, groupID, now())
}

// RevokeGroup withdraws one. Withdrawing the last leaves the channel open.
func (t *Timeline) RevokeGroup(channelID, groupID string) (bool, error) {
	return t.changed(
		"DELETE FROM channel_audience WHERE channel_id = ? AND group_id = ?",
		channelID, groupID)
}

// Audience is who a message in this room should be delivered to.
func (t *Timeline) Audience(roomID string) ([]string, error) {
	var kind string
	err := t.db.QueryRow("SELECT kind FROM rooms WHERE id = ?", roomID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if kind == ChannelKind {
		return t.subscribers(roomID)
	}
	return t.participants(roomID)
}

// HasAccess reports whether a grant admits this user, directly or by group.
func (t *Timeline) HasAccess(roomID, username string) (bool, error) {
	var kind string
	err := t.db.QueryRow("SELECT kind FROM rooms WHERE id = ?", roomID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var found int
	if kind == ChannelKind {
		err = t.db.QueryRow(`
            SELECT 1 FROM subscriptions s
             WHERE s.channel_id = ? AND s.username = ?
               AND (NOT EXISTS (SELECT 1 FROM channel_audience
                                 WHERE channel_id = s.channel_id)
                    OR EXISTS (SELECT 1 FROM channel_audience ca
                                 JOIN group_members gm ON gm.group_id = ca.group_id
                                WHERE ca.channel_id = s.channel_id
                                  AND gm.username = s.username))`,
			roomID, username).Scan(&found)
	} else {
		err = t.db.QueryRow(`
            SELECT 1 FROM grants
             WHERE room_id = ? AND principal_kind = 'user' AND principal_id = ?
            UNION ALL
            SELECT 1 FROM grants g
              JOIN group_members gm ON gm.group_id = g.principal_id
             WHERE g.room_id = ? AND g.principal_kind = 'group' AND gm.username = ?
             LIMIT 1`, roomID, username, roomID, username).Scan(&found)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// RoomsFor is every room this user may enter, most recently active first.
func (t *Timeline) RoomsFor(username string) ([]Room, error) {
	ids, err := t.strings(`
        SELECT r.id
          FROM rooms r
          LEFT JOIN messages msg ON msg.room_id = r.id
         WHERE r.kind = 'room' AND (
               EXISTS (SELECT 1 FROM grants
                        WHERE room_id = r.id AND principal_kind = 'user'
                          AND principal_id = ?)
            OR EXISTS (SELECT 1 FROM grants g
                         JOIN group_members gm ON gm.group_id = g.principal_id
                        WHERE g.room_id = r.id AND g.principal_kind = 'group'
                          AND gm.username = ?))
         GROUP BY r.id
         ORDER BY COALESCE(MAX(msg.at), r.created_at) DESC`, username, username)
	if err != nil {
		return nil, err
	}
	return t.describeAll(ids)
}

func (t *Timeline) ChannelsFor(username string) ([]Room, error) {
	ids, err := t.strings(`
        SELECT r.id
          FROM rooms r
          JOIN subscriptions s ON s.channel_id = r.id
          LEFT JOIN messages msg ON msg.room_id = r.id
         WHERE r.kind = 'channel' AND s.username = ?
           AND (NOT EXISTS (SELECT 1 FROM channel_audience WHERE channel_id = r.id)
                OR EXISTS (SELECT 1 FROM channel_audience ca
                             JOIN group_members gm ON gm.group_id = ca.group_id
                            WHERE ca.channel_id = r.id AND gm.username = ?))
         GROUP BY r.id
         ORDER BY COALESCE(MAX(msg.at), r.created_at) DESC`, username, username)
	if err != nil {
		return nil, err
	}
	return t.describeAll(ids)
}

func (t *Timeline) describeAll(ids []string) ([]Room, error) {
	rooms := make([]Room, 0, len(ids))
	for _, id := range ids {
		room, err := t.Room(id)
		if err != nil {
			return nil, err
		}
		if room != nil {
			rooms = append(rooms, *room)
		}
	}
	return rooms, nil
}

// DeleteRoom removes a room and everything that referred to it.
func (t *Timeline) DeleteRoom(roomID string) error {
	transaction, err := t.db.Begin()
	if err != nil {
		return err
	}
	defer transaction.Rollback()

	for _, statement := range []string{
		"DELETE FROM messages WHERE room_id = ?",
		"DELETE FROM grants WHERE room_id = ?",
		"DELETE FROM subscriptions WHERE channel_id = ?",
		"DELETE FROM channel_audience WHERE channel_id = ?",
		"DELETE FROM read_cursors WHERE room_id = ?",
		"DELETE FROM rooms WHERE id = ?",
	} {
		if _, err := transaction.Exec(statement, roomID); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

// -- grants and subscriptions ------------------------------------------------

// AddGrant invites a principal, reporting whether it was not already invited.
func (t *Timeline) AddGrant(roomID, kind, id string) (bool, error) {
	return t.changed(
		"INSERT OR IGNORE INTO grants"+
			" (room_id, principal_kind, principal_id, granted_at) VALUES (?, ?, ?, ?)",
		roomID, kind, id, now())
}

func (t *Timeline) RemoveGrant(roomID, kind, id string) (bool, error) {
	return t.changed(
		"DELETE FROM grants WHERE room_id = ? AND principal_kind = ? AND principal_id = ?",
		roomID, kind, id)
}

func (t *Timeline) Subscribe(channelID, username string) (bool, error) {
	return t.changed(
		"INSERT OR IGNORE INTO subscriptions (channel_id, username) VALUES (?, ?)",
		channelID, username)
}

func (t *Timeline) Unsubscribe(channelID, username string) (bool, error) {
	return t.changed(
		"DELETE FROM subscriptions WHERE channel_id = ? AND username = ?", channelID, username)
}

// -- messages ----------------------------------------------------------------

// History returns messages after `since`, oldest first.
//
// Capped at the tail rather than the head: a client that has been away for a
// thousand messages wants the recent ones. That cap is not a window a caller can
// page through -- asking again from the same cursor returns the same slice -- so
// a reply short of the room's end is a gap that will not be filled. It is
// detectable, which is the point.
func (t *Timeline) History(roomID string, since int64) ([]Message, error) {
	rows, err := t.db.Query(`
        SELECT room_id, seq, author, kind, body, at FROM (
            SELECT * FROM messages
             WHERE room_id = ? AND seq > ?
             ORDER BY seq DESC
             LIMIT ?
        ) ORDER BY seq ASC`, roomID, since, t.historyLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := []Message{}
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.Room, &message.Seq, &message.Author,
			&message.Kind, &message.Body, &message.At); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// Append stores one message and returns it, sequence number included.
//
// The read of the high-water mark and the insert that depends on it are one
// transaction, so two appends to a room cannot be handed the same number. The
// mark is advanced rather than recomputed: it is the room's own count of what it
// has ever issued, which keeps it correct if anything ever removes a message.
func (t *Timeline) Append(roomID, author, body, kind string) (Message, error) {
	transaction, err := t.db.Begin()
	if err != nil {
		return Message{}, err
	}
	defer transaction.Rollback()

	var seq int64
	err = transaction.QueryRow("SELECT high_seq FROM rooms WHERE id = ?", roomID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, fmt.Errorf("%w: %s", ErrNoRoom, roomID)
	}
	if err != nil {
		return Message{}, err
	}
	seq++

	at := now()
	if _, err := transaction.Exec(
		"UPDATE rooms SET high_seq = ? WHERE id = ?", seq, roomID); err != nil {
		return Message{}, err
	}
	if _, err := transaction.Exec(
		"INSERT INTO messages (room_id, seq, author, kind, body, at) VALUES (?, ?, ?, ?, ?, ?)",
		roomID, seq, author, kind, body, at); err != nil {
		return Message{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Message{}, err
	}
	return Message{Room: roomID, Seq: seq, Author: author, Kind: kind, Body: body, At: at}, nil
}

// MarkRead advances a user's read cursor. It never moves backwards.
func (t *Timeline) MarkRead(roomID, username string, seq int64) error {
	_, err := t.db.Exec(
		"INSERT INTO read_cursors (room_id, username, seq) VALUES (?, ?, ?)"+
			" ON CONFLICT (room_id, username) DO UPDATE SET seq = MAX(seq, excluded.seq)",
		roomID, username, seq)
	return err
}

func (t *Timeline) ReadCursors(username string) (map[string]int64, error) {
	rows, err := t.db.Query(
		"SELECT room_id, seq FROM read_cursors WHERE username = ?", username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cursors := map[string]int64{}
	for rows.Next() {
		var room string
		var seq int64
		if err := rows.Scan(&room, &seq); err != nil {
			return nil, err
		}
		cursors[room] = seq
	}
	return cursors, rows.Err()
}

// -- presence and occupancy --------------------------------------------------

// Arrive records a live connection and returns its presence id.
func (t *Timeline) Arrive(username string) string {
	id := uuid.NewString()
	t.live.Lock()
	defer t.live.Unlock()
	t.presence[id] = username
	return id
}

func (t *Timeline) Depart(presenceID string) {
	t.live.Lock()
	defer t.live.Unlock()
	delete(t.presence, presenceID)
}

func (t *Timeline) Online() map[string]bool {
	t.live.Lock()
	defer t.live.Unlock()
	online := make(map[string]bool, len(t.presence))
	for _, username := range t.presence {
		online[username] = true
	}
	return online
}

// Enter records that a user is in a room and returns the occupancy id.
//
// Entering clears empty_since: a transient room with somebody in it is not
// counting down, and a re-entry during the grace period is exactly the case that
// must cancel the deletion.
func (t *Timeline) Enter(roomID, username string) (string, error) {
	id := uuid.NewString()

	t.live.Lock()
	t.occupancies[id] = seat{room: roomID, user: username}
	t.live.Unlock()

	_, err := t.db.Exec("UPDATE rooms SET empty_since = NULL WHERE id = ?", roomID)
	return id, err
}

// Exit drops one occupancy and returns the room it was in, or "".
//
// When it was the last one, the room records the instant it emptied. A stored
// fact rather than a timer, because it has to survive the process that observed
// it.
func (t *Timeline) Exit(occupancyID string) (string, error) {
	t.live.Lock()
	held, ok := t.occupancies[occupancyID]
	if !ok {
		t.live.Unlock()
		return "", nil
	}
	delete(t.occupancies, occupancyID)

	occupied := false
	for _, other := range t.occupancies {
		if other.room == held.room {
			occupied = true
			break
		}
	}
	t.live.Unlock()

	if occupied {
		return held.room, nil
	}
	_, err := t.db.Exec(
		"UPDATE rooms SET empty_since = ? WHERE id = ? AND empty_since IS NULL",
		now(), held.room)
	return held.room, err
}

func (t *Timeline) OccupantsOf(roomID string) []string {
	t.live.Lock()
	defer t.live.Unlock()

	seen := map[string]bool{}
	for _, held := range t.occupancies {
		if held.room == roomID {
			seen[held.user] = true
		}
	}

	occupants := make([]string, 0, len(seen))
	for username := range seen {
		occupants = append(occupants, username)
	}
	sort.Strings(occupants)
	return occupants
}

// ExpiredTransientRooms lists transient rooms whose grace period has run out.
func (t *Timeline) ExpiredTransientRooms() ([]string, error) {
	return t.strings(
		"SELECT id FROM rooms"+
			" WHERE kind = 'room' AND retention = 'transient'"+
			"   AND empty_since IS NOT NULL AND ? - empty_since >= ?",
		now(), t.grace.Seconds())
}

// -- helpers -----------------------------------------------------------------

func (t *Timeline) strings(query string, args ...any) ([]string, error) {
	rows, err := t.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (t *Timeline) changed(query string, args ...any) (bool, error) {
	result, err := t.db.Exec(query, args...)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}
