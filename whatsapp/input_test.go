package whatsapp

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
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

// metaTemplateButtonTap is Meta's documented template quick-reply payload, verbatim.
// https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/button
const metaTemplateButtonTap = `{
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQUFERjg0NDEzNDdFODU3MUMxMAA=",
  "timestamp": "1750091045",
  "type": "button",
  "context": {"id": "wamid.THE_TEMPLATE_WE_SENT"},
  "button": {"payload": "Unsubscribe", "text": "Unsubscribe"}
}`

// metaButtonReply is Meta's documented interactive button_reply payload, verbatim.
const metaButtonReply = `{
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQTZAQzg0MzQ4QjRCM0NGNkVGOAA=",
  "timestamp": "1750025136",
  "type": "interactive",
  "interactive": {"type": "button_reply", "button_reply": {"id": "cancel-button", "title": "Cancel"}}
}`

// metaListReply is Meta's documented interactive list_reply payload, verbatim.
const metaListReply = `{
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQUFERjg0NDEzNDdFODU3MUMxMAA=",
  "timestamp": "1749854575",
  "type": "interactive",
  "interactive": {"type": "list_reply", "list_reply": {
    "id": "priority_express", "title": "Priority Mail Express", "description": "Next Day to 2 Days"}}
}`

func inputFromJSON(t *testing.T, raw string) botinput.InputMessage {
	t.Helper()
	var m InboundMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("failed to parse Meta's example: %v", err)
	}
	return NewWebhookInput(m, "106540352242922", nil)
}

// TestWebhookInput_interactiveReplyCarriesCallbackState pins the good case: inside
// the 24h window, the business-supplied id round-trips, so Debtus's "pay?id=42"
// callback data survives exactly as on Telegram.
func TestWebhookInput_interactiveReplyCarriesCallbackState(t *testing.T) {
	in := inputFromJSON(t, metaButtonReply)

	if in.InputType() != botinput.TypeCallbackQuery {
		t.Errorf("InputType = %v, want TypeCallbackQuery so existing handlers route it", in.InputType())
	}
	cq, ok := in.(botinput.CallbackQuery)
	if !ok {
		t.Fatalf("%T does not implement botinput.CallbackQuery", in)
	}
	if cq.GetData() != "cancel-button" {
		t.Errorf("GetData() = %q, want the business-supplied id", cq.GetData())
	}
	if in.(waCallbackQuery).IsTemplateButton() {
		t.Error("an interactive reply is not a template button")
	}
}

func TestWebhookInput_listReplyCarriesCallbackState(t *testing.T) {
	in := inputFromJSON(t, metaListReply)
	cq := in.(botinput.CallbackQuery)
	if cq.GetData() != "priority_express" {
		t.Errorf("GetData() = %q, want the business-supplied row id", cq.GetData())
	}
}

// TestWebhookInput_templateButtonCarriesNoState is the finding that matters most in
// this package.
//
// Outside the 24h window — where every Debtus reminder lives — a template button tap
// returns only the button's LABEL. Meta describes button.payload and button.text
// identically ("Quick-reply button label text") and its own example carries the same
// value in both. There is no per-message state, so "which debt was this?" cannot be
// answered from the payload. context.id is the only link back.
func TestWebhookInput_templateButtonCarriesNoState(t *testing.T) {
	in := inputFromJSON(t, metaTemplateButtonTap)

	if in.InputType() != botinput.TypeCallbackQuery {
		t.Errorf("InputType = %v, want TypeCallbackQuery", in.InputType())
	}
	cq := in.(waCallbackQuery)

	if !cq.IsTemplateButton() {
		t.Error("IsTemplateButton() must be true so callers know GetData is only a label")
	}
	// The label, not state. This is the whole point.
	if cq.GetData() != "Unsubscribe" {
		t.Errorf("GetData() = %q, want the button label", cq.GetData())
	}
	// The correlation key, and the only one available.
	if cq.ContextMessageID() != "wamid.THE_TEMPLATE_WE_SENT" {
		t.Errorf("ContextMessageID() = %q, want the wamid of the template we sent", cq.ContextMessageID())
	}
	// Telegram embeds the original message here, which is what makes "edit the
	// message you came from" work. WhatsApp sends only its id.
	if cq.GetMessage() != nil {
		t.Error("GetMessage() must be nil: the webhook carries no original message content")
	}
}

// TestWebhookInput_templateButtonDataIsNotRoutableAsURL pins why the framework's
// router cannot route a template tap.
//
// bots-fw parses callback data as a URL and matches commands on its path. A label
// like "Pay now" has no path to match, so template-borne callbacks must be routed by
// ContextMessageID instead.
func TestWebhookInput_templateButtonDataIsNotRoutableAsURL(t *testing.T) {
	in := inputFromJSON(t, metaTemplateButtonTap)
	data := in.(botinput.CallbackQuery).GetData()

	u, err := url.Parse(data)
	if err != nil {
		return // unparseable is fine — it certainly will not route
	}
	if u.Path == data && strings.Contains(data, "?") {
		t.Errorf("unexpected: %q looks URL-shaped; the point is that a label is not", data)
	}
}
