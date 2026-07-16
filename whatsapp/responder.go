package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botsfw"
)

// Unsupported-capability errors.
//
// Each of these is a bots-fw affordance with no WhatsApp counterpart. They fail
// loudly rather than silently degrading: a message that renders as literal "<b>"
// to a real user is worse than a send that refuses and says why.
var (
	// ErrEditNotSupported means the Cloud API has no edit endpoint.
	//
	// The Messages API reference enumerates 40+ operations and every one is a Send.
	// bots-fw's MessageFromBot.IsEdit therefore cannot be honoured — and it is the
	// dominant Telegram idiom ("tap a button, rewrite the message in place"), so
	// this is a redesign signal, not a TODO.
	ErrEditNotSupported = errors.New("whatsapp has no edit-message endpoint; send a new message instead")

	// ErrDeleteNotSupported means the Cloud API has no delete endpoint.
	//
	// botsfw.WebhookResponder requires DeleteMessage of every platform, so this is
	// a method that must exist and can only ever fail.
	ErrDeleteNotSupported = errors.New("whatsapp has no delete-message endpoint")

	// ErrFormatNotSupported means a rich text format was requested.
	//
	// Deliberately an error rather than a passthrough: WhatsApp's markup support is
	// UNVERIFIED (Meta's text-messages page documents a plain body with an optional
	// link preview and no markup syntax). Passing HTML through would render literal
	// tags to users. Resolve the formatting question before relaxing this.
	ErrFormatNotSupported = errors.New("whatsapp text formatting is unverified; only botmsg.FormatText is supported")

	// ErrKeyboardNotSupported means a botkb.Keyboard was attached.
	//
	// WhatsApp interactive messages exist (max 3 reply buttons, or a 10-row list),
	// but their inbound and outbound wire shapes are not yet modelled — see the
	// package roadmap. Guessing a wire format is what produced the Retry-After bug
	// in bots-api-whatsapp.
	ErrKeyboardNotSupported = errors.New("whatsapp keyboards are not implemented yet")

	// ErrNoRecipient means the message carried no resolvable chat.
	ErrNoRecipient = errors.New("cannot determine the recipient from MessageFromBot.ToChat")
)

// sentMessage adapts a Cloud API send result to botsfw.MessengerResponse.
type sentMessage struct {
	id string
}

var _ botsfw.MessengerResponse = sentMessage{}

// GetMessageID returns the wamid of the sent message.
func (s sentMessage) GetMessageID() string {
	return s.id
}

// Responder sends messages through the WhatsApp Cloud API.
//
// It implements both botsfw.WebhookResponder and botsfw.SendGate. The gate half is
// essential: bots-fw treats a responder that does not implement SendGate as always
// permitting, so a WhatsApp responder without it would attempt out-of-window sends
// and fail every one with 131047.
type Responder struct {
	client *wabotapi.Client
	gate   botsfw.SendGate
}

var (
	_ botsfw.WebhookResponder = (*Responder)(nil)
	_ botsfw.SendGate         = (*Responder)(nil)
)

// NewResponder returns a Responder that sends via client and enforces the
// customer service window using lastInbound.
//
// Panics on a nil client or provider: both are unrecoverable wiring errors, and a
// responder that cannot gate is worse than none.
func NewResponder(client *wabotapi.Client, lastInbound LastInboundProvider) *Responder {
	if client == nil {
		panic("client must not be nil")
	}
	return &Responder{
		client: client,
		gate:   NewWindowGate(lastInbound),
	}
}

// CanSend implements botsfw.SendGate by delegating to the window gate.
func (r *Responder) CanSend(ctx context.Context, m botmsg.MessageFromBot) error {
	return r.gate.CanSend(ctx, m)
}

// SendMessage implements botsfw.WebhookResponder.
//
// The channel argument is ignored. bots-fw's BotAPISendMessageOverResponse means
// "write the API call into the webhook's 200 body", which is a Telegram trick; the
// Cloud API requires a separate authenticated HTTPS request either way. Telegram's
// own responder already discards the channel too.
func (r *Responder) SendMessage(
	ctx context.Context,
	m botmsg.MessageFromBot,
	_ botmsg.BotAPISendMessageChannel,
) (botsfw.OnMessageSentResponse, error) {
	var zero botsfw.OnMessageSentResponse

	if err := r.rejectUnsupported(m); err != nil {
		return zero, err
	}

	to := chatUID(m)
	if to == "" {
		return zero, ErrNoRecipient
	}

	resp, err := r.send(ctx, to, m)
	if err != nil {
		return zero, err
	}

	return botsfw.OnMessageSentResponse{
		StatusCode: http.StatusOK,
		Message:    sentMessage{id: resp.MessageID()},
	}, nil
}

// send dispatches on the concrete BotMessage type.
//
// Dispatch is by type rather than by botmsg.Type because that enum is a list of
// Telegram Bot API methods and has no template member.
func (r *Responder) send(
	ctx context.Context,
	to string,
	m botmsg.MessageFromBot,
) (*wabotapi.SendMessageResponse, error) {
	switch bm := m.BotMessage.(type) {
	case TemplateMessage:
		return r.client.SendMessage(ctx, bm.toConfig(to))
	case *TemplateMessage:
		return r.client.SendMessage(ctx, bm.toConfig(to))
	}

	if m.Text == "" {
		return nil, fmt.Errorf("nothing to send: %w", wabotapi.ErrEmptyBody)
	}
	cfg := wabotapi.NewSendText(to, m.Text)
	if !m.DisableWebPagePreview {
		cfg = cfg.WithPreviewURL()
	}
	return r.client.SendMessage(ctx, cfg)
}

// rejectUnsupported fails fast on bots-fw affordances WhatsApp cannot honour.
//
// Templates are exempt from the format and keyboard checks: their content is
// defined by the approved template, not by these fields.
func (r *Responder) rejectUnsupported(m botmsg.MessageFromBot) error {
	if m.IsEdit || m.EditMessageIntID != 0 || m.EditMessageUID != nil {
		return ErrEditNotSupported
	}
	if isTemplate(m) {
		return nil
	}
	if m.Format != botmsg.FormatText {
		return fmt.Errorf("format %v requested: %w", m.Format, ErrFormatNotSupported)
	}
	if m.Keyboard != nil {
		return ErrKeyboardNotSupported
	}
	return nil
}

// DeleteMessage implements botsfw.WebhookResponder.
//
// Always fails: the Cloud API has no delete endpoint. The method exists only
// because botsfw.WebhookResponder requires it of every platform — an example of
// the framework mandating a Telegram capability as universal.
func (r *Responder) DeleteMessage(_ context.Context, _ string) error {
	return ErrDeleteNotSupported
}
