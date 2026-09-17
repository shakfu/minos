// Package vfs backs the /vfs/* API.
//
// Paths arrive from the client as "<mountpoint>:/<path>". Each mountpoint maps
// to a real directory. A path is checked against its mountpoint lexically, and
// every file operation then runs through an os.Root on that directory, so a
// symlink cannot lead outside it either.
package vfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
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

// place is a virtual path resolved to its mountpoint's directory and a name
// inside it. The name is "." for the mountpoint itself.
type place struct {
	dir      string
	name     string
	writable bool
}

// real is the path on disk, for naming and typing a file rather than opening it.
func (p place) real() string { return filepath.Join(p.dir, p.name) }

// open is the mountpoint as a root. A writable one is created if it is missing.
func (p place) open() (*os.Root, error) {
	if p.writable {
		if err := os.MkdirAll(p.dir, 0o755); err != nil {
			return nil, err
		}
	}
	return os.OpenRoot(p.dir)
}

// locate resolves a virtual path, refusing escapes and read-only writes. The
// method decides only whether a write is being attempted.
func (v *Vfs) locate(virtual, username, method string) (place, error) {
	name, rest, found := strings.Cut(virtual, ":")
	if !found {
		return place{}, fail(http.StatusBadRequest, "Malformed VFS path: %q", virtual)
	}

	dir, writable, known := v.mount(name, username)
	if !known {
		return place{}, fail(http.StatusNotFound, "No such mountpoint: %s", name)
	}
	if !writable && !readOnlyMethods[method] {
		return place{}, fail(http.StatusForbidden, "Mountpoint %s is read-only", name)
	}

	dir, err := filepath.Abs(dir)
	if err != nil {
		return place{}, fail(http.StatusBadRequest, "Malformed VFS path: %q", virtual)
	}
	target := filepath.Clean(filepath.Join(dir, strings.TrimLeft(rest, "/")))

	// Clean has already collapsed any "..", so a path that escaped is now
	// simply outside the root and says so by its prefix.
	if target != dir && !strings.HasPrefix(target, dir+string(os.PathSeparator)) {
		return place{}, fail(http.StatusForbidden, "Path escapes its mountpoint")
	}
	relative, err := filepath.Rel(dir, target)
	if err != nil {
		return place{}, fail(http.StatusBadRequest, "Malformed VFS path: %q", virtual)
	}
	return place{dir: dir, name: filepath.ToSlash(relative), writable: writable}, nil
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
		access, modify, change := rawTimes(raw)
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
	if _, err := v.locate(path, username, "capabilities"); err != nil {
		return nil, err
	}
	return map[string]bool{"sort": false, "pagination": false}, nil
}

func (v *Vfs) Exists(username, path string) (any, error) {
	target, err := v.locate(path, username, "exists")
	if err != nil {
		return nil, err
	}
	root, err := target.open()
	if err != nil {
		return false, nil
	}
	defer root.Close()
	_, err = root.Stat(target.name)
	return err == nil, nil
}

func (v *Vfs) Stat(username, path string) (any, error) {
	target, err := v.locate(path, username, "stat")
	if err != nil {
		return nil, err
	}
	root, err := target.open()
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", path)
	}
	defer root.Close()
	info, err := root.Stat(target.name)
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", path)
	}
	return describe(path, target.real(), info), nil
}

func (v *Vfs) Readdir(username, path string) (any, error) {
	target, err := v.locate(path, username, "readdir")
	if err != nil {
		return nil, err
	}
	root, err := target.open()
	if err != nil {
		return nil, fail(http.StatusNotFound, "Not a directory: %s", path)
	}
	defer root.Close()

	children, err := fs.ReadDir(root.FS(), target.name)
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
		entries = append(entries, describe(base+"/"+child.Name(), filepath.Join(target.real(), child.Name()), childInfo))
	}
	return entries, nil
}

// Readfile opens a file to send, having checked it is a regular one. The caller
// closes it.
func (v *Vfs) Readfile(username, path string) (*os.File, error) {
	target, err := v.locate(path, username, "readfile")
	if err != nil {
		return nil, err
	}
	root, err := target.open()
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", path)
	}
	defer root.Close()

	handle, err := root.Open(target.name)
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", path)
	}
	if info, err := handle.Stat(); err != nil || !info.Mode().IsRegular() {
		handle.Close()
		return nil, fail(http.StatusNotFound, "No such file: %s", path)
	}
	return handle, nil
}

// ReadFile is a whole file's contents, for the server's own use of a home.
func (v *Vfs) ReadFile(username, path string) ([]byte, error) {
	target, err := v.locate(path, username, "readfile")
	if err != nil {
		return nil, err
	}
	root, err := target.open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(target.name)
}

// WriteFile replaces a whole file, creating its parents.
func (v *Vfs) WriteFile(username, path string, data []byte) error {
	target, err := v.locate(path, username, "writefile")
	if err != nil {
		return err
	}
	root, err := target.open()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(target.name), 0o755); err != nil {
		return err
	}
	return root.WriteFile(target.name, data, 0o644)
}

func (v *Vfs) Writefile(username, path string, body io.Reader) (any, error) {
	target, err := v.locate(path, username, "writefile")
	if err != nil {
		return nil, err
	}
	root, err := target.open()
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot write: %s", path)
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(target.name), 0o755); err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot write: %s", path)
	}

	handle, err := root.Create(target.name)
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
	target, err := v.locate(path, username, "mkdir")
	if err != nil {
		return nil, err
	}
	root, err := target.open()
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot create: %s", path)
	}
	defer root.Close()
	if _, err := root.Stat(target.name); err == nil {
		if ensure {
			return true, nil
		}
		return nil, fail(http.StatusConflict, "Already exists: %s", path)
	}

	if ensure {
		err = root.MkdirAll(target.name, 0o755)
	} else {
		err = root.Mkdir(target.name, 0o755)
	}
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot create: %s", path)
	}
	return true, nil
}

