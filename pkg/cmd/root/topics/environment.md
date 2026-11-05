# Environment variables

The CLI reads the variables below and ignores any other `SEPAY_` variable, without a warning.
Like any Go program it also honours `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, `SSL_CERT_FILE` and
`SSL_CERT_DIR`. No variable supplies a token: a credential comes only from `sepay login`.

## General

| Variable | Effect |
|---|---|
| `SEPAY_CONFIG_DIR` | Directory for `config.yml`, `hosts.yml` and the file-stored credential |
| `SEPAY_PROFILE` | Profile to use, as `--profile` |
| `NO_COLOR` | Disable color, as `--no-color` |
| `SEPAY_NO_UPDATE_NOTIFIER` | Turn off the daily update notice |

An on/off variable reads its value: `0`, `false`, `no`, `off` and empty mean off, so `NO_COLOR=0`
keeps color on.

Without `SEPAY_CONFIG_DIR` the directory is:

| OS | Directory |
|---|---|
| Linux | `~/.config/sepay/` |
| macOS | `~/Library/Application Support/sepay/` |
| Windows | `%AppData%\sepay\` |

`--config <file>` reads one file instead. In CI, or when an agent runs beside your own session,
point `SEPAY_CONFIG_DIR` at a scratch directory so it cannot touch your logged-in profile.

The update notice asks the npm registry once a day for the latest release and whether yours is
deprecated, then prints one line to stderr after the command. It is off when `CI` is set, when
stderr is not a terminal, with `--json`, `-o yaml` or `--quiet`, and in a dev build. The answer is
cached in `update-state.json` in the config directory.

## Hosts

| Variable | Host |
|---|---|
| `SEPAY_SANDBOX_API_URL`, `SEPAY_PRODUCTION_API_URL` | SePay API |
| `SEPAY_SANDBOX_PG_API_URL`, `SEPAY_PRODUCTION_PG_API_URL` | Payment Gateway API |
| `SEPAY_SANDBOX_PG_CHECKOUT_URL`, `SEPAY_PRODUCTION_PG_CHECKOUT_URL` | Payment Gateway checkout (`pg checkout init`) |
| `SEPAY_SANDBOX_BANKHUB_API_URL`, `SEPAY_PRODUCTION_BANKHUB_API_URL` | Bank Hub API |
| `SEPAY_SANDBOX_EINVOICE_API_URL`, `SEPAY_PRODUCTION_EINVOICE_API_URL` | E-invoice API |
| `SEPAY_OAUTH_URL` | OAuth host for `sepay login`, shared by both environments |

Each variable applies to one environment only. For the active environment `E`, the first match
wins:

```
--api-base  >  SEPAY_{E}_API_URL  >  config <E>.api_url  >  built-in default
```

`--api-base` moves the SePay API host only. Most `bankhub`, `pg` and `einvoice` commands mint
their credential there but send their requests to the product's own host: move that one with its
variable above, or `config set <E>.bankhub_api_url`, `<E>.pg_api_url`, `<E>.einvoice_api_url`.
`pg checkout init` also posts to the checkout host (`<E>.pg_checkout_url`). `bankhub ipn` and
`pg ipn` run on the SePay API host, so `--api-base` moves them entirely.

An authenticated command sends a token over plain http only to a loopback host, and a
sandbox-only command reaches only the sandbox host or loopback. Neither check can be turned off;
reach a remote backend over https or an SSH tunnel.

```
Error: refusing to send credentials over plaintext http to non-loopback
host "10.0.0.5"; use https
```

## Which environment is active

Sandbox and production hold separate data. `--env` overrides the stored default, and
`sepay config set default_env production` asks for confirmation before it changes it.

- `sepay --map=json` reports `active_env`; use it in scripts.
- Commands print `env: <env>` to stderr before their first prompt or request, except with
  `--json`, `-o yaml` or `--quiet`. An overridden host is named:
  `env: sandbox · host from SEPAY_SANDBOX_API_URL`. A `bankhub`, `pg` or `einvoice` command also
  names an overridden API host: `· API host from SEPAY_SANDBOX_API_URL`, and with `--api-base`
  adds a `note:` naming each of its own hosts still on the default. `--verbose` prints the URL.
- `--help` shows it in the `[Agent guidance]` block.
