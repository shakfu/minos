// Package gateway exposes one worker's room to an A2A client.
//
// The gateway is an ordinary minos client, like the terminal client: it holds a
// session, posts to the room and reads it. It serves A2A v1.0 over JSON-RPC
// (https://a2a-protocol.org/latest/specification) so an orchestrator that
// speaks A2A can talk to a worker without learning the minos wire. Nothing on
// the server, the broker or the container changes, and the room stays the
// record: everything the orchestrator says is said in the room, under the
// gateway's account, where the developer reads it.
//
// # Mapping
//
// A message starts a task. Its text is posted to the room, and the worker's
// first text message after it is the task's artifact; the task then completes.
// Tasks in flight together are answered by the same message, because a turn
// answers everything held while it ran. A worker's `say` and its relayed turn
// look the same in the room, so a `say` mid-turn completes a waiting task.
//
// CancelTask posts `/interrupt` to the room. It stops the turn only if the
// gateway's account is one of the broker's `-interrupters`.
//
// Push notifications are refused: a request from the host to an address the
// caller chose is the webhook that design.md section 12 rules out. File parts
// are refused for the same reason the bus carries no files.
//
// See docs/dev/agent2agent.md.
package gateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"minos/internal/client"
)

// RPCPath is where the JSON-RPC binding is served. The agent card is at
// a2asrv.WellKnownAgentCardPath.
const RPCPath = "/a2a"

// DefaultTimeout is how long a task waits for the worker before it fails.
const DefaultTimeout = 30 * time.Minute

// Config is one gateway: the account it speaks as, and the worker it fronts.
type Config struct {
	Server   string
	User     string
	Password string

	// Room carries the conversation; Worker is the account whose messages
	// answer a task.
	Room   string
	Worker string

	// Base is the URL the gateway is reached at, as the card advertises it.
	Base string
	// Token is the bearer token every RPC must carry. The card is public.
	Token string
	// Timeout bounds the wait for the worker's reply.
	Timeout time.Duration
}

// Gateway is one room, served as one A2A agent.
type Gateway struct {
	chat   *client.Client
	config Config
	me     string
	title  string

	mutex sync.Mutex
	// changed is closed when anything arrives and replaced, so every waiting
	// task wakes.
	changed chan struct{}
	// missing counts messages lost to a gap the client could not backfill.
	// A reply may be among them, so every waiting task fails.
	missing int64
	// sending serializes posts, so the log's last sequence before a post is
	// below the post itself.
	sending sync.Mutex
}

// Open logs in and checks that the room is readable and the worker is
// someone else.
func Open(config Config) (*Gateway, error) {
	switch {
	case config.Room == "":
		return nil, errors.New("a gateway needs a room")
	case config.Worker == "":
		return nil, errors.New("a gateway needs the worker's account")
	case config.Token == "":
		return nil, errors.New("a gateway needs a bearer token")
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultTimeout
	}

	_, chat, profile, err := client.Connect(config.Server, config.User, config.Password)
	if err != nil {
		return nil, err
	}
	if profile.Username == config.Worker {
		chat.Stop()
		return nil, fmt.Errorf("%s cannot front its own messages", profile.Username)
	}
	room, ok := chat.Space(config.Room)
	if !ok {
		chat.Stop()
		return nil, fmt.Errorf("%s cannot read room %s", profile.Username, config.Room)
	}

	g := &Gateway{
		chat:    chat,
		config:  config,
		me:      profile.Username,
		title:   room.Title,
		changed: make(chan struct{}),
	}
	chat.SetGapHandler(g.tear)
	chat.SetHandlers(g.wake, func(string) {})
	return g, nil
}

// Stop releases the session.
func (g *Gateway) Stop() { g.chat.Stop() }

// Handler serves the agent card and the JSON-RPC binding.
func (g *Gateway) Handler() http.Handler {
	rpc := a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(g))
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(g.Card()))
	mux.Handle(RPCPath, g.authorize(rpc))
	return mux
}

