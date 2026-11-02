# Version info injected into the binary via -ldflags.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

PKG := github.com/sepayvn/sepay-cli/internal/build
LDFLAGS := -X '$(PKG).Version=$(VERSION)' -X '$(PKG).Commit=$(COMMIT)' -X '$(PKG).Date=$(DATE)'

build:
	go build -ldflags "$(LDFLAGS)" -o bin/sepay ./cmd/sepay

run:
	go run ./cmd/sepay

clean:
	rm -rf bin completions

# Generated on demand, never committed. GoReleaser calls this before packaging so
# the shipped scripts always match the binary being released.
completions: build
	@mkdir -p completions
	bin/sepay completion bash > completions/sepay.bash
	bin/sepay completion zsh  > completions/_sepay
	bin/sepay completion fish > completions/sepay.fish

# Developer convenience: put the binary on PATH and register bash completion in the
# XDG dir that bash-completion lazy-loads, so no shell rc edit is needed.
BINDIR ?= $(HOME)/.local/bin
COMPDIR ?= $(or $(XDG_DATA_HOME),$(HOME)/.local/share)/bash-completion/completions

install: build
	@mkdir -p "$(BINDIR)" "$(COMPDIR)"
	install -m 0755 bin/sepay "$(BINDIR)/sepay"
	bin/sepay completion bash > "$(COMPDIR)/sepay"
	@chmod 0644 "$(COMPDIR)/sepay"
	@echo "installed $(BINDIR)/sepay — open a new shell for completion"

# Secret scan of the working tree, run before anything is pushed to GitHub. CI scans
# the full history with the same version.
leaks:
	go run github.com/zricethezav/gitleaks/v8@v8.21.2 dir . --redact

.PHONY: build run clean completions install leaks
