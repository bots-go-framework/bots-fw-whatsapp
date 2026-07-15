# bots-fw-whatsapp

[![Go CI](https://github.com/bots-go-framework/bots-fw-whatsapp/actions/workflows/ci.yml/badge.svg)](https://github.com/bots-go-framework/bots-fw-whatsapp/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/bots-go-framework/bots-fw-whatsapp)](https://goreportcard.com/report/github.com/bots-go-framework/bots-fw-whatsapp)
[![GoDoc](https://pkg.go.dev/badge/github.com/bots-go-framework/bots-fw-whatsapp)](https://pkg.go.dev/github.com/bots-go-framework/bots-fw-whatsapp)

WhatsApp Cloud API adapter for [bots-fw](https://github.com/bots-go-framework/bots-fw),
built on [bots-api-whatsapp](https://github.com/bots-go-framework/bots-api-whatsapp).

> **Status: early — not yet a working adapter.** The customer-service-window gate,
> webhook signature verification, and inbound payload types are implemented and
> tested. The webhook handler, context, input types, responder, and chat-data store
> are not. See [Roadmap](#roadmap).

<!-- dev-approach:v1 -->
## Our approach to development

We build with our own tooling:

- **[SpecScore](https://specscore.md)** — specify requirements as `SpecScore.md` artifacts
- **[SpecStudio](https://specscore.studio)** — author & manage specs across their lifecycle
- **[inGitDB](https://ingitdb.com)** — store structured data in Git where applicable
- **[DALgo](https://dalgo.io)** — data access layer for Go
- **[cover100.dev](https://cover100.dev)** — drive toward 100% test coverage
- **[DataTug](https://datatug.io)** — query & explore data
<!-- /dev-approach -->

## The 24-hour customer service window

This is the thing that makes WhatsApp structurally different from Telegram, and
most of this package exists because of it.

A business may send **free-form** messages only within **24 hours** of the
recipient's last reply. Outside that window the send fails with error `131047`,
and only a **pre-approved template** may be delivered.

A Telegram bot may message any chat it knows, at any time. That is a *Telegram
property*, not a universal one — so `bots-fw` had no way for a platform to say
"not now". [bots-fw#80](https://github.com/bots-go-framework/bots-fw/pull/80) added
the optional `botsfw.SendGate` interface for exactly this, and this package
supplies the first implementation.

The gate refuses an out-of-window free-form send **before** it reaches the API.
That matters: the attempt would not merely fail, it could deliver a **billable**
template the app never intended.

```go
gate := whatsapp.NewWindowGate(lastInboundProvider)
// Free-form 48h after the user's last reply -> refused, no API call made.
// A whatsapp.TemplateMessage -> permitted, because templates are the mechanism
// for reaching users outside the window.
```

### Sending a template

Templates are expressed via `MessageFromBot.BotMessage`, mirroring how Telegram
expresses `SendPhoto` with a `tgbotapi.PhotoConfig`:

```go
m := botmsg.MessageFromBot{
	ToChat: whatsapp.ChatID("16505551234"),
	BotMessage: whatsapp.TemplateMessage{
		Name:         "payment_reminder",
		LanguageCode: "en_US",
		BodyParams:   []string{"Jessica", "$40"},
	},
}
```

## Why `LastInboundProvider` exists

The window needs one fact: **when did the user last reply?**

`bots-fw` looks like it already tracks this — `ChatBaseData.DtLastInteraction`.
It does not. `botswebhook/router.go` only stamps it when
`chatData.IsChanged() || chatData.HasChangedVars()`, so a reply that mutates
nothing leaves it stale. A window built on it would refuse sends that are in fact
permitted, silently.

So the adapter tracks last-inbound itself, and `LastInboundProvider` is the seam:

```go
type LastInboundProvider interface {
	LastInboundAt(ctx context.Context, chatID string) (time.Time, error)
}
```

Two rules the implementation must respect, both pinned by tests:

- **Only `messages` webhooks advance the window.** A `statuses` webhook is the fate
  of the *business's own* outbound message, and Meta sends up to three per message
  (sent / delivered / read). Counting those would hold the window open forever on
  the bot's own traffic.
- **A lookup failure fails open.** A store outage is our problem, not a platform
  refusal; failing closed would silently drop real messages.

## Webhook security

```go
// Subscription handshake (GET): echoes hub.challenge after a constant-time
// verify-token comparison; 403 and silent otherwise.
whatsapp.HandleVerification(w, r, verifyToken)

// Event notifications (POST): HMAC-SHA256 over the RAW body, keyed by app secret.
err := whatsapp.ValidateSignature(r.Header.Get(whatsapp.SignatureHeader), rawBody, appSecret)
```

Validate **before** decoding. Meta signs the bytes on the wire, so a re-marshalled
payload will not verify — there is a test pinning exactly that.

Note this does not fit `bots-fw`'s `WebhookSecretToken`, which models Telegram's
"echo a shared secret in a header". WhatsApp signs over the body instead.

## Roadmap

Implemented:

- `botsfw.BotPlatform` for `botsfwconst.PlatformWhatsApp`
- The 24h window + `botsfw.SendGate` implementation
- `TemplateMessage` (named and positional body parameters)
- `ChatID` — a phone-number `botmsg.ChatUID` (`botmsg.ChatIntID` is an `int64` and
  cannot carry a `wa_id`)
- Webhook signature validation and the `hub.challenge` handshake
- Inbound payload types, with `messages` vs `statuses` correctly separated

Not yet implemented:

- The `botsfw.WebhookHandler` tying the above together, and `WebhookContext`
- `botinput` input types, and the `LastInboundProvider` store implementation
- The responder that calls the API
- Interactive `button_reply` / `list_reply` inbound payloads — each has a dedicated
  Meta reference page not yet read, and guessing a wire shape is not worth it
- Media, location, contacts
- **Deduplication.** Meta explicitly states *"your server should handle
  deduplication"* on retries over 36 hours. `bots-fw`'s `IsNewerThen` /
  `UpdateLastProcessed` contract is unimplemented and uncalled, so this needs
  solving here or upstream.

## Known constraints

Verified against Meta's docs while building this. Full records in the private
backstage repo.

| | |
|---|---|
| Reply buttons | **max 3**, flat, no grid. Labels ≤ 20 chars. Button id ≤ 256 chars — *more* than Telegram's 64-byte `callback_data` cap, so callback payloads port fine. The count does not. |
| List messages | max 10 sections, but **10 rows across all sections combined** |
| Callback ack | **none** — taps arrive as ordinary inbound messages. No `answerCallbackQuery` analogue |
| Edit message | **no edit endpoint** — the Messages API enumerates 40+ operations and all are `Send` |
| Groups | **unverified** — examples only ever show `recipient_type: individual`, but no doc states groups are unsupported |

## Related

- [bots-fw](https://github.com/bots-go-framework/bots-fw) — the framework
- [bots-api-whatsapp](https://github.com/bots-go-framework/bots-api-whatsapp) — the API client
- [bots-fw-telegram](https://github.com/bots-go-framework/bots-fw-telegram) — the sibling adapter this mirrors
