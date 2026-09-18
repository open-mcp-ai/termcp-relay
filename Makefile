# termcp-relay — build all platforms, test, clean.
#
#   make        cross-compile every supported platform into dist/
#   make test   run all tests
#   make clean  remove build output
#
# Needs GNU make and a POSIX shell (Git Bash / MSYS on Windows).

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
DIST      := dist

# Stamped into main.version; falls back to "dev" outside a git checkout.
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

.PHONY: all test clean

all:
	@mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out="$(DIST)/termcp-relay-$$os-$$arch"; \
		[ "$$os" = windows ] && out="$$out.exe" || true; \
		echo "  $$out"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" .; \
	done
	@echo "built $(VERSION) for: $(PLATFORMS)"

test:
	go test ./...

clean:
	rm -rf $(DIST) termcp-relay termcp-relay.exe
