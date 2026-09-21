// Package broker is the host-side half of a container run.
//
// One broker serves one run. It holds the credential, the conversation and the
// run's socket; the container holds a shim and nothing else. It answers six
// operations and implements no seventh, so a container cannot reach a file
// write or a settings replacement at any credential: the denial is the absence
// of the code rather than a check that is scheduled.
//
// See docs/dev/recommended-architecture.md and docs/dev/implementation-plan.md.
//
// # Two spaces, for now
//
// A run holds a room and, optionally, a channel. The room carries the
// conversation. The channel carries submissions, because a submission is a
// channel operation on this wire and a room has no queue (wire-contract.md
// section 9). Both ids come from the dispatcher; the broker founds neither.
//
// # Delivery
//
// The broker owns the cursor. It drains the client's log, which applies the
// sequence contract of wire-contract.md section 8 and backfills through
// `history` when a push is dropped. A backfill is capped on the tail, so a
// broker far enough behind cannot recover what it missed. That is a tear: the
// broker reports it, refuses to advance over it, and every later operation
// carries it. A worker that cannot read what it missed must stop, not guess.
package broker

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"minos/internal/client"
	"minos/internal/link"
	"minos/internal/messaging"
)

// How long await blocks when its caller names no timeout.
const defaultAwait = 5 * time.Minute

// What marks a relayed message the wire would not take whole.
const relayCut = "\n\n[cut: the rest is in the run record]"

// The fence a payload travels in. The wire carries a body and nothing else, so
// a payload rides in the message under a tag `pma` can find. The room stays
// readable: a person sees the text and a folded block.
const payloadFence = "minos-payload"

// Config is one run, as the dispatcher describes it.
type Config struct {
	Server   string
	User     string
	Password string

	// Room carries the conversation; Channel carries submissions and may be
	// empty, which refuses `submit` and `await` rather than failing the run.
	Room    string
	Channel string

	// Window is what the grant lets this run read. Phase 0 has no grants, so it
	// is reported by `status` and enforced by nothing. Phase 1 item 5 is what
	// makes it a rule.
	Window string
}

// Broker is one run's conversation, its cursor and its socket.
type Broker struct {
	session *client.HTTP
	chat    *client.Client
	config  Config
	me      string

	// changed is signalled when anything arrives, so the relay wakes without
	// polling. Buffered by one and never blocking: it is signalled from the
	// goroutine that drains the connection.
	changed chan struct{}

	mutex sync.Mutex
	// delivered is the highest sequence handed over the socket, and pushed the
	// highest written to the agent's stdin. Two markers, because both lanes
	// carry every message: the socket is what the agent asks for, and stdin is
	// what reaches it whether it asks or not.
	delivered int64
	pushed    int64
	// torn is a gap wider than the server can backfill, and is terminal.
	torn    bool
	missing int64
	// Submissions this broker made, by id, with the last state seen.
	mine map[string]client.Submission
	// Callers blocked in await, by submission id.
	waiting map[string][]chan client.Submission

	stopped chan struct{}
	once    sync.Once
}

