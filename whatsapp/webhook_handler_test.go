package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-fw/botmsg"
)

func newTestWebhookHandler(store ChatDataStore, onMessages MessagesHandler) *WebhookHandler {
	return NewWebhookHandler("bot1", "meatyhamhock", testAppSecret, store, onMessages)
}

// postSigned builds a correctly signed POST, as Meta would send.
func postSigned(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/wa/hook", strings.NewReader(body))
	r.Header.Set(SignatureHeader, signFor([]byte(body), testAppSecret))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestWebhookHandler_verificationHandshake(t *testing.T) {
	h := newTestWebhookHandler(newMemStore(), func(context.Context, []InboundMessage) error { return nil })

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/wa/hook?hub.mode=subscribe&hub.challenge=1158201444&hub.verify_token=meatyhamhock", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "1158201444" {
		t.Errorf("body = %q, want the bare challenge", w.Body.String())
	}
}

func TestWebhookHandler_verificationWrongTokenIsForbidden(t *testing.T) {
	h := newTestWebhookHandler(newMemStore(), func(context.Context, []InboundMessage) error { return nil })

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/wa/hook?hub.mode=subscribe&hub.challenge=1158201444&hub.verify_token=wrong", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("a rejected handshake must echo nothing, got %q", w.Body.String())
	}
}

// TestWebhookHandler_deliversNewMessage pins the happy path end to end: signed
// payload -> window advanced -> message handed to the app -> 200.
func TestWebhookHandler_deliversNewMessage(t *testing.T) {
	store := newMemStore()
	var got []InboundMessage
	h := newTestWebhookHandler(store, func(_ context.Context, msgs []InboundMessage) error {
		got = msgs
		return nil
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, postSigned(metaInboundTextWebhook))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Meta requires 200 on every notification)", w.Code)
	}
	if len(got) != 1 || got[0].Text == nil || got[0].Text.Body != "Does it come in another color?" {
		t.Fatalf("handler got %+v, want the inbound text", got)
	}
	if store.data["bot1/16505551234"].DtLastInbound.IsZero() {
		t.Error("the customer service window must have advanced")
	}
}

// TestWebhookHandler_rejectsUnsignedAndForged is the security boundary. An
// unauthenticated payload must never reach the store or the app.
func TestWebhookHandler_rejectsUnsignedAndForged(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"no signature", func(r *http.Request) { r.Header.Del(SignatureHeader) }},
		{"wrong secret", func(r *http.Request) {
			r.Header.Set(SignatureHeader, signFor([]byte(metaInboundTextWebhook), "attacker"))
		}},
		{"signature for different body", func(r *http.Request) {
			r.Header.Set(SignatureHeader, signFor([]byte(`{"object":"other"}`), testAppSecret))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemStore()
			called := false
			h := newTestWebhookHandler(store, func(context.Context, []InboundMessage) error {
				called = true
				return nil
			})

			r := postSigned(metaInboundTextWebhook)
			tt.mutate(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", w.Code)
			}
			if called {
				t.Error("an unauthenticated payload must never reach the app")
			}
			if store.saves != 0 {
				t.Error("an unauthenticated payload must never touch chat data")
			}
		})
	}
}

// TestWebhookHandler_duplicateIsAcknowledgedNotReprocessed pins Meta's retry
// contract: a redelivery must return 200 (so Meta stops) without the app seeing it
// twice.
func TestWebhookHandler_duplicateIsAcknowledgedNotReprocessed(t *testing.T) {
	store := newMemStore()
	var calls int
	h := newTestWebhookHandler(store, func(context.Context, []InboundMessage) error {
		calls++
		return nil
	})

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, postSigned(metaInboundTextWebhook))
		if w.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i, w.Code)
		}
	}
	if calls != 1 {
		t.Errorf("app saw the message %d times, want exactly 1 across 3 deliveries", calls)
	}
}

// TestWebhookHandler_statusDoesNotReachMessagesHandler pins that a status webhook
// is acknowledged but never mistaken for user activity.
func TestWebhookHandler_statusDoesNotReachMessagesHandler(t *testing.T) {
	store := newMemStore()
	called := false
	var statuses []MessageStatus

	h := newTestWebhookHandler(store, func(context.Context, []InboundMessage) error {
		called = true
		return nil
	}).OnStatuses(func(_ context.Context, s []MessageStatus) { statuses = s })

	w := httptest.NewRecorder()
	h.ServeHTTP(w, postSigned(metaStatusWebhook))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if called {
		t.Error("a status must never reach the messages handler")
	}
	if len(statuses) != 1 || statuses[0].Status != "delivered" {
		t.Errorf("statuses = %+v, want the delivered status", statuses)
	}
	if store.saves != 0 {
		t.Error("a status must not advance the window")
	}
	// pricing.billable is how a business learns a template cost money.
	if statuses[0].Pricing == nil || !statuses[0].Pricing.Billable {
		t.Errorf("pricing must be surfaced, got %+v", statuses[0].Pricing)
	}
}

