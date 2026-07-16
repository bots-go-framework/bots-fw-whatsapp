package whatsapp

import (
	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
)

// sendImageMessage is a botmsg.BotMessage that carries a native WhatsApp image
// send, built by the botplan renderer.
//
// The recipient (to) is not known at render time; toSendable stamps it.
// Follows the same deferred-recipient pattern as TemplateMessage.
type sendImageMessage struct {
	// mediaID is the uploaded media asset ID (preferred over link).
	mediaID string
	// link is the publicly hosted URL (used when mediaID is empty).
	link string
	// caption is the optional image caption (may be empty).
	caption string
}

var _ botmsg.BotMessage = sendImageMessage{}

// BotMessageType implements botmsg.BotMessage.
func (sendImageMessage) BotMessageType() botmsg.Type { return botmsg.TypeUndefined }

// toConfig builds the wabotapi config for the given recipient.
func (m sendImageMessage) toConfig(to string) *wabotapi.SendImageConfig {
	var cfg *wabotapi.SendImageConfig
	if m.mediaID != "" {
		cfg = wabotapi.NewSendImageByID(to, m.mediaID)
	} else {
		cfg = wabotapi.NewSendImageByLink(to, m.link)
	}
	if m.caption != "" {
		cfg = cfg.WithCaption(m.caption)
	}
	return cfg
}

// sendCTAURLMessage is a botmsg.BotMessage that carries a native WhatsApp
// interactive cta_url button message, built by the botplan renderer.
//
// The recipient (to) is not known at render time; toSendable stamps it.
// Follows the same deferred-recipient pattern as TemplateMessage.
type sendCTAURLMessage struct {
	// body is the required message body.
	body string
	// displayText is the button label (max wabotapi.MaxCtaDisplayTextLength).
	displayText string
	// url is the URL the button opens.
	url string
}

var _ botmsg.BotMessage = sendCTAURLMessage{}

// BotMessageType implements botmsg.BotMessage.
func (sendCTAURLMessage) BotMessageType() botmsg.Type { return botmsg.TypeUndefined }

// toConfig builds the wabotapi config for the given recipient.
func (m sendCTAURLMessage) toConfig(to string) *wabotapi.SendCTAURLConfig {
	return wabotapi.NewSendCTAURL(to, m.body, m.displayText, m.url)
}
