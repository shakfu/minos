VENV := .venv
PY := $(VENV)/bin/python

# Debian's nodejs package ships without npm; corepack (bundled with node)
# provides a working one, so fall back to it when npm is not on PATH.
NPM := $(shell command -v npm >/dev/null 2>&1 && echo npm || echo 'corepack npm@11')

.PHONY: install client serve dev test clean

install: $(VENV)/bin/pytest client/node_modules

$(VENV)/bin/pytest: pyproject.toml
	@command -v uv >/dev/null 2>&1 || { \
	  echo "uv is required: https://docs.astral.sh/uv/getting-started/installation/"; \
	  exit 1; }
	uv venv $(VENV)
	uv pip install --python $(PY) -r pyproject.toml --group dev
	@touch $@

client/node_modules: client/package.json
	cd client && $(NPM) install
	@touch client/node_modules

## The minos front end -> dist/index.html
client: client/node_modules
	cd client && $(NPM) run build

serve: install
	$(PY) -m server.app

## Vite with hot reload, proxying the API to a running `make serve`.
## --open launches a browser; BROWSER=none skips it.
dev: client/node_modules
	cd client && $(NPM) run dev -- --open

## Types and unit tests.
test: install
	cd client && $(NPM) run typecheck && $(NPM) test
	$(VENV)/bin/pytest -q

clean:
	rm -rf $(VENV) client/node_modules dist .run
