.PHONY: build init clean proto proto-update test run

GOPATH_BIN := $(shell go env GOPATH)/bin

VERSION   ?= $(shell tag=$$(git describe --tags --exact-match 2>/dev/null || echo "v0.0.0"); rev=$$(git rev-parse --short=8 HEAD); v="$${tag}-$${rev}"; git diff --quiet 2>/dev/null || v="$${v}-dirty"; printf "%s" "$$v")
BUILDDATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := \
	-X github.com/champly/mecha/cmd.Version=$(VERSION) \
	-X github.com/champly/mecha/cmd.BuildDate=$(BUILDDATE)

ITERM_PROTO_DIR  := pkg/term/iterm2/api
ITERM_PROTO_FILE := $(ITERM_PROTO_DIR)/api.proto
ITERM_PROTO_OUT  := pkg/term/iterm2/api

API_PROTO_FILE := pkg/api/api.proto

build:
	go build -ldflags "$(LDFLAGS)" -o $(GOPATH_BIN)/mecha .

init:
	go run . init

run:
	go build -ldflags "$(LDFLAGS)" -o $(GOPATH_BIN)/mecha . && $(GOPATH_BIN)/mecha

proto:
	protoc --go_out=$(ITERM_PROTO_OUT) --go_opt=paths=source_relative -I $(ITERM_PROTO_DIR) $(ITERM_PROTO_FILE)
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		-I . $(API_PROTO_FILE)

proto-update:
	curl -sL "https://gitlab.com/gnachman/iterm2/-/raw/master/proto/api.proto" -o $(ITERM_PROTO_FILE)
	$(MAKE) proto

test:
	go test ./pkg/...

clean:
	rm -rf bin/ $(ITERM_PROTO_OUT)/*.pb.go pkg/api/*.pb.go
