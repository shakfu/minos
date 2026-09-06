// Package vfs backs the /vfs/* API.
//
// Paths arrive from the client as "<mountpoint>:/<path>". Each mountpoint maps
// to a real directory; every resolved path is checked against its mountpoint
// root so a crafted path cannot escape it.
package vfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"minos/internal/config"
)

// Error is a VFS request that must be reported to the client with a status.
type Error struct {
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Message }

func fail(status int, format string, args ...any) error {
	return &Error{Message: fmt.Sprintf(format, args...), Status: status}
}

// Methods that may run against a read-only mountpoint.
var readOnlyMethods = map[string]bool{
	"capabilities": true, "exists": true, "stat": true,
	"readdir": true, "readfile": true, "search": true,
}

// Types the browser may render in place. Anything else a user uploaded is
// handed over as a download instead, because a document served inline from this
// origin can script it: it reaches the whole /vfs API with the viewer's cookie.
// SVG is excluded deliberately -- it is an image that carries script.
const inlinePrefix = "image/"

var inlineExact = map[string]bool{"text/plain": true}
var neverInline = map[string]bool{"image/svg+xml": true}

// Extra types OS.js expects that the stdlib table does not carry, plus the ones
// the contract names outright, so the answer does not depend on /etc/mime.types.
var extraTypes = map[string]string{
	".py": "text/x-python", ".ly": "text/x-lilypond", ".ily": "text/x-lilypond",
	".tgz": "application/tar+gzip", ".md": "text/markdown",
	".txt": "text/plain", ".html": "text/html", ".svg": "image/svg+xml",
	".png": "image/png",
}

var byFilename = map[string]string{"Makefile": "text/x-makefile", ".gitignore": "text/plain"}

// Files written into a home directory the first time its owner logs in.
var homeTemplate = map[string]string{".desktop/.shortcuts.json": "[]"}

func init() {
	for suffix, kind := range extraTypes {
		_ = mime.AddExtensionType(suffix, kind)
	}
}

// Vfs resolves virtual paths for one server.
type Vfs struct {
	dist string
	root string
}

func New(settings config.Config) *Vfs {
	return &Vfs{dist: settings.Dist, root: settings.VfsRoot}
}

// mount returns the real root of a mountpoint, and whether it is writable.
func (v *Vfs) mount(name, username string) (string, bool, bool) {
	switch name {
	case "osjs":
		return v.dist, false, true
	case "home":
		return filepath.Join(v.root, username), true, true
	}
	return "", false, false
}

// Resolve maps a virtual path to a real one, refusing escapes and read-only
// writes. The method decides only whether a write is being attempted.
func (v *Vfs) Resolve(virtual, username, method string) (string, error) {
	name, rest, found := strings.Cut(virtual, ":")
	if !found {
		return "", fail(http.StatusBadRequest, "Malformed VFS path: %q", virtual)
	}

	root, writable, known := v.mount(name, username)
	if !known {
		return "", fail(http.StatusNotFound, "No such mountpoint: %s", name)
	}
	if !writable && !readOnlyMethods[method] {
		return "", fail(http.StatusForbidden, "Mountpoint %s is read-only", name)
	}

	root, err := filepath.Abs(root)
	if err != nil {
		return "", fail(http.StatusBadRequest, "Malformed VFS path: %q", virtual)
	}
	target := filepath.Clean(filepath.Join(root, strings.TrimLeft(rest, "/")))

	// Clean has already collapsed any "..", so a path that escaped is now
	// simply outside the root and says so by its prefix.
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", fail(http.StatusForbidden, "Path escapes its mountpoint")
	}
	return target, nil
}

