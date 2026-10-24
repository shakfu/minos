package main

// The scripted agent, run in place of claude under each minosb. It speaks
// Claude Code's stream-json, so the broker's claude adapter reads it
// unchanged, and it reaches the room through minosa alone.
//
// Its first turn is its script: progress posted with `say`, a finding posted
// with a payload, a wait on findings other agents post, and one submission
// awaited to its decision. Every later frame on stdin ends a turn that says
// nothing, so agents sharing a room never answer each other in a loop.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The fence the broker wraps a payload in (internal/broker, payloadFence).
const fence = "```minos-payload\n"

// How long a wait step polls for the findings it needs.
const needWithin = time.Minute

type finding struct {
	Topic    string `json:"topic"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

// step is one action in a script. Exactly one field besides pause is set.
type step struct {
	say     string
	share   *finding
	needs   []string
	propose string
	body    string
	pause   time.Duration
}

type role struct {
	task    string
	steps   []step
	summary string
}

// The review of a fictional pull request. bob's script cannot pass its wait
// until alice's finding reaches him, and worker's needs both.
var roles = map[string]role{
	"alice": {
		task: "Review PR #42, a session cache in minosd, for security.",
		steps: []step{
			{say: "reading the diff: 4 files, +212 -18", pause: 400 * time.Millisecond},
			{say: "the cache maps a session cookie's hash to the session, for 2 minutes", pause: 500 * time.Millisecond},
			{say: "the key is the cookie hash alone", pause: 500 * time.Millisecond},
			{share: &finding{"cache-key", "high", "the cache key omits the tenant id; two tenants can share an entry"}},
			{propose: "Block #42 until the cache key includes the tenant id", body: "One high finding, cache-key, posted in the room."},
		},
		summary: "security: 1 high finding (cache-key); merge blocked until it is fixed",
	},
	"bob": {
		task: "Review PR #42, a session cache in minosd, for performance.",
		steps: []step{
			{say: "login path without the cache: p50 41 ms, p99 180 ms", pause: 600 * time.Millisecond},
			{say: "with the cache: p50 6 ms, p99 22 ms, hit rate 91%", pause: 300 * time.Millisecond},
			{needs: []string{"cache-key"}},
			{say: "re-measured with the tenant id in the key: p50 7 ms, hit rate 89%", pause: 300 * time.Millisecond},
			{share: &finding{"cache-cost", "low", "the tenant id in the key costs 1 ms at p50 and 2 points of hit rate"}},
			{propose: "Raise the cache TTL from 2 to 10 minutes", body: "Recovers the 2 points of hit rate the tenant id costs."},
		},
		summary: "performance: the cache-key fix costs 1 ms at p50; TTL change proposed",
	},
	"worker": {
		task: "Review PR #42, a session cache in minosd, for test coverage.",
		steps: []step{
			{say: "running the suite: 212 tests, 0 failures", pause: 500 * time.Millisecond},
			{say: "no test logs in two tenants whose cookies hash alike", pause: 300 * time.Millisecond},
			{needs: []string{"cache-key", "cache-cost"}},
			{say: "drafting a two-tenant isolation test: cache-key sets what it asserts, cache-cost its timing bound", pause: 300 * time.Millisecond},
			{propose: "Add a two-tenant isolation test to #42 before merge", body: "Covers cache-key; bounded by cache-cost."},
		},
		summary: "tests: suite passes; isolation test proposed",
	},
}

// agent runs one role's script against its stdin and stdout, and returns the
// exit code. What it notes on stderr is for the simulation's narration.
func agent(name string) int {
	r, ok := roles[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "no role %q\n", name)
		return 2
	}
	pace := options{}
	pace.pace, _ = strconv.ParseFloat(os.Getenv(paceVariable), 64)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 8<<20)
	if !in.Scan() {
		fmt.Fprintln(os.Stderr, "no task on stdin")
		return 1
	}
	for _, text := range frameTexts(in.Bytes()) {
		fmt.Fprintf(os.Stderr, "stdin <- task: %s\n", text)
	}
	for _, s := range r.steps {
		if err := s.run(); err != nil {
			fmt.Fprintf(os.Stderr, "failed: %v\n", err)
			return 1
		}
		time.Sleep(pace.scaled(s.pause))
	}
	result(r.summary)
	for in.Scan() {
		texts := frameTexts(in.Bytes())
		var authors []string
		for _, text := range texts {
			if author, _, ok := strings.Cut(text, ": "); ok && !slices.Contains(authors, author) {
				authors = append(authors, author)
			}
		}
		noun := "messages"
		if len(texts) == 1 {
			noun = "message"
		}
		fmt.Fprintf(os.Stderr, "stdin <- one frame, %d %s, from %s\n", len(texts), noun, strings.Join(authors, ", "))
		result("")
	}
	return 0
}

func (s step) run() error {
	switch {
	case s.say != "":
		_, err := minosa("say", "--", s.say)
		return err
	case s.share != nil:
		return share(*s.share)
	case len(s.needs) > 0:
		return need(s.needs)
	case s.propose != "":
		return propose(s.propose, s.body)
	}
	return nil
}

func share(f finding) error {
	raw, _ := json.Marshal(f)
	file := filepath.Join(os.TempDir(), fmt.Sprintf("minos-sim-%d-%s.json", os.Getpid(), f.Topic))
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		return err
	}
	defer os.Remove(file)
	_, err := minosa("say", "-payload", file, "--", fmt.Sprintf("finding %s (%s): %s", f.Topic, f.Severity, f.Detail))
	return err
}

// need polls the socket lane until every topic has been shared by someone.
// The same messages also reach stdin, held until this turn ends.
func need(topics []string) error {
	missing := map[string]bool{}
	for _, topic := range topics {
		missing[topic] = true
	}
	fmt.Fprintf(os.Stderr, "waiting for findings: %s\n", strings.Join(topics, ", "))
	deadline := time.Now().Add(needWithin)
	for len(missing) > 0 {
		if time.Now().After(deadline) {
			return fmt.Errorf("no finding on %v within %s", topics, needWithin)
		}
		out, err := minosa("messages", "-json")
		if err != nil {
			return err
		}
		var reply struct {
			Records []struct {
				Author string `json:"author"`
				Body   string `json:"body"`
			} `json:"records"`
		}
		if err := json.Unmarshal([]byte(out), &reply); err != nil {
			return fmt.Errorf("messages: %v", err)
		}
		for _, record := range reply.Records {
			if f, ok := payload(record.Body); ok && missing[f.Topic] {
				delete(missing, f.Topic)
				fmt.Fprintf(os.Stderr, "socket -> %s's finding %s\n", record.Author, f.Topic)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

func payload(body string) (finding, bool) {
	_, rest, ok := strings.Cut(body, fence)
	if !ok {
		return finding{}, false
	}
	raw, _, _ := strings.Cut(rest, "\n```")
	var f finding
	return f, json.Unmarshal([]byte(raw), &f) == nil && f.Topic != ""
}

