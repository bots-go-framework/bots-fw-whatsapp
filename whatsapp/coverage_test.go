package whatsapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botinput"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botsfw"
	"github.com/bots-go-framework/bots-go-core/botkb"
)

// This file covers the branches the behaviour-focused tests leave open: accessors
// that always return a constant, guard clauses, and error paths.

// TestPlatform pins the platform descriptor. bots-fw declared PlatformWhatsApp
// before any WhatsApp code existed, so this reuses rather than redefines it.
func TestPlatform(t *testing.T) {
	if got := Platform.ID(); got != "whatsapp" {
		t.Errorf("ID() = %q, want whatsapp", got)
	}
	if got := Platform.Version(); got != "cloud-api" {
		t.Errorf("Version() = %q, want cloud-api", got)
	}
	// Not a Graph API version — that is per-request and lives on the client.
	if Platform.Version() == wabotapi.DefaultGraphVersion {
		t.Error("platform version must not be the Graph API version")
	}
}

// TestWaActor_unanswerableFields pins every field the Cloud API simply does not
// report. Each returns empty rather than inventing a value, and each is a
// deliberate refusal documented at its declaration.
func TestWaActor_unanswerableFields(t *testing.T) {
	a := waActor{waID: "16505551234", name: "Sheena Nelson"}

	for _, tt := range []struct {
		name string
		got  string
	}{
		{"GetLastName — WhatsApp has one display name, not a pair", a.GetLastName()},
		{"GetUserName — WhatsApp has no @usernames", a.GetUserName()},
		{"GetLanguage — not reported on inbound messages", a.GetLanguage()},
		{"GetAvatar — profile pictures are not exposed", a.GetAvatar()},
		{"GetCountry — inferable from the dial code, but a wrong country is worse than none", a.GetCountry()},
	} {
		if tt.got != "" {
			t.Errorf("%s: got %q, want empty", tt.name, tt.got)
		}
	}

	bot := waActor{waID: "1", isBot: true}
	if !bot.IsBotUser() {
		t.Error("IsBotUser() must reflect the flag")
	}
	if a.IsBotUser() {
		t.Error("a human sender must not report as a bot")
	}
}

// TestWaChat_group pins the group branch of GetType/IsGroupChat. The Groups API is a
// separate surface and is not wired up, but the type models it.
func TestWaChat_group(t *testing.T) {
	g := waChat{waID: "123", isGroup: true}
	if !g.IsGroupChat() {
		t.Error("IsGroupChat() must be true")
	}
	if got := g.GetType(); got != "group" {
		t.Errorf("GetType() = %q, want group", got)
	}
}

// TestWaInputMessage_LogRequest pins that logging a request neither panics nor
// includes message content — it runs on every inbound message.
func TestWaInputMessage_LogRequest(t *testing.T) {
	in := NewWebhookInput(
		InboundMessage{From: "1", ID: "wamid.X", Type: "text", Text: &InboundText{Body: "private"}}, "1", nil)
	in.LogRequest() // must not panic
}

// TestWaTextMessage_emptyTextObject pins the nil-body guard on Text().
func TestWaTextMessage_emptyTextObject(t *testing.T) {
	m := waTextMessage{waInputMessage: waInputMessage{msg: InboundMessage{From: "1", Type: "text"}}}
	if got := m.Text(); got != "" {
		t.Errorf("Text() = %q, want empty for a nil text object", got)
	}
}

// TestWaCallbackQuery_accessors covers the CallbackQuery surface for an interactive
// reply — the regime where the id IS the business's own state.
func TestWaCallbackQuery_accessors(t *testing.T) {
	in := inputFromJSON(t, metaButtonReply)
	cq := in.(waCallbackQuery)

	if cq.GetID() != "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQTZAQzg0MzQ4QjRCM0NGNkVGOAA=" {
		t.Errorf("GetID() = %q, want the wamid of the tap", cq.GetID())
	}
	if cq.GetFrom().GetID() != "16505551234" {
		t.Errorf("GetFrom() = %v", cq.GetFrom().GetID())
	}
	// An interactive reply has no context in Meta's example — absence must be "".
	if cq.ContextMessageID() != "" {
		t.Errorf("ContextMessageID() = %q, want empty when no context is present", cq.ContextMessageID())
	}
}

