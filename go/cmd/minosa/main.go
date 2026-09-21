// Command minosa is the container's whole reach into the conversation.
//
// It opens the run's socket, sends one request, prints the reply and exits with
// the broker's status. It holds no policy, no credential and no state: replacing
// it with a hostile program changes nothing, because it never had authority.
//
// No subcommand takes JSON on a command line. A payload is read from a file or
// from stdin, because quoting JSON in an argument is the most reliable way to
// produce a malformed request.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"minos/internal/link"
)

const usage = `minosa reaches this run's conversation. Six operations:

  minosa messages [-since N] [-json]     what was said, since the last drain
  minosa say [-file F|-stdin] [text]     post to the room
  minosa submit -subject S [-file F]     propose something needing a decision
  minosa await ID [-timeout SECONDS]     block until it is decided
  minosa progress N                      record that N has been read
  minosa status                          the room, the window and the connection

  -payload F   a JSON file to carry beside the text of say or submit
  -socket P    the run's socket; MINOS_SOCKET is the default
`

// call is the socket round trip, replaced in tests.
type call func(link.Request, time.Duration) (link.Reply, error)

func main() {
	os.Exit(shim(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, nil))
}

func shim(args []string, stdin io.Reader, stdout, stderr io.Writer, do call) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return link.CodeLocal
	}

	set := flag.NewFlagSet(args[0], flag.ContinueOnError)
	set.SetOutput(stderr)
	socket := set.String("socket", os.Getenv("MINOS_SOCKET"), "the run's socket")
	asJSON := set.Bool("json", false, "print the reply as JSON")
	since := set.Int64("since", 0, "deliver everything after this sequence number")
	subject := set.String("subject", "", "the headline of a submission")
	file := set.String("file", "", "read the body from this file")
	fromStdin := set.Bool("stdin", false, "read the body from stdin")
	payload := set.String("payload", "", "read a JSON payload from this file")
	timeout := set.Float64("timeout", 0, "seconds to block in await")

	flags, rest := split(args[1:])
	if err := set.Parse(flags); err != nil {
		return link.CodeLocal
	}
	if *socket == "" {
		fmt.Fprintln(stderr, "minosa: no socket. Set MINOS_SOCKET or pass -socket.")
		return link.CodeLocal
	}
	if do == nil {
		do = func(request link.Request, deadline time.Duration) (link.Reply, error) {
			return link.Do(*socket, request, deadline)
		}
	}

	request := link.Request{Op: args[0]}
	deadline := 30 * time.Second

	switch args[0] {
	case link.OpMessages:
		request.Since = *since

	case link.OpSay, link.OpSubmit:
		body, err := text(rest, *file, *fromStdin, stdin)
		if err != nil {
			fmt.Fprintf(stderr, "minosa: %v\n", err)
			return link.CodeLocal
		}
		request.Body = body
		request.Subject = *subject
		if *payload != "" {
			raw, err := os.ReadFile(*payload)
			if err != nil {
				fmt.Fprintf(stderr, "minosa: cannot read %s: %v\n", *payload, err)
				return link.CodeLocal
			}
			// Checked here so the refusal names the file. The broker checks it
			// again, because the shim is not trusted to have checked anything.
			if !json.Valid(raw) {
				fmt.Fprintf(stderr, "minosa: %s is not JSON. Fix the file and run this again.\n", *payload)
				return link.CodeLocal
			}
			request.Payload = json.RawMessage(raw)
		}

	case link.OpAwait:
		if len(rest) != 1 {
			fmt.Fprintln(stderr, "minosa: await names one submission.")
			return link.CodeLocal
		}
		request.ID = rest[0]
		request.Timeout = *timeout
		wait := *timeout
		if wait <= 0 {
			wait = 300
		}
		// The broker answers when it decides or when the wait runs out, so the
		// connection must outlast the wait rather than cut it short.
		deadline = time.Duration(wait+30) * time.Second

	case link.OpProgress:
		if len(rest) != 1 {
			fmt.Fprintln(stderr, "minosa: progress names one sequence number.")
			return link.CodeLocal
		}
		seq, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil {
			fmt.Fprintf(stderr, "minosa: %q is not a sequence number.\n", rest[0])
			return link.CodeLocal
		}
		request.Seq = seq

	case link.OpStatus:

	default:
		fmt.Fprint(stderr, usage)
		return link.CodeLocal
	}

	reply, err := do(request, deadline)
	if err != nil {
		fmt.Fprintf(stderr, "minosa: %v\n", err)
		return link.CodeLocal
	}
	show(stdout, stderr, args[0], reply, *asJSON)
	return reply.Code
}

// takesValue names the flags whose value is the next argument, which is what
// tells a positional argument from a flag's value when the two are mixed.
var takesValue = map[string]bool{
	"socket": true, "since": true, "subject": true,
	"file": true, "payload": true, "timeout": true,
}

// split separates flags from positional arguments, in any order. The flag
// package stops at the first non-flag, and `minosa await ID -timeout 30` is
// what a model writes: refusing it teaches nothing and costs a turn.
func split(args []string) (flags, rest []string) {
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if !strings.HasPrefix(argument, "-") || argument == "-" {
			rest = append(rest, argument)
			continue
		}
		flags = append(flags, argument)

		name, _, joined := strings.Cut(strings.TrimLeft(argument, "-"), "=")
		if takesValue[name] && !joined && index+1 < len(args) {
			index++
			flags = append(flags, args[index])
		}
	}
	return flags, rest
}

// text is the body, from the arguments, a file or stdin. Exactly one source.
func text(rest []string, file string, fromStdin bool, stdin io.Reader) (string, error) {
	sources := 0
	for _, used := range []bool{len(rest) > 0, file != "", fromStdin} {
		if used {
			sources++
		}
	}
	switch {
	case sources == 0:
		return "", nil
	case sources > 1:
		return "", fmt.Errorf("the body comes from arguments, -file or -stdin, not from more than one")
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("cannot read %s: %v", file, err)
		}
		return string(raw), nil
	case fromStdin:
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("cannot read stdin: %v", err)
		}
		return string(raw), nil
	default:
		return strings.Join(rest, " "), nil
	}
}

func show(stdout, stderr io.Writer, op string, reply link.Reply, asJSON bool) {
	if asJSON {
		raw, err := json.Marshal(reply)
		if err == nil {
			fmt.Fprintln(stdout, string(raw))
			return
		}
	}
	if reply.Error != "" {
		fmt.Fprintln(stderr, reply.Error)
		if reply.Rule != "" {
			fmt.Fprintln(stderr, "Permitted by: "+reply.Rule)
		}
		return
	}

	switch op {
	case link.OpMessages:
		for _, record := range reply.Records {
			switch record.Kind {
			case link.KindGap:
				fmt.Fprintln(stderr, record.Body)
			case link.KindEvent:
				fmt.Fprintf(stdout, "[%d] %s\n", record.Seq, record.Body)
			default:
				fmt.Fprintf(stdout, "[%d] %s: %s\n", record.Seq, record.Author, record.Body)
			}
		}
	case link.OpSubmit:
		fmt.Fprintln(stdout, reply.ID)
	case link.OpAwait:
		line := reply.State
		if reply.Comment != "" {
			line += ": " + reply.Comment
		}
		fmt.Fprintln(stdout, line)
	case link.OpStatus:
		if reply.Status != nil {
			raw, _ := json.MarshalIndent(reply.Status, "", "  ")
			fmt.Fprintln(stdout, string(raw))
		}
	}
}
