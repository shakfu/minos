package conformance

// The suite must not import the implementation it tests: a test that reaches
// into the server passes for reasons another server could not reproduce.

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var forbidden = []string{"minos/internal/", "minos/cmd/"}

func TestNoFileImportsTheImplementation(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	offenders := map[string][]string{}
	for _, name := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", name, err)
		}
		for _, spec := range parsed.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			for _, prefix := range forbidden {
				if strings.HasPrefix(path, prefix) {
					offenders[name] = append(offenders[name], path)
				}
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("conformance tests must be black box: %v", offenders)
	}
}

// Guards the guard: a glob that matched nothing would pass silently.
func TestTheSuiteWasActuallyRead(t *testing.T) {
	tests, _ := filepath.Glob("*_test.go")
	if len(tests) < 2 {
		t.Fatalf("read %d test files", len(tests))
	}
}
