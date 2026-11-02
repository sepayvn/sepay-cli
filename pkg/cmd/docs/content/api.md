# Hosts and credentials

What an application sends when it calls SePay directly instead of through this
CLI. Each API has its own host and its own credential; a credential for one is
not accepted by another.

## SePay API v2

Bank accounts, transactions, virtual accounts, webhooks, pay codes — the API
behind most `sepay` commands.

| Environment | Base URL |
|---|---|
| sandbox | `https://cli-api-sandbox.sepay.vn/v2` |
| production | `https://cli-api.sepay.vn/v2` |

```
Authorization: Bearer <credential>
```

Two credentials fit that header, and the server branches on their shape:

| Credential | Shape | Granted |
|---|---|---|
| static API key | 64 alphanumeric characters | the whole company; capability scopes, per-user permissions and the production-write feature flag do not apply |
| OAuth2 access token | a JWT (contains dots) | narrowed by capability scopes — `sepay docs environment` |

Obtaining a key:

- sandbox — `sepay apikey create --name <name>` prints it, and is the only
  command that can: the server returns a `****`-prefixed hint everywhere else, so
  store it then or create a replacement. The company needs test mode on, or the
  key authenticates with `Test Mode is not enabled for this company`.
- production — created in the my.sepay.vn portal. The key endpoints themselves
  are sandbox-only.

### Parameter names

A list command's filter flags are the query parameters: drop the dashes and
replace `-` with `_`. `--transaction-date-from` is `transaction_date_from`,
`--transfer-type` is `transfer_type`, `--per-page` is `per_page`. A flag that
selects a path segment — `va list --bank-account` — is part of the URL instead.

```bash
sepay transaction list --transaction-date-from 2026-07-01 --transfer-type in -v
sepay api /transactions -f transfer_type=in -f per_page=5
```

`-v` prints the request URL the CLI sent. `sepay api <path>` sends one directly:
`--field` values are query parameters on a GET, a JSON body on a write.

### Envelope

```json
{
  "status": "success",
  "data": [],
  "meta": { "pagination": { "total": 0, "per_page": 20, "current_page": 1, "last_page": 1, "has_more": false } }
}
```

- `data` is an array for a list, an object for a single record. A delete answers
  `204` with no body.
- Pagination is `page` and `per_page`, capped at 100. The end of a list is
  `meta.pagination.has_more`, not a short page.
- `since_id` on the transaction list returns the rows after an xid.
- Identifiers are opaque `xid` strings, taken from a list.
- `X-Idempotency-Key` is required on four writes — creating a bank account,
  creating a webhook, replaying a webhook log, and issuing or revoking a VA — and
  missing it fails `400 idempotency_key_required`. One key per logical operation,
  reused across its retries. Every other write, deleting a bank account or a
  webhook included, does not take it.
- `X-SePay-Api-Version` is optional; a value the server does not speak fails
  `400 unsupported_api_version`. Today it speaks `2025-07-01`.
- Money is an integer number of VND. Text carries Vietnamese as UTF-8.
- An error is `{"status":"error","message":…,"error_code":…}`. Codes and their
  fixes: `sepay docs errors`.

## The other APIs

| API | Host — sandbox · production | Credential |
|---|---|---|
| Payment Gateway (REST) | `pgapi-sandbox` · `pgapi` .sepay.vn | HTTP Basic `merchant_id:secret_key` |
| Payment Gateway (checkout) | `pay-sandbox` · `pay` .sepay.vn | a signature over the form fields; no Basic header |
| Bank Hub | `bankhub-api-sandbox` · `bankhub-api` .sepay.vn | short-lived opaque Bearer token, minted per session |
| e-invoice | `einvoice-api-sandbox` · `einvoice-api` .sepay.vn | opaque Bearer token, ~24h |

Their envelopes differ from API v2's, and each mints its credential its own way.
The pages below carry both.

## Reference docs

`developer.sepay.vn` is the authority for every platform contract — payload
fields, response rules, retry curves, signatures, per-bank behaviour. This topic
does not restate them, because a copy here goes stale the moment the site moves a
page and a wrong copy is worse than a link.

Two URL forms serve the same page:

| Reader | Form | Example |
|---|---|---|
| a person, in a browser | no suffix | `https://developer.sepay.vn/en/sepay-webhooks/xac-thuc` |
| an agent or a script | append `.md` | `https://developer.sepay.vn/en/sepay-webhooks/xac-thuc.md` |

Paths below are `/en`. The same page in Vietnamese is the identical path under `/vi`.

**The complete, always-current index of every page is
`https://developer.sepay.vn`.** Fetch that to find a page rather than
guessing a path.

### Start here, per product

| Product | Overview | Credential |
|---|---|---|
| SePay API v2 | `/en/sepay-api/v2/gioi-thieu` | `/en/sepay-api/v2/xac-thuc` · `/en/sepay-api/v2/tao-api-token` |
| Webhooks | `/en/sepay-webhooks` | `/en/sepay-webhooks/xac-thuc` |
| Payment Gateway | `/en/cong-thanh-toan/gioi-thieu` | `/en/cong-thanh-toan/API/tong-quan` |
| Bank Hub | `/en/bankhub/tong-quan` | `/en/bankhub/api/tao-token` |
| e-invoice | `/en/einvoice-api/v1/tong-quan` | `/en/einvoice-api/v1/tao-token` |
| Test mode | `/en/tien-ich-khac/test-mode` | — |

### The notification contracts

What a server receives is the part most worth reading in full before writing a
handler, and each product has its own:

| Channel | Page |
|---|---|
| Webhook — body, response rule, dedup | `/en/sepay-webhooks/tich-hop-webhook` |
| Webhook — auth methods and HMAC | `/en/sepay-webhooks/xac-thuc` |
| Webhook — retries and failure handling | `/en/sepay-webhooks/xu-ly-loi` |
| Payment Gateway — IPN | `/en/cong-thanh-toan/IPN` |
| Bank Hub — IPN (a balance-change notification) | `/en/bankhub/thong-bao-bien-dong-so-du` |
| Bank Hub — link-event webhook | `/en/bankhub/webhook-event` |