// Card describes the worker as one agent with one skill.
func (g *Gateway) Card() *a2a.AgentCard {
	const scheme = a2a.SecuritySchemeName("bearer")
	return &a2a.AgentCard{
		Name: fmt.Sprintf("minos worker %s in %s", g.config.Worker, g.title),
		Description: "A coding agent in a sealed container, reached through its minos room. " +
			"Messages are posted to the room, where a developer reads them; " +
			"the worker's next message in the room is the result.",
		Version: "1",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(strings.TrimSuffix(g.config.Base, "/")+RPCPath, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
		SecuritySchemes: a2a.NamedSecuritySchemes{
			scheme: a2a.HTTPAuthSecurityScheme{Scheme: "Bearer"},
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{{scheme: {}}},
		Skills: []a2a.AgentSkill{{
			ID:          "converse",
			Name:        "Converse with the worker",
			Description: "Say something to the worker and receive its next reply.",
			Tags:        []string{"minos", "coding"},
		}},
	}
}

// authorize refuses a request without the bearer token. The comparison takes
// the same time however much of the token matches.
func (g *Gateway) authorize(next http.Handler) http.Handler {
	want := []byte("Bearer " + g.config.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="minos"`)
			http.Error(w, "a bearer token is required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Execute posts the message to the room and waits for the worker's reply.
func (g *Gateway) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		text, err := textOf(execCtx.Message)
		if err != nil {
			yield(nil, err)
			return
		}
		if execCtx.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}

		asked, err := g.post(ctx, text)
		if err != nil {
			yield(failed(execCtx, err), nil)
			return
		}
		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
			return
		}

		reply, err := g.reply(ctx, asked)
		if err != nil {
			yield(failed(execCtx, err), nil)
			return
		}
		if !yield(a2a.NewArtifactEvent(execCtx, a2a.NewTextPart(reply.Body)), nil) {
			return
		}
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
	}
}

// Cancel asks the worker to stop its turn, and cancels the task.
func (g *Gateway) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if err := g.chat.Send(g.config.Room, "/interrupt"); err != nil {
			yield(nil, fmt.Errorf("cannot interrupt the worker: %w", err))
			return
		}
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// textOf joins a message's text parts. Anything else is refused: the room
// carries text, and files move on the work mount.
func textOf(message *a2a.Message) (string, error) {
	if message == nil {
		return "", fmt.Errorf("%w: a message is required", a2a.ErrInvalidParams)
	}
	var texts []string
	for _, part := range message.Parts {
		if _, ok := part.Content.(a2a.Text); !ok {
			return "", fmt.Errorf("%w: this agent takes text parts only", a2a.ErrUnsupportedContentType)
		}
		texts = append(texts, part.Text())
	}
	text := strings.TrimSpace(strings.Join(texts, "\n"))
	if text == "" {
		return "", fmt.Errorf("%w: the message has no text", a2a.ErrInvalidParams)
	}
	return text, nil
}

// post sends text to the room and returns its sequence number.
func (g *Gateway) post(ctx context.Context, text string) (int64, error) {
	g.sending.Lock()
	defer g.sending.Unlock()

	mark := g.last()
	if err := g.chat.Send(g.config.Room, text); err != nil {
		return 0, err
	}
	// The reply to a send carries no sequence number; the push does.
	found, err := g.await(ctx, 10*time.Second, func(m client.Message) bool {
		return m.Seq > mark && m.Author == g.me && m.Body == text
	})
	if err != nil {
		return 0, fmt.Errorf("the room did not echo the message: %w", err)
	}
	return found.Seq, nil
}

// reply is the worker's first text message after seq.
func (g *Gateway) reply(ctx context.Context, seq int64) (client.Message, error) {
	return g.await(ctx, g.config.Timeout, func(m client.Message) bool {
		return m.Seq > seq && m.Author == g.config.Worker && m.Kind == "text"
	})
}

// await blocks until the room holds a message that matches, the context ends,
// the timeout passes, or a gap makes the answer unknowable.
func (g *Gateway) await(ctx context.Context, timeout time.Duration, match func(client.Message) bool) (client.Message, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		g.mutex.Lock()
		changed, missing := g.changed, g.missing
		g.mutex.Unlock()
		if missing > 0 {
			return client.Message{}, fmt.Errorf("the gateway missed %d messages in the room, and the reply may be among them", missing)
		}
		for _, message := range g.chat.Log(g.config.Room) {
			if message.Missing == 0 && match(message) {
				return message, nil
			}
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return client.Message{}, ctx.Err()
		case <-timer.C:
			return client.Message{}, fmt.Errorf("no reply within %s", timeout)
		}
	}
}

func (g *Gateway) last() int64 {
	log := g.chat.Log(g.config.Room)
	if len(log) == 0 {
		return 0
	}
	return log[len(log)-1].Seq
}

func (g *Gateway) wake() {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *Gateway) tear(room string, missing int64) {
	if room != g.config.Room {
		return
	}
	g.mutex.Lock()
	g.missing += missing
	g.mutex.Unlock()
	g.wake()
}

func failed(execCtx *a2asrv.ExecutorContext, err error) a2a.Event {
	message := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(err.Error()))
	return a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, message)
}
