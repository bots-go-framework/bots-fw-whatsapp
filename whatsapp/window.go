package whatsapp

import (
	"context"
	"fmt"
	"time"

	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botsfw"
)

// CustomerServiceWindow is how long a business may send free-form messages after
// the recipient's last reply.
//
// Outside it, a free-form send fails with wabotapi.ErrCodeReEngagementRequired
// (131047) and only a pre-approved template may be delivered.
//
// https://developers.facebook.com/documentation/business-messaging/whatsapp/support/error-codes
const CustomerServiceWindow = 24 * time.Hour

// LastInboundProvider reports when a WhatsApp user last messaged the bot in a chat.
//
// This exists because bots-fw does not track it. ChatBaseData.DtLastInteraction
// looks like the right field but is not: botswebhook/router.go only stamps it when
// chatData.IsChanged() || chatData.HasChangedVars(), so a reply that mutates
// nothing leaves it stale. A window built on it would refuse sends that are in
// fact permitted — worse than no gate, because the failure is silent.
//
// The adapter's webhook handler is therefore responsible for recording an inbound
// timestamp on EVERY inbound user message, and supplying it back through this
// interface.
//
// Implementations must return the zero Time when nothing is known about a chat.
// A zero time is treated as "outside the window", which is the safe default: the
// worst case is sending a template that was not strictly required, rather than
// burning an API call on a send that cannot land.
type LastInboundProvider interface {
	// LastInboundAt returns when the user last sent a message in chatID.
	//
	// Returns the zero Time if the chat is unknown or the user has never written.
	LastInboundAt(ctx context.Context, chatID string) (time.Time, error)
}

// LastInboundFunc adapts a plain function to LastInboundProvider.
type LastInboundFunc func(ctx context.Context, chatID string) (time.Time, error)

// LastInboundAt implements LastInboundProvider.
func (f LastInboundFunc) LastInboundAt(ctx context.Context, chatID string) (time.Time, error) {
	return f(ctx, chatID)
}

// windowGate decides whether a free-form send is currently permitted.
//
// now is injectable so the window can be tested without sleeping.
type windowGate struct {
	lastInbound LastInboundProvider
	now         func() time.Time
}

// IsWithinWindow reports whether lastInbound is recent enough to permit a
// free-form send at now.
//
// The boundary is exclusive: exactly 24h after the last reply the window is
// closed. Meta says "more than 24 hours have passed", so 24h exactly is the last
// permitted instant; anything beyond is refused. Erring toward refusal at the
// boundary costs a template, whereas erring toward permission costs a failed send.
func IsWithinWindow(lastInbound, now time.Time) bool {
	if lastInbound.IsZero() {
		return false
	}
	return now.Sub(lastInbound) <= CustomerServiceWindow
}

// canSendFreeForm reports whether a free-form message may be sent to chatID.
func (g windowGate) canSendFreeForm(ctx context.Context, chatID string) error {
	if chatID == "" {
		// Nothing to check against; let the send proceed and let the API judge it.
		return nil
	}
	lastInbound, err := g.lastInbound.LastInboundAt(ctx, chatID)
	if err != nil {
		// Fail open: a lookup failure is our problem, not a platform refusal.
		// Refusing here would silently drop messages whenever the store hiccups.
		return nil
	}
	if IsWithinWindow(lastInbound, g.now()) {
		return nil
	}
	if lastInbound.IsZero() {
		return fmt.Errorf(
			"the user has never messaged this bot, so only a template may be sent: %w",
			botsfw.ErrSendNotPermitted,
		)
	}
	return fmt.Errorf(
		"%s since the user last replied (at %s), so only a pre-approved template may be sent: %w",
		g.now().Sub(lastInbound).Round(time.Minute),
		lastInbound.UTC().Format(time.RFC3339),
		botsfw.ErrSendNotPermitted,
	)
}

// CanSend implements botsfw.SendGate.
//
// Templates bypass the window: they are precisely the mechanism for reaching a
// user outside it. Everything else is gated on the recipient's last reply.
func (g windowGate) CanSend(ctx context.Context, m botmsg.MessageFromBot) error {
	if isTemplate(m) {
		return nil
	}
	return g.canSendFreeForm(ctx, chatUID(m))
}

// chatUID extracts the recipient chat ID from m, or "" if absent.
//
// Note m.ToChat is a botmsg.ChatUID, whose only implementation in the workspace is
// botmsg.ChatIntID — an int64. A WhatsApp recipient is a phone number, so this
// adapter supplies its own ChatUID implementation rather than using ChatIntID.
func chatUID(m botmsg.MessageFromBot) string {
	if m.ToChat == nil {
		return ""
	}
	return m.ToChat.ChatUID()
}
