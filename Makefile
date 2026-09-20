MEDIA := docs/media

.PHONY: go serve tui user-demo user-alice demo test conformance diagrams clean-diagrams clean

GO_SOURCES := $(shell find go -name '*.go' 2>/dev/null) go/go.mod go/go.sum

## The server, go/minosd, and the terminal client, go/minos.
go: go/minosd go/minos

go/minosd: $(GO_SOURCES)
	@cd go && go build -o minosd ./cmd/minosd

go/minos: $(GO_SOURCES)
	@cd go && go build -o minos ./cmd/minos

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

## Unit tests, then the wire contract against the built server. -count=1
## because go test cannot see that the binary under test changed.
test: go/minosd
	@cd go && MINOS_CONFORMANCE_CMD=$(CURDIR)/go/minosd go test -count=1 ./...

## The wire contract alone. MINOS_CONFORMANCE_URL points it at a server that is
## already running. See docs/dev/conformance-plan.md.
conformance: go/minosd
	@cd go && MINOS_CONFORMANCE_CMD=$(CURDIR)/go/minosd go test -count=1 ./conformance

## The diagrams in $(MEDIA), from their d2 sources. The only target that
## needs a toolchain other than Go, and nothing else depends on it: the SVGs
## are committed, so a tree without d2 builds, tests and reads the docs.
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
	@rm -rf .run go/minosd go/minos
