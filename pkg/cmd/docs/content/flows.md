# Flows

The order commands go in for common tasks. Every flow runs in sandbox unless it
says otherwise.

## Confirmation, and why `--yes` appears below

Some writes ask before they run in sandbox too, not only on production, and
every production write asks. "Irreversible" is not the whole rule — a few
reversible-but-disruptive writes ask as well. `sepay --map=json` is the
authority: a command with `confirms` prompts, one with `destructive` cannot be
undone, and the two do not always coincide.

Without a terminal there is nobody to ask, so in CI or under an agent those
commands exit 2 with `requires confirmation; pass --yes`. Passing `--yes` is how
a script gives the answer, not a way around the check. That is why the
irreversible commands below carry it and the rest do not.

## Authenticate

```bash
sepay login          # opens a browser to approve a device code
sepay auth status    # confirms which account and company you are on
```

Requires an existing SePay account and a browser. On a machine with no browser,
pipe an OAuth2 refresh token in: `sepay login --with-token`.

`login` requests all capability scopes by default. Those scopes only matter on
production — sandbox still requires a login but enforces no scope. To widen an
existing login later: `sepay auth refresh --scopes webhook:write`.
`sepay --map=json` shows which scope a command needs, if any.

## Receive your first webhook event

The point of this flow is that it needs no public URL and no tunnel: `listen`
opens a stream and replays events into a handler on your machine.

Three things have to line up, and they are the usual reason nothing arrives:

1. A webhook exists and is active.
2. That webhook **targets the bank account the transfer arrives on** — `all`
   covers every account, `single`/`multi` cover only the ones they name.
3. The transfer arrives on that account, not a sibling.

An event reaches the stream only through a webhook delivery, so a webhook on one
account and a transfer on another produces nothing at all. `listen` states what
it will deliver before it starts, and `webhook test` refuses to fire on an
account nothing covers rather than reporting a timeout.

```bash
# 0. Have your own HTTP handler running, e.g. on localhost:3000.

# 1. Provision a sandbox account to fire events under, with a webhook covering it.
sepay sandbox init --with-va --with-webhook-listen
sepay bank-account list          # note the xid

# 2. On an account you already have, create the webhook yourself instead.
sepay webhook list
sepay webhook create --listen-only --bank-mode all --event-type in

# 3. Stream events to your handler.
sepay webhook listen --forward localhost:3000 --print

# 4. In a second shell, fire one.
sepay sandbox simulate payment_in --bank-account <ba_xid> --amount 50000 --code ABC123
```

`--listen-only` (sandbox), or `sandbox init --with-webhook-listen`, is why the
webhook needs no URL at all: it has no destination, SePay sends no HTTP request
for it, and `listen` is what receives the event. With a URL the webhook needs a
real, publicly resolvable host — the API rejects one whose domain has no DNS
record or resolves to a private address, so reserved names like `example.invalid`
fail validation — and every simulated transaction then costs a delivery attempt
to a host that is not yours.

`listen` warns at startup when no webhook covers the account, prints the
handler's response status per event, and reconnects on its own with backoff.
`Ctrl+C` stops it.

It runs on production too — `--env production` live-tails real deliveries — but
only as an observer: nothing fabricates an event there, because `sandbox
simulate` and `webhook test` are sandbox-only.

**A deployed application does not run `listen`.** SePay POSTs every delivery to
the webhook's `--url` itself, and in production that URL is the application. Use
`listen` while you develop, because it needs no public URL and no tunnel; ship
the handler behind a real URL. See *Integrate an app that calls SePay directly*.

Two things about the stream are easy to misread. It is **company-wide**: you
receive events for every bank account of the company, not only the one you fired
on. And one transaction arrives **once per matching webhook** — three matching
webhooks means three POSTs with the same `id`, each signed with that webhook's
own secret.

Every line is tagged `txn=<transaction xid>` (paste it into `sepay transaction get`) and
`wh=<webhook xid>` (paste it into `sepay webhook get`) so you can see which transaction it carries and which webhook a delivery
came from, and `--webhook <xid>` keeps only one of them. Deduplicate on `id`
regardless.