// Open logs in, syncs, and checks that the run's spaces are reachable. A run
// whose room it cannot read is refused here rather than at the first message.
func Open(config Config) (*Broker, error) {
	if config.Room == "" {
		return nil, fmt.Errorf("a run needs a room")
	}
	if config.Window == "" {
		config.Window = "all"
	}

	session, chat, profile, err := client.Connect(config.Server, config.User, config.Password)
	if err != nil {
		return nil, err
	}

	b := &Broker{
		session: session,
		chat:    chat,
		config:  config,
		me:      profile.Username,
		mine:    map[string]client.Submission{},
		waiting: map[string][]chan client.Submission{},
		changed: make(chan struct{}, 1),
		stopped: make(chan struct{}),
	}

	if _, ok := chat.Space(config.Room); !ok {
		chat.Stop()
		return nil, fmt.Errorf("%s cannot read room %s", profile.Username, config.Room)
	}
	if config.Channel != "" {
		// A room arrives by invitation, so a broker that cannot see one is
		// misconfigured. A channel is subscribed to, which the broker may do
		// for itself rather than making the dispatcher do it first.
		if _, ok := chat.Space(config.Channel); !ok {
			if _, err := chat.Subscribe(config.Channel); err != nil {
				chat.Stop()
				return nil, fmt.Errorf("%s cannot read channel %s: %w", profile.Username, config.Channel, err)
			}
		}
	}

	// Both handlers run on the goroutine that applies pushes. Neither blocks,
	// so a container that never calls `messages` cannot stop the connection
	// being drained, and a blocked model call cannot stop a decision arriving.
	chat.SetSubmissionHandler(b.decided)
	chat.SetGapHandler(b.tear)
	chat.SetHandlers(b.wake, func(string) {})
	return b, nil
}

// Stop releases the session. The container's socket is closed by its owner.
func (b *Broker) Stop() {
	b.once.Do(func() {
		close(b.stopped)
		b.chat.Stop()
		_ = b.session.Logout()
	})
}

// Chat exposes the client for the parts of a run that are not the container's:
// the dispatcher's own reading, and the relay of a turn into the room.
func (b *Broker) Chat() *client.Client { return b.chat }

// Torn reports delivery that gave up, which ends the run.
func (b *Broker) Torn() (bool, int64) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.torn, b.missing
}

// wake tells the relay something arrived. A send that would block is dropped:
// the signal says there is work, not how much, and the relay reads the log.
func (b *Broker) wake() {
	select {
	case b.changed <- struct{}{}:
	default:
	}
}

// forPush is what the agent has not been told over its stdin. Text from
// others only: a room event is the server's bookkeeping, and the worker's own
// messages are not news to it.
func (b *Broker) forPush() []string {
	b.mutex.Lock()
	since, torn := b.pushed, b.torn
	b.mutex.Unlock()
	if torn {
		return nil
	}

	var texts []string
	last := since
	for _, message := range b.chat.Log(b.config.Room) {
		if message.Seq <= since {
			continue
		}
		last = message.Seq
		if message.Author == b.me || message.Kind != "text" {
			continue
		}
		texts = append(texts, fmt.Sprintf("%s: %s", message.Author, message.Body))
	}

	b.mutex.Lock()
	if last > b.pushed {
		b.pushed = last
	}
	b.mutex.Unlock()
	return texts
}

// relay posts a turn's final message to the room under the worker's name. Cut
// to what the wire takes rather than refused: the room is the record of the
// run, and a verbose agent must not be able to leave it empty.
func (b *Broker) relay(text string) error {
	if len(text) > relayLimit {
		text = text[:relayLimit-len(relayCut)] + relayCut
	}
	return b.chat.Send(b.config.Room, text)
}

func (b *Broker) tear(room string, missing int64) {
	if room != b.config.Room {
		return
	}
	b.mutex.Lock()
	b.torn = true
	b.missing += missing
	b.mutex.Unlock()
}

func (b *Broker) decided(submission client.Submission) {
	// The author alone is not enough to tell one run from another: in phase 0
	// every worker shares an account, so a sibling run's submission arrives here
	// too. The channel is what makes it this run's.
	if submission.Author != b.me || submission.Channel != b.config.Channel {
		return
	}
	b.mutex.Lock()
	if _, ours := b.mine[submission.ID]; !ours && submission.State == "" {
		b.mutex.Unlock()
		return
	}
	b.mine[submission.ID] = submission
	waiting := b.waiting[submission.ID]
	if submission.State != link.StatePending {
		delete(b.waiting, submission.ID)
	}
	b.mutex.Unlock()

	if submission.State == link.StatePending {
		return
	}
	for _, waiter := range waiting {
		// Buffered by one and read once, so this never blocks the push loop.
		select {
		case waiter <- submission:
		default:
		}
	}
}