func (v *Vfs) Unlink(username, path string) (any, error) {
	target, err := v.locate(path, username, "unlink")
	if err != nil {
		return nil, err
	}
	if target.name == "." {
		return nil, fail(http.StatusForbidden, "A mountpoint cannot be deleted")
	}
	root, err := target.open()
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", path)
	}
	defer root.Close()
	// Lstat, so a link whose target is gone can still be removed.
	if _, err := root.Lstat(target.name); err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", path)
	}
	if err := root.RemoveAll(target.name); err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot delete: %s", path)
	}
	return true, nil
}

func (v *Vfs) Touch(username, path string) (any, error) {
	target, err := v.locate(path, username, "touch")
	if err != nil {
		return nil, err
	}
	root, err := target.open()
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot create: %s", path)
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(target.name), 0o755); err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot create: %s", path)
	}

	now := time.Now()
	if err := root.Chtimes(target.name, now, now); err == nil {
		return true, nil
	}
	handle, err := root.OpenFile(target.name, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot create: %s", path)
	}
	handle.Close()
	return true, nil
}

func (v *Vfs) Copy(username, from, to string) (any, error) {
	source, err := v.locate(from, username, "readfile")
	if err != nil {
		return nil, err
	}
	destination, err := v.locate(to, username, "writefile")
	if err != nil {
		return nil, err
	}

	sourceRoot, err := source.open()
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", from)
	}
	defer sourceRoot.Close()
	destinationRoot, err := destination.open()
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot copy: %s", from)
	}
	defer destinationRoot.Close()

	info, err := sourceRoot.Stat(source.name)
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", from)
	}
	if err := refuseSelfCopy(info, destinationRoot, destination.name, to); err != nil {
		return nil, err
	}

	if info.IsDir() {
		err = copyTree(sourceRoot, source.name, destinationRoot, destination.name)
	} else {
		err = copyFile(sourceRoot, source.name, destinationRoot, destination.name, info.Mode())
	}
	if err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot copy: %s", from)
	}
	return true, nil
}

// refuseSelfCopy compares files rather than names, so a link or a second path
// to the same file is caught too. Copying a file onto itself truncates it before
// reading it, and copying a directory under itself walks what it creates.
func refuseSelfCopy(source fs.FileInfo, root *os.Root, name, virtual string) error {
	if existing, err := root.Stat(name); err == nil && os.SameFile(source, existing) {
		return fail(http.StatusBadRequest, "Cannot copy onto itself: %s", virtual)
	}
	if !source.IsDir() {
		return nil
	}
	for parent := path.Dir(name); ; parent = path.Dir(parent) {
		if ancestor, err := root.Stat(parent); err == nil && os.SameFile(source, ancestor) {
			return fail(http.StatusBadRequest, "Cannot copy into itself: %s", virtual)
		}
		if parent == "." {
			return nil
		}
	}
}

func (v *Vfs) Rename(username, from, to string) (any, error) {
	source, err := v.locate(from, username, "unlink")
	if err != nil {
		return nil, err
	}
	destination, err := v.locate(to, username, "writefile")
	if err != nil {
		return nil, err
	}
	if source.name == "." || destination.name == "." {
		return nil, fail(http.StatusForbidden, "A mountpoint cannot be moved")
	}
	// Only home is writable, so both ends are in the same mountpoint.
	root, err := source.open()
	if err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", from)
	}
	defer root.Close()
	if _, err := root.Lstat(source.name); err != nil {
		return nil, fail(http.StatusNotFound, "No such file: %s", from)
	}
	if err := root.Rename(source.name, destination.name); err != nil {
		return nil, fail(http.StatusBadRequest, "Cannot rename: %s", from)
	}
	return true, nil
}

func (v *Vfs) Search(username, virtual, pattern string) (any, error) {
	target, err := v.locate(virtual, username, "search")
	if err != nil {
		return nil, err
	}
	root, err := target.open()
	if err != nil {
		return nil, fail(http.StatusNotFound, "Not a directory: %s", virtual)
	}
	defer root.Close()
	info, err := root.Stat(target.name)
	if err != nil || !info.IsDir() {
		return nil, fail(http.StatusNotFound, "Not a directory: %s", virtual)
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
	err = fs.WalkDir(root.FS(), target.name, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == target.name {
			return nil
		}
		if ok, _ := filepath.Match(glob, strings.ToLower(entry.Name())); ok {
			matches = append(matches, name)
		}
		return nil
	})
	if err != nil {
		return nil, fail(http.StatusNotFound, "Not a directory: %s", virtual)
	}
	sort.Strings(matches)

	base := virtualParent(virtual)
	results := make([]Entry, 0, len(matches))
	for _, match := range matches {
		info, err := root.Stat(match)
		if err != nil {
			continue
		}
		relative := match
		if target.name != "." {
			relative = strings.TrimPrefix(match, target.name+"/")
		}
		results = append(results, describe(base+"/"+relative, filepath.Join(target.dir, match), info))
		if len(results) >= config.SearchLimit {
			break
		}
	}
	return results, nil
}

func copyFile(from *os.Root, source string, to *os.Root, destination string, mode fs.FileMode) error {
	in, err := from.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := to.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func copyTree(from *os.Root, source string, to *os.Root, destination string) error {
	return fs.WalkDir(from.FS(), source, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := destination
		if source == "." && name != "." {
			target = path.Join(destination, name)
		} else if name != source {
			target = path.Join(destination, strings.TrimPrefix(name, source+"/"))
		}
		if entry.IsDir() {
			return to.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(from, name, to, target, info.Mode())
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
