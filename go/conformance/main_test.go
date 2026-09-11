package conformance

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// The demo accounts the contract names. demo is the only administrator.
const admin = "demo"

var passwords = map[string]string{"demo": "demo", "alice": "alice", "bob": "bob"}

// The server every test shares, and the argv a fresh one is launched with.
var (
	shared *Server
	argv   []string
)

// One server for the whole run, because a restart costs more than the suite.
// So no test may assume it is alone: address rooms by the id returned, suffix
// names that must be unique, and count nothing server-wide.
func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	if base := ExternalURL(); base != "" {
		shared = External(base)
		if err := AwaitReady(shared); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return m.Run()
	}

	scratch, err := os.MkdirTemp("", "minos-conformance-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(scratch)

	argv = Command()
	if argv == nil {
		binary := filepath.Join(scratch, "minosd")
		build := exec.Command("go", "build", "-o", binary, "minos/cmd/minosd")
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "cannot build minosd:", err)
			return 1
		}
		argv = []string{binary}
	}

	shared, err = Launch(filepath.Join(scratch, "shared"), argv, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer shared.Stop()
	return m.Run()
}

// -- fixtures -----------------------------------------------------------------

func anonymous(t *testing.T) *Http { return NewHttp(t, shared.Base) }

func session(t *testing.T, username string) *Http { return login(t, shared, username) }

func login(t *testing.T, server *Server, username string) *Http {
	t.Helper()
	h := NewHttp(t, server.Base)
	if response := h.Login(username, passwords[username]); response.Status != 200 {
		t.Fatalf("login as %s: %d %s", username, response.Status, response.Text())
	}
	return h
}

// dial opens a socket that is closed when the test ends. Not yet handshaken.
func dial(t *testing.T, base, cookie string) *Socket {
	t.Helper()
	socket := Dial(t, base, cookie)
	t.Cleanup(socket.Close)
	return socket
}

// attach is a logged-in socket against any server, handshaken and synced.
func attach(t *testing.T, server *Server, username string) *Socket {
	t.Helper()
	h := login(t, server, username)
	socket := dial(t, server.Base, h.CookieHeader())
	socket.Handshake()
	socket.Call("sync")
	return socket
}

func connect(t *testing.T, username string) *Socket {
	t.Helper()
	return attach(t, shared, username)
}

// freshServer is a server of the test's own, for start-up or a moved clock.
func freshServer(t *testing.T, settings map[string]string) *Server {
	t.Helper()
	if ExternalURL() != "" {
		t.Skip("A server was supplied; this test must launch its own")
	}
	server, err := Launch(t.TempDir(), argv, settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	return server
}

// unique is a name no other test will have used.
func unique(prefix string) string {
	raw := make([]byte, 5)
	_, _ = rand.Read(raw)
	return prefix + "-" + hex.EncodeToString(raw)
}

func gone(room any) Pred {
	return func(event Obj) bool { return event["type"] == "roomGone" && event["room"] == room }
}

func group(id any) Obj { return Obj{"kind": "group", "id": id} }

// -- assertions ---------------------------------------------------------------

// same fails unless got and want encode to the same JSON, so numbers, slices
// and maps compare by what the wire would carry.
func same(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(normal(t, got), normal(t, want)) {
		t.Fatalf("got %s, want %s", encode(got), encode(want))
	}
}

func normal(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("cannot encode %v: %v", value, err)
	}
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	return decoded
}

func encode(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func truth(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

// null fails unless key is present and null.
func null(t *testing.T, o Obj, key string) {
	t.Helper()
	if value, present := o[key]; !present || value != nil {
		t.Fatalf("%s is %s, want null (present: %v)", key, encode(value), present)
	}
}

// keySet fails unless o has exactly these keys.
func keySet(t *testing.T, o Obj, want ...string) {
	t.Helper()
	got := make([]string, 0, len(o))
	for key := range o {
		got = append(got, key)
	}
	slices.Sort(got)
	slices.Sort(want)
	same(t, got, want)
}

// -- reading replies ----------------------------------------------------------

func obj(value any) Obj { o, _ := value.(Obj); return o }

func list(value any) []any { l, _ := value.([]any); return l }

func str(value any) string { s, _ := value.(string); return s }

func num(value any) float64 { n, _ := value.(float64); return n }

func strs(value any) []string {
	result := []string{}
	for _, item := range list(value) {
		result = append(result, str(item))
	}
	return result
}

func sorted(value any) []string {
	result := strs(value)
	slices.Sort(result)
	return result
}

func has(value any, want string) bool { return slices.Contains(strs(value), want) }

// pluck is one field of every object in a list.
func pluck(value any, key string) []any {
	result := []any{}
	for _, item := range list(value) {
		result = append(result, obj(item)[key])
	}
	return result
}

// byID keys a list of objects by their id.
func byID(value any) map[string]Obj {
	result := map[string]Obj{}
	for _, item := range list(value) {
		result[str(obj(item)["id"])] = obj(item)
	}
	return result
}