// EnsureHome creates a user's home directory and seeds it on first login.
func (v *Vfs) EnsureHome(username string) error {
	home := filepath.Join(v.root, username)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	for relative, contents := range homeTemplate {
		target := filepath.Join(home, relative)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// -- descriptors -------------------------------------------------------------

// Stat is the times and mode block inside a descriptor.
type Stat struct {
	Size    int64   `json:"size"`
	Mode    uint32  `json:"mode"`
	Atime   string  `json:"atime"`
	Mtime   string  `json:"mtime"`
	Ctime   string  `json:"ctime"`
	AtimeMs float64 `json:"atimeMs"`
	MtimeMs float64 `json:"mtimeMs"`
	CtimeMs float64 `json:"ctimeMs"`
}

// Entry is the file descriptor the client expects from readdir and stat.
type Entry struct {
	IsDirectory bool    `json:"isDirectory"`
	IsFile      bool    `json:"isFile"`
	Mime        *string `json:"mime"`
	Size        int64   `json:"size"`
	Path        string  `json:"path"`
	Filename    string  `json:"filename"`
	Stat        Stat    `json:"stat"`
}

// Times are ISO 8601 in UTC, with the offset spelled out rather than as "Z",
// because that is what the client was written against.
const isoUTC = "2006-01-02T15:04:05.999999-07:00"

func describe(virtual, target string, info fs.FileInfo) Entry {
	isDir := info.IsDir()

	entry := Entry{
		IsDirectory: isDir,
		IsFile:      info.Mode().IsRegular(),
		Size:        info.Size(),
		Path:        virtual,
		Filename:    filepath.Base(target),
		Stat:        Stat{Size: info.Size(), Mode: uint32(info.Mode().Perm())},
	}
	if !isDir {
		kind := GuessMime(target)
		entry.Mime = &kind
	}

	// Mode carries the file-type bits as well as the permissions, and the
	// three times come from the same place, so both go through the raw stat.
	if raw, ok := info.Sys().(*syscall.Stat_t); ok {
		entry.Stat.Mode = uint32(raw.Mode)
		access := time.Unix(raw.Atim.Sec, raw.Atim.Nsec)
		modify := time.Unix(raw.Mtim.Sec, raw.Mtim.Nsec)
		change := time.Unix(raw.Ctim.Sec, raw.Ctim.Nsec)
		entry.Stat.Atime, entry.Stat.AtimeMs = stamp(access)
		entry.Stat.Mtime, entry.Stat.MtimeMs = stamp(modify)
		entry.Stat.Ctime, entry.Stat.CtimeMs = stamp(change)
	}
	return entry
}

func stamp(at time.Time) (string, float64) {
	return at.UTC().Format(isoUTC), float64(at.UnixNano()) / float64(time.Millisecond)
}

// GuessMime reports a file's type, by name first and extension second.
func GuessMime(target string) string {
	name := filepath.Base(target)
	if kind, ok := byFilename[name]; ok {
		return kind
	}
	kind := mime.TypeByExtension(filepath.Ext(name))
	if kind == "" {
		return "application/octet-stream"
	}
	// The contract reports a bare type; the charset is the response header's
	// business rather than the descriptor's.
	if base, _, found := strings.Cut(kind, ";"); found {
		return strings.TrimSpace(base)
	}
	return kind
}

// MayRenderInline reports whether a type is safe to serve without a download
// disposition. See the note on neverInline above.
func MayRenderInline(kind string) bool {
	if neverInline[kind] {
		return false
	}
	return inlineExact[kind] || strings.HasPrefix(kind, inlinePrefix)
}

// -- operations --------------------------------------------------------------

// virtualParent is the prefix a directory's children are named from.
func virtualParent(virtual string) string {
	name, rest, _ := strings.Cut(virtual, ":")
	return name + ":" + strings.TrimRight(rest, "/")
}

func (v *Vfs) Capabilities(username, path string) (any, error) {
	if _, err := v.Resolve(path, username, "capabilities"); err != nil {
		return nil, err
	}
	return map[string]bool{"sort": false, "pagination": false}, nil
}

func (v *Vfs) Exists(username, path string) (any, error) {
	target, err := v.Resolve(path, username, "exists")
	if err != nil {
		return nil, err
	}
	_, err = os.Stat(target)
	return err == nil, nil
}

func (v *Vfs) Stat(username, path string) (any, error) {
	target, err := v.Resolve(path, username, "stat")
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", path)
	}
	return describe(path, target, info), nil
}

func (v *Vfs) Readdir(username, path string) (any, error) {
	target, err := v.Resolve(path, username, "readdir")
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return nil, fail(http.StatusNotFound, "Not a directory: %s", path)
	}

	children, err := os.ReadDir(target)
	if err != nil {
		return nil, fail(http.StatusNotFound, "Not a directory: %s", path)
	}
	sort.Slice(children, func(a, b int) bool {
		return strings.ToLower(children[a].Name()) < strings.ToLower(children[b].Name())
	})

	base := virtualParent(path)
	entries := make([]Entry, 0, len(children))
	for _, child := range children {
		childInfo, err := child.Info()
		if err != nil {
			continue
		}
		entries = append(entries, describe(base+"/"+child.Name(), filepath.Join(target, child.Name()), childInfo))
	}
	return entries, nil
}