`sepay docs webhook-payload` has the event body your handler receives, the
signature headers, and what to return.

## Verify a handler in CI

One shot, no second shell, exits non-zero if the handler misbehaves — which is
what makes it usable as a test step.

```bash
sepay webhook test --forward localhost:3000 --bank-account <ba_xid> \
  --amount 50000 --event payment_in --expect-status 200
```

`--forward` and `--amount` are required. `--bank-account` is needed only when
the login has more than one sandbox account: with exactly one it is picked for
you, and in CI with several the run stops asking for it. Add `--webhook <xid>`
when more than one webhook covers the account, or the run may judge a sibling's
delivery instead of yours.

It fires a simulated event, waits for your handler, and exits 6 if the forward
fails or the status does not match. Sandbox only.

## Register a real webhook endpoint

Use this when SePay should call a URL you host, rather than streaming to your
machine.

```bash
sepay webhook create --url https://example.com/hook --bank-mode all \
  --event-type in --auth-type hmac --secret <shared-secret>

sepay webhook logs                 # what was delivered, and the response
sepay webhook replay <log_xid> --yes   # re-deliver one event (sandbox)
```

`--bank-mode` is required and says which accounts the webhook covers: `all`, or
`single`/`multi` with `--bank-account <xid>` repeated.

`--auth-type hmac` makes SePay sign each request so your handler can verify it
came from SePay. Narrow what you receive with `--event-type`, `--only-va`,
`--prefix-filter`, or `--bank-account`.

Reference: `developer.sepay.vn/en/sepay-webhooks/tao-webhook` for the options,
`/xac-thuc` for auth, `/bao-mat` for hardening, `/giam-sat` for monitoring
deliveries.

## Integrate an app that calls SePay directly

For an application you deploy. The CLI is a development tool here, not a runtime
dependency: it provisions and tests, then the deployed app talks to the API over
HTTP on its own. **Nothing on the server needs the `sepay` binary, a stored
profile, or a running `listen`.**

```bash
# 1. Provision, once, from your machine.
sepay sandbox init --with-va
sepay apikey create --name my-app        # prints the key in full
sepay webhook create --url https://myapp.example.com/webhook/sepay \
  --bank-mode all --event-type in --auth-type hmac --secret <shared-secret>

# 2. Develop against the handler before it is public.
sepay webhook listen --forward localhost:3000 --print
sepay sandbox simulate payment_in --bank-account <ba_xid> --amount 50000

# 3. Gate it in CI.
sepay webhook test --forward localhost:3000 --bank-account <ba_xid> \
  --amount 50000 --event payment_in --expect-status 200
```

The app then holds two secrets and no CLI state: the API key it sends as
`Authorization: Bearer <key>`, and the webhook secret it verifies each delivery
with. `sepay docs api` has the hosts, the envelope, and the headers; `sepay docs
webhook-payload` has the body and the signature.

`SEPAY_OAUTH_URL` and the `SEPAY_*_API_URL` overrides configure *this CLI* and
mean nothing to the app — `sepay docs environment` lists what belongs where.

## Decide how payments get matched

Two ways to tell which order a transfer paid, for money arriving on an account
you own. Pick one before you build reconciliation; `sepay docs glossary` has what
each is. A hosted gateway order does its own matching and needs neither — see
*Take a payment on a hosted page*.

**By memo code** — a prefix per order family, buyers type the code:

```bash
sepay paycode get
sepay paycode set DH --character-type NumberOnly --suffix-from 6 --suffix-to 12
sepay paycode create HD --character-type NumberAndLetter --suffix-from 6 --suffix-to 12 --active
```

`set` edits one structure and keeps the rest; `create` adds one at the end of the
list and refuses a prefix the company already has, in any letter case.

**By account number** — a distinct VA per order, buyers type nothing:

