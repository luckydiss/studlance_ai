SHELL := /bin/sh

GO ?= go
PNPM ?= pnpm

.PHONY: all generate lint test web build build-windows clean

all: lint test build

generate:
	$(GO) generate ./...
	$(PNPM) -C web --filter @studlance/shared generate

lint:
	golangci-lint run

test:
	$(GO) test -race ./...

web:
	$(PNPM) -C web install
	$(PNPM) -C web build

build:
	$(GO) build -o bin/studlance-server ./cmd/server
	$(GO) build -o bin/studlance-worker ./cmd/worker

build-windows:
	GOOS=windows GOARCH=amd64 $(GO) build -o bin/studlance-server.exe ./cmd/server
	GOOS=windows GOARCH=amd64 $(GO) build -o bin/studlance-worker.exe ./cmd/worker

clean:
	rm -rf bin