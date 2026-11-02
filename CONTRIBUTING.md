# Contributing to sepay-cli

`sepay` is the open-source Go CLI for the SePay payment platform.

By contributing, you agree that your contributions are licensed under the
[Apache-2.0 License](LICENSE).

## Prerequisites

- [Go](https://go.dev/dl/) **1.25+** (CI and releases build with 1.26.10)
- Optional (CI runs these): [`golangci-lint`](https://golangci-lint.run/) v2,
  [`goreleaser`](https://goreleaser.com/) v2.19, Node 22 for the npm channel

## Development setup

```bash
git clone https://github.com/sepayvn/sepay-cli.git
cd sepay-cli
make build        # builds ./bin/sepay with version info embedded
./bin/sepay version
```

## Build, test, lint

```bash
gofmt -l ./cmd ./pkg ./internal    # must print nothing
go mod tidy && git diff --exit-code go.mod go.sum
go vet ./...
go test ./... -race
golangci-lint run                  # optional locally; always runs in CI
make leaks                         # gitleaks: must print "no leaks found"
```

## Coding standards

- Code passes `gofmt`, `go vet` and `golangci-lint` (`.golangci.yml`).
- User-facing CLI strings are English, terse (error strings lowercase, no trailing punctuation).
- `internal/` is private infrastructure (auth / config / api …); `pkg/` is the command layer.
  Commands are `NewCmdXxx(f *cmdutil.Factory) *cobra.Command` and take dependencies from the Factory.
- The CLI ships no secret and calls only the public SePay API (see [SECURITY.md](SECURITY.md)).

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

## Releasing

Everything happens on GitHub; nobody builds or publishes from a laptop.

1. Every merge to `main` updates one open PR titled **`chore(main): release X.Y.Z`**
   (version bump in `.release-please-manifest.json` + the new `CHANGELOG.md` section).
2. To ship: review that PR and **merge it**. The `Release` workflow then
   - tags `vX.Y.Z` and creates the GitHub Release with the changelog as notes,
   - builds 6 binaries (linux / darwin / windows × amd64 / arm64) with GoReleaser, signs
     `checksums.txt` with cosign and attaches everything to the Release,
   - builds the 7 npm packages from the signed archives, checks them, publishes the 6 platform
     packages, re-downloads and compares checksums, test-installs, then publishes
     `sepay-cli` under the **`latest`** dist-tag.
3. Prerelease (to try a build before releasing it). Take X.Y.Z from the open release PR title:
   ```bash
   git tag -a v0.3.0-rc.1 -m v0.3.0-rc.1 origin/main && git push origin v0.3.0-rc.1
   ```
   | Tag | npm dist-tag | Meaning |
   |---|---|---|
   | `vX.Y.Z-alpha.N` | `alpha` | early, may change a lot |
   | `vX.Y.Z-beta.N` | `beta` | feature complete, still testing |
   | `vX.Y.Z-rc.N` | `next` | release candidate: this exact code becomes X.Y.Z |

   → a GitHub prerelease + that npm dist-tag (`npx sepay-cli@next version`). `latest` does not move. Push one tag at a time, never `git push --tags`.
   The tag must point at a commit of `main`: the workflow refuses any other commit before
   building anything (it protects against tagging the wrong commit; someone with write access
   could still edit `release.yml` in that commit, so keep write access to 1–2 people).
4. Provenance follows the repository visibility (step `provenance must match the repository
   visibility` in `release.yml`). While the repository is private, leave the repository variable
   `NPM_PROVENANCE` unset: every release, `latest` included, publishes without provenance (npm
   refuses `--provenance` from a private repository). Once it is public, set `NPM_PROVENANCE=true`
   and add required reviewers to the `release` environment; until the variable is set the npm job
   refuses to publish.
5. A broken version is never unpublished: fix forward with a new version, and mark the bad one
   `npm deprecate sepay-cli@X.Y.Z "use X.Y.Z+1"`.

### First-time setup (once per package)

npm only lets a trusted publisher be attached to a package that exists. A new package is created
with staged publishing, from a maintainer's machine (npm >= 11.15, account with 2FA):

```bash
npx -y npm@11.21.1 login
bash scripts/npm-bootstrap.sh     # stage the 7 names, then bind each to release.yml
```

`npm stage publish` of a name that does not exist creates it with a public placeholder version
`0.0.0-stage`; the staged content stays private and is never approved. `npm trust github` binds the
package to `sepayvn/sepay-cli` + `release.yml` with direct publish allowed. No npm token is stored
in GitHub. A new platform package (a new OS / CPU) needs the same two steps before its first release.

Useful checks:

```bash
npm view sepay-cli dist-tags
gh release list --repo sepayvn/sepay-cli
```

## License

By submitting a contribution you certify that you have the right to do so and agree to license it
under Apache-2.0. © Công ty CP SePay.
