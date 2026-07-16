package whatsapp

import (
	"context"
	"errors"
	"time"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botinput"
	"github.com/strongo/logus"
)

// waActor is a WhatsApp participant — the sender or the business.
//
// WhatsApp identity is a phone number (wa_id), and the Cloud API exposes almost
// nothing else about a user: no username, no surname, no language, no avatar. Most
// of botinput.User is therefore unanswerable here, and every such method returns
// empty rather than inventing a value.
type waActor struct {
	waID string

	// name is the WhatsApp profile name, present only on inbound messages via
	// contacts[].profile.name. It is a single display string, not first/last.
	name string

	isBot bool
}

var (
	_ botinput.Actor     = (*waActor)(nil)
	_ botinput.User      = (*waActor)(nil)
	_ botinput.Recipient = (*waActor)(nil)
)

// Platform implements botinput.Actor.
func (a waActor) Platform() string { return string(PlatformID) }

// GetID implements botinput.Actor.
//
// Returns a string: a wa_id is a phone number. Note the interface is `any`, a
// Telegram-shaped concession to its int64 user IDs, and bots-fw stringifies the
// result with fmt.Sprintf("%v", ...) anyway.
func (a waActor) GetID() any { return a.waID }

// IsBotUser implements botinput.Actor.
func (a waActor) IsBotUser() bool { return a.isBot }

// GetFirstName implements botinput.Actor.
//
// WhatsApp profiles carry one display name, not a first/last pair. It is reported
// as the first name so a greeting has something to use, and GetLastName is empty —
// rather than splitting on a space, which mangles names that are not "Given Family".
func (a waActor) GetFirstName() string { return a.name }

// GetLastName implements botinput.Actor. Always empty — see GetFirstName.
func (a waActor) GetLastName() string { return "" }

// GetUserName implements botinput.Actor. Always empty: WhatsApp has no @usernames.
func (a waActor) GetUserName() string { return "" }

// GetLanguage implements botinput.Actor.
//
// Always empty: the Cloud API does not report a user's language on inbound
// messages. A bot needing one must ask, or infer it from the phone number's country
// code — which this package deliberately does not guess at.
func (a waActor) GetLanguage() string { return "" }

// GetAvatar implements botinput.Sender. Always empty: profile pictures are not
// exposed on inbound messages.
func (a waActor) GetAvatar() string { return "" }

// GetCountry implements botinput.User. Always empty.
//
// A wa_id begins with a country calling code, so this is inferable — but doing so
// requires a prefix table, and a wrong country is worse than none.
func (a waActor) GetCountry() string { return "" }

// waChat is a WhatsApp conversation.
type waChat struct {
	waID    string
	isGroup bool
}

var _ botinput.Chat = (*waChat)(nil)

// GetID implements botinput.Chat. The chat ID is the user's phone number.
func (c waChat) GetID() string { return c.waID }

// GetType implements botinput.Chat.
func (c waChat) GetType() string {
	if c.isGroup {
		return "group"
	}
	return "individual"
}

// IsGroupChat implements botinput.Chat.
//
// Always false for now: the Groups API is a separate surface (invite-only, max 8
// participants, requires an Official Business Account) and is not wired up here.
func (c waChat) IsGroupChat() bool { return c.isGroup }

// ErrNoChatID is returned when an inbound message carries no sender.
var ErrNoChatID = errors.New("cannot determine chat ID: inbound message has no sender")

// waInputMessage is the shared base for every inbound WhatsApp message type.
type waInputMessage struct {
	msg InboundMessage

	// phoneNumberID is the business number the message arrived on.
	phoneNumberID string

	sender waActor
}

// GetSender implements botinput.InputMessage.
func (m waInputMessage) GetSender() botinput.User { return m.sender }

// GetRecipient implements botinput.InputMessage — the business phone number.
func (m waInputMessage) GetRecipient() botinput.Recipient {
	return waActor{waID: m.phoneNumberID, isBot: true}
}

// GetTime implements botinput.InputMessage.
func (m waInputMessage) GetTime() time.Time { return m.msg.SentAt() }

// MessageIntID implements botinput.InputMessage.
//
// Always 0. A wamid is an opaque string ("wamid.HBgLMTY1..."), not a number, so
// there is no integer to return. This method is mandatory on every input because
// Telegram's message IDs are ints; use MessageStringID instead.
func (m waInputMessage) MessageIntID() int { return 0 }

// MessageStringID implements botinput.InputMessage. Returns the wamid.
func (m waInputMessage) MessageStringID() string { return m.msg.ID }

// BotChatID implements botinput.InputMessage.
func (m waInputMessage) BotChatID() (string, error) {
	if m.msg.From == "" {
		return "", ErrNoChatID
	}
	return m.msg.From, nil
}

// Chat implements botinput.InputMessage.
func (m waInputMessage) Chat() botinput.Chat {
	return waChat{waID: m.msg.From}
}

// LogRequest implements botinput.InputMessage.
//
// Deliberately logs no message content: bodies carry whatever a user typed, and
// this runs on every inbound message.
func (m waInputMessage) LogRequest() {
	// The interface takes no context, so there is none to pass through.
	logus.Debugf(context.Background(), "whatsapp inbound: type=%s id=%s", m.msg.Type, m.msg.ID)
}

// waTextMessage is an inbound text message.
type waTextMessage struct {
	waInputMessage
}

var (
	_ botinput.Message      = (*waTextMessage)(nil)
	_ botinput.InputMessage = (*waTextMessage)(nil)
	_ botinput.TextMessage  = (*waTextMessage)(nil)
)

