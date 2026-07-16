package whatsapp

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-fw/botinput"
)

// TestNewWebhookInput_text pins conversion of Meta's verbatim inbound-text example
// into a bots-fw input.
func TestNewWebhookInput_text(t *testing.T) {
	var p WebhookPayload
	if err := json.Unmarshal([]byte(metaInboundTextWebhook), &p); err != nil {
		t.Fatalf("failed to parse Meta's example: %v", err)
	}
	value := p.Entry[0].Changes[0].Value
	in := NewWebhookInput(value.Messages[0], value.Metadata.PhoneNumberID, value.Contacts)

	if in.InputType() != botinput.TypeText {
		t.Errorf("InputType = %v, want TypeText", in.InputType())
	}
	text, ok := in.(botinput.TextMessage)
	if !ok {
		t.Fatalf("%T does not implement botinput.TextMessage", in)
	}
	if text.Text() != "Does it come in another color?" {
		t.Errorf("Text() = %q", text.Text())
	}
	if chatID, err := in.BotChatID(); err != nil || chatID != "16505551234" {
		t.Errorf("BotChatID() = %q, %v", chatID, err)
	}
	if want := time.Unix(1749416383, 0).UTC(); !in.GetTime().Equal(want) {
		t.Errorf("GetTime() = %v, want %v", in.GetTime(), want)
	}
}

// TestWebhookInput_identityIsAPhoneNumber pins gap-analysis §1.4/§1.5 concretely:
// the sender's ID is a string, and the message ID is an opaque string with no
// integer form.
func TestWebhookInput_identityIsAPhoneNumber(t *testing.T) {
	in := NewWebhookInput(
		InboundMessage{From: "16505551234", ID: "wamid.ABC", Timestamp: "1749416383", Type: "text",
			Text: &InboundText{Body: "hi"}},
		"106540352242922",
		[]WebhookContact{{WaID: "16505551234", Profile: WebhookProfile{Name: "Sheena Nelson"}}},
	)

	if got := in.GetSender().GetID(); got != "16505551234" {
		t.Errorf("GetID() = %v (%T), want the phone number as a string", got, got)
	}
	if in.MessageStringID() != "wamid.ABC" {
		t.Errorf("MessageStringID() = %q, want the wamid", in.MessageStringID())
	}
	// A wamid has no integer form. This method is mandatory only because Telegram's
	// message IDs are ints.
	if in.MessageIntID() != 0 {
		t.Errorf("MessageIntID() = %d, want 0: a wamid is not a number", in.MessageIntID())
	}
	if in.GetRecipient().GetID() != "106540352242922" {
		t.Errorf("recipient = %v, want the business phone number ID", in.GetRecipient().GetID())
	}
}

// TestWebhookInput_profileName pins that the display name is pulled from the
// payload's contacts array, which sits alongside messages rather than inside them.
func TestWebhookInput_profileName(t *testing.T) {
	contacts := []WebhookContact{
		{WaID: "999", Profile: WebhookProfile{Name: "Someone Else"}},
		{WaID: "16505551234", Profile: WebhookProfile{Name: "Sheena Nelson"}},
	}
	in := NewWebhookInput(InboundMessage{From: "16505551234", Type: "text", Text: &InboundText{}}, "1", contacts)

	sender := in.GetSender()
	if sender.GetFirstName() != "Sheena Nelson" {
		t.Errorf("GetFirstName() = %q", sender.GetFirstName())
	}
	// WhatsApp has one display name, not a first/last pair. Splitting on a space
	// would mangle names that are not "Given Family", so last name stays empty.
	if sender.GetLastName() != "" {
		t.Errorf("GetLastName() = %q, want empty", sender.GetLastName())
	}
	if sender.GetUserName() != "" {
		t.Errorf("GetUserName() = %q, want empty: WhatsApp has no @usernames", sender.GetUserName())
	}
}