// TestWebhookHandler_appErrorReturns500 pins that a failing app makes Meta retry
// rather than silently dropping the message. Dedup makes the retry safe.
func TestWebhookHandler_appErrorReturns500(t *testing.T) {
	h := newTestWebhookHandler(newMemStore(), func(context.Context, []InboundMessage) error {
		return errors.New("downstream exploded")
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, postSigned(metaInboundTextWebhook))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 so Meta retries", w.Code)
	}
}

// TestWebhookHandler_storeFailureReturns500 pins the same for persistence.
func TestWebhookHandler_storeFailureReturns500(t *testing.T) {
	store := newMemStore()
	store.saveErr = errors.New("datastore unavailable")
	h := newTestWebhookHandler(store, func(context.Context, []InboundMessage) error { return nil })

	w := httptest.NewRecorder()
	h.ServeHTTP(w, postSigned(metaInboundTextWebhook))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// TestWebhookHandler_signedGarbageIs400 pins that a signed-but-unparseable body is
// NOT retried: it is our bug, and identical bytes would fail identically.
func TestWebhookHandler_signedGarbageIs400(t *testing.T) {
	h := newTestWebhookHandler(newMemStore(), func(context.Context, []InboundMessage) error { return nil })

	w := httptest.NewRecorder()
	h.ServeHTTP(w, postSigned(`{"object": not json`))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (retrying identical bytes would fail identically)", w.Code)
	}
}

func TestWebhookHandler_methodNotAllowed(t *testing.T) {
	h := newTestWebhookHandler(newMemStore(), func(context.Context, []InboundMessage) error { return nil })
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/wa/hook", nil))

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow != "GET, POST" {
		t.Errorf("Allow = %q", allow)
	}
}

// TestWebhookHandler_closesTheLoop is the whole point of the receive path: a
// message arriving through the handler must open the window for the responder.
func TestWebhookHandler_closesTheLoop(t *testing.T) {
	store := newMemStore()
	h := newTestWebhookHandler(store, func(context.Context, []InboundMessage) error { return nil })
	gate := NewWindowGate(h.LastInboundProvider())
	ctx := context.Background()

	m := messageTo("16505551234", "a proactive reminder")

	// Before the user writes, only a template may be sent.
	if err := gate.CanSend(ctx, m); err == nil {
		t.Fatal("the window must be shut before the user has ever written")
	}

	// The user writes; the handler records it.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, postSigned(inboundNow("16505551234", "wamid.LOOP")))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// The window is now open for a free-form reply.
	if err := gate.CanSend(ctx, m); err != nil {
		t.Errorf("the window must be open right after the user wrote, got: %v", err)
	}
}

func TestNewWebhookHandler_panicsOnMissingWiring(t *testing.T) {
	store := newMemStore()
	ok := func(context.Context, []InboundMessage) error { return nil }

	for _, tt := range []struct {
		name string
		f    func()
	}{
		{"no botID", func() { NewWebhookHandler("", "tok", "sec", store, ok) }},
		{"no verifyToken", func() { NewWebhookHandler("b", "", "sec", store, ok) }},
		{"no appSecret", func() { NewWebhookHandler("b", "tok", "", store, ok) }},
		{"no store", func() { NewWebhookHandler("b", "tok", "sec", nil, ok) }},
		{"no messages handler", func() { NewWebhookHandler("b", "tok", "sec", store, nil) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Error("expected a panic: a misconfigured webhook endpoint must not start")
				}
			}()
			tt.f()
		})
	}
}

// messageTo builds a plain free-form message for the gate tests.
func messageTo(to, text string) botmsg.MessageFromBot {
	m := botmsg.MessageFromBot{ToChat: ChatID(to)}
	m.Text = text
	return m
}

// inboundNow renders an inbound text webhook stamped at the current second, so the
// window it opens is genuinely current rather than a fixture from 2025.
func inboundNow(from, id string) string {
	return fmt.Sprintf(`{
	  "object": "whatsapp_business_account",
	  "entry": [{"id":"1","changes":[{"field":"messages","value":{
	    "messaging_product":"whatsapp",
	    "metadata":{"display_phone_number":"1","phone_number_id":"1234567890"},
	    "messages":[{"from":%q,"id":%q,"timestamp":"%d","type":"text","text":{"body":"hi"}}]
	  }}]}]
	}`, from, id, time.Now().Unix())
}