```bash
sepay va issue --bank-account <ba_xid> --label "order-1709"
sepay va list --bank-account <ba_xid>
```

A VA is nested under its parent account, so there is no flat list of every VA —
`va list` is scoped to one bank account.

A pay code is a prefix of **2 to 5 letters** plus a suffix of 1 to 30 characters,
either digits (`NumberOnly`) or digits and letters (`NumberAndLetter`). The prefix
is stored uppercase and matched case-insensitively. Design the order number
against those bounds before you build: a 7-letter prefix is rejected, so an order
family like `DHSEPAY001` is prefix `DH` with `SEPAY001` in the suffix, which needs
`--character-type NumberAndLetter` and a suffix range that admits 8 characters.
`ON`, `OFF`, `YES`, `NO`, `TRUE` and `FALSE` are legal prefixes that read as
boolean flag syntax, so pass one as `--prefix ON` rather than as a bare argument.

`sandbox simulate --code` caps at 10 characters, which is tighter than the 35 a
pay code can reach. A longer live code is legal; it just cannot be fired by the
simulator. A gateway order carries its own number instead, up to 100 characters
and free of the prefix rules — see *Take a payment on a hosted page*.

On production, changing the paycode prefix changes matching immediately.
Issuing and revoking VAs are sandbox-only.

## Onboard a merchant through Bank Hub

For platforms connecting other businesses' accounts. Sandbox only.

Your own Bank Hub sandbox merchant is created for you on the first `bankhub`
command — there is nothing to set up in the portal first. It arrives with no IPN
endpoint, which is the next step below.

```bash
sepay bankhub auth                                  # mints the credential, creating the merchant

# Set IPN BEFORE anyone links an account: a linked account's delivery row is
# seeded from this URL at the moment it links. Set it after and the accounts
# already linked are re-pointed, but until then their deliveries go nowhere.
sepay bankhub ipn set https://example.com/bankhub-ipn --api-key <key> --yes
sepay bankhub webhook set https://example.com/bankhub-hook --secret <key> --yes

sepay bankhub company create --name "Shop A"        # note the company xid
sepay bankhub company edit <xid> --status Active --yes   # created Pending; link-token needs Active

sepay bankhub link-token create --company <xid> --purpose LINK_BANK_ACCOUNT \
  --redirect-uri https://example.com/done
# Send the returned hosted URL to the merchant; they link their bank there. This
# is the only way in — the portal has no screen for it.

sepay bankhub bank-account list --company <xid>     # appears once they finish
sepay bankhub transaction list --company <xid>
sepay bankhub ipn get                               # accounts_total > 0 once one is linked
sepay bankhub ipn listen --forward localhost:3000
sepay bankhub sandbox simulate-transaction --bank-account <xid> --amount 50000 --content "test"
```

The Bank Hub credential is short-lived; the CLI re-mints it for you.
`sepay bankhub auth` inspects it.

Bank Hub has two outbound channels and they are easy to confuse. The *webhook* set
above carries account-linking lifecycle events
(`developer.sepay.vn/en/bankhub/webhook-event`). Transfers arrive over its *IPN*
(a balance-change notification), configured separately — `sepay bankhub ipn set|get|listen`
(`developer.sepay.vn/en/bankhub/thong-bao-bien-dong-so-du`). `sepay webhook listen`
is the wrong stream for it: that one carries accounts you own, and a linked account
belongs to the merchant you onboarded.

Reference: `developer.sepay.vn/en/bankhub/tong-quan`, sandbox specifics at
`/en/bankhub/thong-tin-sandbox`.

## Take a payment on a hosted page

The alternative to matching a bank transfer yourself: SePay hosts the payment
page, and the buyer picks card, QR Banking or QR NAPAS there.

```bash
sepay pg checkout init --amount 100000 --description "Order DHSEPAY001" \
  --invoice DHSEPAY001 \
  --success-url https://myapp.example.com/order/DHSEPAY001?payment=success \
  --cancel-url  https://myapp.example.com/order/DHSEPAY001?payment=cancel \
  --error-url   https://myapp.example.com/order/DHSEPAY001?payment=error

sepay pg order get DHSEPAY001     # read order_status
```

