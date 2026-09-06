VENV := .venv
PY := $(VENV)/bin/python

.PHONY: install go serve serve-go tui test conformance conformance-go clean

install: $(VENV)/bin/pytest

$(VENV)/bin/pytest: pyproject.toml
	@command -v uv >/dev/null 2>&1 || { \
	  echo "uv is required: https://docs.astral.sh/uv/getting-started/installation/"; \
	  exit 1; }
	uv venv --allow-existing $(VENV)
	uv pip install --python $(PY) -r pyproject.toml --group dev
	@touch $@

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

## The unit tests.
test: install
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
	rm -rf $(VENV) .run go/minosd
