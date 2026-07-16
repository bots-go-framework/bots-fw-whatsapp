package whatsapp

import (
	"context"
	"fmt"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botplan"
)

// wabotapi limit aliases, so the descriptor and renderer read the platform facts
// from one place (the client package) rather than re-declaring numbers.
const (
	wabotapiMaxReplyButtons = wabotapi.MaxReplyButtons      // 3
	wabotapiMaxListRows     = wabotapi.MaxListRows          // 10
	wabotapiMaxButtonTitle  = wabotapi.MaxButtonTitleLength // 20
)

// renderTemplate resolves an out-of-window proactive plan to a TemplateMessage.
//
// It looks the purpose up in the catalog (with locale fallback), validates that
// the plan matches the approved template's shape, maps the plan's params to the
// template's body parameters in declared order, and returns a
// botmsg.MessageFromBot carrying a whatsapp.TemplateMessage — the only thing
// deliverable outside the 24h window.
//
// Errors are typed for the caller's scenario policy (SYS-TPL-030):
//   - ErrNoTemplateForPurpose: no approved template for the purpose.
//   - ErrTemplateMismatch: an approved template exists but the plan does not fit
//     it — a missing body param, or a prompt whose choices do not match the
//     template's approved quick-reply labels (quick replies are fixed at approval
//     time, not composed per send — whatsapp/template-buttons).
func (r *Renderer) renderTemplate(ctx context.Context, plan botplan.MessagePlan, target botplan.RenderTarget) (botmsg.MessageFromBot, []Degradation, error) {
	var zero botmsg.MessageFromBot

	if r.catalog == nil {
		return zero, nil, fmt.Errorf("%w: no template catalog configured for purpose %q",
			botplan.ErrNoTemplateForPurpose, plan.Proactive.Purpose)
	}

	locale := plan.Proactive.Locale
	if locale == "" {
		locale = target.Locale
	}

	def, found, err := r.catalog.Get(ctx, plan.Proactive.Purpose, locale)
	if err != nil {
		return zero, nil, err
	}
	if !found {
		return zero, nil, fmt.Errorf("%w: purpose=%q locale=%q",
			botplan.ErrNoTemplateForPurpose, plan.Proactive.Purpose, locale)
	}

	bodyParams, err := mapBodyParams(def, plan.Proactive.Params)
	if err != nil {
		return zero, nil, err
	}

	if err := validateQuickReplies(def, plan.Prompt); err != nil {
		return zero, nil, err
	}

	var notes []Degradation
	if plan.Media != nil {
		notes = append(notes, Degradation(
			"media dropped from an out-of-window proactive send: only the approved template's components are deliverable"))
	}
	if plan.URLAction != nil && def.URLButton == nil {
		notes = append(notes, Degradation(
			"URL action dropped: the approved template has no URL button (buttons are fixed at approval time)"))
	}

	// The neutral plan carries no recipient (that is a transport concern), so
	// ToChat is left unset here; the caller stamps it before sending, exactly as
	// with the free-form messages.
	msg := botmsg.MessageFromBot{
		BotMessage: TemplateMessage{
			Name:         def.Name,
			LanguageCode: def.Locale,
			BodyParams:   bodyParams,
		},
	}
	return msg, notes, nil
}

// mapBodyParams orders the plan's params to match the template's declared body
// placeholders. Every placeholder must have a value; a missing one is a mismatch
// rather than a silently blank parameter (Meta rejects unfilled placeholders).
func mapBodyParams(def TemplateDef, params map[string]string) ([]string, error) {
	out := make([]string, 0, len(def.BodyParams))
	for _, name := range def.BodyParams {
		v, ok := params[name]
		if !ok {
			return nil, fmt.Errorf("%w: template %q body placeholder %q has no value in the plan's params",
				botplan.ErrTemplateMismatch, def.Name, name)
		}
		out = append(out, v)
	}
	return out, nil
}

// validateQuickReplies checks the plan's prompt against the template's approved
// quick-reply labels.
//
// A template's quick-reply buttons are fixed at approval time
// (whatsapp/template-buttons buttonsFixedAtApprovalNotPerSend), so the plan
// cannot introduce different or extra choices. The rule: if the template
// declares quick replies, the plan's prompt (when present) must name the same
// labels, in order, and no more than the template declares. A plan with no
// prompt is fine — the approved buttons still render. The label round-trips as
// the callback payload (whatsapp/template-buttons quickReplyPayloadIsLabelText),
// which is why the labels, not tokens, must match.
func validateQuickReplies(def TemplateDef, prompt *botplan.ActionPrompt) error {
	if prompt == nil {
		return nil
	}
	if len(prompt.Choices) > len(def.QuickReplies) {
		return fmt.Errorf("%w: plan has %d choices but template %q approves %d quick replies",
			botplan.ErrTemplateMismatch, len(prompt.Choices), def.Name, len(def.QuickReplies))
	}
	for i, c := range prompt.Choices {
		if c.Label != def.QuickReplies[i] {
			return fmt.Errorf("%w: choice %d label %q does not match template %q approved quick reply %q",
				botplan.ErrTemplateMismatch, i, c.Label, def.Name, def.QuickReplies[i])
		}
	}
	return nil
}
