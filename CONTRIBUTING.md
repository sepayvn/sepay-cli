# Contributing to sepay-cli

`sepay` is the open-source Go CLI for SePay, a leading Open Banking infrastructure company in
Vietnam. Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md). Security issues
go through [SECURITY.md](SECURITY.md), not public issues.

## Before you start

- Open an issue before a large change (a new command, a new flag on many commands, a changed
  `--json` schema or exit code) so the design is agreed before you write it. Small fixes and docs
  can go straight to a PR.
- Looking for something to pick up? Start with issues labelled `good first issue` or `help wanted`.
- No CLA, but every commit is signed off under the
  [Developer Certificate of Origin](https://developercertificate.org/): you certify that you wrote the
  change or have the right to submit it under [Apache-2.0](LICENSE). Sign off with `git commit -s`,
  which adds `Signed-off-by: Your Name <you@example.com>` matching your commit author. To sign off
  commits already on your branch, run `git rebase --signoff main` and force-push.

## Setup and the local gate

You need [Go](https://go.dev/dl/) 1.27.4 or later. With the default `GOTOOLCHAIN=auto`, an older
Go (1.21+) downloads 1.27.4 itself. Nothing else needs installing: `make check` runs the pinned
[`golangci-lint`](https://golangci-lint.run/) v2.15.0 and gofumpt through `go run`.

```bash
git clone https://github.com/sepayvn/sepay-cli.git
cd sepay-cli
make build && ./bin/sepay version
make check        # run before every push
```

`make check` is the gate CI runs: `gofumpt` (fix with `go run mvdan.cc/gofumpt@v0.12.0 -w .`), `go vet`, `golangci-lint`, `go test ./... -race` and a
`go mod tidy` check that `go.mod` and `go.sum` are clean. `make leaks` runs gitleaks and
`make vuln` runs govulncheck; both are pinned in the `tool` block of `go.mod`.
`make help` lists every target. Without a C compiler, `-race` cannot build: run `make test RACE=`.

CI runs the tests on Linux, macOS and Windows. A test that depends on Unix file modes skips or guards
that assertion on Windows, with the reason in the code.

Tests do not call `t.Parallel()` on purpose: they set environment variables with `t.Setenv` and point
the config directory at a temp dir, and both are process-global.

JSON output is pinned by golden files in `testdata/*.golden` (helper `internal/golden`). After an
intended output change, regenerate them one package at a time, e.g. `go test ./pkg/jsonfmt -update`,
and review the diff.

## Testing against sandbox

`make check` needs no account: tests run against `httptest` servers. To try a change end to end:

1. Create an account at [my.sepay.vn/register](https://my.sepay.vn/register) and turn on
   [Test mode](https://developer.sepay.vn/en/tien-ich-khac/test-mode) (the sandbox). It is free and
   moves no real money.
2. Keep your test login apart from your own: `export SEPAY_CONFIG_DIR=$(mktemp -d)`.
3. `./bin/sepay login`. Every command uses the sandbox unless you pass `--env production`. No
   environment variable supplies a token.
4. `./bin/sepay sandbox init --with-webhook-listen`, then `sandbox simulate`, `webhook listen` and the
   other commands you changed. `sandbox reset` clears the sandbox.

`sepay help environment` lists every variable the CLI reads, including the per-environment host
overrides for pointing it at a local backend.

## Code layout and standards

- `cmd/sepay` is the entry point, `pkg/cmd/<noun>` holds the commands (for example
  `pkg/cmd/bankaccount`), `internal/` holds auth, config, the API clients and `internal/listen`.
  Commands are `NewCmdXxx(f *cmdutil.Factory) *cobra.Command` and take dependencies from the Factory.
- `pkg/` mirrors the [cli/cli](https://github.com/cli/cli) layout. It is not a stable Go API: do not
  import it from another module.
- User-facing strings are English and terse; error strings are lowercase with no trailing punctuation.
- The CLI ships no secret and calls only the public SePay API.

## Pull requests

Branch off `main` (`feat/…`, `fix/…`), open a PR, **squash merge**. The PR title becomes the
commit message on `main`, and [release-please](https://github.com/googleapis/release-please) reads
it, so it must follow [Conventional Commits](https://www.conventionalcommits.org/) (CI checks it):

| Title | Effect on the next version (pre-1.0) |
|---|---|
| `feat: …` | minor (0.1.0 → 0.2.0) |
| `fix: …`, `perf: …` | patch (0.1.0 → 0.1.1) |
| `feat!: …` or a `BREAKING CHANGE:` footer | breaking; minor before 1.0, major after |
| `docs:`, `test:`, `ci:`, `build:`, `refactor:`, `chore:` | no release |

A breaking change to a command's `--json` schema or an exit code counts as breaking.
Do not edit `CHANGELOG.md` by hand: release-please writes it.

## Go version policy

`go.mod` says `go 1.27.4`: the floor for every build, `go install` included, is the toolchain that
CI tests and govulncheck scans. Every workflow reads Go from `go.mod` (`go-version-file`), and a CI
step fails if the installed Go differs from that line. To bump Go, change only that line.

## Releasing

Maintainers release from GitHub only; see [docs/RELEASING.md](docs/RELEASING.md).

## License

Apache-2.0. © Công ty CP SePay.