// propose submits and awaits the decision, then says what it was.
func propose(subject, body string) error {
	id, err := minosa("submit", "-subject", subject, "--", body)
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if _, err := minosa("say", "--", "awaiting a decision on: "+subject); err != nil {
		return err
	}
	timeout := os.Getenv(awaitVariable)
	if timeout == "" {
		timeout = "300"
	}
	decided, err := minosa("await", id, "-timeout", timeout)
	if err != nil {
		return err
	}
	state, comment, _ := strings.Cut(strings.TrimSpace(decided), ": ")
	var said string
	switch state {
	case "approved":
		said = "approved, proceeding: " + subject
	case "rejected":
		said = "rejected, dropping it: " + subject
		if comment != "" {
			said += " (" + comment + ")"
		}
	default:
		said = "no decision on: " + subject + "; leaving it pending"
	}
	_, err = minosa("say", "--", said)
	return err
}

// minosa runs one shim operation and answers its stdout.
func minosa(args ...string) (string, error) {
	command := exec.Command("minosa", args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("minosa %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// result ends a turn. An empty result is relayed as nothing.
func result(text string) {
	raw, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": text})
	os.Stdout.Write(append(raw, '\n'))
}

// frameTexts is the text blocks of one pushed user frame.
func frameTexts(line []byte) []string {
	var frame struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &frame) != nil {
		return []string{string(line)}
	}
	texts := make([]string, 0, len(frame.Message.Content))
	for _, block := range frame.Message.Content {
		texts = append(texts, oneLine(block.Text))
	}
	return texts
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }
