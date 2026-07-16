package whatsapp

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-go-core/botkb"
)

// Degradation is a note describing something lost on the way to WhatsApp.
//
// The app writes one rich message aimed at Telegram; this adapter fits it to
// WhatsApp's limits. Losses are returned rather than swallowed so they can be
// logged — silent degradation is how a product quietly gets worse.
type Degradation string

// htmlTag matches an HTML tag. Sufficient for the subset bots-fw's FormatHTML
// actually carries (<b>, <i>, <code>, <a href>); this is not a general parser and
// does not need to be.
var htmlTag = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)

// htmlAnchor captures an <a href="URL">TEXT</a> pair.
var htmlAnchor = regexp.MustCompile(`(?is)<a\s+[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)

// htmlEntities are the entities bots-fw apps escape into FormatHTML text.
var htmlEntities = strings.NewReplacer(
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&#39;", "'",
	"&amp;", "&", // last: unescaping it first would corrupt the others
)

// htmlToText flattens FormatHTML into plain text.
//
// WhatsApp's markup support is unverified — Meta's text-messages page documents a
// plain body with an optional link preview and no syntax — so no markup is emitted.
// Passing the HTML through unchanged would render literal "<b>" to real users, and
// erroring would push platform branches back into the app.
//
// Anchors become "text (url)" because WhatsApp auto-hyperlinks bare URLs, so the
// destination stays reachable even though the anchor text cannot be a link.
func htmlToText(s string) string {
	s = htmlAnchor.ReplaceAllStringFunc(s, func(m string) string {
		g := htmlAnchor.FindStringSubmatch(m)
		url, text := g[1], strings.TrimSpace(htmlTag.ReplaceAllString(g[2], ""))
		if text == "" || text == url {
			return url
		}
		return text + " (" + url + ")"
	})
	s = htmlTag.ReplaceAllString(s, "")
	return strings.TrimSpace(htmlEntities.Replace(s))
}

// truncate shortens s to at most max runes, marking the cut with an ellipsis so a
// clipped label does not read as the whole label.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// actionable is a button reduced to what WhatsApp can carry: a label and a
// callback payload.
type actionable struct {
	title string
	id    string
}

// flattenKeyboard reduces a botkb keyboard to a flat list of actionable buttons,
// plus notes for anything that could not survive.
//
// botkb models Telegram: NewMessageKeyboard takes [][]Button (rows), and every
// KeyboardType constant is annotated "Used by: Telegram". WhatsApp has no grid, so
// rows are flattened — visual grouping is lost, the actions are not.
func flattenKeyboard(kb botkb.Keyboard) (buttons []actionable, extraText []string, notes []Degradation) {
	mk, ok := kb.(*botkb.MessageKeyboard)
	if !ok || mk == nil {
		return nil, nil, nil
	}

	var rows int
	for _, row := range mk.Buttons {
		if len(row) > 0 {
			rows++
		}
		for _, b := range row {
			switch btn := b.(type) {
			case *botkb.DataButton:
				buttons = append(buttons, actionable{title: btn.Text, id: btn.Data})
			case *botkb.TextButton:
				// No callback payload: echo the label back as the id so a tap is
				// still identifiable.
				buttons = append(buttons, actionable{title: btn.Text, id: btn.Text})
			case *botkb.UrlButton:
				// WhatsApp reply buttons are type "reply" only — there is no URL
				// button. The link moves into the body, where it auto-hyperlinks.
				extraText = append(extraText, fmt.Sprintf("%s: %s", btn.Text, btn.URL))
				notes = append(notes, Degradation(
					fmt.Sprintf("URL button %q moved into the message body: WhatsApp reply buttons cannot carry links", btn.Text)))
			case *botkb.SwitchInlineQueryButton, *botkb.SwitchInlineQueryCurrentChatButton:
				// Inline mode is Telegram-only and has no WhatsApp analogue at all.
				notes = append(notes, Degradation(
					fmt.Sprintf("switch-inline-query button %q dropped: WhatsApp has no inline mode", b.GetText())))
			default:
				notes = append(notes, Degradation(
					fmt.Sprintf("button %q of unhandled type dropped", b.GetText())))
			}
		}
	}
	if rows > 1 {
		notes = append(notes, Degradation(
			fmt.Sprintf("keyboard flattened from %d rows: WhatsApp has no button grid", rows)))
	}
	return buttons, extraText, notes
}

// degradeToSendable converts a rich bots-fw message into the best WhatsApp
// representation available, returning notes describing anything lost.
//
// The ladder, richest first:
//
//	1–3 buttons   -> interactive reply buttons (taps still route by callback id)
//	4–10 buttons  -> an interactive list (still tappable, one extra tap to open)
//	>10 buttons   -> a numbered text menu (the user replies with a number)
//	no buttons    -> plain text
//
// Each rung keeps the message actionable. Nothing is rejected for being too rich.
func degradeToSendable(to string, m botmsg.MessageFromBot) (wabotapi.Sendable, []Degradation, error) {
	var notes []Degradation

	body := m.Text
	if m.Format != botmsg.FormatText && body != "" {
		if plain := htmlToText(body); plain != body {
			notes = append(notes, Degradation(
				"rich formatting stripped: WhatsApp text markup is unverified, so plain text is sent"))
			body = plain
		}
	}

	buttons, extraText, kbNotes := flattenKeyboard(m.Keyboard)
	notes = append(notes, kbNotes...)
	if len(extraText) > 0 {
		body = strings.TrimSpace(body + "\n\n" + strings.Join(extraText, "\n"))
	}

	if body == "" {
		return nil, notes, wabotapi.ErrEmptyBody
	}

	switch {
	case len(buttons) == 0:
		cfg := wabotapi.NewSendText(to, truncate(body, wabotapi.MaxTextBodyLength))
		if !m.DisableWebPagePreview {
			cfg = cfg.WithPreviewURL()
		}
		return cfg, notes, nil

	case len(buttons) <= wabotapi.MaxReplyButtons:
		return buttonsSendable(to, body, buttons, notes)

	case len(buttons) <= wabotapi.MaxListRows:
		notes = append(notes, Degradation(fmt.Sprintf(
			"%d buttons rendered as a tap-to-open list: WhatsApp allows at most %d inline buttons",
			len(buttons), wabotapi.MaxReplyButtons)))
		return listSendable(to, body, buttons, notes)

	default:
		notes = append(notes, Degradation(fmt.Sprintf(
			"%d buttons rendered as a numbered text menu: WhatsApp allows at most %d list rows",
			len(buttons), wabotapi.MaxListRows)))
		return numberedMenuSendable(to, body, buttons, notes)
	}
}

// buttonsSendable renders up to MaxReplyButtons as interactive reply buttons.
func buttonsSendable(to, body string, btns []actionable, notes []Degradation) (wabotapi.Sendable, []Degradation, error) {
	replies := make([]wabotapi.ReplyButton, 0, len(btns))
	seen := make(map[string]bool, len(btns))
	for _, b := range btns {
		title := truncate(b.title, wabotapi.MaxButtonTitleLength)
		if len(b.title) != len(title) {
			notes = append(notes, Degradation(fmt.Sprintf(
				"button label %q truncated to %d characters", b.title, wabotapi.MaxButtonTitleLength)))
		}
		// Meta rejects duplicate titles outright, so disambiguate rather than fail.
		if seen[title] {
			for i := 2; seen[title]; i++ {
				title = truncate(b.title, wabotapi.MaxButtonTitleLength-2) + " " + fmt.Sprint(i)
			}
			notes = append(notes, Degradation(
				"duplicate button label disambiguated: WhatsApp requires unique titles"))
		}
		seen[title] = true
		replies = append(replies, wabotapi.NewReplyButton(truncate(b.id, wabotapi.MaxButtonIDLength), title))
	}
	return wabotapi.NewSendButtons(to, truncate(body, wabotapi.MaxButtonsBodyLength), replies...), notes, nil
}

// listSendable renders up to MaxListRows as a tap-to-open list.
func listSendable(to, body string, btns []actionable, notes []Degradation) (wabotapi.Sendable, []Degradation, error) {
	rows := make([]wabotapi.ListRow, 0, len(btns))
	for _, b := range btns {
		title := truncate(b.title, wabotapi.MaxRowTitleLength)
		if len(b.title) != len(title) {
			notes = append(notes, Degradation(fmt.Sprintf(
				"row label %q truncated to %d characters", b.title, wabotapi.MaxRowTitleLength)))
		}
		rows = append(rows, wabotapi.ListRow{
			ID:    truncate(b.id, wabotapi.MaxRowIDLength),
			Title: title,
		})
	}
	return wabotapi.NewSendList(
		to,
		truncate(body, wabotapi.MaxListBodyLength),
		"Choose",
		wabotapi.ListSection{Rows: rows},
	), notes, nil
}

// numberedMenuSendable is the last rung: more options than any WhatsApp affordance
// holds, so they become a numbered menu the user replies to.
//
// Lossy — the callback ids cannot ride along — but the message stays actionable,
// which beats refusing to send it.
func numberedMenuSendable(to, body string, btns []actionable, notes []Degradation) (wabotapi.Sendable, []Degradation, error) {
	var sb strings.Builder
	sb.WriteString(body)
	sb.WriteString("\n")
	for i, b := range btns {
		_, _ = fmt.Fprintf(&sb, "\n%d. %s", i+1, b.title)
	}
	sb.WriteString("\n\nReply with a number to choose.")
	return wabotapi.NewSendText(to, truncate(sb.String(), wabotapi.MaxTextBodyLength)), notes, nil
}
