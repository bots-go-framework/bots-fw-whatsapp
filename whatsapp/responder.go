package whatsapp

import (
	"context"
	"errors"
	"net/http"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botsfw"
)

var (
	// ErrDeleteNotSupported means the Cloud API has no delete endpoint.
	//
	// botsfw.WebhookResponder requires DeleteMessage of every platform, so this is
	// a method that must exist and can only ever fail. Unlike the other gaps there
	// is nothing to degrade to: a delete either happens or it does not.
	ErrDeleteNotSupported = errors.New("whatsapp has no delete-message endpoint")

	// ErrNoRecipient means the message carried no resolvable chat.
	ErrNoRecipient = errors.New("cannot determine the recipient from MessageFromBot.ToChat")
)

// DegradationLogger is called with each loss incurred fitting a message to
// WhatsApp, so degradation is observable rather than silent.
//
// Optional: a nil logger discards the notes.
type DegradationLogger func(ctx context.Context, m botmsg.MessageFromBot, notes []Degradation)

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
//
// Messages are progressively degraded to fit WhatsApp, never rejected for being too
// rich. The app writes one message aimed at Telegram's full capability; fitting it
// to a weaker platform is this adapter's job, not the app's. Rejecting rich
// messages would push platform branches back into the app — or, worse, invite
// someone to write to the lowest common denominator and quietly degrade Telegram.
//
// See degradeToSendable for the ladder.
type Responder struct {
	client *wabotapi.Client
	gate   botsfw.SendGate
	onLoss DegradationLogger
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

// OnDegradation registers a callback invoked whenever a message loses something
// on the way to WhatsApp. Returns the Responder for chaining.
//
// Worth wiring up: these notes are the running record of where the WhatsApp
// experience diverges from Telegram's, and they are invisible otherwise.
func (r *Responder) OnDegradation(log DegradationLogger) *Responder {
	r.onLoss = log
	return r
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

	to := chatUID(m)
	if to == "" {
		return zero, ErrNoRecipient
	}

	sendable, notes, err := r.toSendable(to, m)
	if err != nil {
		return zero, err
	}
	if len(notes) > 0 && r.onLoss != nil {
		r.onLoss(ctx, m, notes)
	}

	resp, err := r.client.SendMessage(ctx, sendable)
	if err != nil {
		return zero, err
	}
	return botsfw.OnMessageSentResponse{
		StatusCode: http.StatusOK,
		Message:    sentMessage{id: resp.MessageID()},
	}, nil
}

// toSendable picks the WhatsApp representation for m.
//
// Dispatch is by concrete BotMessage type rather than by botmsg.Type, because that
// enum is a list of Telegram Bot API methods and has no template member.
func (r *Responder) toSendable(to string, m botmsg.MessageFromBot) (wabotapi.Sendable, []Degradation, error) {
	switch bm := m.BotMessage.(type) {
	case TemplateMessage:
		return bm.toConfig(to), nil, nil
	case *TemplateMessage:
		return bm.toConfig(to), nil, nil
	}

	sendable, notes, err := degradeToSendable(to, m)
	if err != nil {
		return nil, notes, err
	}

	// An edit is degraded, not refused: WhatsApp has no edit endpoint, so the
	// update arrives as a new message and the conversation becomes append-only.
	// This is the #1 Telegram idiom in Debtus ("tap a button, rewrite the message
	// in place"), and it is the single most visible difference for users.
	if m.IsEdit || m.EditMessageIntID != 0 || m.EditMessageUID != nil {
		notes = append(notes, Degradation(
			"edit sent as a new message: WhatsApp has no edit endpoint, so the original stays visible"))
	}
	return sendable, notes, nil
}

// DeleteMessage implements botsfw.WebhookResponder.
//
// Always fails: the Cloud API has no delete endpoint. The method exists only
// because botsfw.WebhookResponder requires it of every platform — an example of
// the framework mandating a Telegram capability as universal.
func (r *Responder) DeleteMessage(_ context.Context, _ string) error {
	return ErrDeleteNotSupported
}
