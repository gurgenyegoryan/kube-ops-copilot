BINARY_NAME ?= kube-ops-copilot
PKG ?= ./cmd/$(BINARY_NAME)

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS ?= -s -w \
	-X github.com/gurgenyegoryan/kube-ops-copilot/internal/cli.Version=$(VERSION) \
	-X github.com/gurgenyegoryan/kube-ops-copilot/internal/cli.Commit=$(COMMIT) \
	-X github.com/gurgenyegoryan/kube-ops-copilot/internal/cli.Date=$(DATE)

.PHONY: test
test:
	go test ./...

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o ./bin/$(BINARY_NAME) $(PKG)

.PHONY: diagnose
# Example: make diagnose KUBECONFIG=~/.kube/config CONTEXT=prod
# If CONTEXT is empty, kubectl default context is used.
diagnose: build
	./bin/$(BINARY_NAME) diagnose --kubeconfig "$(KUBECONFIG)" --context "$(CONTEXT)" --output markdown

.PHONY: lint
lint:
	golangci-lint run

.PHONY: fmt
fmt:
	gofmt -w .