`--invoice` is your own order number: letters, digits, `-` and `_`, up to 100
characters, and free of the pay-code prefix rules — a `/`, which Vietnamese
invoice numbers often carry, is rejected. `--payment-method` narrows what the page
offers to one of `BANK_TRANSFER`, `CARD`, `NAPAS_BANK_TRANSFER`; omit it to offer
all of them. The buyer returns to whichever of the three URLs applies.

**A gateway order does not notify you through `sepay webhook`.** The two payment
paths have separate notification channels, and mixing them is the usual reason
nothing arrives:

| | Bank transfer on your own account | Gateway order |
|---|---|---|
| Notification | webhook — `sepay webhook create` | IPN — `sepay pg ipn set`, or the Payment Gateway IPN settings in the portal |
| Auth on the request | HMAC signature over the body | `X-Secret-Key` header, no signature |
| Body | `sepay docs webhook-payload` | `sepay docs ipn-payload` |
| Stream it locally | `sepay webhook listen` | `sepay pg ipn listen` |

So `webhook listen` never carries a gateway event, and a handler written against
`sepay docs webhook-payload` will not parse an IPN.

## Receive a Payment Gateway IPN

The stream mirrors real deliveries, so an IPN endpoint has to be set and active
first — the worker only sends for a config with a non-empty URL, and what it
sends is what gets mirrored:

```bash
sepay pg ipn set https://example.com/ipn --secret s3cret   # once, if not already set
sepay pg ipn listen --forward localhost:3000 --print
```

`listen` says so up front if that config is missing or switched off, so a silent
session is never a mystery.

Then pay a sandbox order (the section above) and the notification arrives. SePay
mirrors it as it is composed, and only on the first attempt, so one order produces
exactly one notification however your endpoint answers. `--events ORDER_PAID` narrows the stream;
`--order <id>` pins it to one order.

Each event carries the same body and the same headers your endpoint received, so
your handler — signature check and all — runs its real path. `--headers` prints the
header names with credential values redacted, since a terminal becomes a log.

The URL must be publicly reachable — SePay has to deliver there for anything to
reach the stream. `sepay pg ipn get` shows the current setting.

`pg` commands are sandbox-only in this CLI. The gateway itself has a production
host, which an application reaches directly; `sepay docs api` has both.

## End a Payment Gateway order

Which command depends on how the order was paid and whether it was paid at all.
Both are irreversible.

```bash
sepay pg order get <invoice-number>     # read order_status and payment_method first
```

| Situation | Command |
|---|---|
| Bank transfer, not paid yet | `sepay pg order cancel <invoice-number>` |
| Card, paid, not yet settled | `sepay pg order void <invoice-number>` |
| Card, paid, after settlement | Neither works — refund instead |

Read the status before acting: `cancel` is rejected on an order that is already
`CAPTURED` or `CANCELLED`, and `void` is rejected on one that is not `CAPTURED`.

`void` can return a partial success — the payment is reversed but cancelling an
attached auto-renew agreement failed. The CLI reports it as a warning with the
agreement id; the payment is still voided.

## Reset a dirty sandbox

```bash
sepay quota                # what is left of the daily allowance
sepay sandbox reset --yes  # clears webhooks and bank accounts
```

Sandbox allows 500 simulated transactions per day, and the counter resets at
00:00 UTC+7.

Reference: `developer.sepay.vn/en/tien-ich-khac/test-mode`, and
`/en/sepay-webhooks/test-mode/so-sanh-voi-live` for how test mode differs from live.

## Reference docs

The pages above are on `developer.sepay.vn`, which is the authority for every
platform contract this CLI only summarises. Append `.md` to any of them for raw
Markdown an agent can fetch, and see `https://developer.sepay.vn/llms.txt` for the
complete index. `sepay docs api` has the per-product entry points.
