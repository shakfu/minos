package main

// The whole simulation, against an in-process server, with minosb and minosa
// built from this tree. The test binary plays each agent, as `make sim` does.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"minos/internal/testserver"
)

func TestMain(m *testing.M) {
	if name := os.Getenv(agentVariable); name != "" {
		os.Exit(agent(name))
	}
	os.Exit(m.Run())
}

// binaries builds minosb and minosa, and names the binary that plays the agent.
func binaries(t *testing.T) (bin, self string) {
	t.Helper()
	bin = t.TempDir()
	for _, name := range []string{"minosb", "minosa"} {
		build := exec.Command("go", "build", "-o", filepath.Join(bin, name), "minos/cmd/"+name)
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("cannot build %s: %v\n%s", name, err, out)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return bin, self
}

func TestThreeAgentsShareFindingsAndEveryProposalIsDecided(t *testing.T) {
	bin, self := binaries(t)
	for name, value := range agentSettings() {
		t.Setenv(name, value)
	}
	server := testserver.Start(t, 200)

	var out strings.Builder
	if err := simulate(server.Base, bin, self, options{}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	// bob and worker cannot pass their waits without these reads.
	for _, line := range []string{
		"bob           socket -> alice's finding cache-key",
		"worker        socket -> alice's finding cache-key",
		"worker        socket -> bob's finding cache-cost",
		"demo rejects",
		"room          bob: rejected, dropping it: Raise the cache TTL",
		"room          alice: approved, proceeding: Block #42",
		"pushing the",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("the narration lacks %q\n%s", line, out.String())
		}
	}
}
