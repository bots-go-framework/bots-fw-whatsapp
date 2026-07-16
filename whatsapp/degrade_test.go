package whatsapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-go-core/botkb"
)

func TestHtmlToText(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		// The tags Debtus actually uses: <b> x43, <i> x7, <code> x5, plus <a href>.
		{"bold", "<b>Payment due</b>", "Payment due"},
		{"italic", "<i>soon</i>", "soon"},
		{"code", "<code>ABC123</code>", "ABC123"},
		{"mixed", "<b>Due:</b> <i>$40</i>", "Due: $40"},
		{"anchor keeps the link reachable", `<a href="https://x.io/pay">Pay now</a>`, "Pay now (https://x.io/pay)"},
		{"anchor with same text and url", `<a href="https://x.io">https://x.io</a>`, "https://x.io"},
		{"anchor with nested tags", `<a href="https://x.io"><b>Pay</b></a>`, "Pay (https://x.io)"},
		{"entities unescaped", "5 &lt; 10 &amp;&amp; 10 &gt; 5", "5 < 10 && 10 > 5"},
		{"plain text untouched", "just text", "just text"},
		{"empty", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := htmlToText(tt.in); got != tt.want {
				t.Errorf("htmlToText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 20); got != "short" {
		t.Errorf("got %q, want unchanged", got)
	}
	if got := truncate("abcdefghij", 5); got != "abcd…" {
		t.Errorf("got %q, want an ellipsis marking the cut", got)
	}
	// Runes, not bytes — a multi-byte label must not be cut mid-character.
	if got := truncate("日本語テキスト", 3); got != "日本…" {
		t.Errorf("got %q, want rune-safe truncation", got)
	}
}

func kbd(buttons ...botkb.Button) botkb.Keyboard {
	return botkb.NewMessageKeyboard(botkb.KeyboardTypeInline, buttons)
}

// TestDegrade_noButtonsIsPlainText pins the simplest rung.
func TestDegrade_noButtonsIsPlainText(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = "You owe $40"

	s, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := s.(*wabotapi.SendTextConfig); !ok {
		t.Errorf("got %T, want a text message", s)
	}
	if len(notes) != 0 {
		t.Errorf("a plain message should lose nothing, got %v", notes)
	}
}

// TestDegrade_upTo3ButtonsStayInteractive pins the richest rung: Debtus's callback
// data rides along in the button id, so routing is preserved exactly.
func TestDegrade_upTo3ButtonsStayInteractive(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = "You owe $40"
	m.Keyboard = kbd(
		botkb.NewDataButton("Pay now", "pay?id=42"),
		botkb.NewDataButton("Remind me", "snooze?id=42"),
	)

	s, _, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, ok := s.(*wabotapi.SendButtonsConfig)
	if !ok {
		t.Fatalf("got %T, want interactive reply buttons", s)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatalf("degradation must produce a valid config: %v", err)
	}

	btns := cfg.Interactive.Action.Buttons
	if len(btns) != 2 {
		t.Fatalf("got %d buttons, want 2", len(btns))
	}
	// The callback payload survives intact — the whole point.
	if btns[0].Reply.ID != "pay?id=42" || btns[0].Reply.Title != "Pay now" {
		t.Errorf("button 0 = %+v, want the callback data preserved", btns[0].Reply)
	}
}

// TestDegrade_4to10ButtonsBecomeAList pins the middle rung: still tappable, still
// routed by callback id, one extra tap to open.
func TestDegrade_4to10ButtonsBecomeAList(t *testing.T) {
	var buttons []botkb.Button
	for i := 0; i < 6; i++ {
		buttons = append(buttons, botkb.NewDataButton("Option "+string(rune('A'+i)), "opt?i="+string(rune('a'+i))))
	}
	m := botmsg.MessageFromBot{}
	m.Text = "Pick a debt"
	m.Keyboard = kbd(buttons...)

	s, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, ok := s.(*wabotapi.SendListConfig)
	if !ok {
		t.Fatalf("got %T, want a list message", s)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatalf("degradation must produce a valid config: %v", err)
	}
	if n := len(cfg.Interactive.Action.Sections[0].Rows); n != 6 {
		t.Errorf("got %d rows, want 6", n)
	}
	if cfg.Interactive.Action.Sections[0].Rows[0].ID != "opt?i=a" {
		t.Errorf("row id = %q, want the callback data preserved", cfg.Interactive.Action.Sections[0].Rows[0].ID)
	}
	if !hasNote(notes, "at most 3 inline buttons") {
		t.Errorf("the loss should be reported, got %v", notes)
	}
}

// TestDegrade_over10ButtonsBecomeNumberedText pins the last rung. Lossy — callback
// ids cannot ride along — but the message stays actionable rather than unsendable.
func TestDegrade_over10ButtonsBecomeNumberedText(t *testing.T) {
	var buttons []botkb.Button
	for i := 0; i < 12; i++ {
		buttons = append(buttons, botkb.NewDataButton("Option "+string(rune('A'+i)), "opt?i="+string(rune('a'+i))))
	}
	m := botmsg.MessageFromBot{}
	m.Text = "Pick a debt"
	m.Keyboard = kbd(buttons...)

	s, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, ok := s.(*wabotapi.SendTextConfig)
	if !ok {
		t.Fatalf("got %T, want a numbered text menu", s)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatalf("degradation must produce a valid config: %v", err)
	}
	for _, want := range []string{"1. Option A", "12. Option L", "Reply with a number"} {
		if !strings.Contains(cfg.Text.Body, want) {
			t.Errorf("body missing %q:\n%s", want, cfg.Text.Body)
		}
	}
	if !hasNote(notes, "numbered text menu") {
		t.Errorf("the loss should be reported, got %v", notes)
	}
}

// TestDegrade_exactlyAtBoundaries pins the rung edges.
func TestDegrade_exactlyAtBoundaries(t *testing.T) {
	mk := func(n int) botmsg.MessageFromBot {
		var buttons []botkb.Button
		for i := 0; i < n; i++ {
			buttons = append(buttons, botkb.NewDataButton("B"+string(rune('a'+i)), "d"+string(rune('a'+i))))
		}
		m := botmsg.MessageFromBot{}
		m.Text = "body"
		m.Keyboard = kbd(buttons...)
		return m
	}

	for _, tt := range []struct {
		n    int
		want string
	}{
		{3, "*wabotapi.SendButtonsConfig"}, // at the button cap
		{4, "*wabotapi.SendListConfig"},    // one over -> list
		{10, "*wabotapi.SendListConfig"},   // at the list cap
		{11, "*wabotapi.SendTextConfig"},   // one over -> numbered text
	} {
		s, _, err := degradeToSendable("16505551234", mk(tt.n))
		if err != nil {
			t.Fatalf("%d buttons: %v", tt.n, err)
		}
		if got := typeName(s); got != tt.want {
			t.Errorf("%d buttons: got %s, want %s", tt.n, got, tt.want)
		}
	}
}

// TestDegrade_htmlIsStrippedNotRejected pins the #7 Debtus collision degrading
// rather than failing: 88 FormatHTML sites keep working, minus the styling.
func TestDegrade_htmlIsStrippedNotRejected(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = `<b>Overdue:</b> <a href="https://x.io/pay">settle up</a>`
	m.Format = botmsg.FormatHTML

	s, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("HTML must degrade, not error: %v", err)
	}
	cfg := s.(*wabotapi.SendTextConfig)
	if strings.Contains(cfg.Text.Body, "<b>") {
		t.Errorf("literal tags must never reach a user: %q", cfg.Text.Body)
	}
	if !strings.Contains(cfg.Text.Body, "https://x.io/pay") {
		t.Errorf("the link must stay reachable: %q", cfg.Text.Body)
	}
	if !hasNote(notes, "formatting stripped") {
		t.Errorf("the loss should be reported, got %v", notes)
	}
}

// TestDegrade_urlButtonMovesIntoBody pins that a link button is not silently lost:
// WhatsApp reply buttons are type "reply" only.
func TestDegrade_urlButtonMovesIntoBody(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = "Invoice ready"
	m.Keyboard = kbd(botkb.NewUrlButton("View invoice", "https://x.io/i/42"))

	s, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, ok := s.(*wabotapi.SendTextConfig)
	if !ok {
		t.Fatalf("got %T, want text with the link inlined", s)
	}
	if !strings.Contains(cfg.Text.Body, "https://x.io/i/42") {
		t.Errorf("the URL must survive in the body: %q", cfg.Text.Body)
	}
	if !hasNote(notes, "cannot carry links") {
		t.Errorf("the loss should be reported, got %v", notes)
	}
}

// TestDegrade_switchInlineQueryIsDropped pins that a Telegram-only affordance is
// dropped loudly. Inline mode has no WhatsApp analogue at all.
func TestDegrade_switchInlineQueryIsDropped(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = "Share this"
	m.Keyboard = kbd(botkb.NewSwitchInlineQueryButton("Share", "q"))

	_, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasNote(notes, "no inline mode") {
		t.Errorf("dropping a button must be reported, got %v", notes)
	}
}

// TestDegrade_gridIsFlattened pins that Telegram's rows collapse. botkb models
// [][]Button; WhatsApp has no grid.
func TestDegrade_gridIsFlattened(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = "Pick"
	m.Keyboard = botkb.NewMessageKeyboard(botkb.KeyboardTypeInline,
		[]botkb.Button{botkb.NewDataButton("A", "a")},
		[]botkb.Button{botkb.NewDataButton("B", "b")},
	)

	s, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := s.(*wabotapi.SendButtonsConfig)
	if len(cfg.Interactive.Action.Buttons) != 2 {
		t.Errorf("both rows' buttons must survive the flattening, got %d", len(cfg.Interactive.Action.Buttons))
	}
	if !hasNote(notes, "flattened") {
		t.Errorf("the loss should be reported, got %v", notes)
	}
}

// TestDegrade_longLabelsTruncatedToValidConfig pins that over-long labels are cut
// to fit rather than earning a 400.
func TestDegrade_longLabelsTruncatedToValidConfig(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = "Pick"
	m.Keyboard = kbd(botkb.NewDataButton(strings.Repeat("Very long label ", 5), "d"))

	s, notes, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := s.(*wabotapi.SendButtonsConfig)
	if err = cfg.Validate(); err != nil {
		t.Fatalf("a truncated label must produce a valid config: %v", err)
	}
	if !hasNote(notes, "truncated") {
		t.Errorf("truncation should be reported, got %v", notes)
	}
}

// TestDegrade_duplicateLabelsDisambiguated pins that Meta's unique-title rule is
// satisfied by renaming rather than by failing the send.
func TestDegrade_duplicateLabelsDisambiguated(t *testing.T) {
	m := botmsg.MessageFromBot{}
	m.Text = "Pick"
	m.Keyboard = kbd(
		botkb.NewDataButton("Pay", "pay?id=1"),
		botkb.NewDataButton("Pay", "pay?id=2"),
	)

	s, _, err := degradeToSendable("16505551234", m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := s.(*wabotapi.SendButtonsConfig)
	if err = cfg.Validate(); err != nil {
		t.Fatalf("duplicate labels must be disambiguated, not rejected: %v", err)
	}
	// Both actions must remain distinguishable by their callback ids.
	btns := cfg.Interactive.Action.Buttons
	if btns[0].Reply.ID == btns[1].Reply.ID {
		t.Error("callback ids must stay distinct")
	}
}

func hasNote(notes []Degradation, substr string) bool {
	for _, n := range notes {
		if strings.Contains(string(n), substr) {
			return true
		}
	}
	return false
}

func typeName(v any) string {
	return fmt.Sprintf("%T", v)
}
