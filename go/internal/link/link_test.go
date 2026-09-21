package link

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minos/internal/testserver"
)

func serve(t *testing.T, handle Handler) string {
	t.Helper()
	path := testserver.SocketPath(t)
	listener, err := Listen(path, 0o600)
	if err != nil {
		t.Fatalf("cannot listen on %s: %v", path, err)
	}
	t.Cleanup(func() { listener.Close() })
	go Serve(listener, handle)
	return path
}

func TestOneConnectionCarriesOneRequestAndOneReply(t *testing.T) {
	path := serve(t, func(request Request) Reply {
		return Reply{Code: CodeOK, Seq: request.Seq}
	})

	for _, seq := range []int64{1, 2, 3} {
		reply, err := Do(path, Request{Op: OpProgress, Seq: seq}, time.Second)
		if err != nil {
			t.Fatalf("round trip %d: %v", seq, err)
		}
		if reply.Seq != seq {
			t.Fatalf("round trip %d answered %+v", seq, reply)
		}
	}
}

// The socket is the credential in this design, so its mode is the gate on it.
func TestTheSocketAdmitsItsOwnerAlone(t *testing.T) {
	path := serve(t, func(Request) Reply { return Reply{Code: CodeOK} })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cannot stat the socket: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the socket's mode is %o", mode)
	}
}

// A path bind would refuse is named as too long, before anything is created.
func TestAPathPastTheSocketLimitIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("d", maxPath))
	_, err := Listen(filepath.Join(dir, "run.sock"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("Listen returned %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the directory was created: %v", err)
	}
}

// The limit is bind's own: a path of exactly that length still binds.
func TestAPathAtTheSocketLimitBinds(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "minos-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := dir + "/" + strings.Repeat("s", maxPath-len(dir)-1)
	listener, err := Listen(path, 0o600)
	if err != nil {
		t.Fatalf("a %d-byte path was refused: %v", len(path), err)
	}
	listener.Close()
}

// Listen replaces an earlier socket, and nothing else: a mistyped -socket must
// not delete a file or an empty directory.
func TestSomethingThatIsNotASocketIsNotReplaced(t *testing.T) {
	dir := filepath.Dir(testserver.SocketPath(t))
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, empty} {
		if _, err := Listen(path, 0o600); err == nil || !strings.Contains(err.Error(), "not a socket") {
			t.Fatalf("Listen(%s) returned %v", path, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was removed: %v", path, err)
		}
	}
}

func TestAnEarlierSocketAtThePathIsReplaced(t *testing.T) {
	path := testserver.SocketPath(t)
	first, err := Listen(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	second, err := Listen(path, 0o600)
	if err != nil {
		t.Fatalf("a second listener was refused: %v", err)
	}
	second.Close()
}

func TestARequestThatIsNotJSONIsRefusedRatherThanGuessedAt(t *testing.T) {
	path := serve(t, func(Request) Reply {
		t.Error("the handler saw a request that could not be read")
		return Reply{}
	})

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("messages\n")); err != nil {
		t.Fatal(err)
	}

	var reply Reply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatalf("no reply: %v", err)
	}
	if reply.Code != CodeRefused || reply.Error == "" {
		t.Fatalf("a malformed request answered %+v", reply)
	}
}

// A payload larger than the server would accept is cut off here rather than
// read into memory and refused later.
func TestARequestOverTheLimitIsCutOff(t *testing.T) {
	path := serve(t, func(request Request) Reply {
		return Reply{Code: CodeOK, Seq: int64(len(request.Body))}
	})

	oversized := Request{Op: OpSay, Body: strings.Repeat("x", MaxRequest+1024)}
	reply, err := Do(path, oversized, 5*time.Second)
	if err != nil {
		// A refused read may close the connection first; either outcome keeps
		// the oversized body out of the handler.
		return
	}
	if reply.Code == CodeOK {
		t.Fatalf("an oversized request was answered: %+v", reply)
	}
}

func TestAnAbsentSocketIsALocalFailure(t *testing.T) {
	_, err := Do(filepath.Join(t.TempDir(), "absent.sock"), Request{Op: OpStatus}, time.Second)
	if err == nil {
		t.Fatal("dialling nothing succeeded")
	}
}
