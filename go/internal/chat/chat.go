// Package chat binds the messaging layer to this server's websocket protocol.
//
// Everything conversational lives in messaging/, which knows nothing about the
// OS.js wire format or who has an account here. This package is the seam: it
// maps operation names to messaging calls, turns a refusal into the error shape
// the client expects, and fans deliveries out over the connection registry.
//
// Two things it owns rather than messaging/:
//
//   - **Who is an administrator.** The messaging layer takes it as an argument,
//     so the question of who counts stays with the host that has the accounts.
//   - **Occupancy for the life of a connection.** A user who is in a room holds
//     an occupancy, and a transient room dies once its last one is released. A
//     connection that drops must therefore release everything it held, or a room
//     nobody is in stays alive forever.
package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"minos/internal/config"
	"minos/internal/messaging"
	"minos/internal/socket"
	"minos/internal/timeline"
)

// Application is the package name a client addresses its operations to.
const Application = "Chat"

// Handler is the websocket-facing half: operation names in, replies out.
type Handler struct {
	service  *messaging.Messaging
	registry *socket.Registry
	settings config.Config

	// Connection -> the occupancies it holds. A connection can be in more than
	// one room at once, and all of them are released together when it goes away.
	mutex       sync.Mutex
	occupancies map[*socket.Connection]map[string]bool

	stop chan struct{}
	done sync.WaitGroup
}

// New assembles the messaging layer against this server's transport.
func New(store *timeline.Timeline, registry *socket.Registry, settings config.Config) *Handler {
	handler := &Handler{
		registry:    registry,
		settings:    settings,
		occupancies: map[*socket.Connection]map[string]bool{},
		stop:        make(chan struct{}),
	}

	// One process serves every connection, so a delivery is a fan-out over the
	// local registry and nothing else. This is the callback the whole message
	// bus used to sit behind.
	deliver := func(audience []string, event any) {
		wanted := make(map[string]bool, len(audience))
		for _, username := range audience {
			wanted[username] = true
		}
		registry.Push(Application, event, func(profile socket.Profile) bool {
			return wanted[profile.Username]
		})
	}

	handler.service = messaging.New(store, deliver, settings.Roster)
	registry.Register(Application, handler.Handle)
	return handler
}

func (h *Handler) Service() *messaging.Messaging { return h.service }

// EnsureSystemChannel declares the one space nobody is invited to: machine
// events, everyone subscribed. A channel rather than a room, because nothing
// typed goes into it.
func (h *Handler) EnsureSystemChannel() error {
	roster := h.settings.Roster()
	_, err := h.service.EnsureChannel(config.SystemChannel, "System", roster)
	return err
}

// PublishSystemEvent announces a server-side event on the system channel. A
// failure is swallowed: a channel must never be able to fail a request that has
// already been carried out.
func (h *Handler) PublishSystemEvent(text string) {
	h.service.PostEventQuietly(config.SystemChannel, text, h.settings.Roster())
}

// createChannel founds a channel and says so where everybody is listening.
//
// A new channel has no subscribers, so the only way anyone learns it exists is
// the machine channel every account is already in.
func (h *Handler) createChannel(
	username string, isAdmin bool, title string, groups []any,
) (any, error) {
	channel, err := h.service.CreateChannel(username, isAdmin, title, groups)
	if err != nil {
		return nil, err
	}
	h.PublishSystemEvent(fmt.Sprintf(
		"%s opened the channel %s (%s)", username, channel.Title, channel.ID))
	return channel, nil
}

// StartSweeper runs the transient-room sweep until the server stops.
//
// The deletion is a promise to the people who spoke in a room, so the check has
// to run whether or not anybody is connected -- which is why it is a goroutine
// here rather than something a request happens to trigger.
func (h *Handler) StartSweeper() {
	h.done.Add(1)
	go func() {
		defer h.done.Done()
		ticker := time.NewTicker(h.settings.Sweep)
		defer ticker.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-ticker.C:
				if _, err := h.service.Sweep(); err != nil {
					log.Printf("Transient room sweep failed: %v", err)
				}
			}
		}
	}()
}

func (h *Handler) Close() {
	close(h.stop)
	h.done.Wait()
}

// -- the life of a connection ------------------------------------------------

func (h *Handler) Disconnect(connection *socket.Connection) {
	h.mutex.Lock()
	held := h.occupancies[connection]
	delete(h.occupancies, connection)
	h.mutex.Unlock()

	for occupancy := range held {
		if _, err := h.service.Exit(occupancy); err != nil {
			log.Printf("Could not release occupancy %s: %v", occupancy, err)
		}
	}
}

// -- dispatch ----------------------------------------------------------------

type reply struct {
	Error string `json:"error"`
}

// Handle routes one osjs/application:socket:message frame.
func (h *Handler) Handle(connection *socket.Connection, respond func(any), args []json.RawMessage) {
	fields := map[string]json.RawMessage{}
	if len(args) > 0 {
		_ = json.Unmarshal(args[0], &fields)
	}

	profile := connection.Profile()
	username, isAdmin := profile.Username, profile.IsAdmin()

	result, err := h.run(connection, username, isAdmin, fields)
	if err != nil {
		var refusal *messaging.Refusal
		if errors.As(err, &refusal) {
			respond(reply{Error: refusal.Message})
			return
		}
		log.Printf("Chat operation failed: %v", err)
		respond(reply{Error: "Request failed"})
		return
	}
	respond(result)
}

