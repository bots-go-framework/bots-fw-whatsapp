package whatsapp

import (
	"strconv"
	"time"
)

// WebhookPayload is the envelope Meta POSTs to a webhook endpoint.
//
// A single POST may batch up to 1000 updates, and Meta states batching "cannot be
// guaranteed", so always iterate — never assume one entry, one change, or one
// message.
//
// https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages
type WebhookPayload struct {
	// Object is "whatsapp_business_account" for WhatsApp webhooks.
	Object string `json:"object"`

	Entry []WebhookEntry `json:"entry"`
}

// WebhookEntry is one WhatsApp Business Account's updates within a payload.
type WebhookEntry struct {
	// ID is the WhatsApp Business Account ID.
	ID string `json:"id"`

	Changes []WebhookChange `json:"changes"`
}

// WebhookChange is one field's update.
type WebhookChange struct {
	// Field is "messages" for message webhooks.
	Field string `json:"field"`

	Value WebhookValue `json:"value"`
}

// WebhookValue carries the actual update.
//
// Meta distinguishes the two kinds structurally: an inbound user message populates
// Messages, while the status of a message the business sent populates Statuses.
// "You can easily identify these because they include a statuses array."
//
// This distinction is load-bearing for the customer service window — see
// InboundMessages.
type WebhookValue struct {
	MessagingProduct string `json:"messaging_product"`

	Metadata WebhookMetadata `json:"metadata"`

	// Contacts describes the sender of an inbound message.
	Contacts []WebhookContact `json:"contacts,omitempty"`

	// Messages is present when a WhatsApp user sent a message to the business.
	Messages []InboundMessage `json:"messages,omitempty"`

	// Statuses is present when reporting the status of a business-sent message.
	Statuses []MessageStatus `json:"statuses,omitempty"`
}

// WebhookMetadata identifies the business phone number the update concerns.
type WebhookMetadata struct {
	DisplayPhoneNumber string `json:"display_phone_number"`

	// PhoneNumberID is the ID to use when replying.
	PhoneNumberID string `json:"phone_number_id"`
}

// WebhookContact describes an inbound message's sender.
type WebhookContact struct {
	Profile WebhookProfile `json:"profile"`

	// WaID is the sender's WhatsApp ID — a phone number.
	WaID string `json:"wa_id"`
}

// WebhookProfile is a sender's WhatsApp profile.
type WebhookProfile struct {
	Name string `json:"name"`
}

// InboundMessage is a message a WhatsApp user sent to the business.
//
// Note the field set here is limited to what has been verified against Meta's
// docs. Per-type payloads (interactive button_reply / list_reply, media, location,
// contacts) each have a dedicated reference page and are NOT modelled yet —
// see the package roadmap. Guessing a wire shape is not worth the round trip.
type InboundMessage struct {
	// From is the sender's WhatsApp ID (phone number).
	From string `json:"from"`

	// ID is the message's wamid.
	ID string `json:"id"`

	// Timestamp is unix seconds — as a STRING, not a number.
	Timestamp string `json:"timestamp"`

	// Type discriminates the payload, e.g. "text", "interactive", "unsupported".
	Type string `json:"type"`

	// Text is present when Type == "text".
	Text *InboundText `json:"text,omitempty"`

	// Context is present when the message replies to another.
	Context *InboundContext `json:"context,omitempty"`
}

// InboundText is the body of an inbound text message.
type InboundText struct {
	Body string `json:"body"`
}

// InboundContext links an inbound message to the one it replies to.
type InboundContext struct {
	From string `json:"from,omitempty"`

	// ID is the wamid of the message being replied to.
	ID string `json:"id,omitempty"`
}

// SentAt parses Timestamp into a time.Time.
//
// Returns the zero Time if it is absent or unparseable, so a malformed timestamp
// degrades to "unknown" rather than to 1970 — which would look like an ancient
// reply and wrongly close the customer service window.
func (m InboundMessage) SentAt() time.Time {
	if m.Timestamp == "" {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(m.Timestamp, 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	return time.Unix(secs, 0).UTC()
}

// MessageStatus reports the delivery status of a message the business sent.
type MessageStatus struct {
	// ID is the wamid of the business's outbound message.
	ID string `json:"id"`

	// Status is "sent", "delivered", "read", or "failed".
	Status string `json:"status"`

	// Timestamp is unix seconds, as a string.
	Timestamp string `json:"timestamp"`

	// RecipientID is the WhatsApp user the message was sent to.
	RecipientID string `json:"recipient_id"`

	Conversation *StatusConversation `json:"conversation,omitempty"`

	Pricing *StatusPricing `json:"pricing,omitempty"`
}

// StatusConversation identifies the billed conversation a message belongs to.
type StatusConversation struct {
	ID string `json:"id"`

	Origin *ConversationOrigin `json:"origin,omitempty"`
}

// ConversationOrigin describes what opened a conversation.
type ConversationOrigin struct {
	// Type is the conversation category, e.g. "service".
	Type string `json:"type"`
}

// StatusPricing reports whether a message was billable.
//
// This is how a business learns a template actually cost money — worth surfacing
// rather than discarding, since outside the 24h window every send is a template.
type StatusPricing struct {
	Billable bool `json:"billable"`

	PricingModel string `json:"pricing_model"`

	// Category is the billing category, e.g. "service".
	Category string `json:"category"`
}

// InboundMessages returns every message a WhatsApp user sent, flattened across all
// entries and changes in the payload.
//
// Statuses are deliberately excluded. A status webhook reports the fate of the
// BUSINESS's own outbound message, and each outbound message produces up to three
// of them (sent, delivered, read). Treating those as user activity would hold the
// customer service window open forever on the bot's own traffic — the window would
// never appear to close, every proactive free-form send would be attempted, and
// every one would fail with 131047.
func (p WebhookPayload) InboundMessages() []InboundMessage {
	var out []InboundMessage
	for _, entry := range p.Entry {
		for _, change := range entry.Changes {
			out = append(out, change.Value.Messages...)
		}
	}
	return out
}

// Statuses returns every outbound-message status in the payload.
func (p WebhookPayload) Statuses() []MessageStatus {
	var out []MessageStatus
	for _, entry := range p.Entry {
		for _, change := range entry.Changes {
			out = append(out, change.Value.Statuses...)
		}
	}
	return out
}

// LatestInboundPerChat returns the most recent inbound timestamp per sender.
//
// This is what feeds LastInboundProvider: the handler records these on every
// inbound webhook, unconditionally, because bots-fw's DtLastInteraction only
// advances when chat data changed and so cannot be trusted for the window.
//
// Messages with an unparseable timestamp are skipped rather than recorded as zero.
func (p WebhookPayload) LatestInboundPerChat() map[string]time.Time {
	out := make(map[string]time.Time)
	for _, m := range p.InboundMessages() {
		if m.From == "" {
			continue
		}
		at := m.SentAt()
		if at.IsZero() {
			continue
		}
		if prev, ok := out[m.From]; !ok || at.After(prev) {
			out[m.From] = at
		}
	}
	return out
}
