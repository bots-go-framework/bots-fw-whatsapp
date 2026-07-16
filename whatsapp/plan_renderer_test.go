package whatsapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botplan"
	"github.com/bots-go-framework/bots-go-core/botkb"
)

func waTarget(windowOpen bool) botplan.RenderTarget {
	return botplan.RenderTarget{Platform: botplan.PlatformWhatsApp, WindowOpen: windowOpen, Locale: "en"}
}

// choices builds n choices with distinct labels and tokens.
func choices(n int) []botplan.Choice {
	cs := make([]botplan.Choice, n)
	for i := 0; i < n; i++ {
		cs[i] = botplan.Choice{
			Label: string(rune('A' + i)),
			Token: string(rune('a' + i)),
		}
	}
	return cs
}

func keyboardOf(t *testing.T, m botmsg.MessageFromBot) *botkb.MessageKeyboard {
	t.Helper()
	kb, ok := m.Keyboard.(*botkb.MessageKeyboard)
	if !ok {
		t.Fatalf("want *botkb.MessageKeyboard, got %T", m.Keyboard)
	}
	return kb
}

func buttonCount(kb *botkb.MessageKeyboard) int {
	n := 0
	for _, row := range kb.Buttons {
		n += len(row)
	}
	return n
}

func TestRichToMarkers(t *testing.T) {
	tests := []struct {
		name string
		rich botplan.Rich
		want string
	}{
		{
			name: "bold italic code",
			rich: botplan.Rich{Lines: []botplan.Line{botplan.Para(
				botplan.Bold("A"), botplan.Text(" "), botplan.Italic("B"), botplan.Text(" "), botplan.Code("C"))}},
			want: "*A* _B_ ```C```",
		},
		{
			name: "link becomes anchor: url (no anchor markup on WA)",
			rich: botplan.Rich{Lines: []botplan.Line{botplan.Para(botplan.Link("View day", "https://x.io/d/1"))}},
			want: "View day: https://x.io/d/1",
		},
		{
			name: "link with anchor equal to url",
			rich: botplan.Rich{Lines: []botplan.Line{botplan.Para(botplan.Link("https://x.io", "https://x.io"))}},
			want: "https://x.io",
		},
		{
			name: "list and quote markers",
			rich: botplan.Rich{Lines: []botplan.Line{
				botplan.Item(botplan.Text("kite")),
				botplan.Quote(botplan.Text("moved")),
			}},
			want: "* kite\n> moved",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := richToMarkers(tt.rich); got != tt.want {
				t.Errorf("richToMarkers() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderInWindowTextOnly(t *testing.T) {
	plan := botplan.MessagePlan{Text: botplan.RichText("hi")}
	msgs, err := NewRenderer(nil).Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text != "hi" || msgs[0].Keyboard != nil {
		t.Errorf("unexpected: %+v", msgs)
	}
}

func TestRenderPromptBoundary3(t *testing.T) {
	// 3 choices → stays reply-button-shaped (no paging, no More).
	plan := botplan.MessagePlan{Text: botplan.RichText("x"), Prompt: &botplan.ActionPrompt{Choices: choices(3)}}
	msgs, err := NewRenderer(nil).Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	kb := keyboardOf(t, msgs[0])
	if buttonCount(kb) != 3 {
		t.Fatalf("want 3 buttons, got %d", buttonCount(kb))
	}
	if hasMoreChoice(kb) {
		t.Error("3 choices should not page")
	}
}

func TestRenderPromptBoundary10(t *testing.T) {
	// 10 choices → exactly the list ceiling, no paging.
	plan := botplan.MessagePlan{Text: botplan.RichText("x"), Prompt: &botplan.ActionPrompt{Choices: choices(10)}}
	msgs, err := NewRenderer(nil).Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	kb := keyboardOf(t, msgs[0])
	if buttonCount(kb) != 10 {
		t.Fatalf("want 10 buttons, got %d", buttonCount(kb))
	}
	if hasMoreChoice(kb) {
		t.Error("10 choices should not page")
	}
}

func TestRenderPromptBoundary12Pages(t *testing.T) {
	// 12 choices → paged: 9 real choices + a "More…" choice = 10 rows.
	var lost []Degradation
	r := NewRenderer(nil).OnDegradation(func(_ context.Context, _ botmsg.MessageFromBot, notes []Degradation) {
		lost = append(lost, notes...)
	})
	plan := botplan.MessagePlan{Text: botplan.RichText("x"), Prompt: &botplan.ActionPrompt{Choices: choices(12)}}
	msgs, err := r.Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	kb := keyboardOf(t, msgs[0])
	if buttonCount(kb) != wabotapiMaxListRows {
		t.Fatalf("paged page should be %d rows, got %d", wabotapiMaxListRows, buttonCount(kb))
	}
	if !hasMoreChoice(kb) {
		t.Fatal("12 choices should produce a More… choice")
	}
	more := lastButton(kb)
	db, ok := more.(*botkb.DataButton)
	if !ok || db.Text != MoreChoiceLabel {
		t.Fatalf("last button should be the More choice, got %+v", more)
	}
	if !strings.HasPrefix(db.Data, PaginationVerb) {
		t.Errorf("More token %q should carry the pagination verb %q", db.Data, PaginationVerb)
	}
	if len(lost) == 0 {
		t.Error("paging should be reported as a degradation")
	}
}

func TestRenderPromptPlusURLActionTwoMessages(t *testing.T) {
	// cta_url cannot share a message with reply buttons: prompt first, URL second.
	plan := botplan.MessagePlan{
		Text:      botplan.RichText("Coming?"),
		Prompt:    &botplan.ActionPrompt{Choices: choices(2)},
		URLAction: &botplan.URLAction{Label: "View", URL: "https://x.io/d/1"},
	}
	msgs, err := NewRenderer(nil).Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages (prompt then cta_url), got %d", len(msgs))
	}
	if msgs[0].Keyboard == nil {
		t.Error("first message should be the prompt")
	}
	cta, ok := msgs[1].BotMessage.(sendCTAURLMessage)
	if !ok {
		t.Fatalf("second message should be a native cta_url, got %T", msgs[1].BotMessage)
	}
	if cta.url != "https://x.io/d/1" {
		t.Errorf("cta_url URL = %q, want https://x.io/d/1", cta.url)
	}
	if cta.displayText != "View" {
		t.Errorf("cta_url display_text = %q, want View", cta.displayText)
	}
}

func TestRenderLivePanelIsAppendWithNote(t *testing.T) {
	var lost []Degradation
	r := NewRenderer(nil).OnDegradation(func(_ context.Context, _ botmsg.MessageFromBot, notes []Degradation) {
		lost = append(lost, notes...)
	})
	plan := botplan.MessagePlan{Text: botplan.RichText("updated"), LivePanel: &botplan.LivePanel{PanelKey: "card1"}}
	msgs, err := r.Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].IsEdit {
		t.Error("WhatsApp has no edit: must not be an edit")
	}
	if !containsNote(lost, "append-only") {
		t.Errorf("live-panel append should be reported, notes=%v", lost)
	}
}

func TestRenderMediaNative(t *testing.T) {
	// Media renders as a native image message (no degradation note).
	var lost []Degradation
	r := NewRenderer(nil).OnDegradation(func(_ context.Context, _ botmsg.MessageFromBot, notes []Degradation) {
		lost = append(lost, notes...)
	})
	plan := botplan.MessagePlan{Text: botplan.RichText("below"), Media: &botplan.MediaRef{ImageURL: "https://x.io/a.jpg", Caption: "cap"}}
	msgs, err := r.Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	img, ok := msgs[0].BotMessage.(sendImageMessage)
	if !ok {
		t.Fatalf("want sendImageMessage, got %T", msgs[0].BotMessage)
	}
	if img.link != "https://x.io/a.jpg" {
		t.Errorf("image link = %q, want https://x.io/a.jpg", img.link)
	}
	// Caption is MediaRef.Caption + "\n" + plan body.
	if !strings.Contains(img.caption, "cap") || !strings.Contains(img.caption, "below") {
		t.Errorf("image caption should carry both caption and body: %q", img.caption)
	}
	if containsNote(lost, "image sent as text") {
		t.Error("native image send must not report a text-degradation note")
	}
}

func TestRenderProactiveInWindowIsFreeForm(t *testing.T) {
	// In-window proactive renders free-form, no template involved.
	plan := botplan.MessagePlan{
		Text:      botplan.RichText("Sat kitesurf 15:00"),
		Proactive: &botplan.ProactiveSpec{Purpose: "intent_notice"},
	}
	msgs, err := NewRenderer(nil).Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text != "Sat kitesurf 15:00" {
		t.Errorf("unexpected free-form render: %+v", msgs)
	}
	if _, isTpl := msgs[0].BotMessage.(TemplateMessage); isTpl {
		t.Error("in-window proactive must not be a template")
	}
}

func TestRenderProactiveOutOfWindowUsesTemplate(t *testing.T) {
	ctx := context.Background()
	cat := NewMemoryTemplateCatalog()
	_ = cat.Upsert(ctx, "intent_notice", TemplateDef{
		Name:         "togd_intent_notice",
		Locale:       "en",
		Status:       TemplateStatusApproved,
		BodyParams:   []string{"spot", "date_line"},
		QuickReplies: []string{"I'll be there", "Maybe", "Can't make it"},
	})
	r := NewRenderer(cat)
	plan := botplan.MessagePlan{
		Text: botplan.RichText("ignored out of window"),
		Prompt: &botplan.ActionPrompt{Choices: []botplan.Choice{
			{Label: "I'll be there", Token: "y"},
			{Label: "Maybe", Token: "m"},
			{Label: "Can't make it", Token: "n"},
		}},
		Proactive: &botplan.ProactiveSpec{
			Purpose: "intent_notice",
			Locale:  "en",
			Params:  map[string]string{"spot": "Kite Beach", "date_line": "Sat 15:00"},
		},
	}
	msgs, err := r.Render(plan, waTarget(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 template message, got %d", len(msgs))
	}
	tpl, ok := msgs[0].BotMessage.(TemplateMessage)
	if !ok {
		t.Fatalf("want TemplateMessage, got %T", msgs[0].BotMessage)
	}
	if tpl.Name != "togd_intent_notice" || tpl.LanguageCode != "en" {
		t.Errorf("template = %+v", tpl)
	}
	if len(tpl.BodyParams) != 2 || tpl.BodyParams[0] != "Kite Beach" || tpl.BodyParams[1] != "Sat 15:00" {
		t.Errorf("body params in wrong order: %v", tpl.BodyParams)
	}
}

func TestRenderProactiveOutOfWindowNoTemplate(t *testing.T) {
	r := NewRenderer(NewMemoryTemplateCatalog())
	plan := botplan.MessagePlan{
		Text:      botplan.RichText("x"),
		Proactive: &botplan.ProactiveSpec{Purpose: "unknown_purpose"},
	}
	_, err := r.Render(plan, waTarget(false))
	if !errors.Is(err, botplan.ErrNoTemplateForPurpose) {
		t.Errorf("want ErrNoTemplateForPurpose, got %v", err)
	}
}

func TestRenderProactiveOutOfWindowNilCatalog(t *testing.T) {
	r := NewRenderer(nil)
	plan := botplan.MessagePlan{Text: botplan.RichText("x"), Proactive: &botplan.ProactiveSpec{Purpose: "p"}}
	_, err := r.Render(plan, waTarget(false))
	if !errors.Is(err, botplan.ErrNoTemplateForPurpose) {
		t.Errorf("want ErrNoTemplateForPurpose, got %v", err)
	}
}

func TestRenderTemplateQuickReplyMismatch(t *testing.T) {
	ctx := context.Background()
	cat := NewMemoryTemplateCatalog()
	_ = cat.Upsert(ctx, "intent_notice", TemplateDef{
		Name:         "togd_intent_notice",
		Locale:       "en",
		Status:       TemplateStatusApproved,
		QuickReplies: []string{"Yes", "No"},
	})
	r := NewRenderer(cat)
	plan := botplan.MessagePlan{
		Text: botplan.RichText("x"),
		Prompt: &botplan.ActionPrompt{Choices: []botplan.Choice{
			{Label: "Absolutely", Token: "y"}, // does not match approved "Yes"
		}},
		Proactive: &botplan.ProactiveSpec{Purpose: "intent_notice", Locale: "en"},
	}
	_, err := r.Render(plan, waTarget(false))
	if !errors.Is(err, botplan.ErrTemplateMismatch) {
		t.Errorf("want ErrTemplateMismatch, got %v", err)
	}
}

func TestRenderTemplateMissingBodyParam(t *testing.T) {
	ctx := context.Background()
	cat := NewMemoryTemplateCatalog()
	_ = cat.Upsert(ctx, "reminder", TemplateDef{
		Name:       "togd_reminder",
		Locale:     "en",
		Status:     TemplateStatusApproved,
		BodyParams: []string{"spot", "time"},
	})
	r := NewRenderer(cat)
	plan := botplan.MessagePlan{
		Text:      botplan.RichText("x"),
		Proactive: &botplan.ProactiveSpec{Purpose: "reminder", Locale: "en", Params: map[string]string{"spot": "Kite"}},
	}
	_, err := r.Render(plan, waTarget(false))
	if !errors.Is(err, botplan.ErrTemplateMismatch) {
		t.Errorf("want ErrTemplateMismatch for missing 'time' param, got %v", err)
	}
}

func TestRenderRejectsWrongPlatform(t *testing.T) {
	_, err := NewRenderer(nil).Render(botplan.MessagePlan{Text: botplan.RichText("x")},
		botplan.RenderTarget{Platform: botplan.PlatformTelegram})
	if !errors.Is(err, botplan.ErrUnsupportedTarget) {
		t.Errorf("want ErrUnsupportedTarget, got %v", err)
	}
}

func TestWhatsAppDescriptorMatchesCapabilityFacts(t *testing.T) {
	d := NewRenderer(nil).Descriptor()
	if d.MaxPromptButtons != 3 || d.MaxListRows != 10 {
		t.Errorf("button ceilings = %d/%d, want 3/10", d.MaxPromptButtons, d.MaxListRows)
	}
	if d.SupportsEdit || d.SupportsDelete || d.SupportsCallbackAck {
		t.Error("whatsapp has no edit/delete/ack")
	}
	if !d.WindowGated {
		t.Error("whatsapp is window-gated")
	}
	if d.SupportsButtonGrid || d.SupportsAnchorTextLinks {
		t.Error("whatsapp has no grid and no anchor links")
	}
	if d.TextMarkup != botplan.MarkupMarkers {
		t.Error("whatsapp uses markers")
	}
}

// --- helpers ---

func hasMoreChoice(kb *botkb.MessageKeyboard) bool {
	for _, row := range kb.Buttons {
		for _, b := range row {
			if b.GetText() == MoreChoiceLabel {
				return true
			}
		}
	}
	return false
}

func lastButton(kb *botkb.MessageKeyboard) botkb.Button {
	row := kb.Buttons[len(kb.Buttons)-1]
	return row[len(row)-1]
}

func containsNote(notes []Degradation, sub string) bool {
	for _, n := range notes {
		if strings.Contains(string(n), sub) {
			return true
		}
	}
	return false
}

func TestRenderURLActionLongLabelTruncated(t *testing.T) {
	// A URL action label longer than 20 chars is truncated with a degradation note.
	var lost []Degradation
	r := NewRenderer(nil).OnDegradation(func(_ context.Context, _ botmsg.MessageFromBot, notes []Degradation) {
		lost = append(lost, notes...)
	})
	longLabel := "This label is definitely over twenty characters long"
	plan := botplan.MessagePlan{
		Text:      botplan.RichText("body"),
		URLAction: &botplan.URLAction{Label: longLabel, URL: "https://x.io"},
	}
	msgs, err := r.Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	cta, ok := msgs[0].BotMessage.(sendCTAURLMessage)
	if !ok {
		t.Fatalf("want sendCTAURLMessage, got %T", msgs[0].BotMessage)
	}
	if len([]rune(cta.displayText)) > wabotapi.MaxCtaDisplayTextLength {
		t.Errorf("display_text %q still exceeds the 20-char limit", cta.displayText)
	}
	if !containsNote(lost, "truncated") {
		t.Errorf("truncation should be reported, got %v", lost)
	}
}

func TestRenderMediaByID(t *testing.T) {
	// When MediaRef carries a MediaID, the native image message uses ID over URL.
	plan := botplan.MessagePlan{
		Media: &botplan.MediaRef{MediaID: "media-abc", Caption: "pic"},
	}
	msgs, err := NewRenderer(nil).Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	img, ok := msgs[0].BotMessage.(sendImageMessage)
	if !ok {
		t.Fatalf("want sendImageMessage, got %T", msgs[0].BotMessage)
	}
	if img.mediaID != "media-abc" || img.link != "" {
		t.Errorf("want mediaID=media-abc, got %+v", img)
	}
	if img.caption != "pic" {
		t.Errorf("caption = %q, want pic", img.caption)
	}
}

func TestRenderMediaBodyOnlyCaption(t *testing.T) {
	// When MediaRef has no Caption but the plan has body text, body becomes caption.
	plan := botplan.MessagePlan{
		Text:  botplan.RichText("see image"),
		Media: &botplan.MediaRef{ImageURL: "https://x.io/a.jpg"},
	}
	msgs, err := NewRenderer(nil).Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	img, ok := msgs[0].BotMessage.(sendImageMessage)
	if !ok {
		t.Fatalf("want sendImageMessage, got %T", msgs[0].BotMessage)
	}
	if img.caption != "see image" {
		t.Errorf("caption = %q, want body text as caption", img.caption)
	}
}

func TestRenderURLActionOnlyUsesBodyAsCtaBody(t *testing.T) {
	// When there is only a URLAction (no prompt), plan text becomes the cta_url body.
	plan := botplan.MessagePlan{
		Text:      botplan.RichText("Visit our site"),
		URLAction: &botplan.URLAction{Label: "Open", URL: "https://x.io"},
	}
	msgs, err := NewRenderer(nil).Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	cta, ok := msgs[0].BotMessage.(sendCTAURLMessage)
	if !ok {
		t.Fatalf("want sendCTAURLMessage, got %T", msgs[0].BotMessage)
	}
	if cta.body != "Visit our site" {
		t.Errorf("cta body = %q, want plan text", cta.body)
	}
	if cta.displayText != "Open" {
		t.Errorf("display_text = %q, want Open", cta.displayText)
	}
}
