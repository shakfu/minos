VENV := .venv
PY := $(VENV)/bin/python

# Debian's nodejs package ships without npm; corepack (bundled with node)
# provides a working one, so fall back to it when npm is not on PATH.
NPM := $(shell command -v npm >/dev/null 2>&1 && echo npm || echo 'corepack npm@11')

.PHONY: install client go serve serve-go dev tui test conformance conformance-go clean

install: $(VENV)/bin/pytest client/node_modules

$(VENV)/bin/pytest: pyproject.toml
	@command -v uv >/dev/null 2>&1 || { \
	  echo "uv is required: https://docs.astral.sh/uv/getting-started/installation/"; \
	  exit 1; }
	uv venv --allow-existing $(VENV)
	uv pip install --python $(PY) -r pyproject.toml --group dev
	@touch $@

client/node_modules: client/package.json
	cd client && $(NPM) install
	@touch client/node_modules

## The minos front end -> dist/index.html
client: client/node_modules
	cd client && $(NPM) run build

## The server -> go/minosd. This is the implementation; server/ is the
## specification it was written from. See docs/wire-contract.md.
go: go/minosd

go/minosd: $(shell find go -name '*.go' 2>/dev/null) go/go.mod
	cd go && go build -o minosd ./cmd/minosd

## The Python server: the executable specification, not the deployable one.
serve: install
	$(PY) -m server.app

## The compiled server, on the same port and the same contract.
serve-go: go
	./go/minosd

## The terminal client, against a running `make serve`.
## MINOS_SERVER overrides the address; --user skips the username prompt.
tui: install
	$(PY) -m tui

## Vite with hot reload, proxying the API to a running `make serve`.
## --open launches a browser; BROWSER=none skips it.
dev: client/node_modules
	cd client && $(NPM) run dev -- --open

## Types and unit tests.
test: install
	cd client && $(NPM) run typecheck && $(NPM) test
	$(VENV)/bin/pytest -q

## The wire contract alone, against any implementation of it.
## MINOS_CONFORMANCE_CMD launches a different server; MINOS_CONFORMANCE_URL
## points at one that is already running. See docs/dev/conformance-plan.md.
conformance: install
	$(VENV)/bin/pytest tests/conformance -q

## The same suite against the compiled server.
conformance-go: install go
	MINOS_CONFORMANCE_CMD=$(CURDIR)/go/minosd $(VENV)/bin/pytest tests/conformance -q

clean:
	rm -rf $(VENV) client/node_modules dist .run go/minosd
