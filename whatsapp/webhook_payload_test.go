package whatsapp

import (
	"encoding/json"
	"testing"
	"time"
)

// metaInboundTextWebhook is Meta's documented inbound-text example, verbatim.
// https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages
const metaInboundTextWebhook = `{
  "object": "whatsapp_business_account",
  "entry": [
    {
      "id": "102290129340398",
      "changes": [
        {
          "value": {
            "messaging_product": "whatsapp",
            "metadata": {
              "display_phone_number": "15550783881",
              "phone_number_id": "106540352242922"
            },
            "contacts": [
              { "profile": { "name": "Sheena Nelson" }, "wa_id": "16505551234" }
            ],
            "messages": [
              {
                "from": "16505551234",
                "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQTRBNjU5OUFFRTAzODEwMTQ0RgA=",
                "timestamp": "1749416383",
                "type": "text",
                "text": { "body": "Does it come in another color?" }
              }
            ]
          },
          "field": "messages"
        }
      ]
    }
  ]
}`

// metaStatusWebhook is Meta's documented outbound-status example, verbatim.
const metaStatusWebhook = `{
  "object": "whatsapp_business_account",
  "entry": [
    {
      "id": "102290129340398",
      "changes": [
        {
          "value": {
            "messaging_product": "whatsapp",
            "metadata": {
              "display_phone_number": "15550783881",
              "phone_number_id": "106540352242922"
            },
            "statuses": [
              {
                "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgARGBI3MTE5MjVBOTE3MDk5QUVFM0YA",
                "status": "delivered",
                "timestamp": "1750263773",
                "recipient_id": "16505551234",
                "conversation": {
                  "id": "6ceb9d929c9bdc4f90e967a32f8639b4",
                  "origin": { "type": "service" }
                },
                "pricing": {
                  "billable": true,
                  "pricing_model": "CBP",
                  "category": "service"
                }
              }
            ]
          },
          "field": "messages"
        }
      ]
    }
  ]
}`

