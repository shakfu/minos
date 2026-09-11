// Package config reads the server's settings from the environment.
//
// The names and defaults are the contract's, not this implementation's: see
// docs/wire-contract.md. A server that ignores them cannot be tested.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Session lifetime, refreshed on every request. The client is told this value
// during the websocket handshake and keeps it alive by pinging /ping.
const SessionLifetime = 12 * time.Hour

// Messages returned for a room when the client has no cursor, and the ceiling
// on any single backfill.
const HistoryLimit = 200

// The most results a search reports.
const SearchLimit = 100

// The channel every user subscribes to, carrying machine events rather than
// typed messages. A channel rather than a room: the server is its only producer
// and its audience may only read.
const SystemChannel = "system"

// Config is everything the server reads before it starts.
type Config struct {
	Host string
	Port int

	// Dist is the built client and the osjs: mountpoint; VfsRoot holds home
	// directories; RunDir holds the timeline database and nothing a client
	// ever sees.
	Dist    string
	VfsRoot string
	RunDir  string

	Secret []byte

	// Grace is how long a transient room outlives its last occupant, and Sweep
	// is how often that is checked -- so a room goes between the two.
	Grace time.Duration
	Sweep time.Duration

	// Ping is how long a client may be silent before a keepalive frame.
	Ping time.Duration

	// Test credentials, and who among them is an administrator. A fixture
	// rather than a placeholder: this server is not to be exposed, so no
	// adapter is coming.
	Users  map[string]string
	Admins map[string]bool
}

// Load reads the environment, falling back to the documented defaults.
func Load() Config {
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}

	runDir := path("MINOS_RUN", filepath.Join(root, ".run"))

	return Config{
		Host:    text("MINOS_HOST", "127.0.0.1"),
		Port:    number("MINOS_PORT", 8000),
		Dist:    path("MINOS_DIST", filepath.Join(root, "dist")),
		VfsRoot: path("MINOS_VFS", filepath.Join(root, "vfs")),
		RunDir:  runDir,
		Secret:  []byte(text("MINOS_SECRET", "minos-development-secret")),
		Grace:   seconds("MINOS_ROOM_GRACE", 120),
		Sweep:   seconds("MINOS_ROOM_SWEEP", 15),
		Ping:    seconds("MINOS_WS_PING", 30),
		Users:   map[string]string{"demo": "demo", "alice": "alice", "bob": "bob"},
		Admins:  map[string]bool{"demo": true},
	}
}

// DatabasePath is where the timeline lives. Under RunDir unless overridden,
// because it is runtime state rather than anything a user owns.
func (c Config) DatabasePath() string {
	return path("MINOS_TIMELINE_DB", filepath.Join(c.RunDir, "timeline.db"))
}

// Roster is every account that exists, which the messaging layer takes as an
// injected fact rather than looking up.
func (c Config) Roster() []string {
	names := make([]string, 0, len(c.Users))
	for name := range c.Users {
		names = append(names, name)
	}
	return names
}

func text(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func path(name, fallback string) string {
	absolute, err := filepath.Abs(text(name, fallback))
	if err != nil {
		return fallback
	}
	return absolute
}

func number(name string, fallback int) int {
	value, err := strconv.Atoi(text(name, ""))
	if err != nil {
		return fallback
	}
	return value
}

// seconds accepts a fractional count, so a test can ask for sub-second timings.
func seconds(name string, fallback float64) time.Duration {
	value, err := strconv.ParseFloat(text(name, ""), 64)
	if err != nil {
		value = fallback
	}
	return time.Duration(value * float64(time.Second))
}
