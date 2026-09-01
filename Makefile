VENV := .venv
PY := $(VENV)/bin/python
PIP := $(VENV)/bin/pip

.PHONY: install client osjs serve dev test lint clean

install: $(VENV)/bin/pytest client/node_modules

$(VENV)/bin/pytest: server/requirements.txt
	python3 -m venv $(VENV)
	$(PIP) install --upgrade pip
	$(PIP) install -r server/requirements.txt

client/node_modules: client/package.json
	cd client && npm install
	@touch client/node_modules

node_modules: package.json
	npm install
	@touch node_modules

## The minos front end -> dist/index.html
client: client/node_modules
	cd client && npm run build

## The OS.js client kept as a reference -> dist/osjs.html
osjs: node_modules
	npm run build
	npx osjs-cli package:discover

serve: install
	$(PY) -m server.app

## Vite with hot reload, proxying the API to a running `make serve`
dev: client/node_modules
	cd client && npm run dev

lint: node_modules
	npm run stylelint

## Types and unit tests. `make lint` covers the OS.js theme CSS separately,
## because that needs the legacy webpack dependency tree.
test: install
	cd client && npm run typecheck && npm test
	$(VENV)/bin/pytest -q

clean:
	rm -rf $(VENV) node_modules client/node_modules dist
