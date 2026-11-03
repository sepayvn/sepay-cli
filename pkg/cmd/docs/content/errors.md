# Errors

The CLI maps the error codes below to an English message and an exit code. A
code it does not know falls through with the server's own message, which is
often Vietnamese — as are some success lines that come from the server. Branch
on the exit code and the `error_code`, never on the message text.

## SePay API

| Server code | Exit | Meaning and fix |
|---|:--:|---|
| `unauthorized` | 4 | The session expired or was revoked. `sepay login` |
| `insufficient_scope` | 5 | The token cannot do this on production. `sepay auth refresh --scopes <scope>` |
| `network_error` | 1 | No usable answer came back: no connection, a DNS failure, a refused or cut-off connection, a timeout or an untrusted certificate. Calls to the SePay API are tried up to 3 more times first, except a create without an idempotency key and a conditional write (`paycode set`) whose first attempt may have landed; sign-in, token and product calls fail on the first try, and `webhook listen` reconnects on its own. Check your internet connection and run it again. A request you cancel with Ctrl+C is `interrupted`, not this. Set by the CLI, never by the server |
| `rate_limited` | 1 | Too many requests. The CLI honours `Retry-After` and retries automatically |
| `validation_error` | 1 | A field was rejected; the CLI lists the offending fields |
| `feature_disabled` | 8 | The CLI is not enabled for your company on production. SePay turns it on |
| `test_mode_disabled` | 8 | Test Mode is off for your company, so the sandbox API refuses this. SePay turns it on — logging in again cannot fix it. A product credential already minted keeps working, so `pg` / `bankhub` / `einvoice` calls are unaffected until it expires |
| `sandbox_not_provisioned` | 8 | Your company has no sandbox environment yet. SePay provisions it |
| `forbidden` | 8 | Your account lacks the role or the per-bank permission for this action. An admin of your company grants it |
| `insufficient_role` | 8 | This action is restricted to a company Admin or SuperAdmin. Ask one to run it |
| `multiple_companies` | 1 | Your account belongs to more than one company; the CLI cannot pick one for you |
| `sms_not_allowed` | 1 | Your plan has no SMS connections (SMS is deprecated). Create the account with `--connection api` |
| `connection_not_supported` | 1 | The bank has no API connection. Pick an API bank, or use `--connection sms` on a plan that still includes SMS |
| `server_error` | 1 | SePay failed on its side (HTTP 5xx) without naming a code. Retry in a moment; if it keeps failing, give SePay support the `request_id` from `-o json` (also printed in the message) when the host sent one. A 5xx that does carry a code keeps that code, and the same advice applies |
| `access_revoked` | 4 | Your access to the listen stream was revoked (removed from the company, or a session ended) after the request was authorised. Run `sepay login`; the CLI does not reconnect |
| `replay_not_dispatched` | 1 | `webhook replay` was refused and nothing was sent: the original delivery was skipped by a filter, the webhook is off, the transaction is gone, or another replay of the same pair is still running. The server's reason is printed |
| `otp_required` | 7 | The CLI cannot answer an OTP challenge; do this action in the web app |
| `unsupported_api_version` | 1 | This CLI is older than the server accepts. Upgrade |
| `not_found` | 1 | Not a missing record: the endpoint exists only in the sandbox. The server's message names the remedy, so it is passed through as sent |

An unrecognised 403 also exits 8: without a known code the CLI will not send you
off to widen a scope that may not be the problem.

Exit 4 means the credential is the problem, so `sepay login` is the fix. Exit 8
means the credential is fine and your company's configuration is not, so someone
else has to act — the two sandbox codes above are the common case.

A `404` on a resource you believe exists usually means the wrong environment,
not a missing record: sandbox and production hold entirely separate data. Check
the `env:` line the CLI prints to stderr.

Bank Hub and e-invoice answer a missing record with a code of their own, and the
CLI maps each to English. `error_code` is unchanged, so a script still branches on
what the server sent:

| Server code | Exit | Message |
|---|:--:|---|
| `404` (Bank Hub, which mirrors the HTTP status into its code field) | 1 | not found, or not visible to this merchant |
| `EINVOICE_DOCUMENT_NOT_FOUND` | 1 | no invoice with that reference code |
| `EINVOICE_ACCOUNT_NOT_FOUND` | 1 | this company has no e-invoice provider account |

`bankhub link-token get` and `pg order get` answer with the generic code `error`,
which covers more than a missing record and so cannot be mapped: both still
surface the server's own message.

## Payment Gateway

`pg` reports business failures with its own codes:

| Code | Meaning |
|---|---|
| `4000` | The order has not been paid, so there is nothing to void |
| `4001` | The order was already cancelled |
| `4002` | No payment transaction exists on the order |
| `2070` | Partial success: the payment was voided, but cancelling its auto-renew agreement failed |

`4000`, `4001` and `4002` are mapped to English; other PG failures surface with
the gateway's own Vietnamese message.

`2070` is not a failure of the void. The money is reversed; the agreement needs
a separate cancel. The CLI reports it as a warning and includes the agreement
id.

## Bank Hub

A `401` mid-session is normal and handled: the Bank Hub credential is
short-lived and the CLI re-mints it. A `401` that persists means the merchant is
not enabled for hosted link, or is inactive.

## Reading errors as a script

Match on the exit code and `error_code`, never on the message — messages are
translated and may change. Under `--json` / `-o yaml` the failure is printed to
stderr as an object carrying both (see `sepay docs exit-codes`), stdout stays
data-only, and no error ever contains a token.
