package conformance

// Projects: wire-contract 12. A project bundles places as a group bundles
// principals. It holds no messages and decides no access, so every caller sees
// every project and filing a room moves nobody's access.

import (
	"strings"
	"testing"
)

// roomOf is one room as this client sees it, or nil.
func roomOf(socket *Socket, id string) Obj {
	return byID(socket.Call("sync")["rooms"])[id]
}

// projectOf is one project as this client sees it, or nil.
func projectOf(socket *Socket, id string) Obj {
	return byID(socket.Call("sync")["projects"])[id]
}

func TestAProjectIsCreatedAndCarriesARoom(t *testing.T) {
	demo := connect(t, admin)
	name := unique("cynn")

	reply := demo.Call("project.create", "name", name, "tags", []string{"Go", "infra", "go"})
	same(t, reply["ok"], true)
	project := obj(reply["project"])
	keySet(t, project, "id", "name", "createdAt", "tags")
	same(t, project["name"], name)
	// Folded and deduplicated: a tag exists to be filtered on.
	same(t, project["tags"], []string{"go", "infra"})

	room := demo.Call("create", "title", unique("task"), "invite", []any{})
	for field, want := range map[string]string{"project": "", "scope": "", "task": ""} {
		same(t, room[field], want)
	}

	filed := demo.Call("project.file", "room", room["id"], "project", project["id"])
	same(t, filed["ok"], true)
	same(t, obj(filed["room"])["project"], project["id"])
	// Filed without saying what it is about: the project as a whole.
	same(t, obj(filed["room"])["scope"], "project")
	same(t, roomOf(demo, str(room["id"]))["project"], project["id"])

	// Empty files it under none; there is no separate unfile op.
	cleared := demo.Call("project.file", "room", room["id"], "project", "")
	same(t, obj(cleared["room"])["project"], "")
	same(t, obj(cleared["room"])["scope"], "")
}

// A scope is a position within a project, and a task names one. The label is
// opaque: the server stores it and never reads it.
func TestARoomCarriesItsScopeAndTask(t *testing.T) {
	demo := connect(t, admin)
	project := obj(demo.Call("project.create", "name", unique("cynn"), "tags", []any{})["project"])
	room := demo.Call("create", "title", unique("task"), "invite", []any{})

	filed := obj(demo.Call("project.file", "room", room["id"], "project", project["id"],
		"scope", "task", "task", "31")["room"])
	same(t, filed["scope"], "task")
	same(t, filed["task"], "31")

	// Unfiling clears the position with the project.
	cleared := obj(demo.Call("project.file", "room", room["id"], "project", "")["room"])
	same(t, cleared["scope"], "")
	same(t, cleared["task"], "")

	for _, case_ := range []struct{ scope, task, refusal string }{
		{"task", "", "A task room names its task"},
		{"project", "31", "Only a task room names a task"},
		{"epic", "", `A scope is "project" or "task"`},
	} {
		same(t, demo.Refuse("project.file", "room", room["id"], "project", project["id"],
			"scope", case_.scope, "task", case_.task), case_.refusal)
	}
	// Without a project there is no position to hold.
	same(t, demo.Refuse("project.file", "room", room["id"], "project", "", "scope", "task",
		"task", "31"), "A room under no project has no scope")
}

// Tagging answers the state rather than the change, so a caller that means to
// end up tagged need not know whether it already was.
func TestTagsClassifyAProjectAndFoldToLowerCase(t *testing.T) {
	demo := connect(t, admin)
	project := obj(demo.Call("project.create", "name", unique("cynn"), "tags", []any{})["project"])
	same(t, project["tags"], []any{})

	same(t, obj(demo.Call("project.tag", "project", project["id"], "tag", "Infra")["project"])["tags"],
		[]string{"infra"})
	same(t, obj(demo.Call("project.tag", "project", project["id"], "tag", "infra")["project"])["tags"],
		[]string{"infra"})
	same(t, obj(demo.Call("project.tag", "project", project["id"], "tag", "go")["project"])["tags"],
		[]string{"go", "infra"})

	same(t, obj(demo.Call("project.untag", "project", project["id"], "tag", "GO")["project"])["tags"],
		[]string{"infra"})
	same(t, obj(demo.Call("project.untag", "project", project["id"], "tag", "go")["project"])["tags"],
		[]string{"infra"})

	same(t, demo.Refuse("project.tag", "project", project["id"], "tag", "two words"),
		"A tag is one word")
	same(t, demo.Refuse("project.tag", "project", project["id"], "tag", "  "), "A tag needs text")
	same(t, demo.Refuse("project.tag", "project", "absent", "tag", "go"), "No such project: absent")

	// Dissolving takes the tags with it; nothing else carries them.
	demo.Call("project.dissolve", "project", project["id"])
	truth(t, projectOf(demo, str(project["id"])) == nil, "the project is still listed")
}

