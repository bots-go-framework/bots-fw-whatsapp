# wasmoke — WhatsApp Cloud API smoke harness

A manual tool to exercise the adapter against a **real** WhatsApp Business number.
Every unit test in this repo runs against `httptest` and Meta's documented payloads;
nothing has touched the wire. This is what closes that gap.

It is **not** a `go test`: it takes real credentials and sends real messages. `go
build ./...` compiles it, `go test ./...` ignores it, CI never runs it. You run it by
hand, once, when a number is provisioned.

## What it's really for

The whole ToGethered design rests on one **unverified inference**: that a template
quick-reply tap *reopens* the 24-hour customer-service window. Both halves are
documented; the conjunction is not. This harness makes it observable — see
[The door-opener test](#the-door-opener-test).

## Prerequisites

1. A **WhatsApp Business Account** with a phone number, via
   [Meta for Developers](https://developers.facebook.com/) → a Business app with the
   WhatsApp product added.
2. From the app's WhatsApp → API Setup page:
   - **Phone number ID** (not the display number) → `WA_PHONE_NUMBER_ID`
   - a **temporary or permanent access token** → `WA_ACCESS_TOKEN`
3. From App Settings → Basic: the **App Secret** → `WA_APP_SECRET`
4. A **verify token** you invent (any string) → `WA_VERIFY_TOKEN`
5. A way to expose localhost to Meta — [ngrok](https://ngrok.com) or a Cloudflare
   Tunnel. Meta requires HTTPS.

```sh
export WA_PHONE_NUMBER_ID=1234567890
export WA_ACCESS_TOKEN=EAAG...          # keep this out of shell history
export WA_APP_SECRET=abc123...
export WA_VERIFY_TOKEN=pick-any-string
```

## Runbook

### 1. Start the receiver

```sh
go run ./cmd/wasmoke serve            # listens on :8080/webhook
```

### 2. Expose it and register the webhook

```sh
ngrok http 8080                        # → https://<id>.ngrok-free.app
```

In the app dashboard → WhatsApp → Configuration → Webhook:

- **Callback URL:** `https://<id>.ngrok-free.app/webhook`
- **Verify token:** the same string as `WA_VERIFY_TOKEN`
- Click **Verify and save** — `serve` completes the `hub.challenge` handshake and the
  dashboard shows a green tick. (If it fails, the token doesn't match.)
- **Subscribe** to the `messages` field.

### 3. Send something

In another shell (with `WA_PHONE_NUMBER_ID` + `WA_ACCESS_TOKEN` exported):

```sh
# A template — the only thing deliverable to someone who hasn't messaged you in 24h.
# "hello_world" is a default template Meta pre-approves on new accounts.
go run ./cmd/wasmoke send -to <your-personal-number> -template hello_world -lang en_US

# A free-form text — succeeds ONLY if that number messaged you in the last 24h,
# otherwise you'll see error 131047 with an explanation.
go run ./cmd/wasmoke send -to <your-personal-number> -text "hello from wasmoke"
```

`send` classifies any failure (131047 / throttle / unreachable / auth / template) and
tells you what it means, so the error model is legible rather than a raw dump.

## The door-opener test

This is the experiment worth running. It either confirms or kills a load-bearing
assumption.

1. `serve` running (step 1–2).
2. Send a template **with buttons** to your own number (needs an approved template
   that has quick-reply buttons; `hello_world` has none, so approve one — e.g. a
   template with a "Yes" / "No" quick reply — in WhatsApp Manager first).
3. On your phone, **tap a button**.
4. Watch `serve`. You should see:

   ```
   ← inbound  from=<you> type=button id=wamid...
       TEMPLATE button tap — payload="Yes" text="Yes"
       context.id="wamid...(the template you sent)"
       ⇒ payload == text: confirms the payload is the button label, carrying no state
       window: OPEN (last inbound 2026-...)
       ⇒ a plain -text send to this number should now SUCCEED
   ```

5. **The proof:** immediately run `send -to <you> -text "now in-window"`. If it
   succeeds where it failed before the tap, **the tap reopened the window** — the
   door-opener holds, and the ToGethered design stands. If it still returns 131047,
   the inference is wrong and the design needs rethinking.

Two facts this run also settles, both currently `inferred` in the capability matrix:

- **`payload == text`** on a template button → the payload is only the label. Record
  it as confirmed on the wire (`can-i-use` currently infers it from the docs).
- Whether the window truly reopens (the `window: OPEN` line after a tap).

Report whatever you observe back into `bots-go-framework/can-i-use`.

## Commands

```
wasmoke serve
wasmoke send -to <number> -text <string>
wasmoke send -to <number> -template <name> -lang <code> [-param v -param v ...]
```

`-param` repeats, in order, for a template's positional body variables:

```sh
go run ./cmd/wasmoke send -to 16505551234 -template order_update -lang en_US \
  -param "Jessica" -param "SKBUP2-4CPIG9"
```

## Notes

- The recipient must have opted in (messaged your business, or accepted on a test
  number). Meta will not deliver cold outside a template.
- `serve` keeps window/dedup state **in memory** — restart it and the window resets.
  Production uses `whatsapp.NewDalgoChatDataStore`.
- Never commit real tokens. These are read from the environment for exactly that
  reason.
