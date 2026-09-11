.PHONY: go serve tui demo test conformance clean

GO_SOURCES := $(shell find go -name '*.go' 2>/dev/null) go/go.mod go/go.sum

## The server, go/minosd, and the terminal client, go/minos.
go: go/minosd go/minos

go/minosd: $(GO_SOURCES)
	cd go && go build -o minosd ./cmd/minosd

go/minos: $(GO_SOURCES)
	cd go && go build -o minos ./cmd/minos

## The server, on http://127.0.0.1:8000.
serve: go/minosd
	./go/minosd

## The terminal client, against a running `make serve`.
## MINOS_SERVER overrides the address; -user skips the username prompt.
tui: go/minos
	./go/minos

## A narrated run of the channel audience rule against a server of its own.
demo: go/minosd
	cd go && MINOS_CONFORMANCE_CMD=$(CURDIR)/go/minosd go run ./cmd/demo

## Unit tests, then the wire contract against the built server. -count=1
## because go test cannot see that the binary under test changed.
test: go/minosd
	cd go && MINOS_CONFORMANCE_CMD=$(CURDIR)/go/minosd go test -count=1 ./...

## The wire contract alone. MINOS_CONFORMANCE_URL points it at a server that is
## already running. See docs/dev/conformance-plan.md.
conformance: go/minosd
	cd go && MINOS_CONFORMANCE_CMD=$(CURDIR)/go/minosd go test -count=1 ./conformance

clean:
	rm -rf .run go/minosd go/minos
