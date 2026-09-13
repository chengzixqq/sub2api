.PHONY: build generate test test-unit test-integration test-e2e

ifeq ($(OS),Windows_NT)
VERSION ?= $(strip $(file <cmd/server/VERSION))
CGO_ENV := set "CGO_ENABLED=0" &&
else
VERSION ?= $(shell ./scripts/resolve-version.sh)
CGO_ENV := CGO_ENABLED=0
endif
LDFLAGS ?= -s -w -X main.Version=$(VERSION)

build:
	$(CGO_ENV) go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test:
	go test ./...
	golangci-lint run ./...

test-unit:
	go test -tags=unit ./...

test-integration:
	go test -tags=integration ./...

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