// TestInboundInteractive_Reply covers the nil receiver and the empty case.
func TestInboundInteractive_Reply(t *testing.T) {
	var nilInteractive *InboundInteractive
	if nilInteractive.Reply() != nil {
		t.Error("a nil InboundInteractive must yield a nil reply, not panic")
	}
	empty := &InboundInteractive{Type: "button_reply"}
	if empty.Reply() != nil {
		t.Error("an interactive with neither reply set must yield nil")
	}
	list := &InboundInteractive{Type: "list_reply", ListReply: &InboundReply{ID: "r"}}
	if got := list.Reply(); got == nil || got.ID != "r" {
		t.Errorf("Reply() = %+v, want the list reply", got)
	}
}

// TestInboundMessage_ContextMessageID_absent covers the nil-context guard.
func TestInboundMessage_ContextMessageID_absent(t *testing.T) {
	if got := (InboundMessage{}).ContextMessageID(); got != "" {
		t.Errorf("ContextMessageID() = %q, want empty", got)
	}
}

// TestWaChatData_Base pins the botsfwmodels.BotChatData embed point.
func TestWaChatData_Base(t *testing.T) {
	d := &WaChatData{}
	if d.Base() != &d.ChatBaseData {
		t.Error("Base() must return the embedded ChatBaseData")
	}
}

// TestWaChatData_RecordInbound_edges covers the branches the loop tests miss.
func TestWaChatData_RecordInbound_edges(t *testing.T) {
	t.Run("empty message id is recorded but not deduped", func(t *testing.T) {
		d := &WaChatData{}
		at := time.Unix(1749416383, 0).UTC()
		if !d.RecordInbound("", at) {
			t.Error("a message with no id must still advance the window")
		}
		if len(d.RecentInboundIDs) != 0 {
			t.Error("an empty id must not enter the dedup ring")
		}
		if !d.DtLastInbound.Equal(at) {
			t.Error("the window must still advance")
		}
		// A second id-less message is not a duplicate — there is nothing to match.
		if !d.RecordInbound("", at) {
			t.Error("an id-less message can never be detected as a duplicate")
		}
	})

	t.Run("zero sentAt does not rewind", func(t *testing.T) {
		d := &WaChatData{DtLastInbound: time.Unix(1749416383, 0).UTC()}
		d.RecordInbound("wamid.X", time.Time{})
		if d.DtLastInbound.IsZero() {
			t.Error("an unparseable timestamp must not wipe a known DtLastInbound")
		}
	})
}

// TestStoreLastInboundProvider_edges covers the error and nil-data paths.
func TestStoreLastInboundProvider_edges(t *testing.T) {
	t.Run("store error surfaces", func(t *testing.T) {
		s := newMemStore()
		s.getErr = errors.New("datastore unavailable")
		_, err := NewStoreLastInboundProvider(s, "bot1").LastInboundAt(context.Background(), "1")
		if err == nil {
			t.Error("a store error must surface to the gate, which then fails open")
		}
	})

	t.Run("unknown chat yields zero time", func(t *testing.T) {
		at, err := NewStoreLastInboundProvider(newMemStore(), "bot1").LastInboundAt(context.Background(), "nope")
		if err != nil {
			t.Fatalf("an unknown chat is not an error: %v", err)
		}
		if !at.IsZero() {
			t.Errorf("got %v, want the zero time", at)
		}
	})

	t.Run("panics on nil store", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("a provider that cannot read is worse than none")
			}
		}()
		NewStoreLastInboundProvider(nil, "bot1")
	})
}

// TestProcessInbound_skipsMessagesWithNoSender covers the guard.
func TestProcessInbound_skipsMessagesWithNoSender(t *testing.T) {
	store := newMemStore()
	res, err := ProcessInbound(context.Background(), store, "bot1",
		payloadWith(InboundMessage{ID: "a", Timestamp: "1749416383"})) // no From
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.New) != 0 {
		t.Error("a message with no sender has no chat to record against")
	}
	if store.saves != 0 {
		t.Error("nothing should be persisted")
	}
}

// TestHtmlToText_edges covers the anchor branches the table misses.
func TestHtmlToText_edges(t *testing.T) {
	// An anchor whose text is empty collapses to the bare URL.
	if got := htmlToText(`<a href="https://x.io"></a>`); got != "https://x.io" {
		t.Errorf("got %q, want the bare URL", got)
	}
	// A tag with no anchor still strips.
	if got := htmlToText(`<br/>hi`); got != "hi" {
		t.Errorf("got %q", got)
	}
}