// Handle answers one operation from the container. An unknown op is refused
// rather than routed: the six are the authority.
func (b *Broker) Handle(request link.Request) link.Reply {
	switch request.Op {
	case link.OpMessages:
		return b.messages(request)
	case link.OpSay:
		return b.say(request)
	case link.OpSubmit:
		return b.submit(request)
	case link.OpAwait:
		return b.await(request)
	case link.OpProgress:
		return b.progress(request)
	case link.OpStatus:
		return b.status()
	default:
		return link.Refuse("",
			"There is no operation %q. There are six: messages, say, submit, await, progress, status.",
			request.Op)
	}
}

// messages drains everything the room has said since the caller's cursor. The
// worker's own messages are not delivered back to it.
func (b *Broker) messages(request link.Request) link.Reply {
	b.mutex.Lock()
	since := b.delivered
	if request.Since > 0 {
		since = request.Since
	}
	torn, missing := b.torn, b.missing
	b.mutex.Unlock()

	log := b.chat.Log(b.config.Room)

	// A tear is reported before anything else and instead of everything else.
	// Delivering the tail would hand the worker instructions that answer a
	// message it never saw.
	if torn {
		return link.Reply{
			Code: link.CodeGap,
			Records: []link.Record{{
				Kind:    link.KindGap,
				Missing: missing,
				Body: fmt.Sprintf(
					"%d message(s) were lost: the server's history is capped and this run fell behind it.",
					missing),
			}},
			Last: since,
		}
	}

	records := make([]link.Record, 0, len(log))
	last := since
	for _, message := range log {
		if message.Seq <= since {
			continue
		}
		// A jump in the log is a gap the client could not repair either.
		if message.Seq > last+1 {
			b.tear(b.config.Room, message.Seq-last-1)
			return b.messages(request)
		}
		last = message.Seq
		if message.Author == b.me {
			continue
		}
		kind := link.KindMessage
		if message.Kind == "event" {
			kind = link.KindEvent
		}
		records = append(records, link.Record{
			Kind:   kind,
			Seq:    message.Seq,
			Author: message.Author,
			Body:   message.Body,
			At:     message.At,
		})
	}

	b.mutex.Lock()
	if last > b.delivered {
		b.delivered = last
	}
	b.mutex.Unlock()
	return link.Reply{Code: link.CodeOK, Records: records, Last: last}
}

// say posts to the room, with an optional payload beside the text.
func (b *Broker) say(request link.Request) link.Reply {
	body, refusal := b.compose(request.Body, request.Payload)
	if refusal != nil {
		return *refusal
	}
	if err := b.chat.Send(b.config.Room, body); err != nil {
		return link.Refuse("", "%s", sentence(err))
	}
	return link.Reply{Code: link.CodeOK}
}

// submit proposes something that needs a decision, and returns its id.
func (b *Broker) submit(request link.Request) link.Reply {
	if b.config.Channel == "" {
		return link.Refuse(
			"A run may submit when its dispatcher gives it a decision channel.",
			"This run has no decision channel, so nothing can be submitted. Say it in the room instead.")
	}
	body, refusal := b.compose(request.Body, request.Payload)
	if refusal != nil {
		return *refusal
	}
	submission, err := b.chat.Submit(b.config.Channel, request.Subject, body)
	if err != nil {
		return link.Refuse("", "%s", sentence(err))
	}
	b.mutex.Lock()
	b.mine[submission.ID] = submission
	b.mutex.Unlock()
	return link.Reply{Code: link.CodeOK, ID: submission.ID, State: link.StatePending}
}

