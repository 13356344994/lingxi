.PHONY: fmt vet test race cover build vuln check

GO ?= go
BINARY ?= lingxi
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X 'main.appVersion=$(VERSION)' -X 'main.gitCommit=$(COMMIT)' -X 'main.buildTime=$(BUILD_TIME)'

fmt:
	@find . -name '*.go' -not -path './.gocache/*' -print0 | xargs -0 $(GO)fmt -w

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) .

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: fmt vet test build
