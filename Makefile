BIN_DIR ?= $(HOME)/.config/rex/rex8s/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GO_DIRS := cmd internal rex
# the linter CI installs; `make lint` uses whichever golangci-lint is on PATH
GOLANGCI_LINT_VERSION := v2.11.4

.PHONY: build check fmt vet test race lint raycast install

build:
	go build -ldflags "$(LDFLAGS)" -o bin/rex8s ./cmd/rex8s
	go build -ldflags "$(LDFLAGS)" -o bin/rex8s-guard ./cmd/rex8s-guard

# what to run before a commit: format, vet, test
check: fmt vet test

fmt:
	gofmt -w $(GO_DIRS)

vet:
	go vet ./...
	GOOS=darwin GOARCH=arm64 go vet ./...
	GOOS=linux GOARCH=amd64 go vet ./...

# -short skips the end-to-end test (it builds the binary)
test:
	go test ./...

race:
	go test -race ./...

# golangci-lint (.golangci.yml), plus the gofmt check CI runs
lint:
	@test -z "$$(gofmt -l $(GO_DIRS))" || { echo "gofmt needed:"; gofmt -l $(GO_DIRS); exit 1; }
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not found: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)"; exit 1; }
	golangci-lint run ./...

# type-check the Raycast extension
raycast:
	cd raycast && { test -d node_modules || npm ci --no-audit --no-fund; } && npx tsc --noEmit -p .

# install the binaries, the k* commands, rex8s.lua and require("rex8s"),
# then reload Rex (rex8s setup does all but the copy); run `make check` first
install: build
	mkdir -p $(BIN_DIR)
	install -m 0755 bin/rex8s bin/rex8s-guard $(BIN_DIR)/
	$(BIN_DIR)/rex8s setup
