.PHONY: build init clean

GOPATH_BIN := $(shell go env GOPATH)/bin

VERSION   ?= $(shell tag=$$(git describe --tags --exact-match 2>/dev/null || echo "v0.0.0"); rev=$$(git rev-parse --short=8 HEAD); v="$${tag}-$${rev}"; git diff --quiet 2>/dev/null || v="$${v}-dirty"; printf "%s" "$$v")
BUILDDATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := \
	-X github.com/champly/mecha/cmd.Version=$(VERSION) \
	-X github.com/champly/mecha/cmd.BuildDate=$(BUILDDATE)

build:
	go build -ldflags "$(LDFLAGS)" -o $(GOPATH_BIN)/mecha .

init:
	go run . init

run:
	go build -ldflags "$(LDFLAGS)" -o $(GOPATH_BIN)/mecha . && $(GOPATH_BIN)/mecha

clean:
	rm -rf bin/
