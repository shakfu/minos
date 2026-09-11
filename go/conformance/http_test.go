package conformance

// The HTTP half of the contract: routes, sessions, settings and the VFS. It is
// what @osjs/client asks for, so this is a record of what may not change.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

var cspDirectives = []string{
	"default-src 'self'",
	"img-src 'self' data: blob:",
	"object-src 'none'",
	"base-uri 'self'",
	"form-action 'self'",
	"frame-ancestors 'none'",
}

func readFile(h *Http, params url.Values) *Response {
	return h.Call("GET", "/vfs/readfile", nil, params, nil, nil)
}

func basename(path string) string { return path[strings.LastIndex(path, "/")+1:] }

// -- the session --------------------------------------------------------------

func TestPingAnswersWithoutASession(t *testing.T) {
	response := anonymous(t).Ping()
	same(t, response.Status, http.StatusOK)
	same(t, response.Text(), "ok")
}

func TestLoginReturnsTheFrozenProfileShape(t *testing.T) {
	response := anonymous(t).Login("alice", "alice")
	same(t, response.Status, http.StatusOK)
	same(t, response.JSON(), Obj{"id": "alice", "username": "alice", "name": "alice", "groups": []any{}})
}

// groups is the only field the role travels on.
func TestAnAdministratorIsNamedOnTheProfile(t *testing.T) {
	same(t, obj(anonymous(t).Login("demo", "demo").JSON())["groups"], []string{"admin"})
}

func TestAWrongPasswordIsRefusedWithoutSayingWhichHalf(t *testing.T) {
	response := anonymous(t).Login("alice", "bob")
	same(t, response.Status, http.StatusForbidden)
	same(t, response.Refusal(), "Invalid login or permission denied")
}

func TestAnUnknownUserGetsTheSameAnswer(t *testing.T) {
	response := anonymous(t).Login("nobody", "nobody")
	same(t, response.Status, http.StatusForbidden)
	same(t, response.Refusal(), "Invalid login or permission denied")
}

func TestLogoutEndsTheSession(t *testing.T) {
	h := session(t, "alice")
	same(t, h.Logout().JSON(), Obj{})
	same(t, h.Settings().Status, http.StatusForbidden)
}

func TestEveryOtherRouteNeedsASession(t *testing.T) {
	for _, route := range [][2]string{{"GET", "/settings"}, {"POST", "/settings"}, {"GET", "/vfs/readdir"}} {
		t.Run(route[0]+route[1], func(t *testing.T) {
			response := anonymous(t).Call(route[0], route[1], nil, nil, nil, nil)
			same(t, response.Status, http.StatusForbidden)
			same(t, response.Refusal(), "Not authenticated")
		})
	}
}

// -- headers ------------------------------------------------------------------

func TestSecurityHeadersAreOnEveryResponse(t *testing.T) {
	header := anonymous(t).Ping().Header
	same(t, header.Get("X-Content-Type-Options"), "nosniff")
	same(t, header.Get("X-Frame-Options"), "DENY")

	policy := header.Get("Content-Security-Policy")
	for _, directive := range cspDirectives {
		truth(t, strings.Contains(policy, directive), "%q lacks %q", policy, directive)
	}

	// The socket is ws:// while the page is http://, so 'self' does not cover it.
	host := strings.TrimPrefix(shared.Base, "http://")
	want := fmt.Sprintf("connect-src 'self' ws://%s wss://%s", host, host)
	truth(t, strings.Contains(policy, want), "%q lacks %q", policy, want)
}

// -- settings -----------------------------------------------------------------

func TestSettingsStartEmptyAndRoundTrip(t *testing.T) {
	h := session(t, "bob")
	same(t, h.Settings().JSON(), Obj{})

	same(t, h.PutSettings(Obj{"osjs/desktop": Obj{"theme": "dark"}}).JSON(), true)
	same(t, h.Settings().JSON(), Obj{"osjs/desktop": Obj{"theme": "dark"}})
}

// A client merges; the server stores what it is given.
func TestSettingsAreReplacedWholesale(t *testing.T) {
	h := session(t, "bob")
	h.PutSettings(Obj{"a": 1, "b": 2})
	h.PutSettings(Obj{"a": 9})
	same(t, h.Settings().JSON(), Obj{"a": 9})
}

func TestSettingsRefuseAnythingButAnObject(t *testing.T) {
	response := session(t, "bob").Call("POST", "/settings", []any{1, 2}, nil, nil, nil)
	same(t, response.Status, http.StatusBadRequest)
	same(t, response.Refusal(), "Settings must be a JSON object")
}

func TestSettingsArePerUser(t *testing.T) {
	session(t, "alice").PutSettings(Obj{"who": "alice"})
	session(t, "bob").PutSettings(Obj{"who": "bob"})
	same(t, session(t, "alice").Settings().JSON(), Obj{"who": "alice"})
}

// -- the VFS ------------------------------------------------------------------

