package broker

// The shim inside a real container, reaching the broker through a bind-mounted
// socket. Opt-in: set MINOS_CONTAINER=1 with a working `docker`. The scripted
// tests cannot check the two facts this does: that a container on the host's
// uid reaches the socket with no network at all, and that one on another uid
// cannot.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minos/internal/config"
	"minos/internal/link"
	"minos/internal/testserver"
)

const smokeImage = "minos-smoke:test"

func TestTheShimReachesTheBrokerFromInsideAContainer(t *testing.T) {
	if os.Getenv("MINOS_CONTAINER") != "1" {
		t.Skip("set MINOS_CONTAINER=1 to run against docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker")
	}
	buildImage(t)

	run := dispatch(t, config.HistoryLimit, true)
	socket := testserver.SocketPath(t)
	listener, err := link.Listen(socket, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go link.Serve(listener, run.worker.Handle)
	mount := filepath.Dir(socket)
	self := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())

	// status, and nothing else in the container: no network, a read-only root.
	if out, code := shim(t, mount, self, "status"); code != 0 || !strings.Contains(out, run.room) {
		t.Fatalf("status exited %d:\n%s", code, out)
	}

	say(t, run.developer, run.room, "hello, container")
	if out, code := shim(t, mount, self, "messages"); code != 0 || !strings.Contains(out, "demo: hello, container") {
		t.Fatalf("messages exited %d:\n%s", code, out)
	}

	if out, code := shim(t, mount, self, "say", "hello from inside"); code != 0 {
		t.Fatalf("say exited %d:\n%s", code, out)
	}
	waitFor(t, "the container's message in the room", func() bool {
		for _, message := range run.developer.Log(run.room) {
			if message.Body == "hello from inside" && message.Author == "bob" {
				return true
			}
		}
		return false
	})

	out, code := shim(t, mount, self, "submit", "-subject", "install ripgrep", "apt-get install ripgrep")
	id := strings.TrimSpace(out)
	if code != 0 || id == "" {
		t.Fatalf("submit exited %d:\n%s", code, out)
	}
	waitFor(t, "the submission in the queue", func() bool { _, ok := run.developer.Queued()[id]; return ok })
	if err := run.developer.Approve(id); err != nil {
		t.Fatal(err)
	}
	if out, code := shim(t, mount, self, "await", id, "-timeout", "10"); code != 0 || strings.TrimSpace(out) != "approved" {
		t.Fatalf("await exited %d:\n%s", code, out)
	}

	delivered := run.worker.Handle(link.Request{Op: link.OpStatus}).Status.Delivered
	if out, code := shim(t, mount, self, "progress", fmt.Sprint(delivered)); code != 0 {
		t.Fatalf("progress exited %d:\n%s", code, out)
	}

	// The socket is the credential: with its directory open to all, the
	// socket's own mode is what keeps another uid out.
	if err := os.Chmod(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := shim(t, mount, "65534:65534", "status"); code == 0 || !strings.Contains(out, "permission denied") {
		t.Fatalf("another uid reached the socket: exit %d:\n%s", code, out)
	}
}

// shim runs one minosa operation in a fresh container and returns its combined
// output and exit code.
func shim(t *testing.T, mount, user string, args ...string) (string, int) {
	t.Helper()
	argv := append([]string{
		"run", "--rm", "--network", "none", "--read-only", "--user", user,
		"-v", mount + ":/run/minos", "-e", "MINOS_SOCKET=/run/minos/run.sock",
		smokeImage,
	}, args...)
	command := exec.Command("docker", argv...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = command.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("docker %v did not finish", args)
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode()
	}
	if err != nil {
		t.Fatalf("docker: %v\n%s", err, out)
	}
	return string(out), 0
}

// buildImage is minosa, static, in an image with nothing else in it.
func buildImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "minosa"), "minos/cmd/minosa")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cannot build minosa: %v\n%s", err, out)
	}
	dockerfile := "FROM scratch\nCOPY minosa /minosa\nENTRYPOINT [\"/minosa\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "build", "-q", "-t", smokeImage, dir).CombinedOutput(); err != nil {
		t.Fatalf("cannot build the image: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", smokeImage).Run() })
}
