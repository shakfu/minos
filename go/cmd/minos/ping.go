package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"minos/internal/client"
)

// What this build of the minos tools does that a dispatcher may depend on.
// `make install` installs minos and minosb together, so minos speaks for both.
// Add a name when a flag or event a dispatcher uses is added; never rename one.
var capabilities = []string{
	"minos.ping",
	"minos.room-ensure",
	"minos.room",
	"minos.exit-with",
	"minosb.task",
	"minosb.events",
	"minosb.linger",
	"minosb.interrupters",
	"minosb.end",
	"minosb.pushed",
	"minosb.session",
	"minosb.agent-spec",
	"minosb.acp",
}

// Exit statuses of `minos ping`, for a dispatcher to tell apart.
const (
	pingNoServer = 3
	pingRefused  = 4
)

// ping checks the server and, with credentials in the environment, a login.
// It prints one JSON line naming what it checked and this build's
// capabilities.
func ping(args []string, stdout, stderr io.Writer) int {
	fallback := os.Getenv("MINOS_SERVER")
	if fallback == "" {
		fallback = "http://127.0.0.1:8000"
	}
	flags := flag.NewFlagSet("minos ping", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", fallback, "base URL")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	session := client.NewHTTP(*server)
	if err := session.Ping(); err != nil {
		fmt.Fprintf(stderr, "minos: no server: %v\n", err)
		return pingNoServer
	}
	report := map[string]any{"server": *server, "capabilities": capabilities}
	if user, password := os.Getenv("MINOS_USER"), os.Getenv("MINOS_PASSWORD"); user != "" && password != "" {
		if _, err := session.Login(user, password); err != nil {
			var refusal *client.TransportError
			if errors.As(err, &refusal) && refusal.Status == http.StatusForbidden {
				fmt.Fprintf(stderr, "minos: %s was refused: %v\n", user, err)
				return pingRefused
			}
			fmt.Fprintf(stderr, "minos: no server: %v\n", err)
			return pingNoServer
		}
		_ = session.Logout()
		report["user"] = user
	}
	raw, _ := json.Marshal(report)
	fmt.Fprintln(stdout, string(raw))
	return 0
}
