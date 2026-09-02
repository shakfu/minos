# Changelog

Notable changes to minos. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Nothing is released
yet, so everything so far sits under Unreleased.

## [Unreleased]

The OS.js reference client is gone. `client/` is now the only front end, and
the server keeps the OS.js wire format without carrying any OS.js code.

### Removed

- The OS.js v3 reference client and the whole `src/` tree: the bootstrap and
  config under `src/client/`, the CLI config under `src/cli/`, and the
  `MonoBlueTheme` package. It built to `dist/osjs.html`, which is now a 404.
- Its build: `webpack.config.js`, the root `package.json` and
  `package-lock.json`, and with them every `@osjs/*` dependency. The only
  JavaScript toolchain left is the one under `client/`.
- The `osjs` and `lint` make targets. `lint` ran stylelint over the OS.js theme
  CSS and had nothing else to cover; there is no stylelint config any more.
- `DistWatcher` and its polling thread. It pushed `osjs/dist:changed` and
  `osjs/packages:metadata:changed`, both of which had lost their consumer: the
  manifest was written by `osjs-cli package:discover`, and the new client logs
  any frame it does not handle. Vite emits fingerprinted files under
  `dist/assets/` while the watcher only scanned the top level, so it could no
  longer fire at all. Hot reload during development is Vite's, via `make dev`.
- `WATCH_DIST` and `WATCH_INTERVAL` from `server/config.py`, so the
  `MINOS_WATCH_DIST` environment variable is no longer read.
- `tests/test_theme_package.py` (guards on the MonoBlueTheme package contract)
  and `tests/test_watcher.py`.

### Added

- `make dev` opens a browser at the Vite URL, through Vite's own `--open`.
  `BROWSER=none make dev` starts the server without one. `npm run dev` inside
  `client/` is unchanged and still opens nothing.

### Changed

- `make install` builds the Python venv with [uv](https://docs.astral.sh/uv/)
  rather than `python3 -m venv` plus pip. The stdlib path fails outright on
  distributions that ship Python without `ensurepip`.
- The Makefile falls back to corepack's npm where the node install has none,
  so `make dev` and `make client` work on a Debian `nodejs` package.
- `client/vite.config.ts` sets `emptyOutDir: true`. It was off only because the
  OS.js build wrote into the same `dist/`; `make client` now clears the
  directory first.
- The websocket broadcast tests use `osjs/vfs:watch:change` as their sample
  frame. They exercise `Registry.broadcast`, and the name they used before is
  no longer sent by anything.

### Unchanged

The server still speaks the OS.js contract, and none of it moved: the
`@osjs/server` route shapes, the `osjs/*` websocket message names, the read-only
`osjs:` mountpoint over `dist/`, and settings at `home:/.osjs/settings.json`.
`Session.patchDesktop` still merges rather than overwrites, because homes
created under the old client carry `osjs/*` keys.
