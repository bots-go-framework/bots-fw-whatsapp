package whatsapp

import (
	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
)

// TemplateMessage asks the bot to send a pre-approved WhatsApp template.
//
// Templates are the only messages deliverable outside the 24-hour customer
// service window, so any proactive send — a reminder, a notification, anything
// the user did not just prompt — must be one.
//
// Usage mirrors the existing convention for platform-specific messages: set it on
// MessageFromBot.BotMessage, exactly as a Telegram photo is sent by assigning a
// tgbotapi.PhotoConfig. The app therefore imports this package to build one.
//
//	m := botmsg.MessageFromBot{
//		BotMessage: whatsapp.TemplateMessage{
//			Name:         "payment_reminder",
//			LanguageCode: "en_US",
//			BodyParams:   []string{"Jessica", "$40"},
//		},
//	}
//
// Note bots-fw's botmsg.Type enum has no template member — it is a list of
// Telegram Bot API methods — so BotMessageType() cannot describe this honestly.
// The responder dispatches on the concrete type instead, which is why this works
// today without a framework change.
type TemplateMessage struct {
	// Name is the template's registered name, as approved by Meta.
	Name string

	// LanguageCode selects the template localisation, e.g. "en_US".
	LanguageCode string

	// BodyParams supplies positional body parameters, in placeholder order.
	// Mutually exclusive with NamedBodyParams.
	BodyParams []string

	// NamedBodyParams supplies named body parameters. Mutually exclusive with
	// BodyParams: a component may not mix the two formats.
	NamedBodyParams []wabotapi.NamedParam
}

var _ botmsg.BotMessage = (*TemplateMessage)(nil)

// BotMessageType implements botmsg.BotMessage.
//
// Returns TypeUndefined because bots-fw's enum cannot express a template. This is
// deliberate: inventing an out-of-range constant would collide the moment bots-fw
// appends an enum member. Dispatch is by concrete type.
func (TemplateMessage) BotMessageType() botmsg.Type {
	return botmsg.TypeUndefined
}

// toConfig converts to an API client config for the given recipient.
func (t TemplateMessage) toConfig(to string) *wabotapi.SendTemplateConfig {
	cfg := wabotapi.NewSendTemplate(to, t.Name, t.LanguageCode)
	switch {
	case len(t.NamedBodyParams) > 0:
		cfg = cfg.WithNamedBodyParams(t.NamedBodyParams...)
	case len(t.BodyParams) > 0:
		cfg = cfg.WithBodyParams(t.BodyParams...)
	}
	return cfg
}

// isTemplate reports whether m carries a TemplateMessage, and so may be sent
// regardless of the customer service window.
func isTemplate(m botmsg.MessageFromBot) bool {
	switch m.BotMessage.(type) {
	case TemplateMessage, *TemplateMessage:
		return true
	}
	return false
}
