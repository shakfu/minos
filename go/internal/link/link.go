// Package link is the socket protocol between the shim and the broker.
//
// One connection carries one request and one reply, both a single line of JSON,
// and then closes. There is no multiplexing and no session: the shim is a
// short-lived process that runs one operation and exits.
//
// It imports nothing but the standard library, and neither the wire to minosd
// nor the client that speaks it. The shim links this package alone, so a shim
// replaced by a hostile program still reaches six operations and no authority.
package link

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// The six operations, and no seventh. A request naming anything else is refused
// by the broker rather than routed.
const (
	OpMessages = "messages"
	OpSay      = "say"
	OpSubmit   = "submit"
	OpAwait    = "await"
	OpProgress = "progress"
	OpStatus   = "status"
)

// Exit codes. The shim exits with the broker's status, so these are what the
// agent's harness sees.
const (
	// CodeOK is a request the broker answered.
	CodeOK = 0
	// CodeRefused is a request the broker understood and would not perform. The
	// reason is a sentence on stderr.
	CodeRefused = 1
	// CodeLocal is the shim's own failure: no socket, a broken connection, an
	// unreadable payload file, a subcommand that does not exist.
	CodeLocal = 2
	// CodeGap is delivery that cannot be repaired. The records name what was
	// missed; the run must stop rather than act on what is left.
	CodeGap = 3
)

// MaxRequest is the largest request the broker reads, matching the server's own
// limit on a JSON body. A payload larger than this belongs on the bind mount.
const MaxRequest = 1 << 20

// Request is one operation. Fields not named by the operation are ignored.
type Request struct {
	Op string `json:"op"`

	// messages: deliver everything after this sequence number, in place of the
	// broker's own marker. Absent is zero, which the broker reads as its marker.
	Since int64 `json:"since,omitempty"`

	// say and submit: the text, and an optional JSON payload beside it.
	Body    string          `json:"body,omitempty"`
	Subject string          `json:"subject,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`

	// await: which submission, and how long to block for.
	ID      string  `json:"id,omitempty"`
	Timeout float64 `json:"timeout,omitempty"`

	// progress: the sequence number the worker has read through.
	Seq int64 `json:"seq,omitempty"`
}

// Reply is one answer. Code is what the shim exits with.
type Reply struct {
	Code int `json:"code"`

	// Error is a sentence, always, and never an error table: a model changes
	// course on a sentence. Rule names the rule that would permit the request,
	// so a worker can propose a policy line rather than guess. It is empty
	// where no grant could permit it: a malformed request, the run's own
	// state, a size limit, or the server's refusal relayed.
	Error string `json:"error,omitempty"`
	Rule  string `json:"rule,omitempty"`

	// messages.
	Records []Record `json:"records,omitempty"`
	Last    int64    `json:"last,omitempty"`

	// say and progress.
	Seq int64 `json:"seq,omitempty"`

	// submit and await.
	ID      string `json:"id,omitempty"`
	State   string `json:"state,omitempty"`
	Comment string `json:"comment,omitempty"`

	// status.
	Status *Status `json:"status,omitempty"`
}

// Record is one item of delivery. Kind is `message` for what was said, `event`
// for what the server reported, and `gap` for what cannot be recovered.
type Record struct {
	Kind   string  `json:"kind"`
	Seq    int64   `json:"seq"`
	Author string  `json:"author,omitempty"`
	Body   string  `json:"body,omitempty"`
	At     float64 `json:"at,omitempty"`

	// Missing counts the messages a gap swallowed.
	Missing int64 `json:"missing,omitempty"`
}

// Kinds a record carries.
const (
	KindMessage = "message"
	KindEvent   = "event"
	KindGap     = "gap"
)

// States a submission ends in. Timeout is the shim's wait ending, not a
// decision: the submission is still pending and may still be decided.
const (
	StateApproved = "approved"
	StateRejected = "rejected"
	StatePending  = "pending"
	StateTimeout  = "timeout"
)

// Status is what the grant permits and whether the broker is holding it.
type Status struct {
	Room    string `json:"room"`
	Channel string `json:"channel,omitempty"`
	Window  string `json:"window"`
	// Expiry is the grant's. Unset until grants exist (implementation-plan 5).
	Expiry    string `json:"expiry,omitempty"`
	Connected bool   `json:"connected"`
	Delivered int64  `json:"delivered"`
	// Earlier is how many messages the room held before this run could read
	// it: archived, or past the server's history limit when the run attached.
	Earlier int64 `json:"earlier,omitempty"`

	// Torn is delivery that gave up: a gap wider than the server can backfill.
	Torn bool `json:"torn"`
}

// Refuse is a reply the broker understood and would not perform.
func Refuse(rule, format string, args ...any) Reply {
	return Reply{Code: CodeRefused, Error: fmt.Sprintf(format, args...), Rule: rule}
}

