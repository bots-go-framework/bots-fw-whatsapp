package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/dal-go/dalgo/adapters/dalgo2memory"
)

func TestDalgoChatDataStore(t *testing.T) {
	ctx := context.Background()
	const botID = "bot1"
	const chatID = "16505551234"

	newStore := func() ChatDataStore {
		return NewDalgoChatDataStore(dalgo2memory.NewDB())
	}

	t.Run("GetChatData_returns_zero_value_when_absent", func(t *testing.T) {
		store := newStore()
		data, err := store.GetChatData(ctx, botID, chatID)
		if err != nil {
			t.Fatalf("GetChatData error: %v", err)
		}
		if data == nil {
			t.Fatal("GetChatData: expected non-nil zero-value, got nil")
		}
		if !data.DtLastInbound.IsZero() {
			t.Fatalf("GetChatData: expected zero DtLastInbound, got %v", data.DtLastInbound)
		}
		if len(data.RecentInboundIDs) != 0 {
			t.Fatalf("GetChatData: expected empty RecentInboundIDs, got %v", data.RecentInboundIDs)
		}
	})

	t.Run("SaveChatData_then_GetChatData_round_trips", func(t *testing.T) {
		store := newStore()
		now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
		saved := &WaChatData{
			DtLastInbound:    now,
			RecentInboundIDs: []string{"wamid.AAA", "wamid.BBB"},
		}
		if err := store.SaveChatData(ctx, botID, chatID, saved); err != nil {
			t.Fatalf("SaveChatData error: %v", err)
		}
		got, err := store.GetChatData(ctx, botID, chatID)
		if err != nil {
			t.Fatalf("GetChatData after save error: %v", err)
		}
		if got == nil {
			t.Fatal("GetChatData: expected non-nil after save, got nil")
		}
		if !got.DtLastInbound.Equal(now) {
			t.Fatalf("DtLastInbound: expected %v, got %v", now, got.DtLastInbound)
		}
		if len(got.RecentInboundIDs) != 2 {
			t.Fatalf("RecentInboundIDs length: expected 2, got %d", len(got.RecentInboundIDs))
		}
		if got.RecentInboundIDs[0] != "wamid.AAA" || got.RecentInboundIDs[1] != "wamid.BBB" {
			t.Fatalf("RecentInboundIDs: expected [wamid.AAA wamid.BBB], got %v", got.RecentInboundIDs)
		}
	})
}
