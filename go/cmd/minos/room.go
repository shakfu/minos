package main

// `minos room ensure`: one-shot room founding for a dispatcher. See
// docs/dev/run-rooms.md for why pma founds a run's room and minosb does not.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"minos/internal/client"
)

// many is a flag that may be given more than once.
type many []string

func (m *many) String() string     { return strings.Join(*m, ",") }
func (m *many) Set(v string) error { *m = append(*m, v); return nil }

func room(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "ensure" {
		fmt.Fprintln(stderr, "usage: minos room ensure -project NAME -task KEY [-invite PRINCIPAL]... [-tag TAG]...")
		return 2
	}
	return ensure(args[1:], stdout, stderr)
}

// ensure prints the id of the room filed under a project and task, founding,
// inviting and reopening as needed. Credentials come from the environment
// alone, as minosb's do.
func ensure(args []string, stdout, stderr io.Writer) int {
	fallback := os.Getenv("MINOS_SERVER")
	if fallback == "" {
		fallback = "http://127.0.0.1:8000"
	}
	flags := flag.NewFlagSet("minos room ensure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", fallback, "base URL")
	project := flags.String("project", "", "the project the room is filed under; created if missing")
	task := flags.String("task", "", "the task the room is about; its title is task/<task>")
	var invites, tags many
	flags.Var(&invites, "invite", "a user, or @group, to invite; repeatable")
	flags.Var(&tags, "tag", "a tag for the project; repeatable")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	user, password := os.Getenv("MINOS_USER"), os.Getenv("MINOS_PASSWORD")
	switch {
	case *project == "" || *task == "":
		return refuse(stderr, "minos room ensure needs -project and -task")
	case user == "" || password == "":
		return refuse(stderr, "minos room ensure needs MINOS_USER and MINOS_PASSWORD in the environment")
	}

	session, chat, _, err := client.Connect(*server, user, password)
	if err != nil {
		fmt.Fprintf(stderr, "minos: could not connect: %v\n", err)
		return 1
	}
	defer func() {
		chat.Stop()
		_ = session.Logout()
	}()
	// Checked here: a non-administrator granted the room would otherwise find
	// it and never reach a call the server refuses.
	if !chat.IsAdmin() {
		return refuse(stderr, "Only an administrator may do that")
	}

	found, err := ensureRoom(chat, *project, *task, invites, tags)
	if err != nil {
		return refuse(stderr, "%v", err)
	}
	fmt.Fprintln(stdout, found.ID)
	return 0
}

func ensureRoom(c *client.Client, projectName, task string, invites, tags []string) (client.Room, error) {
	project, err := ensureProject(c, projectName)
	if err != nil {
		return client.Room{}, err
	}
	for _, tag := range tags {
		if _, err := c.Tag(project.ID, tag); err != nil {
			return client.Room{}, err
		}
	}
	principals, err := resolve(c, invites)
	if err != nil {
		return client.Room{}, err
	}

	title := "task/" + task
	found, ok := filed(c, project.ID, title)
	if !ok {
		created, err := c.CreateRoom(title, principals, project.ID, "task", task)
		if err == nil {
			return created, nil
		}
		// A concurrent ensure may have founded it: the server checks titles
		// under a lock, so the loser finds the winner's room.
		if found, ok = refound(c, func() (client.Room, bool) { return filed(c, project.ID, title) }); !ok {
			return client.Room{}, err
		}
	}

	for _, principal := range principals {
		if !granted(found, principal) {
			if found, err = c.Invite(found.ID, principal); err != nil {
				return client.Room{}, err
			}
		}
	}
	// A rework reads what the last run was told (design.md D5).
	if !found.Open() {
		if found, err = c.SetState(found.ID, true); err != nil {
			return client.Room{}, err
		}
	}
	return found, nil
}

func ensureProject(c *client.Client, name string) (client.Project, error) {
	byName := func() (client.Project, bool) {
		for _, project := range c.Projects() {
			if strings.EqualFold(project.Name, name) {
				return project, true
			}
		}
		return client.Project{}, false
	}
	if project, ok := byName(); ok {
		return project, nil
	}
	project, err := c.CreateProject(name, nil)
	if err == nil {
		return project, nil
	}
	if project, ok := refound(c, byName); ok {
		return project, nil
	}
	return client.Project{}, err
}

// refound syncs and looks again, after a create lost a race.
func refound[T any](c *client.Client, look func() (T, bool)) (T, bool) {
	if _, err := c.Sync(); err != nil {
		var none T
		return none, false
	}
	return look()
}

// filed is the admin-founded room with this title in the project. Titles
// compare without case, as the server's uniqueness check does.
func filed(c *client.Client, project, title string) (client.Room, bool) {
	for _, room := range c.Rooms() {
		if room.Authority == "admin" && room.Project == project && strings.EqualFold(room.Title, title) {
			return room, true
		}
	}
	return client.Room{}, false
}

// resolve reads each invite as a user, or a group when written `@name`.
func resolve(c *client.Client, invites []string) ([]client.Principal, error) {
	found := make([]client.Principal, 0, len(invites))
	for _, token := range invites {
		wanted, isGroup := strings.CutPrefix(token, "@")
		if !isGroup {
			found = append(found, client.Principal{Kind: "user", ID: token})
			continue
		}
		var match *client.Principal
		for _, group := range c.Groups() {
			if strings.EqualFold(group.Name, wanted) || group.ID == wanted {
				match = &client.Principal{Kind: "group", ID: group.ID}
				break
			}
		}
		if match == nil {
			return nil, &client.ChatError{Message: "No such group: " + wanted}
		}
		found = append(found, *match)
	}
	return found, nil
}

func granted(room client.Room, principal client.Principal) bool {
	for _, grant := range room.Grants {
		if grant == principal {
			return true
		}
	}
	return false
}

func refuse(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "minos: "+format+"\n", args...)
	return 2
}
