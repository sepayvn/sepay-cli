# Exit codes

Script against the exit code and `error_code`, not the message text.

| Code | Meaning | What to do |
|---|---|---|
| 0 | Success | |
| 1 | Server error, network error or rejected input | Branch on `error_code` before retrying |
| 2 | Bad flags, bad arguments, unknown command | Fix the command |
| 3 | The command has no backend | Do not retry |
| 4 | Not logged in | `sepay login` |
| 5 | The token lacks the required scope | `sepay auth refresh --scopes <scope>` |
| 6 | Your local handler did not accept the event (`webhook test` only) | Fix the handler, then retry |
| 7 | The server requires an OTP | Do the action in the web app |
| 8 | Your account or company is not allowed to do this | Ask SePay or a company admin |
| 130 | Interrupted: Ctrl+C, Esc at a prompt, SIGINT or SIGTERM | Run it again |

Both 5 and 8 come from an HTTP 403: you can fix 5 yourself, 8 needs someone else. Code 8 covers
the CLI not being enabled for the company, Test Mode being off, an unprovisioned sandbox, a
missing permission and a listen feature SePay has switched off.

## Retrying

Codes 2 to 8 need a change before another attempt; a retry loop on them never ends. Exit 1 is
retryable for `network_error` and `server_error`, never for `validation_error`. Cap the attempts.

## Errors under `--json`

Errors go to stderr and results to stdout. With `--json` or `-o yaml` the error is an object, bad
flags and argument counts included. The production warning is left out there, and so are retry
notes unless `--verbose` is on:

```json
{
  "status": "error",
  "error_code": "feature_disabled",
  "message": "the CLI is not enabled for your company on production; ask SePay to turn it on",
  "exit_code": 8
}
```

| Field | Value |
|---|---|
| `error_code` | The server's code, or a local one: `usage_error`, `not_implemented`, `unauthenticated`, `insufficient_scope`, `interrupted`, `network_error`, `error` |
| `errors` | On `validation_error` only: field name to message |
| `request_id` | When the API sent one; quote it to SePay support for a 5xx |

A 403 without a code becomes `blocked`, a 5xx without one `server_error`. `network_error` means no
usable answer came back: no connection, DNS failure, refused or cut connection, timeout or an
untrusted certificate.

Exit 6 has no error object: `webhook test` writes its result to stdout
(`{"event": …, "status_code": …, "latency_ms": …, "ok": false}`) and the reason to stderr. Branch
on `ok`.

`einvoice invoice create-status` and `issue-status` exit 1 on a `Failed` invoice with no error
object either: the result goes to stdout (`{"reference_code": …, "status": "Failed", "message": …}`),
`message` holding the provider's reason, and stderr stays empty. Branch on `status`; `Pending` and
`Success` exit 0.

A command with no backend is hidden from `--help`; `sepay --map=json` lists it with
`"implemented": false`.