func TestCapabilitiesReportsTheFrozenAnswer(t *testing.T) {
	response := session(t, "alice").Vfs("capabilities", Obj{"path": "home:/"})
	same(t, response.JSON(), Obj{"sort": false, "pagination": false})
}

func TestAWriteReadAndDeleteRoundTrip(t *testing.T) {
	h := session(t, "alice")
	path := "home:/" + unique("file") + ".txt"

	same(t, h.Upload(path, []byte("hello")).JSON(), 5)
	same(t, h.Vfs("exists", Obj{"path": path}).JSON(), true)
	same(t, readFile(h, url.Values{"path": {path}}).Text(), "hello")
	same(t, h.Vfs("unlink", Obj{"path": path}).JSON(), true)
	same(t, h.Vfs("exists", Obj{"path": path}).JSON(), false)
}

func TestStatReturnsADescriptor(t *testing.T) {
	h := session(t, "alice")
	path := "home:/" + unique("note") + ".txt"
	h.Upload(path, []byte("abc"))

	descriptor := obj(h.Vfs("stat", Obj{"path": path}).JSON())
	same(t, descriptor["isFile"], true)
	same(t, descriptor["isDirectory"], false)
	same(t, descriptor["mime"], "text/plain")
	same(t, descriptor["size"], 3)
	same(t, descriptor["path"], path)
	same(t, descriptor["filename"], basename(path))
	keySet(t, obj(descriptor["stat"]),
		"size", "mode", "atime", "mtime", "ctime", "atimeMs", "mtimeMs", "ctimeMs")
	mtime := str(obj(descriptor["stat"])["mtime"])
	truth(t, strings.HasSuffix(mtime, "+00:00"), "mtime %q is not UTC", mtime)
}

func TestStatOfAMissingFileIsNotFound(t *testing.T) {
	same(t, session(t, "alice").Vfs("stat", Obj{"path": "home:/absent"}).Status, http.StatusNotFound)
}

func TestReaddirListsADirectoryWithFullVirtualPaths(t *testing.T) {
	h := session(t, "alice")
	directory := unique("dir")
	h.Vfs("mkdir", Obj{"path": "home:/" + directory})
	h.Upload("home:/"+directory+"/one.txt", []byte("1"))

	entries := h.Vfs("readdir", Obj{"path": "home:/" + directory}).JSON()
	same(t, pluck(entries, "path"), []string{"home:/" + directory + "/one.txt"})
}

func TestReaddirSortsByLowercasedName(t *testing.T) {
	h := session(t, "alice")
	directory := unique("sorted")
	h.Vfs("mkdir", Obj{"path": "home:/" + directory})
	for _, name := range []string{"b.txt", "A.txt", "c.txt"} {
		h.Upload("home:/"+directory+"/"+name, []byte("x"))
	}

	entries := h.Vfs("readdir", Obj{"path": "home:/" + directory}).JSON()
	same(t, pluck(entries, "filename"), []string{"A.txt", "b.txt", "c.txt"})
}

func TestADirectoryHasNoMime(t *testing.T) {
	h := session(t, "alice")
	directory := unique("dir")
	h.Vfs("mkdir", Obj{"path": "home:/" + directory})
	null(t, obj(h.Vfs("stat", Obj{"path": "home:/" + directory}).JSON()), "mime")
}

func TestMkdirRefusesACollisionUnlessEnsureIsSet(t *testing.T) {
	h := session(t, "alice")
	path := "home:/" + unique("dir")
	same(t, h.Vfs("mkdir", Obj{"path": path}).JSON(), true)

	same(t, h.Vfs("mkdir", Obj{"path": path}).Status, http.StatusConflict)
	same(t, h.Vfs("mkdir", Obj{"path": path, "options": Obj{"ensure": true}}).JSON(), true)
}

func TestCopyAndRename(t *testing.T) {
	h := session(t, "alice")
	stem := unique("move")
	h.Upload("home:/"+stem+"-a.txt", []byte("data"))

	same(t, h.Vfs("copy", Obj{"from": "home:/" + stem + "-a.txt", "to": "home:/" + stem + "-b.txt"}).JSON(), true)
	same(t, h.Vfs("rename", Obj{"from": "home:/" + stem + "-b.txt", "to": "home:/" + stem + "-c.txt"}).JSON(), true)
	same(t, h.Vfs("exists", Obj{"path": "home:/" + stem + "-b.txt"}).JSON(), false)
	same(t, readFile(h, url.Values{"path": {"home:/" + stem + "-c.txt"}}).Text(), "data")
}

func TestSearchWrapsABarePatternInWildcards(t *testing.T) {
	h := session(t, "alice")
	directory := unique("search")
	h.Vfs("mkdir", Obj{"path": "home:/" + directory})
	h.Upload("home:/"+directory+"/report-final.txt", []byte("x"))
	h.Upload("home:/"+directory+"/other.txt", []byte("x"))

	found := h.Vfs("search", Obj{"root": "home:/" + directory, "pattern": "report"})
	same(t, pluck(found.JSON(), "filename"), []string{"report-final.txt"})
}

