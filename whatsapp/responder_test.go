package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestResponder_editDegradesToNewMessage pins the #3 Debtus collision DEGRADING
// rather than failing.
//
// 30 sites use "tap a button, rewrite the message in place". WhatsApp has no edit
// endpoint, so the update arrives as a new message and the conversation becomes
// append-only. Debtus keeps working unchanged; the loss is reported, not raised.
func TestResponder_editDegradesToNewMessage(t *testing.T) {
	var body []byte
	ts := sendOK(t, &body)
	defer ts.Close()

	var got []Degradation
	r := newTestResponder(ts).OnDegradation(func(_ context.Context, _ botmsg.MessageFromBot, n []Degradation) {
		got = n
	})

	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	m.Text = "updated"
	m.IsEdit = true

	if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); err != nil {
		t.Fatalf("an edit must degrade to a new message, not error: %v", err)
	}
	if body == nil {
		t.Fatal("the update must still reach the user as a new message")
	}
	if !hasNote(got, "no edit endpoint") {
		t.Errorf("the loss must be reported, got %v", got)
	}
}

// TestResponder_richFormatDegrades pins the #7 Debtus collision degrading: 88
// FormatHTML sites keep working, minus the styling, and never emit literal tags.
func TestResponder_richFormatDegrades(t *testing.T) {
	var body []byte
	ts := sendOK(t, &body)
	defer ts.Close()
	r := newTestResponder(ts)

	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	m.Text = "<b>Overdue</b>"
	m.Format = botmsg.FormatHTML

	if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); err != nil {
		t.Fatalf("HTML must degrade, not error: %v", err)
	}
	if strings.Contains(string(body), `<b>`) || strings.Contains(string(body), "<b>") {
		t.Errorf("literal tags must never reach a user: %s", body)
	}
	if !strings.Contains(string(body), "Overdue") {
		t.Errorf("the text must survive: %s", body)
	}
}

// TestResponder_keyboardDegradesToButtons pins the #2 Debtus collision degrading:
// a rich keyboard becomes native reply buttons, callback routing intact.
func TestResponder_keyboardDegradesToButtons(t *testing.T) {
	var body []byte
	ts := sendOK(t, &body)
	defer ts.Close()
	r := newTestResponder(ts)

	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	m.Text = "You owe $40"
	m.Keyboard = botkb.NewMessageKeyboard(botkb.KeyboardTypeInline,
		[]botkb.Button{botkb.NewDataButton("Pay now", "pay?id=1")},
	)

	if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); err != nil {
		t.Fatalf("a keyboard must degrade, not error: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("bad body: %v", err)
	}
	if sent["type"] != "interactive" {
		t.Errorf("type = %v, want interactive reply buttons", sent["type"])
	}
	if !strings.Contains(string(body), "pay?id=1") {
		t.Errorf("callback data must survive: %s", body)
	}
}

// TestResponder_templateIgnoresDegradation pins that a template's content comes
// from the approved template, so free-form degradation does not touch it.
func TestResponder_templateIgnoresDegradation(t *testing.T) {
	var body []byte
	ts := sendOK(t, &body)
	defer ts.Close()
	r := newTestResponder(ts)

	m := botmsg.MessageFromBot{
		ToChat:     ChatID("16505551234"),
		BotMessage: TemplateMessage{Name: "payment_reminder", LanguageCode: "en_US"},
	}
	m.Format = botmsg.FormatHTML

	if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("bad body: %v", err)
	}
	if sent["type"] != "template" {
		t.Errorf("type = %v, want template", sent["type"])
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