// Line is a body as it may appear on one line of text an agent reads. A body
// that could break the line, by a newline or any other non-printable rune, is
// quoted; otherwise it is left as is, so ordinary messages read unchanged.
func Line(body string) string {
	if strings.ContainsFunc(body, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return strconv.Quote(body)
	}
	return body
}

// Handler answers one request. It must not block on anything but its own work:
// the broker's connection to the server is drained by a different goroutine.
type Handler func(Request) Reply

// maxPath is the longest path a unix socket binds at: sun_path less its NUL,
// 103 bytes on macOS and 107 on Linux. Longer fails with a bare EINVAL.
var maxPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// CheckPath refuses a path Listen cannot use: one too long to bind, or one
// holding something other than a socket, which Listen would otherwise delete.
// A caller runs it first to fail before work the socket's failure would waste.
func CheckPath(path string) error {
	if len(path) > maxPath {
		return fmt.Errorf("the path is %d bytes and a unix socket's is at most %d; choose a shorter one", len(path), maxPath)
	}
	info, err := os.Lstat(path)
	if err == nil && info.Mode().Type() != os.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket, so it is not replaced", path)
	}
	return nil
}

// Listen creates the run's socket. The directory is created, an earlier socket
// at the same path is removed (anything else there is refused), and the socket
// is born with its mode: the socket is the credential in this design, so its
// mode is the gate, and a chmod after binding leaves a window.
func Listen(path string, mode os.FileMode) (net.Listener, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// The umask is process-wide, so this is for startup, before anything else
	// creates files.
	previous := syscall.Umask(0o777 &^ int(mode.Perm()))
	listener, err := net.Listen("unix", path)
	syscall.Umask(previous)
	if err != nil {
		return nil, err
	}
	return listener, nil
}

// How long a connection has to send its request, and how many may be open at
// once. Variables so a test can shorten them. The limit counts connections, not
// operations: an await holds its connection for as long as it waits.
var (
	readTimeout    = 5 * time.Second
	maxConnections = 64
)

// Serve answers connections until the listener is closed. Each runs on its own
// goroutine, because await blocks for as long as its caller asked.
func Serve(listener net.Listener, handle Handler) {
	slots := make(chan struct{}, maxConnections)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			// Refused rather than queued: a queued caller cannot tell a full
			// broker from a hung one.
			go refuse(conn, fmt.Sprintf(
				"The broker has %d connections open, which is its limit. Wait for an await to return, then try again.",
				maxConnections))
			continue
		}
		go func() {
			defer func() { <-slots }()
			answer(conn, handle)
		}()
	}
}

func answer(conn net.Conn, handle Handler) {
	defer conn.Close()

	var request Request
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	decoder := json.NewDecoder(io.LimitReader(conn, MaxRequest))
	if err := decoder.Decode(&request); err != nil {
		reason := "That request could not be read as JSON."
		if errors.Is(err, os.ErrDeadlineExceeded) {
			reason = fmt.Sprintf("No request arrived within %s.", readTimeout)
		}
		write(conn, Reply{Code: CodeRefused, Error: reason})
		return
	}
	write(conn, handle(request))
}

func refuse(conn net.Conn, reason string) {
	defer conn.Close()
	// Bounded, so a peer that never reads cannot hold this goroutine.
	_ = conn.SetWriteDeadline(time.Now().Add(readTimeout))
	write(conn, Reply{Code: CodeRefused, Error: reason})
}

func write(conn net.Conn, reply Reply) {
	raw, err := json.Marshal(reply)
	if err != nil {
		raw, _ = json.Marshal(Reply{Code: CodeLocal, Error: "The reply could not be encoded."})
	}
	_, _ = conn.Write(append(raw, '\n'))
}

// Do runs one operation against the socket at path. A reply that cannot be read
// is a local failure: the shim never guesses what the broker meant.
func Do(path string, request Request, timeout time.Duration) (Reply, error) {
	conn, err := net.DialTimeout("unix", path, dialTimeout)
	if err != nil {
		return Reply{}, fmt.Errorf("cannot reach the broker at %s: %w", path, err)
	}
	defer conn.Close()

	// await blocks for as long as its caller asked, so a deadline is per call
	// rather than a constant. Zero leaves the connection without one.
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	raw, err := json.Marshal(request)
	if err != nil {
		return Reply{}, err
	}
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		return Reply{}, fmt.Errorf("cannot send to the broker: %w", err)
	}

	reader := bufio.NewReader(io.LimitReader(conn, MaxRequest))
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Reply{}, fmt.Errorf("the broker answered nothing: %w", err)
	}
	var reply Reply
	if err := json.Unmarshal(line, &reply); err != nil {
		return Reply{}, fmt.Errorf("the broker's reply could not be read: %w", err)
	}
	return reply, nil
}

const dialTimeout = 5 * time.Second
