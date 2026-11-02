# Glossary

What each SePay noun means, and which command it belongs to.

Pages cited as `developer.sepay.vn/en/...` are the platform's own reference. Open
one as-is in a browser, or append `.md` to fetch it as raw Markdown.

## Core

**environment** — `sandbox` or `production`. Sandbox is the default and holds
its own separate data; nothing you do there touches real money. The
environment is chosen by `--env`, which picks a different API host. There is no
flag that mixes the two.

**profile** — one stored login (an account plus its company). `sepay login`
creates one; `--profile <name>` or `sepay auth switch` picks between them.

**xid** — the public UUID of a resource. Commands address resources by xid,
never by a numeric database id, and nested ones name theirs (`ba_xid` is a bank
account). The exception is `pg order`, which takes an invoice number.

**scope** — a capability your token carries: `read`, `webhook:write`,
`bank-account:write`, `paycode:write`. Scopes gate the
**production** host only; the sandbox host still requires a valid login but
enforces none of them. A token with no capability scope can therefore read and
write in sandbox and do nothing on production. Add one with
`sepay auth refresh --scopes <scope>`.

**OTP** — a one-time code the server can demand for some production writes on
an official VA. The CLI has no prompt for one: a challenge comes back as exit 7
and the command stops. Do that action in the web portal.

In practice it does not come up: the commands that could trigger one are
sandbox-only, and sandbox never challenges.

## Banking

**bank account** — a real bank account of yours that SePay watches. Incoming
transfers to it become transactions. `sepay bank-account`.

**transaction** — one movement of money SePay observed on a bank account,
in or out. Read-only: SePay records what the bank reported. `sepay transaction`.

**paycode** — a short code the payer types into the bank transfer memo, so
SePay can match the incoming transfer to your order.

You configure the shape, not the codes: a prefix of 2 to 5 letters plus a
suffix of 1 to 30 characters, numeric or alphanumeric. SePay scans the memo of
every incoming transfer for something matching that shape and extracts it.

Changing the prefix on production changes matching immediately — transfers
already in flight carrying the old prefix stop matching. `sepay paycode`.

**VA (virtual account)** — an extra account number underneath one of your bank
accounts. Give a different VA number to each order or customer and an incoming
transfer identifies itself by *where it arrived*, so the payer does not have to
type anything. This is the alternative to paycode matching.

Two kinds. An **official** VA is a real number issued by the bank; a
**non-official** VA is SePay-side only. Which kind you get follows the parent
bank account's setup, not a flag on `va issue`; `va list --official 1` filters
by it.

A VA always lives under a parent bank account, which is why `sepay va list`
takes `--bank-account`. The cap is 100 VAs per bank account.

**webhook** — an HTTPS endpoint of yours that SePay calls when a transaction
arrives. `sepay webhook listen` is the local alternative: it streams the same
events straight to a handler on your machine, so you can develop without a
public URL. `sepay webhook`.

**quota** — the caps on your account, reported by `sepay quota`.

In sandbox these are the Test Mode limits: 500 transactions per day (the
counter resets at 00:00 UTC+7), 50 bank accounts, 50 webhooks, 50 API keys, and
100 VAs per bank account. Each rejects the write that would exceed it —
`enforcement: blocked`. The 100-VA cap is a sandbox rule and does not apply to a
production bank account.

On production the command needs an Admin or SuperAdmin role, because plan figures
are restricted to company administrators everywhere else too. The only reported
cap is the plan's transaction allowance for the current billing cycle. What
happens once you pass it — whether transactions stop or are billed on top — is
set by your plan agreement, not by this command; the portal's plan page and your
contract are the authority. `enforcement` says whether the allowance is enforced
at all:

| `enforcement` | Meaning |
|---|---|
| `capped` | The allowance is enforced; `remaining` counts down against it |
| `unlimited` | The plan carries no transaction allowance |
| `exempt` | The allowance is not enforced for your account |
| `unknown` | The subscription has no billing window, so usage cannot be measured |

`unlimited`, `exempt` and `unknown` omit `limit` and `remaining` entirely. Your
plan's bank-account and webhook figures are never reported, because nothing
enforces them.

## pg — Payment Gateway

A checkout you send a buyer to, for card payments and bank transfers. Distinct
from the banking commands above: those watch an account you already own, `pg`
runs a payment flow you initiated. Sandbox only.

**order** — one payment attempt, addressed by its invoice number. `pg order
list --status` filters on the three you will normally see:

| Status | Meaning |
|---|---|
| `AUTHENTICATION_NOT_NEEDED` | created, waiting for the buyer to pay |
| `CAPTURED` | paid |
| `CANCELLED` | cancelled or voided |

The gateway defines more than these (authentication, dispute, refund and payout
states). Read the `order_status` you are given rather than assuming it is one of
the three.

**cancel** (`sepay pg order cancel`) — abandons an order that has *not* been
paid. Bank-transfer orders only, and only while the status is neither
`CAPTURED` nor `CANCELLED`. Nothing un-cancels an order.

**void** (`sepay pg order void`) — reverses a card payment that *has* been
paid, before the money is settled. `CAPTURED` card orders only.

**settlement** — the point where a captured card payment is handed to the bank.
`void` works before it; after it the money has moved and a refund is the only
route. The CLI does not report when an individual payment settles, so a batch
job that voids on a schedule can silently run too late — check with the
Payment Gateway side before relying on a window.

**IPN** (Instant Payment Notification) — the gateway's outbound notification,
sent when an order is paid, renewed or voided. A separate channel from a
**webhook**, which reports a transfer on a bank account you own: different body,
different auth (a static `X-Secret-Key` header, with no signature), and a separate
config. Stream it with `sepay pg ipn listen`; body in `sepay docs ipn-payload`,
contract at `developer.sepay.vn/en/cong-thanh-toan/IPN`.

Bank Hub uses both words for two of its own channels: its *webhook* carries
account-linking lifecycle events (`developer.sepay.vn/en/bankhub/webhook-event`),
while its *IPN* (a balance-change notification) carries transfers on the accounts your merchants
linked. Each has its own config and its own stream — `sepay bankhub webhook set`
and `sepay bankhub ipn set|get|listen`
(`developer.sepay.vn/en/bankhub/thong-bao-bien-dong-so-du`).

## bankhub — Bank Hub

A separate SePay product for connecting **other businesses'** bank accounts,
not your own.

Use `bank-account` and `transaction` when the account is yours. Use `bankhub`
when you are a platform onboarding merchants: create a `company` for each
merchant, mint a `link-token`, send them to the hosted link page, and they
connect their own bank there. You then read the resulting accounts and
transactions — you cannot create them, which is why `bankhub bank-account` has
no `create`.

Different host and a different credential (`sepay bankhub auth`), short-lived
and re-minted automatically when it expires. Sandbox only.

**company** — one merchant you have onboarded. **link-token** — a single-use
token that produces a hosted URL where that merchant links or unlinks an
account, or creates a VA.

## Other

**einvoice** — Vietnamese electronic invoicing. Creating and issuing are
asynchronous: the command submits, then polls for the result. Contract:
`developer.sepay.vn/en/einvoice-api/v1/tong-quan`.

**apikey** — a long-lived static key a non-CLI integration sends as
`Authorization: Bearer <api_key>` on SePay API v2. It acts for the whole company
and is not narrowed by OAuth scopes. The CLI manages keys (`sepay apikey`,
sandbox only) but authenticates with OAuth itself. Hosts, header and the
production route: `sepay docs api`.

**idempotency key** — a header the CLI generates for you on writes that create
something, so a retry after a timeout cannot create it twice. One key per
logical operation, reused across retries.
