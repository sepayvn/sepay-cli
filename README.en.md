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
| Script (Windows, PowerShell) | `irm https://get.sepay.vn/sepay-cli-install.ps1 \| iex` |
| npm | `npm i -g sepay-cli` |
| Go | `go install github.com/sepayvn/sepay-cli/cmd/sepay@latest` |
| Manual download | per-platform archives on [GitHub Releases](https://github.com/sepayvn/sepay-cli/releases) |

The install script verifies the archive's SHA-256 against `checksums.txt`, and the cosign signature
of `checksums.txt` when `cosign` is installed; `SEPAY_REQUIRE_SIGNATURE=1` refuses to install
without `cosign`. `SEPAY_VERSION` (or `--version`) picks a version, `SEPAY_INSTALL_DIR` (or `--dir`)
the install directory (default `$HOME/.local/bin`).

When the install directory is not on PATH, the script appends a marked block
(`# >>> sepay-cli PATH >>>` … `# <<< sepay-cli PATH <<<`) to the startup file of your shell
(`$SHELL`): `~/.zshrc` (zsh, under `$ZDOTDIR` when set), for bash on macOS the first of
`~/.bash_profile`, `~/.bash_login`, `~/.profile` that exists (`~/.bash_profile` when none does),
`~/.bashrc` (bash on Linux), `~/.config/fish/conf.d/sepay.fish` (fish, under `$XDG_CONFIG_HOME`
when set), `~/.profile` (sh, dash, ksh). For any other shell (tcsh, nushell…), or with `--dir` when `$HOME` is not set, it only says to add the directory by hand (without `$HOME` and `--dir` it installs nothing).
Running it again adds nothing. It cannot change the PATH of the terminal already open: open a new
one, or run the `export PATH=…` line it prints. `SEPAY_NO_MODIFY_PATH=1` (or `--no-modify-path`)
leaves those files alone and only prints the `export` line. To uninstall, delete `sepay` from the
install directory and the marked block (for fish, the `sepay.fish` file).

The PowerShell script (Windows PowerShell 5.1 or later) runs the same checks and reads the same
environment variables, set before the command: `$env:SEPAY_VERSION = '0.2.0'; irm … | iex`. It
installs to `%LOCALAPPDATA%\Programs\sepay` and adds that directory to the user PATH (no admin
rights needed); `SEPAY_NO_MODIFY_PATH=1` leaves PATH alone.

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
go install github.com/sepayvn/sepay-cli/cmd/sepay@latest
sepay update                         # installed with the script or from an archive
```

`sepay update` (or `sepay upgrade`) downloads the latest release for your OS and architecture from
GitHub Releases, checks its SHA-256 against `checksums.txt` (and the signature when `cosign` is
installed; `SEPAY_REQUIRE_SIGNATURE=1` refuses to update without `cosign`), runs the new binary
once, and only then replaces the one in use. It asks y/N first;
`--yes` skips the question and is required without a terminal. `--version 0.2.0` installs that
release, `--check` only reports whether a newer one exists (add `--json` for scripts). A sepay
installed with npm, Homebrew, Scoop or `go install` is left alone: the command prints that tool's
update command and exits 1.

Once a day, when you run a command in a terminal, the CLI checks whether a newer version exists and
whether yours is no longer supported (withdrawn or vulnerable), then prints a one-line notice to
stderr after the command finishes, with the update command for the way you installed it. The
notice never installs anything, and stays silent in CI, with `--json`, or when
`SEPAY_NO_UPDATE_NOTIFIER=1` is set.

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

## Production writes

A command that changes data on production runs only when you confirm it on your own terminal. An AI
does not confirm it for you: it hands you the command to run yourself.

- The command needs `--env production` on the line; `default_env: production` does not count.
- On a terminal the CLI asks `[y/N]`, default No.
- Without a terminal (an agent, a pipe, CI) it exits 2 with `ask the user to run this command in their
  own terminal`. A script you write yourself runs it with `--env production`, the id of the resource it
  changes and `--yes`; a create (no id) runs only on a terminal.
- `--dry-run` prints the request it would send and the before / after values, and sends no write.
  It still sends the reads it needs, so on production the token needs the `read` scope:
  `webhook update`, `webhook delete` and `bank-account update` read the resource, `webhook create`
  and `update` the bank accounts they target, `paycode update` the pay code list, `webhook replay`
  the transaction and its last delivery. `paycode create` and `sepay api --dry-run` send nothing,
  a GET included.

The commands under this rule: `sepay --map=json --env production`, the ones with `confirms: terminal`.
An agent never adds `--env production` or `--yes` to them. Details:
https://developer.sepay.vn/en/agents/cli/quy-uoc-chung#production-writes

On production the CLI only reads Payment Gateway orders (`pg order list`, `pg order get`) and e-invoices
(`einvoice invoice list`, `einvoice invoice get`); every other `pg`, `einvoice`, `bankhub` and `sandbox`
command runs on sandbox only.

## Receive your first webhook on localhost

No public URL, no tunnel.

```bash
# Your handler is running on localhost:3000

sepay sandbox init --with-va --with-webhook-listen        # sandbox account + webhook
sepay webhook listen --forward localhost:3000 --print     # forward events to your handler (sandbox only)

# In a second terminal: create a transaction
sepay bank-account list                                   # note the xid
sepay sandbox simulate payment_in --bank-account <ba_xid> --amount 50000 --code ABC123
```

Each event carries the exact body and signature headers SePay sends, so your handler runs its real
path, including its own signature check. Check that your handler answers correctly:
`sepay webhook test --forward localhost:3000 --amount 50000`.
`webhook listen` runs on sandbox only: with `--env production` the CLI exits 2, and the server answers 404
to any listen call on production. On production, read deliveries with `sepay webhook logs`.

## Commands

| Command | What it does |
|---|---|
| `login`, `logout` | Log in, log out |
| `auth` | `status`, `switch`, `refresh`, `sessions`, `revoke`, `token` |
| `transaction` | List and inspect transactions |
| `bank-account` | Manage bank accounts |
| `webhook` | Create, update, read logs, `replay`; `listen`, `test` on sandbox only |
| `va` | Manage virtual accounts (VA) |
| `paycode` | Pay code prefixes |
| `pg` | Payment Gateway orders and merchant; on production only `order list`, `order get` |
| `bankhub` | Bank Hub companies, accounts and link tokens |
| `einvoice` | Electronic invoices; on production only `invoice list`, `invoice get` |
| `apikey` | Sandbox API keys. Production API keys are created and revoked on my.sepay.vn; the CLI does not manage them |
| `quota` | Plan quota and usage; on production also the e-invoices left |
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
| `-y, --yes` | Skip the confirmation (scripts, CI). For a production write (`confirms: terminal` in `sepay --map=json --env production`), `--yes` counts only when the command also carries `--env production` and names the resource it changes |
| `--no-input` | Never prompt; a missing value is an error |
| `--idempotency-key` | Retry an interrupted write without performing it twice |
| `-v, --verbose` | Debug log; tokens are always redacted |

Environment variables: `SEPAY_PROFILE`, `SEPAY_CONFIG_DIR`, `SEPAY_NO_UPDATE_NOTIFIER`, `SEPAY_LANG`, `NO_COLOR`. Details: `sepay help environment`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Server error, network error, or rejected input; read `error_code` before retrying |
| 2 | Bad flags, bad arguments, unknown command, or a production write you must run on your own terminal |
| 3 | The command exists but has no backend; do not retry |
| 4 | Not logged in: `sepay login` |
| 5 | The token lacks the required scope: `sepay login --scopes <scope>` |
| 6 | Your local handler failed to accept an event (`webhook test`) |
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
