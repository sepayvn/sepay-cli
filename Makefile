# Version info injected into the binary via -ldflags.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

PKG := github.com/sepayvn/sepay-cli/internal/build
LDFLAGS := -X '$(PKG).Version=$(VERSION)' -X '$(PKG).Commit=$(COMMIT)' -X '$(PKG).Date=$(DATE)'

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
# The gofumpt that GOLANGCI_LINT embeds; bump both together.
GOFUMPT := go run mvdan.cc/gofumpt@v0.12.0
# -race needs cgo and a C compiler; `make test RACE=` runs without it.
RACE ?= -race

.DEFAULT_GOAL := build

help: ## list the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## build bin/sepay with version info
	go build -ldflags "$(LDFLAGS)" -o bin/sepay ./cmd/sepay

run: ## run the CLI from source
	go run ./cmd/sepay

clean: ## delete bin/ and completions/
	rm -rf bin completions

# Generated on demand, never committed. GoReleaser calls this before packaging so
# the shipped scripts always match the binary being released.
completions: build ## generate shell completion scripts
	@mkdir -p completions
	bin/sepay completion bash > completions/sepay.bash
	bin/sepay completion zsh  > completions/_sepay
	bin/sepay completion fish > completions/sepay.fish

# Developer convenience: put the binary on PATH and register bash completion in the
# XDG dir that bash-completion lazy-loads, so no shell rc edit is needed.
BINDIR ?= $(HOME)/.local/bin
COMPDIR ?= $(or $(XDG_DATA_HOME),$(HOME)/.local/share)/bash-completion/completions

install: build ## install bin/sepay and bash completion for this user
	@mkdir -p "$(BINDIR)" "$(COMPDIR)"
	install -m 0755 bin/sepay "$(BINDIR)/sepay"
	bin/sepay completion bash > "$(COMPDIR)/sepay"
	@chmod 0644 "$(COMPDIR)/sepay"
	@echo "installed $(BINDIR)/sepay — open a new shell for completion"

# Secret scan of the working tree, run before anything is pushed to GitHub. CI scans
# the full history with the same version.
leaks: ## scan the working tree for secrets
	go tool -modfile=tools/go.mod gitleaks dir . --redact

# The checks CI runs, with the same flags (.github/workflows/ci.yml lint and test jobs).
fmt: ## fail if a Go file needs gofumpt
	@out=$$($(GOFUMPT) -l ./cmd ./pkg ./internal) || exit 1; \
	if [ -n "$$out" ]; then echo "gofumpt needed on (fix: $(GOFUMPT) -w .):"; echo "$$out"; exit 1; fi

vet: ## go vet
	go vet ./...

tidy-check: ## fail if go mod tidy would change go.mod, go.sum or tools/go.mod
	go mod tidy -diff
	go -C tools mod tidy -diff

vuln: ## govulncheck, same version as CI
	go tool -modfile=tools/go.mod govulncheck ./...

lint: ## golangci-lint, same version as CI
	$(GOLANGCI_LINT) run

test: ## go test with the race detector
	go test ./... $(RACE)

check: fmt vet tidy-check lint test ## everything CI checks on Go code

.PHONY: help build run clean completions install leaks fmt vet tidy-check vuln lint test check