// TestWebhookInput_unknownSenderHasNoName pins that a missing contact yields an
// empty name rather than a mismatched one.
func TestWebhookInput_unknownSenderHasNoName(t *testing.T) {
	in := NewWebhookInput(
		InboundMessage{From: "16505551234", Type: "text", Text: &InboundText{}},
		"1",
		[]WebhookContact{{WaID: "someone-else", Profile: WebhookProfile{Name: "Wrong Person"}}},
	)
	if got := in.GetSender().GetFirstName(); got != "" {
		t.Errorf("GetFirstName() = %q, want empty rather than another contact's name", got)
	}
}

// TestWebhookInput_unsupportedTypeIsRoutableNotDropped pins the degradation
// principle on the inbound side: an unmodelled type still produces an input, so the
// bot can answer rather than go silent.
func TestWebhookInput_unsupportedTypeIsRoutableNotDropped(t *testing.T) {
	for _, waType := range []string{"image", "location", "interactive", "audio", "unsupported"} {
		in := NewWebhookInput(InboundMessage{From: "1", ID: "wamid.X", Type: waType}, "1", nil)
		if in == nil {
			t.Fatalf("%s: input must never be nil — a dropped message means a silent bot", waType)
		}
		if in.InputType() != botinput.TypeNotImplemented {
			t.Errorf("%s: InputType = %v, want TypeNotImplemented", waType, in.InputType())
		}
		u, ok := in.(waUnsupportedMessage)
		if !ok {
			t.Fatalf("%s: got %T", waType, in)
		}
		if u.WhatsAppType() != waType {
			t.Errorf("WhatsAppType() = %q, want %q so a handler can see what arrived", u.WhatsAppType(), waType)
		}
	}
}

// TestWebhookInput_textTypeWithoutBodyIsNotText pins that a malformed text message
// degrades rather than panicking on a nil body.
func TestWebhookInput_textTypeWithoutBodyIsNotText(t *testing.T) {
	in := NewWebhookInput(InboundMessage{From: "1", ID: "wamid.X", Type: "text"}, "1", nil)
	if in.InputType() != botinput.TypeNotImplemented {
		t.Errorf("a text message with no text object must not claim to be TypeText")
	}
}

func TestWebhookInput_noSender(t *testing.T) {
	in := NewWebhookInput(InboundMessage{ID: "wamid.X", Type: "text", Text: &InboundText{Body: "hi"}}, "1", nil)
	if _, err := in.BotChatID(); !errors.Is(err, ErrNoChatID) {
		t.Errorf("BotChatID() err = %v, want ErrNoChatID", err)
	}
}

// TestWebhookInput_isEditedIsAlwaysFalse pins that inbound edits cannot exist —
// the concept is Telegram's.
func TestWebhookInput_isEditedIsAlwaysFalse(t *testing.T) {
	in := NewWebhookInput(
		InboundMessage{From: "1", ID: "wamid.X", Type: "text", Text: &InboundText{Body: "hi"}}, "1", nil)
	if in.(botinput.TextMessage).IsEdited() {
		t.Error("IsEdited() must be false: WhatsApp has no edit endpoint")
	}
}

func TestWebhookInput_platformAndChat(t *testing.T) {
	in := NewWebhookInput(
		InboundMessage{From: "16505551234", ID: "wamid.X", Type: "text", Text: &InboundText{Body: "hi"}}, "1", nil)

	if got := in.GetSender().Platform(); got != string(PlatformID) {
		t.Errorf("Platform() = %q, want %q", got, PlatformID)
	}
	chat := in.Chat()
	if chat.GetID() != "16505551234" {
		t.Errorf("chat.GetID() = %q", chat.GetID())
	}
	if chat.IsGroupChat() {
		t.Error("a 1:1 chat must not report as a group")
	}
	if chat.GetType() != "individual" {
		t.Errorf("chat.GetType() = %q", chat.GetType())
	}
}
