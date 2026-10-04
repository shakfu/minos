MEDIA := docs/media

.PHONY: go serve tui user-demo user-alice demo host-run test conformance cover container diagrams clean-diagrams clean

GO_SOURCES := $(shell find go -name '*.go' 2>/dev/null) go/go.mod go/go.sum

## The server, the terminal client, and one run's pair: the host-side broker
## go/minosb and the shim go/minosa that the container carries.
go: go/minosd go/minos go/minosb go/minosa

go/minosd: $(GO_SOURCES)
	@cd go && go build -o minosd ./cmd/minosd

go/minos: $(GO_SOURCES)
	@cd go && go build -o minos ./cmd/minos

go/minosb: $(GO_SOURCES)
	@cd go && go build -o minosb ./cmd/minosb

## Static, because it runs in a container that carries nothing else.
go/minosa: $(GO_SOURCES)
	@cd go && CGO_ENABLED=0 go build -o minosa ./cmd/minosa

## The server, on http://127.0.0.1:8000.
serve: go/minosd
	@./go/minosd

## The terminal client, against a running `make serve`.
## MINOS_SERVER overrides the address; -user skips the username prompt.
tui: go/minos
	@./go/minos

user-demo: go/minos
	@./go/minos -user demo -password demo

user-alice: go/minos
	@./go/minos -user alice -password alice

## A narrated run of the channel audience rule against a server of its own.
demo: go/minosd
	@cd go && MINOS_CONFORMANCE_CMD=$(CURDIR)/go/minosd go run ./cmd/demo

## One real claude under go/minosb on the host, no container: relay, held
## messages, both interrupt lanes, the shim. Spends turns on your account.
host-run: go
	@cd go && go run ./cmd/hostrun

## Unit tests, then the wire contract against the built server. -count=1
## because go test cannot see that the binary under test changed. -race covers
## the test binaries, not go/minosd itself; it costs about 10 s of 50.
test: go/minosd
	@cd go && MINOS_CONFORMANCE_CMD=$(CURDIR)/go/minosd go test -race -count=1 ./...

## The wire contract alone. MINOS_CONFORMANCE_URL points it at a server that is
## already running. See docs/dev/conformance-plan.md.
conformance: go/minosd
	@cd go && MINOS_CONFORMANCE_CMD=$(CURDIR)/go/minosd go test -count=1 ./conformance

## The shim in a real container, through a bind-mounted socket. Needs docker;
## builds a scratch image holding only minosa, and removes it afterwards.
container:
	@cd go && MINOS_CONTAINER=1 go test -count=1 -run InsideAContainer ./internal/broker

## The server's coverage under the wire contract. GOCOVERDIR must be absolute:
## the server runs in go/conformance, where a relative one names no directory,
## and the runtime then writes nothing and says nothing.
COVER := $(CURDIR)/.cache/cover
cover:
	@rm -rf $(COVER) && mkdir -p $(COVER)
	@cd go && go build -cover -o $(CURDIR)/.cache/minosd-cover ./cmd/minosd
	@cd go && GOCOVERDIR=$(COVER) MINOS_CONFORMANCE_CMD=$(CURDIR)/.cache/minosd-cover go test -count=1 ./conformance
	@cd go && go tool covdata percent -i=$(COVER)

## The diagrams in $(MEDIA), from their d2 sources. The only target that
## needs a toolchain other than Go, and nothing else depends on it. The
## renderings are not committed; the docs link the sources.
diagrams: $(MEDIA)/architecture.svg $(MEDIA)/network.svg $(MEDIA)/architecture.pdf $(MEDIA)/network.pdf

clean-diagrams:
	@rm -f $(MEDIA)/architecture.svg $(MEDIA)/architecture.pdf
	@rm -f $(MEDIA)/network.svg $(MEDIA)/network.pdf

$(MEDIA)/architecture.svg: $(MEDIA)/architecture.d2
	@d2 --layout=tala $< $@

$(MEDIA)/network.svg: $(MEDIA)/network.d2
	@d2 --layout=tala $< $@

$(MEDIA)/architecture.pdf: $(MEDIA)/architecture.d2
	@d2 --layout=tala $< $@

$(MEDIA)/network.pdf: $(MEDIA)/network.d2
	@d2 --layout=tala $< $@


clean:
	@rm -rf .run go/minosd go/minos go/minosb go/minosa
