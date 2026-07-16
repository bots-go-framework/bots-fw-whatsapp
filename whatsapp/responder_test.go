package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botsfw"
	"github.com/bots-go-framework/bots-go-core/botkb"
)

// sendOK is a stub Cloud API accepting any send and echoing a wamid.
func sendOK(t *testing.T, capture *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			*capture, _ = io.ReadAll(r.Body)
		}
		_, _ = w.Write([]byte(`{"messaging_product":"whatsapp","messages":[{"id":"wamid.SENT"}]}`))
	}))
}

// newTestResponder wires a Responder at ts, with the user having replied recently
// so the window is open and does not interfere with what is under test.
func newTestResponder(ts *httptest.Server) *Responder {
	c := wabotapi.NewClientWithHTTPClient("tok", "1234567890", ts.Client())
	c.BaseURL = ts.URL
	return NewResponder(c, LastInboundFunc(func(context.Context, string) (time.Time, error) {
		return time.Now().Add(-time.Minute), nil
	}))
}

func TestResponder_satisfiesBothInterfaces(t *testing.T) {
	var _ botsfw.WebhookResponder = (*Responder)(nil)
	// The gate half is essential: bots-fw treats a responder that does NOT implement
	// SendGate as always permitting, so omitting it would silently attempt every
	// out-of-window send.
	var _ botsfw.SendGate = (*Responder)(nil)
}

func TestResponder_sendsText(t *testing.T) {
	var body []byte
	ts := sendOK(t, &body)
	defer ts.Close()

	r := newTestResponder(ts)
	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	m.Text = "hello"

	resp, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d", resp.StatusCode)
	}
	if resp.Message == nil || resp.Message.GetMessageID() != "wamid.SENT" {
		t.Errorf("Message = %+v, want the wamid", resp.Message)
	}

	var sent map[string]any
	if err = json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("bad request body: %v", err)
	}
	if sent["type"] != "text" || sent["to"] != "16505551234" {
		t.Errorf("sent = %v", sent)
	}
}

func TestResponder_sendsTemplate(t *testing.T) {
	var body []byte
	ts := sendOK(t, &body)
	defer ts.Close()

	r := newTestResponder(ts)
	m := botmsg.MessageFromBot{
		ToChat: ChatID("16505551234"),
		BotMessage: TemplateMessage{
			Name: "payment_reminder", LanguageCode: "en_US", BodyParams: []string{"Jessica", "$40"},
		},
	}

	if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("bad request body: %v", err)
	}
	if sent["type"] != "template" {
		t.Errorf("type = %v, want template", sent["type"])
	}
}

// TestResponder_rejectsEdit pins the #3 Debtus collision at the type level.
//
// 30 sites use the "tap a button, rewrite the message in place" idiom. WhatsApp has
// no edit endpoint, so this must fail loudly here rather than silently send a
// duplicate message and leave the original stale on the user's phone.
func TestResponder_rejectsEdit(t *testing.T) {
	var body []byte
	ts := sendOK(t, &body)
	defer ts.Close()
	r := newTestResponder(ts)

	for _, tt := range []struct {
		name string
		mut  func(*botmsg.MessageFromBot)
	}{
		{"IsEdit", func(m *botmsg.MessageFromBot) { m.IsEdit = true }},
		{"EditMessageIntID", func(m *botmsg.MessageFromBot) { m.EditMessageIntID = 42 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body = nil
			m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
			m.Text = "updated"
			tt.mut(&m)

			_, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS)
			if !errors.Is(err, ErrEditNotSupported) {
				t.Errorf("got %v, want ErrEditNotSupported", err)
			}
			if body != nil {
				t.Error("a rejected edit must not reach the API as a new message")
			}
		})
	}
}

// TestResponder_rejectsRichFormat pins the #7 Debtus collision.
//
// 88 sites use FormatHTML. WhatsApp's markup support is UNVERIFIED, so passing HTML
// through would render literal "<b>" to real users. Failing loudly is the honest
// behaviour until the formatting question is answered.
func TestResponder_rejectsRichFormat(t *testing.T) {
	ts := sendOK(t, nil)
	defer ts.Close()
	r := newTestResponder(ts)

	for _, f := range []botmsg.Format{botmsg.FormatHTML, botmsg.FormatMarkdown} {
		m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
		m.Text = "<b>bold</b>"
		m.Format = f

		_, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS)
		if !errors.Is(err, ErrFormatNotSupported) {
			t.Errorf("format %v: got %v, want ErrFormatNotSupported", f, err)
		}
	}
}

