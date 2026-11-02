# sepay

The command-line tool for **SePay**. Log in, watch transactions, create webhooks and stream
them to `localhost`, create hosted-checkout orders, and simulate all of it in sandbox.

```
sepay <command> <subcommand> [flags]
```

## Install

```bash
npm install --save-dev sepay-cli@<version>   # pin the version
npx sepay-cli version
npx sepay-cli@next version                    # latest release candidate
```

Per-platform archives and the cosign-signed `checksums.txt` are attached to each
[GitHub Release](https://github.com/sepayvn/sepay-cli/releases).

### Updating

```bash
npm install -g sepay-cli@latest      # global install
npm install -D sepay-cli@latest      # pinned in a project (or let Dependabot / Renovate open the PR)
```

Once a day an interactive run checks for a newer release, and whether the version you run has
been deprecated (withdrawn or vulnerable), and prints a short notice to stderr after the command.
It never installs anything and stays quiet in CI, under `--json`, and when
`SEPAY_NO_UPDATE_NOTIFIER=1`.

## Get started

```bash
sepay login          # opens a browser to approve a one-time code
sepay auth status    # the account and company you are on
```

The token is stored in your OS keychain. On a machine with no browser: `sepay login --with-token`. With no usable keychain (a server, a container): add `--insecure-storage` to keep it in a `0600` file.

Every command runs against **sandbox** by default. Add `--env production` to touch live data.

## Receive your first webhook on localhost

No public URL, no tunnel.

```bash
# Your handler is running on localhost:3000

sepay sandbox init --with-va --with-webhook-listen        # sandbox account + webhook
sepay webhook listen --forward localhost:3000 --print     # stream events to the handler

# Second shell: fire a transaction
sepay bank-account list                                   # note the xid
sepay sandbox simulate payment_in --bank-account <ba_xid> --amount 50000 --code ABC123
```

Each event carries the same body and signature headers SePay sends, so your handler runs its real
path, including its own signature check. Assert the handler responds correctly: `sepay webhook test --forward localhost:3000 --amount 50000`.

## Commands

| Command | What it does |
|---|---|
| `transaction` | List and inspect transactions |
| `bank-account` | Manage bank accounts |
| `webhook` | Create, update, view logs, `listen`, `test`, `replay` |
| `va` | Manage virtual accounts |
| `paycode` | Payment code prefix |
| `pg` | Payment Gateway orders and merchant |
| `bankhub` | Bank Hub companies, accounts, and link tokens |
| `einvoice` | Electronic invoices |
| `apikey` | API keys |
| `quota` | Plan quota and usage |
| `sandbox` | `init`, `seed`, `simulate`, `reset` (sandbox only) |
| `api` | Call any SePay API endpoint with the stored token |
| `auth` | `status`, `switch`, `refresh`, `sessions`, `revoke`, `token` |
| `config` | CLI settings |
| `docs` | Reference topics in the terminal |

`sepay --help` lists everything. `sepay --map=paths` prints every command; `--map=json` adds
machine-readable metadata.

## Global flags

| Flag | Meaning |
|---|---|
| `--env sandbox\|production` | Environment, default `sandbox` |
| `--json`, `-o json\|yaml` | Machine-readable output, no color; every read command supports it |
| `-p, --profile` | Account to use, when more than one is logged in |
| `-y, --yes` | Skip the confirmation on write commands (scripts, CI); cannot bypass OTP |
| `--no-input` | Never prompt; a missing value is an error |
| `--idempotency-key` | Retry an interrupted write without repeating it |
| `-v, --verbose` | Debug logging; tokens and OTP are always redacted |

Environment variables: `SEPAY_PROFILE`, `SEPAY_CONFIG_DIR`, `SEPAY_NO_UPDATE_NOTIFIER`, `NO_COLOR`. Details: `sepay docs environment`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Server error or rejected input; read `error_code` before retrying |
| 2 | Bad flags, bad arguments, unknown command |
| 4 | Not authenticated: `sepay login` |
| 5 | Token lacks the required scope: `sepay auth refresh --scopes <scope>` |
| 6 | Your local handler did not accept the event (`webhook test`) |
| 7 | The server demanded an OTP; do this action in the web app |
| 8 | Your account or company is not allowed to do this |

Errors go to `stderr`, output to `stdout`. Under `--json` the error is JSON too, with `error_code`
and `exit_code`. Full list: `sepay docs exit-codes`.

## Shell completion

From a release archive or npm, run once:

```bash
mkdir -p ~/.local/share/bash-completion/completions && sepay completion bash > ~/.local/share/bash-completion/completions/sepay   # needs bash-completion
mkdir -p ~/.zfunc && sepay completion zsh > ~/.zfunc/_sepay   # ~/.zshrc: fpath+=~/.zfunc before compinit
sepay completion fish > ~/.config/fish/completions/sepay.fish
sepay completion powershell | Out-String | Invoke-Expression   # append to $PROFILE
```

## Documentation

| Topic | Command |
|---|---|
| What SePay is, which payment path to pick | `sepay docs overview` |
| Command order for common tasks | `sepay docs flows` |
| Webhook body and signature headers | `sepay docs webhook-payload` |
| Payment Gateway order IPN | `sepay docs ipn-payload` |
| Calling the SePay API directly from your app | `sepay docs api` |

Full documentation: [developer.sepay.vn/vi/tien-ich-khac/sepay-cli](https://developer.sepay.vn/vi/tien-ich-khac/sepay-cli).

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md). Report a security vulnerability: [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE) © SePay JSC.