func (h *Handler) run(
	connection *socket.Connection, username string, isAdmin bool,
	fields map[string]json.RawMessage,
) (any, error) {
	switch text(fields, "op") {
	case "sync":
		return h.service.Sync(username, isAdmin)

	case "history":
		return h.service.History(username, roomID(fields), cursor(fields, "since"))

	case "send":
		return h.service.Send(username, roomID(fields), text(fields, "body"))

	case "open":
		invite, err := list(fields, "invite")
		if err != nil {
			return nil, err
		}
		return h.service.OpenRoom(username, invite, text(fields, "title"), retention(fields))

	case "create":
		invite, err := list(fields, "invite")
		if err != nil {
			return nil, err
		}
		return h.service.CreateRoom(username, isAdmin, text(fields, "title"), invite)

	case "invite":
		principal, err := value(fields, "principal")
		if err != nil {
			return nil, err
		}
		return h.service.Invite(username, isAdmin, roomID(fields), principal)

	case "uninvite":
		principal, err := value(fields, "principal")
		if err != nil {
			return nil, err
		}
		return h.service.Uninvite(username, isAdmin, roomID(fields), principal)

	case "leave":
		return h.service.Leave(username, roomID(fields))

	case "enter":
		return h.enter(connection, username, roomID(fields))

	case "exit":
		return h.exit(connection, text(fields, "occupancy"))

	case "read":
		return h.service.MarkRead(username, roomID(fields), cursor(fields, "seq"))

	case "group.create":
		members, err := list(fields, "members")
		if err != nil {
			return nil, err
		}
		return h.service.CreateGroup(isAdmin, text(fields, "name"), members)

	case "group.assign":
		return h.service.AssignGroup(isAdmin, text(fields, "group"), text(fields, "username"))

	case "group.unassign":
		return h.service.UnassignGroup(isAdmin, text(fields, "group"), text(fields, "username"))

	case "subscribe":
		return h.service.Subscribe(username, text(fields, "channel"))

	case "unsubscribe":
		return h.service.Unsubscribe(username, text(fields, "channel"))

	case "channel.create":
		groups, err := list(fields, "groups")
		if err != nil {
			return nil, err
		}
		return h.createChannel(username, isAdmin, text(fields, "title"), groups)

	case "channel.publish":
		return h.service.PublishMessage(
			username, isAdmin, text(fields, "channel"), text(fields, "body"))

	case "channel.admit":
		return h.service.Admit(isAdmin, text(fields, "channel"), text(fields, "group"))

	case "channel.revoke":
		return h.service.Revoke(isAdmin, text(fields, "channel"), text(fields, "group"))

	case "channel.appoint":
		return h.service.Appoint(isAdmin, text(fields, "channel"), text(fields, "username"))

	case "channel.dismiss":
		return h.service.Dismiss(isAdmin, text(fields, "channel"), text(fields, "username"))

	case "channel.submit":
		return h.service.Submit(username, text(fields, "channel"), text(fields, "body"))

	case "channel.queue":
		return h.service.Queue(username, text(fields, "channel"))

	case "submission.approve":
		return h.service.Approve(username, text(fields, "submission"))

	case "submission.reject":
		return h.service.Reject(username, text(fields, "submission"), text(fields, "comment"))

	case "submission.acknowledge":
		return h.service.Acknowledge(username, text(fields, "submission"))
	}

	return nil, &messaging.Refusal{Message: "No such chat operation: " + text(fields, "op")}
}

// enter takes a place in a room, remembering it against this connection.
func (h *Handler) enter(connection *socket.Connection, username, room string) (any, error) {
	result, err := h.service.Enter(username, room)
	if err != nil {
		return nil, err
	}

	h.mutex.Lock()
	held, ok := h.occupancies[connection]
	if !ok {
		held = map[string]bool{}
		h.occupancies[connection] = held
	}
	held[result.Occupancy] = true
	h.mutex.Unlock()

	return result, nil
}

func (h *Handler) exit(connection *socket.Connection, occupancy string) (any, error) {
	h.mutex.Lock()
	held := h.occupancies[connection]
	if !held[occupancy] {
		h.mutex.Unlock()
		// Not this connection's to release. Releasing another's would let one
		// client end a room somebody else is sitting in.
		return nil, &messaging.Refusal{Message: "Not in that room"}
	}
	delete(held, occupancy)
	h.mutex.Unlock()

	return h.service.Exit(occupancy)
}

// -- reading a request -------------------------------------------------------
//
// Fields arrive as raw JSON so a value of the wrong type can be answered the way
// the contract says rather than failing the whole request. A room id that is not
// a string is not a room that exists; an unparseable cursor is no cursor at all.
// The exception is a field that must be a list: there the type *is* the request,
// and a number where an array belongs is a broken client rather than a refusal.

func text(fields map[string]json.RawMessage, name string) string {
	raw, ok := fields[name]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	if string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// roomID renders a room field the way an error message quotes it: a string as
// itself, anything else as it arrived, and a missing one as None, the spelling
// inherited from the retired Python server.
func roomID(fields map[string]json.RawMessage) string {
	raw, ok := fields["room"]
	if !ok || string(raw) == "null" {
		return "None"
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return string(raw)
}

func cursor(fields map[string]json.RawMessage, name string) int64 {
	raw, ok := fields[name]
	if !ok {
		return 0
	}

	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return max(0, number)
	}
	// A string that spells a number counts, which is what a client that has
	// round-tripped its cursor through a text field sends.
	var spelled string
	if err := json.Unmarshal(raw, &spelled); err == nil {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(spelled), 10, 64); err == nil {
			return max(0, parsed)
		}
	}
	return 0
}

func retention(fields map[string]json.RawMessage) string {
	if chosen := text(fields, "retention"); chosen != "" {
		return chosen
	}
	return timeline.Persisted
}

func value(fields map[string]json.RawMessage, name string) (any, error) {
	raw, ok := fields[name]
	if !ok {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func list(fields map[string]json.RawMessage, name string) ([]any, error) {
	raw, ok := fields[name]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var decoded []any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}
