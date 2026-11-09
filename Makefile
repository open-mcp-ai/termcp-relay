# termcp-relay — build, test, clean.
#
#   make        build dist/termcp-relay-$(GOOS)-$(GOARCH) for the host platform
#   make test   vet + run all tests (same commands CI runs)
#   make clean  remove build output
#
# Other targets: make GOOS=linux GOARCH=arm64  (defaults come from `go env`)
# Needs GNU make and a POSIX shell (Git Bash / MSYS on Windows).

.DEFAULT_GOAL := all

DIST      := dist
GOOS      ?= $(shell go env GOOS)
GOARCH    ?= $(shell go env GOARCH)
BIN       := termcp-relay-$(GOOS)-$(GOARCH)
ifeq ($(GOOS),windows)
BIN       := $(BIN).exe
endif

# Stamped into main.version; falls back to "dev" outside a git checkout.
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

.PHONY: all test clean

all:
	@mkdir -p $(DIST)
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 \
		go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN) .
	@echo "built $(DIST)/$(BIN) ($(VERSION))"

test:
	go vet ./...
	go test ./... -count=1 -timeout 120s

clean:
	rm -rf $(DIST) termcp-relay termcp-relay.exe
