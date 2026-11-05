# Glossary

The SePay terms the commands use. Links point to `developer.sepay.vn`; append `.md` to a page for
Markdown.

## Core

**environment**: `sandbox` (the default) or `production`. Each has its own host and its own data;
sandbox moves no real money. `--env` picks one.

**profile**: one stored login, an account and its company. `sepay login` adds one; `--profile` or
`sepay auth switch` chooses between them.

**xid**: the public id of a resource. Commands take xids, never database ids, and name them by
type (`ba_xid` is a bank account). `pg order` takes an invoice number instead.

**scope**: a permission the token carries: `read`, `webhook:write`, `bank-account:write`,
`paycode:write`. Scopes apply on production only; in sandbox a login can read and write without
them. Add one with `sepay auth refresh --scopes <scope>`.

**OTP**: a one-time code the server can require for some production writes on an official VA. The
CLI cannot answer one: the command exits 7 and the action is done in the web app. The commands
that could trigger an OTP are sandbox-only, and sandbox never asks for one.

## Banking

**bank account**: your bank account that SePay watches. `sepay bank-account`.

**transaction**: money in or out of a bank account, as the bank reported it. Read-only.
`sepay transaction`.

**paycode**: a code the payer writes in the transfer memo so SePay can match the transfer to your
order. You set its shape, not the codes: a prefix of 2 to 5 letters and a suffix of 1 to 30
digits or letters. A new prefix on production applies at once, so transfers that carry the old
prefix stop matching. `sepay paycode`.

**VA (virtual account)**: an extra account number under one of your bank accounts. Give each
order or customer its own VA and a transfer is matched by where it arrives, with nothing to type.
- An **official** VA is issued by the bank and reported through its API; it exists only where the
  bank offers VAs for that account (`official_va_supported`).
- A **non-official** VA is created by SePay and recognised from the transfer content, over API,
  SMS or both.

The parent bank account's setup decides the kind; `va list --official 1` filters by it. A bank
account holds up to 100 VAs. `sepay va`.

**webhook**: your HTTPS endpoint that SePay calls for each matching transaction.
`sepay webhook listen` sends the same events to a handler on your machine instead, with no public
URL. `sepay webhook`.

**quota**: the limits on your account, shown by `sepay quota`.
- Sandbox (Test Mode): 500 transactions a day (reset at 00:00 UTC+7), 50 bank accounts,
  50 webhooks, 50 API keys and 100 VAs per bank account. A write over a limit is rejected
  (`enforcement: blocked`).
- Production: needs an Admin or SuperAdmin and reports only the plan's transaction allowance for
  the billing cycle. What happens past it is set by your plan, not by the CLI.

| `enforcement` | Meaning |
|---|---|
| `capped` | The allowance applies; `remaining` counts down |
| `unlimited` | The plan has no allowance |
| `exempt` | The allowance applies but is not enforced: going past it does not stop transactions |
| `unknown` | The subscription has no billing window, so usage cannot be measured |

`capped` and `exempt` report `limit` and `remaining`; read `enforcement` to know whether
reaching the limit stops anything.

## pg: Payment Gateway

A hosted checkout for card payments and bank transfers that you start for a buyer. Sandbox only.

**order**: one payment, addressed by its invoice number. `pg order list --status` takes
`AUTHENTICATION_NOT_NEEDED` (waiting for payment), `CAPTURED` (paid) or `CANCELLED` (cancelled or
voided). The gateway has more states for authentication, disputes, refunds and payouts, so read
`order_status` as it comes.

**cancel** (`sepay pg order cancel`): abandon an unpaid bank-transfer order, unless it is
`CAPTURED` or `CANCELLED`. It cannot be undone.

**void** (`sepay pg order void`): reverse a `CAPTURED` card payment before settlement.

**settlement**: when a captured card payment is passed to the bank. After it, only a refund
returns the money. The CLI does not report when a payment settles.

**IPN**: the gateway's notification when an order is paid, renewed or voided. It is separate from
a webhook: its own body, a static `X-Secret-Key` header instead of a signature, and its own
config. `sepay pg ipn listen` streams it; contract at `developer.sepay.vn/en/cong-thanh-toan/IPN`.

## bankhub: Bank Hub

Connects bank accounts of **other businesses**, for a platform that onboards merchants. For your
own accounts use `bank-account` and `transaction`.

You create a **company** for each merchant and a **link-token**, a single-use token for a hosted
page where the merchant links or unlinks a bank account or creates a VA. You read the linked
accounts and their transactions but cannot create them. Bank Hub has its own host and a
short-lived credential the CLI renews (`sepay bankhub auth`). Sandbox only.

Bank Hub has two channels of its own: a webhook for linking events (`sepay bankhub webhook set`,
`developer.sepay.vn/en/bankhub/webhook-event`) and an IPN for transfers on linked accounts
(`sepay bankhub ipn set|get|listen`, `developer.sepay.vn/en/bankhub/thong-bao-bien-dong-so-du`).

## Other

**einvoice**: Vietnamese electronic invoices. Create and issue submit a job and wait for its
result. Contract: `developer.sepay.vn/en/einvoice-api/v1/tong-quan`.

**apikey**: a long-lived key your own server sends as `Authorization: Bearer <api_key>` to SePay
API v2. It acts for the whole company, without scopes. `sepay apikey` manages keys in sandbox; the
CLI itself signs in with OAuth. Details: `developer.sepay.vn/en/sepay-api/v2/xac-thuc`.

**idempotency key**: a header the CLI adds to writes that create something, reused on retry, so a
timeout cannot create the same thing twice.
