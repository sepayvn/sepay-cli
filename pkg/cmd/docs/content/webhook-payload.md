# Webhook payload

What SePay POSTs to your handler, and what your handler has to send back.
`webhook listen` and `webhook test` replay these bytes verbatim, so a handler
that works under them works in production.

## The body

```json
{
  "id": 92704,
  "gateway": "Vietcombank",
  "transactionDate": "2026-07-28 11:08:33",
  "accountNumber": "1017588888",
  "subAccount": "",
  "code": "DH000123",
  "content": "DH000123 thanh toan don hang",
  "transferType": "in",
  "description": "NGUYEN VAN A chuyen tien",
  "transferAmount": 5000000,
  "accumulated": 105000000,
  "referenceCode": "FT26012345678"
}
```

Enough to orient: `id` is the transaction id to deduplicate on — a number, and
not the identifier the API takes (see below), `transferAmount` is always positive
with `transferType` giving direction, `subAccount` is the virtual account and
`code` the extracted pay code. `content`, `description` and `gateway` carry
Vietnamese as UTF-8.

**The field list is a platform contract, not a CLI one** — types, formats and any
field added later live on `/en/sepay-webhooks/tich-hop-webhook`. Check it there
rather than trusting this example to be current.

A Bank Hub notification is a **different** body on its own page — snake_case, with
`transfer_type: credit|debit` where this one has `transferType: in|out`. If you
are building against Bank Hub, read `/en/bankhub/thong-bao-bien-dong-so-du`, not
this topic.

## Matching a transfer to your order

Use `subAccount` when it is set, then fall back to `code`. Both arrive as empty
strings rather than `null` when they do not apply, so test for truthiness, not
for `null`:

```js
const key = event.subAccount || event.code;
```

That order means a later switch from pay codes to virtual accounts changes only
how you provision, not your handler. Do not parse the order id out of `content`
yourself — SePay has already extracted it into `code`. Do not match on amount
plus timestamp; two buyers paying the same amount in the same minute is normal,
and mis-matching a payment is not recoverable.

See `sepay docs glossary` for what a pay code and a VA are.

## Looking the transaction up afterwards

`id` deduplicates, but it is not what the API accepts. Every `sepay` command
takes the opaque `xid` string a list returns, so the number from a payload does
not resolve:

```console
$ sepay transaction get 99108
Error: This transaction was not found
```

`referenceCode` is what bridges the two: it is the bank's reference, and it
matches a transaction's `reference_number`.

```bash
sepay transaction list --reference-number SBB2FA70FF5F17
```

So store `referenceCode` alongside `id` in your handler. Storing only `id` leaves
you able to deduplicate but not to reconcile, which is the half you need when a
payment is disputed.

## Headers

`Content-Type` follows the webhook's `--content-type`: `application/json`,
`application/x-www-form-urlencoded`, or `multipart/form-data`.

Authentication follows `--auth-type`:

| `--auth-type` | Header |
|---|---|
| `none` | — |
| `apikey` | `Authorization: Apikey <your --secret>` |
| `oauth2` | `Authorization: Bearer <token SePay fetched from your token URL>` |
| `hmac` | `X-SePay-Signature: sha256=<hex>` and `X-SePay-Timestamp: <unix seconds>` |

Header names are case-insensitive and most stacks normalise them — a handler
behind `webhook listen` sees `X-Sepay-Signature`. Look them up case-insensitively
rather than by exact string.

## Webhooks with no URL

`sepay webhook create --listen-only` (sandbox) makes a webhook that has no
destination: SePay sends no HTTP request for it, and the event reaches you only
through `sepay webhook listen`. The body and the headers above are unchanged — they
are what a delivery to a URL would have sent — so a handler that verifies the HMAC
signature runs the same code path either way. `oauth2` is the exception: fetching a
token is itself an outbound request, so it is not available in this mode.

## Verifying the HMAC signature