// TestResponder_rejectsKeyboard pins the #2 Debtus collision: 158 button
// constructions, and the interactive wire shape is not yet modelled.
//
// Note the shape of botkb itself: NewMessageKeyboard takes [][]Button — rows, i.e.
// a Telegram grid — and every KeyboardType constant is annotated "Used by:
// Telegram". WhatsApp allows a FLAT MAXIMUM OF 3 reply buttons, so botkb's model
// does not merely need mapping, it does not fit. This adapter is botkb's first
// real consumer and it cannot use it as-is.
func TestResponder_rejectsKeyboard(t *testing.T) {
	ts := sendOK(t, nil)
	defer ts.Close()
	r := newTestResponder(ts)

	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	m.Text = "pick one"
	m.Keyboard = botkb.NewMessageKeyboard(botkb.KeyboardTypeInline,
		[]botkb.Button{botkb.NewDataButton("Pay now", "pay?id=1")},
	)

	_, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS)
	if !errors.Is(err, ErrKeyboardNotSupported) {
		t.Errorf("got %v, want ErrKeyboardNotSupported", err)
	}
}

// TestResponder_templateExemptFromFormatAndKeyboardChecks pins that a template's
// content is defined by the approved template, not by these fields — so they must
// not block the one message type that works outside the window.
func TestResponder_templateExemptFromFormatAndKeyboardChecks(t *testing.T) {
	ts := sendOK(t, nil)
	defer ts.Close()
	r := newTestResponder(ts)

	m := botmsg.MessageFromBot{
		ToChat:     ChatID("16505551234"),
		BotMessage: TemplateMessage{Name: "payment_reminder", LanguageCode: "en_US"},
	}
	m.Format = botmsg.FormatHTML // would be rejected on a free-form message

	if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); err != nil {
		t.Errorf("a template must not be blocked by free-form checks, got: %v", err)
	}
}

// TestResponder_editRejectedEvenForTemplate pins that edit is refused first:
// there is no edit endpoint for ANY message type.
func TestResponder_editRejectedEvenForTemplate(t *testing.T) {
	ts := sendOK(t, nil)
	defer ts.Close()
	r := newTestResponder(ts)

	m := botmsg.MessageFromBot{
		ToChat:     ChatID("16505551234"),
		BotMessage: TemplateMessage{Name: "x", LanguageCode: "en_US"},
	}
	m.IsEdit = true

	if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); !errors.Is(err, ErrEditNotSupported) {
		t.Errorf("got %v, want ErrEditNotSupported", err)
	}
}

func TestResponder_noRecipient(t *testing.T) {
	ts := sendOK(t, nil)
	defer ts.Close()
	r := newTestResponder(ts)

	m := botmsg.MessageFromBot{}
	m.Text = "hello"
	if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); !errors.Is(err, ErrNoRecipient) {
		t.Errorf("got %v, want ErrNoRecipient", err)
	}
}

// TestResponder_DeleteMessage pins that delete always fails. The method exists only
// because botsfw.WebhookResponder requires it of every platform — the framework
// mandating a Telegram capability as universal.
func TestResponder_DeleteMessage(t *testing.T) {
	ts := sendOK(t, nil)
	defer ts.Close()
	r := newTestResponder(ts)

	if err := r.DeleteMessage(context.Background(), "wamid.X"); !errors.Is(err, ErrDeleteNotSupported) {
		t.Errorf("got %v, want ErrDeleteNotSupported", err)
	}
}

// TestResponder_CanSendDelegatesToWindow pins that the responder's gate half
// actually enforces the window rather than rubber-stamping.
func TestResponder_CanSendDelegatesToWindow(t *testing.T) {
	ts := sendOK(t, nil)
	defer ts.Close()

	c := wabotapi.NewClientWithHTTPClient("tok", "1234567890", ts.Client())
	c.BaseURL = ts.URL
	// The user last replied 48h ago: the window is shut.
	r := NewResponder(c, LastInboundFunc(func(context.Context, string) (time.Time, error) {
		return time.Now().Add(-48 * time.Hour), nil
	}))

	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	m.Text = "a proactive reminder"

	err := r.CanSend(context.Background(), m)
	if !botsfw.IsSendNotPermitted(err) {
		t.Errorf("got %v, want a SendGate refusal", err)
	}

	// ...but a template is the documented remedy, and must pass.
	tm := botmsg.MessageFromBot{
		ToChat:     ChatID("16505551234"),
		BotMessage: TemplateMessage{Name: "payment_reminder", LanguageCode: "en_US"},
	}
	if err = r.CanSend(context.Background(), tm); err != nil {
		t.Errorf("a template must pass the gate outside the window, got: %v", err)
	}
}

// TestResponder_channelIsIgnored pins that BotAPISendMessageOverResponse does not
// change behaviour. That channel means "write the API call into the webhook's 200
// body" — a Telegram trick. The Cloud API needs a separate authenticated request
// either way, and Telegram's own responder discards the channel too.
func TestResponder_channelIsIgnored(t *testing.T) {
	var body []byte
	ts := sendOK(t, &body)
	defer ts.Close()
	r := newTestResponder(ts)

	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	m.Text = "hello"

	if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverResponse); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(body) == 0 {
		t.Error("the message must still be sent over HTTPS regardless of channel")
	}
}

func TestNewResponder_panicsOnNilClient(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected a panic on a nil client")
		}
	}()
	NewResponder(nil, LastInboundFunc(func(context.Context, string) (time.Time, error) {
		return time.Time{}, nil
	}))
}
