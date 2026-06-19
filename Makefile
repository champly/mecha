.PHONY: build init clean proto proto-update test run

GOPATH_BIN := $(shell go env GOPATH)/bin

VERSION   ?= $(shell tag=$$(git describe --tags --exact-match 2>/dev/null || echo "v0.0.0"); rev=$$(git rev-parse --short=8 HEAD); v="$${tag}-$${rev}"; git diff --quiet 2>/dev/null || v="$${v}-dirty"; printf "%s" "$$v")
BUILDDATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := \
	-X github.com/champly/mecha/cmd.Version=$(VERSION) \
	-X github.com/champly/mecha/cmd.BuildDate=$(BUILDDATE)

PROTO_DIR := pkg/term/iterm2/api
PROTO_FILE := $(PROTO_DIR)/api.proto
PROTO_OUT := pkg/term/iterm2/api

build:
	go build -ldflags "$(LDFLAGS)" -o $(GOPATH_BIN)/mecha .

init:
	go run . init

run:
	go build -ldflags "$(LDFLAGS)" -o $(GOPATH_BIN)/mecha . && $(GOPATH_BIN)/mecha

proto:
	protoc --go_out=$(PROTO_OUT) --go_opt=paths=source_relative -I $(PROTO_DIR) $(PROTO_FILE)

proto-update:
	curl -sL "https://gitlab.com/gnachman/iterm2/-/raw/master/proto/api.proto" -o $(PROTO_FILE)
	$(MAKE) proto

test:
	go test ./pkg/...

clean:
	rm -rf bin/ $(PROTO_OUT)/*.pb.go