The signature is over the timestamp, a literal `.`, and the exact request body:

```js
const signed = `${req.header('X-SePay-Timestamp')}.${rawBody}`;
const expected = 'sha256=' + crypto.createHmac('sha256', secret).update(signed).digest('hex');
```

Compare with a constant-time equality, and read the **raw** body — a body that
has been parsed and re-serialized will not match. `webhook listen` forwards the
original bytes for exactly this reason, so the check runs the real path locally.

Full auth reference, including the OAuth2 and API-key variants:
`https://developer.sepay.vn/en/sepay-webhooks/xac-thuc`.

## What to return

**HTTP 200 or 201, with a JSON body of `{"success": true}`, within 30 seconds.**
Anything else — including a 204, or a 200 with an empty body — is not a success.

Return it for every event you have made a decision about, including "no order
matches this code" and "already processed". That is a decision, not a failure.

A handler that does slow work should acknowledge first and process afterwards.
Deduplicate on `id`: redelivery is normal, and crediting an order twice is the
failure that costs money.

Retry behaviour (when it applies, how many attempts, the backoff) is a platform
rule, not a CLI one — `https://developer.sepay.vn/en/sepay-webhooks/xu-ly-loi`.

Under `webhook listen` the handler's status is printed per event, so a wrong
status is visible immediately. `webhook test` turns the same check into a CI step
and exits 6 when the status is wrong — pass `--expect-status 200`.

## What the stream carries

`sepay webhook listen` mirrors a delivery: the same body bytes and the same headers
your endpoint just received, so one handler behaves the same under `listen` and in
production.

It carries **first attempts**, plus a replay you asked for with
`sepay webhook replay` (sandbox). An **automatic retry never reaches the stream** —
a failing endpoint retries over hours, and mirroring every attempt would show the
same transaction two to four times. So SePay's redelivery to your registered URL
appears **once** here while the backend keeps retrying behind it, and on production,
where replay is unavailable, the stream is first attempts only.

Every attempt SePay made is recorded: `sepay webhook logs` has one row per attempt,
with `type` (`First` or `Retry`) and `is_replay`. Read the log, not the stream, to
answer "how many times did SePay deliver this".

## Retrying a failed forward locally

When your handler fails a forward, `listen` re-sends it on SePay's own schedule —
seven more attempts by default, the last a little over half an hour out — so you can
build the idempotency the real channel demands. `--forward-retries <n>` sets the count
(`0` delivers once, maximum 10). The gap grows with each attempt, so raising the count
lengthens the run steeply: 10 attempts spans about 2.5 hours, not half an hour.

These attempts are the CLI's own, and differ from SePay's in ways worth knowing:

- **The replay is byte-identical**, including `X-SePay-Signature` and
  `X-SePay-Timestamp`. SePay re-signs every retry with a fresh timestamp; the CLI
  holds no secret and cannot. So if your handler rejects a stale timestamp — which
  it should — late local attempts fail where production's would pass. That is the
  emulation's limit, not a bug in your handler.
- **A non-2xx retries only where your webhook says so.** A transport failure always
  comes back. A handler that *answered* comes back only when that webhook has
  `retry_conditions.non_2xx_status_code` on — the same switch SePay itself reads.
  Never configured means transport failures only. On the gateway-IPN channel a non-2xx
  never retries, here or in production. The value is not in the `webhook list` table;
  read it with `sepay webhook list --json` or `sepay webhook get <xid> --json`.
- **The switch is read once, at startup.** Changing `retry_conditions` in the portal
  does not reach a session already running — restart `listen` to pick it up.
- **The delivery itself is never written to disk.** Ending the last session holding a
  chain drops it and says how many were lost, and a restart cannot resume one: replaying
  a delivery needs its body and its `X-Secret-Key`, and neither is stored. What is
  waiting is capped — 24 MiB of held payload and 500 deliveries — and past that a failed
  forward is simply not retried, with one warning saying so.
