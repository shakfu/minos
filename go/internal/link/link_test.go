package link

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, handle Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.sock")
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

func TestAnEarlierSocketAtThePathIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.sock")
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
