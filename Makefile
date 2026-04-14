APP_NAME := lanby-relay
BIN_DIR  := bin
CMD_DIR  := ./cmd/relay
GOLANGCI_LINT_VERSION ?= v2.11.4

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
VERSION_PKG  = github.com/lanby-dev/lanby-relay/internal/relay
LDFLAGS      = -X $(VERSION_PKG).Version=$(VERSION)

.PHONY: help build run test vet lint fmt fmt-check tidy clean check ci install dev

## Show this help
help:
	@echo "Lanby Relay"
	@echo ""
	@echo "Build & Run:"
	@echo "  make build      Build binary to bin/relay"
	@echo "  make run        Run relay from source"
	@echo "  make install    Install relay binary to GOPATH/bin"
	@echo "  make dev        Run via helper script with local defaults"
	@echo ""
	@echo "Quality:"
	@echo "  make test       Run Go tests"
	@echo "  make vet        Run go vet"
	@echo "  make lint       Run golangci-lint"
	@echo "  make fmt        Run gofmt on all Go files"
	@echo "  make fmt-check  Fail if files are not gofmt-formatted"
	@echo "  make tidy       Run go mod tidy"
	@echo "  make check      Run fmt-check + vet + test"
	@echo "  make ci         Run check + lint"
	@echo ""
	@echo "Housekeeping:"
	@echo "  make clean      Remove build artifacts"

## Build binary to bin/relay
build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/relay $(CMD_DIR)

## Run the relay from source
run:
	go run -ldflags "$(LDFLAGS)" $(CMD_DIR)

## Install binary into GOPATH/bin
install:
	go install -ldflags "$(LDFLAGS)" $(CMD_DIR)

## Run all Go tests
test:
	go test ./...

## Run go vet checks
vet:
	go vet ./...

## Run golangci-lint (via `go run` so the binary matches this repo's Go toolchain)
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

## Format Go files
fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

## Verify all Go files are gofmt-formatted
fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))" || (echo "Go files need formatting. Run: make fmt" && exit 1)

## Tidy go.mod and go.sum
tidy:
	go mod tidy

## Remove generated artifacts
clean:
	rm -rf $(BIN_DIR)

## Fast local verification
check: fmt-check vet test

## CI-equivalent verification
ci: check lint

## Dev helper script
dev:
	@./scripts/dev-run.sh
