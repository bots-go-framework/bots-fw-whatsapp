package whatsapp

import (
	"strings"

	"github.com/bots-go-framework/bots-fw/botplan"
)

// richToMarkers renders a botplan.Rich as WhatsApp body text using WhatsApp's
// inline markers (capability-map whatsapp/text-formatting).
//
//   - bold   → *text*
//   - italic → _text_
//   - code   → ```text``` (WhatsApp's monospace marker)
//   - link   → "anchor: url" — WhatsApp has NO anchor-text markup, so the anchor
//     and target are shown side by side and the bare URL auto-hyperlinks
//     (whatsapp/text-formatting noAnchorTextLinks). When anchor == url or the
//     anchor is empty, only the url is emitted, to avoid "https://x: https://x".
//   - list item → "* item" (WhatsApp bulleted-list marker)
//   - quote     → "> text"
//
// WhatsApp markers are always interpreted — there is no parse-mode opt-out
// (whatsapp/text-formatting noParseModeParameter) — so literal marker characters
// in user text would be misread. This is a known, recorded limitation; the
// neutral Rich model carries no literal markers, only styled spans, so the app
// never has to think about it.
func richToMarkers(r botplan.Rich) string {
	var sb strings.Builder
	for i, line := range r.Lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		switch line.Kind {
		case botplan.LineListItem:
			sb.WriteString("* ")
		case botplan.LineQuote:
			sb.WriteString("> ")
		}
		writeSpansMarkers(&sb, line.Spans)
	}
	return sb.String()
}

func writeSpansMarkers(sb *strings.Builder, spans []botplan.Span) {
	for _, sp := range spans {
		switch sp.Kind {
		case botplan.SpanBold:
			sb.WriteString("*")
			sb.WriteString(sp.Text)
			sb.WriteString("*")
		case botplan.SpanItalic:
			sb.WriteString("_")
			sb.WriteString(sp.Text)
			sb.WriteString("_")
		case botplan.SpanCode:
			sb.WriteString("```")
			sb.WriteString(sp.Text)
			sb.WriteString("```")
		case botplan.SpanLink:
			if sp.Text == "" || sp.Text == sp.URL {
				sb.WriteString(sp.URL)
			} else {
				sb.WriteString(sp.Text)
				sb.WriteString(": ")
				sb.WriteString(sp.URL)
			}
		default:
			sb.WriteString(sp.Text)
		}
	}
}