// A project decides no access, so it has no audience to filter by: a
// non-administrator who reaches none of its rooms still sees it in sync.
func TestEverybodySeesEveryProject(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	name := unique("cynn")

	alice.Drain()
	project := obj(demo.Call("project.create", "name", name, "tags", []any{})["project"])

	pushed := alice.ExpectPush(PushOf("project"))
	same(t, obj(pushed["project"])["id"], project["id"])
	truth(t, projectOf(alice, str(project["id"])) != nil, "alice does not see the project")

	// The room it holds is demo's alone, and alice still sees the project.
	room := demo.Call("create", "title", unique("task"), "invite", []any{})
	demo.Call("project.file", "room", room["id"], "project", project["id"])
	truth(t, roomOf(alice, str(room["id"])) == nil, "alice sees a room she was not invited to")
	truth(t, projectOf(alice, str(project["id"])) != nil, "alice lost the project")
}

// Filing is a listing, not a grant. Nobody joins or leaves a room over it.
func TestFilingARoomMovesNobodysAccess(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	project := obj(demo.Call("project.create", "name", unique("cynn"), "tags", []any{})["project"])
	room := demo.Call("create", "title", unique("task"), "invite", []string{"alice"})

	// Wait for the invitation push before draining, or the one filing sends
	// races it and the drain clears nothing.
	alice.ExpectPush(PushOf("room"))
	alice.Drain()
	demo.Call("project.file", "room", room["id"], "project", project["id"])

	pushed := alice.ExpectPush(PushOf("room"))
	same(t, obj(pushed["room"])["project"], project["id"])
	same(t, sorted(obj(pushed["room"])["audience"]), []string{"alice", "demo"})
	truth(t, alice.Pushes.Empty(), "filing a room sent alice something else")
}

// Names are compared without case, as room titles are: a project is named
// where a room is filed, so two that differ only in capitalisation would not
// help anybody tell them apart.
func TestAProjectNameIsTakenOnlyOnce(t *testing.T) {
	demo := connect(t, admin)
	name := unique("cynn")
	demo.Call("project.create", "name", name, "tags", []any{})

	for _, taken := range []string{name, strings.ToUpper(name)} {
		same(t, demo.Refuse("project.create", "name", taken, "tags", []any{}),
			"There is already a project called "+name)
	}
	same(t, demo.Refuse("project.create", "name", "   ", "tags", []any{}), "A project needs a name")
}

// A transient room is discarded when everyone leaves, so filing it records a
// place about to stop existing.
func TestATransientRoomIsNotFiled(t *testing.T) {
	demo := connect(t, admin)
	project := obj(demo.Call("project.create", "name", unique("cynn"), "tags", []any{})["project"])
	meeting := demo.Call("open", "invite", []string{"alice"}, "title", "", "retention", "transient")

	same(t, demo.Refuse("project.file", "room", meeting["id"], "project", project["id"]),
		"A transient room is not filed under a project")
}

// Dissolving keeps the rooms: a project holds no messages, so there is nothing
// in it to lose.
func TestDissolvingAProjectFreesItsRooms(t *testing.T) {
	demo := connect(t, admin)
	project := obj(demo.Call("project.create", "name", unique("cynn"), "tags", []any{})["project"])
	room := demo.Call("create", "title", unique("task"), "invite", []any{})
	demo.Call("project.file", "room", room["id"], "project", project["id"])

	demo.Drain()
	same(t, demo.Call("project.dissolve", "project", project["id"])["ok"], true)

	demo.ExpectPush(PushOf("projectGone"))
	truth(t, projectOf(demo, str(project["id"])) == nil, "the project is still listed")

	kept := roomOf(demo, str(room["id"]))
	truth(t, kept != nil, "dissolving the project took the room with it")
	same(t, kept["project"], "")
	same(t, kept["scope"], "")
}

func TestAProjectMustExistToBeNamed(t *testing.T) {
	demo := connect(t, admin)
	room := demo.Call("create", "title", unique("task"), "invite", []any{})

	same(t, demo.Refuse("project.file", "room", room["id"], "project", "absent"),
		"No such project: absent")
	same(t, demo.Refuse("project.file", "room", "absent", "project", ""), "No such room: absent")
	same(t, demo.Refuse("project.dissolve", "project", "absent"), "No such project: absent")
}

func TestOnlyAnAdministratorKeepsProjects(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	project := obj(demo.Call("project.create", "name", unique("cynn"), "tags", []any{})["project"])
	room := demo.Call("create", "title", unique("task"), "invite", []string{"alice"})
	refusal := "Only an administrator may do that"

	same(t, alice.Refuse("project.create", "name", unique("mine"), "tags", []any{}), refusal)
	same(t, alice.Refuse("project.file", "room", room["id"], "project", project["id"]), refusal)
	same(t, alice.Refuse("project.dissolve", "project", project["id"]), refusal)
	same(t, alice.Refuse("project.tag", "project", project["id"], "tag", "go"), refusal)
	same(t, alice.Refuse("project.untag", "project", project["id"], "tag", "go"), refusal)
}
