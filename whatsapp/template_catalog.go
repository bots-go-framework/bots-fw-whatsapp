package whatsapp

import (
	"context"
	"strings"
	"sync"
)

// TemplateStatus is the lifecycle status of a WhatsApp template.
type TemplateStatus string

const (
	TemplateStatusApproved TemplateStatus = "approved"
	TemplateStatusPending  TemplateStatus = "pending"
	TemplateStatusRejected TemplateStatus = "rejected"
	TemplateStatusPaused   TemplateStatus = "paused"
	TemplateStatusDisabled TemplateStatus = "disabled"
)

// URLButtonSpec defines the URL button shape of a template.
type URLButtonSpec struct {
	Label   string
	BaseURL string
}

// TemplateDef describes an approved WhatsApp template.
type TemplateDef struct {
	Name         string
	Locale       string // e.g. "en_US" or "en" or "ru"
	Version      string
	Status       TemplateStatus
	BodyParams   []string       // placeholder names, e.g. ["name", "date"]
	QuickReplies []string       // quick-reply button labels, in order
	URLButton    *URLButtonSpec // nil if no URL button
}

// SyncSource is a hook for future Business-Management-API synchronisation.
// An implementation should return the full current template list; the catalog
// calls it during a sync pass. The API client (bots-api-whatsapp) is built
// in a parallel task; this interface decouples the catalog from it.
type SyncSource interface {
	ListTemplates(ctx context.Context) ([]TemplateDef, error)
}

// TemplateCatalog is the registry of approved WhatsApp templates.
type TemplateCatalog interface {
	// Get returns the best available approved template for a purpose key with
	// locale fallback: exact locale → language-only (first part of locale tag)
	// → default locale (first registered for purpose). Returns found=false if
	// no approved template exists for purpose.
	Get(ctx context.Context, purpose, locale string) (TemplateDef, bool, error)

	// Upsert adds or replaces a template definition.
	Upsert(ctx context.Context, purpose string, def TemplateDef) error

	// SetStatus updates the status of a named template. Returns false if no
	// template with that name is registered.
	SetStatus(ctx context.Context, name string, status TemplateStatus) (found bool, err error)
}

// langCode returns the language portion of a locale tag (e.g. "en" from "en_US").
func langCode(locale string) string {
	if idx := strings.IndexByte(locale, '_'); idx >= 0 {
		return locale[:idx]
	}
	return locale
}

// pickApproved applies locale fallback and returns the best approved TemplateDef
// from defs. Order: exact locale → language prefix → first approved.
func pickApproved(defs []TemplateDef, locale string) (TemplateDef, bool) {
	lang := langCode(locale)
	var langMatch *TemplateDef
	var firstApproved *TemplateDef
	for i := range defs {
		d := &defs[i]
		if d.Status != TemplateStatusApproved {
			continue
		}
		if firstApproved == nil {
			firstApproved = d
		}
		if d.Locale == locale {
			return *d, true
		}
		if langMatch == nil && langCode(d.Locale) == lang {
			langMatch = d
		}
	}
	if langMatch != nil {
		return *langMatch, true
	}
	if firstApproved != nil {
		return *firstApproved, true
	}
	return TemplateDef{}, false
}

// ---- In-memory implementation ----

type memoryTemplateCatalog struct {
	mu sync.RWMutex
	// byPurpose maps purpose → list of TemplateDefs (all locales/versions)
	byPurpose map[string][]TemplateDef
	// byName maps template name → purpose (for SetStatus)
	byName map[string]string
}

// NewMemoryTemplateCatalog returns a thread-safe in-memory TemplateCatalog.
func NewMemoryTemplateCatalog() TemplateCatalog {
	return &memoryTemplateCatalog{
		byPurpose: make(map[string][]TemplateDef),
		byName:    make(map[string]string),
	}
}

// Get implements TemplateCatalog.
func (c *memoryTemplateCatalog) Get(_ context.Context, purpose, locale string) (TemplateDef, bool, error) {
	c.mu.RLock()
	defs := c.byPurpose[purpose]
	c.mu.RUnlock()
	def, found := pickApproved(defs, locale)
	return def, found, nil
}

// Upsert implements TemplateCatalog.
func (c *memoryTemplateCatalog) Upsert(_ context.Context, purpose string, def TemplateDef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defs := c.byPurpose[purpose]
	replaced := false
	for i, d := range defs {
		if d.Name == def.Name && d.Locale == def.Locale {
			defs[i] = def
			replaced = true
			break
		}
	}
	if !replaced {
		defs = append(defs, def)
	}
	c.byPurpose[purpose] = defs
	c.byName[def.Name] = purpose
	return nil
}

// SetStatus implements TemplateCatalog.
func (c *memoryTemplateCatalog) SetStatus(_ context.Context, name string, status TemplateStatus) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	purpose, ok := c.byName[name]
	if !ok {
		return false, nil
	}
	defs := c.byPurpose[purpose]
	for i := range defs {
		if defs[i].Name == name {
			defs[i].Status = status
			c.byPurpose[purpose] = defs
			return true, nil
		}
	}
	return false, nil
}
