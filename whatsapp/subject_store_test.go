package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/dal-go/dalgo/adapters/dalgo2memory"
)

func testSubjectStore(t *testing.T, newStore func() SubjectStore) {
	t.Helper()
	ctx := context.Background()
	const botID = "bot1"
	const wamid = "wamid.ABC123"
	const subject = "payment_reminder"

	t.Run("PutSubject_then_GetSubject_returns_value", func(t *testing.T) {
		store := newStore()
		expiresAt := time.Now().Add(35 * 24 * time.Hour)
		if err := store.PutSubject(ctx, botID, wamid, subject, expiresAt); err != nil {
			t.Fatalf("PutSubject error: %v", err)
		}
		got, found, err := store.GetSubject(ctx, botID, wamid)
		if err != nil {
			t.Fatalf("GetSubject error: %v", err)
		}
		if !found {
			t.Fatal("GetSubject: expected found=true, got false")
		}
		if got != subject {
			t.Fatalf("GetSubject: expected %q, got %q", subject, got)
		}
	})

	t.Run("expired_record_returns_not_found", func(t *testing.T) {
		store := newStore()
		expiresAt := time.Now().Add(-1 * time.Second) // already expired
		if err := store.PutSubject(ctx, botID, wamid, subject, expiresAt); err != nil {
			t.Fatalf("PutSubject error: %v", err)
		}
		_, found, err := store.GetSubject(ctx, botID, wamid)
		if err != nil {
			t.Fatalf("GetSubject error: %v", err)
		}
		if found {
			t.Fatal("GetSubject: expected found=false for expired record, got true")
		}
	})

	t.Run("missing_key_returns_not_found", func(t *testing.T) {
		store := newStore()
		_, found, err := store.GetSubject(ctx, botID, "nonexistent-wamid")
		if err != nil {
			t.Fatalf("GetSubject error: %v", err)
		}
		if found {
			t.Fatal("GetSubject: expected found=false for missing key, got true")
		}
	})
}

func TestMemorySubjectStore(t *testing.T) {
	testSubjectStore(t, func() SubjectStore {
		return NewMemorySubjectStore()
	})
}

func TestDalgoSubjectStore(t *testing.T) {
	testSubjectStore(t, func() SubjectStore {
		db := dalgo2memory.NewDB()
		return NewDalgoSubjectStore(db)
	})
}
