package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botsfw"
)

// Fixed clock: the window is time-dependent, and a test that sleeps is a test
// nobody runs.
var testNow = time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

func newTestGate(lastInbound time.Time, err error) windowGate {
	return windowGate{
		lastInbound: LastInboundFunc(func(context.Context, string) (time.Time, error) {
			return lastInbound, err
		}),
		now: func() time.Time { return testNow },
	}
}

func TestIsWithinWindow(t *testing.T) {
	for _, tt := range []struct {
		name        string
		lastInbound time.Time
		want        bool
	}{
		{"just replied", testNow, true},
		{"an hour ago", testNow.Add(-time.Hour), true},
		{"23h59m ago", testNow.Add(-23*time.Hour - 59*time.Minute), true},
		{"exactly 24h ago is the last permitted instant", testNow.Add(-24 * time.Hour), true},
		{"a second past 24h is closed", testNow.Add(-24*time.Hour - time.Second), false},
		{"a week ago", testNow.Add(-7 * 24 * time.Hour), false},
		{"never replied", time.Time{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWithinWindow(tt.lastInbound, testNow); got != tt.want {
				t.Errorf("IsWithinWindow() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCanSend_freeFormInsideWindow pins the ordinary case: the user just wrote,
// so a normal reply is fine.
func TestCanSend_freeFormInsideWindow(t *testing.T) {
	g := newTestGate(testNow.Add(-time.Hour), nil)
	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	if err := g.CanSend(context.Background(), m); err != nil {
		t.Errorf("expected a permitted send inside the window, got: %v", err)
	}
}

// TestCanSend_freeFormOutsideWindowIsRefused is the whole point of the adapter's
// gate: a proactive free-form send must be stopped BEFORE it costs an API call.
func TestCanSend_freeFormOutsideWindowIsRefused(t *testing.T) {
	g := newTestGate(testNow.Add(-48*time.Hour), nil)
	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}

	err := g.CanSend(context.Background(), m)
	if err == nil {
		t.Fatal("expected a refusal 48h after the last reply")
	}
	if !botsfw.IsSendNotPermitted(err) {
		t.Errorf("refusal must be classifiable by the framework, got: %v", err)
	}
	// The operator needs to know why, and for how long.
	if !contains(err.Error(), "template") {
		t.Errorf("the refusal should name the remedy (a template), got: %v", err)
	}
}

// TestCanSend_templateBypassesWindow pins the escape hatch. Without this, the
// gate would block the very mechanism that exists to reach users outside the window.
func TestCanSend_templateBypassesWindow(t *testing.T) {
	g := newTestGate(testNow.Add(-30*24*time.Hour), nil) // a month of silence
	m := botmsg.MessageFromBot{
		ToChat: ChatID("16505551234"),
		BotMessage: TemplateMessage{
			Name:         "payment_reminder",
			LanguageCode: "en_US",
			BodyParams:   []string{"Jessica", "$40"},
		},
	}
	if err := g.CanSend(context.Background(), m); err != nil {
		t.Errorf("a template must be sendable outside the window, got: %v", err)
	}
}

// TestCanSend_neverRepliedIsRefused pins that a user who has never written can
// only be reached by template — the cold-start case for any proactive bot.
func TestCanSend_neverRepliedIsRefused(t *testing.T) {
	g := newTestGate(time.Time{}, nil)
	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}

	err := g.CanSend(context.Background(), m)
	if err == nil {
		t.Fatal("expected a refusal when the user has never messaged the bot")
	}
	if !botsfw.IsSendNotPermitted(err) {
		t.Errorf("refusal must be classifiable, got: %v", err)
	}
}

// TestCanSend_lookupFailureFailsOpen pins the deliberate choice to fail OPEN.
//
// A store hiccup is our problem, not a platform refusal. Failing closed would
// silently drop real messages during an outage; failing open costs at worst a
// rejected API call that the error model already classifies.
func TestCanSend_lookupFailureFailsOpen(t *testing.T) {
	g := newTestGate(time.Time{}, errors.New("datastore unavailable"))
	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	if err := g.CanSend(context.Background(), m); err != nil {
		t.Errorf("a lookup failure must not masquerade as a platform refusal, got: %v", err)
	}
}

// TestCanSend_noRecipientDefersToAPI pins that a message with no ToChat is not
// second-guessed here — there is nothing to check it against.
func TestCanSend_noRecipientDefersToAPI(t *testing.T) {
	g := newTestGate(testNow.Add(-48*time.Hour), nil)
	if err := g.CanSend(context.Background(), botmsg.MessageFromBot{}); err != nil {
		t.Errorf("expected no local refusal without a recipient, got: %v", err)
	}
}

// TestWindowGate_satisfiesSendGate pins the contract against bots-fw. If the
// framework's interface drifts, this fails at compile time.
func TestWindowGate_satisfiesSendGate(t *testing.T) {
	var _ botsfw.SendGate = windowGate{}
}

// TestTemplateMessage_toConfig pins the conversion into the API client, including
// that named and positional formats stay mutually exclusive.
func TestTemplateMessage_toConfig(t *testing.T) {
	t.Run("positional", func(t *testing.T) {
		cfg := TemplateMessage{
			Name: "order_confirmation", LanguageCode: "en_US",
			BodyParams: []string{"Jessica", "SKBUP2-4CPIG9"},
		}.toConfig("16505551234")
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected a valid config, got: %v", err)
		}
		if cfg.Template.Name != "order_confirmation" {
			t.Errorf("template name = %q", cfg.Template.Name)
		}
	})

	t.Run("named", func(t *testing.T) {
		cfg := TemplateMessage{
			Name: "order_confirmation", LanguageCode: "en_US",
			NamedBodyParams: []wabotapi.NamedParam{{Name: "first_name", Value: "Jessica"}},
		}.toConfig("16505551234")
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected a valid config, got: %v", err)
		}
	})

	t.Run("named wins when both supplied, never mixed", func(t *testing.T) {
		cfg := TemplateMessage{
			Name: "order_confirmation", LanguageCode: "en_US",
			BodyParams:      []string{"positional"},
			NamedBodyParams: []wabotapi.NamedParam{{Name: "first_name", Value: "Jessica"}},
		}.toConfig("16505551234")
		// Must not produce a mixed-format component, which the API rejects with 132018.
		if err := cfg.Validate(); err != nil {
			t.Fatalf("must never emit a mixed-format component: %v", err)
		}
	})
}

func TestIsTemplate(t *testing.T) {
	if !isTemplate(botmsg.MessageFromBot{BotMessage: TemplateMessage{}}) {
		t.Error("a TemplateMessage value must be recognised")
	}
	if !isTemplate(botmsg.MessageFromBot{BotMessage: &TemplateMessage{}}) {
		t.Error("a *TemplateMessage pointer must be recognised")
	}
	if isTemplate(botmsg.MessageFromBot{}) {
		t.Error("a plain message must not be recognised as a template")
	}
}

func TestChatID_ChatUID(t *testing.T) {
	// A phone number must survive intact — the failure botmsg.ChatIntID would cause.
	if got := ChatID("16505551234").ChatUID(); got != "16505551234" {
		t.Errorf("ChatUID() = %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TestNewWindowGate_returnsUsableSendGate pins the exported constructor the README
// documents, and that it wires a real clock rather than a zero func.
func TestNewWindowGate_returnsUsableSendGate(t *testing.T) {
	g := NewWindowGate(LastInboundFunc(func(context.Context, string) (time.Time, error) {
		return time.Now().Add(-time.Hour), nil
	}))
	m := botmsg.MessageFromBot{ToChat: ChatID("16505551234")}
	if err := g.CanSend(context.Background(), m); err != nil {
		t.Errorf("expected a permitted send an hour after the last reply, got: %v", err)
	}
}

// TestNewWindowGate_panicsOnNilProvider pins the refusal to build a gate that
// cannot determine the window — it would look like protection while permitting
// every out-of-window send.
func TestNewWindowGate_panicsOnNilProvider(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected a panic on a nil LastInboundProvider")
		}
	}()
	NewWindowGate(nil)
}
