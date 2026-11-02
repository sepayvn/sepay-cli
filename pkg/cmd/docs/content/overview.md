# What sepay is, and what it is for

SePay is a Vietnamese payment platform. Its core service watches the bank
accounts a business owns and reports money arriving on them in near real time —
so an order can be marked paid from a bank transfer, with no card network
involved. Around that sit a hosted checkout, a way to read *other* businesses'
accounts, and electronic invoicing.

`sepay` is the command-line tool for that platform: `sepay <noun> <verb>`, in the
shape of `gh` or `stripe`. It talks to the same public API an application would.

## What you can do with it

| Goal | Start here |
|---|---|
| See money arriving on your accounts | `sepay bank-account list`, `sepay transaction list` |
| Get notified of each payment | `sepay webhook create`, then `sepay webhook listen` |
| Know which order a transfer paid | `sepay paycode`, or `sepay va issue` for one account number per order |
| Charge a buyer on a payment page | `sepay pg checkout init` |
| Test all of it without real money | `sepay sandbox init`, `sepay sandbox simulate` |
| Give an application its own credential | `sepay apikey create` |
| Read another business's linked accounts | `sepay bankhub` |
| Issue an electronic invoice | `sepay einvoice` |
| Reach an endpoint no command covers yet | `sepay api /path` |

Every read and list command takes `--json`. `sepay --map=json` is the whole
surface as machine-readable metadata, including which commands are sandbox-only
and which cannot be undone.

## Which path is yours

Two ways to get paid, and they do not mix — each has its own notification
channel:

- **Money into an account you own.** You register a webhook, SePay POSTs each
  matching transaction to your URL, and you match it to an order by a code in the
  memo or by a per-order account number. This is the platform's core.
- **A hosted payment page.** You create a gateway order, send the buyer to a
  SePay-hosted page where they pay by card or QR, and the gateway reports back
  over its own IPN. Use it when you want cards, or do not want to run matching.

Both channels stream to a local handler while you build, so neither needs a public
URL: `sepay webhook listen` for the first, `sepay pg ipn listen` for the second.

`sepay docs flows` walks both, and states which notification belongs to which.

## Where the authority is

These topics cover the CLI: what a command does, what it replays, masks or
asserts. Every **platform** contract — payload fields, response rules, retry
curves, signatures, per-bank behaviour — belongs to `developer.sepay.vn`, and is
linked rather than copied here so it cannot go stale. `sepay docs api` has the
per-product entry points; `https://developer.sepay.vn/llms.txt` indexes every
page, and appending `.md` to any page URL returns raw Markdown an agent can fetch.

## What it is not

- **Not a runtime dependency.** The CLI provisions and tests during development;
  a deployed application calls the API over HTTP itself and needs no `sepay`
  binary, stored profile, or running `listen`. Hosts and credentials for that:
  `sepay docs api`.
- **Not a way around a permission.** Scopes, environment isolation and OTP are
  enforced by the server. The CLI surfaces them; `--yes` skips a confirmation
  prompt and never an OTP.
- **Not a holder of secrets.** It ships none, and a credential enters only
  through `sepay login`. Tokens are never printed except by `sepay auth token`.

## Getting oriented

```bash
sepay login              # OAuth device flow; approve in a browser
sepay auth status        # which account and company you are on
sepay sandbox init       # a sandbox bank account to work against
```

Everything runs against **sandbox** until you pass `--env production`.

Next: `sepay docs glossary` for what the nouns mean, `sepay docs flows` for the
command order of a task, `sepay docs api` for building an application against the
API directly.
