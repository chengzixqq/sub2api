.PHONY: build generate test test-unit test-integration test-e2e

EXACT_VERSION_TAG := $(firstword \
	$(shell git -C .. tag --points-at HEAD --list "v[0-9]*" --sort=-version:refname) \
	$(shell git -C .. tag --points-at HEAD --list "[0-9]*" --sort=-version:refname))
VERSION ?= $(if $(EXACT_VERSION_TAG),$(patsubst v%,%,$(EXACT_VERSION_TAG)),$(strip $(file <cmd/server/VERSION)))
CGO_ENABLED ?= 0
export CGO_ENABLED
LDFLAGS ?= -s -w -X main.Version=$(VERSION)

build:
	go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

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
