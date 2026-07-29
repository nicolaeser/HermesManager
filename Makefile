APP := hermes-manager
PACKAGE := ./cmd/hermes-manager
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)

.PHONY: all build vet check format release clean

all: check build

build:
	mkdir -p build
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o build/$(APP) $(PACKAGE)

vet:
	go vet ./...

format:
	gofmt -w cmd internal

check: vet
	test -z "$$(gofmt -l cmd internal)"
	sh -n install.sh scripts/build-release.sh

release:
	./scripts/build-release.sh "$(VERSION)"

clean:
	rm -rf -- build dist
	go clean