// await blocks until the submission is decided or the wait runs out. A wait
// that runs out is not a decision: the submission is still pending.
func (b *Broker) await(request link.Request) link.Reply {
	if request.ID == "" {
		return link.Refuse("", "await names the submission to wait on.")
	}

	b.mutex.Lock()
	known, ours := b.mine[request.ID]
	if !ours {
		b.mutex.Unlock()
		return link.Refuse("", "This run did not submit %s, so it cannot wait on it.", request.ID)
	}
	if known.State != link.StatePending && known.State != "" {
		b.mutex.Unlock()
		return decision(known)
	}
	waiter := make(chan client.Submission, 1)
	b.waiting[request.ID] = append(b.waiting[request.ID], waiter)
	b.mutex.Unlock()

	wait := defaultAwait
	if request.Timeout > 0 {
		wait = time.Duration(request.Timeout * float64(time.Second))
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case submission := <-waiter:
		return decision(submission)
	case <-timer.C:
		return link.Reply{Code: link.CodeOK, ID: request.ID, State: link.StateTimeout}
	case <-b.stopped:
		return link.Refuse("", "The run is ending, so %s will not be decided here.", request.ID)
	}
}

// progress advances the durable read marker. It never moves past what was
// delivered, and a torn run cannot move it at all.
func (b *Broker) progress(request link.Request) link.Reply {
	b.mutex.Lock()
	delivered, torn := b.delivered, b.torn
	b.mutex.Unlock()

	if torn {
		return link.Refuse("",
			"Delivery is torn, so the read marker cannot move. This run has to stop.")
	}
	if request.Seq <= 0 {
		return link.Refuse("", "progress names the sequence number read through.")
	}
	if request.Seq > delivered {
		return link.Refuse("",
			"Message %d was never delivered to this run; delivery reached %d.", request.Seq, delivered)
	}
	if err := b.chat.MarkRead(b.config.Room, request.Seq); err != nil {
		return link.Refuse("", "%s", sentence(err))
	}
	return link.Reply{Code: link.CodeOK, Seq: request.Seq}
}

func (b *Broker) status() link.Reply {
	b.mutex.Lock()
	delivered, torn := b.delivered, b.torn
	b.mutex.Unlock()
	return link.Reply{Code: link.CodeOK, Status: &link.Status{
		Room:      b.config.Room,
		Channel:   b.config.Channel,
		Window:    b.config.Window,
		Connected: b.chat.Connected(),
		Delivered: delivered,
		Torn:      torn,
	}}
}

// compose puts the text and its payload into one body, refusing what the server
// would refuse anyway and what it cannot check.
func (b *Broker) compose(body string, payload json.RawMessage) (string, *link.Reply) {
	body = strings.TrimSpace(body)
	if body == "" && len(payload) == 0 {
		refusal := link.Refuse("", "Nothing was said: a message needs a body, a payload, or both.")
		return "", &refusal
	}
	if len(payload) > 0 {
		var compact json.RawMessage
		if err := json.Unmarshal(payload, &compact); err != nil {
			refusal := link.Refuse("",
				"That payload is not JSON: %v. Write it to a file and pass the file.", err)
			return "", &refusal
		}
		block := "```" + payloadFence + "\n" + string(compact) + "\n```"
		if body == "" {
			body = block
		} else {
			body = body + "\n\n" + block
		}
	}
	if len(body) > messaging.BodyLimit {
		refusal := link.Refuse("",
			"That message is %d bytes and the limit is %d. Write it to the work mount and say where it is.",
			len(body), messaging.BodyLimit)
		return "", &refusal
	}
	return body, nil
}

func decision(submission client.Submission) link.Reply {
	reply := link.Reply{Code: link.CodeOK, ID: submission.ID, State: submission.State}
	if submission.Comment != nil {
		reply.Comment = *submission.Comment
	}
	return reply
}

// sentence turns a refusal into what a model reads. Every refusal is a readable
// string, so a worker changes course without an error table.
func sentence(err error) string {
	text := err.Error()
	if text == "" {
		return "That could not be done."
	}
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "?") {
		text += "."
	}
	return text
}
