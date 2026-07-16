package whatsapp

import (
	"context"
	"fmt"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botplan"
	"github.com/bots-go-framework/bots-go-core/botkb"
)

// WhatsAppDescriptor is WhatsApp's static capability descriptor.
//
// Every field mirrors a record in can-i-use/capability-map.json (cited on the
// botplan.Descriptor field it sets). WhatsApp is the weaker pilot platform: at
// most 3 reply buttons then a 10-row list, no edit or delete, no callback ack,
// a 24-hour send window, markers not HTML, no anchor-text links.
var WhatsAppDescriptor = botplan.Descriptor{
	MaxPromptButtons:    wabotapiMaxReplyButtons, // whatsapp/reply-buttons maxButtons=3
	MaxListRows:         wabotapiMaxListRows,     // whatsapp/list-messages maxRowsAcrossAllSections=10
	MaxButtonLabelChars: wabotapiMaxButtonTitle,  // whatsapp/reply-buttons buttonLabelMaxChars=20

	SupportsEdit:            false, // whatsapp/edit-message absent → append
	SupportsDelete:          false, // whatsapp/delete-message absent
	SupportsCallbackAck:     false, // whatsapp/callback-ack absent — nothing to ack
	WindowGated:             true,  // whatsapp/customer-service-window (24h)
	SupportsInlineURLButton: true,  // whatsapp/cta-url-button native — but in-window only
	SupportsButtonGrid:      false, // whatsapp/reply-buttons grid=false
	SupportsMedia:           true,  // whatsapp/send-image native
	TextMarkup:              botplan.MarkupMarkers,
	SupportsAnchorTextLinks: false, // whatsapp/text-formatting noAnchorTextLinks
}

// PaginationVerb is the token verb the renderer emits on the "More…" choice when
// a prompt exceeds the list-row ceiling and must be paged. The application's
// callback router recognises it and re-renders the next page.
const PaginationVerb = "more"

// morePageArg is the token arg carrying the next page index on a "More…" choice.
const morePageArg = "p"

// MoreChoiceLabel is the label of the paging choice appended to a full page.
const MoreChoiceLabel = "More…"

// Renderer renders a neutral botplan.MessagePlan into WhatsApp messages.
//
// It emits botmsg.MessageFromBot values the existing Responder already sends:
// text uses WhatsApp markers (richToMarkers), a prompt becomes a botkb keyboard
// that degradeToSendable fits to reply-buttons / list / paged-list, a live panel
// is rendered as an ordinary (append) message, an out-of-window proactive send
// becomes a TemplateMessage resolved from the catalog, and everything lossy is
// reported through the DegradationLogger.
//
// The renderer holds the TemplateCatalog because out-of-window proactive plans
// must resolve a purpose to an approved template. The botplan.Renderer.Render
// method has no context; Render therefore uses context.Background for the catalog
// lookup, and RenderWithContext is provided for callers that have a request
// context (the recommended path for the dalgo-backed catalog).
type Renderer struct {
	catalog TemplateCatalog
	onLoss  DegradationLogger
}

var _ botplan.Renderer = (*Renderer)(nil)

// NewRenderer returns a WhatsApp renderer using catalog to resolve out-of-window
// proactive templates. catalog may be nil if the caller never renders an
// out-of-window proactive plan; such a plan then fails with ErrNoTemplateForPurpose.
func NewRenderer(catalog TemplateCatalog) *Renderer {
	return &Renderer{catalog: catalog}
}

// OnDegradation registers a callback invoked whenever rendering a plan loses
// something on the way to WhatsApp. Returns the Renderer for chaining.
func (r *Renderer) OnDegradation(log DegradationLogger) *Renderer {
	r.onLoss = log
	return r
}

// Descriptor implements botplan.Renderer.
func (*Renderer) Descriptor() botplan.Descriptor { return WhatsAppDescriptor }

// Render implements botplan.Renderer using a background context for the template
// catalog lookup. Prefer RenderWithContext where a request context exists.
func (r *Renderer) Render(plan botplan.MessagePlan, target botplan.RenderTarget) ([]botmsg.MessageFromBot, error) {
	return r.RenderWithContext(context.Background(), plan, target)
}