- **Sandbox and loopback only.** On production SePay is already re-sending the same
  delivery to the same handler, so the CLI stays out of the way; an off-machine
  target is excluded too. Either case prints a note and delivers once.
- **Several shells on one handler retry it once, not once each.** A channel fans out to
  every shell listening on it, so the *first* delivery does reach your handler once per
  shell. `--webhook <xid>` splits them only when you have a webhook per shell to give;
  with one webhook and two terminals — the usual way this happens while developing —
  the first delivery still arrives twice, so deduplicating on `id` is the thing that
  actually saves you. The retries are coordinated
  through a small state file: one shell sends each attempt and the others print the
  answer it recorded. A shell opened *after* a delivery failed never receives that frame,
  so it arms nothing; it still follows the chains the others hold — `retries` lists them
  as `another session`, `kill <n>` reaches them, and it reports each attempt as it lands.
  It cannot send one, for the same reason a restart cannot resume: it has no delivery to
  send. When no shell is left holding a chain, whichever is still open says so
  (`#3 abandoned — the session holding it exited`), within a few seconds, rather than
  leaving it to vanish. That note goes to stderr, so a piped session reports it too;
  what a piped session will not do is reprint another shell's delivery onto its stdout,
  so a parsed stream still carries only what this shell forwarded.

The platform's own retry rule — when it applies, how many attempts, the backoff —
is documented at `/en/sepay-webhooks/xu-ly-loi`.

## One transaction, several deliveries

A transaction is delivered once per webhook that **targets its bank account**,
each signed with that webhook's own secret. A webhook targeting a different
account delivers nothing, which is the usual reason a transfer produces no
callback at all. Three matching webhooks means three POSTs carrying
the same `id`, and a handler that verifies the signature will reject the two
signed with secrets it does not hold.

Each event names its origin, so you can keep only yours:

```bash
sepay webhook listen --forward localhost:3000 --webhook <xid>
sepay webhook test   --forward localhost:3000 --webhook <xid> --expect-status 200
```

`listen` tags every line with `txn=<transaction xid>` and `wh=<webhook xid>`, so one
transaction reaching three webhooks reads as one `txn` against three `wh`. Both values
are printed whole: `txn` goes straight into `sepay transaction get`, `wh` into `sepay webhook get`. Without `--webhook`,
`webhook test` reports which delivery it judged. `sepay webhook list` has the
xids. Deduplicate on `id` regardless — redelivery is normal on its own.

## Canonical reference

This topic covers what the CLI does with a delivery — the bytes it replays, the
status it asserts, the origin it tags. The platform contract itself lives on
`developer.sepay.vn`, which is where to check a field or a rule:

| Subject | Page |
|---|---|
| **Body: every field, type and format** | `/en/sepay-webhooks/tich-hop-webhook` |
| Response rule, dedup, code samples | `/en/sepay-webhooks/tich-hop-webhook` |
| Bank Hub balance notification (a different body) | `/en/bankhub/thong-bao-bien-dong-so-du` |
| Auth methods and HMAC | `/en/sepay-webhooks/xac-thuc` |
| Retries and failure handling | `/en/sepay-webhooks/xu-ly-loi` |
| Security hardening | `/en/sepay-webhooks/bao-mat` |
| Payment-code configuration | `/en/sepay-webhooks/cau-hinh-ma-thanh-toan` |
| Monitoring deliveries | `/en/sepay-webhooks/giam-sat` |
| Reconciling transactions | `/en/sepay-webhooks/doi-soat-giao-dich` |
| Test mode vs live | `/en/sepay-webhooks/test-mode/so-sanh-voi-live` |

Prefix with `https://developer.sepay.vn`; append `.md` for raw Markdown an agent
can fetch, and swap `/en/` for `/vi/` to read it in Vietnamese. Every page:
`https://developer.sepay.vn/llms.txt`.
