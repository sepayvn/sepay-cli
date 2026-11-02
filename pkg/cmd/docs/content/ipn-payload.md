# Payment Gateway IPN payload

What SePay POSTs to your IPN endpoint when a gateway order is paid, voided or
renewed, and what your handler has to send back.

This is a different channel from `sepay docs webhook-payload`: a webhook reports a
transfer on a bank account you own, an IPN reports the outcome of a gateway order.
The bodies share no fields, and a handler written for one will not parse the other.

## The body

```json
{
  "timestamp": 1769712000,
  "notification_type": "ORDER_PAID",
  "order": {
    "id": "0f9a1c2e-6b7d-4e5f-8a90-1b2c3d4e5f60",
    "order_id": "DHSEPAY001",
    "order_status": "CAPTURED",
    "order_currency": "VND",
    "order_amount": 100000,
    "order_invoice_number": "DHSEPAY001",
    "custom_data": {},
    "user_agent": "Mozilla/5.0",
    "ip_address": "203.0.113.9",
    "order_description": "Order DHSEPAY001"
  },
  "transaction": {
    "id": "7c1e2b3a-4d5f-6a7b-8c9d-0e1f2a3b4c5d",
    "payment_method": "CARD",
    "transaction_id": "1",
    "transaction_type": "PAYMENT",
    "transaction_date": "2026-07-30 11:08:33",
    "transaction_status": "APPROVED",
    "transaction_amount": 100000,
    "transaction_currency": "VND",
    "authentication_status": "AUTHENTICATION_SUCCESSFUL",
    "card_number": "xxxxxxxxxxxx1111",
    "card_holder_name": "NGUYEN VAN A",
    "card_expiry": "0130",
    "card_funding_method": "CREDIT",
    "card_brand": "VISA"
  },
  "customer": { "id": "…", "customer_id": "cus_1" },
  "agreement": null
}
```

`order.id` and `transaction.id` are SePay's uuids; `order.order_id` /
`order_invoice_number` is your own order number, and the field to reconcile on.
`customer` and `agreement` are `null` unless the order carried them — `agreement`
is populated only for an active auto-renew agreement, and `notification_type` is
`RENEWAL_ORDER_PAID` for its recurring charges.

## Event types

`notification_type` is one of:

| Value | When |
|---|---|
| `ORDER_PAID` | the order was captured |
| `RENEWAL_ORDER_PAID` | a recurring charge on an auto-renew agreement was captured |
| `TRANSACTION_VOID` | a card payment was voided before settlement |

The same three values are what `--events` accepts and what a delivery line shows.

## Authentication — there is no signature

SePay sends `X-Secret-Key: <your secret>` when the IPN config has
`auth_type: SECRET_KEY`, and nothing else. **The body is not signed.** Do not
write an HMAC check against this header: compare the whole value in constant
time, or allowlist SePay's IP ranges, or both.

`sepay pg ipn listen` replays that header, so your own check runs locally against
the real value. `--headers` prints the header names but redacts credential values,
because a terminal becomes scrollback and a CI log.

## What to send back

`200`, with a JSON body of `{"success": true}`.

Two behaviours worth knowing before you rely on them, because they are not what a
webhook does:

- **A `4xx` or `5xx` from your endpoint counts as delivered and is not retried.**
  Only a transport failure — a connect error or a timeout — is. So handle the
  notification before responding; an error status does not buy you a redelivery.
- **Retries repeat the same notification**, so deduplicate on `transaction.id`.

## What the stream carries

`sepay pg ipn listen` mirrors a delivery: the same body bytes and the same headers
your endpoint just received. Write your handler once and it behaves the same under
`listen` and in production.

Two limits worth knowing. It mirrors **deliveries**, so it needs an IPN endpoint
configured — `sepay pg ipn listen` says so at startup when none is. And it reports
**first attempts only**: a retry re-sends a notification you already saw.

A forward your local handler cannot receive is re-sent by the CLI on SePay's own
schedule, and mirrors the channel's rule above — a handler that answers 4xx or 5xx
counts as delivered and gets no retry, so only a connect error or timeout comes back.
Those attempts replay the original bytes and end with the last session holding them;
sandbox and loopback only. `--forward-retries 0` delivers once. Details:
`sepay docs webhook-payload`.

`card_number` is masked by the gateway itself in production (`4111XXXXXXXX1111`).
A sandbox order carries whatever card number was typed into the test checkout, so
treat sandbox output as sensitive.

## Configuring delivery

```bash
sepay pg ipn get                                             # current config, secret masked
sepay pg ipn set https://example.com/ipn --secret s3cret --yes   # changes only what you pass
```

`set` is a partial update: a key you omit keeps its stored value, so setting a URL
cannot turn authentication off. Pass `--auth-type ""` to remove it.

The URL must be publicly reachable — a loopback or private address is rejected.
Only one IPN config exists per merchant, and the secret cannot be read back once
set.

## Canonical reference

This topic covers what the CLI does with an IPN — what it mirrors, masks and
omits. The platform contract is on `developer.sepay.vn`:

| Subject | Page |
|---|---|
| IPN body, response, code samples | `/en/cong-thanh-toan/IPN` |
| Gateway auth and request signing | `/en/cong-thanh-toan/API/tong-quan` |
| Payment flow and order states | `/en/cong-thanh-toan/luong-thanh-toan` |
| Sandbox behaviour | `/en/cong-thanh-toan/sandbox` |

Prefix with `https://developer.sepay.vn`; append `.md` for raw Markdown an agent
can fetch. Every page: `https://developer.sepay.vn/llms.txt`.
