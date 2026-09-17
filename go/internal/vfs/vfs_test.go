package vfs

// What the conformance suite cannot reach from outside: symlinks and hard links,
// which no VFS call creates.

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"minos/internal/config"
)

// home is a Vfs whose user demo owns one file, f.txt, and one directory, a.
func home(t *testing.T) (*Vfs, string) {
	t.Helper()
	root := t.TempDir()
	files := New(config.Config{Dist: filepath.Join(root, "dist"), VfsRoot: filepath.Join(root, "vfs")})
	dir := filepath.Join(root, "vfs", "demo")
	must(t, os.MkdirAll(filepath.Join(dir, "a"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("precious"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "a", "g.txt"), []byte("g"), 0o644))
	return files, dir
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func refusedWith(t *testing.T, err error, status int) {
	t.Helper()
	known, ok := AsError(err)
	if !ok || known.Status != status {
		t.Fatalf("got %v, want a %d refusal", err, status)
	}
}

func contents(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	must(t, err)
	return string(raw)
}

// A hard link is the same file under a second name, and O_TRUNC would empty both.
func TestCopyOntoTheSameFileIsRefused(t *testing.T) {
	files, dir := home(t)
	must(t, os.Link(filepath.Join(dir, "f.txt"), filepath.Join(dir, "link.txt")))

	for _, to := range []string{"home:/f.txt", "home:/link.txt"} {
		_, err := files.Copy("demo", "home:/f.txt", to)
		refusedWith(t, err, http.StatusBadRequest)
	}
	if got := contents(t, filepath.Join(dir, "f.txt")); got != "precious" {
		t.Fatalf("the file now holds %q", got)
	}
}

func TestCopyIntoItsOwnSubtreeIsRefused(t *testing.T) {
	files, dir := home(t)
	for _, to := range []string{"home:/a/b", "home:/a/b/c", "home:/a"} {
		_, err := files.Copy("demo", "home:/a", to)
		refusedWith(t, err, http.StatusBadRequest)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a/b was created: %v", err)
	}
}

func TestCopyOfATreeCopiesItsFiles(t *testing.T) {
	files, dir := home(t)
	if _, err := files.Copy("demo", "home:/a", "home:/b"); err != nil {
		t.Fatal(err)
	}
	if got := contents(t, filepath.Join(dir, "b", "g.txt")); got != "g" {
		t.Fatalf("b/g.txt holds %q", got)
	}
}

func TestAMountpointCannotBeDeletedOrMoved(t *testing.T) {
	files, dir := home(t)
	_, err := files.Unlink("demo", "home:/")
	refusedWith(t, err, http.StatusForbidden)
	_, err = files.Rename("demo", "home:/", "home:/x")
	refusedWith(t, err, http.StatusForbidden)
	_, err = files.Rename("demo", "home:/f.txt", "home:/")
	refusedWith(t, err, http.StatusForbidden)
	if _, err := os.Stat(filepath.Join(dir, "f.txt")); err != nil {
		t.Fatalf("home was changed: %v", err)
	}
}

func TestASymlinkOutOfTheMountpointLeadsNowhere(t *testing.T) {
	files, dir := home(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	must(t, os.WriteFile(outside, []byte("secret"), 0o644))
	must(t, os.Symlink(outside, filepath.Join(dir, "escape.txt")))

	if handle, err := files.Readfile("demo", "home:/escape.txt"); err == nil {
		handle.Close()
		t.Fatal("readfile followed a link out of home")
	}
	_, err := files.Copy("demo", "home:/escape.txt", "home:/kept.txt")
	refusedWith(t, err, http.StatusNotFound)
	if exists, _ := files.Exists("demo", "home:/escape.txt"); exists != false {
		t.Fatal("a link out of home reads as existing")
	}
}

func TestASymlinkInsideTheMountpointIsFollowed(t *testing.T) {
	files, dir := home(t)
	must(t, os.Symlink("f.txt", filepath.Join(dir, "alias.txt")))

	handle, err := files.Readfile("demo", "home:/alias.txt")
	must(t, err)
	defer handle.Close()
	if filepath.Base(handle.Name()) != "alias.txt" {
		t.Fatalf("served as %q", handle.Name())
	}
}