// RenderWithContext renders plan for a WhatsApp target, using ctx for the
// template catalog lookup that an out-of-window proactive send requires.
func (r *Renderer) RenderWithContext(ctx context.Context, plan botplan.MessagePlan, target botplan.RenderTarget) ([]botmsg.MessageFromBot, error) {
	if target.Platform != botplan.PlatformWhatsApp {
		return nil, botplan.ErrUnsupportedTarget
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}

	var notes []Degradation

	// Out-of-window proactive send: the only deliverable is an approved template
	// (whatsapp/customer-service-window). This path ignores the free-form body,
	// buttons and media — the template's approved shape governs.
	if plan.IsProactive() && !target.WindowOpen {
		msg, tnotes, err := r.renderTemplate(ctx, plan, target)
		if err != nil {
			return nil, err
		}
		r.report(ctx, notes, tnotes)
		return []botmsg.MessageFromBot{msg}, nil
	}

	// In-window (or reply): free-form. Body is markers; buttons degrade via the
	// existing ladder; a URL action and media are handled explicitly.
	msgs, freeNotes := r.renderFreeForm(plan)
	notes = append(notes, freeNotes...)
	r.report(ctx, notes, nil)
	return msgs, nil
}

// renderFreeForm builds the in-window messages for a plan.
//
// Ordering, per architecture.md §4.2 and the cta_url constraint
// (whatsapp/cta-url-button — a URL button cannot share a message with reply
// buttons): when a plan carries BOTH a prompt and a URL action, the prompt
// message is sent first and the URL action follows as a second message. When it
// carries only one, a single message suffices.
func (r *Renderer) renderFreeForm(plan botplan.MessagePlan) ([]botmsg.MessageFromBot, []Degradation) {
	var msgs []botmsg.MessageFromBot
	var notes []Degradation

	body := richToMarkers(plan.Text)

	if plan.LivePanel != nil {
		notes = append(notes, Degradation(
			"live-panel rendered as a new message: WhatsApp has no edit endpoint, so the conversation is append-only"))
	}

	// Media: a separate image message preceding the text.
	if plan.Media != nil {
		mediaMsg, mnotes := r.mediaMessage(plan.Media, body)
		msgs = append(msgs, mediaMsg)
		notes = append(notes, mnotes...)
		// The caption carried the body; the text message below would duplicate it.
		body = ""
	}

	// Prompt: a botkb keyboard the existing degrade ladder fits to WhatsApp, with
	// paging beyond the list ceiling.
	if plan.Prompt != nil {
		promptMsg, pnotes := r.promptMessage(plan.Prompt, body)
		msgs = append(msgs, promptMsg)
		notes = append(notes, pnotes...)
		body = "" // consumed by the prompt message
	}

	// URL action: its own interactive cta_url message. cta_url cannot combine
	// with reply buttons (whatsapp/cta-url-button). When the plan carries a
	// prompt, the prompt message was already emitted above and body is now empty;
	// the cta_url body falls back to the action label. When the plan carries text
	// but no prompt, body carries the plan text and becomes the cta_url body.
	if plan.URLAction != nil {
		urlMsg, unotes := r.urlActionMessage(plan.URLAction, body)
		msgs = append(msgs, urlMsg)
		notes = append(notes, unotes...)
		body = "" // consumed by the cta_url message
	}

	// Remaining body (no prompt, no media, no URLAction consumed it).
	if body != "" {
		msgs = append(msgs, textMessage(body))
	}

	return msgs, notes
}

// promptMessage builds the message carrying a prompt's choices.
//
// Up to MaxListRows choices become a botkb keyboard the existing ladder renders
// as reply buttons (≤3) or a list (4..10). Beyond MaxListRows the choices are
// paged: the first (MaxListRows-1) choices plus a "More…" choice carrying a
// pagination token, so every page stays a single tappable list.
func (r *Renderer) promptMessage(prompt *botplan.ActionPrompt, body string) (botmsg.MessageFromBot, []Degradation) {
	var notes []Degradation
	choices := prompt.Choices

	if len(choices) > wabotapiMaxListRows {
		var pageNote Degradation
		choices, pageNote = firstPageWithMore(choices, 0)
		notes = append(notes, pageNote)
	}

	rows := make([]botkb.Button, 0, len(choices))
	for _, c := range choices {
		rows = append(rows, botkb.NewDataButton(c.Label, c.Token))
	}
	kb := botkb.NewMessageKeyboard(botkb.KeyboardTypeInline, rows)

	m := botmsg.MessageFromBot{
		TextMessageFromBot: botmsg.TextMessageFromBot{
			Text:     body,
			Keyboard: kb,
		},
	}
	return m, notes
}

