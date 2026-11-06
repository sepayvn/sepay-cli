[Tiếng Việt](README.md) · [English](README.en.md)

# sepay-cli

[![CI](https://github.com/sepayvn/sepay-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/sepayvn/sepay-cli/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/sepayvn/sepay-cli)](https://github.com/sepayvn/sepay-cli/releases/latest)
[![npm](https://img.shields.io/npm/v/sepay-cli)](https://www.npmjs.com/package/sepay-cli)
[![Go Reference](https://pkg.go.dev/badge/github.com/sepayvn/sepay-cli.svg)](https://pkg.go.dev/github.com/sepayvn/sepay-cli)
[![License](https://img.shields.io/github/license/sepayvn/sepay-cli)](LICENSE)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/sepayvn/sepay-cli/badge)](https://scorecard.dev/viewer/?uri=github.com/sepayvn/sepay-cli)

SePay is a leading Open Banking infrastructure company in Vietnam, providing bank APIs and cash-flow automation for businesses, from collecting payments and reconciliation to notifications and e-invoicing.
Create an account at [my.sepay.vn/register](https://my.sepay.vn/register), then turn on [Test mode](https://developer.sepay.vn/en/tien-ich-khac/test-mode) (the sandbox) for your company.
The sandbox is free and never touches real money.

`sepay` is the SePay command-line tool. Log in, list transactions, create webhooks and receive them
on `localhost`, create hosted checkout orders, and simulate all of it in the sandbox.

```
sepay <command> <subcommand> [flags]
```

## Install

| Channel | Command |
|---|---|
| Homebrew (macOS, Linux) | `brew install sepayvn/sepay-cli/sepay` |
| Scoop (Windows) | `scoop bucket add sepay https://github.com/sepayvn/scoop-sepay-cli`, then `scoop install sepay/sepay` |
| Script (macOS, Linux) | `curl -fsSL https://get.sepay.vn/sepay-cli-install.sh \| sh` |
| npm | `npm i -g sepay-cli` |
| Go | `go install github.com/sepayvn/sepay-cli/cmd/sepay@latest` |
| Manual download | per-platform archives on [GitHub Releases](https://github.com/sepayvn/sepay-cli/releases) |

The install script verifies the archive's SHA-256 against `checksums.txt`, and the cosign signature
of `checksums.txt` when `cosign` is installed; `SEPAY_REQUIRE_SIGNATURE=1` refuses to install
without `cosign`. `SEPAY_VERSION` (or `--version`) picks a version, `SEPAY_INSTALL_DIR` (or `--dir`)
the install directory (default `$HOME/.local/bin`).

Pin a version in a Node project:

```bash
npm install --save-dev sepay-cli@<version>
npx sepay-cli version
npx sepay-cli@next version                    # latest release candidate
```

The npm platform packages live under the `@sepayjsc` org (`@sepayjsc/cli-*`). `@sepayjsc` is the
npm org of SePay JSC (Công ty CP SePay); the source is on [github.com/sepayvn](https://github.com/sepayvn).

### Verify a download

Every GitHub Release carries `checksums.txt`, its signature `checksums.txt.sig` and certificate
`checksums.txt.pem`. Download them next to the archive, then:

```bash
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github\.com/sepayvn/sepay-cli/\.github/workflows/release\.yml@refs/(heads/main|tags/v.*)$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum -c --ignore-missing checksums.txt      # macOS: shasum -a 256 -c --ignore-missing checksums.txt
gh attestation verify sepay_<version>_linux_amd64.tar.gz -R sepayvn/sepay-cli
```

The first command proves `checksums.txt` was signed by this repository's release workflow. The
second proves the archive matches it. The last checks build provenance: which commit and workflow
built the archive.

### Update

```bash
npm install -g sepay-cli@latest      # global npm install
npm install -D sepay-cli@latest      # pinned in a project (or let Dependabot / Renovate open the PR)
brew upgrade sepay                   # Homebrew
scoop update sepay                   # Scoop
```

Installed with the script or `go install`: run the same command again.

Once a day, when you run a command in a terminal, the CLI checks whether a newer version exists and
whether yours is no longer supported (withdrawn or vulnerable), then prints a one-line notice to
stderr after the command finishes. It never installs anything, and stays silent in CI, with
`--json`, or when `SEPAY_NO_UPDATE_NOTIFIER=1` is set.

## Get started

```bash
sepay login          # opens a browser to confirm a one-time code
sepay auth status    # the account and company you are using
```

The token is stored in the OS keychain. On a machine with no usable keychain (a server, an SSH
session), the CLI stores it in a file only your user can read and prints a yellow warning; nothing
else to do. To use the file from the start: `sepay login --insecure-storage`. No browser on the
machine: `sepay login --with-token`.

Every command runs against the **sandbox** by default. Add `--env production` for live data.

## Receive your first webhook on localhost

No public URL, no tunnel.

```bash
# Your handler is running on localhost:3000

sepay sandbox init --with-va --with-webhook-listen        # sandbox account + webhook
sepay webhook listen --forward localhost:3000 --print     # forward events to your handler

# In a second terminal: create a transaction
sepay bank-account list                                   # note the xid
sepay sandbox simulate payment_in --bank-account <ba_xid> --amount 50000 --code ABC123
```

Each event carries the exact body and signature headers SePay sends, so your handler runs its real
path, including its own signature check. Check that your handler answers correctly:
`sepay webhook test --forward localhost:3000 --amount 50000`.

## Commands

| Command | What it does |
|---|---|
| `login`, `logout` | Log in, log out |
| `auth` | `status`, `switch`, `refresh`, `sessions`, `revoke`, `token` |
| `transaction` | List and inspect transactions |
| `bank-account` | Manage bank accounts |
| `webhook` | Create, update, read logs, `listen`, `test`, `replay` |
| `va` | Manage virtual accounts (VA) |
| `paycode` | Pay code prefixes |
| `pg` | Payment Gateway orders and merchant |
| `bankhub` | Bank Hub companies, accounts and link tokens |
| `einvoice` | Electronic invoices |
| `apikey` | API keys |
| `quota` | Plan quota and usage |
| `sandbox` | `init`, `seed`, `simulate`, `reset` (sandbox only) |
| `api` | Call any SePay API endpoint with the stored token |
| `config` | CLI settings |
| `completion` | Shell completion scripts |
| `version` | Version and build info |

`sepay --help` lists everything. `sepay --map=paths` prints every command; `--map=json` adds
machine-readable metadata.

## Global flags

| Flag | Meaning |
|---|---|
| `--env sandbox\|production` | Environment, default `sandbox` |
| `--json`, `-o json\|yaml` | Machine-readable output, no color; every read command supports it |
| `-p, --profile` | Account to use when several are logged in |
| `-y, --yes` | Skip the confirmation on write commands (scripts, CI); never skips an OTP |
| `--no-input` | Never prompt; a missing value is an error |
| `--idempotency-key` | Retry an interrupted write without performing it twice |
| `-v, --verbose` | Debug log; tokens and OTPs are always redacted |

Environment variables: `SEPAY_PROFILE`, `SEPAY_CONFIG_DIR`, `SEPAY_NO_UPDATE_NOTIFIER`, `SEPAY_LANG`, `NO_COLOR`. Details: `sepay help environment`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Server error, network error, or rejected input; read `error_code` before retrying |
| 2 | Bad flags, bad arguments, unknown command |
| 3 | The command exists but has no backend; do not retry |
| 4 | Not logged in: `sepay login` |
| 5 | The token lacks the required scope: `sepay auth refresh --scopes <scope>` |
| 6 | Your local handler failed to accept an event (`webhook test`) |
| 7 | The server demanded an OTP; do this action in the web app |
| 8 | Your account or company is not allowed to do this |
| 130 | Interrupted: Ctrl+C or Esc at a prompt, or a stop signal |

Errors go to `stderr`, results to `stdout`. With `--json`, errors are JSON too, with `error_code`
and `exit_code`. Full list: `sepay help exit-codes`.

## Shell completion

Homebrew installs the bash, zsh and fish scripts for you. Installed from a release archive
or npm, write the script to a file once; the shell loads it from there, so `sepay` does not
run at every shell start.

```bash
# Bash (needs the bash-completion package)
mkdir -p ~/.local/share/bash-completion/completions
sepay completion bash > ~/.local/share/bash-completion/completions/sepay

# Zsh (then add fpath+=~/.zfunc to ~/.zshrc, before compinit)
mkdir -p ~/.zfunc
sepay completion zsh > ~/.zfunc/_sepay

# Fish
sepay completion fish > ~/.config/fish/completions/sepay.fish

# PowerShell (add the line to $PROFILE)
sepay completion powershell | Out-String | Invoke-Expression
```

## Documentation

Guides, the webhook and IPN bodies and the API reference:
[developer.sepay.vn/en/agents/cli](https://developer.sepay.vn/en/agents/cli).

In the terminal, `sepay help <topic>` prints `environment`, `errors`, `exit-codes` and `glossary`
for the version you run.

## Go API

This module is a CLI, not a library. Packages under `pkg/` mirror the layout of
[cli/cli](https://github.com/cli/cli) and carry no compatibility promise between versions.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md). Report security issues: [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE) © SePay JSC.