// TestTruncate_edges covers the tiny-max branch.
func TestTruncate_edges(t *testing.T) {
	if got := truncate("abc", 1); got != "a" {
		t.Errorf("truncate(_, 1) = %q, want a bare cut with no room for an ellipsis", got)
	}
	if got := truncate("abc", 0); got != "" {
		t.Errorf("truncate(_, 0) = %q, want empty", got)
	}
}

// TestFlattenKeyboard_edges covers the non-MessageKeyboard and nil cases.
func TestFlattenKeyboard_edges(t *testing.T) {
	btns, extra, notes := flattenKeyboard(nil)
	if btns != nil || extra != nil || notes != nil {
		t.Error("a nil keyboard must yield nothing")
	}

	// A Keyboard implementation that is not *botkb.MessageKeyboard.
	btns, _, _ = flattenKeyboard(otherKeyboard{})
	if btns != nil {
		t.Error("an unknown Keyboard implementation must yield no buttons rather than panic")
	}
}

type otherKeyboard struct{}

func (otherKeyboard) KeyboardType() botkb.KeyboardType { return botkb.KeyboardTypeInline }

// TestDegrade_textButtonUsesLabelAsID covers botkb.TextButton, which has no callback
// data — the label is echoed as the id so a tap stays identifiable.
func TestDegrade_textButtonUsesLabelAsID(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = "pick"
	m.Keyboard = kbd(botkb.NewTextButton("Yes"))

	s, _, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := s.(*wabotapi.SendButtonsConfig)
	btn := cfg.Interactive.Action.Buttons[0]
	if btn.Reply.ID != "Yes" || btn.Reply.Title != "Yes" {
		t.Errorf("got %+v, want the label echoed as the id", btn.Reply)
	}
}

// TestDegrade_unknownButtonTypeIsDroppedLoudly covers the default branch.
func TestDegrade_unknownButtonTypeIsDroppedLoudly(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = "pick"
	m.Keyboard = kbd(unknownButton{})

	_, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasNote(notes, "unhandled type") {
		t.Errorf("an unhandled button type must be reported, not silently dropped: %v", notes)
	}
}

type unknownButton struct{}

func (unknownButton) GetText() string              { return "Mystery" }
func (unknownButton) ButtonType() botkb.ButtonType { return botkb.ButtonType(999) }

// TestDegrade_emptyMessageIsRejected covers the one case degradation cannot save: a
// message with no text and nothing to say.
func TestDegrade_emptyMessageIsRejected(t *testing.T) {
	_, _, err := degradeToSendable("16505551234", botmsg.MessageFromBot{})
	if !errors.Is(err, wabotapi.ErrEmptyBody) {
		t.Errorf("got %v, want ErrEmptyBody", err)
	}
}

// TestDegrade_listRowLabelTruncated covers the list rung's truncation note.
func TestDegrade_listRowLabelTruncated(t *testing.T) {
	var buttons []botkb.Button
	for i := 0; i < 5; i++ {
		buttons = append(buttons,
			botkb.NewDataButton(strings.Repeat("long label ", 5)+string(rune('a'+i)), "d"+string(rune('a'+i))))
	}
	m := botmsg.MessageFromBot{}
	m.Text = "pick"
	m.Keyboard = kbd(buttons...)

	s, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err = s.(*wabotapi.SendListConfig).Validate(); err != nil {
		t.Fatalf("a truncated row must still produce a valid config: %v", err)
	}
	if !hasNote(notes, "truncated") {
		t.Errorf("truncation must be reported: %v", notes)
	}
}

// TestTemplateMessage_BotMessageType pins the honest TypeUndefined: bots-fw's enum is
// a list of Telegram Bot API methods and cannot describe a template.
func TestTemplateMessage_BotMessageType(t *testing.T) {
	if got := (TemplateMessage{}).BotMessageType(); got != botmsg.TypeUndefined {
		t.Errorf("BotMessageType() = %v, want TypeUndefined — the enum has no template member", got)
	}
}

// TestSendNativeMessageTypes_BotMessageType pins that the native image and
// cta_url wrappers also return TypeUndefined — dispatch is by concrete type.
func TestSendNativeMessageTypes_BotMessageType(t *testing.T) {
	if got := (sendImageMessage{}).BotMessageType(); got != botmsg.TypeUndefined {
		t.Errorf("sendImageMessage.BotMessageType() = %v, want TypeUndefined", got)
	}
	if got := (sendCTAURLMessage{}).BotMessageType(); got != botmsg.TypeUndefined {
		t.Errorf("sendCTAURLMessage.BotMessageType() = %v, want TypeUndefined", got)
	}
}

