# Exit codes

Every command exits with one of these. They are stable — script against them
rather than matching on message text.

| Code | Meaning | What to do |
|---|---|---|
| 0 | Success | — |
| 1 | Everything else: a server error, but also a rejected field | Read the message before retrying |
| 2 | Bad flags, bad arguments, unknown command | Fix the command; do not retry |
| 3 | The command exists but has no backend | Do not retry, and do not report it as a failure of your input |
| 4 | Not authenticated | `sepay login` |
| 5 | Token lacks the required scope | `sepay auth refresh --scopes <scope>` — you can fix this yourself |
| 6 | Your local handler failed to accept a forwarded event | Fix the handler; only `webhook test` returns it |
| 7 | The server demanded an OTP | The CLI cannot answer one; do this action in the web app |
| 8 | Your account or company is not allowed to do this | Someone else must act: SePay enables the CLI for the company, turns Test Mode on, or provisions the sandbox; or an admin grants you the permission |
| 130 | Interrupted with Ctrl+C | Nothing printed in a terminal; rerun when ready |

5 and 8 both come back as HTTP 403 and are split on purpose: 5 is yours to fix,
8 is not.

## Notes for scripts

Only 6 is safely retryable after you fix your handler. 2, 3, 4, 5, 7 and 8 all
need a change before another attempt — a retry loop on them spins forever.

Exit 1 is the one to be careful with: it covers a transient server error, where
retrying helps, and a `validation_error`, where retrying the same input never
will. Branch on `error_code`, and cap your attempts.

Errors go to stderr and normal output to stdout, so `--json` output stays
parseable when a command fails. Under `--json` or `-o yaml` the error itself is
structured — including a bad flag or argument count, rejected before the command
runs — so nothing has to be read out of prose:

```json
{
  "status": "error",
  "error_code": "feature_disabled",
  "message": "the CLI is not enabled for your company on production; ask SePay to turn it on",
  "exit_code": 8
}
```

`error_code` is the server's code when the request reached the API, otherwise a
name for the local failure (`usage_error`, `not_implemented`, `unauthenticated`,
`insufficient_scope`, `interrupted`, `network_error`, `error`). `network_error` means no usable answer
came back (no connection, DNS, refused or cut-off connection, timeout, untrusted certificate); exit 1, worth retrying. A 403 carrying no code of its own falls back to
`blocked`, and a 5xx carrying none to `server_error`. A `validation_error` also
carries an `errors` object of field to message, and `request_id` is present
whenever the API host sent one: quote it to SePay support for a 5xx.

Exit 6 is the one failure with no envelope. `webhook test` has already written
its result to **stdout** — `{"event": …, "status_code": …, "latency_ms": …, "ok":
false}` — with the diagnostic on stderr, so branch on `ok` there.

A command with no backend is hidden from `--help` and exits 3. `sepay --map=json`
lists it with `"implemented": false`, so the full surface is visible without
discovering it by trial and error.
