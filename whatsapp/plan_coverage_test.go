package whatsapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bots-go-framework/bots-fw/botmsg"
	"github.com/bots-go-framework/bots-fw/botplan"
)

// This file closes the branches the behaviour tests leave open, matching the
// repo's coverage_test.go convention.

// errCatalog is a TemplateCatalog whose Get always errors, to exercise the
// catalog-error path of renderTemplate.
type errCatalog struct{}

func (errCatalog) Get(context.Context, string, string) (TemplateDef, bool, error) {
	return TemplateDef{}, false, errors.New("catalog boom")
}
func (errCatalog) Upsert(context.Context, string, TemplateDef) error { return nil }
func (errCatalog) SetStatus(context.Context, string, TemplateStatus) (bool, error) {
	return false, nil
}

func TestRenderTemplateCatalogError(t *testing.T) {
	r := NewRenderer(errCatalog{})
	plan := botplan.MessagePlan{Text: botplan.RichText("x"), Proactive: &botplan.ProactiveSpec{Purpose: "p"}}
	_, err := r.Render(plan, waTarget(false))
	if err == nil || strings.Contains(err.Error(), "botplan") {
		t.Fatalf("want raw catalog error, got %v", err)
	}
}

// TestRenderTemplateLocaleFallsBackToTarget covers the empty-ProactiveSpec.Locale
// branch, where the target locale is used instead.
func TestRenderTemplateLocaleFallsBackToTarget(t *testing.T) {
	ctx := context.Background()
	cat := NewMemoryTemplateCatalog()
	_ = cat.Upsert(ctx, "digest", TemplateDef{
		Name: "togd_daily_digest", Locale: "en", Status: TemplateStatusApproved,
	})
	r := NewRenderer(cat)
	plan := botplan.MessagePlan{
		Text:      botplan.RichText("x"),
		Proactive: &botplan.ProactiveSpec{Purpose: "digest"}, // no locale → uses target.Locale "en"
	}
	msgs, err := r.Render(plan, waTarget(false))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := msgs[0].BotMessage.(TemplateMessage); !ok {
		t.Fatalf("want TemplateMessage, got %T", msgs[0].BotMessage)
	}
}

// TestRenderTemplateDropsMediaAndURLAction covers the two drop-notes in
// renderTemplate for an out-of-window proactive plan that carried media and a
// URL action the approved template cannot express.
func TestRenderTemplateDropsMediaAndURLAction(t *testing.T) {
	ctx := context.Background()
	cat := NewMemoryTemplateCatalog()
	_ = cat.Upsert(ctx, "notice", TemplateDef{
		Name: "togd_notice", Locale: "en", Status: TemplateStatusApproved,
	})
	var lost []Degradation
	r := NewRenderer(cat).OnDegradation(func(_ context.Context, _ botmsg.MessageFromBot, notes []Degradation) {
		lost = append(lost, notes...)
	})
	plan := botplan.MessagePlan{
		Text:      botplan.RichText("x"),
		Media:     &botplan.MediaRef{ImageURL: "https://x.io/a.jpg"},
		URLAction: &botplan.URLAction{Label: "View", URL: "https://x.io"},
		Proactive: &botplan.ProactiveSpec{Purpose: "notice", Locale: "en"},
	}
	if _, err := r.Render(plan, waTarget(false)); err != nil {
		t.Fatal(err)
	}
	if !containsNote(lost, "media dropped") {
		t.Errorf("expected media-drop note, got %v", lost)
	}
	if !containsNote(lost, "URL action dropped") {
		t.Errorf("expected url-drop note, got %v", lost)
	}
}

// TestRenderTemplateMatchingQuickReplies covers validateQuickReplies' success
// loop with an exact label match.
func TestRenderTemplateMatchingQuickReplies(t *testing.T) {
	ctx := context.Background()
	cat := NewMemoryTemplateCatalog()
	_ = cat.Upsert(ctx, "notice", TemplateDef{
		Name: "togd_notice", Locale: "en", Status: TemplateStatusApproved,
		QuickReplies: []string{"Yes", "No"},
	})
	r := NewRenderer(cat)
	plan := botplan.MessagePlan{
		Text: botplan.RichText("x"),
		Prompt: &botplan.ActionPrompt{Choices: []botplan.Choice{
			{Label: "Yes", Token: "y"},
			{Label: "No", Token: "n"},
		}},
		Proactive: &botplan.ProactiveSpec{Purpose: "notice", Locale: "en"},
	}
	if _, err := r.Render(plan, waTarget(false)); err != nil {
		t.Fatalf("matching quick replies should render: %v", err)
	}
}

