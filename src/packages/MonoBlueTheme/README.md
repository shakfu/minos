# Mono Blue

A minimal OS.js theme: flat white surfaces, hard 1px black rules, and blue
reserved for one job -- marking what is selected, focused or active.

Plain CSS with no build step, so `dist/main.css` is the source. `package:discover`
symlinks a package's `dist/` into the site's `dist/themes/`, which is why the
file lives there rather than under a `src/`.

Layout comes from `@osjs/client`, `@osjs/gui`, `@osjs/dialogs` and `@osjs/panels`.
This theme only sets colour and edges. It ships no `main.js`: the base
stylesheet already hides minimized windows, so none of the window-transition
hooks that `@osjs/standard-theme` needs apply here.

The desktop wallpaper is a user setting, not part of a theme. This project sets
the default to a flat white fill in `src/client/config.js`.

Chrome surfaces -- panel, title bars, menus, notifications -- are white with
black text, so window focus is marked by an accent rule under the title bar
rather than by a dark fill. Swap `--mb-chrome-bg` and `--mb-chrome-fg` to go
back to dark chrome.