// InputType implements botinput.InputMessage.
func (m waTextMessage) InputType() botinput.Type { return botinput.TypeText }

// Text implements botinput.TextMessage.
func (m waTextMessage) Text() string {
	if m.msg.Text == nil {
		return ""
	}
	return m.msg.Text.Body
}

// IsEdited implements botinput.TextMessage.
//
// Always false. WhatsApp has no edit endpoint, so an edited inbound message cannot
// exist — the concept is Telegram's.
func (m waTextMessage) IsEdited() bool { return false }

// waUnsupportedMessage is an inbound message of a type this adapter does not model
// yet — media, location, contacts, interactive replies.
//
// It exists so an unmodelled type degrades to a routable input rather than being
// dropped: the bot can answer "I can't read that yet" instead of going silent.
type waUnsupportedMessage struct {
	waInputMessage
}

var _ botinput.InputMessage = (*waUnsupportedMessage)(nil)

// InputType implements botinput.InputMessage.
func (m waUnsupportedMessage) InputType() botinput.Type { return botinput.TypeNotImplemented }

// WhatsAppType returns the raw Cloud API message type, so a handler can log or
// branch on what actually arrived.
func (m waUnsupportedMessage) WhatsAppType() string { return m.msg.Type }

// NewWebhookInput converts an inbound Cloud API message into a bots-fw input.
//
// contacts supplies the sender's profile name, which the Cloud API puts alongside
// the messages array rather than inside each message.
//
// An unmodelled message type yields a waUnsupportedMessage rather than an error:
// dropping it would leave the user talking to a bot that never answers.
func NewWebhookInput(msg InboundMessage, phoneNumberID string, contacts []WebhookContact) botinput.InputMessage {
	base := waInputMessage{
		msg:           msg,
		phoneNumberID: phoneNumberID,
		sender:        waActor{waID: msg.From, name: profileName(msg.From, contacts)},
	}
	switch {
	case msg.Type == string(wabotapi.MessageTypeText) && msg.Text != nil:
		return waTextMessage{waInputMessage: base}

	// A template quick-reply tap. The payload is the button's LABEL, not state.
	case msg.Type == "button" && msg.Button != nil:
		return waCallbackQuery{waInputMessage: base, data: msg.Button.Payload}

	// A reply to a free-form interactive message. The id IS business-supplied.
	case msg.Type == string(wabotapi.MessageTypeInteractive) && msg.Interactive.Reply() != nil:
		return waCallbackQuery{waInputMessage: base, data: msg.Interactive.Reply().ID}
	}
	return waUnsupportedMessage{waInputMessage: base}
}

// profileName finds the display name for waID among a payload's contacts.
func profileName(waID string, contacts []WebhookContact) string {
	for _, c := range contacts {
		if c.WaID == waID {
			return c.Profile.Name
		}
	}
	return ""
}

// waCallbackQuery adapts a WhatsApp button tap to botinput.CallbackQuery.
//
// WhatsApp has no callback-query update type — Meta states taps arrive with "the
// same common structure" as any message. But bots-fw routes button presses through
// CallbackQuery, and an app's existing handlers are written against it, so mapping
// here is what lets those handlers work unchanged rather than forcing every app to
// learn WhatsApp's shape.
//
// GetData is where the two inbound kinds diverge, and the difference is the single
// most important thing in this package:
//
//   - interactive reply (inside the 24h window): the id is business-supplied, so
//     "pay?id=42" round-trips exactly. Telegram parity.
//   - template button tap (outside the window): the payload is only the button's
//     LABEL. There is no per-message state. Use ContextMessageID to correlate.
type waCallbackQuery struct {
	waInputMessage
	data string
}

var (
	_ botinput.CallbackQuery = (*waCallbackQuery)(nil)
	_ botinput.InputMessage  = (*waCallbackQuery)(nil)
)

// InputType implements botinput.InputMessage.
func (m waCallbackQuery) InputType() botinput.Type { return botinput.TypeCallbackQuery }

// GetID implements botinput.CallbackQuery — the wamid of the tap itself.
func (m waCallbackQuery) GetID() string { return m.msg.ID }

// GetFrom implements botinput.CallbackQuery.
func (m waCallbackQuery) GetFrom() botinput.Sender { return m.sender }

// GetMessage implements botinput.CallbackQuery.
//
// Always nil: the webhook carries only the wamid of the tapped message
// (context.id), never its content. Telegram embeds the whole original message here,
// which is what makes "edit the message you came from" possible — and is another
// reason that idiom does not port. Use ContextMessageID and look it up.
func (m waCallbackQuery) GetMessage() botinput.Message { return nil }

// GetData implements botinput.CallbackQuery.
//
// ⚠️ For a TEMPLATE button tap this is the button's label text, NOT developer state.
// bots-fw's router parses callback data as a URL and matches on its path
// (router.go:251-263), which will not match a label like "Pay now". Template-borne
// callbacks must be routed by ContextMessageID, not by this value.
func (m waCallbackQuery) GetData() string { return m.data }

// IsTemplateButton reports whether this tap came from a template button, in which
// case GetData is a label and carries no state.
func (m waCallbackQuery) IsTemplateButton() bool { return m.msg.Type == "button" }

// ContextMessageID returns the wamid of the message the tapped button belonged to.
//
// For template taps this is the ONLY link back to what the tap was about, so the
// mapping wamid -> subject must be stored when the template is sent.
func (m waCallbackQuery) ContextMessageID() string { return m.msg.ContextMessageID() }
