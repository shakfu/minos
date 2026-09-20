package timeline

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// fresh is a store whose grace period is already over the moment a room empties.
func fresh(t *testing.T) (*Timeline, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := Open(path, 200, 0, time.Hour)
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

func alone(t *testing.T, store *Timeline, authority string) *Room {
	t.Helper()
	room, err := store.CreateRoom("Solo", "alice", RoomKind, authority, Transient, "",
		[]Principal{{Kind: PrincipalUser, ID: "alice"}}, Filing{})
	if err != nil {
		t.Fatalf("cannot create a room: %v", err)
	}
	return room
}

func TestTheLastGrantOfAnAdHocRoomTakesTheRoomWithIt(t *testing.T) {
	store, _ := fresh(t)
	room := alone(t, store, User)
	if got, err := store.RemoveGrant(room.ID, PrincipalUser, "alice"); err != nil || got != RoomDeleted {
		t.Fatalf("RemoveGrant = %v, %v", got, err)
	}
	if gone, _ := store.Room(room.ID); gone != nil {
		t.Fatal("the room is still there")
	}
}

func TestThePermanentRoomKeepsItsLastGrant(t *testing.T) {
	store, _ := fresh(t)
	room := alone(t, store, Admin)
	if got, err := store.RemoveGrant(room.ID, PrincipalUser, "alice"); err != nil || got != LastKept {
		t.Fatalf("RemoveGrant = %v, %v", got, err)
	}
	if kept, _ := store.Room(room.ID); kept == nil || len(kept.Grants) != 1 {
		t.Fatalf("the grant went: %+v", kept)
	}
	if got, _ := store.RemoveGrant(room.ID, PrincipalUser, "bob"); got != NotGranted {
		t.Fatalf("removing a grant never made gave %v", got)
	}
}

// The sweep listed the room; an entry before the delete must keep it.
func TestAnExpiredRoomThatWasEnteredIsNotDeleted(t *testing.T) {
	store, _ := fresh(t)
	room := alone(t, store, User)
	occupancy, _ := store.Enter(room.ID, "alice")
	if _, err := store.Exit(occupancy); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if expired, _ := store.ExpiredTransientRooms(); len(expired) != 1 {
		t.Fatalf("expired: %v", expired)
	}

	if _, err := store.Enter(room.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteExpired(room.ID); err != nil || deleted {
		t.Fatalf("DeleteExpired = %v, %v", deleted, err)
	}
}

func TestADeletedRoomCannotBeEntered(t *testing.T) {
	store, _ := fresh(t)
	room := alone(t, store, User)
	occupancy, _ := store.Enter(room.ID, "alice")
	store.Exit(occupancy)
	time.Sleep(time.Millisecond)
	if deleted, err := store.DeleteExpired(room.ID); err != nil || !deleted {
		t.Fatalf("DeleteExpired = %v, %v", deleted, err)
	}
	if _, err := store.Enter(room.ID, "alice"); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("entering a deleted room gave %v", err)
	}
	if occupants := store.OccupantsOf(room.ID); len(occupants) != 0 {
		t.Fatalf("a deleted room has occupants %v", occupants)
	}
}

func TestReleaseStartsTheCountdown(t *testing.T) {
	store, _ := fresh(t)
	room := alone(t, store, User)
	store.Enter(room.ID, "alice")
	store.Enter(room.ID, "alice")
	if released, err := store.Release(room.ID, "alice"); err != nil || released != 2 {
		t.Fatalf("Release = %v, %v", released, err)
	}
	time.Sleep(time.Millisecond)
	if expired, _ := store.ExpiredTransientRooms(); len(expired) != 1 {
		t.Fatalf("the room is not counting down: %v", expired)
	}
}

func TestPresenceCountsConnectionsPerUser(t *testing.T) {
	store, _ := fresh(t)
	laptop, first := store.Arrive("bob")
	phone, second := store.Arrive("bob")
	if !first || second {
		t.Fatalf("arrivals reported first=%v, %v", first, second)
	}
	if store.Depart(laptop) {
		t.Fatal("bob's first departure was taken for his last")
	}
	if !store.Depart(phone) || store.Online()["bob"] {
		t.Fatal("bob's last departure was not reported")
	}
}

func TestARoomNobodyEnteredExpiresAfterItsOwnPeriod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.db")
	store, err := Open(path, 200, time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	unattended := alone(t, store, User)
	attended := alone(t, store, User)
	store.Enter(attended.ID, "alice")
	time.Sleep(time.Millisecond)

	if expired, _ := store.ExpiredTransientRooms(); !slices.Equal(expired, []string{unattended.ID}) {
		t.Fatalf("expired: %v", expired)
	}
	if deleted, err := store.DeleteExpired(attended.ID); err != nil || deleted {
		t.Fatalf("an occupied room was deleted: %v, %v", deleted, err)
	}
	if deleted, err := store.DeleteExpired(unattended.ID); err != nil || !deleted {
		t.Fatalf("DeleteExpired = %v, %v", deleted, err)
	}
}
