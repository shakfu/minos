# minos

A web desktop: a Python backend, and a TypeScript front end built on it.

It started as the [OS.js](https://www.os-js.org) v3 client against a Python
server that reimplements the contract OS.js expects. That client is still here,
unchanged, as a working reference. The front end under `client/` is the
replacement being written against the same backend.

## Run

```
make client   # build the minos front end -> dist/index.html
make osjs     # build the OS.js reference client -> dist/osjs.html
make serve    # Flask on http://127.0.0.1:8000
```

- minos: http://127.0.0.1:8000/
- OS.js reference: http://127.0.0.1:8000/osjs.html

Log in as `demo` / `demo`.

```
make test     # typecheck, vitest, pytest
make lint     # stylelint over the OS.js theme CSS
make dev      # Vite with hot reload, proxying the API to a running `make serve`
```

Both builds write into `dist/`. Vite keeps to `assets/`; the OS.js build writes
flat files at the root plus `apps/`, `themes/`, `icons/`, `sounds/` and
`fonts/`. They only ever contended for `index.html`, so the OS.js page is
emitted as `osjs.html` instead and neither build cleans the directory.

The reference client is a single file rather than a directory because Flask
serves `dist/` as plain static files and only maps `/` to an index, so a bare
`/osjs/` would be a 404.

## Layout

| Path | Contents |
|-|-|
| `client/` | The minos front end. TypeScript, Vite, no UI framework. |
| `server/` | Flask app, VFS, websocket, config. |
| `tests/` | pytest suite against the Flask test client. |
| `src/` | The OS.js reference client: bootstrap, CLI config, local packages. |
| `dist/` | Build output for both clients. Generated. |
| `vfs/` | User home directories. Generated. |

## Front end

`client/` is vanilla TypeScript. A window manager is imperative DOM work --
drag, resize, stacking, focus -- so there is no virtual DOM to fight; the whole
toolchain is Vite and TypeScript.

| Path | Contents |
|-|-|
| `client/src/core/` | API client, session, websocket, path helpers, event bus. |
| `client/src/wm/` | Window and WindowManager. |
| `client/src/ui/` | Panel, menu, dialogs, login. |
| `client/src/apps/` | File manager and file viewer. |

Two things worth knowing:

- `core/api.ts` is the only module that knows the wire format. The tests in
  `client/tests/api.test.ts` pin every URL and body shape, so a drift from the
  frozen server contract fails there rather than in the browser.
- Both clients write to one settings file, so `Session.patchDesktop` merges into
  a `minos/desktop` key and writes the whole object back. A blind overwrite
  would drop the OS.js client's theme and session.

`WindowManager` takes its workspace rect as an injected function rather than
measuring the DOM, which is what makes the geometry testable without layout.

## Theming

Right-click the desktop for **Select Theme** and **Select Wallpaper**. The choice
saves through `POST /settings` and re-applies live, with no reload.

The desktop default is a flat white fill, set in `src/client/config.js`.
Wallpaper is a desktop setting rather than part of a theme -- the desktop writes
it as an inline style, which a theme stylesheet cannot override without
`!important`, and that would break Select Wallpaper.

Two themes are discovered: `StandardTheme` from npm, and `MonoBlueTheme` in
`src/packages/MonoBlueTheme/` -- flat white surfaces, hard 1px black rules, and
blue reserved for what is selected, focused or active. It is plain CSS with no
build step and no `main.js`; see its README for why `dist/` is the source.

`no-descending-specificity` is off in the stylelint config. It flags ordering
across unrelated components -- a scrollbar rule after `:root`, a tab rule after
a menubar rule -- that cannot conflict.

## API

The client talks to these routes. Shapes match `@osjs/server` so the stock
client needs no patching.

| Route | Purpose |
|-|-|
| `GET /ping` | Session keepalive. |
| `POST /login` | Returns a user profile, sets the session cookie. |
| `POST /logout` | Clears the session. |
| `GET,POST /settings` | Per-user settings, stored at `home:/.osjs/settings.json`. |
| `GET /vfs/{capabilities,exists,stat,readdir,readfile}` | Query-parameter reads. |
| `POST /vfs/{writefile,mkdir,unlink,touch,rename,copy,search}` | Writes; `writefile` is multipart. |
| `/*` | Static files from `dist/`. |
| `WS /` | Core websocket. Shares the path with the index route. |

Paths are `<mountpoint>:/<path>`. Two mountpoints are configured: `osjs:/` maps
to `dist/` read-only, `home:/` to `vfs/<username>/`. Every resolved path is
checked against its mountpoint root, so traversal and symlinks cannot escape it.

## WebSocket

One socket carries every named message, as JSON `{name, params}` frames. It
requires a session; an anonymous upgrade is closed with 1008.

Server to client:

- `osjs/core:connected` on connect, carrying the session lifetime.
- `osjs/core:ping` after 30 seconds of client silence.
- `osjs/dist:changed` when a top-level `.js` or `.css` file in `dist/` changes.
  The client hot-reloads matching stylesheets, so `npm run watch` in one
  terminal restyles the running desktop without a refresh.
- `osjs/packages:metadata:changed` when `dist/metadata.json` changes, which
  makes the client re-read the package manifest after `package:discover`.

Client to server: only `osjs/application:socket:message` is accepted. Every
other `osjs*` name is refused, so a page cannot forge core events. Messages
route to handlers registered by package name:

```python
from server.sockets import register_application_handler

register_application_handler("Textpad", lambda conn, respond, args: respond("pong"))
```

Only `dist/` top level is polled, not the package directories under it, which
are symlinks into `node_modules`. Rebuilding a single application therefore
does not push `osjs/packages:package:changed`.

## Scope

This is a demo, not a deployment.

- Credentials are a plaintext dict in `server/config.py`; the session key
  defaults to a fixed string. Both need replacing before the server is exposed.
- The Flask development server handles the websocket. It is threaded, which is
  enough for a demo; anything real wants gunicorn with a gevent worker.
- No VFS change notifications. `osjs/vfs:watch:change` has no consumer in the
  installed client packages, so nothing watches the user filesystem.
- No server-side application handlers ship with the project, so packages that
  need a backend (`metadata.json` `server` field) will not work until one is
  registered.
