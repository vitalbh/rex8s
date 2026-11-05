BIN_DIR ?= $(HOME)/.config/rex/rex8s/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test install
build:
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/rex8s ./cmd/rex8s
	go build -ldflags "-s -w" -o bin/rex8s-guard ./cmd/rex8s-guard

test:
	go vet ./... && go test ./...

# install the binaries and (re)generate the wrappers that panes put on PATH
install: test build
	mkdir -p $(BIN_DIR)
	install -m 0755 bin/rex8s bin/rex8s-guard $(BIN_DIR)/
	./scripts/wrappers.sh $(BIN_DIR)
