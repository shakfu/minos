package conformance

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// A first run creates a database, so the server gets a generous start.
const (
	StartupTimeout  = 20 * time.Second
	startupPoll     = 50 * time.Millisecond
	shutdownTimeout = 5 * time.Second
)

// Command is the argv MINOS_CONFORMANCE_CMD names, split on spaces, or nil.
func Command() []string {
	if argv := strings.Fields(os.Getenv("MINOS_CONFORMANCE_CMD")); len(argv) > 0 {
		return argv
	}
	return nil
}

// ExternalURL is a server the operator is running themselves, if any.
func ExternalURL() string { return os.Getenv("MINOS_CONFORMANCE_URL") }

// FreePort is a port nothing is listening on. Racy in principle, adequate for
// a local run; the alternative is a contract that says how to ask.
func FreePort() (int, error) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer probe.Close()
	return probe.Addr().(*net.TCPAddr).Port, nil
}

// Server is one server process, and the base URL that reaches it. A server
// the operator supplied has no process.
type Server struct {
	Base   string
	cmd    *exec.Cmd
	exited chan struct{}
	log    string
	runDir string
}

// External wraps a server that is already running.
func External(base string) *Server { return &Server{Base: base} }

// Launch starts argv on a free port with its state under stateDir. settings
// are extra environment variables, named as in docs/wire-contract.md.
func Launch(stateDir string, argv []string, settings map[string]string) (*Server, error) {
	if len(argv) == 0 {
		return nil, errors.New("no server command to launch")
	}
	port, err := FreePort()
	if err != nil {
		return nil, err
	}
	// Not under stateDir: a server may bind a Unix socket here, and those
	// paths are limited to 103 bytes.
	runDir, err := os.MkdirTemp("", "minos-")
	if err != nil {
		return nil, err
	}

	// The index route is part of the contract, so the suite provides one.
	dist := filepath.Join(stateDir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>minos</html>"), 0o644); err != nil {
		return nil, err
	}

	environment := append(os.Environ(),
		"MINOS_HOST=127.0.0.1",
		fmt.Sprintf("MINOS_PORT=%d", port),
		"MINOS_RUN="+runDir,
		"MINOS_VFS="+filepath.Join(stateDir, "vfs"),
		"MINOS_DIST="+dist,
	)
	for name, value := range settings {
		environment = append(environment, name+"="+value)
	}

	logPath := filepath.Join(stateDir, "server.log")
	output, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = environment
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		output.Close()
		return nil, fmt.Errorf("cannot start %v: %w", argv, err)
	}

	server := &Server{
		Base: fmt.Sprintf("http://127.0.0.1:%d", port), cmd: cmd,
		exited: make(chan struct{}), log: logPath, runDir: runDir,
	}
	go func() {
		_ = cmd.Wait()
		output.Close()
		close(server.exited)
	}()
	if err := AwaitReady(server); err != nil {
		server.Stop()
		return nil, err
	}
	return server, nil
}

// AwaitReady polls /ping until the server answers, or fails with its output.
func AwaitReady(server *Server) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(StartupTimeout)
	for time.Now().Before(deadline) {
		if server.exited != nil {
			select {
			case <-server.exited:
				return fmt.Errorf("server exited with %v:\n%s", server.cmd.ProcessState, server.Output())
			default:
			}
		}
		response, err := client.Get(server.Base + "/ping")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(startupPoll)
	}
	return fmt.Errorf("server did not answer /ping within %v:\n%s", StartupTimeout, server.Output())
}

// Stop terminates the process, kills it if it will not go, and removes its run
// directory.
func (s *Server) Stop() {
	if s.cmd == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.exited:
	case <-time.After(shutdownTimeout):
		_ = s.cmd.Process.Kill()
		<-s.exited
	}
	_ = os.RemoveAll(s.runDir)
}

// Output is whatever the server printed. Only read when something failed.
func (s *Server) Output() string {
	if s.log == "" {
		return ""
	}
	raw, _ := os.ReadFile(s.log)
	return string(raw)
}
