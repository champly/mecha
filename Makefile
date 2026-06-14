.PHONY: build init clean

GOPATH_BIN := $(shell go env GOPATH)/bin

build:
	go build -o bin/mecha ./cmd && mv bin/mecha ~/go/bin/mecha

init:
	go run ./cmd init

run:
	go build -o $(GOPATH_BIN)/mecha ./cmd && mecha

clean:
	rm -rf bin/