// TestRenderMediaOnlyEmptyText covers mediaMessage's "[image]" fallback when the
// plan carries media but no text and no caption.
func TestRenderMediaOnlyEmptyText(t *testing.T) {
	plan := botplan.MessagePlan{Media: &botplan.MediaRef{MediaID: "id123"}}
	msgs, err := NewRenderer(nil).Render(plan, waTarget(true))
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Text != "[image]" {
		t.Errorf("want [image] fallback, got %q", msgs[0].Text)
	}
}

// TestRenderNoDegradationLoggerIsSafe covers report()'s nil-logger and
// no-notes early returns.
func TestRenderNoDegradationLoggerIsSafe(t *testing.T) {
	// nil logger, with a plan that produces notes (live panel).
	plan := botplan.MessagePlan{Text: botplan.RichText("x"), LivePanel: &botplan.LivePanel{PanelKey: "k"}}
	if _, err := NewRenderer(nil).Render(plan, waTarget(true)); err != nil {
		t.Fatal(err)
	}
	// logger set, but a plain plan with no notes → the no-notes early return.
	called := false
	r := NewRenderer(nil).OnDegradation(func(context.Context, botmsg.MessageFromBot, []Degradation) { called = true })
	if _, err := r.Render(botplan.MessagePlan{Text: botplan.RichText("x")}, waTarget(true)); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("no-loss plan should not invoke the degradation logger")
	}
}

// TestRenderPropagatesInvalidPlan covers RenderWithContext's Validate-error path.
func TestRenderPropagatesInvalidPlan(t *testing.T) {
	_, err := NewRenderer(nil).Render(botplan.MessagePlan{}, waTarget(true)) // no text, no media
	if !errors.Is(err, botplan.ErrInvalidPlan) {
		t.Errorf("want ErrInvalidPlan, got %v", err)
	}
}

// TestRenderTemplateTooManyChoices covers validateQuickReplies' count-exceeds
// branch: the plan names more choices than the template approves.
func TestRenderTemplateTooManyChoices(t *testing.T) {
	ctx := context.Background()
	cat := NewMemoryTemplateCatalog()
	_ = cat.Upsert(ctx, "notice", TemplateDef{
		Name: "togd_notice", Locale: "en", Status: TemplateStatusApproved,
		QuickReplies: []string{"Yes"}, // only one approved
	})
	r := NewRenderer(cat)
	plan := botplan.MessagePlan{
		Text: botplan.RichText("x"),
		Prompt: &botplan.ActionPrompt{Choices: []botplan.Choice{
			{Label: "Yes", Token: "y"},
			{Label: "No", Token: "n"}, // one too many
		}},
		Proactive: &botplan.ProactiveSpec{Purpose: "notice", Locale: "en"},
	}
	_, err := r.Render(plan, waTarget(false))
	if !errors.Is(err, botplan.ErrTemplateMismatch) {
		t.Errorf("want ErrTemplateMismatch, got %v", err)
	}
}

// TestFirstPageWithMoreClampsLastPage covers the end-of-slice clamp in
// firstPageWithMore via a page count that does not divide evenly (14 choices:
// page 0 shows 9 + More; page 1 would show the remaining 5 + More).
func TestFirstPageWithMoreLastPageClamp(t *testing.T) {
	cs := choices(14)
	page, note := firstPageWithMore(cs, 1) // second page
	// perPage = 9; page 1 covers indices 9..13 (5 real) + More = 6 rows.
	if len(page) != 6 {
		t.Fatalf("want 6 rows on the clamped last page, got %d", len(page))
	}
	if page[len(page)-1].Label != MoreChoiceLabel {
		t.Error("last row should be More…")
	}
	if !strings.Contains(string(note), "paged") {
		t.Errorf("note should mention paging: %q", note)
	}
}