func TestSearchIsCaseInsensitive(t *testing.T) {
	h := session(t, "alice")
	directory := unique("search")
	h.Vfs("mkdir", Obj{"path": "home:/" + directory})
	h.Upload("home:/"+directory+"/README.md", []byte("x"))

	found := h.Vfs("search", Obj{"root": "home:/" + directory, "pattern": "readme"})
	same(t, pluck(found.JSON(), "filename"), []string{"README.md"})
}

// It arrives as JSON text on a GET, and a bad one is not worth an error.
func TestAnUnparseableOptionsFieldMeansNoOptions(t *testing.T) {
	h := session(t, "alice")
	params := url.Values{"path": {"home:/" + unique("dir")}, "options": {"{oops"}}
	same(t, h.Call("GET", "/vfs/exists", nil, params, nil, nil).Status, http.StatusOK)
}

func TestAnUnknownVfsMethodIsNotFound(t *testing.T) {
	response := session(t, "alice").Vfs("frobnicate", Obj{"path": "home:/"})
	same(t, response.Status, http.StatusNotFound)
	same(t, response.Refusal(), "No such VFS method: frobnicate")
}

// -- mountpoints --------------------------------------------------------------

func TestAPathMayNotEscapeItsMountpoint(t *testing.T) {
	response := session(t, "alice").Vfs("readdir", Obj{"path": "home:/../../etc"})
	same(t, response.Status, http.StatusForbidden)
	same(t, response.Refusal(), "Path escapes its mountpoint")
}

func TestTheBuildMountpointIsReadOnly(t *testing.T) {
	response := session(t, "alice").Vfs("mkdir", Obj{"path": "osjs:/anything"})
	same(t, response.Status, http.StatusForbidden)
	truth(t, strings.Contains(response.Refusal(), "read-only"), "refusal was %q", response.Refusal())
}

func TestTheBuildMountpointIsReadable(t *testing.T) {
	same(t, session(t, "alice").Vfs("exists", Obj{"path": "osjs:/index.html"}).JSON(), true)
}

func TestAnUnknownMountpointIsNotFound(t *testing.T) {
	response := session(t, "alice").Vfs("readdir", Obj{"path": "nowhere:/"})
	same(t, response.Status, http.StatusNotFound)
	same(t, response.Refusal(), "No such mountpoint: nowhere")
}

func TestAMalformedPathIsABadRequest(t *testing.T) {
	same(t, session(t, "alice").Vfs("readdir", Obj{"path": "no-colon"}).Status, http.StatusBadRequest)
}

// alice's home is not bob's, and neither can name the other's.
func TestHomesAreSeparate(t *testing.T) {
	name := unique("private")
	session(t, "alice").Upload("home:/"+name+".txt", []byte("secret"))
	same(t, session(t, "bob").Vfs("exists", Obj{"path": "home:/" + name + ".txt"}).JSON(), false)
}

// -- disposition --------------------------------------------------------------

// SVG is excluded deliberately: an image that carries script.
func TestOnlyTypesThatCannotScriptAreServedInline(t *testing.T) {
	cases := []struct {
		name     string
		contents string
		inline   bool
	}{
		{"plain.txt", "text", true},
		{"pixel.png", "\x89PNG\r\n\x1a\n", true},
		{"page.html", "<b>x</b>", false},
		{"vector.svg", "<svg/>", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := session(t, "alice")
			path := "home:/" + unique("disp") + "-" + c.name
			h.Upload(path, []byte(c.contents))

			disposition := readFile(h, url.Values{"path": {path}}).Header.Get("Content-Disposition")
			same(t, strings.Contains(disposition, "attachment"), !c.inline)
		})
	}
}

func TestTheDownloadOptionForcesAnAttachment(t *testing.T) {
	h := session(t, "alice")
	path := "home:/" + unique("disp") + ".txt"
	h.Upload(path, []byte("text"))

	response := readFile(h, url.Values{"path": {path}, "options": {`{"download": true}`}})
	disposition := response.Header.Get("Content-Disposition")
	truth(t, strings.Contains(disposition, "attachment"), "disposition was %q", disposition)
}

func TestTheMimeIsReportedWhateverTheDisposition(t *testing.T) {
	h := session(t, "alice")
	path := "home:/" + unique("disp") + ".svg"
	h.Upload(path, []byte("<svg/>"))

	kind := readFile(h, url.Values{"path": {path}}).Header.Get("Content-Type")
	truth(t, strings.HasPrefix(kind, "image/svg+xml"), "Content-Type was %q", kind)
}

// -- the index ----------------------------------------------------------------

func TestTheIndexIsServedFromTheBuildDirectory(t *testing.T) {
	response := anonymous(t).Call("GET", "/", nil, nil, nil, nil)
	same(t, response.Status, http.StatusOK)
	truth(t, strings.Contains(response.Text(), "minos"), "index was %q", response.Text())
}
