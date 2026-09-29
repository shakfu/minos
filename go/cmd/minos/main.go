// Command minos is the terminal client.
//
// Login happens before the screen is taken, so a refusal is a line on the
// terminal rather than something drawn inside an interface with nothing to show.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"net/url"
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
	user := flag.String("user", "", "username; MINOS_USER, or prompted for when absent")
	password := flag.String("password", "",
		"password; MINOS_PASSWORD, or prompted for when absent. Prefer either: an argument is visible in the process list.")
	flag.Parse()

	if cleartext(*server) {
		fmt.Fprintf(os.Stderr, "minos: %s is plain http to another host; the password crosses the network unencrypted.\n", *server)
	}

	stdin := bufio.NewReader(os.Stdin)
	username := *user
	if username == "" {
		username = os.Getenv("MINOS_USER")
	}
	if username == "" {
		fmt.Print("username: ")
		line, _ := stdin.ReadString('\n')
		username = strings.TrimSpace(line)
	}
	secret := *password
	if secret == "" {
		secret = os.Getenv("MINOS_PASSWORD")
	}
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

// cleartext is whether a login to server would cross a network in the clear:
// http to anything but this host.
func cleartext(server string) bool {
	parsed, err := url.Parse(server)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
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
