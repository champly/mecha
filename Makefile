.PHONY: build init run test vet fmt tools clean proto proto-update

GOBIN := $(shell go env GOPATH)/bin

TAG       := $(shell git describe --tags --exact-match 2>/dev/null)
REV       := $(shell git rev-parse --short=8 HEAD 2>/dev/null)
DIRTY     := $(shell git diff --quiet 2>/dev/null || echo -dirty)
VERSION   ?= $(if $(TAG),$(TAG),v0.0.0-$(REV))$(DIRTY)
BUILDDATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := \
	-X github.com/champly/mecha/cmd.Version=$(VERSION) \
	-X github.com/champly/mecha/cmd.BuildDate=$(BUILDDATE)

ITERM_PROTO_DIR := pkg/term/iterm2/api
ITERM_PROTO     := $(ITERM_PROTO_DIR)/api.proto
API_PROTO       := pkg/api/api.proto

build:
	go build -ldflags "$(LDFLAGS)" -o $(GOBIN)/mecha .

init:
	go run . init

run: build
	$(GOBIN)/mecha

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

proto:
	protoc --go_out=$(ITERM_PROTO_DIR) --go_opt=paths=source_relative -I $(ITERM_PROTO_DIR) $(ITERM_PROTO)
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		-I . $(API_PROTO)

proto-update:
	curl -sL "https://gitlab.com/gnachman/iterm2/-/raw/master/proto/api.proto" -o $(ITERM_PROTO)
	$(MAKE) proto

clean:
	go clean
	rm -rf bin/ $(GOBIN)/mecha
