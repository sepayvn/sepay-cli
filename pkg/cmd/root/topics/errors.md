# Errors

The CLI turns most codes below into an English message, and gives some their own exit code. Any
other code passes through with the server's message, which may be in Vietnamese, and exits 4 on a
401, 8 on a 403 and 1 otherwise. Scripts branch on the exit code and `error_code` (see
`sepay help exit-codes`), never on the message. No error contains a token.

## SePay API

| Code | Exit | Meaning and fix |
|---|:--:|---|
| `unauthorized` | 4 | The session expired or was revoked. Run `sepay login` |
| `access_revoked` | 4 | Access to the `webhook listen` stream was revoked after it opened. Run `sepay login`; the CLI does not reconnect |
| `insufficient_scope` | 5 | The token lacks a scope on production. Run `sepay auth refresh --scopes <scope>` |
| `otp_required` | 7 | Do the action in the web app |
| `feature_disabled` | 8 | The CLI is not enabled for your company on production. SePay turns it on |
| `test_mode_disabled` | 8 | Test Mode is off for your company. SePay turns it on; logging in again does not help. A `pg`, `bankhub` or `einvoice` credential already issued keeps working until it expires |
| `sandbox_not_provisioned` | 8 | Your company has no sandbox yet. SePay provisions it |
| `forbidden` | 8 | Your account lacks the role or bank permission. A company admin grants it |
| `insufficient_role` | 8 | Only a company Admin or SuperAdmin can do this. The server's message is printed |
| `sse_hub_disabled` | 8 | `webhook listen`, `pg ipn listen` or `bankhub ipn listen` is switched off on SePay's side; it works again once SePay turns it back on |
| `quota_exceeded` | 1 | A sandbox quota is used up; the server's message says which. See `sepay quota` |
| `validation_error` | 1 | A field was rejected; the message lists the fields |
| `rate_limited` | 1 | Too many requests. The CLI waits for `Retry-After` and retries |
| `network_error` | 1 | No usable answer came back. Set by the CLI; see below |
| `server_error` | 1 | HTTP 5xx without a code. Retry later; if it persists, give SePay support the `request_id` |
| `multiple_companies` | 1 | Your account belongs to several companies |
| `sms_not_allowed` | 1 | Your plan has no SMS connections. Use `--connection api` |
| `connection_not_supported` | 1 | The bank has no API connection. Pick another bank, or `--connection sms` on a plan with SMS |
| `replay_not_dispatched` | 1 | `webhook replay` sent nothing: the delivery was filtered, the webhook is off, the transaction is gone, or the same replay is still running. The server's reason is printed |
| `unsupported_api_version` | 1 | The CLI is too old for the server. Upgrade |
| `not_found` | 1 | The endpoint exists only in the sandbox; the server's message says what to do |
| `paycode_prefix_exists` | 1 | The company already has a pay-code structure with that prefix, letter case aside. Change it with `sepay paycode update --structure <prefix>` |
| `paycode_version_conflict` | 1 | The pay-code configuration changed since it was read; nothing was saved. Run the command again, with a new `--idempotency-key` if you passed one |

### Sandbox product credentials

A `pg`, `bankhub` or `einvoice` command first asks the SePay API for its product credential. A
failure there answers 500 with one generic message, so only the code tells the steps apart.

| Code | Exit | Meaning and fix |
|---|:--:|---|
| `pg_provision_failed`, `bankhub_provision_failed`, `einvoice_provision_failed` | 1 | Setting up the sandbox product failed server-side. Retry, then report it to SePay |
| `bankhub_token_failed`, `einvoice_token_failed`, `product_token_failed` | 1 | The product exists but SePay could not issue its credential. Retry, then report it |
| `live_company_unresolved` | 1 | SePay could not resolve your company for this request. Run `sepay login` |

A 403 with an unknown code exits 8, not 5: widening a scope would not fix it. Exit 4 means the
credential is wrong and `sepay login` fixes it; exit 8 means your company's settings must change.

A 404 for a record you know exists usually means the wrong environment: sandbox and production
hold separate data. Check the `env:` line on stderr.

### Retries before `network_error`

SePay API calls are retried up to 3 times, except a create without an idempotency key and a
conditional write (`paycode update`), which may already have been applied. Sign-in, token and
`pg`, `bankhub`, `einvoice` calls are not retried, and `webhook listen` reconnects by itself. A
request you cancel with Ctrl+C is `interrupted`.

## Bank Hub and e-invoice

A missing record has its own code. The message is in English; `error_code` stays as the server
sent it.

| Code | Exit | Message |
|---|:--:|---|
| `404` (Bank Hub copies the HTTP status into its code) | 1 | not found, or not visible to this merchant |
| `EINVOICE_DOCUMENT_NOT_FOUND` | 1 | no invoice with that reference code |
| `EINVOICE_ACCOUNT_NOT_FOUND` | 1 | this company has no e-invoice provider account |

`bankhub link-token get` and `pg order get` return the generic code `error`, so they print the
server's message.

The Bank Hub credential is short-lived and the CLI renews it on a `401`. A `401` that persists
means the merchant is inactive or not enabled for hosted link.

## Payment Gateway

| Code | Meaning |
|---|---|
| `4000` | The order is unpaid, so there is nothing to void |
| `4001` | The order is already cancelled |
| `4002` | The order has no payment transaction |
| `2070` | The payment was voided but its auto-renew agreement was not cancelled |

`4000`, `4001` and `4002` have English messages; other gateway codes print the gateway's
Vietnamese message. `2070` is a warning, not a failure: the money is returned, and the warning
names the agreement id to cancel separately.
