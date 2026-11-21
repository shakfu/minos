// Command acprun runs one ACP agent under go/minosb, against a server of its
// own. demo posts each -say in turn; the run prints the room's replies and the
// ACP events. -hold keeps the run up for the terminal client.
//
// It is a probe, not a test: a real agent spends turns on its provider, and
// runs unsandboxed in a temporary directory. Run it from go/ with
// `make acp-run`, which builds go/minosd and go/minosb first.
//
//	go run ./cmd/acprun [-isolate] [-permission allow] [-hold] [-say TEXT]... -- AGENT ARGS...
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"minos/conformance"
	"minos/internal/client"
)

type says []string

func (s *says) String() string     { return strings.Join(*s, "|") }
func (s *says) Set(v string) error { *s = append(*s, v); return nil }

func main() { os.Exit(run()) }

func run() int {
	var prompts says
	flag.Var(&prompts, "say", "a message demo posts; repeat for more turns")
	permission := flag.String("permission", "deny", "minosb -permission")
	isolate := flag.Bool("isolate", false, "give the agent a temporary HOME and XDG dirs")
	hold := flag.Bool("hold", false, "after the scripted turns, keep running for the TUI until Ctrl-C")
	limit := flag.Duration("turn", 3*time.Minute, "how long one turn may take")
	flag.Parse()
	argv := flag.Args()
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "acprun: give the agent command after --")
		return 2
	}
	root, err := filepath.Abs("..")
	if err != nil {
		fmt.Fprintln(os.Stderr, "acprun:", err)
		return 1
	}
	out, err := os.MkdirTemp("", "acprun-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "acprun:", err)
		return 1
	}
	work := filepath.Join(out, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "acprun:", err)
		return 1
	}

	server, err := conformance.Launch(filepath.Join(out, "server"), []string{filepath.Join(root, "go", "minosd")}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acprun:", err)
		return 1
	}
	defer server.Stop()
	_, demo, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		fmt.Fprintln(os.Stderr, "acprun: demo cannot connect:", err)
		return 1
	}
	defer demo.Stop()
	room, err := demo.OpenRoom([]client.Principal{{Kind: "user", ID: "worker"}}, "ACP run", "persisted")
	if err != nil {
		fmt.Fprintln(os.Stderr, "acprun: cannot open the room:", err)
		return 1
	}

	events := filepath.Join(out, "events.jsonl")
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("acprun-%d.sock", os.Getpid()))
	args := []string{"-server", server.Base, "-user", "worker", "-room", room.ID, "-socket", socket,
		"-interrupters", "demo", "-events", events, "-agent", "acp", "-permission", *permission, "--"}
	broker := exec.Command(filepath.Join(root, "go", "minosb"), append(args, argv...)...)
	broker.Dir = work
	broker.Env = append(os.Environ(), "MINOS_PASSWORD=worker")
	if *isolate {
		home := filepath.Join(out, "home")
		broker.Env = append(broker.Env, "HOME="+home, "XDG_CONFIG_HOME="+home+"/.config",
			"XDG_DATA_HOME="+home+"/.local/share", "XDG_STATE_HOME="+home+"/.local/state", "XDG_CACHE_HOME="+home+"/.cache")
	}
	stderr, err := os.Create(filepath.Join(out, "minosb.stderr"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "acprun:", err)
		return 1
	}
	defer stderr.Close()
	broker.Stderr = stderr
	stdout, err := os.Create(filepath.Join(out, "agent.jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "acprun:", err)
		return 1
	}
	defer stdout.Close()
	broker.Stdout = stdout
	control, err := broker.StdinPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "acprun:", err)
		return 1
	}
	if err := broker.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "acprun: cannot start minosb:", err)
		return 1
	}
	exited := make(chan struct{})
	code := 0
	go func() {
		if e, ok := broker.Wait().(*exec.ExitError); ok {
			code = e.ExitCode()
		}
		close(exited)
	}()

	fmt.Printf("agent: %s\nout:   %s\n", strings.Join(argv, " "), out)
	tail := &tailer{path: events}
	if !tail.until(30*time.Second, exited, func(e map[string]any) bool { return e["event"] == "ready" }) {
		fmt.Println("FAIL  minosb never reported ready; see", filepath.Join(out, "minosb.stderr"))
		return 1
	}
	failed := 0
	for _, text := range prompts {
		fmt.Printf("\ndemo:   %s\n", text)
		seen := len(demo.Log(room.ID))
		_ = demo.Send(room.ID, text)
		if !tail.until(*limit, exited, func(e map[string]any) bool { return e["event"] == "turn" }) {
			fmt.Println("FAIL  no turn within", *limit)
			failed++
			break
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if printReplies(demo.Log(room.ID)[seen:]) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	if *hold {
		fmt.Printf("\nHolding. In another terminal:\n  %s/go/minos -server %s -user demo -password demo -room %s\nCtrl-C ends the run.\n",
			root, server.Base, room.ID)
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt)
		go func() { tail.until(24*time.Hour, exited, func(map[string]any) bool { return false }) }()
		select {
		case <-stop:
		case <-exited:
		}
	}
	_ = control.Close()
	_ = broker.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		tail.until(2*time.Second, nil, func(e map[string]any) bool { return e["event"] == "stopped" })
		fmt.Printf("\nminosb exited %d\n", code)
	case <-time.After(15 * time.Second):
		_ = broker.Process.Kill()
		fmt.Println("FAIL  minosb did not exit within 15 s of SIGTERM")
		failed++
	}
	_ = os.Remove(socket)
	return failed
}

// printReplies prints what anyone but demo said; true once there is one.
func printReplies(messages []client.Message) bool {
	replied := false
	for _, m := range messages {
		if m.Author != "demo" {
			fmt.Printf("%s: %s\n", m.Author, strings.ReplaceAll(m.Body, "\n", "\n        "))
			replied = true
		}
	}
	return replied
}

// tailer reads the events file as minosb appends to it.
type tailer struct {
	path   string
	offset int64
}

var shown = map[string]bool{"turn": true, "result": true, "usage": true, "permission": true,
	"refused": true, "undelivered": true, "torn": true, "exited": true, "stopped": true, "interrupt": true}

// until prints the ACP events as they come and answers once match holds.
func (t *tailer) until(limit time.Duration, exited <-chan struct{}, match func(map[string]any) bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if file, err := os.Open(t.path); err == nil {
			_, _ = file.Seek(t.offset, 0)
			reader := bufio.NewReader(file)
			for {
				line, err := reader.ReadBytes('\n')
				if err != nil {
					break
				}
				t.offset += int64(len(line))
				var e map[string]any
				if json.Unmarshal(line, &e) != nil {
					continue
				}
				if name, _ := e["event"].(string); shown[name] {
					if name == "result" && len(fmt.Sprint(e["text"])) > 80 {
						e["text"] = fmt.Sprint(e["text"])[:80] + "..."
					}
					raw, _ := json.Marshal(e)
					fmt.Printf("  event %s\n", raw)
				}
				if match(e) {
					file.Close()
					return true
				}
			}
			file.Close()
		}
		select {
		case <-exited:
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}
