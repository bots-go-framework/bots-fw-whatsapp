package whatsapp

import "github.com/bots-go-framework/bots-fw/botmsg"

// ChatID identifies a WhatsApp chat by the user's WhatsApp ID (wa_id), which is a
// phone number in E.164 without the leading '+'.
//
// It exists because botmsg.ChatIntID — the only botmsg.ChatUID implementation in
// the framework — is an int64, modelled on Telegram's numeric chat_id. A phone
// number does not survive that type, and bots-fw-telegram's responder makes the
// assumption explicit with an unchecked assertion:
//
//	int64(m.ToChat.(botmsg.ChatIntID))
//
// So while MessageFromBot.ToChat is typed as the ChatUID interface and looks open,
// only Telegram's shape has ever passed through it. This is the second implementation.
type ChatID string

var _ botmsg.ChatUID = ChatID("")

// ChatUID implements botmsg.ChatUID.
func (c ChatID) ChatUID() string {
	return string(c)
}
