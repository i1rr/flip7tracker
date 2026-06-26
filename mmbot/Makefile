# flip7bot (mmbot) — Go/Mattermost bot build targets.
# Go 1.24+ is required by the Mattermost client module; the project pins
# toolchain go1.26.4 in go.mod. Set GOTOOLCHAIN=auto so the correct toolchain
# is fetched automatically.

GO ?= go
PKG ?= ./...
export GOTOOLCHAIN ?= auto
export CGO_ENABLED ?= 0

.PHONY: all build test vet fmt tidy check run clean

all: check

build:
	CGO_ENABLED=0 $(GO) build $(PKG)

test:
	$(GO) test $(PKG)

vet:
	$(GO) vet $(PKG)

# fmt prints any files that are not gofmt-clean. A clean tree prints nothing.
fmt:
	@gofmt -l .

# check is the full gate: build + vet + tests + a clean gofmt.
check: build vet test
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt needs to run on:"; echo "$$out"; exit 1; \
	fi

tidy:
	$(GO) mod tidy

run: build
	$(GO) run ./cmd/mmbot

clean:
	rm -f mmbot
	rm -rf bin
