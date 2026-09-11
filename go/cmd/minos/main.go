// Command minos is the terminal client.
//
// Login happens before the screen is taken, so a refusal is a line on the
// terminal rather than something drawn inside an interface with nothing to show.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"minos/internal/client"
	"minos/internal/tui"
)

func main() { os.Exit(run()) }

func run() int {
	fallback := os.Getenv("MINOS_SERVER")
	if fallback == "" {
		fallback = "http://127.0.0.1:8000"
	}
	server := flag.String("server", fallback, "base URL")
	user := flag.String("user", "", "username; prompted for when absent")
	password := flag.String("password", "",
		"password; prompted for when absent. Prefer the prompt: an argument is visible in the process list.")
	flag.Parse()

	stdin := bufio.NewReader(os.Stdin)
	username := *user
	if username == "" {
		fmt.Print("username: ")
		line, _ := stdin.ReadString('\n')
		username = strings.TrimSpace(line)
	}
	secret := *password
	if secret == "" {
		secret = readPassword(stdin)
	}

	session, chat, profile, err := client.Connect(*server, username, secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not connect: %v\n", err)
		return 1
	}
	defer func() {
		chat.Stop()
		_ = session.Logout()
	}()

	if err := tui.Run(chat, profile); err != nil {
		fmt.Fprintf(os.Stderr, "minos: %v\n", err)
		return 1
	}
	return 0
}

// readPassword prompts without echo on a terminal, and reads a line otherwise.
func readPassword(stdin *bufio.Reader) string {
	fmt.Print("password: ")
	descriptor := int(os.Stdin.Fd())
	if term.IsTerminal(descriptor) {
		secret, _ := term.ReadPassword(descriptor)
		fmt.Println()
		return string(secret)
	}
	line, _ := stdin.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}
