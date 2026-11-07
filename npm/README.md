# __PKG__

The command-line tool for **SePay**, a Vietnamese payment platform. Log in, watch transactions,
register webhooks and stream them to `localhost`, create hosted-checkout orders, and simulate all
of it in sandbox.

## Install

```sh
npm install --save-dev __PKG__@__VERSION__
npx __PKG__ version
```

**Always pin the version.** `__PKG__@latest` and a bare `npx __PKG__` resolve to whatever is current at
that moment, which is the opposite of what a lockfile is for. Prereleases go to their own dist-tags
(`alpha`, `beta`, `next` for a release candidate) and never move `latest`.

In CI, install from the lockfile and leave install scripts off:

```sh
npm ci --ignore-scripts
npx __PKG__ version
```

This package ships no install scripts at all, so `--ignore-scripts` costs you nothing here — and it
keeps the flag meaningful for every other dependency in your tree.

## How it works

`__PKG__` contains no binary. It declares six optional dependencies — one per platform — and npm
installs only the one matching your machine:

| Platform | Package |
|---|---|
| macOS Intel | `__SCOPE__/cli-darwin-x64` |
| macOS Apple silicon | `__SCOPE__/cli-darwin-arm64` |
| Linux x86-64 | `__SCOPE__/cli-linux-x64` |
| Linux arm64 | `__SCOPE__/cli-linux-arm64` |
| Windows x86-64 | `__SCOPE__/cli-win32-x64` |
| Windows arm64 | `__SCOPE__/cli-win32-arm64` |

Before running the binary, `__PKG__` checks its SHA-256 against a table built into this package, and
refuses to run on a mismatch.

`__SCOPE__` is the npm organization of SePay JSC (Công ty CP SePay), which publishes all seven
packages. The source lives in the company's GitHub organization, at
<https://github.com/sepayvn/sepay-cli>; the two names differ, the owner is the same.

## Verifying where this package came from

Two commands. There is no badge to trust instead.

```sh
npm audit signatures
npm view __PKG__ repository.url
```

The second must print `git+https://github.com/sepayvn/sepay-cli.git`. "Has provenance" only means
*some* repository built it — anyone who takes over a package name can publish with full provenance
from their own repository, and the check still passes. **The repository name is the part that
carries the meaning.**

## Known limitations of this channel

1. **No shell completions.** Registering them needs an install script, and this package deliberately
   has none. Run `sepay completion --help` to install them by hand.
2. **No cosign check at install time.** npm has no equivalent. On this channel the cosign signature
   over `checksums.txt` is verified **once, inside the release pipeline**, and what reaches you is the
   checksum table derived from that signed file.
3. **`npm install --omit=optional` (or `--no-optional`) breaks it.** The binary lives in an optional
   dependency, so skipping those skips the binary. The package's error message says so.

## Other downloads

Per-platform archives and the signed `checksums.txt` are attached to each GitHub Release:
<https://github.com/sepayvn/sepay-cli/releases>

Apache-2.0 © Công ty CP SePay