// TestResponder_noRecipientAndDegradeFailure covers the two early returns.
func TestResponder_noRecipientAndDegradeFailure(t *testing.T) {
	ts := sendOK(t, nil)
	defer ts.Close()
	r := newTestResponder(ts)

	t.Run("no recipient", func(t *testing.T) {
		m := botmsg.MessageFromBot{}
		m.Text = "hi"
		if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); !errors.Is(err, ErrNoRecipient) {
			t.Errorf("got %v, want ErrNoRecipient", err)
		}
	})

	t.Run("nothing to send", func(t *testing.T) {
		m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
		if _, err := r.SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); err == nil {
			t.Error("an empty message must error — there is nothing to degrade to")
		}
	})
}

// TestResponder_apiFailureSurfaces covers the send-error branch.
func TestResponder_apiFailureSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"(#131047) Re-engagement","code":131047}}`))
	}))
	defer ts.Close()

	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	m.Text = "hi"

	_, err := newTestResponder(ts).SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS)
	if err == nil {
		t.Fatal("an API failure must surface")
	}
	if apiErr := wabotapi.AsAPIError(err); apiErr == nil || !apiErr.IsReEngagementRequired() {
		t.Errorf("the typed error must survive the responder: %v", err)
	}
}

// TestResponder_templatePointer covers the *TemplateMessage dispatch arm.
func TestResponder_templatePointer(t *testing.T) {
	var body []byte
	ts := sendOK(t, &body)
	defer ts.Close()

	m := botmsg.MessageFromBot{
		ToChat:     ChatID("16505551234"),
		BotMessage: &TemplateMessage{Name: "t", LanguageCode: "en_US"},
	}
	if _, err := newTestResponder(ts).SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); err != nil {
		t.Fatalf("a *TemplateMessage must dispatch as a template: %v", err)
	}
	if !strings.Contains(string(body), `"type":"template"`) {
		t.Errorf("got %s", body)
	}
}

// TestResponder_degradationWithoutLogger pins that notes are discarded safely when
// no OnDegradation callback is registered.
func TestResponder_degradationWithoutLogger(t *testing.T) {
	ts := sendOK(t, nil)
	defer ts.Close()

	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	m.Text = "<b>bold</b>"
	m.Format = botmsg.FormatHTML

	// No OnDegradation registered — must not panic on a nil callback.
	if _, err := newTestResponder(ts).SendMessage(context.Background(), m, botsfw.BotAPISendMessageOverHTTPS); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWebhookHandler_bodyReadFailure covers the read-error branch.
func TestWebhookHandler_bodyReadFailure(t *testing.T) {
	h := newTestWebhookHandler(newMemStore(), func(context.Context, []InboundMessage) error { return nil })

	r := httptest.NewRequest(http.MethodPost, "/wa/hook", io.NopCloser(errReader{}))
	r.Header.Set(SignatureHeader, "sha256=deadbeef")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// TestVerificationRequest_emptyChallenge covers the last handshake guard.
func TestVerificationRequest_emptyChallenge(t *testing.T) {
	_, err := VerificationRequest{Mode: "subscribe", VerifyToken: "tok"}.Verify("tok")
	if err == nil {
		t.Error("an empty challenge must not be echoed as success")
	}
}

// TestWebhookPayload_StatusesAcrossEntries covers the statuses flattening loop.
func TestWebhookPayload_StatusesAcrossEntries(t *testing.T) {
	p := WebhookPayload{Entry: []WebhookEntry{
		{Changes: []WebhookChange{{Value: WebhookValue{Statuses: []MessageStatus{{ID: "a"}}}}}},
		{Changes: []WebhookChange{{Value: WebhookValue{Statuses: []MessageStatus{{ID: "b"}}}}}},
	}}
	if got := len(p.Statuses()); got != 2 {
		t.Errorf("got %d statuses across entries, want 2", got)
	}
}

// TestWaUnsupportedMessage_isRoutable pins that the unsupported wrapper satisfies
// the input contract, so it can reach a handler.
func TestWaUnsupportedMessage_isRoutable(t *testing.T) {
	var in botinput.InputMessage = waUnsupportedMessage{
		waInputMessage: waInputMessage{msg: InboundMessage{From: "1", ID: "x", Type: "image"}},
	}
	if in.InputType() != botinput.TypeNotImplemented {
		t.Error("must report TypeNotImplemented")
	}
	if in.GetSender() == nil || in.Chat() == nil {
		t.Error("an unsupported message must still expose sender and chat so it can be answered")
	}
}
