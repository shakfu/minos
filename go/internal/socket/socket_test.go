package socket

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// pair is a live websocket, both ends. A Connection needs a real one because
// hanging up closes it, and the tests below hang up deliberately.
func pair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()

	accepted := make(chan *websocket.Conn, 1)
	refused := make(chan error, 1)
	release := make(chan struct{})

	// The handler holds until cleanup. A websocket outlives the request that
	// opened it, and the test needs the accepted end for longer than the handler.
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			refused <- err
			return
		}
		accepted <- ws
		<-release
	}))

	client, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(listener.URL, "http"), nil)
	if err != nil {
		close(release)
		listener.Close()
		t.Fatalf("cannot dial: %v", err)
	}
	t.Cleanup(func() {
		client.CloseNow()
		close(release)
		listener.Close()
	})

	select {
	case server = <-accepted:
		return server, client
	case err := <-refused:
		t.Fatalf("cannot accept: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no websocket was accepted")
	}
	return nil, nil
}

// stalled is a connection with no writer goroutine, which is indistinguishable
// from a peer that has stopped reading: nothing ever leaves the queue.
func stalled(t *testing.T) *Connection {
	t.Helper()
	server, _ := pair(t)
	return newConnection(t.Context(), server, Profile{ID: "1", Username: "alice"})
}

// drained is a connection whose frames reach the client end.
func drained(t *testing.T, username string) (*Connection, *websocket.Conn) {
	t.Helper()
	server, client := pair(t)
	connection := newConnection(t.Context(), server, Profile{ID: username, Username: username})
	go connection.writer()
	return connection, client
}

// next is one frame off the client end, decoded far enough to identify it.
func next(t *testing.T, client *websocket.Conn) frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind, raw, err := client.Read(ctx)
	if err != nil {
		t.Fatalf("cannot read a frame: %v", err)
	}
	if kind != websocket.MessageText {
		t.Fatalf("frame is %v, want text", kind)
	}
	var decoded frame
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("cannot decode %s: %v", raw, err)
	}
	return decoded
}

// fill queues outboundDepth frames, which is as many as the connection holds.
func fill(t *testing.T, connection *Connection) {
	t.Helper()
	for i := range outboundDepth {
		if err := connection.SendFrame(Encode("osjs/test", []any{i})); err != nil {
			t.Fatalf("frame %d of %d was refused with room left: %v", i+1, outboundDepth, err)
		}
	}
}

// sendWithin is one send that is not allowed to wait. A send over a full queue
// is where blocking would appear, and it would otherwise appear as a whole
// package timing out ten minutes later rather than as this line.
func sendWithin(t *testing.T, connection *Connection, wait time.Duration) error {
	t.Helper()
	failed := make(chan error, 1)
	go func() { failed <- connection.SendFrame(Encode("osjs/test", nil)) }()

	select {
	case err := <-failed:
		return err
	case <-time.After(wait):
		t.Fatalf("a send blocked for %s on a peer that is not reading", wait)
	}
	return nil
}

func TestTheQueueAbsorbsItsDepth(t *testing.T) {
	fill(t, stalled(t))
}

func TestAPeerThatFallsBehindIsHungUpOn(t *testing.T) {
	connection := stalled(t)
	fill(t, connection)

	// One frame past the depth. Waiting for this peer would make its problem
	// every other recipient's, so it is dropped instead.
	if err := sendWithin(t, connection, 5*time.Second); !errors.Is(err, ErrGone) {
		t.Fatalf("a saturated peer was tolerated: %v", err)
	}
	select {
	case <-connection.done:
	default:
		t.Fatal("the connection was refused a frame but left open")
	}
}

func TestSendToAClosedConnectionIsRefused(t *testing.T) {
	connection := stalled(t)
	connection.shutdown()
	// Twice: Serve closes the connection on its way out whether or not a
	// saturated send closed it first.
	connection.shutdown()

	if err := connection.SendFrame(Encode("osjs/test", nil)); !errors.Is(err, ErrGone) {
		t.Fatalf("a closed connection accepted a frame: %v", err)
	}
	if queued := len(connection.outbound); queued != 0 {
		t.Fatalf("a closed connection queued %d frames", queued)
	}
}

func TestAFanOutSkipsASaturatedPeerAndReachesTheRest(t *testing.T) {
	// The fault this exists for: a synchronous fan-out let one unresponsive peer
	// stall every other recipient for a full write timeout.
	registry := NewRegistry()

	saturated := stalled(t)
	fill(t, saturated)
	reading, client := drained(t, "bob")

	registry.add(saturated)
	registry.add(reading)

	if reached := registry.Broadcast("osjs/test", []any{"one"}, nil); reached != 1 {
		t.Fatalf("the fan-out reached %d connections, want 1", reached)
	}
	if got := next(t, client); got.Name != "osjs/test" {
		t.Fatalf("the reading peer got %q", got.Name)
	}
	select {
	case <-saturated.done:
	default:
		t.Fatal("the fan-out left the saturated peer in the registry's way")
	}
}

func TestQueuedFramesArriveInTheOrderTheyWereSent(t *testing.T) {
	// One writer goroutine per connection is what serialises the queue: two
	// senders cannot interleave the halves of a message.
	connection, client := drained(t, "alice")

	const count = 16
	for i := range count {
		if err := connection.Send("osjs/test", []any{i}); err != nil {
			t.Fatalf("cannot send frame %d: %v", i, err)
		}
	}
	for i := range count {
		got := next(t, client)
		if len(got.Params) != 1 {
			t.Fatalf("frame %d carries %d params, want 1", i, len(got.Params))
		}
		var position int
		if err := json.Unmarshal(got.Params[0], &position); err != nil {
			t.Fatalf("cannot decode frame %d: %v", i, err)
		}
		if position != i {
			t.Fatalf("frame %d arrived in position %d", i, position)
		}
	}
}