// firstPageWithMore returns the choices for page pageIndex: the first
// (MaxListRows-1) not-yet-shown choices followed by a "More…" choice whose token
// points at the next page. Reserving one row for "More…" keeps the whole page a
// single list message (MaxListRows rows total).
func firstPageWithMore(choices []botplan.Choice, pageIndex int) ([]botplan.Choice, Degradation) {
	perPage := wabotapiMaxListRows - 1
	start := pageIndex * perPage
	end := start + perPage
	if end > len(choices) {
		end = len(choices)
	}
	page := make([]botplan.Choice, 0, wabotapiMaxListRows)
	page = append(page, choices[start:end]...)
	page = append(page, botplan.Choice{
		Label: MoreChoiceLabel,
		Token: fmt.Sprintf("%s\t\t%s=%d", PaginationVerb, morePageArg, pageIndex+1),
	})
	note := Degradation(fmt.Sprintf(
		"%d choices exceed the %d-row list ceiling: paged with a %q choice (page %d shown)",
		len(choices), wabotapiMaxListRows, MoreChoiceLabel, pageIndex))
	return page, note
}

// urlActionMessage renders a URL action as a native cta_url interactive button.
//
// The label is capped at wabotapi.MaxCtaDisplayTextLength (20 characters) and
// truncated with an ellipsis when it exceeds the limit, which is recorded as a
// degradation. The body is the plan text that rode with the URLAction (may be
// empty — cta_url requires a non-empty body, so a placeholder is used).
func (r *Renderer) urlActionMessage(a *botplan.URLAction, body string) (botmsg.MessageFromBot, []Degradation) {
	var notes []Degradation
	displayText := a.Label
	if truncated := truncate(displayText, wabotapi.MaxCtaDisplayTextLength); truncated != displayText {
		notes = append(notes, Degradation(fmt.Sprintf(
			"URL action label %q truncated to %d characters for cta_url display_text", a.Label, wabotapi.MaxCtaDisplayTextLength)))
		displayText = truncated
	}
	ctaBody := body
	if ctaBody == "" {
		ctaBody = a.Label
	}
	m := botmsg.MessageFromBot{
		BotMessage: sendCTAURLMessage{
			body:        ctaBody,
			displayText: displayText,
			url:         a.URL,
		},
	}
	return m, notes
}

// mediaMessage renders an image as a native WhatsApp image message.
//
// MediaID is preferred over ImageURL when both are set (Meta recommends
// uploaded assets — whatsapp/send-image). The caption is the MediaRef's
// Caption; when body is also non-empty it is appended after a newline.
func (r *Renderer) mediaMessage(m *botplan.MediaRef, body string) (botmsg.MessageFromBot, []Degradation) {
	caption := m.Caption
	if body != "" {
		if caption != "" {
			caption += "\n" + body
		} else {
			caption = body
		}
	}
	img := sendImageMessage{caption: caption}
	if m.MediaID != "" {
		img.mediaID = m.MediaID
	} else {
		img.link = m.ImageURL
	}
	return botmsg.MessageFromBot{BotMessage: img}, nil
}

// textMessage builds a plain body MessageFromBot.
func textMessage(body string) botmsg.MessageFromBot {
	return botmsg.MessageFromBot{
		TextMessageFromBot: botmsg.TextMessageFromBot{Text: body},
	}
}

// report forwards accumulated degradation notes to the logger, if any.
func (r *Renderer) report(ctx context.Context, a, b []Degradation) {
	if r.onLoss == nil {
		return
	}
	notes := append(a, b...)
	if len(notes) == 0 {
		return
	}
	r.onLoss(ctx, botmsg.MessageFromBot{}, notes)
}
