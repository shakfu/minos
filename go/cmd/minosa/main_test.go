package main

// The shim against a fake broker: no container, no server, no model. What is
// tested is the one thing the shim decides, which is what it sends.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minos/internal/link"
)

// record captures the request the shim would have sent.
func record(reply link.Reply) (call, *link.Request) {
	var seen link.Request
	return func(request link.Request, _ time.Duration) (link.Reply, error) {
		seen = request
		return reply, nil
	}, &seen
}

func run(t *testing.T, args []string, stdin string, reply link.Reply) (int, string, string, link.Request) {
	t.Helper()
	do, seen := record(reply)
	var stdout, stderr bytes.Buffer
	args = append([]string{args[0], "-socket", "/run/minos.sock"}, args[1:]...)
	code := shim(args, strings.NewReader(stdin), &stdout, &stderr, do)
	return code, stdout.String(), stderr.String(), *seen
}

func TestEachOperationSendsWhatItWasAsked(t *testing.T) {
	for _, c := range []struct {
		name  string
		args  []string
		stdin string
		want  link.Request
	}{
		{"messages", []string{"messages"}, "", link.Request{Op: "messages"}},
		{"messages since", []string{"messages", "-since", "12"}, "", link.Request{Op: "messages", Since: 12}},
		{"say", []string{"say", "on", "it"}, "", link.Request{Op: "say", Body: "on it"}},
		{"say from stdin", []string{"say", "-stdin"}, "a long report", link.Request{Op: "say", Body: "a long report"}},
		{"submit", []string{"submit", "-subject", "install", "apt-get install rg"},
			"", link.Request{Op: "submit", Subject: "install", Body: "apt-get install rg"}},
		{"await", []string{"await", "abc", "-timeout", "30"}, "", link.Request{Op: "await", ID: "abc", Timeout: 30}},
		{"await joined", []string{"await", "abc", "--timeout=30"}, "", link.Request{Op: "await", ID: "abc", Timeout: 30}},
		{"progress", []string{"progress", "41"}, "", link.Request{Op: "progress", Seq: 41}},
		{"status", []string{"status"}, "", link.Request{Op: "status"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _, stderr, sent := run(t, c.args, c.stdin, link.Reply{Code: link.CodeOK})
			if code != link.CodeOK {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if sent.Op != c.want.Op || sent.Since != c.want.Since || sent.Body != c.want.Body ||
				sent.Subject != c.want.Subject || sent.ID != c.want.ID ||
				sent.Timeout != c.want.Timeout || sent.Seq != c.want.Seq {
				t.Fatalf("sent %+v, wanted %+v", sent, c.want)
			}
		})
	}
}

// The shim exits with the broker's status, which is how a harness tells a
// refusal from a tear from its own failure.
func TestTheShimExitsWithTheBrokersStatus(t *testing.T) {
	for _, code := range []int{link.CodeOK, link.CodeRefused, link.CodeGap} {
		got, _, _, _ := run(t, []string{"status"}, "", link.Reply{Code: code, Error: "because"})
		if got != code {
			t.Fatalf("the broker answered %d and the shim exited %d", code, got)
		}
	}
}

func TestARefusalIsASentenceOnStderrWithTheRuleThatWouldPermitIt(t *testing.T) {
	_, stdout, stderr, _ := run(t, []string{"submit", "-subject", "s", "body"}, "",
		link.Reply{Code: link.CodeRefused, Error: "This run has no decision channel.", Rule: "A run may submit when it has one."})
	if !strings.Contains(stderr, "no decision channel") || !strings.Contains(stderr, "Permitted by:") {
		t.Fatalf("the refusal reads %q", stderr)
	}
	if stdout != "" {
		t.Fatalf("a refusal wrote %q to stdout", stdout)
	}
}

func TestAPayloadIsReadFromAFileAndNeverFromAnArgument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, []byte(`{"verb":"install"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, sent := run(t, []string{"say", "-payload", path, "a request"}, "", link.Reply{Code: link.CodeOK})
	var decoded struct {
		Verb string `json:"verb"`
	}
	if err := json.Unmarshal(sent.Payload, &decoded); err != nil || decoded.Verb != "install" {
		t.Fatalf("the payload arrived as %s (%v)", sent.Payload, err)
	}
}

// The broker refuses a malformed payload too. Refusing it here as well is what
// puts the file's name in the sentence the model reads.
func TestAPayloadThatIsNotJSONIsRefusedBeforeItIsSent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, []byte("{verb: install"), 0o600); err != nil {
		t.Fatal(err)
	}
	do, seen := record(link.Reply{Code: link.CodeOK})
	var stdout, stderr bytes.Buffer
	code := shim([]string{"say", "-socket", "/run/minos.sock", "-payload", path, "text"},
		strings.NewReader(""), &stdout, &stderr, do)
	if code != link.CodeLocal || seen.Op != "" {
		t.Fatalf("exit %d after sending %+v", code, *seen)
	}
	if !strings.Contains(stderr.String(), "is not JSON") {
		t.Fatalf("stderr reads %q", stderr.String())
	}
}

func TestABodyComesFromOneSource(t *testing.T) {
	code, _, stderr, _ := run(t, []string{"say", "-stdin", "text"}, "other", link.Reply{Code: link.CodeOK})
	if code != link.CodeLocal || !strings.Contains(stderr, "not from more than one") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestDeliveryIsPrintedOneRecordPerLineAndAGapGoesToStderr(t *testing.T) {
	reply := link.Reply{Code: link.CodeGap, Records: []link.Record{
		{Kind: link.KindMessage, Seq: 4, Author: "demo", Body: "stop"},
		{Kind: link.KindGap, Missing: 12, Body: "12 message(s) were lost"},
	}}
	code, stdout, stderr, _ := run(t, []string{"messages"}, "", reply)
	if code != link.CodeGap {
		t.Fatalf("a gap exited %d", code)
	}
	if stdout != "[4] demo: stop\n" {
		t.Fatalf("stdout reads %q", stdout)
	}
	if !strings.Contains(stderr, "12 message(s) were lost") {
		t.Fatalf("stderr reads %q", stderr)
	}
}

func TestAnUnknownSubcommandIsUsageAndNotARequest(t *testing.T) {
	do, seen := record(link.Reply{Code: link.CodeOK})
	var stdout, stderr bytes.Buffer
	code := shim([]string{"writefile", "-socket", "/run/minos.sock"}, strings.NewReader(""), &stdout, &stderr, do)
	if code != link.CodeLocal || seen.Op != "" {
		t.Fatalf("exit %d after sending %+v", code, *seen)
	}
	if !strings.Contains(stderr.String(), "Six operations") {
		t.Fatalf("stderr reads %q", stderr.String())
	}
}

func TestWithoutASocketNothingIsAttempted(t *testing.T) {
	do, seen := record(link.Reply{Code: link.CodeOK})
	var stdout, stderr bytes.Buffer
	t.Setenv("MINOS_SOCKET", "")
	code := shim([]string{"status"}, strings.NewReader(""), &stdout, &stderr, do)
	if code != link.CodeLocal || seen.Op != "" {
		t.Fatalf("exit %d after sending %+v", code, *seen)
	}
}
