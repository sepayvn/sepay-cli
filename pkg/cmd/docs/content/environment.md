# Environment variables

Everything the CLI reads from the environment. Anything not listed here is
ignored — the CLI will not tell you it ignored it.

## Running the CLI more than once on one machine

| Variable | Effect |
|---|---|
| `SEPAY_CONFIG_DIR` | Where config and the file-fallback credential live. Defaults to the OS config dir, below |
| `SEPAY_PROFILE` | Which stored profile to act as, same as `--profile` |
| `NO_COLOR` | Disable colour, same as `--no-color` |
| `SEPAY_NO_UPDATE_NOTIFIER` | Turn off the once-a-day "new release available" / "this version is deprecated" notice |

Unset, the directory is the OS's own user-config location plus `sepay`, so it is
not the same path on every machine:

| OS | `config.yml` and `hosts.yml` live in |
|---|---|
| Linux | `~/.config/sepay/` |
| macOS | `~/Library/Application Support/sepay/` |
| Windows | `%AppData%\sepay\` |

`sepay config get` prints from whichever of those applies; `--config <file>` reads
one exact file instead.

An on/off variable reads its value, not its presence: `0`, `false`, `no`, `off`
and empty all mean off, so `NO_COLOR=0` keeps colour on.

Once a day an interactive run asks the npm registry for the latest release and
for the version it is running. After the command finishes it prints one notice to
stderr when a newer release exists or when the running version is deprecated
(withdrawn, or found vulnerable), with the deprecation message. It never
installs anything, and it stays off when `CI` is set, when stderr is not a
terminal, under `--json` / `-o yaml` / `--quiet`, and for a dev build. The answer
is cached in `update-state.json` in the config directory.

`SEPAY_CONFIG_DIR` is the one to reach for in CI or when an agent runs
alongside a human's session: point it at a scratch directory and nothing you do
can touch the developer's logged-in profile.

There is no environment variable that supplies a token, and there will not be
one. A credential enters the CLI only through `sepay login`, so a leaked
environment cannot authenticate as you.

## Pointing at a different backend

| Variable | Effect |
|---|---|
| `SEPAY_SANDBOX_API_URL` · `SEPAY_PRODUCTION_API_URL` | API host for that one environment |
| `SEPAY_SANDBOX_PG_API_URL` · `SEPAY_PRODUCTION_PG_API_URL` | Payment Gateway REST API host |
| `SEPAY_SANDBOX_PG_CHECKOUT_URL` · `SEPAY_PRODUCTION_PG_CHECKOUT_URL` | Payment Gateway checkout host (`pg checkout init`) |
| `SEPAY_SANDBOX_BANKHUB_API_URL` · `SEPAY_PRODUCTION_BANKHUB_API_URL` | Bank Hub host |
| `SEPAY_SANDBOX_EINVOICE_API_URL` · `SEPAY_PRODUCTION_EINVOICE_API_URL` | e-invoice host |
| `SEPAY_OAUTH_URL` | OAuth host for `sepay login`. Single value — OAuth is not per-environment |

Each override names its environment, so pointing sandbox at a local mock cannot
accidentally move production too. Resolution order for the active environment
`E`, first match wins:

```
--api-base  >  SEPAY_{E}_API_URL  >  config <E>.api_url  >  built-in default
```

Any authenticated command refuses to send your token over plain http to a host
that is not loopback, and a sandbox-only command refuses any host other than the
sandbox host or loopback:

```
Error: refusing to send credentials over plaintext http to non-loopback
host "10.0.0.5"; use https
```

A backend on another machine is reached over https, or through an SSH tunnel
that makes it loopback. There is no switch that relaxes either refusal.

## Knowing which environment you are on

The environment follows the host, not a flag on the request, and sandbox and
production hold entirely separate data. Three ways to read it:

- `sepay --map=json` → `active_env` — use this in a script
- the `env: <env> → <host>` line commands print to stderr, which is suppressed
  under `--json`, `-o yaml` and `--quiet`
- the `[Agent guidance]` block in any `--help`

`--env` beats the stored default; `sepay config set default_env production`
moves that default and is confirmed before it takes effect.
