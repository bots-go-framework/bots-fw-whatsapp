package whatsapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/strongo/logus"
)

// MaxWebhookBodyBytes caps how much of a webhook body is read.
//
// Meta batches up to 1000 updates, so bodies are large but bounded. The cap exists
// so a malformed or hostile request cannot exhaust memory before the signature is
// even checked — the body must be buffered whole to verify the HMAC, so it cannot
// be streamed.
const MaxWebhookBodyBytes = 8 << 20 // 8 MiB

// MessagesHandler processes the inbound user messages in one webhook delivery.
//
// Only messages not seen before are passed; duplicates are already filtered.
// Returning an error makes the endpoint respond 500 so Meta retries the delivery,
// which deduplication makes safe.
type MessagesHandler func(ctx context.Context, messages []InboundMessage) error

// StatusesHandler observes delivery statuses of the business's own messages.
//
// Worth wiring up: statuses carry pricing.billable, which is how a business learns
// a template actually cost money — and outside the 24h window every send is a
// template. Errors are not propagated; a status is informational and must not cause
// Meta to redeliver the whole batch.
type StatusesHandler func(ctx context.Context, statuses []MessageStatus)

// WebhookHandler serves the WhatsApp Cloud API webhook endpoint.
//
// It owns the security boundary and the customer-service-window bookkeeping, then
// hands genuinely new messages to the application.
type WebhookHandler struct {
	verifyToken string
	appSecret   string
	botID       string
	store       ChatDataStore

	onMessages MessagesHandler
	onStatuses StatusesHandler
}

var _ http.Handler = (*WebhookHandler)(nil)

// NewWebhookHandler returns a handler for one bot's webhook endpoint.
//
// verifyToken is the string configured in the App Dashboard, echoed back during the
// subscription handshake. appSecret signs every event notification.
//
// Panics on missing wiring: each of these is unrecoverable, and a webhook endpoint
// that silently accepts unsigned payloads is worse than one that fails to start.
func NewWebhookHandler(
	botID, verifyToken, appSecret string,
	store ChatDataStore,
	onMessages MessagesHandler,
) *WebhookHandler {
	if botID == "" {
		panic("botID must not be empty")
	}
	if verifyToken == "" {
		panic("verifyToken must not be empty")
	}
	if appSecret == "" {
		panic("appSecret must not be empty: without it payloads cannot be authenticated")
	}
	if store == nil {
		panic("store must not be nil: the customer service window cannot be tracked without it")
	}
	if onMessages == nil {
		panic("onMessages must not be nil")
	}
	return &WebhookHandler{
		botID:       botID,
		verifyToken: verifyToken,
		appSecret:   appSecret,
		store:       store,
		onMessages:  onMessages,
	}
}

// OnStatuses registers an optional handler for delivery statuses.
func (h *WebhookHandler) OnStatuses(f StatusesHandler) *WebhookHandler {
	h.onStatuses = f
	return h
}

// LastInboundProvider returns a provider backed by this handler's store, for
// wiring into a Responder so the window gate sees what the handler records.
func (h *WebhookHandler) LastInboundProvider() LastInboundProvider {
	return NewStoreLastInboundProvider(h.store, h.botID)
}

// ServeHTTP implements http.Handler.
//
// GET is the subscription handshake. POST is an event notification.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if err := HandleVerification(w, r, h.verifyToken); err != nil {
			logus.Warningf(r.Context(), "wa webhook verification refused: %v", err)
		}
	case http.MethodPost:
		h.handleEvent(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleEvent authenticates and processes one event notification.
func (h *WebhookHandler) handleEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// The body must be buffered whole: the signature covers the exact bytes, so it
	// cannot be verified while streaming, and it must be verified before decoding.
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxWebhookBodyBytes))
	if err != nil {
		logus.Errorf(ctx, "wa webhook: failed to read body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err = ValidateSignature(r.Header.Get(SignatureHeader), body, h.appSecret); err != nil {
		// 403 and no detail: an attacker probing signatures learns nothing.
		logus.Warningf(ctx, "wa webhook: rejected unauthenticated payload: %v", err)
		w.WriteHeader(http.StatusForbidden)
		return
	}

	var payload WebhookPayload
	if err = json.Unmarshal(body, &payload); err != nil {
		// A signed but unparseable body is our bug, not an attack. 400 rather than
		// 500: retrying identical bytes would fail identically.
		logus.Errorf(ctx, "wa webhook: signed payload failed to parse: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	result, err := ProcessInbound(ctx, h.store, h.botID, payload)
	if err != nil {
		// 500 so Meta retries. Safe because dedup filters the replay, and necessary
		// because an un-advanced window would refuse later valid sends.
		logus.Errorf(ctx, "wa webhook: failed to record inbound: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if n := len(result.Duplicates); n > 0 {
		logus.Debugf(ctx, "wa webhook: skipped %d duplicate message(s)", n)
	}
	if len(result.Statuses) > 0 && h.onStatuses != nil {
		h.onStatuses(ctx, result.Statuses)
	}

	if len(result.New) > 0 {
		if err = h.onMessages(ctx, result.New); err != nil {
			logus.Errorf(ctx, "wa webhook: handler failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	// Meta: "Your endpoint should respond to all Event Notifications with 200 OK."
	w.WriteHeader(http.StatusOK)
}
