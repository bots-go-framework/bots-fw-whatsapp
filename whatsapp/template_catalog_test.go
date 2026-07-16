package whatsapp

import (
	"context"
	"testing"

	"github.com/dal-go/dalgo/adapters/dalgo2memory"
)

func testTemplateCatalog(t *testing.T, newCatalog func() TemplateCatalog) {
	t.Helper()
	ctx := context.Background()

	t.Run("Upsert_and_Get_exact_locale", func(t *testing.T) {
		cat := newCatalog()
		def := TemplateDef{
			Name:   "payment_reminder_en_us",
			Locale: "en_US",
			Status: TemplateStatusApproved,
		}
		if err := cat.Upsert(ctx, "payment_reminder", def); err != nil {
			t.Fatalf("Upsert error: %v", err)
		}
		got, found, err := cat.Get(ctx, "payment_reminder", "en_US")
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if !found {
			t.Fatal("Get: expected found=true, got false")
		}
		if got.Name != def.Name {
			t.Fatalf("Get: expected Name=%q, got %q", def.Name, got.Name)
		}
	})

	t.Run("locale_fallback_language_only", func(t *testing.T) {
		cat := newCatalog()
		// Register en_GB but request en_US → should match language prefix "en"
		def := TemplateDef{
			Name:   "reminder_en_gb",
			Locale: "en_GB",
			Status: TemplateStatusApproved,
		}
		if err := cat.Upsert(ctx, "reminder", def); err != nil {
			t.Fatalf("Upsert error: %v", err)
		}
		got, found, err := cat.Get(ctx, "reminder", "en_US")
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if !found {
			t.Fatal("Get: expected found=true via lang fallback, got false")
		}
		if got.Locale != "en_GB" {
			t.Fatalf("Get: expected Locale=en_GB, got %q", got.Locale)
		}
	})

	t.Run("locale_fallback_first_registered", func(t *testing.T) {
		cat := newCatalog()
		// Register ru only; request fr → should fall back to first (ru)
		defRU := TemplateDef{
			Name:   "reminder_ru",
			Locale: "ru",
			Status: TemplateStatusApproved,
		}
		if err := cat.Upsert(ctx, "reminder", defRU); err != nil {
			t.Fatalf("Upsert ru error: %v", err)
		}
		got, found, err := cat.Get(ctx, "reminder", "fr")
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if !found {
			t.Fatal("Get: expected found=true via first-registered fallback, got false")
		}
		if got.Locale != "ru" {
			t.Fatalf("Get: expected Locale=ru, got %q", got.Locale)
		}
	})

	t.Run("only_approved_returned_by_Get", func(t *testing.T) {
		cat := newCatalog()
		pending := TemplateDef{
			Name:   "promo_en",
			Locale: "en",
			Status: TemplateStatusPending,
		}
		if err := cat.Upsert(ctx, "promo", pending); err != nil {
			t.Fatalf("Upsert error: %v", err)
		}
		_, found, err := cat.Get(ctx, "promo", "en")
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if found {
			t.Fatal("Get: expected found=false for non-approved template, got true")
		}
	})

	t.Run("missing_purpose_returns_not_found", func(t *testing.T) {
		cat := newCatalog()
		_, found, err := cat.Get(ctx, "nonexistent", "en")
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if found {
			t.Fatal("Get: expected found=false for missing purpose, got true")
		}
	})

	t.Run("Upsert_then_SetStatus_then_Get_not_found", func(t *testing.T) {
		cat := newCatalog()
		def := TemplateDef{
			Name:   "notice_en",
			Locale: "en",
			Status: TemplateStatusApproved,
		}
		if err := cat.Upsert(ctx, "notice", def); err != nil {
			t.Fatalf("Upsert error: %v", err)
		}
		// Confirm it's findable before status change.
		_, found, err := cat.Get(ctx, "notice", "en")
		if err != nil {
			t.Fatalf("Get (before SetStatus) error: %v", err)
		}
		if !found {
			t.Fatal("Get: expected found=true before SetStatus, got false")
		}
		// Pause the template.
		wasFound, err := cat.SetStatus(ctx, "notice_en", TemplateStatusPaused)
		if err != nil {
			t.Fatalf("SetStatus error: %v", err)
		}
		if !wasFound {
			t.Fatal("SetStatus: expected found=true, got false")
		}
		// Should no longer be returned.
		_, found, err = cat.Get(ctx, "notice", "en")
		if err != nil {
			t.Fatalf("Get (after SetStatus) error: %v", err)
		}
		if found {
			t.Fatal("Get: expected found=false after pausing, got true")
		}
	})

	t.Run("SetStatus_unknown_name_returns_not_found", func(t *testing.T) {
		cat := newCatalog()
		found, err := cat.SetStatus(ctx, "does_not_exist", TemplateStatusApproved)
		if err != nil {
			t.Fatalf("SetStatus error: %v", err)
		}
		if found {
			t.Fatal("SetStatus: expected found=false for unknown name, got true")
		}
	})
}

func TestMemoryTemplateCatalog(t *testing.T) {
	testTemplateCatalog(t, func() TemplateCatalog {
		return NewMemoryTemplateCatalog()
	})
}

func TestDalgoTemplateCatalog(t *testing.T) {
	testTemplateCatalog(t, func() TemplateCatalog {
		db := dalgo2memory.NewDB()
		return NewDalgoTemplateCatalog(db)
	})
}