func TestWebhookPayload_parseInboundText(t *testing.T) {
	var p WebhookPayload
	if err := json.Unmarshal([]byte(metaInboundTextWebhook), &p); err != nil {
		t.Fatalf("failed to parse Meta's documented example: %v", err)
	}

	msgs := p.InboundMessages()
	if len(msgs) != 1 {
		t.Fatalf("got %d inbound messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.From != "16505551234" {
		t.Errorf("From = %q", m.From)
	}
	if m.Type != "text" {
		t.Errorf("Type = %q", m.Type)
	}
	if m.Text == nil || m.Text.Body != "Does it come in another color?" {
		t.Errorf("Text = %+v", m.Text)
	}
	if want := time.Unix(1749416383, 0).UTC(); !m.SentAt().Equal(want) {
		t.Errorf("SentAt() = %v, want %v", m.SentAt(), want)
	}
	if len(p.Statuses()) != 0 {
		t.Error("an inbound-message webhook must yield no statuses")
	}
}

// TestWebhookPayload_statusWebhookIsNotInbound is the load-bearing test of this
// file.
//
// A status webhook reports the fate of the BUSINESS's own outbound message, and
// every outbound message produces up to three (sent, delivered, read). If those
// counted as user activity, the customer service window would never appear to
// close — the bot's own traffic would hold it open forever, every proactive
// free-form send would be attempted, and every one would fail with 131047.
func TestWebhookPayload_statusWebhookIsNotInbound(t *testing.T) {
	var p WebhookPayload
	if err := json.Unmarshal([]byte(metaStatusWebhook), &p); err != nil {
		t.Fatalf("failed to parse Meta's documented example: %v", err)
	}

	if got := p.InboundMessages(); len(got) != 0 {
		t.Fatalf("a status webhook must yield NO inbound messages, got %d", len(got))
	}
	if got := p.LatestInboundPerChat(); len(got) != 0 {
		t.Fatalf("a status webhook must not advance the window, got %v", got)
	}

	statuses := p.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	s := statuses[0]
	if s.Status != "delivered" {
		t.Errorf("Status = %q", s.Status)
	}
	if s.RecipientID != "16505551234" {
		t.Errorf("RecipientID = %q", s.RecipientID)
	}
	// Billing is how a business learns a template actually cost money.
	if s.Pricing == nil || !s.Pricing.Billable {
		t.Errorf("Pricing = %+v, want billable", s.Pricing)
	}
	if s.Conversation == nil || s.Conversation.Origin == nil || s.Conversation.Origin.Type != "service" {
		t.Errorf("Conversation = %+v", s.Conversation)
	}
}

// TestWebhookPayload_batching pins that batched payloads are fully flattened.
// Meta says a payload may carry up to 1000 updates and that batching "cannot be
// guaranteed", so nothing may assume one entry/change/message.
func TestWebhookPayload_batching(t *testing.T) {
	p := WebhookPayload{
		Entry: []WebhookEntry{
			{Changes: []WebhookChange{
				{Value: WebhookValue{Messages: []InboundMessage{
					{From: "1", Timestamp: "1749416383"},
					{From: "2", Timestamp: "1749416384"},
				}}},
				{Value: WebhookValue{Messages: []InboundMessage{{From: "3", Timestamp: "1749416385"}}}},
			}},
			{Changes: []WebhookChange{
				{Value: WebhookValue{Messages: []InboundMessage{{From: "4", Timestamp: "1749416386"}}}},
			}},
		},
	}
	if got := len(p.InboundMessages()); got != 4 {
		t.Errorf("got %d messages across entries/changes, want 4", got)
	}
}

// TestLatestInboundPerChat_keepsMostRecent pins that a batch containing several
// messages from one user records the LATEST, not the last-iterated.
func TestLatestInboundPerChat_keepsMostRecent(t *testing.T) {
	p := WebhookPayload{Entry: []WebhookEntry{{Changes: []WebhookChange{
		{Value: WebhookValue{Messages: []InboundMessage{
			{From: "16505551234", Timestamp: "1749416385"}, // later, listed first
			{From: "16505551234", Timestamp: "1749416383"},
			{From: "16505559999", Timestamp: "1749416384"},
		}}},
	}}}}

	got := p.LatestInboundPerChat()
	if len(got) != 2 {
		t.Fatalf("got %d chats, want 2", len(got))
	}
	if want := time.Unix(1749416385, 0).UTC(); !got["16505551234"].Equal(want) {
		t.Errorf("got %v for the repeated sender, want the most recent %v", got["16505551234"], want)
	}
}

// TestInboundMessage_SentAt_malformed pins that a bad timestamp degrades to the
// zero Time rather than to 1970 — which would look like an ancient reply and
// wrongly slam the window shut.
func TestInboundMessage_SentAt_malformed(t *testing.T) {
	for _, ts := range []string{"", "not-a-number", "0", "-1"} {
		m := InboundMessage{Timestamp: ts}
		if !m.SentAt().IsZero() {
			t.Errorf("Timestamp=%q: SentAt() = %v, want zero", ts, m.SentAt())
		}
	}
}

// TestLatestInboundPerChat_skipsUnusable pins that unusable rows are dropped
// rather than recorded as an epoch-zero reply.
func TestLatestInboundPerChat_skipsUnusable(t *testing.T) {
	p := WebhookPayload{Entry: []WebhookEntry{{Changes: []WebhookChange{
		{Value: WebhookValue{Messages: []InboundMessage{
			{From: "", Timestamp: "1749416383"},            // no sender
			{From: "16505551234", Timestamp: "garbage"},    // bad timestamp
			{From: "16505559999", Timestamp: "1749416384"}, // good
		}}},
	}}}}

	got := p.LatestInboundPerChat()
	if len(got) != 1 {
		t.Fatalf("got %v, want only the usable entry", got)
	}
	if _, ok := got["16505559999"]; !ok {
		t.Errorf("got %v, want the good sender", got)
	}
}