// Readfile returns the real path to send, having checked it is one.
func (v *Vfs) Readfile(username, path string) (string, error) {
	target, err := v.Resolve(path, username, "readfile")
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return "", fail(http.StatusNotFound, "No such file: %s", path)
	}
	return target, nil
}

func (v *Vfs) Writefile(username, path string, body io.Reader) (any, error) {
	target, err := v.Resolve(path, username, "writefile")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot write: %s", path)
	}

	handle, err := os.Create(target)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot write: %s", path)
	}
	defer handle.Close()

	written, err := io.Copy(handle, body)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot write: %s", path)
	}
	return written, nil
}

func (v *Vfs) Mkdir(username, path string, ensure bool) (any, error) {
	target, err := v.Resolve(path, username, "mkdir")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(target); err == nil {
		if ensure {
			return true, nil
		}
		return nil, fail(http.StatusConflict, "Already exists: %s", path)
	}

	if ensure {
		err = os.MkdirAll(target, 0o755)
	} else {
		err = os.Mkdir(target, 0o755)
	}
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot create: %s", path)
	}
	return true, nil
}

func (v *Vfs) Unlink(username, path string) (any, error) {
	target, err := v.Resolve(path, username, "unlink")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(target); err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", path)
	}
	if err := os.RemoveAll(target); err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot delete: %s", path)
	}
	return true, nil
}

func (v *Vfs) Touch(username, path string) (any, error) {
	target, err := v.Resolve(path, username, "touch")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot create: %s", path)
	}

	now := time.Now()
	if err := os.Chtimes(target, now, now); err == nil {
		return true, nil
	}
	handle, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot create: %s", path)
	}
	handle.Close()
	return true, nil
}

func (v *Vfs) Copy(username, from, to string) (any, error) {
	source, err := v.Resolve(from, username, "readfile")
	if err != nil {
		return nil, err
	}
	destination, err := v.Resolve(to, username, "writefile")
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(source)
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", from)
	}
	if info.IsDir() {
		err = copyTree(source, destination)
	} else {
		err = copyFile(source, destination, info.Mode())
	}
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot copy: %s", from)
	}
	return true, nil
}

func (v *Vfs) Rename(username, from, to string) (any, error) {
	source, err := v.Resolve(from, username, "unlink")
	if err != nil {
		return nil, err
	}
	destination, err := v.Resolve(to, username, "writefile")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(source); err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", from)
	}
	if err := os.Rename(source, destination); err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot rename: %s", from)
	}
	return true, nil
}

func (v *Vfs) Search(username, root, pattern string) (any, error) {
	target, err := v.Resolve(root, username, "search")
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return nil, fail(http.StatusNotFound, "Not a directory: %s", root)
	}

	// A pattern with no wildcard in it is a substring, which is what a person
	// typing into a search box means.
	glob := pattern
	if !strings.ContainsAny(pattern, "*?[") {
		glob = "*" + pattern + "*"
	}
	glob = strings.ToLower(glob)

	// Match first, then sort, then stat: matching is a name comparison with no
	// syscall behind it, and only the survivors are worth describing.
	var matches []string
	err = filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == target {
			return nil
		}
		if ok, _ := filepath.Match(glob, strings.ToLower(entry.Name())); ok {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, fail(http.StatusNotFound, "Not a directory: %s", root)
	}
	sort.Strings(matches)

	base := virtualParent(root)
	results := make([]Entry, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(target, match)
		if err != nil {
			continue
		}
		results = append(results, describe(base+"/"+filepath.ToSlash(relative), match, info))
		if len(results) >= config.SearchLimit {
			break
		}
	}
	return results, nil
}

func copyFile(source, destination string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}

// AsError reports whether an error carries a status, and what it is.
func AsError(err error) (*Error, bool) {
	var known *Error
	if errors.As(err, &known) {
		return known, true
	}
	return nil, false
}
