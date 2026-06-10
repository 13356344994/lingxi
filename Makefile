.PHONY: fmt vet test race cover build vuln check

GO ?= go
BINARY ?= lingxi
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
VERSION_PKG := lingxi/internal/app
LDFLAGS := -X '$(VERSION_PKG).appVersion=$(VERSION)' -X '$(VERSION_PKG).gitCommit=$(COMMIT)' -X '$(VERSION_PKG).buildTime=$(BUILD_TIME)'

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
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/lingxi
	# SELinux(Enforcing):go build 产出新 inode 会丢 bin_t 标签,systemd 将以 203/EXEC 拒绝执行;
	# semanage 规则已持久化(fcontext /root/lingxi/lingxi → bin_t),这里每次构建后恢复标签。
	-restorecon $(BINARY) 2>/dev/null || true

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: fmt vet test build
